package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// updateProbe captures ONLY the rich_message raw JSON that gotgbot rc.34 drops
// during typed unmarshal (it has no RichMessage field). We unmarshal the raw
// getUpdates response a second time into this minimal shape and pair it with the
// typed updates by array index.
type updateProbe struct {
	Message       *messageProbe `json:"message"`
	EditedMessage *messageProbe `json:"edited_message"`
}

type messageProbe struct {
	RichMessage json.RawMessage `json:"rich_message"`
}

// parseUpdates unmarshals a raw getUpdates result array into BOTH the typed
// gotgbot updates (the existing downstream path, byte-identical to what
// Bot.GetUpdates produced) and the rich-message probes (same array, same order).
// Pure — unit-tested without network.
func parseUpdates(raw []byte) ([]gotgbot.Update, []updateProbe, error) {
	var updates []gotgbot.Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, nil, err
	}
	var probes []updateProbe
	// Best-effort: if the probe unmarshal somehow fails, proceed with no rich
	// data rather than dropping the whole batch.
	_ = json.Unmarshal(raw, &probes)
	return updates, probes, nil
}

// richRawFor returns the rich_message raw JSON for an update's message (or edited
// message), or nil if absent.
func richRawFor(p updateProbe) json.RawMessage {
	if p.Message != nil && len(p.Message.RichMessage) > 0 {
		return p.Message.RichMessage
	}
	if p.EditedMessage != nil && len(p.EditedMessage.RichMessage) > 0 {
		return p.EditedMessage.RichMessage
	}
	return nil
}

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
//   - 409 Conflict: another getUpdates is active for this token. Drive
//     fetch-health (so a PERSISTENT conflict still raises the out-of-band DOWN
//     alert), back off with an escalating delay, and RETRY — do NOT exit. A 409
//     is usually transient (a client-side long-poll timeout racing the server's
//     still-open prior poll) and self-heals on the next success; the broker
//     singleton already prevents a second LOCAL broker. Exiting would turn a
//     few-second blip into a dead bot needing a manual kill+restart.
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
		longPoll            = longPollTimeoutSeconds
		maxRetryAfter       = 60 * time.Second
		trippedSleep        = 5 * time.Minute
		baseBackoff         = time.Second
		maxBackoff          = 30 * time.Second
		offsetSaveFailAlert = 5 // consecutive Save failures before one loud advisory
	)

	// consecTransient counts consecutive TRANSIENT GetUpdates failures (the
	// IP-block signature). When it reaches endpointFailoverThreshold AND more
	// than one endpoint is configured, we advance activeEndpoint to the next
	// base and reset the counter (P2 endpoint failover). It is reset on the
	// first success and on any non-transient outcome — failover is for the
	// "this base is unreachable" case ONLY, never for 401/403 (follow you
	// everywhere) or 409 (conflict). This composes with the Phase-1 fetch-health
	// machine: the health edge still fires; failover just also rotates the base.
	var consecTransient int

	// consecConflict counts consecutive 409 conflicts (diagnostic, surfaced in
	// the log so a one-off transient 409 is distinguishable from a persistent
	// second poller). Reset on the first success and on any non-conflict outcome.
	var consecConflict int

	// consecSaveFail counts consecutive offset-persistence failures so a
	// sustained failure escalates from a quiet per-line log to ONE loud advisory
	// (at offsetSaveFailAlert): a silently-failing Save re-floods the CLI with
	// re-delivered updates on the next restart, which dedup can't cover for long.
	var consecSaveFail int

	// Effective 409-conflict backoff bounds. Zero fields ⇒ the production
	// defaults; tests inject millisecond values to exercise the retry path fast.
	conflictBase := c.conflictBackoffBase
	if conflictBase <= 0 {
		conflictBase = defaultConflictBackoffBase
	}
	conflictMax := c.conflictBackoffMax
	if conflictMax <= 0 {
		conflictMax = defaultConflictBackoffMax
	}
	conflictBackoff := conflictBase

	// Offset resumes from the PERSISTED store. A supervised panic-restart
	// re-enters here and re-Loads, so it resumes from the last SAVED offset, not
	// in-memory progress — if Save was failing (see the offset-save advisory
	// below) a restart can re-deliver recent updates; the dedup map bounds that.
	//
	// lastSaved tracks the highest offset we have written to the store, so we
	// only Save on a real advance. With the persisted-offset tracker (offTrk,
	// set in Start), the tracker is the source of truth across a supervised
	// panic-restart (it lives on the Channel, not re-seeded here), so we resume
	// from its committed prefix; the next fetch is Committed()+1.
	var offset int64
	var lastSaved int64
	if c.offTrk != nil {
		lastSaved = c.offTrk.Committed()
		offset = lastSaved + 1
		if lastSaved > 0 {
			c.host.Logf("telegram: resuming from persisted-offset tracker committed=%d (next=%d)", lastSaved, offset)
		}
	} else if c.offsets != nil {
		if loaded, err := c.offsets.Load(); err == nil && loaded > 0 {
			offset = loaded + 1
			lastSaved = loaded
			c.host.Logf("telegram: resuming from persisted offset=%d (next=%d)", loaded, offset)
		} else if err != nil {
			c.host.Logf("telegram: offset Load failed (%v); starting from 0", err)
		}
	}
	backoff := baseBackoff

	// Component 2: persist the offset "periodically AND on shutdown". The loop
	// already saves per successful batch (the periodic half); this defer covers the
	// shutdown half — on ANY loop exit (ctx cancelled / return) write the highest
	// durably-committed offset so a final batch persisted just before shutdown is
	// not lost to a restart (which would re-deliver it; dedup bounds it but the
	// save avoids the churn). It is a no-op when nothing advanced since the last
	// Save (saveTo <= lastSaved) or when there is no offset store, so it never
	// double-writes the steady-state per-batch save.
	defer func() {
		if c.offTrk == nil || c.offsets == nil {
			return
		}
		if saveTo := c.offTrk.Committed(); saveTo > lastSaved {
			if err := c.offsets.Save(saveTo); err != nil {
				c.host.Logf("telegram: final offset Save on shutdown failed: %v", err)
			} else {
				c.host.Logf("telegram: persisted offset=%d on shutdown", saveTo)
			}
		}
	}()

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

		params := map[string]any{
			"offset":          offset,
			"timeout":         longPoll,
			"allowed_updates": allowedUpdates,
		}
		raw, err := c.bot.RequestWithContext(c.ctx, "getUpdates", params, c.requestOptsFor("getUpdates"))
		var updates []gotgbot.Update
		var probes []updateProbe
		if err == nil {
			updates, probes, err = parseUpdates(raw)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// Shutdown. We intentionally do NOT run the conflict-clear below:
				// the channel is stopping and conflictActive is per-Channel state
				// that dies with the goroutines, so a stale true is harmless here.
				return
			}
			class, retryAfter := classifyError(err)
			if class != errClassConflict {
				// A non-conflict outcome breaks any consecutive-409 streak: reset
				// the conflict counter/backoff and clear conflictActive so a 409 is
				// no longer treated as the current inbound blocker (the success path
				// clears these too). This is also what keeps the consecConflict
				// comment honest ("reset on any non-conflict outcome").
				consecConflict = 0
				conflictBackoff = conflictBase
				c.conflictActive.Store(false)
			}
			switch class {
			case errClassConflict:
				// 409 — Telegram reports another getUpdates is active for this
				// token. This is USUALLY transient and self-healing, NOT a reason
				// to give up: after a client-side long-poll timeout (flaky
				// network/proxy), our next getUpdates races the server's
				// still-open prior long-poll and draws a 409 that clears within
				// seconds. The broker singleton (flock + listen socket) already
				// prevents a second LOCAL broker, so the old behavior — exit
				// pollLoop and demand a manual kill+restart — turned a few-second
				// blip into a dead bot only a human could revive (the 2026-06-22
				// fresh-laptop incident). Instead: drive fetch-health (so a
				// PERSISTENT conflict still raises the out-of-band DOWN alert once
				// consecFails crosses downAfterFails), back off with an escalating
				// delay, and RETRY. The first success auto-recovers and resets —
				// no restart, no debugging. A genuine second poller elsewhere just
				// keeps us in slow-retry + DOWN-alerting until it goes away, at
				// which point the next poll succeeds on its own.
				//
				// A 409 follows the token to every endpoint (like 401/403), so it
				// never triggers endpoint failover — reset the transient streak.
				consecTransient = 0
				consecConflict++
				c.conflictActive.Store(true)
				c.reportHealth(c.health.RecordFailure("409 conflict (another getUpdates active)"))
				c.host.Logf("telegram: 409 CONFLICT (consec=%d) — another getUpdates is active for this bot token; backing off %v and retrying. Self-heals once the other poll ends; no kill/restart needed. Error: %v",
					consecConflict, conflictBackoff, err)
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(conflictBackoff):
				}
				conflictBackoff *= 2
				if conflictBackoff > conflictMax {
					conflictBackoff = conflictMax
				}
				continue
			case errClassPermanent:
				// 401/403 — token issue. Trip-on-N pattern. Only a TRIPPED
				// breaker (token revoked / persistently rejected) drives the
				// health machine DOWN — a couple of transient auth blips
				// shouldn't raise the out-of-band alarm.
				// Permanent (401/403) follows you to every endpoint — never a
				// reason to fail over. Reset the transient streak.
				consecTransient = 0
				tripped := c.authBrk.RecordFail()
				c.host.Logf("telegram: GetUpdates permanent error (consec=%d, tripped=%v): %v",
					c.authBrk.Consec(), tripped, err)
				if tripped {
					c.reportHealth(c.health.RecordFailure("token revoked / persistent 401"))
				}
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
				// 429 is a healthy, reachable server pushing back — it is
				// explicitly NOT a fetch-health failure. Do NOT call
				// RecordFailure here (the central false-positive guard:
				// "429 is NOT down"). The endpoint is reachable, so it is not a
				// failover trigger either — reset the transient streak.
				consecTransient = 0
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
				// Transient network/timeout/5xx — the IP-block / unreachable
				// signature. Feed the health machine; it flips DOWN after
				// downAfterFails consecutive transient failures and the edge
				// fans out the out-of-band alert.
				c.host.Logf("telegram: GetUpdates transient error (backoff %v): %v", backoff, err)
				c.reportHealth(c.health.RecordFailure("transient (network/timeout/5xx)"))
				consecTransient++
				c.maybeFailover(&consecTransient)
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
				// Unclassified is treated as transient (see classifyError) —
				// drive the health machine the same way, INCLUDING the failover
				// counter (an unreachable host can surface as an unclassified
				// network error).
				c.host.Logf("telegram: GetUpdates unclassified error (treating as transient, backoff %v): %v", backoff, err)
				c.reportHealth(c.health.RecordFailure("unclassified (treated as transient)"))
				consecTransient++
				c.maybeFailover(&consecTransient)
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(backoff):
				}
				continue
			}
		}
		// Success — clear auth breaker, reset transient backoff, and record a
		// healthy fetch. A GetUpdates that returned without a transport error is
		// HEALTHY even when it carried ZERO updates (the normal quiet-night
		// long-poll timeout) — that's the central false-positive fix: success
		// here, not a failure. The FIRST success after an outage fires the
		// RECOVERED edge.
		c.authBrk.RecordSuccess()
		c.reportHealth(c.health.RecordSuccess())
		backoff = baseBackoff
		// A success after a 409 means the conflict cleared (the racing poll
		// ended / the other instance went away) — reset the conflict backoff and
		// streak so a later, unrelated conflict starts fresh from the base delay,
		// and clear conflictActive so the heartbeat may resume clearing DOWN.
		conflictBackoff = conflictBase
		consecConflict = 0
		c.conflictActive.Store(false)
		// A working endpoint sticks: the first success clears the failover streak
		// so we don't rotate away from a base that just recovered.
		consecTransient = 0

		var advanced bool
		for i := range updates {
			u := updates[i]
			// Register the accepted update as in-flight in the persisted-offset
			// tracker BEFORE dispatch. The committed offset advances past it only
			// once its message is durably persisted (the broker's persist callback
			// MarkDone-s it) or it is a no-op outcome (markUpdateDone below). When
			// offTrk is nil (conflict/resilience unit tests), we fall back to the
			// legacy highest-seen in-memory advance.
			if c.offTrk != nil {
				c.offTrk.Register(u.UpdateId)
			}
			if c.dedup != nil && c.dedup.SeenOrAdd(&u) {
				c.host.Logf("telegram: dedup skip update=%d (recent duplicate)", u.UpdateId)
				// A dedup-skip is NOT a persist. With offTrk, this is either a
				// genuine Telegram redelivery of an already-committed update (id
				// <= committed ⇒ Register/MarkDone are no-ops) OR an in-flight
				// update being re-fetched because the next-fetch offset tracks
				// Committed()+1 — that one must stay in-flight (the persist
				// callback owns its MarkDone), so we do NOT markUpdateDone here.
				// Without offTrk we keep the legacy highest-seen advance.
				if c.offTrk == nil && u.UpdateId >= offset {
					offset = u.UpdateId + 1
					advanced = true
				}
				continue
			}
			var richRaw json.RawMessage
			if i < len(probes) {
				richRaw = richRawFor(probes[i])
			}
			c.dispatchGuarded(&u, richRaw)
			if c.offTrk == nil && u.UpdateId >= offset {
				offset = u.UpdateId + 1
				advanced = true
			}
		}
		// Persist the offset. With the tracker, save only the highest CONTIGUOUS
		// durably-persisted (or no-op) update_id — never past an update whose
		// Append is still in-flight (that is the whole safety property). Without
		// it (unit tests), fall back to the legacy highest-seen advance.
		var saveTo int64
		var doSave bool
		if c.offTrk != nil {
			if cur := c.offTrk.Committed(); cur > lastSaved {
				saveTo, doSave = cur, true
			}
			// The next getUpdates resumes from the contiguous-committed prefix so
			// an in-flight (unpersisted) update is re-fetched after a crash and
			// never silently acked. Steady-state re-fetches are suppressed by the
			// dedup map.
			offset = c.offTrk.Committed() + 1
		} else if advanced {
			saveTo, doSave = offset-1, true
		}
		if doSave && c.offsets != nil {
			// Best-effort: log the failure but don't stop polling.
			if err := c.offsets.Save(saveTo); err != nil {
				consecSaveFail++
				c.host.Logf("telegram: offset Save failed (consec=%d): %v", consecSaveFail, err)
				if consecSaveFail == offsetSaveFailAlert {
					c.host.Logf("telegram: WARNING offset persistence has failed %d times in a row — a broker restart will RE-DELIVER recent updates (dedup can't cover a long gap) until this clears; check the offset store path is writable.", consecSaveFail)
				}
			} else {
				consecSaveFail = 0
				lastSaved = saveTo
			}
		}
	}
}

