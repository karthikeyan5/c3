package broker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/queue"
)

// JobKind tags route-worker jobs.
type JobKind int

const (
	JobInbound JobKind = iota
	JobOutbound
	JobRelease
	JobFetch
	JobConsume
	JobRefreshText
)

// Job is one unit of work for a route worker. Exactly one of the payload
// fields is set based on Kind.
type Job struct {
	Kind     JobKind
	Inbound  *c3types.Inbound
	Outbound *OutboundJob
	Fetch    *FetchJob
	Consume  *ConsumeJob
	Refresh  *RefreshTextJob
}

// FetchJob asks the worker to Peek/Consume the route's durable queue. Limit<0
// (or All) means everything. Ack=true consumes; false peeks. The result returns
// via ResultCh.
type FetchJob struct {
	Limit    int
	All      bool
	Ack      bool
	ResultCh chan<- FetchResult
}

// FetchResult carries the pulled messages + remaining count back to the handler.
type FetchResult struct {
	Messages  []c3types.Inbound
	Remaining int
	Err       error
}

// ConsumeJob consumes the queued lines a Claude live push covered, off the front
// (Claude live-ack path). A single push may MERGE a debounced batch of N stored
// lines into one notification (mergeBatch), so the ack must consume ALL N lines
// the push covered, not just one — otherwise N-1 stored lines are orphaned as
// phantom backlog. Count is the number of stored lines the acked push covered
// (>=1); MessageID is the merged push's id (the last in the batch), logged for
// audit. Consumption is strictly oldest-first (live delivery is in arrival
// order), so consuming Count off the head matches exactly the covered lines.
type ConsumeJob struct {
	MessageID int64
	Count     int
}

// RefreshTextJob asks the worker to refresh, in place, the stored Text of the
// still-queued line whose MessageID matches (retranscribe in-place refresh, spec
// Component 5). Routed through the single-owner worker so the cap-safe rewrite
// never touches the route's files off the worker goroutine. The result (whether a
// line was refreshed) returns via ResultCh.
type RefreshTextJob struct {
	MessageID int64
	NewText   string
	ResultCh  chan<- RefreshResult
}

// RefreshResult carries whether a queued line was refreshed back to the handler.
type RefreshResult struct {
	Refreshed bool
	Err       error
}

// OutboundJob is a queued tool-call dispatched to a channel.
type OutboundJob struct {
	Tool     string
	Args     map[string]any
	ResultCh chan<- OutboundResult
}

type OutboundResult struct {
	Result map[string]any
	Err    error
}

// RouteWorker is the per-route serial executor (spec §4.2.0). One goroutine
// owns inbound pipeline + outbound channel calls + per-route mutable state.
//
// Inbound debouncing: spec §7.3 — buffer up to debounceMax messages or
// debounceWindow time, whichever comes first, then forward as a single
// merged Inbound (concatenated text, latest message_id, sum of attachments).
type RouteWorker struct {
	key     RouteKey
	queue   chan Job
	idle    time.Duration
	broker  *Broker
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	stopped bool

	// Typing relay state (P5 / spec R3). The ticker re-pulses SendTyping for
	// the route while the agent works a turn. It is ONLY ever touched from the
	// worker's single run goroutine (armed/re-armed/disarmed in forwardOrFallback
	// + dispatchOutbound, drained by the run loop's <-typingC case), so it needs
	// NO lock — no new goroutine is introduced. typingC mirrors the ticker's
	// channel and is nil while disarmed so the select arm parks. The arm gate
	// (holder HasReplied + Capabilities.Typing) lives in armTyping.
	typingTicker *time.Ticker
	typingC      <-chan time.Time

	// typingIvl is the relay re-pulse cadence. Defaults to typingInterval; tests
	// shorten it to exercise pulse behavior within a short idle window.
	typingIvl time.Duration

	// typingPulses counts CONSECUTIVE typing ticks that fired without a reply
	// ending the turn. It is reset to 0 on arm/re-arm and on a reply (disarm),
	// and incremented on each pulse. Belt-and-suspenders: if it exceeds
	// maxTypingPulses the relay self-disarms even if (for any reason) the idle
	// timeout never trips. See pulseTyping.
	typingPulses int

	// dedup suppresses the at-least-once REPLAY a crash-mid-consume cursor rewind
	// can produce (spec: "dedupe by message_id"). Bounded FIFO; touched only from
	// the worker's single run goroutine (flushInbounds), so it needs no lock.
	dedup *deliveredDedup
}

// debounceWindow / debounceMax defaults from spec §7.3 + §6.
const (
	defaultDebounceWindow  = 1500 * time.Millisecond
	defaultDebounceMaxMsgs = 50

	// typingInterval is the re-pulse cadence for the deterministic typing relay
	// (P5). Telegram's "typing" chat action expires ~5s after it is sent, so a
	// ~4s re-pulse keeps the indicator continuously visible while the agent
	// works a turn.
	typingInterval = 4 * time.Second

	// maxTypingPulses caps how many CONSECUTIVE typing pulses may fire without a
	// reply ending the turn before the relay self-disarms. The worker's idle
	// timeout is the primary stop (a typing tick no longer extends idle), so
	// this is a belt-and-suspenders bound: at typingInterval=4s, 15 pulses is a
	// full minute of an agent that took an inbound but never replied (e.g. the
	// user switched to CLI mode mid-turn). It must never pulse forever.
	maxTypingPulses = 15
)

