# C3 Durable Inbound Queue + Backlog Delivery — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Once C3 has received an inbound Telegram message, never lose it. Messages that arrive while no CLI session is attached (or while the broker was down and later catches up) are held durably on disk and delivered — with an agent-visible backlog notification + pull — when a session attaches. Both the Claude and Codex adapters reach parity. The human never has to re-forward.

**Architecture:** A new broker-side **durable, per-route, append-only on-disk JSONL queue** (`internal/queue`) becomes the holding buffer for every inbound. All file I/O for a route is funneled through that route's existing `RouteWorker` goroutine (single owner ⇒ no file locks), extended with `JobFetch` / `JobConsume` job kinds. The Telegram offset advances only to the highest **contiguous** `update_id` whose message has been durably persisted (a new persisted-offset tracker). Live attached sessions still receive messages by immediate push (lifecycle model **B**); messages with no live consumer accumulate in the queue and are delivered on attach via a compact backlog summary + an agent-driven `fetch_queue` pull tool. A `retranscribe` tool re-runs STT on a cached `file_id`. A `/status` Telegram bot command is intercepted before gating/routing. Stdlib-only.

**Tech Stack:** Go (module `github.com/karthikeyan5/c3`), standard library only. Existing length-prefixed JSON IPC over the Unix socket. JSONL on disk under `$XDG_STATE_HOME/c3/queue/`. No third-party libraries.

## Global Constraints

- **No new third-party dependencies** — stdlib only (no SQLite, no embedded KV). Same no-extra-dependency rule Karthi set for the native-Sarvam work.
- **Both adapters reach parity** for: durable delivery, `fetch_queue`, `retranscribe`. Divergence is allowed only where it matches each agent's native consumption model (see Delivery).
- **No silent truncation.** Any cap/drop emits a broker.log line *and* a Telegram notice in the affected topic.
- **Never auto-switch output mode.** Out of scope here, but unchanged.
- **Keep-out values** (proxy subdomains, GCP project, region, static IP) never enter the public repo. This plan uses placeholders only; none are needed here.
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

Additional operational constraints:

- Module path `github.com/karthikeyan5/c3`; all `go test` commands run from repo root `/home/karthi/arogara/c3`.
- Follow existing code patterns (table tests, `t.TempDir()`, `t.Setenv`, metadata-only logging per DEBUGGING.md — never log message text on success paths). TDD: failing test → minimal impl → green. Commit after each task.
- **Do NOT run `git commit` while authoring the plan.** During *execution*, commit each task with the verbatim trailer above.
- Queue dir: `$XDG_STATE_HOME/c3/queue/` (fallback `~/.local/state/c3/queue/`), resolved next to the existing offset store (`telegram/offset_store.go:xdgStateHomeC3()`). Env `C3_QUEUE_DIR` overrides (tests). Dir mode `0700`, file mode `0600`.
- Cursor (`.cur`) = **line number** (1-based count of consumed lines), human-readable.
- Caps: **1000 messages OR 14 days**, whichever first, drop-oldest, never silent.
- Held-reply cadence: reuse the existing 5-minute `fallbackTracker` cooldown.
- `fetch_queue`: `limit` default **3** (also accepts the string `"all"`), max 50; `ack` default **true**.
- `retranscribe`: `file_id` required, `message_id` optional.

---

## File Structure

**Created**

| File | Responsibility |
| --- | --- |
| `internal/queue/store.go` | `Store` — per-route append-only JSONL queue + line-number cursor; `Append/Peek/Consume/Pending/StatusAll/EvictOverCap/RecoverOnStartup`; delete-on-empty; caps. |
| `internal/queue/store_test.go` | Unit + `-race` tests for the store. |
| `internal/queue/paths.go` | `routeKeyFile()` filesystem-safe encoding of a route + `QueueDir()` path resolver. |
| `internal/queue/paths_test.go` | Encoding + path-resolution tests. |
| `internal/channel/telegram/offset_tracker.go` | `offsetTracker` — contiguous-prefix persisted-offset advancer. |
| `internal/channel/telegram/offset_tracker_test.go` | Out-of-order / gated / crash-before-persist tests. |
| `internal/broker/queue_dispatch.go` | Broker handlers for `OpFetchQueue` + `OpRetranscribe`; worker `JobFetch`/`JobConsume` plumbing. |
| `internal/broker/queue_dispatch_test.go` | Fetch/consume/retranscribe handler tests. |
| `internal/broker/status_command.go` | `BrokerHost.HandleCommand` `/status` renderer (in-topic + global). |
| `internal/broker/status_command_test.go` | `/status` rendering tests. |

**Modified**

| File | Responsibility |
| --- | --- |
| `internal/ipc/ops.go` | New op constants: `OpFetchQueue`, `OpFetchQueueResult`, `OpInboundDelivered`, `OpRetranscribe`, `OpRetranscribeResult`. |
| `internal/ipc/messages.go` | `FetchQueueReq/Resp`, `InboundDeliveredMsg`, `RetranscribeReq/Resp`, `QueuedItem`; `AttachedMsg.QueuedCount` + `.QueuedSummary`. |
| `internal/broker/broker.go` | Hold a `*queue.Store`; construct + `RecoverOnStartup` at `New`. |
| `internal/broker/worker.go` | `JobFetch`/`JobConsume` kinds; append+fsync before delivery; per-adapter delivered semantics; held-count auto-reply; STT self-documenting failure text. |
| `internal/broker/host.go` | `Emit` enqueues; `channel.Host.HandleCommand` plumbing. |
| `internal/broker/attach.go` | Backlog summary (`QueuedCount`+`QueuedSummary`) in the attach response. |
| `internal/broker/handler.go` | Route `OpFetchQueue`/`OpRetranscribe`/`OpInboundDelivered` ops. |
| `internal/broker/fallback.go` | `heldReplyText(n)` running-count auto-reply text. |
| `internal/channel/channel.go` | Add `HandleCommand(in) (reply string, handled bool)` to `Host`. |
| `internal/channel/telegram/poll.go` | `/status` intercept before gating; offset advance via `offsetTracker`. |
| `internal/channel/telegram/telegram.go` | `setMyCommands` registration at `Start`; hold the `offsetTracker`. |
| `cmd/c3-claude-adapter/main.go` | `fetch_queue` + `retranscribe` MCP tools; `OpInboundDelivered` ack on push accept; backlog-summary rendering; recovery nudge. |
| `cmd/c3-codex-adapter/main.go` | `inbox`→broker-backed `fetch_queue`; retire in-memory ring; `retranscribe`; "N pending" nudge. |
| `docs/USAGE.md` | Document the queue, `/status`, `fetch_queue`, `retranscribe`. |
| `docs/CHANNELS.md` | Document Telegram `/status` command + durable-queue behavior. |
| `ROADMAP.md` | Mark the durable-inbound-queue line shipped. |

---

### Task 1: IPC ops + message structs

**Files:**
- Modify: `internal/ipc/ops.go` (5 new op constants)
- Modify: `internal/ipc/messages.go` (request/response/event structs + attach-resp extension)
- Test: `internal/ipc/messages_test.go` (append round-trip tests)

**Interfaces:**
- Produces (consumed by Tasks 4, 6, 7, 9, 10):
  - `ipc.OpFetchQueue Op = "fetch_queue"`, `ipc.OpFetchQueueResult Op = "fetch_queue_result"`
  - `ipc.OpInboundDelivered Op = "inbound_delivered"`
  - `ipc.OpRetranscribe Op = "retranscribe"`, `ipc.OpRetranscribeResult Op = "retranscribe_result"`
  - `ipc.FetchQueueReq{Op Op; ID string; Limit int; All bool; Ack bool}`
  - `ipc.FetchQueueResp{Op Op; ID string; Messages []c3types.Inbound; Remaining int; Err string}`
  - `ipc.InboundDeliveredMsg{Op Op; UpdateID int64; OK bool}`
  - `ipc.RetranscribeReq{Op Op; ID string; FileID string; MessageID int64}`
  - `ipc.RetranscribeResp{Op Op; ID string; Text string; Err string}`
  - `ipc.QueuedItem{MessageID int64; Sender string; Kind string; Unix int64; Preview string}`
  - `ipc.AttachedMsg.QueuedCount int` + `ipc.AttachedMsg.QueuedSummary []QueuedItem`

- [ ] **Step 1: Write the failing test** — append to `internal/ipc/messages_test.go`:

```go
func TestFetchQueueReqRoundTrip(t *testing.T) {
	req := FetchQueueReq{Op: OpFetchQueue, ID: "7", Limit: 3, All: false, Ack: true}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	op, err := PeekOp(data)
	if err != nil || op != OpFetchQueue {
		t.Fatalf("PeekOp = %q,%v; want fetch_queue", op, err)
	}
	var got FetchQueueReq
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Limit != 3 || got.Ack != true || got.All != false || got.ID != "7" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestFetchQueueRespCarriesInbound(t *testing.T) {
	resp := FetchQueueResp{
		Op: OpFetchQueueResult, ID: "7", Remaining: 2,
		Messages: []c3types.Inbound{{Channel: "telegram", ChatID: -100, MessageID: 5, Text: "hi"}},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var got FetchQueueResp
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Text != "hi" || got.Remaining != 2 {
		t.Errorf("resp round-trip mismatch: %+v", got)
	}
}

func TestInboundDeliveredAndRetranscribeRoundTrip(t *testing.T) {
	d, _ := json.Marshal(InboundDeliveredMsg{Op: OpInboundDelivered, UpdateID: 42, OK: true})
	if op, _ := PeekOp(d); op != OpInboundDelivered {
		t.Fatalf("delivered op = %q", op)
	}
	r, _ := json.Marshal(RetranscribeReq{Op: OpRetranscribe, ID: "9", FileID: "vf", MessageID: 5})
	if op, _ := PeekOp(r); op != OpRetranscribe {
		t.Fatalf("retranscribe op = %q", op)
	}
	var gr RetranscribeReq
	if err := json.Unmarshal(r, &gr); err != nil {
		t.Fatal(err)
	}
	if gr.FileID != "vf" || gr.MessageID != 5 {
		t.Errorf("retranscribe req mismatch: %+v", gr)
	}
}

func TestAttachedMsgCarriesBacklog(t *testing.T) {
	m := AttachedMsg{
		Op: OpAttached, OK: true, QueuedCount: 2,
		QueuedSummary: []QueuedItem{{MessageID: 5, Sender: "@k", Kind: "text", Unix: 1718722680, Preview: "hi"}},
	}
	data, _ := json.Marshal(m)
	var got AttachedMsg
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.QueuedCount != 2 || len(got.QueuedSummary) != 1 || got.QueuedSummary[0].Preview != "hi" {
		t.Errorf("backlog round-trip mismatch: %+v", got)
	}
}
```

