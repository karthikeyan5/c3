package broker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// Broker holds the in-memory state shared by all connections: stubs registry,
// routes table, worker pool, channel registry, fallback tracker, and a
// snapshot of the mappings.json config.
type Broker struct {
	Mappings  *mappings.MappingsFile
	Stubs     *StubRegistry
	Routes    *Routes
	Workers   *WorkerPool
	Fallbacks *fallbackTracker

	ctx    context.Context
	cancel context.CancelFunc

	chMu     sync.RWMutex
	channels map[string]*channelRegistration
}

const defaultWorkerIdle = 60 * time.Second

// New returns a Broker with empty registries and the given mappings config.
func New(mf *mappings.MappingsFile) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broker{
		Mappings:  mf,
		Stubs:     NewStubRegistry(),
		Routes:    NewRoutes(),
		Fallbacks: newFallbackTracker(defaultFallbackCooldown),
		ctx:       ctx,
		cancel:    cancel,
		channels:  map[string]*channelRegistration{},
	}
	b.Workers = NewWorkerPool(ctx, defaultWorkerIdle, b)
	return b
}

// RegisterChannel adds a channel to the broker. The channel is started
// (which validates config and connects to the upstream API) before the
// registration is recorded — if Start fails, no registration happens.
func (b *Broker) RegisterChannel(ch channel.Channel) error {
	host := NewBrokerHost(b, ch.Name())
	if err := ch.Start(b.ctx, host); err != nil {
		return fmt.Errorf("broker: start channel %q: %w", ch.Name(), err)
	}
	b.chMu.Lock()
	b.channels[ch.Name()] = &channelRegistration{Channel: ch, Host: host}
	b.chMu.Unlock()
	return nil
}

// Channel returns the registered channel implementation by name.
func (b *Broker) Channel(name string) (channel.Channel, error) {
	b.chMu.RLock()
	defer b.chMu.RUnlock()
	reg, ok := b.channels[name]
	if !ok {
		return nil, fmt.Errorf("broker: channel %q not registered", name)
	}
	return reg.Channel, nil
}

// Channels returns the names of all registered channels (diagnostic).
func (b *Broker) Channels() []string {
	b.chMu.RLock()
	defer b.chMu.RUnlock()
	out := make([]string, 0, len(b.channels))
	for name := range b.channels {
		out = append(out, name)
	}
	return out
}

// Shutdown stops all channels, the worker pool, and signals the broker ctx.
// Order: channels first (so they can drain in-flight HTTP), then workers
// (so any tool-call in flight has its channel still alive), then ctx cancel.
func (b *Broker) Shutdown() {
	b.chMu.Lock()
	for _, reg := range b.channels {
		_ = reg.Channel.Stop()
	}
	b.chMu.Unlock()
	b.Workers.Stop()
	b.cancel()
}