// dispatchGuarded runs dispatchUpdate under a panic recover. A panic while
// handling ONE update (a malformed payload, a nil deref in a converter, a bug
// in a plugin) must NOT kill polling: we log the panic + stack and return, and
// the caller advances the offset past this update so it is SKIPPED rather than
// re-fetched forever (a poison pill would otherwise wedge inbound permanently,
// or — under the goroutine supervisor — tight-loop on restart). Losing one bad
// update is the correct trade against inbound going dead. A dispatch panic is a
// code bug, not a Telegram-reachability problem, so it does NOT drive
// fetch-health DOWN (that would be a false outage alarm).
func (c *Channel) dispatchGuarded(u *gotgbot.Update, richRaw json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 8192)
			n := runtime.Stack(buf, false)
			c.host.Logf("telegram: dispatch PANIC recovered (update=%d skipped): %v\n%s", u.UpdateId, r, buf[:n])
			// A panicked (poison) update is SKIPPED: mark it done so the
			// persisted offset advances past it rather than re-fetching it
			// forever (the contiguous-prefix tracker would otherwise wedge here).
			c.markUpdateDone(u.UpdateId)
		}
	}()
	c.dispatchUpdate(u, richRaw)
}

// dispatchUpdate converts a single Update into an Inbound (or several) and
// emits to the host. EditedMessage is treated like a fresh Message for v1 —
// edits flow as new inbound. Plugins (future) can dedupe if needed.
func (c *Channel) dispatchUpdate(u *gotgbot.Update, richRaw json.RawMessage) {
	switch {
	case u.Message != nil:
		// The message path owns its own offset marking: markUpdateDone on every
		// no-op outcome (skip/status/gate-drop/pair-consumed) and the
		// msgToUpdate seam → persist-callback MarkDone on the routed path.
		c.dispatchMessage(u.UpdateId, u.Message, false, richRaw)
	case u.EditedMessage != nil:
		c.dispatchMessage(u.UpdateId, u.EditedMessage, true, richRaw)
	case u.Poll != nil:
		// Events (poll_result / callback / reaction) are NEVER persisted; mark
		// the update done unconditionally so the offset advances past it,
		// regardless of whether the inner handler surfaced or dropped it.
		c.dispatchPollUpdate(u.UpdateId, u.Poll)
		c.markUpdateDone(u.UpdateId)
	case u.CallbackQuery != nil:
		c.dispatchCallback(u.UpdateId, u.CallbackQuery)
		c.markUpdateDone(u.UpdateId)
	case u.MessageReaction != nil:
		c.dispatchReaction(u.UpdateId, u.MessageReaction)
		c.markUpdateDone(u.UpdateId)
	default:
		// Any other subscribed-but-unhandled type. Should not occur given the
		// allowedUpdates list, but log metadata so a future subscription that
		// forgets its handler is visible rather than silently dropped. Never
		// persisted — mark done so it does not wedge the contiguous prefix.
		c.host.Logf("telegram: drop update=%d (subscribed type with no dispatch handler)", u.UpdateId)
		c.markUpdateDone(u.UpdateId)
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
	// Synthesized events (poll_result / reaction / callback) are NEVER persisted
	// to the durable queue — they are delivered live or dropped. The offset
	// marking for the event update_id is owned by dispatchUpdate (which calls
	// markUpdateDone after every event-dispatch case, covering early returns too).
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
	// The bool return is intentionally ignored for events: the event update_id's
	// offset marking is owned by dispatchUpdate (it calls markUpdateDone after the
	// event-dispatch case unconditionally), so a queue-full Emit drop of an event
	// does not strand its update — the offset advances regardless. Events are
	// never persisted, so there is no msgToUpdate seam to clean up here.
	_ = c.host.Emit(in)
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

// isStatusCommand reports whether text is the "/status" bot command (optionally
// "/status@<botname>"), case-insensitive, after trimming. It must be an exact
// command token — "/statusly" and "please /status" are NOT matched.
func (c *Channel) isStatusCommand(text string) bool {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '@'); i >= 0 {
		t = t[:i]
	}
	return strings.EqualFold(t, "/status")
}

// markUpdateDone marks an update done in the persisted-offset tracker for every
// NON-persist outcome (gated, dropped, non-message, pair-consumed, dedup-skip,
// /status). Nil-safe so the early-build paths and tests that leave offTrk unset
// (the conflict/resilience suites) are no-ops.
func (c *Channel) markUpdateDone(updateID int64) {
	if c.offTrk != nil {
		c.offTrk.MarkDone(updateID)
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
func (c *Channel) dispatchMessage(updateID int64, msg *gotgbot.Message, edited bool, richRaw json.RawMessage) {
	if !c.cfg.RichInboundEnabled() {
		richRaw = nil // toggle off ⇒ rich messages surface as today (empty)
	}
	in := convertInbound(c.Name(), msg, c.cfg.STTPrefix, richRaw)
	if in == nil {
		c.host.Logf("telegram: skip update=%d msg=%d chat=%d thread=%d (unsupported service)",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId)
		// Unsupported service message — never persisted; unblock the offset.
		c.markUpdateDone(updateID)
		return
	}
	kind := "text"
	if len(in.Attachments) > 0 && in.Attachments[0].Kind != "" {
		kind = in.Attachments[0].Kind
	}
	// I-SEC: the allowlist gate runs FIRST — BEFORE the /status intercept. A
	// non-allowlisted stranger must get nothing back (no reply) and must never
	// leak the broker-wide /status summary (topic names, pending counts, session
	// counts) in a DM / group-General. So a stranger's "/status" falls into the
	// GateInboundDrop branch below (silent default-deny). Only a GateInboundAllow
	// result reaches the /status intercept that follows the switch.
	switch c.host.GateInbound(in) {
	case channel.GateInboundDrop:
		// Silent drop. Do NOT log content; metadata only — surface enough
		// to debug a misconfigured allowlist via broker.log without
		// echoing message text or sender handle. See default-deny posture
		// in TODO #1.
		c.host.Logf("telegram: GATE drop update=%d msg=%d chat=%d thread=%d sender=%d kind=%s",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, in.Sender.UserID, kind)
		// Dropped — never persisted; unblock the offset over this update.
		c.markUpdateDone(updateID)
		return
	case channel.GateInboundPairConsumed:
		// Body matched an active pairing code; allowlist already updated
		// by the broker. Message itself is a control-plane signal — do
		// NOT forward as inbound content.
		c.host.Logf("telegram: GATE pair-consumed update=%d msg=%d chat=%d thread=%d sender=%d (allowlist updated)",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, in.Sender.UserID)
		// Pair-consumed control signal — never persisted; unblock the offset.
		c.markUpdateDone(updateID)
		return
	}
	// Gate ALLOWED (allowlisted sender). Broker-owned command intercept: a
	// "/status" inbound from an allowlisted sender is handled by the broker
	// directly (it answers + is NEVER queued or routed to an agent). It is
	// intercepted AFTER the gate (I-SEC) so a stranger can never reach it. Other
	// commands fall through to normal routing.
	if c.isStatusCommand(in.Text) {
		if reply, handled := c.host.HandleCommand(in); handled {
			if _, err := c.SendReply(c3types.ReplyArgs{
				Channel: c.Name(), ChatID: in.ChatID, TopicID: in.TopicID, Text: reply,
				Markup: c3types.MarkupMarkdown,
			}); err != nil {
				c.host.Logf("telegram: /status reply send failed update=%d chat=%d: %v", updateID, in.ChatID, err)
			} else {
				c.host.Logf("telegram: /status reply sent update=%d chat=%d thread=%d", updateID, in.ChatID, msg.MessageThreadId)
			}
			c.host.Logf("telegram: /status handled update=%d chat=%d thread=%d (not routed)",
				updateID, msg.Chat.Id, msg.MessageThreadId)
			// Handled, never persisted — unblock the offset over this update.
			c.markUpdateDone(updateID)
			return
		}
	}
	c.host.Logf("telegram: inbound update=%d msg=%d chat=%d thread=%d kind=%s edited=%v",
		updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, kind, edited)
	// Record the message_id → update_id seam BEFORE Emit so the broker's persist
	// callback (fired after Append+fsync) can MarkDone the right source update.
	// Guarded on the tracker being live (Start seeds msgToUpdate alongside it);
	// unit tests that leave offTrk nil never persist, so the seam is unused.
	if c.offTrk != nil {
		c.mu.Lock()
		c.msgToUpdate[in.MessageID] = updateID
		c.mu.Unlock()
	}
	// I4: Emit reports false when the worker queue is full/stopped and the inbound
	// is DROPPED (never persisted). The seam above already staged msgToUpdate +
	// the tracker's in-flight registration for this update, so a drop would strand
	// the update_id in-flight forever and wedge the contiguous-prefix offset for
	// ALL inbound on a >64 burst. The message is gone from the pipeline, so the
	// offset MUST move past it: clear the now-orphaned seam entry and mark the
	// update done. (Telegram won't redeliver it within this offset, matching the
	// existing emit-DROP semantics — a queue-full burst is a capacity drop, logged
	// loudly by Emit, not a silent loss-of-offset wedge.)
	if !c.host.Emit(in) && c.offTrk != nil {
		c.mu.Lock()
		delete(c.msgToUpdate, in.MessageID)
		c.mu.Unlock()
		c.markUpdateDone(updateID)
	}
}