(`internal/ipc/messages_test.go` already declares `package ipc` and imports `encoding/json`, `testing`; ensure the import block also has `"github.com/karthikeyan5/c3/internal/c3types"` — add it if missing.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ipc/ -run 'FetchQueue|InboundDelivered|Retranscribe|AttachedMsgCarriesBacklog' -v`
Expected: FAIL — undefined `OpFetchQueue`, `FetchQueueReq`, `QueuedItem`, `AttachedMsg.QueuedCount`, etc.

- [ ] **Step 3: Add the op constants** — in `internal/ipc/ops.go`, add to the `adapter → broker` group:

```go
	OpFetchQueue      Op = "fetch_queue"
	OpInboundDelivered Op = "inbound_delivered"
	OpRetranscribe    Op = "retranscribe"
```

and to the `broker → adapter` group:

```go
	OpFetchQueueResult   Op = "fetch_queue_result"
	OpRetranscribeResult Op = "retranscribe_result"
```

- [ ] **Step 4: Add the structs** — in `internal/ipc/messages.go`, after `ToolResultMsg` add:

```go
// FetchQueueReq is the adapter → broker pull of held inbound for the stub's
// claimed route. Limit caps the batch (default applied by the adapter: 3, max
// 50); All=true overrides Limit and drains everything. Ack=true consumes
// (advances the cursor, deletes the files when drained); Ack=false peeks.
type FetchQueueReq struct {
	Op    Op     `json:"op"` // = OpFetchQueue
	ID    string `json:"id"`
	Limit int    `json:"limit,omitempty"`
	All   bool   `json:"all,omitempty"`
	Ack   bool   `json:"ack"`
}

// FetchQueueResp is the broker → adapter response to FetchQueueReq. Messages
// are the oldest up-to-Limit (or all) held inbound with full content; Remaining
// is the count still queued after this batch. Err is set (and Messages nil) on
// failure (e.g. no route claimed).
type FetchQueueResp struct {
	Op        Op                `json:"op"` // = OpFetchQueueResult
	ID        string            `json:"id"`
	Messages  []c3types.Inbound `json:"messages,omitempty"`
	Remaining int               `json:"remaining"`
	Err       string            `json:"err,omitempty"`
}

// InboundDeliveredMsg is the Claude adapter → broker live-push ack. The broker
// Consumes the queued line(s) the push covered only after OK=true, so a push the
// adapter never accepted stays queued (backlog + recovery nudge). OK=false is a
// reported failure (the broker leaves it queued and may retry). Count is the
// number of durable queue lines this (possibly merged) push covered — the
// adapter echoes InboundMsg.Covered back so the broker Consumes exactly that many
// off the head (a merged batch of N must drop N lines, not 1). Count<=0 is
// treated as 1.
type InboundDeliveredMsg struct {
	Op       Op    `json:"op"` // = OpInboundDelivered
	UpdateID int64 `json:"update_id"`
	OK       bool  `json:"ok"`
	Count    int   `json:"count,omitempty"`
}

// RetranscribeReq is the adapter → broker request to re-run the STT chain over a
// cached voice attachment by FileID. MessageID is optional: when the matching
// message is still queued, its stored Text is refreshed in place.
type RetranscribeReq struct {
	Op        Op     `json:"op"` // = OpRetranscribe
	ID        string `json:"id"`
	FileID    string `json:"file_id"`
	MessageID int64  `json:"message_id,omitempty"`
}

// RetranscribeResp is the broker → adapter response to RetranscribeReq. Text is
// the fresh transcript; Err is set (Text empty) when the provider chain still
// fails.
type RetranscribeResp struct {
	Op   Op     `json:"op"` // = OpRetranscribeResult
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
	Err  string `json:"err,omitempty"`
}

// QueuedItem is one compact backlog-summary row carried in AttachedMsg. Preview
// is a short, truncated text snippet (never the full body); Unix is the
// message's timestamp.
type QueuedItem struct {
	MessageID int64  `json:"message_id"`
	Sender    string `json:"sender,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Unix      int64  `json:"unix,omitempty"`
	Preview   string `json:"preview,omitempty"`
}
```

Then extend `AttachedMsg` (add the two fields just before its closing brace):

```go
	// QueuedCount is the number of held inbound waiting on the just-claimed
	// route at attach time; QueuedSummary is a compact preview of the oldest few
	// (the adapter renders it and instructs the agent to call fetch_queue).
	// Additive + omitempty: zero/nil for an empty queue and for older brokers.
	QueuedCount   int          `json:"queued_count,omitempty"`
	QueuedSummary []QueuedItem `json:"queued_summary,omitempty"`
```

Also extend the existing `InboundMsg` (the live push frame) with two additive fields. Add just before `InboundMsg`'s closing brace:

```go
	// Pending is the number of messages STILL queued for this route AFTER the
	// lines this push covered (i.e. backlog the live push did not cover). The
	// Claude adapter appends a "(N pending — call fetch_queue)" recovery nudge to
	// the push when Pending > 0, so a stuck backlog item is surfaced on the next
	// successful push — not only at the next re-attach.
	Pending int `json:"pending,omitempty"`
	// Covered is the number of durable queue lines this (possibly MERGED) push
	// covers. A debounced batch of N stored lines is delivered as ONE merged
	// notification; the adapter echoes Covered back in InboundDeliveredMsg.Count
	// so the broker Consumes exactly those N lines on ack (not just 1, which
	// would orphan N-1 as phantom backlog). Defaults to 1 (single-message push /
	// older brokers).
	Covered int `json:"covered,omitempty"`
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/ipc/ -v`
Expected: PASS (new tests + existing `internal/ipc` tests).

- [ ] **Step 6: Commit**

```bash
git add internal/ipc/ops.go internal/ipc/messages.go internal/ipc/messages_test.go
git commit -m "$(printf 'feat(ipc): add fetch_queue, inbound_delivered, retranscribe ops + attach backlog fields\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 2: `internal/queue` package — durable per-route store

**Files:**
- Create: `internal/queue/paths.go`
- Create: `internal/queue/store.go`
- Test: `internal/queue/paths_test.go`
- Test: `internal/queue/store_test.go`

**Interfaces:**
- Consumes: `c3types.Inbound` (existing).
- Produces (consumed by Tasks 3, 4, 6, 7, 8):
  - `queue.QueueDir() string` — `$C3_QUEUE_DIR` override, else `$XDG_STATE_HOME/c3/queue`, else `~/.local/state/c3/queue`.
  - `queue.RouteKey{Channel string; ChatID int64; TopicID *int64}` + `(RouteKey).File() string` (filesystem-safe basename without extension).
  - `queue.Status{Pending int; OldestUnix int64}`
  - `queue.NewStore(dir string) (*Store, error)`
  - `(*Store).Append(rk RouteKey, in *c3types.Inbound) error`
  - `(*Store).Peek(rk RouteKey, n int) ([]c3types.Inbound, error)`
  - `(*Store).Consume(rk RouteKey, n int) ([]c3types.Inbound, error)`
  - `(*Store).Pending(rk RouteKey) (int, time.Time)`
  - `(*Store).StatusAll() map[RouteKey]Status`
  - `(*Store).EvictOverCap(rk RouteKey) (dropped int, err error)`
  - `(*Store).RecoverOnStartup() error`
  - `queue.MaxMessages = 1000`, `queue.MaxAge = 14 * 24 * time.Hour`

- [ ] **Step 1: Write the failing path tests** — create `internal/queue/paths_test.go`:

```go
package queue

import (
	"path/filepath"
	"testing"
)

func TestRouteKeyFile_TopicAndDM(t *testing.T) {
	tid := int64(914)
	withTopic := RouteKey{Channel: "telegram", ChatID: -1003990699908, TopicID: &tid}.File()
	if withTopic != "telegram__-1003990699908__914" {
		t.Errorf("topic file = %q, want telegram__-1003990699908__914", withTopic)
	}
	dm := RouteKey{Channel: "telegram", ChatID: 12345, TopicID: nil}.File()
	if dm != "telegram__12345__none" {
		t.Errorf("dm file = %q, want telegram__12345__none", dm)
	}
}

func TestQueueDir_EnvOverrideAndXDG(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", "/custom/q")
	if got := QueueDir(); got != "/custom/q" {
		t.Errorf("override QueueDir = %q, want /custom/q", got)
	}
	t.Setenv("C3_QUEUE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xs")
	if got := QueueDir(); got != filepath.Join("/tmp/xs", "c3", "queue") {
		t.Errorf("xdg QueueDir = %q", got)
	}
}
```

- [ ] **Step 2: Run the path tests to verify they fail**

Run: `go test ./internal/queue/ -run 'RouteKeyFile|QueueDir' -v`
Expected: FAIL — package `queue` does not compile (`RouteKey`, `QueueDir` undefined).

- [ ] **Step 3: Implement paths** — create `internal/queue/paths.go`:

```go
// Package queue is C3's durable, per-route, append-only on-disk inbound queue.
// Every received Telegram inbound is persisted here (one JSONL line per message)
// before its update_id becomes eligible to advance the Telegram offset, so an
// accepted-but-undelivered message is never lost. The store is single-owner: all
// file operations for a route are funneled through that route's RouteWorker
// goroutine in the broker, so it holds no per-file locks.
package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Caps — never silent (the broker logs + sends a Telegram notice on overflow).
const (
	// MaxMessages is the per-route line cap; EvictOverCap drops oldest beyond it.
	MaxMessages = 1000
	// MaxAge is the per-route age cap; EvictOverCap drops lines older than this.
	MaxAge = 14 * 24 * time.Hour
)

// RouteKey identifies one queued route. TopicID nil = DM / no topic.
type RouteKey struct {
	Channel string
	ChatID  int64
	TopicID *int64
}

// File returns the filesystem-safe basename (no extension) for this route:
// "<channel>__<chat_id>__<topic|none>". The store appends ".jsonl"/".cur".
func (rk RouteKey) File() string {
	topic := "none"
	if rk.TopicID != nil {
		topic = fmt.Sprintf("%d", *rk.TopicID)
	}
	return fmt.Sprintf("%s__%d__%s", rk.Channel, rk.ChatID, topic)
}

// QueueDir resolves the queue directory: $C3_QUEUE_DIR (override, tests), else
// $XDG_STATE_HOME/c3/queue, else ~/.local/state/c3/queue. Mirrors the offset
// store's XDG convention so queue files sit beside <channel>-offset.json.
func QueueDir() string {
	if env := os.Getenv("C3_QUEUE_DIR"); env != "" {
		return env
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "c3", "queue")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "c3", "queue")
}
```

- [ ] **Step 4: Write the failing store tests** — create `internal/queue/store_test.go`:

```go
package queue

import (
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	s, err := NewStore(QueueDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func msg(id int64, text string) *c3types.Inbound {
	return &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: id, Text: text, Timestamp: time.Now()}
}

func TestAppendPeekConsumeAndDeleteOnEmpty(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	for i := int64(1); i <= 3; i++ {
		if err := s.Append(rk, msg(i, "m")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Peek does not advance.
	peek, err := s.Peek(rk, 2)
	if err != nil || len(peek) != 2 || peek[0].MessageID != 1 {
		t.Fatalf("peek = %+v err=%v", peek, err)
	}
	if n, _ := s.Pending(rk); n != 3 {
		t.Fatalf("pending after peek = %d, want 3", n)
	}
	// Consume advances.
	got, err := s.Consume(rk, 2)
	if err != nil || len(got) != 2 || got[1].MessageID != 2 {
		t.Fatalf("consume = %+v err=%v", got, err)
	}
	if n, _ := s.Pending(rk); n != 1 {
		t.Fatalf("pending after consume = %d, want 1", n)
	}
	// Drain the rest → files deleted.
	if _, err := s.Consume(rk, 10); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Pending(rk); n != 0 {
		t.Fatalf("pending after drain = %d, want 0", n)
	}
	// A fresh append after delete-on-empty must restart at line 1.
	if err := s.Append(rk, msg(99, "again")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Consume(rk, 1); len(got) != 1 || got[0].MessageID != 99 {
		t.Fatalf("re-append consume = %+v, want msg 99", got)
	}
}

func TestRecoverOnStartup_CursorBehindReplaysAtLeastOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("C3_QUEUE_DIR", dir)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	s1, _ := NewStore(QueueDir())
	for i := int64(1); i <= 4; i++ {
		_ = s1.Append(rk, msg(i, "m"))
	}
	if _, err := s1.Consume(rk, 2); err != nil { // cursor = 2 persisted
		t.Fatal(err)
	}
	// Fresh store over the same dir simulates a restart.
	s2, err := NewStore(QueueDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.RecoverOnStartup(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.Pending(rk); n != 2 {
		t.Fatalf("recovered pending = %d, want 2 (lines 3,4)", n)
	}
	got, _ := s2.Consume(rk, 2)
	if len(got) != 2 || got[0].MessageID != 3 {
		t.Fatalf("recovered consume = %+v, want msgs 3,4", got)
	}
}

func TestRecoverOnStartup_FullyConsumedPairDeleted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("C3_QUEUE_DIR", dir)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	s1, _ := NewStore(QueueDir())
	_ = s1.Append(rk, msg(1, "m"))
	_ = s1.Append(rk, msg(2, "m"))
	// Simulate a crash AFTER persisting cursor=2 but BEFORE delete-on-empty by
	// writing the .cur to EOF directly via Consume, then dropping the in-memory
	// store and recovering.
	_, _ = s1.Consume(rk, 2)
	s2, _ := NewStore(QueueDir())
	if err := s2.RecoverOnStartup(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.Pending(rk); n != 0 {
		t.Fatalf("fully-consumed route should recover to 0 pending, got %d", n)
	}
}

func TestEvictOverCap_DropsOldestAndAdjustsCursor(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	// Append cap+5 messages.
	for i := int64(1); i <= MaxMessages+5; i++ {
		_ = s.Append(rk, msg(i, "m"))
	}
	dropped, err := s.EvictOverCap(rk)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 5 {
		t.Fatalf("dropped = %d, want 5", dropped)
	}
	if n, _ := s.Pending(rk); n != MaxMessages {
		t.Fatalf("pending after evict = %d, want %d", n, MaxMessages)
	}
	// Oldest survivor is message 6 (1..5 dropped).
	got, _ := s.Peek(rk, 1)
	if got[0].MessageID != 6 {
		t.Fatalf("oldest after evict = %d, want 6", got[0].MessageID)
	}
}

func TestEvictOverCap_DropsByAge(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	old := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 1, Text: "old", Timestamp: time.Now().Add(-MaxAge - time.Hour)}
	fresh := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 2, Text: "new", Timestamp: time.Now()}
	_ = s.Append(rk, old)
	_ = s.Append(rk, fresh)
	dropped, err := s.EvictOverCap(rk)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("age-evict dropped = %d, want 1", dropped)
	}
	got, _ := s.Peek(rk, 5)
	if len(got) != 1 || got[0].MessageID != 2 {
		t.Fatalf("after age-evict = %+v, want only msg 2", got)
	}
}

func TestRecoverOnStartup_SkipsCorruptLine(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	_ = s.Append(rk, msg(1, "ok"))
	// Manually append a corrupt line to the jsonl.
	if err := appendRawLine(t, QueueDir(), rk, "{not json"); err != nil {
		t.Fatal(err)
	}
	_ = s.Append(rk, msg(3, "ok2"))
	// A peek that walks past the corrupt line must skip it, not error.
	got, err := s.Peek(rk, 5)
	if err != nil {
		t.Fatalf("peek over corrupt line errored: %v", err)
	}
	if len(got) != 2 || got[0].MessageID != 1 || got[1].MessageID != 3 {
		t.Fatalf("peek skipping corrupt = %+v, want msgs 1,3", got)
	}
}

func TestStatusAll_ReportsPendingAndOldest(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	_ = s.Append(rk, msg(1, "m"))
	_ = s.Append(rk, msg(2, "m"))
	all := s.StatusAll()
	st, ok := all[rk]
	if !ok || st.Pending != 2 || st.OldestUnix == 0 {
		t.Fatalf("StatusAll[%v] = %+v ok=%v", rk, st, ok)
	}
}

// -race coverage with a DETERMINISTIC post-condition: a single route worker
// interleaving appends + consumes must be race-free (all calls funnel through one
// goroutine, mirroring the worker's single-owner model) AND must never return a
// consumed message twice. Run under `go test -race`. Unlike a bare "it ran"
// test, this asserts (a) the final Pending equals appends-minus-successfully-
// consumed and (b) no MessageID is ever consumed twice, so a regression in the
// cursor/consume math is actually caught.
func TestStore_SingleOwnerSerializedConsumeIsExactlyOnce(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	type op struct {
		append bool
		id     int64
	}
	ops := make(chan op)
	doneCh := make(chan struct{})
	seen := map[int64]int{} // MessageID -> times consumed
	appended, consumed := 0, 0
	go func() { // the single owner goroutine — also owns `seen` (no shared access)
		defer close(doneCh)
		for o := range ops {
			if o.append {
				if err := s.Append(rk, msg(o.id, "m")); err == nil {
					appended++
				}
				continue
			}
			got, err := s.Consume(rk, 1)
			if err != nil {
				t.Errorf("consume: %v", err)
				continue
			}
			for _, m := range got {
				seen[m.MessageID]++
				consumed++
			}
		}
	}()
	var wg sync.WaitGroup
	var nextID int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		id := nextID + 1
		nextID = id
		go func(i int, id int64) { defer wg.Done(); ops <- op{append: i%2 == 0, id: id} }(i, id)
	}
	wg.Wait()
	close(ops)
	<-doneCh
	for id, n := range seen {
		if n != 1 {
			t.Errorf("message %d consumed %d times, want exactly once", id, n)
		}
	}
	if n, _ := s.Pending(rk); n != appended-consumed {
		t.Errorf("final pending = %d, want appended(%d) - consumed(%d) = %d", n, appended, consumed, appended-consumed)
	}
}
```

Add the `appendRawLine` test helper at the bottom of `store_test.go`:

```go
func appendRawLine(t *testing.T, dir string, rk RouteKey, raw string) error {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, rk.File()+".jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(raw + "\n")
	return err
}
```

(add `"os"` and `"path/filepath"` to the test import block.)

- [ ] **Step 5: Run the store tests to verify they fail**

Run: `go test ./internal/queue/ -v`
Expected: FAIL — `NewStore`, `Store`, `Status`, methods undefined.

- [ ] **Step 6: Implement the store** — create `internal/queue/store.go`:

```go
package queue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Status is a per-route snapshot for /status. Pending is lines after the cursor;
// OldestUnix is the timestamp of the oldest pending line (0 when empty).
type Status struct {
	Pending    int
	OldestUnix int64
}

// Store owns the queue directory. It is single-owner per route (the broker
// funnels every route's file ops through that route's RouteWorker goroutine), so
// the file ops hold no per-file locks. Only the cheap cross-route status index
// is mutex-guarded — it touches no files.
type Store struct {
	dir string

	mu  sync.Mutex // guards idx ONLY (the cross-route status counters)
	idx map[RouteKey]Status
}

// NewStore creates the queue dir (0700) and returns a Store. Call
// RecoverOnStartup once after construction to rebuild the index from disk.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("queue: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir, idx: map[RouteKey]Status{}}, nil
}

func (s *Store) jsonlPath(rk RouteKey) string { return filepath.Join(s.dir, rk.File()+".jsonl") }
func (s *Store) curPath(rk RouteKey) string   { return filepath.Join(s.dir, rk.File()+".cur") }

// Append writes one JSON line and fsyncs it (data + parent dir), then refreshes
// the status index. The caller (worker) only treats the source update_id as
// offset-eligible AFTER this returns nil.
func (s *Store) Append(rk RouteKey, in *c3types.Inbound) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("queue: marshal: %w", err)
	}
	f, err := os.OpenFile(s.jsonlPath(rk), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("queue: open append: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("queue: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("queue: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("queue: close: %w", err)
	}
	s.refreshIndex(rk)
	return nil
}

// readLines returns the parsed inbound lines and the cursor. Corrupt lines are
// skipped (logged via the returned skipped count is implicit — caller logs).
func (s *Store) readLines(rk RouteKey) (lines []c3types.Inbound, cursor int, err error) {
	f, err := os.Open(s.jsonlPath(rk))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("queue: open read: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			lines = append(lines, c3types.Inbound{}) // keep line-number alignment
			continue
		}
		var in c3types.Inbound
		if jerr := json.Unmarshal([]byte(line), &in); jerr != nil {
			// Corrupt line: keep a placeholder so the cursor's line-number stays
			// aligned with the file, but mark it skippable via a zero MessageID
			// caller filters. We tag it with a sentinel channel so Peek/Consume
			// can drop it.
			lines = append(lines, c3types.Inbound{Channel: corruptSentinel})
			continue
		}
		lines = append(lines, in)
	}
	if serr := sc.Err(); serr != nil {
		return nil, 0, fmt.Errorf("queue: scan: %w", serr)
	}
	cursor = s.readCursor(rk)
	return lines, cursor, nil
}

const corruptSentinel = "\x00corrupt"

// pendingFrom returns the non-corrupt lines after the cursor.
func pendingFrom(lines []c3types.Inbound, cursor int) []c3types.Inbound {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(lines) {
		cursor = len(lines)
	}
	out := make([]c3types.Inbound, 0, len(lines)-cursor)
	for _, in := range lines[cursor:] {
		if in.Channel == corruptSentinel {
			continue
		}
		out = append(out, in)
	}
	return out
}

// Peek returns up to n oldest pending messages without advancing the cursor.
func (s *Store) Peek(rk RouteKey, n int) ([]c3types.Inbound, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return nil, err
	}
	pending := pendingFrom(lines, cursor)
	if n >= 0 && n < len(pending) {
		pending = pending[:n]
	}
	return pending, nil
}

// Consume returns up to n oldest pending messages AND advances the cursor past
// them (corrupt placeholder lines are stepped over too). Deletes both files when
// the cursor reaches EOF.
func (s *Store) Consume(rk RouteKey, n int) ([]c3types.Inbound, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return nil, err
	}
	out := make([]c3types.Inbound, 0, n)
	pos := cursor
	for pos < len(lines) && (n < 0 || len(out) < n) {
		in := lines[pos]
		pos++
		if in.Channel == corruptSentinel {
			continue // step over corrupt lines, don't return them
		}
		out = append(out, in)
	}
	// Advance the cursor to pos (after the last consumed/skipped line).
	if pos >= len(lines) {
		if err := s.deletePair(rk); err != nil {
			return nil, err
		}
	} else if err := s.writeCursor(rk, pos); err != nil {
		return nil, err
	}
	s.refreshIndex(rk)
	return out, nil
}

// Pending returns the count of pending messages and the oldest pending
// timestamp (zero time when empty).
func (s *Store) Pending(rk RouteKey) (int, time.Time) {
	lines, cursor, err := s.readLines(rk)
	if err != nil {
		return 0, time.Time{}
	}
	pending := pendingFrom(lines, cursor)
	if len(pending) == 0 {
		return 0, time.Time{}
	}
	return len(pending), pending[0].Timestamp
}

// StatusAll returns a snapshot of the in-memory status index (all known routes
// with pending > 0). Cheap; touches no files.
func (s *Store) StatusAll() map[RouteKey]Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[RouteKey]Status, len(s.idx))
	for k, v := range s.idx {
		if v.Pending > 0 {
			out[k] = v
		}
	}
	return out
}

// EvictOverCap drops the oldest lines exceeding MaxMessages OR older than MaxAge
// (a cap-only rewrite), adjusting the cursor by the number dropped. Returns the
// count dropped (0 when under cap). Never silent — the broker logs + sends a
// Telegram notice when dropped > 0.
func (s *Store) EvictOverCap(rk RouteKey) (int, error) {
	lines, cursor, err := s.readLines(rk)
	if err != nil || len(lines) == 0 {
		return 0, err
	}
	cutoff := time.Now().Add(-MaxAge)
	// Find how many leading lines to drop: by age first, then by count.
	drop := 0
	for _, in := range lines {
		if in.Channel != corruptSentinel && !in.Timestamp.IsZero() && in.Timestamp.Before(cutoff) {
			drop++
			continue
		}
		break
	}
	if len(lines)-drop > MaxMessages {
		drop += (len(lines) - drop) - MaxMessages
	}
	if drop == 0 {
		return 0, nil
	}
	if drop > len(lines) {
		drop = len(lines)
	}
	kept := lines[drop:]
	if err := s.rewrite(rk, kept); err != nil {
		return 0, err
	}
	newCursor := cursor - drop
	if newCursor < 0 {
		newCursor = 0
	}
	if newCursor >= len(kept) {
		if err := s.deletePair(rk); err != nil {
			return 0, err
		}
	} else if err := s.writeCursor(rk, newCursor); err != nil {
		return 0, err
	}
	s.refreshIndex(rk)
	return drop, nil
}

// RecoverOnStartup scans the queue dir and rebuilds the status index. A route
// whose cursor is at/after its line count has its pair deleted; otherwise the
// index records the derived pending count.
func (s *Store) RecoverOnStartup() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("queue: readdir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		base := strings.TrimSuffix(name, ".jsonl")
		rk, ok := parseRouteFile(base)
		if !ok {
			continue
		}
		lines, cursor, rerr := s.readLines(rk)
		if rerr != nil {
			continue
		}
		if cursor >= len(lines) {
			_ = s.deletePair(rk)
			continue
		}
		s.refreshIndex(rk)
	}
	return nil
}

// --- cursor + file helpers ---

func (s *Store) readCursor(rk RouteKey) int {
	data, err := os.ReadFile(s.curPath(rk))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *Store) writeCursor(rk RouteKey, n int) error {
	tmp := s.curPath(rk) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(n)), 0600); err != nil {
		return fmt.Errorf("queue: write cursor: %w", err)
	}
	if err := os.Rename(tmp, s.curPath(rk)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: rename cursor: %w", err)
	}
	return nil
}

