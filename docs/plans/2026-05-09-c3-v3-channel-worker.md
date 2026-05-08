# C3 v3 Channel + Worker Substrate Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans.

**Goal:** Land the Channel interface, the plugin Host interface, and the per-route serial executor (worker pool). After this plan no Telegram code exists yet, but the substrate is in place: a stub Channel implementation can be plugged in for tests, the broker routes inbound messages through per-route workers, outbound `tool_call` ops dispatch to the right channel, and the type system is ready for Plan 4B (Telegram cleanroom Go).

**Architecture:** Plain Go stdlib. The Channel interface (spec §4.1) is the boundary between the broker and any transport. The Host interface (spec §4.5.1) is what plugins receive. The route worker (spec §4.2.0) is one goroutine per `RouteKey` that owns inbound pipeline + outbound channel calls + per-route mutable state.

**Tech Stack:** Go ≥1.22, stdlib only.

**Spec reference:** §4.1 (channels), §4.2.0 (per-route serial executor), §4.5.1 (plugin host), §4.4.5 (env contract surfacing through host config — but actual reading lives in Plan 4B).

---

### Task 4A.1: Channel + plugin types in `internal/c3types`

**Files:** Create `internal/c3types/types.go`, `internal/c3types/types_test.go`

**Rationale:** spec types (Inbound, Outbound, Sender, Attachment, ReplyContext, VoicePayload, Mapping pointer-typed) are needed by both the channel package and the plugin package. Putting them in a shared `c3types` avoids an import cycle (channel ↔ plugin would otherwise both want them).

- [ ] **Step 1: Write types**

```go
// Package c3types holds wire-shaped Go types shared by channels, plugins,
// and the broker. Spec §4.1.
package c3types

import "time"

type Inbound struct {
	Channel     string
	ChatID      int64
	TopicID     *int64 // nil = no topic, &1 = General, >1 = custom
	MessageID   int64
	Sender      Sender
	Text        string
	Attachments []Attachment
	ReplyTo     *ReplyContext
	Timestamp   time.Time
}

type Sender struct {
	UserID   int64
	Username string
}

type Attachment struct {
	Kind   string // "voice", "audio", "video", "video_note", "document", "photo", "sticker"
	FileID string
	Size   int64
	MIME   string
	Name   string
}

type ReplyContext struct {
	MessageID int64
	User      Sender
	Text      string
}

type Outbound struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	Text      string
	Files     []string
	ParseMode string
	ReplyTo   *int64
}

type ReplyArgs = Outbound

type EditArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Text      string
	ParseMode string
}

type EditResult struct {
	MessageID int64
}

type ReactArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Emoji     string
}

type VoicePayload struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	MessageID int64
	FileID    string
	MIME      string
	Size      int64
}
```

- [ ] **Step 2: Sanity test (type construction smoke)**

```go
package c3types

import "testing"

func TestInboundConstruct(t *testing.T) {
	id := int64(281)
	in := Inbound{
		Channel:   "telegram",
		ChatID:    -100,
		TopicID:   &id,
		MessageID: 868,
		Sender:    Sender{UserID: 42, Username: "x"},
		Text:      "hi",
	}
	if in.TopicID == nil || *in.TopicID != 281 {
		t.Errorf("TopicID round-trip: %+v", in.TopicID)
	}
}
```

- [ ] **Step 3: Build + test**

```bash
go test ./internal/c3types/... -v
```

- [ ] **Step 4: Commit**

```bash
git add internal/c3types/
git commit -m "c3-v3: c3types package — shared Inbound/Outbound/etc types"
```

---

### Task 4A.2: Channel interface

**Files:** Create `internal/channel/channel.go`

```go
// Package channel defines the contract every transport implements.
// Spec §4.1.
package channel

import (
	"context"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Channel is the contract every transport implements. Methods are called by
// the broker on its own goroutine — implementations must be safe for
// concurrent use, except Start/Stop which are sequenced.
type Channel interface {
	Name() string
	Start(ctx context.Context, host Host) error
	Stop() error

	SendReply(args c3types.ReplyArgs) (sentMessageID int64, err error)
	SendTyping(chatID int64, threadID *int64) error
	EditMessage(args c3types.EditArgs) (*c3types.EditResult, error)
	React(args c3types.ReactArgs) error
	DownloadAttachment(fileID string) (path string, err error)

	// Topic management (Telegram-specific in v1; future channels may stub).
	CreateTopic(chatID int64, name string) (topicID int64, err error)
	ValidateTopic(chatID int64, threadID int64) error
}

// Host is what the broker passes to a Channel. Subset of plugin.Host scoped
// to channel concerns (config + emit + log + done).
type Host interface {
	Config(name string, target any) error
	Emit(in *c3types.Inbound)
	Logf(format string, args ...any)
	Done() <-chan struct{}
}
```

- [ ] **Step 1: Write file**
- [ ] **Step 2: `go build ./internal/channel/...`** — succeeds even with no consumers yet.
- [ ] **Step 3: Commit**

