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
)

// JobKind tags route-worker jobs.
type JobKind int

const (
	JobInbound JobKind = iota
	JobOutbound
	JobRelease
)

// Job is one unit of work for a route worker. Exactly one of the payload
// fields is set based on Kind.
type Job struct {
	Kind     JobKind
	Inbound  *c3types.Inbound
	Outbound *OutboundJob
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
}

// debounceWindow / debounceMax defaults from spec §7.3 + §6.
const (
	defaultDebounceWindow  = 1500 * time.Millisecond
	defaultDebounceMaxMsgs = 50
)

// newRouteWorker starts a worker that runs until ctx is canceled OR no jobs
// arrive within `idle`. broker may be nil in unit tests.
func newRouteWorker(parent context.Context, key RouteKey, idle time.Duration, broker *Broker) *RouteWorker {
	ctx, cancel := context.WithCancel(parent)
	w := &RouteWorker{
		key:    key,
		queue:  make(chan Job, 64),
		idle:   idle,
		broker: broker,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go w.run(ctx)
	return w
}

func (w *RouteWorker) run(ctx context.Context) {
	defer close(w.done)
	idleTimer := time.NewTimer(w.idle)
	defer idleTimer.Stop()

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

	stopIdle := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
	}

	for {
		stopIdle()
		idleTimer.Reset(w.idle)

		select {
		case <-ctx.Done():
			flushDeb()
			return
		case <-idleTimer.C:
			flushDeb()
			return
		case <-debC:
			flushDeb()
		case job, ok := <-w.queue:
			if !ok {
				flushDeb()
				return
			}
			switch job.Kind {
			case JobInbound:
				if job.Inbound == nil {
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
	if w.broker == nil || len(batch) == 0 {
		return
	}

	// Per-inbound STT substitution.
	if w.broker.Plugins != nil {
		for _, in := range batch {
			if !hasVoice(in) {
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
				in.Text = w.sttPrefix(in.Channel) + "[STT FAILED: no_transcript_plugin]"
			}
		}
	}

	// Merge.
	merged := mergeBatch(batch)

	// OnInbound chain on the merged inbound.
	if w.broker.Plugins != nil {
		next := w.broker.Plugins.FireOnInbound(ctx, merged)
		if next == nil {
			return // dropped
		}
		merged = next
	}

	w.forwardOrFallback(ctx, merged)
}

// mergeBatch collapses a batch of inbounds into one. See flushInbounds for
// the merge rules. Single-element batches return the element unchanged.
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
func (w *RouteWorker) forwardOrFallback(_ context.Context, in *c3types.Inbound) {
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

	if claimed {
		conn, ok := holder.Conn.(*ipc.Conn)
		if !ok {
			// Holder is alive (kill -0 succeeded) but its conn is nil —
			// the adapter is between reconnects. Don't deliver to a half-
			// dead stub; drop with a log line. The adapter will catch up
			// when it reconnects (route-conn transfer rebinds the stub).
			log.Printf("deliver SKIP chan=%s chat=%d topic=%s msg=%d: holder cli=%s pid=%d alive but disconnected — %s",
				w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
				holder.CLI, holder.PID, fallbackSummary(in))
			return
		}
		if err := conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: *in}); err != nil {
			log.Printf("deliver FAIL chan=%s chat=%d topic=%s msg=%d to cli=%s pid=%d: %v — %s",
				w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
				holder.CLI, holder.PID, err, fallbackSummary(in))
			return
		}
		log.Printf("delivered chan=%s chat=%d topic=%s msg=%d to cli=%s pid=%d conn=%d",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
			holder.CLI, holder.PID, holder.ConnID)
		return
	}
	if !w.broker.Fallbacks.ShouldSend(w.key) {
		log.Printf("drop chan=%s chat=%d topic=%s msg=%d: no claim, fallback in cooldown — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
			fallbackSummary(in))
		return
	}
	ch, err := w.broker.Channel(in.Channel)
	if err != nil {
		log.Printf("fallback FAIL chan=%s chat=%d topic=%s msg=%d: channel lookup: %v — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, err,
			fallbackSummary(in))
		return
	}
	args := c3types.ReplyArgs{
		Channel: in.Channel,
		ChatID:  in.ChatID,
		TopicID: in.TopicID,
		Text:    fallbackText,
	}
	if _, err := ch.SendReply(args); err != nil {
		log.Printf("fallback FAIL chan=%s chat=%d topic=%s msg=%d: send: %v — %s",
			w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID, err,
			fallbackSummary(in))
		return
	}
	log.Printf("fallback chan=%s chat=%d topic=%s msg=%d: no claim, sent fallback reply — %s",
		w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID,
		fallbackSummary(in))
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

// dispatchOutbound translates an OutboundJob into a channel call, returning
// the result via job.ResultCh.
func (w *RouteWorker) dispatchOutbound(_ context.Context, job *OutboundJob) {
	if job == nil || job.ResultCh == nil {
		return
	}
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
	job.ResultCh <- OutboundResult{Result: result, Err: err}
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