func (s *Store) deletePair(rk RouteKey) error {
	_ = os.Remove(s.curPath(rk))
	if err := os.Remove(s.jsonlPath(rk)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("queue: remove jsonl: %w", err)
	}
	return nil
}

// rewrite atomically replaces the jsonl with the given lines (cap valve only).
func (s *Store) rewrite(rk RouteKey, lines []c3types.Inbound) error {
	tmp := s.jsonlPath(rk) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("queue: open rewrite: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, in := range lines {
		if in.Channel == corruptSentinel {
			continue
		}
		data, merr := json.Marshal(in)
		if merr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("queue: marshal rewrite: %w", merr)
		}
		_, _ = w.Write(append(data, '\n'))
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: flush rewrite: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: fsync rewrite: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: close rewrite: %w", err)
	}
	if err := os.Rename(tmp, s.jsonlPath(rk)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("queue: rename rewrite: %w", err)
	}
	return nil
}

// refreshIndex recomputes the cheap status counters for one route.
func (s *Store) refreshIndex(rk RouteKey) {
	n, oldest := s.Pending(rk)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == 0 {
		delete(s.idx, rk)
		return
	}
	s.idx[rk] = Status{Pending: n, OldestUnix: oldest.Unix()}
}

// parseRouteFile reverses RouteKey.File(): "<channel>__<chat>__<topic|none>".
func parseRouteFile(base string) (RouteKey, bool) {
	parts := strings.Split(base, "__")
	if len(parts) != 3 {
		return RouteKey{}, false
	}
	chat, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return RouteKey{}, false
	}
	rk := RouteKey{Channel: parts[0], ChatID: chat}
	if parts[2] != "none" {
		tid, terr := strconv.ParseInt(parts[2], 10, 64)
		if terr != nil {
			return RouteKey{}, false
		}
		rk.TopicID = &tid
	}
	return rk, true
}
```

- [ ] **Step 7: Run the queue tests (incl. -race) to verify they pass**

Run: `go test ./internal/queue/ -v && go test -race ./internal/queue/`
Expected: PASS, no race warnings.

- [ ] **Step 8: Commit**

```bash
git add internal/queue/paths.go internal/queue/store.go internal/queue/paths_test.go internal/queue/store_test.go
git commit -m "$(printf 'feat(queue): durable per-route append-only JSONL store with line-number cursor\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 3: Persisted-offset tracker (`internal/channel/telegram`)

**Files:**
- Create: `internal/channel/telegram/offset_tracker.go`
- Test: `internal/channel/telegram/offset_tracker_test.go`

**Interfaces:**
- Produces (consumed by the poll loop in Task wiring; standalone-testable here):
  - `newOffsetTracker(committed int64) *offsetTracker` — `committed` seeds from the persisted store (highest already-done id; 0 if none).
  - `(*offsetTracker).Register(updateID int64)` — mark an accepted update *in-flight*.
  - `(*offsetTracker).MarkDone(updateID int64)` — mark an update *done* (persisted, gated, dropped, or non-message).
  - `(*offsetTracker).Committed() int64` — highest contiguous-done update_id (the value to persist as `Save(Committed())`); the next `getUpdates` offset is `Committed()+1`.

The tracker is the seam between the async per-route `Append`+`fsync` and the single offset store. It advances `committed` to the highest id whose entire prefix (`<= id`) is done.

- [ ] **Step 1: Write the failing test** — create `internal/channel/telegram/offset_tracker_test.go`:

```go
package telegram

import "testing"

func TestOffsetTracker_ContiguousPrefixAdvance(t *testing.T) {
	tr := newOffsetTracker(0)
	tr.Register(1)
	tr.Register(2)
	tr.Register(3)
	// Out-of-order completion: 2 done first must NOT advance past the gap at 1.
	tr.MarkDone(2)
	if got := tr.Committed(); got != 0 {
		t.Fatalf("committed with gap at 1 = %d, want 0", got)
	}
	tr.MarkDone(1) // now 1,2 contiguous
	if got := tr.Committed(); got != 2 {
		t.Fatalf("committed after 1,2 done = %d, want 2", got)
	}
	tr.MarkDone(3)
	if got := tr.Committed(); got != 3 {
		t.Fatalf("committed after all done = %d, want 3", got)
	}
}

func TestOffsetTracker_GatedOrDroppedDoesNotBlock(t *testing.T) {
	tr := newOffsetTracker(10)
	tr.Register(11)
	tr.Register(12)
	tr.Register(13)
	// 12 is a gated/dropped/non-message update → MarkDone immediately.
	tr.MarkDone(12)
	tr.MarkDone(11)
	if got := tr.Committed(); got != 12 {
		t.Fatalf("committed = %d, want 12 (gated 12 must not block)", got)
	}
	// 13 still in-flight (mid-STT) holds the line.
	if got := tr.Committed(); got >= 13 {
		t.Fatalf("committed should not pass in-flight 13, got %d", got)
	}
	tr.MarkDone(13)
	if got := tr.Committed(); got != 13 {
		t.Fatalf("committed after 13 = %d, want 13", got)
	}
}

func TestOffsetTracker_CrashBeforePersistDoesNotAdvance(t *testing.T) {
	tr := newOffsetTracker(5)
	tr.Register(6) // accepted but its Append never completes (crash)
	if got := tr.Committed(); got != 5 {
		t.Fatalf("committed with in-flight 6 = %d, want 5 (no advance → Telegram redelivers)", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/channel/telegram/ -run OffsetTracker -v`
Expected: FAIL — `newOffsetTracker` undefined.

- [ ] **Step 3: Implement the tracker** — create `internal/channel/telegram/offset_tracker.go`:

```go
package telegram

import "sync"

// offsetTracker advances the Telegram offset only to the highest CONTIGUOUS
// update_id that is "done" — durably persisted (Append+fsync succeeded), or a
// no-op (gated / dropped / non-message). An update whose message is still
// mid-STT/mid-persist stays in-flight, so the committed offset cannot pass it;
// if the broker crashes there, the offset never advanced and Telegram redelivers
// it (within 24h). Loss-free by construction.
//
// It is goroutine-safe: Register is called from the poll loop, MarkDone from
// both the poll loop (gated/dropped/non-message) and the per-route worker
// goroutine (after Append+fsync), Committed from the poll loop before Save.
type offsetTracker struct {
	mu        sync.Mutex
	committed int64           // highest contiguous-done id
	done      map[int64]bool  // ids > committed that are done (sparse)
	inflight  map[int64]bool  // ids > committed registered but not yet done
}

// newOffsetTracker seeds committed from the persisted store (0 if none).
func newOffsetTracker(committed int64) *offsetTracker {
	return &offsetTracker{
		committed: committed,
		done:      map[int64]bool{},
		inflight:  map[int64]bool{},
	}
}

// Register marks an accepted update_id as in-flight. Ids <= committed are
// ignored (already accounted for — e.g. a dedup-skip re-seen after restart).
func (t *offsetTracker) Register(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id <= t.committed || t.done[id] {
		return
	}
	t.inflight[id] = true
}

// MarkDone marks an update_id done and advances committed over any now-contiguous
// prefix. Safe to call for an id never Registered (gated/dropped/non-message
// updates are marked done directly).
func (t *offsetTracker) MarkDone(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id <= t.committed {
		return
	}
	delete(t.inflight, id)
	t.done[id] = true
	for t.done[t.committed+1] {
		t.committed++
		delete(t.done, t.committed)
	}
}

// Committed returns the highest contiguous-done update_id. Persist this value;
// the next getUpdates offset is Committed()+1.
func (t *offsetTracker) Committed() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.committed
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/channel/telegram/ -run OffsetTracker -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/telegram/offset_tracker.go internal/channel/telegram/offset_tracker_test.go
git commit -m "$(printf 'feat(telegram): contiguous-prefix persisted-offset tracker\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 4: Broker holds the queue + worker append-before-deliver + held-count reply + delivered semantics

**Files:**
- Modify: `internal/broker/broker.go` (hold `*queue.Store`; construct + recover at `New`)
- Modify: `internal/broker/fallback.go` (add `heldReplyText(n int) string`)
- Modify: `internal/broker/worker.go` (`JobFetch`/`JobConsume` kinds; append+fsync before delivery; per-adapter delivered semantics; held-count auto-reply; route the source update_id to the offset tracker via a callback)
- Modify: `internal/broker/host.go` (`Emit` still submits `JobInbound`; carry the update_id so the worker can ack the tracker)
- Test: `internal/broker/queue_integration_test.go` (new)

**Interfaces:**
- Consumes: `queue.NewStore`, `queue.QueueDir`, `(*Store).Append/Peek/Consume/Pending/EvictOverCap/RecoverOnStartup`, `c3types.Inbound`.
- Produces (consumed by Tasks 6, 7, 9, 10):
  - `Broker.Queue *queue.Store` (exported field)
  - `broker.queueRouteKey(RouteKey) queue.RouteKey` helper
  - `heldReplyText(n int) string`
  - `JobFetch`/`JobConsume` `JobKind` values + `FetchJob`/`ConsumeJob{MessageID, Count}` payloads on `Job`
  - `(*RouteWorker).handleConsume` consumes `ConsumeJob.Count` lines off the head (Claude live-ack; covers a merged batch)
  - `forwardOrFallback(ctx, in, covered int)` — covered = lines a (possibly merged) push covers; threaded to `InboundMsg.Covered`/`Pending`
  - new `RouteWorker.dedup *deliveredDedup` field + `newDeliveredDedup`/`(*deliveredDedup).seenBefore` (dedupe-by-message_id, Step 6b), initialized in `newRouteWorker`
  - `(*RouteWorker).notePersistFailure(*c3types.Inbound)` — best-effort Telegram notice on Append failure
  - `covEffective(int) int` clamp helper

For storage, the worker stores **per-message** (one queue line per inbound) at flush time, after STT substitution, so the stored line already contains the transcript. The existing debounce/`mergeBatch` stays a delivery-presentation concern and does NOT merge stored lines.

- [ ] **Step 1: Write the failing integration test** — create `internal/broker/queue_integration_test.go`:

```go
package broker

import (
	"context"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/queue"
)

// No session attached → the inbound is queued (not dropped) AND a held-count
// auto-reply is sent (reusing the 5-min fallback cooldown).
func TestForwardOrFallback_NoSession_QueuesAndHeldReply(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "hello", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in, 1)

	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	if n, _ := b.Queue.Pending(qrk); n != 1 {
		t.Fatalf("no-session inbound should be queued; pending=%d, want 1", n)
	}
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Fatalf("expected one held-count auto-reply, got %d sends", got)
	}
	// Second message within cooldown: queued silently (no second reply).
	in2 := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 2, Text: "again", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in2, 1)
	if n, _ := b.Queue.Pending(qrk); n != 2 {
		t.Fatalf("second inbound should also queue; pending=%d, want 2", n)
	}
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Fatalf("second inbound within cooldown must NOT send a second reply; got %d sends", got)
	}
}

func TestHeldReplyText_CarriesCount(t *testing.T) {
	got := heldReplyText(3)
	// Pin a specific count-bearing phrase, not a stray '3'.
	if !strings.Contains(got, "3 messages queued") {
		t.Fatalf("heldReplyText(3) = %q, want '3 messages queued'", got)
	}
	if !strings.Contains(heldReplyText(1), "1 message queued") {
		t.Fatalf("heldReplyText(1) should use the singular '1 message queued'; got %q", heldReplyText(1))
	}
}
```

(add `"strings"` to `queue_integration_test.go`'s import block. Use `strings.Contains` — do NOT hand-roll a substring helper. If `mfWithTelegram`, `fakeChannel`, `brokerWithChannel`, `(*fakeChannel).sendRepliesSnapshot` already exist in the package's test helpers — they do, per `worker_test.go` — reuse them; do not redefine.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/ -run 'NoSession_QueuesAndHeldReply|HeldReplyText' -v`
Expected: FAIL — `b.Queue` undefined, `heldReplyText` undefined, and the no-session path currently drops (queue stays empty).

- [ ] **Step 3: Add the queue to the broker** — in `internal/broker/broker.go`:

Add the import `"github.com/karthikeyan5/c3/internal/queue"` and a field on `Broker`:

```go
	// Queue is the durable per-route inbound hold buffer. All file ops for a
	// route are funneled through that route's RouteWorker goroutine (single
	// owner ⇒ no file locks). Never nil after New.
	Queue *queue.Store
```

In `New`, after `b.mappings.Store(mf)` and before `b.Workers = ...`, construct + recover (best-effort: a queue init failure must not stop the broker, but log loudly):

```go
	if q, err := queue.NewStore(queue.QueueDir()); err != nil {
		log.Printf("queue: init failed (%v) — durable inbound hold DISABLED for this run", err)
		b.Queue = nil
	} else {
		if rerr := q.RecoverOnStartup(); rerr != nil {
			log.Printf("queue: recovery scan failed: %v", rerr)
		}
		b.Queue = q
	}
```

(add `"log"` to the import block if not present — it is not currently in broker.go, so add it.)

Add a helper at the bottom of `broker.go`:

```go
// queueRouteKey converts a broker RouteKey into the queue package's RouteKey.
func queueRouteKey(k RouteKey) queue.RouteKey {
	rk := queue.RouteKey{Channel: k.Channel, ChatID: k.ChatID}
	if k.HasTopic {
		t := k.TopicID
		rk.TopicID = &t
	}
	return rk
}
```

- [ ] **Step 4: Add `heldReplyText`** — in `internal/broker/fallback.go`, after the `fallbackText` const:

```go
// heldReplyText is the "held, nothing lost" auto-reply sent when an inbound is
// queued because no session is attached. It reassures and carries the running
// count of queued messages. Cadence is the existing 5-min fallback cooldown.
func heldReplyText(n int) string {
	plural := "message"
	if n != 1 {
		plural = "messages"
	}
	return fmt.Sprintf("📨 Held — nothing lost. No CLI is attached to this topic right now. %d %s queued — they'll be delivered when you attach a session here. Send /status to check.", n, plural)
}
```

