package broker

import (
	"context"
	"sync"
	"time"
)

// WorkerPool manages route workers keyed by RouteKey. Workers are started
// lazily on first job submission and reaped when they exit (idle timeout
// or release/stop). Each worker holds a back-reference to the broker so it
// can look up routes, channels, and fire fallbacks during dispatch.
type WorkerPool struct {
	ctx     context.Context
	cancel  context.CancelFunc
	idle    time.Duration
	broker  *Broker
	mu      sync.Mutex
	workers map[RouteKey]*RouteWorker
	wg      sync.WaitGroup
}

// NewWorkerPool returns a pool with the given idle timeout for new workers.
// The broker reference is used by the worker dispatch path to resolve
// claims, channels, and fallback state.
func NewWorkerPool(parent context.Context, idle time.Duration, broker *Broker) *WorkerPool {
	ctx, cancel := context.WithCancel(parent)
	return &WorkerPool{
		ctx:     ctx,
		cancel:  cancel,
		idle:    idle,
		broker:  broker,
		workers: map[RouteKey]*RouteWorker{},
	}
}

// Submit enqueues job for the given route key, starting a worker if none
// exists. Returns false if the pool is stopped or the worker queue is full.
func (p *WorkerPool) Submit(key RouteKey, job Job) bool {
	p.mu.Lock()
	w, ok := p.workers[key]
	if !ok {
		w = newRouteWorker(p.ctx, key, p.idle, p.broker)
		p.workers[key] = w
		p.wg.Add(1)
		go func() {
			<-w.Done()
			p.mu.Lock()
			delete(p.workers, key)
			p.mu.Unlock()
			p.wg.Done()
		}()
	}
	p.mu.Unlock()
	return w.Submit(job)
}

// Stop signals all workers to exit and waits for them.
func (p *WorkerPool) Stop() {
	p.cancel()
	p.wg.Wait()
}

// Active returns the number of currently-running workers (diagnostic).
func (p *WorkerPool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}
