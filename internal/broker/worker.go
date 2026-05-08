package broker

import (
	"context"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
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
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	stopped bool
}

// newRouteWorker starts a worker that runs until ctx is canceled OR no jobs
// arrive within `idle`. Caller observes Done() for completion.
func newRouteWorker(parent context.Context, key RouteKey, idle time.Duration) *RouteWorker {
	ctx, cancel := context.WithCancel(parent)
	w := &RouteWorker{
		key:    key,
		queue:  make(chan Job, 64),
		idle:   idle,
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

// dispatch is the per-job handler. Phase 4A: no plugin chain, no real channel
// hookup. Inbound jobs no-op; outbound jobs return a stub error result.
// Plan 4B/5 wire the real implementations.
func (w *RouteWorker) dispatch(ctx context.Context, job Job) {
	switch job.Kind {
	case JobInbound:
		// Plan 4B/5: STT (OnVoiceReceived), OnInbound chain, debounce window,
		// forward to claimed stub via ipc.OpInbound.
	case JobOutbound:
		if job.Outbound != nil && job.Outbound.ResultCh != nil {
			job.Outbound.ResultCh <- OutboundResult{Err: errOutboundNotImpl}
		}
	case JobRelease:
		// Run loop sees JobRelease and returns; nothing to dispatch here.
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
		// Queue full — drop. Spec §4.4.3: cooldown-fallback handles inbound;
		// for outbound the caller sees no response from ResultCh.
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

// errOutboundNotImpl is the sentinel returned from Phase 4A's stub dispatch.
var errOutboundNotImpl = workerErr("outbound not yet wired (Plan 4B)")

type workerErr string

func (e workerErr) Error() string { return string(e) }