```bash
git add internal/channel/channel.go
git commit -m "c3-v3: Channel interface + minimal Host"
```

---

### Task 4A.3: Plugin Host interface (full)

**Files:** Create `internal/plugin/host.go`

```go
// Package plugin defines the broker-side plugin extension API.
// Spec §4.5.1.
package plugin

import (
	"context"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// Host is what plugins receive from the broker.
type Host interface {
	OnInbound(fn func(ctx context.Context, msg *c3types.Inbound) (*c3types.Inbound, bool /*drop*/))
	OnVoiceReceived(fn func(ctx context.Context, payload c3types.VoicePayload) (string, error))
	OnOutbound(fn func(ctx context.Context, msg *c3types.Outbound) (*c3types.Outbound, bool /*drop*/))
	OnAttach(fn func(*Stub, *Mapping))

	RegisterTools(fn func(*ToolRegistry))

	Config(name string, target any) error
	State(name string) StateDir
	CacheDir(name string) string

	Channel(name string) (channel.Channel, error)

	Logf(format string, args ...any)
	Done() <-chan struct{}
}

type Stub struct {
	CLI    string
	PID    int
	CWD    string
	ConnID uint64
}

type Mapping struct {
	Channel string
	ChatID  int64
	TopicID *int64
	Name    string
	Group   string
}

type StateDir interface {
	Load(name string, target any) error
	Save(name string, target any) error
}

type ToolRegistry interface {
	Add(t Tool)
	Remove(name string)
	List() []Tool
}

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args map[string]any) (any, error)
}
```

- [ ] **Step 1: Write file**
- [ ] **Step 2: `go build ./internal/plugin/...`**
- [ ] **Step 3: Commit**

---

### Task 4A.4: Route worker (per-route serial executor)

**Files:** Create `internal/broker/worker.go`, `internal/broker/worker_test.go`

The route worker owns one goroutine per RouteKey. It receives jobs over a channel and drains them in arrival order. Per spec §4.2.0:

- Inbound jobs (from a channel emission, post-debounce)
- Outbound jobs (from `tool_call` ops, dispatched to the channel)
- Control jobs (claim release, idle shutdown)

Phase 4A scope: the worker struct + job types + start/stop + idle-timeout. Inbound and outbound dispatch are stubbed (no plugin chain yet, no real channel — both come in 4B/Plan 5).

```go
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
	// Release jobs have no payload — Kind alone signals.
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

// RouteWorker is the per-route serial executor.
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
// arrive within `idle`. Caller observes done() for completion.
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
		// Reset timer on every loop iteration to give us idle.duration after
		// the most recent activity.
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
			// Idle shutdown.
			return
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
// hookup. Inbound jobs log + drop; outbound jobs return a stub error result.
// Plan 4B/5 wire the real implementations.
func (w *RouteWorker) dispatch(ctx context.Context, job Job) {
	switch job.Kind {
	case JobInbound:
		// Plan 4B/5: STT, OnInbound chain, debounce, forward to claimed stub.
	case JobOutbound:
		if job.Outbound != nil && job.Outbound.ResultCh != nil {
			// Stub: no channel wired, return placeholder error.
			job.Outbound.ResultCh <- OutboundResult{Err: errOutboundNotImpl}
		}
	case JobRelease:
		// Run-loop sees JobRelease and returns; nothing to dispatch here.
	}
}

// Submit enqueues a job. Returns false if the worker is stopped.
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
		// Queue full — drop. Spec §4.4.3 cooldown-fallback would fire on the
		// next inbound; for outbound the caller sees the closed/full channel.
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
```

Tests cover: worker exits on ctx cancel; worker exits on idle; submit returns false post-stop; release-job triggers exit.

```go
package broker

import (
	"context"
	"testing"
	"time"
)

func TestWorker_IdleShutdown(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, 50*time.Millisecond)
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on idle within 500ms")
	}
}

func TestWorker_StopExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour)
	go w.Stop()
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on Stop")
	}
}

func TestWorker_ReleaseJobExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour)
	if !w.Submit(Job{Kind: JobRelease}) {
		t.Fatal("Submit should succeed before stop")
	}
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on JobRelease")
	}
}

func TestWorker_SubmitAfterStopReturnsFalse(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour)
	w.Stop()
	if w.Submit(Job{Kind: JobInbound}) {
		t.Error("Submit after Stop should return false")
	}
}
```

- [ ] **Step 1: Write tests**
- [ ] **Step 2: Run, verify failure**
- [ ] **Step 3: Implement worker**
- [ ] **Step 4: Run, verify pass**
- [ ] **Step 5: Commit**

```bash
git add internal/broker/worker.go internal/broker/worker_test.go
git commit -m "c3-v3: per-route serial executor (worker scaffold; idle/stop/release)"
```

---

### Task 4A.5: WorkerPool managing per-RouteKey workers

**Files:** Create `internal/broker/workers.go`, `internal/broker/workers_test.go`