// newRouteWorker starts a worker that runs until ctx is canceled OR no jobs
// arrive within `idle`. broker may be nil in unit tests.
func newRouteWorker(parent context.Context, key RouteKey, idle time.Duration, broker *Broker) *RouteWorker {
	ctx, cancel := context.WithCancel(parent)
	w := &RouteWorker{
		key:       key,
		queue:     make(chan Job, 64),
		idle:      idle,
		broker:    broker,
		cancel:    cancel,
		done:      make(chan struct{}),
		typingIvl: typingInterval,
		// 2048 is comfortably larger than the 1000-message per-route cap, so a
		// full-queue recovery replay is fully covered by the dedup window.
		dedup: newDeliveredDedup(2048),
	}
	go w.run(ctx)
	return w
}

func (w *RouteWorker) run(ctx context.Context) {
	defer close(w.done)
	// Backstop: a panic in the run-loop machinery (outside the per-method guards
	// below) is recovered + logged instead of crashing the whole broker. The
	// worker then exits cleanly (done closes); WorkerPool.Submit respawns a fresh
	// worker on the next job for this route. The per-method guards on the
	// panic-prone work (flushInbounds/flushEvent/dispatchOutbound/pulseTyping)
	// keep THIS worker alive for the common case; this only catches the rest.
	defer recoverGoroutine("worker.run")
	idleTimer := time.NewTimer(w.idle)
	defer idleTimer.Stop()
	// Ensure the typing ticker is stopped on any exit (ctx cancel, idle,
	// release, queue close) so it never leaks past the worker's life.
	defer w.disarmTyping()

	var debBuf []*c3types.Inbound
	var debTimer *time.Timer
	var debC <-chan time.Time

	flushDeb := func() {
		if len(debBuf) > 0 {
			w.flushInbounds(ctx, debBuf)
			debBuf = nil
		}
		if debTimer != nil {
			debTimer.Stop()
			debTimer = nil
		}
		debC = nil
	}

	// resetIdle restarts the worker's idle countdown. It is called ONLY from
	// real-work arms (debounce flush, inbound, outbound) — NOT from the typing
	// tick. A typing pulse must not extend the worker's lifetime: typingInterval
	// (4s) is shorter than any sane idle window, so if the tick reset idle, an
	// armed ticker would re-arm idle forever and a worker that took an inbound
	// but never replied (e.g. user switched to CLI mode) would pulse "typing…"
	// indefinitely and never idle out. Resetting idle only on real work lets the
	// worker idle out normally; its defer disarmTyping() then stops the relay.
	resetIdle := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(w.idle)
	}

	for {
		select {
		case <-ctx.Done():
			flushDeb()
			return
		case <-idleTimer.C:
			flushDeb()
			return
		case <-debC:
			resetIdle()
			flushDeb()
		case <-w.typingC:
			// Typing relay tick (P5). Runs in the worker's single goroutine —
			// no new concurrency. Pulse the channel's typing action for this
			// route; the ticker keeps firing on its own cadence until disarmed.
			// Deliberately does NOT resetIdle — a typing pulse is not real work
			// and must not keep the worker (and thus the relay) alive forever.
			w.pulseTyping(ctx)
		case job, ok := <-w.queue:
			if !ok {
				flushDeb()
				return
			}
			resetIdle()
			switch job.Kind {
			case JobInbound:
				if job.Inbound == nil {
					continue
				}
				// CB-1: a synthesized channel EVENT (poll_result / reaction /
				// callback) must NEVER share a debounce batch with text. Merging
				// it would (a) drop its Kind/Event through mergeBatch's text-only
				// copy and (b) run hasVoice/STT over a non-voice event. So: flush
				// any buffered text first, then forward the event ALONE, bypassing
				// the debounce buffer and the STT path entirely.
				if job.Inbound.IsEvent() {
					flushDeb()
					w.flushEvent(ctx, job.Inbound)
					continue
				}
				debBuf = append(debBuf, job.Inbound)
				maxMsgs := w.debounceMaxMessages()
				if len(debBuf) >= maxMsgs {
					flushDeb()
					continue
				}
				if debTimer == nil {
					debTimer = time.NewTimer(w.debounceWindow())
					debC = debTimer.C
				}
			case JobOutbound:
				w.dispatchOutbound(ctx, job.Outbound)
			case JobFetch:
				w.handleFetch(ctx, job.Fetch)
			case JobConsume:
				w.handleConsume(ctx, job.Consume)
			case JobRefreshText:
				w.handleRefreshText(ctx, job.Refresh)
			case JobRelease:
				flushDeb()
				return
			}
		}
	}
}

