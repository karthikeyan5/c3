package broker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
	"github.com/karthikeyan5/c3/internal/plugin"
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
	Plugins   *PluginHost

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
	b.Plugins = newPluginHost(b)
	return b
}

// RegisterBuiltinPlugins calls each builtin plugin's Register with the
// broker's plugin host. Should be called by main() after the broker is
// constructed but BEFORE channels are registered (so plugins are subscribed
// before any inbound flows).
type BuiltinPlugin struct {
	Name     string
	Register func(plugin.Host) error
}

func (b *Broker) RegisterBuiltinPlugins(builtins []BuiltinPlugin) error {
	for _, bp := range builtins {
		if err := bp.Register(b.Plugins); err != nil {
			return fmt.Errorf("plugin %s: %w", bp.Name, err)
		}
	}
	return nil
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

// SetMappings atomically swaps the in-memory mappings pointer. Called on
// SIGHUP to reload config from disk without restarting the daemon. Pointer
// assignment on every architecture Go supports is a single instruction,
// so concurrent readers never see a torn value — they see either the old
// pointer or the new pointer.
//
// Caveats:
//   - In-flight writes to the OLD pointer (UpsertTopic / UpsertMapping in
//     attach handlers) won't be reflected in the NEW pointer. SIGHUP after
//     a manual mappings.json edit is the expected use case; running it
//     in the middle of an attach race is a user error.
//   - Channel-level config (bot_token, group chat_ids, debounce_ms)
//     is consumed by the Telegram channel ONCE at Start; swapping
//     mappings doesn't re-init the channel. Adding new topics or
//     cwd→topic mappings works fine; changing the bot token requires
//     a full broker restart.
func (b *Broker) SetMappings(mf *mappings.MappingsFile) {
	b.Mappings = mf
}