(add `"fmt"` to `fallback.go`'s imports.)

- [ ] **Step 5: Add the job kinds + append-before-deliver + held-reply + delivered ack** — in `internal/broker/worker.go`:

Extend the `JobKind` enum:

```go
const (
	JobInbound JobKind = iota
	JobOutbound
	JobRelease
	JobFetch
	JobConsume
)
```

Extend `Job` and add the fetch/consume payloads:

```go
type Job struct {
	Kind     JobKind
	Inbound  *c3types.Inbound
	Outbound *OutboundJob
	Fetch    *FetchJob
	Consume  *ConsumeJob
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
```

In the `run` loop's `switch job.Kind`, add the two new cases (after `JobOutbound`):

```go
			case JobFetch:
				w.handleFetch(ctx, job.Fetch)
			case JobConsume:
				w.handleConsume(ctx, job.Consume)
```

Wire the new arrival path: when `JobInbound` is a normal message, the worker still debounces for *delivery presentation* but stores **each** message. The cleanest place is `flushInbounds`: after STT substitution + before `mergeBatch`, append every inbound to the queue (each becomes offset-eligible), then forward the merged view. Replace the body of `flushInbounds`' tail (from the `// Merge.` comment to the end) with:

```go
	// Durable storage: persist EACH message (one queue line) AFTER STT
	// substitution so the stored line already carries the transcript. Storage is
	// per-message; the merge below is a delivery-presentation concern only and
	// does not merge stored lines. Append failure = persist failure: do NOT mark
	// the update_id done (the worker's deliveredToTracker callback is only called
	// on success), so the Telegram offset can't pass it and the message is
	// redelivered (loss-free).
	if w.broker != nil && w.broker.Queue != nil {
		qrk := queueRouteKey(w.key)
		for _, in := range batch {
			// Dedup the at-least-once REPLAY a crash-mid-consume can produce
			// (spec: "dedupe by message_id"). See Step 6b for newDeliveredDedup.
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

	w.forwardOrFallback(ctx, merged)
```

Add the persisted-callback + evict helpers to `worker.go`:

```go
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
```

In `forwardOrFallback`, the **claimed + alive + connected** branch is the live-push path. After the successful `WriteJSON(... OpInbound ...)` and `delivered` log line, do NOT consume here — Claude acks via `OpInboundDelivered` (Task 9) which calls `handleConsume`. Add a one-line comment at that point:

```go
		// Live delivery: the message stays queued until the adapter sends
		// OpInboundDelivered{ok:true}, which Consumes it (Task: queue dispatch).
		// This keeps an un-acked push recoverable as backlog (recovery nudge).
```

Also stamp the covered-line count + backlog count onto the live push so (a) the ack can consume exactly the lines this push covered and (b) the Claude adapter can append the recovery nudge (spec Component 3 — the push-path half of the recovery net).

**Thread the covered count to the push site.** A debounced batch merges N stored lines into ONE merged push (`mergeBatch`), so the push must report how many lines it covers. Change `forwardOrFallback`'s signature to `func (w *RouteWorker) forwardOrFallback(ctx context.Context, in *c3types.Inbound, covered int)` and update ALL call sites: the `flushInbounds` tail call (`worker.go:296`) becomes `w.forwardOrFallback(ctx, merged, len(batch))`; the event-forward path (`worker.go:318`, never queued) passes `0`. The append-if-absent direct-call/test path (below) uses `covered` as given. **Also update the four existing `worker_test.go` callers** (`worker_test.go:162,203,242,281`) to pass a trailing `1` (or `0` for the event cases) — this is part of keeping the suite green in Step 8.

Build the `InboundMsg` at the live-push site with `Covered` = the lines this push covers and `Pending` = the route's remaining queued count *beyond the lines this push covers* (the covered lines are still queued at push time — only Consumed on `OpInboundDelivered{ok}` — so subtract `covered` so a fully-caught-up live session shows `Pending:0`). Replace the bare `conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: *in})` at the live-push site with:

```go
		covered := covEffective(covered) // >=1
		pending := 0
		if w.broker != nil && w.broker.Queue != nil {
			if n, _ := w.broker.Queue.Pending(queueRouteKey(w.key)); n > covered {
				pending = n - covered // covered lines are still queued until acked
			}
		}
		if err := conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: *in, Covered: covered, Pending: pending}); err != nil {
```

(keep the existing error-handling body of that `if err := ...` block unchanged.) Add the tiny clamp helper:

```go
// covEffective normalizes a covered-line count to >=1 (a live push always covers
// at least the one merged message; 0/negative comes from the event path or a
// direct test call and is treated as a single line).
func covEffective(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
```

Replace the no-claim message tail of `forwardOrFallback` (the block from `if !w.broker.Fallbacks.ShouldSend(w.key) { ... }` through the final `SendReply(fallbackText)` log) so that, instead of dropping, it **queues then sends the held-count reply**. The message has ALREADY been appended in `flushInbounds`, so here we only send the cooldown'd held-count reply:

```go
	// No live claim: the message is already durably queued (flushInbounds
	// appended it). Replace the old drop with a "held, nothing lost" auto-reply,
	// cooldown'd to once per window, carrying the RUNNING queued count.
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
```

> Note: the integration test in Step 1 calls `forwardOrFallback` directly with a single `*Inbound` that was NOT routed through `flushInbounds`, so its queue line won't exist yet. To keep `forwardOrFallback` self-consistent when reached for a not-yet-stored message (the direct-call test path AND any future caller), append-if-absent at the top of the no-claim block: before computing `count`, ensure the message is stored:

```go
	if w.broker.Queue != nil {
		_ = w.broker.Queue.Append(queueRouteKey(w.key), in)
	}
```

Place this immediately after the `if in.IsEvent() { ... }` event-drop guard and before the `ShouldSend` check, with a comment that the normal path already appended in flushInbounds and Append is idempotent-enough for the direct-call/test path (a duplicate line is bounded by the cap and dedup-by-message_id on the adapter side). For events, keep the existing drop behavior (events are never queued — they are transient).

Add `"github.com/karthikeyan5/c3/internal/queue"` to `worker.go`'s import block.

- [ ] **Step 6: Add the persisted-callback seam on the broker** — in `internal/broker/broker.go`, add a field + setter + notifier so the telegram channel can register its offset tracker without `internal/broker` importing `internal/channel/telegram`:

```go
	// persistedCB is invoked (best-effort, off the hot path is fine) when an
	// inbound's source update_id has been durably appended to the queue. The
	// telegram channel registers this to advance its persisted-offset tracker.
	// nil ⇒ no-op (non-telegram / unit tests).
	persistedMu sync.RWMutex
	persistedCB func(in *c3types.Inbound)
```

```go
// SetPersistedCallback registers the durable-persist notifier (the telegram
// channel sets this to advance its persisted-offset tracker). Safe to call once
// at channel start.
func (b *Broker) SetPersistedCallback(fn func(in *c3types.Inbound)) {
	b.persistedMu.Lock()
	defer b.persistedMu.Unlock()
	b.persistedCB = fn
}

// notifyPersisted invokes the registered persist callback, if any.
func (b *Broker) notifyPersisted(in *c3types.Inbound) {
	b.persistedMu.RLock()
	fn := b.persistedCB
	b.persistedMu.RUnlock()
	if fn != nil {
		fn(in)
	}
}
```

(The wiring of this callback from the telegram channel — passing the `update_id` through `c3types.Inbound` — is finalized in the poll-loop integration of Task 3's tracker; for now `notifyPersisted` exists and is exercised by Task 9's end-to-end test. The `c3types.Inbound` already carries enough to identify the message; the telegram channel maps `MessageID`→`update_id` via a side table it owns. This task only needs `notifyPersisted` to be callable.)

- [ ] **Step 6b: Dedupe by message_id (spec's at-least-once net for crash-mid-consume)** — Spec Component 1 (line 103) + Error-handling (line 229) state: a crash mid-consume leaves the cursor slightly behind ⇒ **at-least-once re-delivery; dedupe by `message_id`**. Note this is NOT a Telegram `update_id` duplicate (the offset tracker handles those) — it is the *durable queue* replaying an already-delivered line after a crash-recovery cursor rewind, so the dedup must be keyed on `c3types.Inbound.MessageID`, on the delivery path, after recovery. Implement a small bounded per-route recently-delivered set on the worker and suppress a re-delivery of an already-seen MessageID at flush time (before the live-push / queue-store). It is bounded (drop-oldest) so it cannot grow unboundedly.

Add to `worker.go` a tiny FIFO-bounded set and a field on `RouteWorker`:

```go
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
```

On `RouteWorker`, add `dedup *deliveredDedup` and initialize it in `newRouteWorker` (`dedup: newDeliveredDedup(2048)` — comfortably larger than the 1000-message cap so a full-queue recovery replay is fully covered). In `flushInbounds`, when iterating `batch` to Append, **skip an inbound whose `MessageID` was already delivered** (log a metadata-only dedup line, do not store or forward it):

```go
		for _, in := range batch {
			if w.dedup != nil && w.dedup.seenBefore(in.MessageID) {
				log.Printf("dedup chan=%s chat=%d topic=%s msg=%d: already delivered, suppressing replay",
					w.key.Channel, w.key.ChatID, TopicKeyStr(w.key), in.MessageID)
				continue
			}
			if err := w.broker.Queue.Append(qrk, in); err != nil {
				// ... existing append-fail branch (unchanged) ...
				continue
			}
			w.markPersisted(in)
			w.evictIfOverCap(qrk)
		}
```

(Fold the dedup check into the existing append loop shown in Step 5 — it is the same loop; this just adds the leading `seenBefore` guard.)

Add a suppression test to `queue_integration_test.go`:

```go
func TestFlushInbounds_DedupesReplayedMessageID(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100}

	in := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 7, Text: "hi", Timestamp: time.Now()}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})
	// Simulate a crash-recovery replay of the SAME message_id.
	w.flushInbounds(context.Background(), []*c3types.Inbound{{Channel: "telegram", ChatID: -100, MessageID: 7, Text: "hi", Timestamp: time.Now()}})

	if n, _ := b.Queue.Pending(qrk); n != 1 {
		t.Fatalf("replayed message_id should be deduped; pending=%d, want 1", n)
	}
}
```

> Scope note: this dedup window is in-memory and per-worker, so it covers the spec's stated case (a crash-mid-consume cursor rewind producing a replay within the same broker run, and the recovery-replay path). A dedup across a full broker restart is bounded by the same cursor/at-least-once guarantee the spec already accepts ("never lose, occasionally repeat"); a persistent dedup log is explicitly NOT built (YAGNI, matches the spec's "occasionally repeat" tolerance).

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./internal/broker/ -run 'NoSession_QueuesAndHeldReply|HeldReplyText' -v && go build ./...`
Expected: PASS, build green.

- [ ] **Step 8: Run the broker suite + race**

Run: `go test ./internal/broker/ && go test -race ./internal/broker/`
Expected: PASS, no races. (Pre-existing tests that asserted the OLD drop behavior — e.g. any test expecting `fallbackText` exactly — must be updated to expect `heldReplyText`; search `internal/broker/*_test.go` for `fallbackText` / "No CLI is currently attached" assertions tied to the no-claim *message* path and adjust them to the held-reply wording. The STALE/BOUNCE branches still use `fallbackText` and are unchanged.)

- [ ] **Step 9: Commit**

```bash
git add internal/broker/broker.go internal/broker/fallback.go internal/broker/worker.go internal/broker/queue_integration_test.go
git commit -m "$(printf 'feat(broker): durable-queue hold on no-session + held-count reply + fetch/consume jobs\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 5: `/status` Telegram command intercept + `setMyCommands` + persisted-offset poll wiring

> **Split into three independently-committed sub-tasks** (each its own red→green→commit cycle, so a bisect/rollback is per-deliverable):
> - **5a** — broker `/status` renderer + `Host.HandleCommand` interface method + **`fakeHost` update** (`internal/broker/status_command.go`, `internal/channel/channel.go`, `internal/channel/telegram/dispatch_gate_test.go`, `internal/broker/status_command_test.go`). Steps 1–5.
> - **5b** — telegram `/status` text intercept + `setMyCommands` registration (`internal/channel/telegram/poll.go`, `internal/channel/telegram/telegram.go`, `internal/channel/telegram/status_intercept_test.go`). Steps 6–9.
> - **5c** — poll-loop persisted-offset wiring (`internal/channel/telegram/telegram.go` struct fields, `internal/channel/telegram/poll.go`). Steps 10–11 + the new offset integration test.

**Files:**
- Modify: `internal/channel/channel.go` (add `HandleCommand` to `Host`) — **5a**
- Create: `internal/broker/status_command.go` (`BrokerHost.HandleCommand` + renderers) — **5a**
- Modify: `internal/channel/telegram/dispatch_gate_test.go` (add `HandleCommand` to the `fakeHost` test double so the telegram package still compiles) — **5a**
- Modify: `internal/channel/telegram/poll.go` (`/status` intercept before gating; reply via channel — **5b**; offset advance via `offsetTracker` — **5c**)
- Modify: `internal/channel/telegram/telegram.go` (`setMyCommands` at Start — **5b**; `mu`/`offTrk`/`msgToUpdate` struct fields + tracker seed + persist callback — **5c**)
- Test: `internal/broker/status_command_test.go` (new) — **5a**
- Test: `internal/channel/telegram/status_intercept_test.go` (new) — **5b**
- Test: `internal/channel/telegram/offset_wiring_test.go` (new — drives a fake `getUpdates` batch and asserts the persisted offset does NOT advance past an update whose `Append` is still in-flight) — **5c**

**Interfaces:**
- Consumes: `Broker.Queue.StatusAll()`, `Broker.Queue.Pending()`, `Broker.Routes.Holder()`, `Broker.Stubs`.
- Produces:
  - `channel.Host.HandleCommand(in *c3types.Inbound) (reply string, handled bool)`
  - `BrokerHost.HandleCommand(in *c3types.Inbound) (string, bool)`
  - `(*Channel).isStatusCommand(text string) bool` (telegram-local)
  - new telegram `Channel` fields: `mu sync.Mutex`, `offTrk *offsetTracker`, `msgToUpdate map[int64]int64`; `(*Channel).markUpdateDone(updateID int64)`

The `/status` text is matched as `/status` or `/status@<botname>` (case-insensitive, trimmed). When matched, the channel asks the host to render the reply, sends it directly via `SendReply`, marks the update done, and NEVER gates/emits/queues it.

> **CRITICAL — every `channel.Host` implementer must gain `HandleCommand` in the SAME commit (5a), or the build breaks.** Adding `HandleCommand` to the `Host` interface (Step 3) silently breaks any type that manually implements `Host`. Today there are exactly two: `broker.BrokerHost` (gets the real method in Step 4) and the telegram test double `fakeHost` in `internal/channel/telegram/dispatch_gate_test.go` (which manually implements `Config/Emit/Logf/Done/GateInbound/NotifyHealth` — verified: no `HandleCommand`). Without the stub below, the entire `internal/channel/telegram` package **fails to compile** and Steps 5/9/11 cannot even build. Grep `grep -rln "func.*GateInbound(in \*c3types.Inbound)" --include=*.go` to confirm no third implementer was added before shipping.

- [ ] **Step 1: Write the failing broker test** — create `internal/broker/status_command_test.go`:

```go
package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/queue"
)

func TestHandleCommand_StatusInTopic(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "x", Timestamp: time.Now().Add(-2 * time.Hour)})

	host := NewBrokerHost(b, "telegram")
	in := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, Text: "/status"}
	reply, handled := host.HandleCommand(in)
	if !handled {
		t.Fatal("/status in a topic should be handled")
	}
	if !strings.Contains(reply, "1 queued") {
		t.Errorf("in-topic status = %q, want '1 queued'", reply)
	}
}

func TestHandleCommand_GlobalInDM(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "x", Timestamp: time.Now()})

	host := NewBrokerHost(b, "telegram")
	in := &c3types.Inbound{Channel: "telegram", ChatID: 555, TopicID: nil, Text: "/status"} // DM
	reply, handled := host.HandleCommand(in)
	if !handled {
		t.Fatal("/status in DM should be handled")
	}
	if !strings.Contains(reply, "Broker up") {
		t.Errorf("global status = %q, want 'Broker up'", reply)
	}
}

func TestHandleCommand_NonStatusNotHandled(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	host := NewBrokerHost(b, "telegram")
	if _, handled := host.HandleCommand(&c3types.Inbound{Channel: "telegram", Text: "hello"}); handled {
		t.Error("non-command text must not be handled")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/ -run HandleCommand -v`
Expected: FAIL — `host.HandleCommand` undefined.

- [ ] **Step 3: Add `HandleCommand` to the Host interface** — in `internal/channel/channel.go`, inside the `Host` interface (after `GateInbound`):

```go
	// HandleCommand lets the channel hand a recognized bot command (e.g.
	// "/status") to the broker for direct handling. Returns the reply text and
	// handled=true when the broker owns the command — the channel then sends the
	// reply itself and MUST NOT gate, emit, queue, or route the message. Returns
	// ("", false) for anything the broker does not handle (the channel proceeds
	// with normal gating/routing). The command is broker-handled, not agent
	// input.
	HandleCommand(in *c3types.Inbound) (reply string, handled bool)
```

- [ ] **Step 4: Implement the broker renderer** — create `internal/broker/status_command.go`:

```go
package broker

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// HandleCommand handles broker-owned Telegram bot commands. Currently only
// "/status" (and "/status@<bot>") is wired — a tiny dispatcher that is trivially
// extensible later (/drain, /clear) but YAGNI for now. A "/status" sent in a
// topic returns that topic's status; in DM/General it returns the global
// summary. Anything else returns ("", false) so the channel routes normally.
func (h *BrokerHost) HandleCommand(in *c3types.Inbound) (string, bool) {
	if in == nil {
		return "", false
	}
	cmd := strings.TrimSpace(in.Text)
	if i := strings.IndexByte(cmd, '@'); i >= 0 { // strip /status@botname
		cmd = cmd[:i]
	}
	if !strings.EqualFold(cmd, "/status") {
		return "", false
	}
	if in.TopicID != nil {
		return h.broker.statusForTopic(in.Channel, in.ChatID, in.TopicID), true
	}
	return h.broker.statusGlobal(), true
}

// statusForTopic renders the per-topic status line.
func (b *Broker) statusForTopic(channelName string, chatID int64, topicID *int64) string {
	key := MakeRouteKey(channelName, chatID, topicID)
	name := b.topicDisplayName(channelName, chatID, topicID)
	pending, oldest := 0, time.Time{}
	if b.Queue != nil {
		pending, oldest = b.Queue.Pending(queueRouteKey(key))
	}
	attached := "no CLI attached"
	if _, held := b.Routes.Holder(key); held {
		attached = "CLI attached"
	}
	return fmt.Sprintf("📊 %s · %d queued%s · %s · broker up", name, pending, oldestSuffix(oldest), attached)
}

// statusGlobal renders the broker-wide summary (empty queues omitted).
func (b *Broker) statusGlobal() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 Broker up (pid %d).", os.Getpid())
	if b.Queue == nil {
		return sb.String()
	}
	all := b.Queue.StatusAll()
	if len(all) > 0 {
		sb.WriteString(" Active queues:")
		type row struct {
			name    string
			pending int
			oldest  int64
		}
		rows := make([]row, 0, len(all))
		for k, st := range all {
			rk := MakeRouteKey(k.Channel, k.ChatID, k.TopicID)
			rows = append(rows, row{b.topicDisplayName(k.Channel, k.ChatID, k.TopicID), st.Pending, st.OldestUnix})
			_ = rk
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		for _, r := range rows {
			fmt.Fprintf(&sb, "\n• %s — %d%s", r.name, r.pending, oldestSuffix(time.Unix(r.oldest, 0)))
		}
	}
	attached, idle := b.sessionCounts()
	fmt.Fprintf(&sb, "\n%d attached · %d idle", attached, idle)
	return sb.String()
}

// topicDisplayName looks up the topic's friendly name; falls back to "dm" or
// "topic-<id>".
func (b *Broker) topicDisplayName(channelName string, chatID int64, topicID *int64) string {
	if topicID == nil {
		return "dm"
	}
	if tp, ok := b.Mappings().LookupTopicByID(channelName, chatID, *topicID); ok && tp.Name != "" {
		return tp.Name
	}
	return fmt.Sprintf("topic-%d", *topicID)
}

// sessionCounts returns (attached, idle) live agent-session counts.
func (b *Broker) sessionCounts() (attached, idle int) {
	for _, s := range b.Stubs.Snapshot() {
		if s.CLI == "c3-broker-cli" {
			continue
		}
		if s.CurrentRoute() != nil {
			attached++
		} else {
			idle++
		}
	}
	return attached, idle
}

// oldestSuffix renders " (oldest 2h)" or "" when there is nothing queued.
func oldestSuffix(oldest time.Time) string {
	if oldest.IsZero() || oldest.Unix() <= 0 {
		return ""
	}
	d := time.Since(oldest)
	switch {
	case d < time.Minute:
		return " (oldest <1m)"
	case d < time.Hour:
		return fmt.Sprintf(" (oldest %dm)", int(d.Minutes()))
	default:
		return fmt.Sprintf(" (oldest %dh)", int(d.Hours()))
	}
}
```

(Confirm `b.Mappings().LookupTopicByID`, `b.Stubs.Snapshot()`, and `(*Stub).CurrentRoute()` exist — they are used elsewhere in the package, including `host.go`/`attach.go`/`handler.go`. If `LookupTopicByID` has a different arity, match its real signature.)

- [ ] **Step 4b: Keep every `Host` implementer compiling — add `HandleCommand` to the telegram `fakeHost` test double** — Step 3 added `HandleCommand` to the `channel.Host` interface, which breaks the manual implementer in `internal/channel/telegram/dispatch_gate_test.go`. In that file, next to the other `fakeHost` methods (`Config/Emit/Logf/Done/GateInbound/NotifyHealth`), add the no-op stub:

```go
// HandleCommand satisfies channel.Host. cmdHandled lets a test opt this double
// into claiming "/status" (used by the not-routed test in 5b); it defaults to
// declining so the channel routes normally.
func (h *fakeHost) HandleCommand(in *c3types.Inbound) (string, bool) {
	if h.cmdHandled && in != nil && in.Text == "/status" {
		return "📊 ok", true
	}
	return "", false
}
```

(also add a `cmdHandled bool` field to the `fakeHost` struct in the same file.) `internal/channel/telegram/dispatch_gate_test.go` already declares `package telegram` and imports `internal/c3types`; no new import needed. This must be in the **same commit** as the interface change (5a) so the telegram package compiles before any later step builds it. After adding it, `go build ./...` must succeed; `BrokerHost.HandleCommand` (Step 4) and this `fakeHost.HandleCommand` are the only two implementers.

- [ ] **Step 5: Run the broker test + a full build to verify it passes (and nothing else broke)**

Run: `go test ./internal/broker/ -run HandleCommand -v && go build ./...`
Expected: PASS, build green (the `fakeHost` stub keeps `internal/channel/telegram` compiling).

- [ ] **Step 5b: Commit 5a** (broker `/status` renderer + `Host.HandleCommand` interface + `fakeHost` update)

```bash
git add internal/channel/channel.go internal/broker/status_command.go internal/broker/status_command_test.go internal/channel/telegram/dispatch_gate_test.go
git commit -m "$(printf 'feat(broker): /status renderer + Host.HandleCommand interface\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

- [ ] **Step 6: Write the failing telegram intercept test** — create `internal/channel/telegram/status_intercept_test.go`:

```go
package telegram

import "testing"

func TestIsStatusCommand(t *testing.T) {
	c := &Channel{}
	cases := map[string]bool{
		"/status":          true,
		" /status ":        true,
		"/STATUS":          true,
		"/status@c3bot":    true,
		"/statusly":        false,
		"hello":            false,
		"please /status":   false,
	}
	for text, want := range cases {
		if got := c.isStatusCommand(text); got != want {
			t.Errorf("isStatusCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

// Spec invariant: an intercepted "/status" must NEVER be Emitted (routed to an
// agent) — the broker handles it and the channel returns early. Drive
// dispatchMessage directly the same way the existing dispatch_gate_test.go cases
// do (hand-built *gotgbot.Message), with a fakeHost whose HandleCommand claims
// "/status". The recover guards the intercept's SendReply call (the bare
// makeChannel has a nil bot); the assertion that matters is Emit==0.
func TestStatusCommand_NotRouted(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow, cmdHandled: true}
	c := makeChannel(h)
	func() {
		defer func() { _ = recover() }() // tolerate nil-bot SendReply in the intercept
		c.dispatchMessage(1, textMsg("/status", 42), false, nil)
	}()
	if got := h.emitCount(); got != 0 {
		t.Errorf("/status must not be routed: Emit called %d times, want 0", got)
	}
}
```

To support this, add to the `fakeHost` in `dispatch_gate_test.go` a `cmdHandled bool` field and make its `HandleCommand` honor it (this supersedes the plain no-op stub from 5a Step 4b — use this richer version, which still satisfies the interface and defaults to declining):

```go
func (h *fakeHost) HandleCommand(in *c3types.Inbound) (string, bool) {
	if h.cmdHandled && in != nil && in.Text == "/status" {
		return "📊 ok", true
	}
	return "", false
}
```

(`makeChannel` builds a `Channel` with a nil `bot`, so the intercept's `SendReply` will panic on this path; the `recover` in the test absorbs that — the spec invariant under test is **a handled `/status` does not call `Emit`**, which is asserted after the recover. If a future refactor gives the test a stub bot, drop the recover.)

- [ ] **Step 7: Run it to verify it fails**

Run: `go test ./internal/channel/telegram/ -run IsStatusCommand -v`
Expected: FAIL — `(*Channel).isStatusCommand` undefined.

- [ ] **Step 8: Implement the intercept** — in `internal/channel/telegram/poll.go`, add the matcher + intercept. Add the helper:

```go
// isStatusCommand reports whether text is the "/status" bot command (optionally
// "/status@<botname>"), case-insensitive, after trimming. It must be an exact
// command token — "/statusly" and "please /status" are NOT matched.
func (c *Channel) isStatusCommand(text string) bool {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '@'); i >= 0 {
		t = t[:i]
	}
	return strings.EqualFold(t, "/status")
}
```

(add `"strings"` to `poll.go`'s imports.)

In `dispatchMessage`, immediately after `in := convertInbound(...)` and the `if in == nil` guard, BEFORE the `GateInbound` switch, add:

```go
	// Broker-owned command intercept: a "/status" inbound is handled by the
	// broker directly (it answers + is NEVER gated, queued, or routed to an
	// agent). Other commands fall through to normal gating.
	if c.isStatusCommand(in.Text) {
		if reply, handled := c.host.HandleCommand(in); handled {
			var topicID *int64
			if in.TopicID != nil {
				topicID = in.TopicID
			}
			if _, err := c.SendReply(c3types.ReplyArgs{
				Channel: c.Name(), ChatID: in.ChatID, TopicID: topicID, Text: reply,
				Markup: c3types.MarkupMarkdown,
			}); err != nil {
				c.host.Logf("telegram: /status reply send failed update=%d chat=%d: %v", updateID, in.ChatID, err)
			}
			c.host.Logf("telegram: /status handled update=%d chat=%d thread=%d (not routed)",
				updateID, msg.Chat.Id, msg.MessageThreadId)
			return
		}
	}
```

(Note: the poll loop must mark this update_id *done* in the offset tracker since it is handled-not-persisted. That marking is part of the poll-loop offset wiring — see Step 10. Here the early `return` skips Emit/queue as required.)

- [ ] **Step 9: Register `/status` via `setMyCommands`** — in `internal/channel/telegram/telegram.go`, inside `Start`, after `c.bot = bot` / `c.host = host` are set and the bot is confirmed reachable (near the GetMe/identity section), add a best-effort registration:

```go
	// Register the /status bot command so it autocompletes in Telegram's "/"
	// menu. Best-effort: a failure here never blocks Start (the command still
	// works when typed; only the menu hint is missing).
	go func() {
		if _, err := c.bot.SetMyCommands(
			[]gotgbot.BotCommand{{Command: "status", Description: "Show C3 broker + queue status"}},
			&gotgbot.SetMyCommandsOpts{},
		); err != nil {
			c.host.Logf("telegram: setMyCommands(/status) failed (non-fatal): %v", err)
		}
	}()
```

(Confirm `gotgbot.BotCommand` / `c.bot.SetMyCommands` signatures against the vendored gotgbot rc.34 — adjust the opts struct name if the version differs. `gotgbot` is already imported in this file.)

- [ ] **Step 9b: Commit 5b** (telegram `/status` intercept + `setMyCommands`)

```bash
git add internal/channel/telegram/poll.go internal/channel/telegram/telegram.go internal/channel/telegram/status_intercept_test.go
git commit -m "$(printf 'feat(telegram): /status command intercept before gating + setMyCommands\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

> **Note on Step 9b's commit and Step 10's struct fields:** the `/status` intercept in Step 8 already references `c.markUpdateDone(updateID)` for the early-return path. To keep 5b self-contained and compiling, add `markUpdateDone` as a nil-safe no-op in 5b (`func (c *Channel) markUpdateDone(updateID int64) { if c.offTrk != nil { c.offTrk.MarkDone(updateID) } }`) and the `offTrk`/`mu`/`msgToUpdate` fields in 5b too (they stay zero/nil until 5c seeds them, so `markUpdateDone` is a no-op in 5b). 5c then wires the tracker live. (Alternatively, do Steps 8–11 in one commit if separating the field-add from the wiring proves awkward — the split is for bisect granularity, not a hard requirement.)

- [ ] **Step 10 (5c): Add the offset-wiring failing integration test** — create `internal/channel/telegram/offset_wiring_test.go`. This is the **safety property the whole plan exists for** (offset advances only after durable persist), so it gets a real red→green test, not "mechanical glue, no test". Drive the tracker the way the poll loop will and assert the crash-before-persist invariant:

```go
package telegram

import "testing"

// The poll loop registers each accepted update as in-flight and only marks it
// done once the broker's persist callback fires (Append+fsync succeeded). An
// update whose Append is still in-flight must NOT let the committed offset pass
// it — otherwise a crash there loses the message (Telegram won't redeliver an
// already-acked offset). This exercises the exact Register → MarkDone(persist)
// seam the poll-loop wiring uses.
func TestPollOffsetWiring_NoAdvancePastUnpersisted(t *testing.T) {
	c := &Channel{}
	c.offTrk = newOffsetTracker(100)
	c.msgToUpdate = map[int64]int64{}

	// Simulate the persist callback the channel registers in Start.
	persist := func(in *c3types.Inbound) {
		c.mu.Lock()
		uid, found := c.msgToUpdate[in.MessageID]
		if found {
			delete(c.msgToUpdate, in.MessageID)
		}
		c.mu.Unlock()
		if found {
			c.offTrk.MarkDone(uid)
		}
	}

	// Batch of three accepted updates 101,102,103; record msg→update like
	// dispatchMessage does before Emit.
	for _, p := range []struct{ msg, upd int64 }{{1, 101}, {2, 102}, {3, 103}} {
		c.offTrk.Register(p.upd)
		c.mu.Lock()
		c.msgToUpdate[p.msg] = p.upd
		c.mu.Unlock()
	}
	// 101 and 103 persist; 102 is still mid-STT/mid-Append (the "crash" point).
	persist(&c3types.Inbound{MessageID: 1})
	persist(&c3types.Inbound{MessageID: 3})
	if got := c.offTrk.Committed(); got != 101 {
		t.Fatalf("committed = %d, want 101 (must NOT pass unpersisted 102)", got)
	}
	// 102 finally persists → committed jumps to 103.
	persist(&c3types.Inbound{MessageID: 2})
	if got := c.offTrk.Committed(); got != 103 {
		t.Fatalf("committed after 102 persisted = %d, want 103", got)
	}
}

// A gated/dropped/non-message/`/status` update is marked done immediately via
// markUpdateDone and must not block the contiguous prefix.
func TestPollOffsetWiring_MarkUpdateDoneUnblocks(t *testing.T) {
	c := &Channel{}
	c.offTrk = newOffsetTracker(200)
	c.offTrk.Register(201)
	c.offTrk.Register(202)
	c.markUpdateDone(202) // e.g. /status — handled, never persisted
	c.markUpdateDone(201)
	if got := c.offTrk.Committed(); got != 202 {
		t.Fatalf("committed = %d, want 202 (gated 202 must not block once 201 done)", got)
	}
}
```

- [ ] **Step 10b (5c): Run it to verify it fails**

Run: `go test ./internal/channel/telegram/ -run PollOffsetWiring -v`
Expected: FAIL — `Channel.offTrk`, `Channel.msgToUpdate`, `Channel.mu`, `(*Channel).markUpdateDone` undefined.

- [ ] **Step 10c (5c): Wire the offset tracker into the poll loop** — concrete field additions + method + call sites:

  - **Struct fields** — in `internal/channel/telegram/telegram.go`, add to the `Channel` struct (the struct currently has only atomics like `activeEndpoint`/`conflictActive` and **no plain mutex** — verified):

```go
	// Persisted-offset wiring (Component 2). offTrk advances the committed
	// offset only over durably-persisted (or no-op) updates. msgToUpdate maps a
	// stored inbound's MessageID back to its source update_id so the broker's
	// persist callback can MarkDone the right update. mu guards msgToUpdate.
	mu          sync.Mutex
	offTrk      *offsetTracker
	msgToUpdate map[int64]int64
```

  (add `"sync"` to `telegram.go`'s imports if not already present.)

  - **Init in `Start`** — after `c.offsets` is created, seed the tracker + map and register the persist callback:

```go
	loaded, _ := c.offsets.Load()
	c.offTrk = newOffsetTracker(loaded)
	c.msgToUpdate = map[int64]int64{}
	if bh, ok := host.(interface{ SetPersistedCallback(func(*c3types.Inbound)) }); ok {
		bh.SetPersistedCallback(func(in *c3types.Inbound) {
			c.mu.Lock()
			uid, found := c.msgToUpdate[in.MessageID]
			if found {
				delete(c.msgToUpdate, in.MessageID)
			}
			c.mu.Unlock()
			if found {
				c.offTrk.MarkDone(uid)
			}
		})
	}
```

  - **`markUpdateDone` method** — add to `poll.go` (or `telegram.go`):

```go
// markUpdateDone marks an update done in the persisted-offset tracker for every
// NON-persist outcome (gated, dropped, non-message, pair-consumed, dedup-skip,
// /status). Nil-safe so the early-build (5b) and non-telegram paths are no-ops.
func (c *Channel) markUpdateDone(updateID int64) {
	if c.offTrk != nil {
		c.offTrk.MarkDone(updateID)
	}
}
```

  - **`pollLoop` call sites** — for each update in a fetched batch, BEFORE `dispatchGuarded`, call `c.offTrk.Register(u.UpdateId)`. After processing the batch, replace the current `c.offsets.Save(offset-1)` advance with `if cur := c.offTrk.Committed(); cur > lastSaved { _ = c.offsets.Save(cur); lastSaved = cur }`. The next `getUpdates` offset becomes `c.offTrk.Committed() + 1` (replace the in-memory `offset` used for the next fetch with `c.offTrk.Committed() + 1`).

  - **`dispatchMessage` record + outcomes** — before `c.host.Emit(in)`, record the seam: `c.mu.Lock(); c.msgToUpdate[in.MessageID] = updateID; c.mu.Unlock()`. On every early-return that is NOT a persist (the `/status` intercept return in Step 8, the `GateInboundDrop` return, and the `GateInboundPairConsumed` return), call `c.markUpdateDone(updateID)` before returning. In `emitEvent` (events are never persisted/queued), call `c.markUpdateDone(updateID)` as well.

  This wiring leans on the already-tested `offsetTracker` (Task 3) and `Store.Append` (Task 2); the new `offset_wiring_test.go` (Step 10/10b) guards the integration seam, so 5c has a real red step.

- [ ] **Step 11 (5c): Run telegram + broker suites + the new wiring test**

Run: `go test ./internal/channel/telegram/ -run PollOffsetWiring -v && go test ./internal/channel/telegram/ && go test ./internal/broker/ && go build ./...`
Expected: PASS, build green. (Update any existing telegram poll test that asserted the exact old `offset` Save value — it now saves `Committed()`, which for the all-contiguous-done case equals the same highest id, so most tests are unaffected; adjust the few that assert intermediate Save calls.)

- [ ] **Step 12 (5c): Commit 5c** (poll-loop persisted-offset wiring)

```bash
git add internal/channel/telegram/poll.go internal/channel/telegram/telegram.go internal/channel/telegram/offset_wiring_test.go
git commit -m "$(printf 'feat(telegram): advance offset only after durable persist (persisted-offset tracker wiring)\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 6: Backlog summary on attach

**Files:**
- Modify: `internal/broker/attach.go` (`tryClaim` success path fills `QueuedCount` + `QueuedSummary`; the per-attach-branch `AttachedMsg` writes carry them)
- Test: `internal/broker/attach_backlog_test.go` (new)

**Interfaces:**
- Consumes: `Broker.Queue.Peek/Pending`, `ipc.QueuedItem`, `ipc.AttachedMsg.QueuedCount/QueuedSummary`.
- Produces: `(*Broker).backlogSummary(key RouteKey) (count int, items []ipc.QueuedItem)` — consumed by the attach-success writers (and by Task 9/10 adapters via the AttachedMsg).

Rather than touch all six `AttachedMsg{OK:true}` writers, add a single helper that the success paths call and a tiny wrapper that stamps the fields onto a constructed message.

- [ ] **Step 1: Write the failing test** — create `internal/broker/attach_backlog_test.go`:

```go
package broker

import (
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/queue"
)

func TestBacklogSummary_PeeksOldestWithoutConsuming(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 5; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{
			Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i,
			Sender: c3types.Sender{Username: "k"}, Text: "msg", Timestamp: time.Now(),
		})
	}
	count, items := b.backlogSummary(key)
	if count != 5 {
		t.Fatalf("backlog count = %d, want 5", count)
	}
	if len(items) == 0 || len(items) > 3 {
		t.Fatalf("summary items = %d, want 1..3 (compact preview)", len(items))
	}
	if items[0].MessageID != 1 {
		t.Errorf("first summary item msg = %d, want 1 (oldest)", items[0].MessageID)
	}
	// Peek must NOT consume.
	if n, _ := b.Queue.Pending(qrk); n != 5 {
		t.Errorf("backlogSummary consumed the queue; pending=%d, want 5", n)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/broker/ -run BacklogSummary -v`
Expected: FAIL — `b.backlogSummary` undefined.

- [ ] **Step 3: Implement the helper** — append to `internal/broker/attach.go`:

```go
// backlogSummaryMax bounds the compact attach-time preview (full content comes
// via fetch_queue). Three rows keep the on-attach notification short.
const backlogSummaryMax = 3

// backlogSummary returns the total queued count and a compact preview (oldest up
// to backlogSummaryMax) for the just-claimed route. Peek only — never consumes;
// the agent drains via fetch_queue. Empty/zero when nothing is queued or the
// queue is disabled.
func (b *Broker) backlogSummary(key RouteKey) (int, []ipc.QueuedItem) {
	if b.Queue == nil {
		return 0, nil
	}
	qrk := queueRouteKey(key)
	count, _ := b.Queue.Pending(qrk)
	if count == 0 {
		return 0, nil
	}
	preview, err := b.Queue.Peek(qrk, backlogSummaryMax)
	if err != nil {
		log.Printf("backlog summary peek FAIL %s: %v", routeKeyStr(key), err)
		return count, nil
	}
	items := make([]ipc.QueuedItem, 0, len(preview))
	for i := range preview {
		in := &preview[i]
		items = append(items, ipc.QueuedItem{
			MessageID: in.MessageID,
			Sender:    senderLabel(in.Sender),
			Kind:      inboundKindLabel(in),
			Unix:      in.Timestamp.Unix(),
			Preview:   previewText(in, 80),
		})
	}
	return count, items
}

// senderLabel renders a compact sender label for the backlog preview.
func senderLabel(s c3types.Sender) string {
	if s.Username != "" {
		return "@" + s.Username
	}
	if s.UserID != 0 {
		return fmt.Sprintf("uid=%d", s.UserID)
	}
	return ""
}

// inboundKindLabel returns "text" or the first attachment kind / event kind.
func inboundKindLabel(in *c3types.Inbound) string {
	if in.IsEvent() {
		return string(in.Kind)
	}
	if len(in.Attachments) > 0 && in.Attachments[0].Kind != "" {
		return in.Attachments[0].Kind
	}
	return "text"
}

// previewText returns a rune-safe truncated snippet of an inbound's text.
func previewText(in *c3types.Inbound, n int) string {
	r := []rune(in.Text)
	if len(r) <= n {
		return in.Text
	}
	return string(r[:n]) + "…"
}
```

(add `"github.com/karthikeyan5/c3/internal/ipc"` to `attach.go`'s imports — it is already imported. `fmt`, `log` already imported.)

- [ ] **Step 4: Stamp the fields onto the success responses** — in `tryClaim`, after `stub.SetRoute(&key)` and before the `if isFresh { go b.sendWelcome(...) }`, the function currently returns bool; the actual `AttachedMsg{OK:true}` is written by each caller. The minimal, DRY change: add a thin helper the success writers use. Add to `attach.go`:

```go
// withBacklog returns msg with the route's queued-count + compact summary
// stamped in (no-op when nothing is queued). Call it on every OK=true attach
// response so a session learns of held messages immediately.
func (b *Broker) withBacklog(key RouteKey, msg ipc.AttachedMsg) ipc.AttachedMsg {
	count, items := b.backlogSummary(key)
	msg.QueuedCount = count
	msg.QueuedSummary = items
	return msg
}
```

Then wrap each `OK:true` `AttachedMsg` literal in the attach flow with `b.withBacklog(key, ...)`. The success writers are in `attachDM`, `attachByTopicID`, the saved-mapping branch + default-group branch of `attachByName`, and `createAndClaim`. Example (saved-mapping branch):

```go
				_ = conn.WriteJSON(b.withBacklog(key, ipc.AttachedMsg{
					Op: ipc.OpAttached, OK: true,
					Status:  ipc.AttachStatusOK,
					Channel: chanName, ChatID: m.ChatID, TopicID: tidPtr,
					Name: m.Name, Group: m.Group,
					Capabilities: b.capsForChannel(chanName),
				}))
```

Apply the same `b.withBacklog(<the branch's key var>, …)` wrap to each `OK:true` writer. (`attachDM` uses `key := MakeRouteKey(chanName, cc.DMChatID, nil)`; `attachByTopicID` uses `key`; `createAndClaim` uses `key`.)

- [ ] **Step 5: Run the test + broker suite**

Run: `go test ./internal/broker/ -run 'BacklogSummary|Attach' -v && go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/attach.go internal/broker/attach_backlog_test.go
git commit -m "$(printf 'feat(broker): backlog count + compact summary on attach response\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 7: Broker dispatch handlers for `OpFetchQueue` + `OpRetranscribe`

**Files:**
- Create: `internal/broker/queue_dispatch.go` (handlers)
- Modify: `internal/broker/handler.go` (route the two ops + `OpInboundDelivered`)
- Test: `internal/broker/queue_dispatch_test.go` (new)

**Interfaces:**
- Consumes: `ipc.FetchQueueReq/Resp`, `ipc.RetranscribeReq/Resp`, `ipc.InboundDeliveredMsg`, `JobFetch`/`FetchResult`, `JobConsume`, `Broker.Queue`, `Broker.Plugins.FireOnVoiceReceived`, `Broker.Channel(...).DownloadAttachment`.
- Produces:
  - `(*Broker).handleFetchQueue(conn, stub, raw)`
  - `(*Broker).handleRetranscribe(conn, stub, raw)`
  - `(*Broker).handleInboundDelivered(stub, raw)`

`fetch_queue` routes through the claimed route's worker (`JobFetch`) so file ops stay single-owner. `retranscribe` re-runs `Plugins.FireOnVoiceReceived` on the `file_id` (downloading via the channel if needed) and returns the fresh transcript.

> **SHIPPED (resolution 1 — implemented).** Spec Component 5 (line 151)'s optional `message_id` in-place refresh IS implemented and tested. `handleRetranscribe` submits a `JobRefreshText{MessageID, NewText}` worker job (single-owner, reusing the store's cap-safe `rewrite` path) when `req.MessageID != 0` and the message is still queued: the matching still-queued line's `Text` is rewritten in place, so a later `fetch_queue` returns the corrected transcript rather than the STT-failure placeholder. A `message_id` that is never-queued / already-consumed is a clean no-op and the transcript is still returned. (The earlier "KNOWN DIVERGENCE / v1 no-op / deferred pending Karthi" note is obsolete — this was built, not deferred. See `internal/queue/store.go` `RefreshText` and `internal/broker/queue_dispatch.go` `handleRetranscribe`.)

- [ ] **Step 1: Write the failing test** — create `internal/broker/queue_dispatch_test.go`:

```go
package broker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/queue"
)

func TestHandleFetchQueue_ConsumesOldest(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 4; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	// Stub holding the route. claimedHolder calls Routes.Claim but does NOT set
	// the stub's CurrentRoute; handleFetchQueue resolves the route via
	// stub.CurrentRoute(), so set it explicitly (mirroring the retranscribe test
	// below) — otherwise the handler returns the no-route Err branch and the
	// assertions below fail.
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	agentSide, brokerSide := newConnPair(t)
	_ = brokerSide
	req := ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Limit: 2, Ack: true}
	raw, _ := json.Marshal(req)
	go b.handleFetchQueue(brokerSide, stub, raw)

	resp := readFetchResp(t, agentSide)
	if len(resp.Messages) != 2 || resp.Messages[0].MessageID != 1 {
		t.Fatalf("fetch_queue returned %+v, want 2 oldest", resp.Messages)
	}
	if resp.Remaining != 2 {
		t.Fatalf("remaining = %d, want 2", resp.Remaining)
	}
	if n, _ := b.Queue.Pending(qrk); n != 2 {
		t.Fatalf("ack=true should consume; pending=%d, want 2", n)
	}
}

func TestHandleFetchQueue_NoRoute(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	stub := &Stub{CLI: "claude"} // no route claimed
	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Ack: true})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if resp.Err == "" {
		t.Fatal("fetch_queue before attach should return an Err")
	}
}

// ack=false PEEKS: returns the oldest batch WITHOUT advancing the cursor, and
// Remaining reflects what is still queued after this (non-consuming) batch.
func TestHandleFetchQueue_PeekDoesNotConsume(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 4; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Limit: 2, Ack: false})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if len(resp.Messages) != 2 || resp.Messages[0].MessageID != 1 {
		t.Fatalf("peek returned %+v, want 2 oldest", resp.Messages)
	}
	if resp.Remaining != 2 {
		t.Fatalf("peek remaining = %d, want 2 (after this non-consuming batch of 2)", resp.Remaining)
	}
	if n, _ := b.Queue.Pending(qrk); n != 4 {
		t.Fatalf("ack=false must NOT consume; pending=%d, want 4", n)
	}
}

func TestHandleRetranscribe_ReRunsSTT(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	b.Plugins.OnVoiceReceived(func(_ context.Context, p c3types.VoicePayload) (string, error) {
		if p.FileID == "vf" {
			return "fresh transcript", nil
		}
		return "", nil
	})
	stub := &Stub{CLI: "claude"}
	stub.SetRoute(&RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 914})

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: "1", FileID: "vf"})
	go b.handleRetranscribe(brokerSide, stub, raw)
	resp := readRetranscribeResp(t, agentSide)
	if resp.Text != "fresh transcript" {
		t.Fatalf("retranscribe text = %q, want 'fresh transcript'", resp.Text)
	}
}

// NOTE (post-ship): this sample pinned the ORIGINALLY-PLANNED v1 behavior where
// message_id was accepted-but-ignored. The in-place refresh WAS subsequently
// implemented (resolution 1 above), so the shipped suite supersedes this: a
// still-queued message_id is now refreshed in place (RefreshText), and the
// shipped tests assert the refresh hit/miss directly. A message_id that is
// absent / already-consumed remains a clean no-op that still returns the
// transcript, which this sample's assertion (no error + transcript returned)
// still illustrates.
func TestHandleRetranscribe_MessageIDIsNoOp(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	b.Plugins.OnVoiceReceived(func(_ context.Context, p c3types.VoicePayload) (string, error) {
		return "fresh transcript", nil
	})
	stub := &Stub{CLI: "claude"}
	stub.SetRoute(&RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 914})

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: "2", FileID: "vf", MessageID: 999})
	go b.handleRetranscribe(brokerSide, stub, raw)
	resp := readRetranscribeResp(t, agentSide)
	if resp.Err != "" {
		t.Fatalf("retranscribe with message_id should not error; got %q", resp.Err)
	}
	if resp.Text != "fresh transcript" {
		t.Fatalf("retranscribe text = %q, want 'fresh transcript' (message_id is a no-op)", resp.Text)
	}
}
```

A merged Claude push covers N stored lines but acks once — the ack must consume all N off the head, not 1 (otherwise N-1 are orphaned as phantom backlog). This pins the merged-batch consume:

```go
func TestHandleInboundDelivered_MergedBatchConsumesAllCovered(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 5; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	// b.Workers.Submit lazily spawns the route worker (WorkerPool.Submit), so the
	// JobConsume the handler submits runs on a live worker — no manual worker
	// setup needed.
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	// A merged push of 3 lines, acked once with Count=3.
	raw, _ := json.Marshal(ipc.InboundDeliveredMsg{Op: ipc.OpInboundDelivered, UpdateID: 3, OK: true, Count: 3})
	b.handleInboundDelivered(stub, raw)

	// Poll until the async JobConsume drains 3 (oldest 1,2,3), leaving 4,5.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := b.Queue.Pending(qrk); n == 2 {
			break
		}
		if time.Now().After(deadline) {
			n, _ := b.Queue.Pending(qrk)
			t.Fatalf("merged ack(Count=3) should consume 3; pending=%d, want 2", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := b.Queue.Peek(qrk, 5)
	if len(got) != 2 || got[0].MessageID != 4 {
		t.Fatalf("after merged ack, head=%+v, want msgs 4,5", got)
	}
}
```

(The assertion — 3 consumed via one Count=3 ack, 4&5 remain — is the C2 regression guard: a merged push must not orphan covered lines.)

Add test helpers at the bottom (reusing `net.Pipe` like `health_notify_test.go`):

```go
func newConnPair(t *testing.T) (agent, broker *ipc.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return ipc.NewConn(a), ipc.NewConn(b)
}

func readFetchResp(t *testing.T, c *ipc.Conn) ipc.FetchQueueResp {
	t.Helper()
	raw, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("read fetch resp: %v", err)
	}
	var r ipc.FetchQueueResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func readRetranscribeResp(t *testing.T, c *ipc.Conn) ipc.RetranscribeResp {
	t.Helper()
	raw, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("read retranscribe resp: %v", err)
	}
	var r ipc.RetranscribeResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
```

(add `"net"` to the test import block.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/broker/ -run 'HandleFetchQueue|HandleRetranscribe' -v`
Expected: FAIL — `handleFetchQueue` / `handleRetranscribe` undefined.

- [ ] **Step 3: Implement the handlers** — create `internal/broker/queue_dispatch.go`:

```go
package broker

import (
	"context"
	"encoding/json"
	"log"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// handleFetchQueue routes a fetch_queue pull through the claimed route's worker
// (single-owner file access). Limit default + max are clamped by the adapter;
// the broker honors All (drain everything) and Ack (consume vs peek).
func (b *Broker) handleFetchQueue(conn *ipc.Conn, stub *Stub, raw []byte) {
	var req ipc.FetchQueueReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.FetchQueueResp{Op: ipc.OpFetchQueueResult, Err: "malformed fetch_queue: " + err.Error()})
		return
	}
	route := stub.CurrentRoute()
	if route == nil {
		_ = conn.WriteJSON(ipc.FetchQueueResp{Op: ipc.OpFetchQueueResult, ID: req.ID, Err: "fetch_queue before attach: no route claimed"})
		return
	}
	resultCh := make(chan FetchResult, 1)
	job := Job{Kind: JobFetch, Fetch: &FetchJob{Limit: req.Limit, All: req.All, Ack: req.Ack, ResultCh: resultCh}}
	if !b.Workers.Submit(*route, job) {
		_ = conn.WriteJSON(ipc.FetchQueueResp{Op: ipc.OpFetchQueueResult, ID: req.ID, Err: "worker queue full or stopped"})
		return
	}
	res := <-resultCh
	resp := ipc.FetchQueueResp{Op: ipc.OpFetchQueueResult, ID: req.ID, Remaining: res.Remaining}
	if res.Err != nil {
		resp.Err = res.Err.Error()
	} else {
		resp.Messages = res.Messages
	}
	_ = conn.WriteJSON(resp)
}

// handleRetranscribe re-runs the STT provider chain on a cached voice
// attachment by file_id and returns the fresh transcript. Downloads via the
// channel are handled inside the STT plugin chain (it owns DownloadAttachment).
// SHIPPED: retranscribe returns the fresh transcript AND, when message_id is
// given and that message is still queued, refreshes its stored Text in place via
// a JobRefreshText worker job (spec Component 5 — implemented, not deferred). The
// body below is the originally-planned no-op sample; the shipped handler adds the
// in-place-refresh block and bounds the STT call with a timeout context. See
// internal/broker/queue_dispatch.go for the as-shipped code.
func (b *Broker) handleRetranscribe(conn *ipc.Conn, stub *Stub, raw []byte) {
	var req ipc.RetranscribeReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.RetranscribeResp{Op: ipc.OpRetranscribeResult, Err: "malformed retranscribe: " + err.Error()})
		return
	}
	if req.FileID == "" {
		_ = conn.WriteJSON(ipc.RetranscribeResp{Op: ipc.OpRetranscribeResult, ID: req.ID, Err: "retranscribe: file_id required"})
		return
	}
	route := stub.CurrentRoute()
	chanName := "telegram"
	var chatID int64
	var topicID *int64
	if route != nil {
		chanName = route.Channel
		chatID = route.ChatID
		if route.HasTopic {
			t := route.TopicID
			topicID = &t
		}
	}
	if b.Plugins == nil {
		_ = conn.WriteJSON(ipc.RetranscribeResp{Op: ipc.OpRetranscribeResult, ID: req.ID, Err: "no STT plugin registered"})
		return
	}
	transcript := b.Plugins.FireOnVoiceReceived(context.Background(), c3types.VoicePayload{
		Channel:   chanName,
		ChatID:    chatID,
		TopicID:   topicID,
		MessageID: req.MessageID,
		FileID:    req.FileID,
	})
	resp := ipc.RetranscribeResp{Op: ipc.OpRetranscribeResult, ID: req.ID}
	if transcript == "" {
		resp.Err = "retranscribe: STT provider still failing (no transcript)"
	} else {
		resp.Text = transcript
	}
	log.Printf("retranscribe chan=%s file_id=%s msg=%d ok=%v", chanName, req.FileID, req.MessageID, transcript != "")
	_ = conn.WriteJSON(resp)
}

// handleInboundDelivered consumes the oldest queued message for the stub's route
// after the Claude adapter acks a successful live push (OK=true). OK=false leaves
// it queued (backlog + recovery nudge). No response is sent — it is a one-way ack.
func (b *Broker) handleInboundDelivered(stub *Stub, raw []byte) {
	var msg ipc.InboundDeliveredMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("inbound_delivered: malformed: %v", err)
		return
	}
	if !msg.OK {
		log.Printf("inbound_delivered NACK update=%d — leaving queued (backlog)", msg.UpdateID)
		return
	}
	route := stub.CurrentRoute()
	if route == nil {
		return
	}
	count := msg.Count
	if count < 1 {
		count = 1 // older adapter / single-message push
	}
	b.Workers.Submit(*route, Job{Kind: JobConsume, Consume: &ConsumeJob{MessageID: msg.UpdateID, Count: count}})
}
```

- [ ] **Step 4: Route the ops** — in `internal/broker/handler.go`, add to the dispatch `switch op`:

```go
		case ipc.OpFetchQueue:
			b.handleFetchQueue(conn, stub, raw)
		case ipc.OpRetranscribe:
			b.handleRetranscribe(conn, stub, raw)
		case ipc.OpInboundDelivered:
			b.handleInboundDelivered(stub, raw)
```

- [ ] **Step 5: Run the test + suite**

Run: `go test ./internal/broker/ -run 'HandleFetchQueue|HandleRetranscribe' -v && go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/queue_dispatch.go internal/broker/handler.go internal/broker/queue_dispatch_test.go
git commit -m "$(printf 'feat(broker): fetch_queue, retranscribe, inbound_delivered IPC handlers\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 8: STT self-documenting failure text

**Files:**
- Modify: `internal/broker/worker.go` (replace the bare STT-failed marker with a self-documenting recovery message)
- Test: `internal/broker/worker_test.go` (update the existing marker assertion + add a richer assertion)

**Interfaces:**
- Produces: `sttFailureText(in *c3types.Inbound, reason string) string` — the agent-facing recovery instruction including `file_id`, mime, duration, and the `download_attachment` + `retranscribe` next-steps.

- [ ] **Step 1: Write/adjust the failing test** — in `internal/broker/worker_test.go`, replace the body of `TestFlushInbounds_VoiceWithoutSTTPluginGetsMarker` assertion with the new contract and add a content test:

```go
func TestFlushInbounds_VoiceWithoutSTTPluginGetsSelfDocumentingFailure(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -100, MessageID: 42,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VFILE", MIME: "audio/ogg", Size: 1000}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	for _, want := range []string{"transcription failed", "VFILE", "download_attachment", "retranscribe", "does not need to resend"} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("STT failure text missing %q; got %q", want, in.Text)
		}
	}
}
```

Delete the old `TestFlushInbounds_VoiceWithoutSTTPluginGetsMarker` (its `[STT FAILED:` assertion is superseded). Keep `TestFlushInbounds_VoiceWithCaptionKeepsCaptionWhenSTTAbsent` (caption still wins) and `TestFlushInbounds_VoiceWithSTTPluginUsesTranscript` unchanged.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/broker/ -run 'SelfDocumentingFailure|VoiceWith' -v`
Expected: FAIL — text doesn't contain the new strings.

- [ ] **Step 3: Implement the message** — in `internal/broker/worker.go`, replace the STT no-transcript branch in `flushInbounds`:

Replace:
```go
				case in.Text == "":
					// Defense-in-depth: ...
					in.Text = w.sttPrefix(in.Channel) + "[STT FAILED: no_transcript_plugin — see " + LogPath() + "]"
```
with:
```go
				case in.Text == "":
					// Self-documenting failure: the text the AGENT sees becomes a
					// recovery instruction (it names the file_id + how to fetch /
					// retry), not a dead end. The audio is durably queued and
					// recoverable; the user never re-forwards.
					in.Text = sttFailureText(in, "no_transcript")
```

Add the helper:

```go
// sttFailureText renders the agent-facing STT-failure recovery message. It is
// self-documenting: the agent learns the audio exists, exactly how to fetch it
// (download_attachment), that it can retry transcription (retranscribe), and
// that the user does NOT need to resend. Includes file_id, mime, and duration
// when known. See broker.log (LogPath) for the provider traceback.
func sttFailureText(in *c3types.Inbound, reason string) string {
	fileID, mime, dur := "", "", ""
	if len(in.Attachments) > 0 {
		fileID = in.Attachments[0].FileID
		mime = in.Attachments[0].MIME
	}
	if mime == "" {
		mime = "audio"
	}
	dur = "duration unknown"
	return fmt.Sprintf("⚠️ [voice transcription failed: %s] The audio is saved and recoverable — the user does not need to resend. Call download_attachment with file_id=%q (%s, %s) to retrieve it, or retranscribe with the same file_id to re-run transcription. Provider traceback: %s",
		reason, fileID, mime, dur, LogPath())
}
```

(The `duration` field is not currently carried on `c3types.Attachment`; the message uses "duration unknown" so it remains self-documenting without a type change. If a future task adds `Attachment.DurationSec`, swap it in here.)

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/broker/ -run 'SelfDocumentingFailure|VoiceWith' -v && go test ./internal/broker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/worker.go internal/broker/worker_test.go
git commit -m "$(printf 'feat(broker): self-documenting STT-failure text (file_id + download_attachment + retranscribe)\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 9: Claude adapter — fetch_queue + retranscribe tools, delivered ack, backlog summary, recovery nudge

**Files:**
- Modify: `cmd/c3-claude-adapter/main.go`

**Interfaces:**
- Consumes: `ipc.FetchQueueReq/Resp`, `ipc.RetranscribeReq/Resp`, `ipc.InboundDeliveredMsg`, `ipc.AttachedMsg.QueuedCount/QueuedSummary`, `ipc.OpFetchQueueResult`, `ipc.OpRetranscribeResult`.
- Produces: `fetch_queue` + `retranscribe` MCP tools; an `OpInboundDelivered{ok:true}` ack on every accepted push; a backlog summary rendered into the attach result; a "(N pending — call fetch_queue)" nudge appended to pushes + attach when undelivered messages exist.

- [ ] **Step 1: Write a failing adapter test** — adapter logic is mostly IO glue; add a unit test for the pure pieces. Create/append `cmd/c3-claude-adapter/backlog_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/ipc"
)

func TestRenderBacklogSummary(t *testing.T) {
	got := renderBacklogSummary(2, []ipc.QueuedItem{
		{MessageID: 5, Sender: "@k", Kind: "text", Preview: "hello"},
		{MessageID: 6, Sender: "@k", Kind: "voice", Preview: ""},
	})
	// Assert per-ITEM content, not just any '2': both items' message_ids, the
	// text preview, and the voice item's "(voice)" fallback must render, plus the
	// fetch_queue hint. This catches a broken item-rendering loop.
	for _, want := range []string{"fetch_queue", "[5]", "hello", "[6]", "(voice)"} {
		if !strings.Contains(got, want) {
			t.Errorf("backlog summary missing %q; got %q", want, got)
		}
	}
}

// count > len(items) must render the "…and N more" truncation line.
func TestRenderBacklogSummary_AndMore(t *testing.T) {
	got := renderBacklogSummary(5, []ipc.QueuedItem{
		{MessageID: 1, Sender: "@k", Kind: "text", Preview: "a"},
		{MessageID: 2, Sender: "@k", Kind: "text", Preview: "b"},
		{MessageID: 3, Sender: "@k", Kind: "text", Preview: "c"},
	})
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("backlog summary = %q, want '…and 2 more' truncation line", got)
	}
}

func TestRenderBacklogSummary_Empty(t *testing.T) {
	if got := renderBacklogSummary(0, nil); got != "" {
		t.Errorf("empty backlog summary = %q, want empty string", got)
	}
}

func TestPendingNudge(t *testing.T) {
	if got := pendingNudge(3); !strings.Contains(got, "3 pending") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("pendingNudge(3) = %q", got)
	}
	if got := pendingNudge(0); got != "" {
		t.Errorf("pendingNudge(0) = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/c3-claude-adapter/ -run 'Backlog|PendingNudge' -v`
Expected: FAIL — `renderBacklogSummary` / `pendingNudge` undefined.

- [ ] **Step 3: Add the pure renderers** — in `cmd/c3-claude-adapter/main.go`:

```go
// renderBacklogSummary renders the on-attach backlog notification text. Empty
// string when nothing is queued. Instructs the agent to call fetch_queue.
func renderBacklogSummary(count int, items []ipc.QueuedItem) string {
	if count <= 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📨 %d message(s) were held while no session was attached. Call `fetch_queue` (limit:3 or \"all\") to retrieve them.", count)
	for _, it := range items {
		preview := it.Preview
		if preview == "" {
			preview = "(" + it.Kind + ")"
		}
		fmt.Fprintf(&sb, "\n  • [%d] %s %s: %s", it.MessageID, it.Sender, it.Kind, preview)
	}
	if count > len(items) {
		fmt.Fprintf(&sb, "\n  …and %d more", count-len(items))
	}
	return sb.String()
}

// pendingNudge returns a "(N pending — call fetch_queue)" suffix, or "" when
// nothing is pending. Appended to pushes + the attach summary so Claude can
// always recover even after a failed push.
func pendingNudge(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("(%d pending — call `fetch_queue`)", n)
}
```

- [ ] **Step 4: Append the recovery nudge to the push, then ack** — two changes in `handleInbound`:

  **(a) Push-path recovery nudge (spec Component 3 — the push half of the recovery net).** After `frame := buildClaudeChannelFrame(&in.Inbound)` and BEFORE the `a.notifyTx.Notify(...)` call, append the nudge to the frame's `content` string when the broker reported a backlog (`in.Pending > 0`). This surfaces a stuck backlog item on the next successful push, not only at the next re-attach:

```go
	if nudge := pendingNudge(in.Pending); nudge != "" {
		if s, ok := frame["content"].(string); ok {
			frame["content"] = s + "\n\n" + nudge
		}
	}
```

  **(b) Delivered ack.** After a SUCCESSFUL `a.notifyTx.Notify(...)` (the `log.Printf("notified ...")` path), send the delivered ack so the broker consumes the queued copy:

```go
	// Tell the broker we accepted this push so it Consumes the queued copy/copies.
	// Echo Covered back as Count so a MERGED push of N stored lines consumes all N
	// (not just 1, which would orphan N-1 as phantom backlog). This is broker↔
	// adapter plumbing the agent never sees (lifecycle B).
	if conn := a.currentConn(); conn != nil {
		_ = conn.WriteJSON(ipc.InboundDeliveredMsg{Op: ipc.OpInboundDelivered, UpdateID: in.Inbound.MessageID, OK: true, Count: in.Covered})
	}
```

(On the notify-FAIL branch, do NOT ack — the message stays queued as backlog, exactly as the recovery-nudge design requires.)

  **Add a unit test (in `cmd/c3-claude-adapter/backlog_test.go`) asserting a push carries the nudge when a backlog exists.** Since `handleInbound` is IO glue, extract the content-decoration into a tiny pure helper and test that:

```go
// decoratePushContent appends the recovery nudge to a push's content string when
// the broker reports remaining backlog. Pure + unit-testable.
func decoratePushContent(content string, pending int) string {
	if nudge := pendingNudge(pending); nudge != "" {
		return content + "\n\n" + nudge
	}
	return content
}
```

```go
func TestDecoratePushContent_CarriesNudgeOnBacklog(t *testing.T) {
	got := decoratePushContent("hello", 2)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "2 pending") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("push content = %q, want body + '2 pending' + fetch_queue nudge", got)
	}
	if got := decoratePushContent("hello", 0); got != "hello" {
		t.Errorf("no-backlog push content = %q, want unchanged 'hello'", got)
	}
}
```

(and have step (a) call `decoratePushContent` instead of inlining the branch: `if s, ok := frame["content"].(string); ok { frame["content"] = decoratePushContent(s, in.Pending) }`.)

- [ ] **Step 5: Register the two tools** — in `registerTools`, append two entries to the `tools` slice (before the closing `}`):

```go
		{
			tool: &mcp.Tool{
				Name:        "fetch_queue",
				Description: "Retrieve inbound Telegram messages held in the durable queue for the attached topic (messages that arrived while no session was attached, or that a live push didn't confirm). `limit` is how many oldest messages to pull (default 3; or pass the string \"all\" to drain everything). `ack` (default true) consumes them (advances the cursor); ack=false peeks without consuming. Drain all at once for bulk catch-up, or pull in small batches (default 3) to process carefully one group at a time. Returns full content (text/transcript, sender, attachments with file_id) plus how many remain.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"description": "integer (default 3, max 50) or the string \"all\""},
						"ack":   map[string]any{"type": "boolean", "default": true},
					},
				},
			},
			handler: a.toolFetchQueue,
		},
		{
			tool: &mcp.Tool{
				Name:        "retranscribe",
				Description: "Re-run speech-to-text on a voice message by file_id (downloading the audio if not cached) and return the fresh transcript. Use this after a '[voice transcription failed]' message once the STT provider is healthy again — the audio is saved, so the user never has to resend. Optional `message_id` refreshes the stored transcript when that message is still queued.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_id":    map[string]any{"type": "string"},
						"message_id": map[string]any{"type": "integer"},
					},
					"required": []string{"file_id"},
				},
			},
			handler: a.toolRetranscribe,
		},
```

- [ ] **Step 6: Implement the two tool handlers** — add to `main.go` (mirroring `toolForward`'s pending-channel pattern, but keyed by the request ID and reading the typed result; reuse the existing `a.pending`/`a.nextID` machinery by routing the typed responses through `dispatchToolResult`'s sibling). For clarity and to avoid overloading the `ToolResultMsg` pending map, give these their own pending slots keyed by `"fq:"+id` / `"rt:"+id` and add the two op cases to `brokerReader`:

```go
// toolFetchQueue forwards a fetch_queue pull to the broker and renders the
// returned messages. The agent sees full content; the broker advanced the
// cursor (ack=true) before replying.
func (a *adapter) toolFetchQueue(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	fq := ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: strconv.FormatUint(a.nextID.Add(1), 10), Ack: true}
	if v, ok := args["ack"].(bool); ok {
		fq.Ack = v
	}
	switch v := args["limit"].(type) {
	case string:
		if strings.EqualFold(v, "all") {
			fq.All = true
		}
	case float64:
		fq.Limit = int(v)
	}
	if !fq.All && fq.Limit <= 0 {
		fq.Limit = 3 // spec default
	}
	if fq.Limit > 50 {
		fq.Limit = 50
	}

	ch := make(chan ipc.FetchQueueResp, 1)
	a.fqmu.Lock()
	a.fqPending[fq.ID] = ch
	a.fqmu.Unlock()
	defer func() { a.fqmu.Lock(); delete(a.fqPending, fq.ID); a.fqmu.Unlock() }()

	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry fetch_queue in a moment"), nil
	}
	if err := conn.WriteJSON(fq); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case <-time.After(120 * time.Second):
		return toolErrorResult("fetch_queue timeout"), nil
	case resp := <-ch:
		if resp.Err != "" {
			return toolErrorResult(resp.Err), nil
		}
		return toolTextResult(renderFetchedMessages(resp.Messages, resp.Remaining)), nil
	}
}

// toolRetranscribe forwards a retranscribe request and returns the transcript.
func (a *adapter) toolRetranscribe(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	fileID, _ := args["file_id"].(string)
	if fileID == "" {
		return toolErrorResult("retranscribe: file_id is required"), nil
	}
	rt := ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: strconv.FormatUint(a.nextID.Add(1), 10), FileID: fileID}
	if v, ok := args["message_id"].(float64); ok {
		rt.MessageID = int64(v)
	}
	ch := make(chan ipc.RetranscribeResp, 1)
	a.rtmu.Lock()
	a.rtPending[rt.ID] = ch
	a.rtmu.Unlock()
	defer func() { a.rtmu.Lock(); delete(a.rtPending, rt.ID); a.rtmu.Unlock() }()

	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry retranscribe in a moment"), nil
	}
	if err := conn.WriteJSON(rt); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case <-time.After(120 * time.Second):
		return toolErrorResult("retranscribe timeout"), nil
	case resp := <-ch:
		if resp.Err != "" {
			return toolErrorResult(resp.Err), nil
		}
		return toolTextResult(resp.Text), nil
	}
}

// renderFetchedMessages turns pulled inbound into agent-readable text.
func renderFetchedMessages(msgs []ipc.QueuedItem, remaining int) string { return "" } // placeholder removed below
```

Replace the placeholder `renderFetchedMessages` above with the real one (it takes `[]c3types.Inbound`, not `QueuedItem`). Do **NOT** reuse `inboundContentSummary` for the body — that renderer prints attachments only as `attach=<kind>/<size>` and OMITS `file_id`, `mime`, and `name`, so the agent could not call `download_attachment`/`retranscribe` on a queued voice/media message (spec Component 4 requires attachments "each with `file_id`, `mime`, `size`, `name`"; this is load-bearing for the STT-failure recovery of backlog items, Component 6c). Use a backlog-specific renderer that spells the attachment fields out:

```go
// renderFetchedMessages turns pulled inbound into agent-readable text, one block
// per message with full content + each attachment's file_id/mime/size/name so
// the agent can act on backlog voice/media (download_attachment / retranscribe).
func renderFetchedMessages(msgs []c3types.Inbound, remaining int) string {
	if len(msgs) == 0 {
		return "c3 queue is empty"
	}
	blocks := make([]string, 0, len(msgs))
	for i := range msgs {
		blocks = append(blocks, renderQueuedInbound(&msgs[i]))
	}
	out := strings.Join(blocks, "\n\n")
	if remaining > 0 {
		out += "\n\n" + pendingNudge(remaining)
	}
	return out
}

// renderQueuedInbound renders one queued message for fetch_queue output. Unlike
// inboundContentSummary (notify-FAIL log line), this exposes the full attachment
// metadata the agent needs to recover backlog media: file_id, mime, size, name.
func renderQueuedInbound(in *c3types.Inbound) string {
	var parts []string
	switch {
	case in.Sender.Username != "":
		parts = append(parts, "from=@"+in.Sender.Username)
	case in.Sender.UserID != 0:
		parts = append(parts, fmt.Sprintf("from=uid=%d", in.Sender.UserID))
	}
	if in.MessageID != 0 {
		parts = append(parts, fmt.Sprintf("message_id=%d", in.MessageID))
	}
	if in.Text != "" {
		parts = append(parts, fmt.Sprintf("text=%q", in.Text))
	}
	if in.ReplyTo != nil {
		parts = append(parts, fmt.Sprintf("reply_to=%d", in.ReplyTo.MessageID))
	}
	for _, att := range in.Attachments {
		parts = append(parts, fmt.Sprintf("attachment{kind=%s file_id=%q mime=%s size=%d name=%q}",
			att.Kind, att.FileID, att.MIME, att.Size, att.Name))
	}
	if in.IsEvent() {
		parts = append(parts, fmt.Sprintf("event=%s", in.Kind))
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " ")
}
```

Add a test (in `cmd/c3-claude-adapter/backlog_test.go`) asserting the rendered fetch_queue output contains the attachment's `file_id` and `mime`:

```go
func TestRenderFetchedMessages_ExposesAttachmentFileID(t *testing.T) {
	got := renderFetchedMessages([]c3types.Inbound{{
		Channel: "telegram", ChatID: -100, MessageID: 7,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VOICE123", MIME: "audio/ogg", Size: 2048, Name: "note.ogg"}},
	}}, 0)
	for _, want := range []string{"VOICE123", "audio/ogg", "message_id=7"} {
		if !strings.Contains(got, want) {
			t.Errorf("fetch_queue render missing %q; got %q", want, got)
		}
	}
}
```

(`cmd/c3-claude-adapter/backlog_test.go` will need `"github.com/karthikeyan5/c3/internal/c3types"` in its import block for this case.)

Add the pending maps to the `adapter` struct (near the existing `pending map[string]chan ipc.ToolResultMsg` / `pmu`):

```go
	fqmu      sync.Mutex
	fqPending map[string]chan ipc.FetchQueueResp
	rtmu      sync.Mutex
	rtPending map[string]chan ipc.RetranscribeResp
```

Initialize them where the adapter is constructed (next to `pending: map[string]chan ipc.ToolResultMsg{}`):

```go
		fqPending: map[string]chan ipc.FetchQueueResp{},
		rtPending: map[string]chan ipc.RetranscribeResp{},
```

In `brokerReader`'s `switch op`, add:

```go
		case ipc.OpFetchQueueResult:
			a.dispatchFetchQueueResult(raw)
		case ipc.OpRetranscribeResult:
			a.dispatchRetranscribeResult(raw)
```

And the two dispatchers:

```go
func (a *adapter) dispatchFetchQueueResult(raw []byte) {
	var resp ipc.FetchQueueResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.fqmu.Lock()
	ch, ok := a.fqPending[resp.ID]
	if ok {
		delete(a.fqPending, resp.ID)
	}
	a.fqmu.Unlock()
	if ok {
		ch <- resp
	}
}

func (a *adapter) dispatchRetranscribeResult(raw []byte) {
	var resp ipc.RetranscribeResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.rtmu.Lock()
	ch, ok := a.rtPending[resp.ID]
	if ok {
		delete(a.rtPending, resp.ID)
	}
	a.rtmu.Unlock()
	if ok {
		ch <- resp
	}
}
```

- [ ] **Step 7: Render the backlog summary on attach** — in `toolAttach`, after `attached, _ := res.Result["_attached"].(ipc.AttachedMsg)` and the `attached.OK` branch, append the backlog summary to the returned text when present:

```go
		text := ipc.FormatAttached(&attached)
		if summary := renderBacklogSummary(attached.QueuedCount, attached.QueuedSummary); summary != "" {
			text += "\n\n" + summary
		}
		return toolTextResult(text), nil
```

(replace the existing `return toolTextResult(ipc.FormatAttached(&attached)), nil`.)

- [ ] **Step 8: Run the tests + build**

Run: `go test ./cmd/c3-claude-adapter/ -v && go build ./...`
Expected: PASS, build green.

- [ ] **Step 9: Commit**

```bash
git add cmd/c3-claude-adapter/main.go cmd/c3-claude-adapter/backlog_test.go
git commit -m "$(printf 'feat(claude-adapter): fetch_queue + retranscribe tools, delivered ack, backlog summary + recovery nudge\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 10: Codex adapter — broker-backed fetch_queue, retire ring, retranscribe, pending nudge

**Files:**
- Modify: `cmd/c3-codex-adapter/main.go`

**Interfaces:**
- Consumes: same IPC types as Task 9.
- Produces: `inbox` tool replaced by broker-backed `fetch_queue`; the in-memory cap-100 ring retired; `retranscribe` tool added; a "N pending" nudge via `notifications/message` on each inbound.

- [ ] **Step 1: Write a failing test** — create/append `cmd/c3-codex-adapter/queue_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func TestRenderFetchedMessages_Codex(t *testing.T) {
	got := renderFetchedMessages([]c3types.Inbound{
		{Channel: "telegram", ChatID: -100, MessageID: 1, Text: "hi", Sender: c3types.Sender{Username: "k"}},
	}, 2)
	if !strings.Contains(got, "hi") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("rendered = %q, want body + remaining nudge", got)
	}
}

// renderFetchedMessages must expose attachment file_id/mime so the agent can
// recover backlog voice via download_attachment/retranscribe (spec Component 4).
func TestRenderFetchedMessages_Codex_ExposesAttachmentFileID(t *testing.T) {
	got := renderFetchedMessages([]c3types.Inbound{{
		Channel: "telegram", ChatID: -100, MessageID: 7,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VOICE123", MIME: "audio/ogg", Size: 2048, Name: "note.ogg"}},
	}}, 0)
	for _, want := range []string{"VOICE123", "audio/ogg", "message_id=7"} {
		if !strings.Contains(got, want) {
			t.Errorf("fetch_queue render missing %q; got %q", want, got)
		}
	}
}

func TestPendingNudge_Codex(t *testing.T) {
	if got := pendingNudge(2); !strings.Contains(got, "2 pending") {
		t.Errorf("pendingNudge(2) = %q", got)
	}
	if got := pendingNudge(0); got != "" {
		t.Errorf("pendingNudge(0) = %q, want empty", got)
	}
}
```

(No `ipc` import — these tests exercise only the package-local render helpers + `c3types`. Do NOT add an unused-import sentinel; the `ipc` types are exercised by the broker handler tests, Task 7.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/c3-codex-adapter/ -run 'RenderFetchedMessages_Codex|PendingNudge_Codex' -v`
Expected: FAIL — `renderFetchedMessages` / `pendingNudge` undefined.

- [ ] **Step 3: Retire the ring** — in `cmd/c3-codex-adapter/main.go`:
  - Remove the `inbox []c3types.Inbound` field, the `inboxCap` const, and the `imu` mutex (and any helper that drains the ring).
  - In `handleInbound`, delete the ring append/cap block; keep the `notifications/message` log notify. Append the pending nudge: after a successful notify (or regardless, since Codex polls), Codex needs to learn a message is waiting. Replace the ring buffering with a "1 pending" nudge (Codex's delivery = a lightweight nudge + the agent calling fetch_queue):

```go
	// Codex cannot render unsolicited content reliably, so it polls. Replace the
	// retired in-memory ring with a lightweight "N pending" nudge — the durable
	// queue in the broker is the source of truth; the agent calls fetch_queue.
	if err := a.transport.Notify(context.Background(), "notifications/message", map[string]any{
		"data": "c3: new Telegram message — call `fetch_queue` to read it. " + pendingNudge(1),
	}); err != nil {
		log.Printf("notify FAIL chan=%s chat=%d msg=%d: %v — content durably queued; call fetch_queue. %s",
			msg.Inbound.Channel, msg.Inbound.ChatID, msg.Inbound.MessageID, err, inboundContentSummary(&msg.Inbound))
	}
```

  (Codex does NOT send `OpInboundDelivered` — its delivery model is pull-only; the cursor advances on `fetch_queue(ack=true)`. Live and backlog are the same path for Codex.)

- [ ] **Step 4: Replace the `inbox` tool with `fetch_queue` + add `retranscribe`** — the Codex adapter is a separate `package main`, so the fetch_queue/retranscribe plumbing cannot be imported from the Claude adapter; it is spelled out here explicitly (the pure renderers are byte-identical to Task 9's but must be redeclared in this package). The Codex `adapter` struct and `newAdapter` constructor differ from Claude's, so those additions are shown verbatim rather than "same as Task 9".

  **(4a) Struct fields** — in `cmd/c3-codex-adapter/main.go`, add to the `adapter` struct (alongside `pmu`/`pending`):

```go
	fqmu      sync.Mutex
	fqPending map[string]chan ipc.FetchQueueResp
	rtmu      sync.Mutex
	rtPending map[string]chan ipc.RetranscribeResp
```

  **(4b) Constructor init** — update `newAdapter` (it currently only inits `pending`):

```go
func newAdapter() *adapter {
	return &adapter{
		pending:   map[string]chan ipc.ToolResultMsg{},
		fqPending: map[string]chan ipc.FetchQueueResp{},
		rtPending: map[string]chan ipc.RetranscribeResp{},
	}
}
```

  **(4c) registerTools** — change the `inbox` entry to a `fetch_queue` entry (use the same description text as the Claude adapter from Task 9 Step 5) pointing `handler` at `a.toolFetchQueue`, and add a `retranscribe` entry identical to Claude's (Task 9 Step 5) pointing at `a.toolRetranscribe`. Delete the old `toolInbox` handler.

  **(4d) Tool handlers** — add `toolFetchQueue` and `toolRetranscribe` with the SAME bodies as Task 9 Step 6 (they reference only `a.nextID`, `a.currentConn()`, `a.fqPending`/`a.rtPending`, `decodeArgs`, `toolErrorResult`, `toolTextResult` — all of which exist in the Codex adapter too; confirm `decodeArgs`/`toolErrorResult`/`toolTextResult` names match the Codex package's helpers and adjust if they differ).

  **(4e) brokerReader op cases** — in the Codex `brokerReader`'s `switch op`, add:

```go
		case ipc.OpFetchQueueResult:
			a.dispatchFetchQueueResult(raw)
		case ipc.OpRetranscribeResult:
			a.dispatchRetranscribeResult(raw)
```

  **(4f) dispatchers + pure renderers** — add `dispatchFetchQueueResult`, `dispatchRetranscribeResult` (same bodies as Task 9 Step 6), and the pure renderers `renderFetchedMessages`, `renderQueuedInbound`, and `pendingNudge` (byte-identical copies of Task 9's — they are package-local; the Codex tests in Step 1 call them). Codex does **not** gain `decoratePushContent` or an `OpInboundDelivered` ack (its delivery is pull-only).

- [ ] **Step 5: Update instructions** — in `buildInstructions`, change the head strings from "Inbound … buffered for the `inbox` tool" / "`inbox` to drain buffered inbound" to reference `fetch_queue`:

```go
		head = fmt.Sprintf("No C3 mapping for %q. Use the `attach` tool to set one up. Inbound Telegram messages are held in C3's durable queue; call `fetch_queue` to read them.", cwd)
```
and
```go
		head = "C3 connected. Use `attach` to claim a Telegram topic, `fetch_queue` to read held/new inbound, `reply` to send. Codex doesn't render unsolicited MCP notifications today; call `fetch_queue` when you see a 'new Telegram message' nudge or periodically."
```

Also update the `HelloMsg` capabilities list from `"inbox"` to `"fetch_queue"`.

- [ ] **Step 6: Run the tests + build**

Run: `go test ./cmd/c3-codex-adapter/ -v && go build ./...`
Expected: PASS, build green. (If any existing codex test references `inbox`/`inboxCap`/`a.inbox`, update it to the fetch_queue path.)

- [ ] **Step 7: Full suite + race**

Run: `go test ./... && go test -race ./internal/queue/ ./internal/broker/ ./internal/channel/telegram/`
Expected: PASS, no races.

- [ ] **Step 8: Commit**

```bash
git add cmd/c3-codex-adapter/main.go cmd/c3-codex-adapter/queue_test.go
git commit -m "$(printf 'feat(codex-adapter): broker-backed fetch_queue (retire ring) + retranscribe + pending nudge\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 11: Docs — USAGE, CHANNELS, ROADMAP

> **No red-green cycle: documentation-only task.** There is no failing-then-passing test for prose; the `go build ./... && go test ./...` in Step 4 is a regression sanity check (confirm the doc edits did not touch code), not a TDD step. Do not look for a red step here.

**Files:**
- Modify: `docs/USAGE.md`
- Modify: `docs/CHANNELS.md`
- Modify: `ROADMAP.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: USAGE.md** — add a "Durable inbound queue & backlog" section documenting:
  - Messages received while no session is attached are held on disk (`$XDG_STATE_HOME/c3/queue/`), never dropped; the human never re-forwards.
  - On attach, the session is told how many messages are held and instructed to call `fetch_queue`.
  - `fetch_queue` params: `limit` (default 3, max 50, or `"all"`), `ack` (default true = consume, false = peek). Drain-all vs one-by-one.
  - `retranscribe(file_id, message_id?)` re-runs STT after a transient provider failure; the audio is saved so no resend.
  - `/status` (typed in Telegram) shows queue depth + attach state (per-topic in a topic; global in DM).
  - Caps: 1000 messages / 14 days, drop-oldest, with a Telegram notice (never silent).
  - 24h Telegram-retention bound: a >24h gap with no broker polling anywhere loses messages at Telegram's level (outside C3's control).

- [ ] **Step 2: CHANNELS.md** — under the Telegram section, document the `/status` bot command (registered via `setMyCommands`, autocompletes in the "/" menu; intercepted by the broker before gating, never routed to an agent) and that the offset advances only after durable persist.

- [ ] **Step 3: ROADMAP.md** — mark the durable-inbound-queue line as shipped (date 2026-06-22), linking the spec + this plan.

- [ ] **Step 4: Regression sanity check (NOT a red-green step) + read the docs once for accuracy.**

Run: `go build ./... && go test ./...`
Expected: PASS (no code change; this is a regression sanity check confirming the doc edits touched no code — there is no failing-then-passing test for documentation).

- [ ] **Step 5: Commit**

```bash
git add docs/USAGE.md docs/CHANNELS.md ROADMAP.md
git commit -m "$(printf 'docs(c3): document durable inbound queue, /status, fetch_queue, retranscribe\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

## Self-Review

**1. Spec coverage** — each spec component/decision maps to a task:
- Component 1 (durable store: append/peek/consume/pending/statusall/evict/recover; delete-on-empty; line-number cursor; caps 1000/14d; corrupt-line skip; -race single-owner) → **Task 2**.
- Component 2 (persisted-offset tracker: contiguous-prefix advance, gated/dropped immediate-done, crash-before-persist no advance; STT at flush time, per-message storage) → **Task 3** (tracker) + **Task 4** (per-message append at flush) + **Task 5c** (poll-loop wiring, with `offset_wiring_test.go` guarding the crash-before-persist seam — the safety property has a real red→green test, not "mechanical glue, no test").
- Component 3 (delivery per-adapter: Claude push + OpInboundDelivered consume; Codex pull-only) → **Task 4** (consume job + delivered semantics; `ConsumeJob.Count` so a MERGED push consumes all N covered lines, not 1), **Task 9** (Claude ack echoes `Covered`→`Count`; push-path recovery nudge via `InboundMsg.Pending`), **Task 10** (Codex pull).
- Component 4 (`fetch_queue`: limit default 3 / "all" / max 50, ack default true, returns content + remaining; drain-all vs batches in tool description) → **Task 1** (IPC), **Task 7** (handler), **Tasks 9/10** (tools).
- Component 5 (`retranscribe{file_id required, message_id optional}` re-runs FireOnVoiceReceived) → **Task 1**, **Task 7**, **Tasks 9/10**. **SHIPPED in full:** spec line 151's "refreshed in place" for a still-queued `message_id` IS implemented — `handleRetranscribe` submits a `JobRefreshText` worker job that rewrites the matching still-queued line's `Text` in place (cap-safe rewrite), tested by the store's `RefreshText` suite + the broker retranscribe-refresh tests. (The earlier "KNOWN DIVERGENCE / no-op / deferred pending Karthi" status is obsolete.)
- Component 6a (held-reply replacing the drop, running count, 5-min cooldown) → **Task 4**.
- Component 6b (`/status` in-topic + global, setMyCommands, never queued/routed, marked done) → **Task 5a** (renderer + interface) / **5b** (intercept + setMyCommands + `TestStatusCommand_NotRouted` asserting no Emit) / **5c** (offset wiring marks `/status` done).
- Component 6c (self-documenting STT-failure text with file_id + download_attachment + retranscribe) → **Task 8**.
- Backlog-on-attach (QueuedCount + QueuedSummary) → **Task 1** (fields) + **Task 6** (fill) + **Task 9** (render).
- Error/edge cases (push-fail stays queued + nudge → Tasks 4/9; crash-mid-STT no advance → Task 3 + Task 5c `offset_wiring_test.go`; crash-mid-consume at-least-once → Task 2 recovery test; **dedupe-by-message_id replay suppression → Task 4 Step 6b + `TestFlushInbounds_DedupesReplayedMessageID`**; cap overflow log+notice → Task 4; /status not queued/routed → Task 5b `TestStatusCommand_NotRouted`; corrupt line skip → Task 2; **disk-full = persist failure → no offset advance + best-effort Telegram notice via `notePersistFailure` → Task 4 append-fail branch**; **merged-push ack consumes all N covered lines → Task 7 `TestHandleInboundDelivered_MergedBatchConsumesAllCovered`**).
- Caps/cooldown/cursor/paths/defaults all use the spec's verbatim values (1000/14d, 5 min, line-number cursor, `$XDG_STATE_HOME/c3/queue/`, limit 3 / "all" / max 50, ack true).

**2. Placeholder scan** — every code step contains complete Go. The one literal "placeholder" line in Task 9 Step 6 (`renderFetchedMessages ... // placeholder removed below`) is IMMEDIATELY replaced by the real implementation in the same step, with an explicit instruction to replace it — no "TBD"/"handle errors" left dangling. Task 10's adapter code is spelled out explicitly (struct fields, constructor init, registerTools, handlers, dispatchers, renderers) rather than delegated by "same bodies as Task 9" — the only shared-by-copy items are the byte-identical pure renderers (Go has no cross-`main`-package sharing). The retranscribe in-place-refresh of a queued `message_id` was implemented (resolution 1): `handleRetranscribe` rewrites the still-queued line's `Text` via a `JobRefreshText` worker job, so the spec-line-151 guarantee holds. (The original "KNOWN DIVERGENCE / v1 no-op" wording elsewhere in this plan predates that and is superseded.)

**3. Type consistency** —
- `ipc.FetchQueueReq{Limit,All,Ack}` / `FetchQueueResp{Messages []c3types.Inbound, Remaining, Err}` consistent across Task 1 (def), Task 7 (handler), Tasks 9/10 (adapters).
- `ipc.RetranscribeReq{FileID,MessageID}` / `RetranscribeResp{Text,Err}` consistent across Tasks 1/7/9/10.
- `ipc.InboundDeliveredMsg{UpdateID,OK,Count}` consistent across Tasks 1/7/9; Claude acks `OK:true` with `Count` echoed from `InboundMsg.Covered` so a MERGED push of N stored lines consumes all N (the broker's `handleConsume` drops `Count` off the head, oldest-first — MessageID is audit-only, matching the spec's "live delivery is one-at-a-time in arrival order").
- `ipc.InboundMsg{Inbound, Pending, Covered}` (push frame) consistent across Tasks 1/4/9: Task 4 sets `Covered=len(batch)` + `Pending=remaining-covered`; Task 9 appends `pendingNudge(Pending)` to the push content (recovery nudge on the push path) and echoes `Covered`→`Count` on ack.
- `ipc.QueuedItem{MessageID,Sender,Kind,Unix,Preview}` + `AttachedMsg.QueuedCount/QueuedSummary` consistent across Tasks 1/6/9.
- `queue.RouteKey{Channel,ChatID,TopicID *int64}` + `.File()` + `queueRouteKey(broker.RouteKey)` bridge consistent across Tasks 2/4/5/6/7.
- `queue.Store` method set matches the spec's interface list verbatim (Append/Peek/Consume/Pending/StatusAll/EvictOverCap/RecoverOnStartup).
- `offsetTracker.{Register,MarkDone,Committed}` consistent across Task 3 (def) and Task 5 (poll-loop wiring).
- `JobFetch`/`JobConsume` + `FetchJob`/`FetchResult`/`ConsumeJob` consistent across Task 4 (def) and Task 7 (submitters).
- `heldReplyText(int)`, `sttFailureText(*Inbound,string)`, `renderBacklogSummary(int,[]QueuedItem)`, `pendingNudge(int)`, `renderFetchedMessages([]c3types.Inbound,int)` signatures consistent between their defining task and every caller.

**4. Dependency ordering** — strictly forward: Task 2 (queue) and Task 3 (tracker) depend only on `c3types`; Task 1 (IPC) is independent; Task 4 consumes Tasks 1+2; Task 5 consumes Tasks 2+3+4; Task 6 consumes Tasks 1+2+4; Task 7 consumes Tasks 1+4; Task 8 consumes Task 4 (worker); Tasks 9/10 consume Tasks 1+4+6+7+8; Task 11 is docs. No task references a type/func defined later.

**5. House-style** — table tests, `t.TempDir()`, `t.Setenv`, `net.Pipe` conn pairs, metadata-only success logging, content-bearing failure logging, atomic temp+rename + fsync for durable writes, recover-guarded worker methods, `*bool`-style nil-safe defaults where applicable — all mirror the existing codebase (`worker_test.go`, `health_notify_test.go`, `offset_store.go`, `connectivity-notifications` plan).