// flushInbounds runs the plugin pipeline + forwards a debounce-collapsed
// batch as a single ipc.OpInbound.
//
// Merge rules (spec §7.3):
//   - Text: each inbound's text joined with "\n", in arrival order. Voice
//     STT (OnVoiceReceived) runs per-inbound BEFORE merge so transcripts
//     land in the right order.
//   - MessageID: latest in the batch (canonical id for the merged block).
//   - Timestamp: earliest (when the burst started).
//   - Sender: from the latest message (most recent author wins; bursts
//     from a single user are the common case).
//   - ReplyTo: from the FIRST message that has one (only one quote-reply
//     attribution per merged block — agent sees the original anchor).
//   - Attachments: concatenated in order — agent sees all media.
//   - OnInbound chain runs ONCE on the merged Inbound, not per-message.
func (w *RouteWorker) flushInbounds(ctx context.Context, batch []*c3types.Inbound) {
	defer recoverGoroutine(fmt.Sprintf("worker.flushInbounds chan=%s chat=%d", w.key.Channel, w.key.ChatID))
	if w.broker == nil || len(batch) == 0 {
		return
	}

	// Per-inbound STT substitution.
	if w.broker.Plugins != nil {
		for _, in := range batch {
			// CB-1 defense-in-depth: never run voice/STT over a synthesized
			// event. The run loop already diverts events to flushEvent, so a
			// batch here is all ordinary messages — this guard makes that
			// invariant explicit and survives any future caller change.
			if in.IsEvent() || !hasVoice(in) {
				continue
			}
			payload := c3types.VoicePayload{
				Channel:   in.Channel,
				ChatID:    in.ChatID,
				TopicID:   in.TopicID,
				MessageID: in.MessageID,
				FileID:    in.Attachments[0].FileID,
				MIME:      in.Attachments[0].MIME,
				Size:      in.Attachments[0].Size,
			}
			transcript := w.broker.Plugins.FireOnVoiceReceived(ctx, payload)
			switch {
			case transcript != "":
				in.Text = w.sttPrefix(in.Channel) + transcript
			case in.Text == "":
				// Defense-in-depth: no plugin produced a transcript AND the
				// message has no caption. Without this, the adapter falls
				// back to a silent "(voice message)" placeholder. Marker
				// shape matches sttFailureMarker() in plugins/builtins/stt.
				// 2026-05-18 (#13): append broker log path so a fresh
				// install user knows where the actual traceback lives.
				in.Text = w.sttPrefix(in.Channel) + "[STT FAILED: no_transcript_plugin — see " + LogPath() + "]"
			}
		}
	}

	// Durable storage: persist EACH message (one queue line) AFTER STT
	// substitution so the stored line already carries the transcript. Storage is
	// per-message; the merge below is a delivery-presentation concern only and
	// does not merge stored lines. Append failure = persist failure: do NOT mark
	// the update_id done (markPersisted is only called on success), so the
	// Telegram offset can't pass it and the message is redelivered (loss-free).
	if w.broker != nil && w.broker.Queue != nil {
		qrk := queueRouteKey(w.key)
		for _, in := range batch {
			// Dedup the at-least-once REPLAY a crash-mid-consume can produce
			// (spec: "dedupe by message_id"). See newDeliveredDedup below.
			if w.dedup != nil && w.dedup.seenBefore(in.MessageID) {
				log.Printf("dedup chan=%s chat=%d topic=%s msg=%d: already delivered, suppressing replay",
					w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID)
				continue
			}
			if err := w.broker.Queue.Append(qrk, in); err != nil {
				log.Printf("queue append FAIL chan=%s chat=%d topic=%s msg=%d: %v — offset will NOT advance; Telegram redelivers — %s",
					w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, err, fallbackSummary(in))
				// Best-effort Telegram notice (spec Error-handling: disk-full =
				// persist failure → log + best-effort Telegram notice). Cooldown'd
				// via the existing fallback tracker so a stuck disk doesn't spam.
				w.notePersistFailure(in)
				continue
			}
			w.markPersisted(in)
			w.evictIfOverCap(qrk)
		}
	}

	// Merge (delivery presentation only).
	merged := mergeBatch(batch)

	// OnInbound chain on the merged inbound.
	if w.broker.Plugins != nil {
		next := w.broker.Plugins.FireOnInbound(ctx, merged)
		if next == nil {
			return // dropped
		}
		merged = next
	}

	w.forwardOrFallback(ctx, merged, len(batch))
}

// flushEvent forwards a single synthesized channel EVENT (poll_result /
// reaction / callback) straight through delivery. CB-1: events are NEVER
// debounce-merged with text and NEVER run through the voice/STT substitution —
// they are delivered intact and on their own. The OnInbound plugin chain still
// runs (so a plugin can observe/drop an event), but the per-inbound STT loop in
// flushInbounds is skipped entirely. An event whose chain returns nil is
// dropped, matching the message path.
func (w *RouteWorker) flushEvent(ctx context.Context, ev *c3types.Inbound) {
	defer recoverGoroutine(fmt.Sprintf("worker.flushEvent chan=%s chat=%d", w.key.Channel, w.key.ChatID))
	if w.broker == nil || ev == nil {
		return
	}
	if w.broker.Plugins != nil {
		next := w.broker.Plugins.FireOnInbound(ctx, ev)
		if next == nil {
			return // dropped by a plugin
		}
		ev = next
	}
	w.forwardOrFallback(ctx, ev, 0)
}

