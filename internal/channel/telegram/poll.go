package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// allowedUpdates is the explicit opt-in list for getUpdates. Telegram's
// allowed_updates is a RE-LISTING contract: ONLY the listed types are delivered,
// and an omitted type is silently dropped server-side. P4 adds "poll" so the
// aggregate poll-result update is delivered (the prior build omitted it — the
// exact mechanism that made poll-reading dead). It is added IN LOCKSTEP with the
// dispatchUpdate case below; never subscribe a type without its handler, or
// updates buffer only to be dropped at the default case.
//
// Note: we deliberately do NOT list "poll_answer". Per Q-RESULT-1 the agent
// receives AGGREGATE tallies only (final-on-close + stop_poll), never per-voter
// identities, so subscribing per-voter answers would only invite spam we drop.
// callback_query + message_reaction were already subscribed; P4 gives them real
// dispatch handling.
var allowedUpdates = []string{"message", "edited_message", "callback_query", "message_reaction", "poll"}

// pollLoop runs Bot.GetUpdates in a loop, converting each Message into an
// Inbound and emitting via host.Emit. Honors c.ctx cancellation.
//
// Error handling (per prior-art parity, see DEBUGGING.md):
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
		longPoll      = longPollTimeoutSeconds
		maxRetryAfter = 60 * time.Second
		trippedSleep  = 5 * time.Minute
		baseBackoff   = time.Second
		maxBackoff    = 30 * time.Second
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
	case u.Poll != nil:
		c.dispatchPollUpdate(u.UpdateId, u.Poll)
	case u.CallbackQuery != nil:
		c.dispatchCallback(u.UpdateId, u.CallbackQuery)
	case u.MessageReaction != nil:
		c.dispatchReaction(u.UpdateId, u.MessageReaction)
	default:
		// Any other subscribed-but-unhandled type. Should not occur given the
		// allowedUpdates list, but log metadata so a future subscription that
		// forgets its handler is visible rather than silently dropped.
		c.host.Logf("telegram: drop update=%d (subscribed type with no dispatch handler)", u.UpdateId)
	}
}

// dispatchPollUpdate converts an aggregate `poll` update into a poll_result
// InboundEvent and emits it via the inbound gate. Q-RESULT-1 = AGGREGATE +
// FINAL-ON-CLOSE: we surface the tally on close (IsClosed) — the firm
// requirement — and refresh the retained latest tally either way. We never
// surface per-voter identity (no poll_answer subscription).
//
// CB-2: the aggregate `poll` update carries NO chat and NO user. We recover the
// originating route + owner from the sentPolls map (populated on sendPoll) and
// stamp Sender.UserID with the route owner so the inbound passes IsUserAllowed
// in a DM. A poll id we never sent (unknown) is dropped with a metadata-only log
// — it is not ours to route.
func (c *Channel) dispatchPollUpdate(updateID int64, poll *gotgbot.Poll) {
	if poll == nil || poll.Id == "" {
		return
	}
	sp, ok := c.sentPolls.Get(poll.Id)
	if !ok {
		// Not a poll we sent (or evicted from the bounded map). Nothing to route.
		c.host.Logf("telegram: drop poll update=%d poll-unknown (not in sentPolls / evicted)", updateID)
		return
	}

	tally := pollTallyFromGotgbot(poll)
	// Refresh the retained latest tally under the map lock — the worker goroutine
	// (StopPoll) can mutate the same *sentPoll concurrently, so a Get→mutate→Put
	// read-modify-write would race. UpdateTally also bumps LRU recency.
	c.sentPolls.UpdateTally(poll.Id, tally)

	// Q-RESULT-1: the firm requirement is FINAL-ON-CLOSE (+ stop_poll). An
	// open-poll aggregate update is retained above for on-demand reads but NOT
	// surfaced to the agent (avoids per-vote spam). Only emit on close.
	if !poll.IsClosed {
		c.host.Logf("telegram: poll update=%d poll=%s open (tally retained, not surfaced — final-on-close)", updateID, poll.Id)
		return
	}

	// Late-event-after-worker-idle (v1 accepted limit): emitEvent → host.Emit →
	// Workers.Submit. If the route's worker has already idle-exited, Submit
	// returns false and host.Emit logs a metadata-only "emit DROP" line (see
	// internal/broker/host.go). For v1 a late aggregate tally on a long-closed
	// poll may thus be dropped — that is acceptable and visible in the log;
	// stop_poll is the deterministic read path that never depends on a live
	// worker. We do NOT silently lose it (the DROP log is the audit trail).
	in := pollResultInbound(c.Name(), sp, poll.Id, tally)
	c.emitEvent(updateID, in, "poll_result")
}

