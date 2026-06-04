package broker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
	"github.com/karthikeyan5/c3/internal/plugin"
	"github.com/karthikeyan5/c3/internal/proctree"
)

// Broker holds the in-memory state shared by all connections: stubs registry,
// routes table, worker pool, channel registry, fallback tracker, and a
// copy-on-write atomic pointer to the mappings.json config snapshot.
//
// Concurrency model for the mappings pointer:
//   - Readers call Mappings() — atomic Load, lock-free, always returns a
//     consistent immutable snapshot.
//   - Writers (UpsertTopic/UpsertMapping during attach, SetMappings on
//     SIGHUP reload) go through mutateMappings(), which holds mutationMu
//     across a Clone → mutate → Store cycle. Concurrent mutations
//     serialize; readers never see a half-updated state.
//
// Previously Mappings was a public *MappingsFile field accessed directly
// by ~20 call sites across attach/handler/worker. Concurrent
// HandleConn goroutines mutated the inner maps via UpsertTopic while
// other goroutines iterated them via range — classic Go data race
// (BLOCKER, code-review 2026-05-15).
type Broker struct {
	Stubs     *StubRegistry
	Routes    *Routes
	Workers   *WorkerPool
	Fallbacks *fallbackTracker
	Plugins   *PluginHost
	Pairing   *pairingState

	mappings   atomic.Pointer[mappings.MappingsFile]
	mutationMu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	chMu     sync.RWMutex
	channels map[string]*channelRegistration

	// sessionPIDResolver maps a registered stub's PID to the real CLI
	// session pid by walking up the /proc tree (defaults to
	// proctree.CLISessionPID). A Claude stub registers under its ADAPTER's
	// pid (comm "c3-claude-adapt"); the slash-command caller resolves the
	// real claude ancestor pid. This resolver bridges that gap in the ping /
	// sessions PID-match. Injectable so handler tests can supply a synthetic
	// process tree without a real /proc.
	sessionPIDResolver func(int) int
}

const defaultWorkerIdle = 60 * time.Second

// New returns a Broker with empty registries and the given mappings config.
func New(mf *mappings.MappingsFile) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broker{
		Stubs:              NewStubRegistry(),
		Routes:             NewRoutes(),
		Fallbacks:          newFallbackTracker(defaultFallbackCooldown),
		Pairing:            newPairingState(),
		ctx:                ctx,
		cancel:             cancel,
		channels:           map[string]*channelRegistration{},
		sessionPIDResolver: proctree.CLISessionPID,
	}
	b.mappings.Store(mf)
	b.Workers = NewWorkerPool(ctx, defaultWorkerIdle, b)
	b.Plugins = newPluginHost(b)
	return b
}

// Mappings returns a read-only snapshot of the current mappings file.
// Always returns a consistent (atomically-loaded) pointer; callers may
// read fields and iterate inner maps without locking. NEVER mutate the
// returned struct — use mutateMappings for that.
func (b *Broker) Mappings() *mappings.MappingsFile {
	return b.mappings.Load()
}

// mutateMappings applies fn to a cloned snapshot and atomically swaps in
// the result. Serializes against other mutations; readers proceed
// concurrently against the old snapshot until the Store completes.
func (b *Broker) mutateMappings(fn func(*mappings.MappingsFile)) {
	b.mutationMu.Lock()
	defer b.mutationMu.Unlock()
	current := b.mappings.Load()
	next := current.Clone()
	fn(next)
	b.mappings.Store(next)
}

// BuiltinPlugin describes one compiled-in plugin: a Name (used for log
// lines and to scope mappings.json:plugins.<name>) and a Register
// function the broker invokes with the plugin Host at startup.
// The broker's main() builds the slice of BuiltinPlugin and passes it
// to RegisterBuiltinPlugins.
type BuiltinPlugin struct {
	Name     string
	Register func(plugin.Host) error
}

// RegisterBuiltinPlugins calls each builtin plugin's Register with the
// broker's plugin host. Should be called by main() after the broker is
// constructed but BEFORE channels are registered (so plugins are subscribed
// before any inbound flows).
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

// Shutdown drains in-flight work and stops all subsystems in the order
// required for clean shutdown:
//
//  1. Stop the worker pool so no new outbound tool-calls dispatch.
//     Workers.Stop() also cancels the per-route worker contexts; any
//     in-flight dispatchOutbound returns from its channel call (or
//     drops out of the queue) before the channel is torn down.
//  2. Stop each channel — by now the worker pool isn't issuing fresh
//     Telegram calls, so Channel.Stop() can drain whatever's already
//     in flight (HTTP requests, polling goroutine) without racing new
//     submissions.
//  3. Cancel the broker context — propagates to any goroutine that
//     was holding a derived context (most are already stopped).
//
// The previous ordering (channels first, then workers) was the bug:
// workers held *Channel refs and called Channel.SendReply mid-flight
// while the channel's Stop was tearing down state, producing 20-second
// hangs on stale request opts timeouts. Addresses code-review-
// 2026-05-15 MAJOR #5 (daemon.md §1.3 drain order).
func (b *Broker) Shutdown() {
	b.Workers.Stop()
	b.chMu.Lock()
	for _, reg := range b.channels {
		_ = reg.Channel.Stop()
	}
	b.chMu.Unlock()
	b.cancel()
}

// SetMappings atomically swaps the in-memory mappings pointer. Called on
// SIGHUP to reload config from disk without restarting the daemon. Holds
// the mutation lock so concurrent UpsertTopic/UpsertMapping calls
// serialize against the swap rather than racing it.
//
// Caveats:
//   - In-flight Upsert mutations that landed BEFORE this call are
//     overwritten by the freshly-loaded file. SIGHUP after a manual
//     mappings.json edit is the expected use case; running it in the
//     middle of an attach race is a user error.
//   - Channel-level config (bot_token, group chat_ids, debounce_ms)
//     is consumed by the Telegram channel ONCE at Start; swapping
//     mappings doesn't re-init the channel. Adding new topics or
//     cwd→topic mappings works fine; changing the bot token requires
//     a full broker restart.
func (b *Broker) SetMappings(mf *mappings.MappingsFile) {
	b.mutationMu.Lock()
	defer b.mutationMu.Unlock()
	b.mappings.Store(mf)
}