// mergeBatch collapses a batch of inbounds into one. See flushInbounds for
// the merge rules. Single-element batches return the element unchanged.
//
// CB-1 invariant: this is only ever called with ORDINARY message inbounds. The
// run loop diverts every Kind != "" event to flushEvent before it can reach a
// debounce batch, so an event never lands here. As defense-in-depth, if an
// event ever does appear in a multi-element batch it is carried through verbatim
// (its Kind/Event preserved) rather than silently text-spliced — see the
// per-element copy below. The single-element fast path already returns the
// element unchanged, preserving Kind/Event for the (event-alone) case.
func mergeBatch(batch []*c3types.Inbound) *c3types.Inbound {
	if len(batch) == 1 {
		return batch[0]
	}
	last := batch[len(batch)-1]
	out := &c3types.Inbound{
		Channel:   last.Channel,
		ChatID:    last.ChatID,
		TopicID:   last.TopicID,
		MessageID: last.MessageID,
		Sender:    last.Sender,
		Timestamp: batch[0].Timestamp,
		// Carry the latest event metadata through so a stray event in a batch is
		// not silently dropped. In normal operation the run loop guarantees no
		// event reaches mergeBatch (events flush alone via flushEvent).
		Kind:  last.Kind,
		Event: last.Event,
	}
	var texts []string
	for _, in := range batch {
		if in.Text != "" {
			texts = append(texts, in.Text)
		}
		out.Attachments = append(out.Attachments, in.Attachments...)
		if out.ReplyTo == nil && in.ReplyTo != nil {
			out.ReplyTo = in.ReplyTo
		}
	}
	out.Text = strings.Join(texts, "\n")
	return out
}