```go
package broker

import (
	"context"
	"sync"
	"time"
)

// WorkerPool manages route workers keyed by RouteKey. Workers are started
// lazily on first job submission and reaped when they exit.
type WorkerPool struct {
	ctx     context.Context
	cancel  context.CancelFunc
	idle    time.Duration
	mu      sync.Mutex
	workers map[RouteKey]*RouteWorker
	wg      sync.WaitGroup
}

// NewWorkerPool returns a pool with the given idle timeout for new workers.
func NewWorkerPool(parent context.Context, idle time.Duration) *WorkerPool {
	ctx, cancel := context.WithCancel(parent)
	return &WorkerPool{
		ctx:     ctx,
		cancel:  cancel,
		idle:    idle,
		workers: map[RouteKey]*RouteWorker{},
	}
}

// Submit enqueues job for the given route key, starting a worker if none
// exists. Returns false if the pool is stopped or the worker queue is full.
func (p *WorkerPool) Submit(key RouteKey, job Job) bool {
	p.mu.Lock()
	w, ok := p.workers[key]
	if !ok {
		w = newRouteWorker(p.ctx, key, p.idle)
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
```

Tests: workers spawn lazily, idle workers reap, Stop drains all.

```go
package broker

import (
	"context"
	"testing"
	"time"
)

func TestWorkerPool_LazySpawnAndReap(t *testing.T) {
	pool := NewWorkerPool(context.Background(), 30*time.Millisecond)
	defer pool.Stop()

	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0", pool.Active())
	}

	pool.Submit(RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 281}, Job{Kind: JobInbound})

	if pool.Active() != 1 {
		t.Errorf("active=%d, want 1 after submit", pool.Active())
	}

	// Wait long enough for idle reap.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && pool.Active() > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0 after idle reap", pool.Active())
	}
}

func TestWorkerPool_StopDrains(t *testing.T) {
	pool := NewWorkerPool(context.Background(), time.Hour)
	for i := 0; i < 5; i++ {
		pool.Submit(RouteKey{Channel: "telegram", ChatID: int64(i)}, Job{Kind: JobInbound})
	}
	if pool.Active() == 0 {
		t.Fatal("expected workers active after submits")
	}
	pool.Stop()
	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0 after Stop", pool.Active())
	}
}
```

- [ ] **Step 1-5: TDD cycle**

```bash
git add internal/broker/workers.go internal/broker/workers_test.go
git commit -m "c3-v3: WorkerPool — lazy per-route worker spawn + idle reap"
```

---

### Task 4A.6: Wire WorkerPool into Broker

**Files:** Modify `internal/broker/broker.go`

```go
package broker

import (
	"context"
	"time"

	"github.com/karthikeyan5/c3/internal/mappings"
)

type Broker struct {
	Mappings *mappings.MappingsFile
	Stubs    *StubRegistry
	Routes   *Routes
	Workers  *WorkerPool

	ctx    context.Context
	cancel context.CancelFunc
}

const defaultWorkerIdle = 60 * time.Second

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
```

Update `cmd/c3-broker/main.go` to call `br.Shutdown()` after `srv.Stop()`.

- [ ] **Step 1: Write modifications**
- [ ] **Step 2: Build all + run all tests**
- [ ] **Step 3: Commit**

```bash
git add internal/broker/broker.go cmd/c3-broker/main.go
git commit -m "c3-v3: wire WorkerPool into Broker; main calls Shutdown"
```

---

### Task 4A.7: Final integration check

- [ ] `make build` produces all binaries.
- [ ] `go test ./...` — every test passes.
- [ ] Live broker still starts and stops cleanly (smoke test from Phase 3 still works).

---

## Out of scope for this plan

- **Plan 4B:** Telegram channel cleanroom Go (gotgbot/v2). Implements the Channel interface from §4.1, getUpdates loop, inbound emission via Host.Emit, outbound tools, createForumTopic, sendChatAction-as-validate, debounce window with cap, cooldown-fallback reply. Wires the Telegram channel into the broker on startup.
- **Plan 5:** Plugin host concrete implementation + STT plugin. Implements the Host interface declared here, builtin plugin registry, hook firing order, STT shim with `//go:embed handler.py`.
- **Plan 6:** Claude Code adapter. MCP stdio server, manual JSON-RPC framing for `notifications/claude/channel`, adapter-local attach/topics, tool list aggregation.
- **Plan 7:** Codex bridge in Go (launcher + adapter + install-codex-shim).
- **Plan 8:** Attach proposal flow integration (cross-group search, validate-by-id, create+register), debounce + dedup, typing indicator, edit_progress placeholder lifecycle. Some of this folds into Plan 4B; the proposal flow specifically depends on Channel.CreateTopic + Channel.ValidateTopic being implemented.
- **Plan 9:** /c3-setup, /c3-build, /c3-status slash commands; Codex SETUP.md.
- **Plan 10:** README/INSTALL rewrite, deviation banner retirement, public release tag.