// dispatchCallback auto-acks an inline-keyboard callback (Q-RESULT-2: no
// "loading…" spinner, no agent-in-the-loop for the ack) and THEN surfaces it as
// a callback InboundEvent. P7 wires outbound keyboards that make these
// actionable; P4 only routes the inbound press.
//
// Pass-1 C4: CallbackQuery.Message is a MaybeInaccessibleMessage (interface),
// not *Message. We type-assert to the accessible *gotgbot.Message for the chat /
// thread coordinates; the inaccessible variant (the original message was deleted
// or is too old) can't be routed to a chat, so we drop it with a metadata-only
// log after still auto-acking (the user's button must not spin either way).
func (c *Channel) dispatchCallback(updateID int64, cq *gotgbot.CallbackQuery) {
	if cq == nil {
		return
	}
	// Auto-ack immediately, regardless of whether we can route the event. A
	// failed ack is logged but not fatal — the surfacing still proceeds.
	if c.bot != nil {
		if _, err := c.bot.AnswerCallbackQuery(cq.Id, &gotgbot.AnswerCallbackQueryOpts{}); err != nil {
			c.host.Logf("telegram: callback update=%d answerCallbackQuery failed: %v", updateID, err)
		}
	}

	msg, ok := cq.Message.(*gotgbot.Message)
	if !ok || msg == nil {
		// Inaccessible message (deleted / too old) — no chat to route to.
		c.host.Logf("telegram: drop callback update=%d (message inaccessible; auto-acked, cannot route)", updateID)
		return
	}

	in := &c3types.Inbound{
		Channel:   c.Name(),
		ChatID:    msg.Chat.Id,
		TopicID:   topicPtrFromThread(msg.MessageThreadId),
		MessageID: msg.MessageId,
		Sender:    convertSender(&cq.From),
		Timestamp: time.Now(),
		Kind:      c3types.InboundCallback,
		Event: &c3types.InboundEvent{
			Callback: &c3types.CallbackEvent{
				CallbackID: cq.Id,
				MessageID:  msg.MessageId,
				Actor:      convertSender(&cq.From),
				Data:       cq.Data,
			},
		},
	}
	c.emitEvent(updateID, in, "callback")
}

// dispatchReaction converts a message_reaction update into a reaction
// InboundEvent. Pass-1 C3: OldReaction/NewReaction are []ReactionType (an
// interface over emoji / custom-emoji / paid). Added/Removed are the set-diff of
// new vs old, rendered via reactionTypeString — standard emoji verbatim, custom/
// paid as the "[custom]"/"[paid]" sentinel (never silently dropped).
func (c *Channel) dispatchReaction(updateID int64, mr *gotgbot.MessageReactionUpdated) {
	if mr == nil {
		return
	}
	added, removed := diffReactions(mr.OldReaction, mr.NewReaction)
	if len(added) == 0 && len(removed) == 0 {
		// No net change (e.g. only custom/paid churn that diffed to nothing) —
		// nothing meaningful to surface.
		c.host.Logf("telegram: reaction update=%d no net change (skipped)", updateID)
		return
	}
	var actor c3types.Sender
	if mr.User != nil {
		actor = convertSender(mr.User)
	}
	in := &c3types.Inbound{
		Channel:   c.Name(),
		ChatID:    mr.Chat.Id,
		MessageID: mr.MessageId,
		Sender:    actor,
		Timestamp: time.Now(),
		Kind:      c3types.InboundReaction,
		Event: &c3types.InboundEvent{
			Reaction: &c3types.ReactionEvent{
				MessageID: mr.MessageId,
				Actor:     actor,
				Added:     added,
				Removed:   removed,
			},
		},
	}
	c.emitEvent(updateID, in, "reaction")
}