// forwardOrFallback is the post-pipeline delivery: claimed stub or
// cooldown-fallback reply.
//
// Logging policy (DEBUGGING.md):
//   - Successful delivery to an adapter → one terse line, NO content.
//   - Any failure path where the CLI never saw the message → log line
//     INCLUDES content (sender, text [200-char cap], attachment summary).
//     This is the explicit "don't lose undelivered content" rule from
//     2026-05-09: "I don't want you to lose the content without
//     delivering it anywhere".
//
// "Failure paths" here means anything that ends without a `delivered` line:
// holder-conn-bad, write-error, fallback-cooldown-drop, fallback-send-fail,
// AND fallback-sent (the user's message was bounced back to Telegram with a
// boilerplate; no CLI processed it).
func (w *RouteWorker) forwardOrFallback(_ context.Context, in *c3types.Inbound, covered int) {
	holder, claimed := w.broker.Routes.Holder(w.key)

	// Liveness sweep: if the holder's PID is dead (e.g. Claude Code killed
	// the adapter as part of /mcp reconnect, or the user quit the CLI),
	// the claim is stale. Release it and fall through to fallback. Without
	// this, every inbound for a dead-holder claim ends with a "holder.Conn
	// is not *ipc.Conn" log line and silently never reaches anywhere
	// (including no Telegram fallback). This is the 2026-05-14 regression
	// Karthi hit immediately after /mcp reconnect.
	if claimed && !holder.IsAlive() {
		log.Printf("deliver STALE chan=%s chat=%d topic=%s msg=%d: holder cli=%s pid=%d is dead, releasing claim",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
			holder.CLI, holder.PID)
		w.broker.Routes.Release(w.key, holder.ConnID)
		claimed = false
		holder = nil
	}

	// Holder is alive (kill -0 succeeded) but its conn is nil — the adapter
	// is between reconnects (D2 / adapter-ipc-1). Previously this DROPPED the
	// inbound silently ("alive but disconnected"); a message sent during the
	// adapter's reconnect window was lost forever, with nothing to replay it.
	// Instead, treat an unreachable-but-alive holder like "no live CLI" and
	// fall through to the SAME Telegram fallback the STALE branch uses, so the
	// user is told to resend rather than left wondering where their message
	// went. We do NOT release the claim — the holder is still alive and will
	// rebind its conn on reconnect; only THIS inbound bounces.
	// Snapshot the holder's conn ONCE under its lock (ConnValue is race-safe vs
	// the handler goroutine's MarkDisconnected/Reattach), and use that one value
	// for both the bounce-check and the delivery — a second raw read could race
	// to a different value mid-delivery.
	var holderConn *ipc.Conn
	if claimed {
		holderConn, _ = holder.ConnValue().(*ipc.Conn)
		if holderConn == nil {
			log.Printf("deliver BOUNCE chan=%s chat=%d topic=%s msg=%d: holder cli=%s pid=%d alive but disconnected — falling back so user can resend — %s",
				w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
				holder.CLI, holder.PID, fallbackSummary(in))
			claimed = false
			holder = nil
		}
	}

	if claimed {
		conn := holderConn
		// Stamp the covered-line count + remaining backlog onto the live push so
		// (a) the OpInboundDelivered ack can Consume exactly the lines this push
		// covered (a merged batch covers N stored lines, not 1) and (b) the Claude
		// adapter can append the recovery nudge. The covered lines are STILL queued
		// at push time (only Consumed on the ack), so subtract covered from the
		// route's pending count → a fully-caught-up live session shows Pending:0.
		covered := covEffective(covered) // >=1
		pending := 0
		if w.broker != nil && w.broker.Queue != nil {
			if n, _ := w.broker.Queue.Pending(queueRouteKey(w.key)); n > covered {
				pending = n - covered // covered lines are still queued until acked
			}
		}
		if err := conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: *in, Covered: covered, Pending: pending}); err != nil {
			log.Printf("deliver FAIL chan=%s chat=%d topic=%s msg=%d to cli=%s pid=%d: %v — %s",
				w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
				holder.CLI, holder.PID, err, fallbackSummary(in))
			return
		}
		// Live delivery: the message stays queued until the adapter sends
		// OpInboundDelivered{ok:true}, which Consumes it (queue dispatch task).
		// This keeps an un-acked push recoverable as backlog (recovery nudge).
		log.Printf("delivered chan=%s chat=%d topic=%s msg=%d to cli=%s pid=%d conn=%d",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
			holder.CLI, holder.PID, holder.ConnID)
		// Typing relay (P5): an inbound was just delivered to the claimed
		// holder, so the agent is about to work this turn. Arm the typing
		// ticker — but ONLY if this holder has already replied at least once
		// (the deterministic "in Telegram mode" gate; see Stub.hasReplied) and
		// the channel supports typing. armTyping enforces both gates.
		w.armTyping(holder)
		return
	}
	// A synthesized channel EVENT (poll_result / reaction / callback) is NOT a
	// human message awaiting a reply, so it must never be bounced back as the
	// "no CLI attached" fallback boilerplate. A late timed/auto-close poll can
	// fire after the session detaches; on an unclaimed route we drop it with a
	// metadata-only log instead of spamming the chat. (A CLAIMED route still
	// receives the event unchanged — handled in the claimed branch above.)
	if in.IsEvent() {
		log.Printf("drop chan=%s chat=%d topic=%s msg=%d: no claim, event not bounced as fallback (kind=%s)",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, in.Kind)
		return
	}
	// Append-if-absent: the normal path already appended this message (and its
	// MessageID is recorded in w.dedup) in flushInbounds. A direct-call/test path
	// (or any future caller that reaches forwardOrFallback without flushInbounds)
	// has NOT — so append it here, gated by the SAME dedup set so the normal path
	// does not double-store (flushInbounds already recorded the id, so seenBefore
	// returns true here and we skip). This keeps the running count below accurate
	// for the message the user just sent. Events are never queued.
	if w.broker.Queue != nil {
		if w.dedup == nil || !w.dedup.seenBefore(in.MessageID) {
			_ = w.broker.Queue.Append(queueRouteKey(w.key), in)
		}
	}
	// No live claim: the message is already durably queued (flushInbounds — or
	// the append-if-absent above — stored it). Replace the old drop with a
	// "held, nothing lost" auto-reply, cooldown'd to once per window, carrying
	// the RUNNING queued count.
	if !w.broker.Fallbacks.ShouldSend(w.key) {
		log.Printf("hold chan=%s chat=%d topic=%s msg=%d: no claim, queued; held-reply in cooldown — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, fallbackSummary(in))
		return
	}
	ch, err := w.broker.Channel(in.Channel)
	if err != nil {
		log.Printf("hold FAIL chan=%s chat=%d topic=%s msg=%d: channel lookup: %v — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, err, fallbackSummary(in))
		return
	}
	count := 1
	if w.broker.Queue != nil {
		if n, _ := w.broker.Queue.Pending(queueRouteKey(w.key)); n > 0 {
			count = n
		}
	}
	args := c3types.ReplyArgs{Channel: in.Channel, ChatID: in.ChatID, TopicID: in.TopicID, Text: heldReplyText(count)}
	if _, err := ch.SendReply(args); err != nil {
		log.Printf("hold FAIL chan=%s chat=%d topic=%s msg=%d: send held-reply: %v — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, err, fallbackSummary(in))
		return
	}
	log.Printf("hold chan=%s chat=%d topic=%s msg=%d: no claim, queued + held-reply (count=%d) — %s",
		w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, count, fallbackSummary(in))
}

// fallbackSummary returns a one-liner of message content for use in
// failure-path log lines. Content is truncated (text 200 chars) and
// quote-escaped. ONLY call this on paths where the message was NOT
// delivered to a CLI — successful delivery never logs content.
func fallbackSummary(in *c3types.Inbound) string {
	var parts []string
	switch {
	case in.Sender.Username != "" && in.Sender.UserID != 0:
		parts = append(parts, fmt.Sprintf("from=@%s(uid=%d)", in.Sender.Username, in.Sender.UserID))
	case in.Sender.Username != "":
		parts = append(parts, fmt.Sprintf("from=@%s", in.Sender.Username))
	case in.Sender.UserID != 0:
		parts = append(parts, fmt.Sprintf("from=uid=%d", in.Sender.UserID))
	}
	if in.Text != "" {
		text := in.Text
		const maxText = 200
		if len(text) > maxText {
			text = text[:maxText] + "…"
		}
		parts = append(parts, fmt.Sprintf("text=%q", text))
	}
	if in.ReplyTo != nil {
		parts = append(parts, fmt.Sprintf("reply_to=%d", in.ReplyTo.MessageID))
	}
	for _, att := range in.Attachments {
		parts = append(parts, fmt.Sprintf("attach=%s/%d", att.Kind, att.Size))
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " ")
}

// covEffective normalizes a covered-line count to >=1 (a live push always covers
// at least the one merged message; 0/negative comes from the event path or a
// direct test call and is treated as a single line).
func covEffective(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// markPersisted notifies the broker that an inbound's source update_id is now
// durably stored, so the persisted-offset tracker may advance over it. The
// broker holds the channel-side tracker via a registered callback (set when the
// telegram channel starts); a nil callback (unit tests, non-telegram) is a no-op.
func (w *RouteWorker) markPersisted(in *c3types.Inbound) {
	if w.broker == nil {
		return
	}
	w.broker.notifyPersisted(in)
}

// notePersistFailure sends ONE best-effort, cooldown'd Telegram notice when a
// durable Append fails (spec Error-handling: "Disk full on append: treat as
// persist failure → do not advance offset → Telegram retains → log + (best-
// effort) Telegram notice"). The offset non-advance + Telegram retention is the
// real safety net; this notice just tells the human why a message seems stuck.
// Reuses the existing fallback cooldown so a stuck disk does not spam the topic.
func (w *RouteWorker) notePersistFailure(in *c3types.Inbound) {
	if w.broker == nil || w.broker.Fallbacks == nil || !w.broker.Fallbacks.ShouldSend(w.key) {
		return
	}
	ch, err := w.broker.Channel(w.key.Channel)
	if err != nil {
		return
	}
	var topicID *int64
	if w.key.HasTopic {
		t := w.key.TopicID
		topicID = &t
	}
	// in's content is intentionally NOT echoed back to the chat (it failed to
	// persist, but we don't surface user content in an error notice). The caller
	// already logged the metadata via fallbackSummary(in) on the append-fail line.
	_, _ = ch.SendReply(c3types.ReplyArgs{
		Channel: w.key.Channel, ChatID: w.key.ChatID, TopicID: topicID,
		Text: "⚠️ Could not persist a received message (storage error) — it was NOT lost; Telegram will redeliver it. Check the broker host's disk.",
	})
}

// evictIfOverCap enforces the per-route cap. On a drop it logs + sends ONE
// Telegram notice (never silent). Errors are logged, not fatal.
func (w *RouteWorker) evictIfOverCap(qrk queue.RouteKey) {
	dropped, err := w.broker.Queue.EvictOverCap(qrk)
	if err != nil {
		log.Printf("queue evict FAIL chan=%s chat=%d topic=%s: %v", w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), err)
		return
	}
	if dropped == 0 {
		return
	}
	log.Printf("queue CAP chan=%s chat=%d topic=%s: dropped %d oldest held message(s) over cap", w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), dropped)
	if ch, cerr := w.broker.Channel(w.key.Channel); cerr == nil {
		var topicID *int64
		if w.key.HasTopic {
			t := w.key.TopicID
			topicID = &t
		}
		_, _ = ch.SendReply(c3types.ReplyArgs{
			Channel: w.key.Channel, ChatID: w.key.ChatID, TopicID: topicID,
			Text: fmt.Sprintf("⚠️ queue full — dropped %d oldest held message(s); attach a session soon.", dropped),
		})
	}
}

// handleFetch peeks or consumes the route's durable queue and returns the batch.
func (w *RouteWorker) handleFetch(_ context.Context, job *FetchJob) {
	if job == nil || job.ResultCh == nil {
		return
	}
	defer recoverGoroutineThen("worker.handleFetch", func() {
		select {
		case job.ResultCh <- FetchResult{Err: fmt.Errorf("internal panic in fetch_queue")}:
		default:
		}
	})
	if w.broker == nil || w.broker.Queue == nil {
		job.ResultCh <- FetchResult{Err: errOutboundNotImpl}
		return
	}
	qrk := queueRouteKey(w.key)
	n := job.Limit
	if job.All {
		n = -1
	}
	var msgs []c3types.Inbound
	var err error
	if job.Ack {
		msgs, err = w.broker.Queue.Consume(qrk, n)
	} else {
		msgs, err = w.broker.Queue.Peek(qrk, n)
	}
	if err != nil {
		job.ResultCh <- FetchResult{Err: err}
		return
	}
	remaining, _ := w.broker.Queue.Pending(qrk)
	if !job.Ack {
		remaining -= len(msgs) // peek doesn't advance; "remaining after this batch"
		if remaining < 0 {
			remaining = 0
		}
	}
	log.Printf("fetch_queue chan=%s chat=%d topic=%s ack=%v returned=%d remaining=%d",
		w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), job.Ack, len(msgs), remaining)
	job.ResultCh <- FetchResult{Messages: msgs, Remaining: remaining}
}

