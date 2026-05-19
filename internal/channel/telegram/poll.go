package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/channel"
)

// allowedUpdates is the conservative default opt-in. message_reaction is
// included so plugins (future) can hook reactions. forum-related updates
// arrive as fields on Message, not as separate update types — no opt-in
// needed there.
var allowedUpdates = []string{"message", "edited_message", "callback_query", "message_reaction"}

// pollLoop runs Bot.GetUpdates in a loop, converting each Message into an
// Inbound and emitting via host.Emit. Honors c.ctx cancellation.
//
// Error handling (per OpenClaw parity, see DEBUGGING.md):
//   - 409 Conflict: another poller holds this token. Log loud and EXIT —
//     retrying would just thrash; the human needs to kill the other poller.
//   - 401/403 Permanent: increment authBreaker. After auth401Threshold
//     consecutive 401s, the breaker trips: sleep long (5min) between
//     attempts so a revoked-token retry-storm can't get the bot deleted.
//   - 429 Rate-limited: honor parameters.retry_after (capped at 60s) before
//     the next attempt. Don't compound onto the backoff timer.
//   - Transient (network/timeout/5xx): exponential backoff 1s → 30s, reset
//     on success.
//   - Any non-classified: treated as transient (better to retry than crash).
//
// Long-poll budget is sized via timeoutFor("getUpdates", ...) — see
// resilience.go. Don't shorten it past the server-side Timeout or we'll
// re-introduce the 2026-05-09 bug.
func (c *Channel) pollLoop() {
	const (
		longPoll          = longPollTimeoutSeconds
		maxRetryAfter     = 60 * time.Second
		trippedSleep      = 5 * time.Minute
		baseBackoff       = time.Second
		maxBackoff        = 30 * time.Second
	)

	var offset int64
	if c.offsets != nil {
		if loaded, err := c.offsets.Load(); err == nil && loaded > 0 {
			offset = loaded + 1
			c.host.Logf("telegram: resuming from persisted offset=%d (next=%d)", loaded, offset)
		} else if err != nil {
			c.host.Logf("telegram: offset Load failed (%v); starting from 0", err)
		}
	}
	backoff := baseBackoff

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// If the auth breaker is tripped, sleep long and retry sparingly.
		// Eventually the user fixes the token; one success clears the breaker.
		if c.authBrk.IsTripped() {
			c.host.Logf("telegram: auth breaker TRIPPED (%d consecutive 401s); sleeping %v before next probe — fix bot_token in mappings.json",
				c.authBrk.Consec(), trippedSleep)
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(trippedSleep):
			}
		}

		opts := &gotgbot.GetUpdatesOpts{
			Offset:         offset,
			Timeout:        longPoll,
			AllowedUpdates: allowedUpdates,
			RequestOpts:    requestOptsFor("getUpdates", longPoll),
		}
		updates, err := c.bot.GetUpdates(opts)
		// Record return time for stallWatchdog regardless of err vs success.
		c.lastPollReturn.Store(time.Now().UnixNano())
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			class, retryAfter := classifyError(err)
			switch class {
			case errClassConflict:
				// 409 — another poller has this token. Stop. The other process
				// (often the Python POC, sometimes a leaked broker) needs to
				// be killed before we can safely poll again. Logging loud so
				// the user sees it in DEBUGGING.md flow.
				c.host.Logf("telegram: 409 CONFLICT — another poller holds this bot token; pollLoop exiting. Kill the other process and restart c3-broker. Error: %v", err)
				return
			case errClassPermanent:
				// 401/403 — token issue. Trip-on-N pattern.
				tripped := c.authBrk.RecordFail()
				c.host.Logf("telegram: GetUpdates permanent error (consec=%d, tripped=%v): %v",
					c.authBrk.Consec(), tripped, err)
				wait := backoff
				if tripped {
					wait = trippedSleep
				}
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(wait):
				}
				if !tripped {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
				continue
			case errClassRateLimited:
				wait := time.Duration(retryAfter) * time.Second
				if wait <= 0 {
					wait = 5 * time.Second
				}
				if wait > maxRetryAfter {
					wait = maxRetryAfter
				}
				c.host.Logf("telegram: GetUpdates 429 rate-limited; honoring retry_after=%ds (capped at %v)",
					retryAfter, maxRetryAfter)
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(wait):
				}
				continue
			case errClassTransient:
				c.host.Logf("telegram: GetUpdates transient error (backoff %v): %v", backoff, err)
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			default:
				c.host.Logf("telegram: GetUpdates unclassified error (treating as transient, backoff %v): %v", backoff, err)
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(backoff):
				}
				continue
			}
		}
		// Success — clear auth breaker and reset transient backoff.
		c.authBrk.RecordSuccess()
		backoff = baseBackoff

		var advanced bool
		for _, u := range updates {
			if c.dedup != nil && c.dedup.SeenOrAdd(&u) {
				c.host.Logf("telegram: dedup skip update=%d (recent duplicate)", u.UpdateId)
				if u.UpdateId >= offset {
					offset = u.UpdateId + 1
					advanced = true
				}
				continue
			}
			c.dispatchUpdate(&u)
			if u.UpdateId >= offset {
				offset = u.UpdateId + 1
				advanced = true
			}
		}
		if advanced && c.offsets != nil {
			// Best-effort: log the failure but don't stop polling.
			if err := c.offsets.Save(offset - 1); err != nil {
				c.host.Logf("telegram: offset Save failed: %v", err)
			}
		}
	}
}

