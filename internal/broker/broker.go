package broker

import (
	"context"
	"time"

	"github.com/karthikeyan5/c3/internal/mappings"
)

// Broker holds the in-memory state shared by all connections: stubs registry,
// routes table, worker pool, and a snapshot of the mappings.json config.
//
// Phase 3 scope: read-only mappings. Phase 4A: per-route worker pool.
// Channel layer + plugin chain land in Plan 4B / Plan 5.
type Broker struct {
	Mappings *mappings.MappingsFile
	Stubs    *StubRegistry
	Routes   *Routes
	Workers  *WorkerPool

	ctx    context.Context
	cancel context.CancelFunc
}

const defaultWorkerIdle = 60 * time.Second

// New returns a Broker with empty registries and the given mappings config.
func New(mf *mappings.MappingsFile) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Broker{
		Mappings: mf,
		Stubs:    NewStubRegistry(),
		Routes:   NewRoutes(),
		Workers:  NewWorkerPool(ctx, defaultWorkerIdle),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Shutdown stops the worker pool and any background goroutines.
func (b *Broker) Shutdown() {
	b.Workers.Stop()
	b.cancel()
}