// handleConsume drops the oldest Count queued messages (Claude live-ack: a
// pushed notification the adapter accepted, which may have MERGED a debounced
// batch of Count stored lines). Consuming Count off the head matches exactly the
// lines the push covered — otherwise a merged push of N would orphan N-1 stored
// lines as phantom backlog. Count defaults to 1 (defensive: an older adapter or
// a single-message push). MessageID is logged for audit; consumption is strictly
// oldest-first (live delivery is in arrival order).
func (w *RouteWorker) handleConsume(_ context.Context, job *ConsumeJob) {
	if job == nil || w.broker == nil || w.broker.Queue == nil {
		return
	}
	defer recoverGoroutine("worker.handleConsume")
	n := job.Count
	if n < 1 {
		n = 1
	}
	qrk := queueRouteKey(w.key)
	if _, err := w.broker.Queue.Consume(qrk, n); err != nil {
		log.Printf("queue consume(live-ack) FAIL chan=%s chat=%d topic=%s msg=%d count=%d: %v",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), job.MessageID, n, err)
	}
}

// handleRefreshText rewrites, in place, the stored Text of the still-queued line
// whose MessageID matches (retranscribe in-place refresh, spec Component 5). Runs
// on the route's single-owner worker so the cap-safe rewrite never races other
// file ops for this route. A miss (message not queued / already consumed) is a
// clean no-op reported as Refreshed=false.
func (w *RouteWorker) handleRefreshText(_ context.Context, job *RefreshTextJob) {
	if job == nil || job.ResultCh == nil {
		return
	}
	defer recoverGoroutineThen("worker.handleRefreshText", func() {
		select {
		case job.ResultCh <- RefreshResult{Err: fmt.Errorf("internal panic in refresh_text")}:
		default:
		}
	})
	if w.broker == nil || w.broker.Queue == nil {
		job.ResultCh <- RefreshResult{Err: errOutboundNotImpl}
		return
	}
	qrk := queueRouteKey(w.key)
	refreshed, err := w.broker.Queue.RefreshText(qrk, job.MessageID, job.NewText)
	if err != nil {
		log.Printf("retranscribe refresh FAIL chan=%s chat=%d topic=%s msg=%d: %v",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), job.MessageID, err)
		job.ResultCh <- RefreshResult{Err: err}
		return
	}
	log.Printf("retranscribe refresh chan=%s chat=%d topic=%s msg=%d refreshed=%v",
		w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), job.MessageID, refreshed)
	job.ResultCh <- RefreshResult{Refreshed: refreshed}
}

