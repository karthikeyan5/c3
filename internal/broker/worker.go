package broker

import (
	"context"
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

// newRouteWorker starts a worker that runs until ctx is canceled OR no jobs
// arrive within `idle`. Caller observes Done() for completion.
//
// broker may be nil for unit tests that exercise idle/stop/release behavior
// without depending on broker state.
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
	timer := time.NewTimer(w.idle)
	defer timer.Stop()

	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.idle)

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return // idle shutdown
		case job, ok := <-w.queue:
			if !ok {
				return
			}
			w.dispatch(ctx, job)
			if job.Kind == JobRelease {
				return
			}
		}
	}
}

// dispatch is the per-job handler. Implements the per-route serial
// invariant from spec §4.2.0.
func (w *RouteWorker) dispatch(ctx context.Context, job Job) {
	switch job.Kind {
	case JobInbound:
		w.dispatchInbound(ctx, job.Inbound)
	case JobOutbound:
		w.dispatchOutbound(ctx, job.Outbound)
	case JobRelease:
		// Run loop sees JobRelease and returns; nothing to dispatch here.
	}
}

// dispatchInbound forwards a normalized inbound to the claimed stub, or fires
// the cooldown-fallback reply if the route is unclaimed.
//
// Phase 4B scope: no plugin chain (Plan 5). The inbound flows directly from
// the channel into the claimed adapter, or into the fallback reply.
func (w *RouteWorker) dispatchInbound(ctx context.Context, in *c3types.Inbound) {
	if w.broker == nil || in == nil {
		return
	}

	holder, claimed := w.broker.Routes.Holder(w.key)
	if claimed {
		// Forward to the claimed stub.
		conn, ok := holder.Conn.(*ipc.Conn)
		if !ok {
			return // stub didn't store an *ipc.Conn — shouldn't happen
		}
		_ = conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: *in})
		return
	}

	// No claim → cooldown-fallback.
	if !w.broker.Fallbacks.ShouldSend(w.key) {
		return
	}
	ch, err := w.broker.Channel(in.Channel)
	if err != nil {
		return
	}
	args := c3types.ReplyArgs{
		Channel: in.Channel,
		ChatID:  in.ChatID,
		TopicID: in.TopicID,
		Text:    fallbackText,
	}
	if _, err := ch.SendReply(args); err != nil {
		// Don't reset the cooldown on failure — Telegram could be down. Just
		// drop and let the next inbound try again after the window.
		return
	}
}

// dispatchOutbound translates an OutboundJob into a channel call, returning
// the result via job.ResultCh. The channel is resolved from the worker's
// route key (which carries the channel name).
func (w *RouteWorker) dispatchOutbound(ctx context.Context, job *OutboundJob) {
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

// errOutboundNotImpl is returned when the worker has no broker (test-only path).
var errOutboundNotImpl = workerErr("worker has no broker reference")

type workerErr string

func (e workerErr) Error() string { return string(e) }