// emitEvent runs a synthesized event Inbound through the inbound gate and emits
// it. CB-3: replicates the metadata-only GATE-drop logging contract — no content
// is ever logged, strangers see nothing. Mirrors dispatchMessage's gate handling
// without the message-only STT/kind plumbing.
func (c *Channel) emitEvent(updateID int64, in *c3types.Inbound, kind string) {
	switch c.host.GateInbound(in) {
	case channel.GateInboundDrop:
		c.host.Logf("telegram: GATE drop update=%d kind=%s chat=%d msg=%d sender=%d",
			updateID, kind, in.ChatID, in.MessageID, in.Sender.UserID)
		return
	case channel.GateInboundPairConsumed:
		// An event body never carries a pairing code; this branch is here for
		// completeness/parity with the message path.
		c.host.Logf("telegram: GATE pair-consumed update=%d kind=%s chat=%d (unexpected for an event)",
			updateID, kind, in.ChatID)
		return
	}
	c.host.Logf("telegram: inbound update=%d kind=%s chat=%d msg=%d", updateID, kind, in.ChatID, in.MessageID)
	c.host.Emit(in)
}

// pollTallyFromGotgbot converts a gotgbot.Poll into the in-package aggregate
// snapshot.
func pollTallyFromGotgbot(poll *gotgbot.Poll) *pollTally {
	opts := make([]pollOptionTally, 0, len(poll.Options))
	for _, o := range poll.Options {
		opts = append(opts, pollOptionTally{Text: o.Text, VoterCount: int(o.VoterCount)})
	}
	return &pollTally{
		Question:    poll.Question,
		TotalVoters: int(poll.TotalVoterCount),
		IsClosed:    poll.IsClosed,
		Options:     opts,
	}
}

// pollResultInbound builds the channel-neutral poll_result Inbound for a tally,
// stamping the stored route + owner (CB-2). Shared by the passive poll-update
// path and stop_poll.
func pollResultInbound(channelName string, sp *sentPoll, pollID string, tally *pollTally) *c3types.Inbound {
	opts := make([]c3types.PollOptionTally, 0, len(tally.Options))
	for _, o := range tally.Options {
		opts = append(opts, c3types.PollOptionTally{Text: o.Text, VoterCount: o.VoterCount})
	}
	return &c3types.Inbound{
		Channel:   channelName,
		ChatID:    sp.ChatID,
		TopicID:   sp.TopicID,
		MessageID: sp.MessageID,
		Sender:    c3types.Sender{UserID: sp.OwnerUserID}, // CB-2: route-owner stamp
		Timestamp: time.Now(),
		Kind:      c3types.InboundPollResult,
		Event: &c3types.InboundEvent{
			PollResult: &c3types.PollResult{
				PollID:      pollID,
				Question:    tally.Question,
				TotalVoters: tally.TotalVoters,
				IsClosed:    tally.IsClosed,
				Options:     opts,
			},
		},
	}
}

// diffReactions computes the added/removed display-string sets between an old
// and new reaction list. Each ReactionType is rendered by reactionTypeString.
func diffReactions(old, current []gotgbot.ReactionType) (added, removed []string) {
	oldSet := reactionStringSet(old)
	newSet := reactionStringSet(current)
	for s := range newSet {
		if !oldSet[s] {
			added = append(added, s)
		}
	}
	for s := range oldSet {
		if !newSet[s] {
			removed = append(removed, s)
		}
	}
	return added, removed
}

// reactionStringSet renders a reaction list into a set of display strings.
func reactionStringSet(rs []gotgbot.ReactionType) map[string]bool {
	set := make(map[string]bool, len(rs))
	for _, r := range rs {
		if s := reactionTypeString(r); s != "" {
			set[s] = true
		}
	}
	return set
}

// reactionTypeString renders a single ReactionType into a display string.
// Pass-1 C3: standard emoji verbatim; custom/paid reactions become the sentinels
// "[custom]"/"[paid]" so the agent sees that SOMETHING reacted rather than the
// reaction being silently dropped. An unknown future type falls back to its
// GetType() tag wrapped in brackets.
func reactionTypeString(r gotgbot.ReactionType) string {
	switch v := r.(type) {
	case gotgbot.ReactionTypeEmoji:
		return v.Emoji
	case gotgbot.ReactionTypeCustomEmoji:
		return "[custom]"
	case gotgbot.ReactionTypePaid:
		return "[paid]"
	default:
		if r == nil {
			return ""
		}
		return "[" + r.GetType() + "]"
	}
}

// topicPtrFromThread converts a gotgbot message_thread_id (0 = none) into the
// *int64 TopicID convention (nil = no topic).
func topicPtrFromThread(threadID int64) *int64 {
	if threadID == 0 {
		return nil
	}
	t := threadID
	return &t
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