// deliveredDedup is a bounded FIFO set of recently-delivered MessageIDs for this
// route. It suppresses the at-least-once REPLAY that a crash-mid-consume cursor
// rewind can produce (spec: "dedupe by message_id"). Bounded so it never grows
// without limit; the window only needs to cover a recovery replay, not history.
type deliveredDedup struct {
	seen  map[int64]struct{}
	order []int64
	cap   int
}

func newDeliveredDedup(capN int) *deliveredDedup {
	return &deliveredDedup{seen: make(map[int64]struct{}, capN), cap: capN}
}

// seenBefore reports whether id was already delivered; otherwise records it
// (dropping the oldest when over cap) and returns false.
func (d *deliveredDedup) seenBefore(id int64) bool {
	if id == 0 {
		return false // unidentifiable; never dedup
	}
	if _, ok := d.seen[id]; ok {
		return true
	}
	d.seen[id] = struct{}{}
	d.order = append(d.order, id)
	if len(d.order) > d.cap {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return false
}

// dispatchOutbound translates an OutboundJob into a channel call, returning
// the result via job.ResultCh.
func (w *RouteWorker) dispatchOutbound(_ context.Context, job *OutboundJob) {
	if job == nil || job.ResultCh == nil {
		return
	}
	// A panic in the channel send (dispatchTool) must NOT crash the broker AND
	// must NOT leave the waiting tool-call blocked on ResultCh forever — recover,
	// log, and best-effort deliver a failure result (non-blocking, in case the
	// result was already sent).
	defer recoverGoroutineThen("worker.dispatchOutbound tool="+job.Tool, func() {
		select {
		case job.ResultCh <- OutboundResult{Err: fmt.Errorf("internal panic dispatching tool %q", job.Tool)}:
		default:
		}
	})
	if w.broker == nil {
		job.ResultCh <- OutboundResult{Err: errOutboundNotImpl}
		return
	}
	ch, err := w.broker.Channel(w.key.Channel)
	if err != nil {
		job.ResultCh <- OutboundResult{Err: err}
		return
	}
	result, err := dispatchTool(ch, w.key, job.Tool, job.Args)

	// Typing relay (P5). All of this runs in the worker's single goroutine.
	//   - A successful `reply` ENDS the turn: mark the holder as having replied
	//     (so future turns on this route arm typing) and disarm the ticker.
	//   - A successful non-reply tool-call (edit_message / react /
	//     download_attachment / send_typing) means the agent is still working;
	//     re-arm so typing stays visible across the turn's tool calls. Re-arm is
	//     gated the same way as the initial arm (holder HasReplied + Typing cap)
	//     via armTyping.
	if err == nil {
		if job.Tool == "reply" {
			if holder, ok := w.broker.Routes.Holder(w.key); ok {
				holder.MarkReplied()
			}
			w.disarmTyping()
		} else if holder, ok := w.broker.Routes.Holder(w.key); ok {
			w.armTyping(holder)
		}
	}

	job.ResultCh <- OutboundResult{Result: result, Err: err}
}

// armTyping starts (or keeps running) the per-route typing ticker, IF the
// deterministic gates pass: the holder has replied at least once (Telegram-mode
// proxy) AND the channel advertises Typing. Both gates avoid pulsing "typing…"
// for default CLI-mode sessions. Called only from the worker goroutine
// (forwardOrFallback delivered path + dispatchOutbound non-reply path), so it
// mutates worker-local ticker state without a lock and never holds a lock across
// a network call. An already-armed ticker is left running (re-arm = no-op while
// armed) so the cadence stays steady across a turn's tool calls.
func (w *RouteWorker) armTyping(holder *Stub) {
	if holder == nil || !holder.HasReplied() {
		return
	}
	if w.broker == nil {
		return
	}
	ch, err := w.broker.Channel(w.key.Channel)
	if err != nil || !ch.Capabilities().Typing {
		return
	}
	if w.typingTicker != nil {
		// Already armed. A re-arm marks fresh agent activity (a non-reply tool
		// call this turn), so reset the unanswered-pulse counter but KEEP the
		// existing ticker so the cadence stays steady across a turn's calls.
		w.typingPulses = 0
		return
	}
	ivl := w.typingIvl
	if ivl <= 0 {
		ivl = typingInterval
	}
	w.typingTicker = time.NewTicker(ivl)
	w.typingC = w.typingTicker.C
	w.typingPulses = 0
}

// disarmTyping stops the per-route typing ticker if armed. Idempotent. Called
// from the worker goroutine (dispatchOutbound reply path) and from run's defer
// on worker shutdown — so the ticker never leaks past the worker's life.
func (w *RouteWorker) disarmTyping() {
	if w.typingTicker == nil {
		return
	}
	w.typingTicker.Stop()
	w.typingTicker = nil
	w.typingC = nil
	w.typingPulses = 0
}

// pulseTyping fires one SendTyping for the route. Resolves the channel + the
// route's chat/topic, releases (it holds no lock across the call), then sends.
// On error it logs and disarms — a persistently failing channel should not keep
// the ticker spinning. Runs in the worker goroutine (the run loop's <-typingC).
func (w *RouteWorker) pulseTyping(_ context.Context) {
	defer recoverGoroutine(fmt.Sprintf("worker.pulseTyping chan=%s chat=%d", w.key.Channel, w.key.ChatID))
	if w.broker == nil {
		w.disarmTyping()
		return
	}
	// Belt-and-suspenders bound: if the relay has pulsed maxTypingPulses times
	// without a reply ending the turn, disarm even though the idle timeout is the
	// primary stop. Guards against any future change that re-extends idle on a
	// pulse and would otherwise let "typing…" spin forever.
	w.typingPulses++
	if w.typingPulses > maxTypingPulses {
		log.Printf("typing RELAY CAP chan=%s chat=%d topic=%s: %d consecutive pulses with no reply — disarming relay",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), w.typingPulses-1)
		w.disarmTyping()
		return
	}
	ch, err := w.broker.Channel(w.key.Channel)
	if err != nil {
		w.disarmTyping()
		return
	}
	var topicID *int64
	if w.key.HasTopic {
		t := w.key.TopicID
		topicID = &t
	}
	if err := ch.SendTyping(w.key.ChatID, topicID); err != nil {
		log.Printf("typing PULSE FAIL chan=%s chat=%d topic=%s: %v (disarming relay)",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), err)
		w.disarmTyping()
	}
}