// dispatchUpdate converts a single Update into an Inbound (or several) and
// emits to the host. EditedMessage is treated like a fresh Message for v1 —
// edits flow as new inbound. Plugins (future) can dedupe if needed.
func (c *Channel) dispatchUpdate(u *gotgbot.Update) {
	switch {
	case u.Message != nil:
		c.dispatchMessage(u.UpdateId, u.Message, false)
	case u.EditedMessage != nil:
		c.dispatchMessage(u.UpdateId, u.EditedMessage, true)
	default:
		// Other update types (callback_query, message_reaction) are accepted
		// in allowed_updates but not yet surfaced. Plan 8+ adds reaction
		// hooks for plugins.
	}
}

// dispatchMessage converts and emits one message. Logs metadata only —
// see DEBUGGING.md for the content policy.
//
// Default-deny gate (TODO #1, locked 2026-05-18): every inbound runs
// through host.GateInbound BEFORE host.Emit. Non-allowlisted senders
// see their messages silently dropped (no broker worker invoked, no
// log of the body — strangers see a dead bot). The only exception is
// during an active pairing window, where a body matching the 4-digit
// code is consumed by the broker (allowlist updated + persisted) and
// the message itself is not forwarded.
func (c *Channel) dispatchMessage(updateID int64, msg *gotgbot.Message, edited bool) {
	in := convertInbound(c.Name(), msg, c.cfg.STTPrefix)
	if in == nil {
		c.host.Logf("telegram: skip update=%d msg=%d chat=%d thread=%d (unsupported service)",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId)
		return
	}
	kind := "text"
	if len(in.Attachments) > 0 && in.Attachments[0].Kind != "" {
		kind = in.Attachments[0].Kind
	}
	switch c.host.GateInbound(in) {
	case channel.GateInboundDrop:
		// Silent drop. Do NOT log content; metadata only — surface enough
		// to debug a misconfigured allowlist via broker.log without
		// echoing message text or sender handle. See default-deny posture
		// in TODO #1.
		c.host.Logf("telegram: GATE drop update=%d msg=%d chat=%d thread=%d sender=%d kind=%s",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, in.Sender.UserID, kind)
		return
	case channel.GateInboundPairConsumed:
		// Body matched an active pairing code; allowlist already updated
		// by the broker. Message itself is a control-plane signal — do
		// NOT forward as inbound content.
		c.host.Logf("telegram: GATE pair-consumed update=%d msg=%d chat=%d thread=%d sender=%d (allowlist updated)",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, in.Sender.UserID)
		return
	}
	c.host.Logf("telegram: inbound update=%d msg=%d chat=%d thread=%d kind=%s edited=%v",
		updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, kind, edited)
	c.host.Emit(in)
}