// Submit enqueues a job. Returns false if the worker is stopped or the
// queue is full.
func (w *RouteWorker) Submit(job Job) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	select {
	case w.queue <- job:
		return true
	default:
		return false
	}
}

// Stop signals the worker to drain and exit. Idempotent.
func (w *RouteWorker) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	w.mu.Unlock()
	w.cancel()
	<-w.done
}

// Done returns a channel that closes when the worker has exited.
func (w *RouteWorker) Done() <-chan struct{} { return w.done }

var errOutboundNotImpl = workerErr("worker has no broker reference")

type workerErr string

func (e workerErr) Error() string { return string(e) }

func hasVoice(in *c3types.Inbound) bool {
	return len(in.Attachments) > 0 && in.Attachments[0].Kind == "voice"
}

func (w *RouteWorker) sttPrefix(chanName string) string {
	if w.broker == nil || w.broker.Mappings() == nil {
		return "[Transcribed voice]: "
	}
	cc, ok := w.broker.Mappings().Channels[chanName]
	if !ok || cc.STTPrefix == "" {
		return "[Transcribed voice]: "
	}
	return cc.STTPrefix
}

func (w *RouteWorker) debounceWindow() time.Duration {
	if w.broker == nil || w.broker.Mappings() == nil {
		return defaultDebounceWindow
	}
	cc, ok := w.broker.Mappings().Channels[w.key.Channel]
	if !ok || cc.DebounceMS <= 0 {
		return defaultDebounceWindow
	}
	return time.Duration(cc.DebounceMS) * time.Millisecond
}

func (w *RouteWorker) debounceMaxMessages() int {
	if w.broker == nil || w.broker.Mappings() == nil {
		return defaultDebounceMaxMsgs
	}
	cc, ok := w.broker.Mappings().Channels[w.key.Channel]
	if !ok || cc.DebounceMaxMessages <= 0 {
		return defaultDebounceMaxMsgs
	}
	return cc.DebounceMaxMessages
}
