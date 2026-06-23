# Auto-Attach on Session Resume — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Re-attach a resumed Claude Code session to the topic it was last attached to, automatically, keyed on `CLAUDE_CODE_SESSION_ID`.

**Architecture:** The broker keeps a `session_attachments` store (`sessionID → route`) in `mappings.json`. The adapter sends its `CLAUDE_CODE_SESSION_ID` on hello. The broker, during the hello handshake, if a valid stored attachment exists for that id and the route isn't held by another live session, claims it (low-level `Routes.Claim`) and reports it (+ held-backlog count) in the hello ack. Recovery takes precedence over the cwd mapping. The store is written on every attach and tombstoned on explicit `detach`.

**Tech Stack:** Go. Existing C3 broker/adapter/IPC/mappings packages. `go test -race`.

**Spec:** `docs/superpowers/specs/2026-06-23-c3-auto-attach-on-resume-design.md`.

## Global Constraints

- **Additive-omitempty IPC** — new IPC fields use `,omitempty`; older brokers/adapters must ignore them (no version field; single-host lockstep via `/c3:build`).
- **Backlog is PULL, not push** — claiming a route never flushes its queue; the agent pulls via `fetch_queue`. Recovery reports a count only.
- **Never silent-steal** — a route held by another LIVE session is detected with `heldByDifferentLiveSession` and recovery is SKIPPED (no claim), falling through to the normal flow. Only an explicit user `steal=true` ever evicts a live holder.
- **Precedence:** explicit user attach > session-id recovery > cwd mapping > NoMapping.
- **TTL:** `sessionAttachmentTTL = 30 * 24 * time.Hour`. Detach tombstones (`Detached=true`); a later attach clears it.
- **Storage:** `session_attachments` map in `mappings.json` (sibling of `mappings`).
- **No behavior change when `SessionID == ""`** (older Claude / Codex / tests): every new path is a no-op.
- Commit trailer verbatim: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Keep the tree green every task: `go build ./... && go vet ./... && go test -race ./...`.

## File Structure

- `internal/mappings/types.go` — `SessionAttachment` type + `MappingsFile.SessionAttachments` field + `Recoverable` method.
- `internal/mappings/mutate.go` — `UpsertSessionAttachment`, `LookupSessionAttachment`, `TombstoneSessionAttachment`, `PruneSessionAttachments`.
- `internal/ipc/messages.go` — `HelloMsg.SessionID`, `HelloAckMsg.QueuedCount`.
- `internal/broker/stubs.go` — `Stub.SessionID` + `Register` param.
- `internal/broker/handler.go` — pass `hello.SessionID` to `Register`; hello recovery block; `OpRelease` tombstone.
- `internal/broker/attach.go` — `persistMapping` records the session attachment; a `sessionAttachmentToWireMapping`/route helper; `sessionAttachmentTTL` const.
- `cmd/c3-claude-adapter/main.go` — `hello()` sends `SessionID`; `buildInstructions()` renders the recovery + backlog line.

---

### Task 1: `SessionAttachment` type, store field, accessors, TTL

**Files:**
- Modify: `internal/mappings/types.go`
- Modify: `internal/mappings/mutate.go`
- Test: `internal/mappings/session_attachment_test.go` (create)

**Interfaces produced:**
- `mappings.SessionAttachment{Channel string; ChatID int64; TopicID *int64; Name string; Group string; CWD string; LastAttachedAt time.Time; Detached bool}`
- `(sa SessionAttachment) Recoverable(now time.Time, ttl time.Duration) bool`
- `(mf *MappingsFile) UpsertSessionAttachment(id string, sa SessionAttachment)`
- `(mf *MappingsFile) LookupSessionAttachment(id string) (SessionAttachment, bool)`
- `(mf *MappingsFile) TombstoneSessionAttachment(id string)`
- `(mf *MappingsFile) PruneSessionAttachments(now time.Time, ttl time.Duration) int`

- [ ] **Step 1: Write failing tests** in `internal/mappings/session_attachment_test.go`:

```go
package mappings

import (
	"testing"
	"time"
)

func TestSessionAttachment_UpsertLookup(t *testing.T) {
	mf := &MappingsFile{}
	tid := int64(914)
	sa := SessionAttachment{Channel: "telegram", ChatID: -100, TopicID: &tid, Name: "c3", LastAttachedAt: time.Now().UTC()}
	mf.UpsertSessionAttachment("sess-1", sa)
	got, ok := mf.LookupSessionAttachment("sess-1")
	if !ok || got.Name != "c3" || got.TopicID == nil || *got.TopicID != 914 {
		t.Fatalf("lookup = %+v, ok=%v", got, ok)
	}
	if _, ok := mf.LookupSessionAttachment("nope"); ok {
		t.Fatal("unknown id should miss")
	}
}

func TestSessionAttachment_Recoverable(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	ttl := 30 * 24 * time.Hour
	fresh := SessionAttachment{LastAttachedAt: now.Add(-time.Hour)}
	if !fresh.Recoverable(now, ttl) {
		t.Fatal("fresh should be recoverable")
	}
	if (SessionAttachment{LastAttachedAt: now.Add(-time.Hour), Detached: true}).Recoverable(now, ttl) {
		t.Fatal("tombstoned must not be recoverable")
	}
	if (SessionAttachment{LastAttachedAt: now.Add(-31 * 24 * time.Hour)}).Recoverable(now, ttl) {
		t.Fatal("expired must not be recoverable")
	}
}

func TestSessionAttachment_Tombstone(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("s", SessionAttachment{Name: "c3", LastAttachedAt: time.Now().UTC()})
	mf.TombstoneSessionAttachment("s")
	got, ok := mf.LookupSessionAttachment("s")
	if !ok || !got.Detached {
		t.Fatalf("expected tombstoned entry, got %+v ok=%v", got, ok)
	}
	mf.TombstoneSessionAttachment("missing") // no panic
}

func TestSessionAttachment_Prune(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	ttl := 30 * 24 * time.Hour
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("old", SessionAttachment{LastAttachedAt: now.Add(-40 * 24 * time.Hour)})
	mf.UpsertSessionAttachment("new", SessionAttachment{LastAttachedAt: now.Add(-time.Hour)})
	n := mf.PruneSessionAttachments(now, ttl)
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if _, ok := mf.LookupSessionAttachment("old"); ok {
		t.Fatal("old should be pruned")
	}
	if _, ok := mf.LookupSessionAttachment("new"); !ok {
		t.Fatal("new should survive")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/mappings/ -run TestSessionAttachment` → FAIL (undefined).

- [ ] **Step 3: Implement.** In `types.go`, add the field to `MappingsFile` (after `Notifications`):

```go
	SessionAttachments map[string]SessionAttachment `json:"session_attachments,omitempty"`
```

and the type + method (end of file):

```go
// SessionAttachment records the last topic a CLI session was attached to,
// keyed by the host CLI's stable session id (Claude: CLAUDE_CODE_SESSION_ID),
// so a resumed session re-attaches automatically regardless of its launch dir.
// Detached is a tombstone: a deliberate `detach` sets it so the resumed
// session stays unattached until it attaches again (which clears it).
type SessionAttachment struct {
	Channel        string    `json:"channel"`
	ChatID         int64     `json:"chat_id"`
	TopicID        *int64    `json:"topic_id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Group          string    `json:"group,omitempty"`
	CWD            string    `json:"cwd,omitempty"`
	LastAttachedAt time.Time `json:"last_attached_at"`
	Detached       bool      `json:"detached,omitempty"`
}

// Recoverable reports whether this attachment may be auto-restored: not
// tombstoned and within ttl of its last attach.
func (sa SessionAttachment) Recoverable(now time.Time, ttl time.Duration) bool {
	return !sa.Detached && now.Sub(sa.LastAttachedAt) < ttl
}
```

In `mutate.go`, add (match the existing `UpsertMapping` style — nil-map init):

```go
// UpsertSessionAttachment records (or replaces) the last-attached route for a
// session id. Clears any prior tombstone (Detached defaults false on the new sa).
func (mf *MappingsFile) UpsertSessionAttachment(id string, sa SessionAttachment) {
	if id == "" {
		return
	}
	if mf.SessionAttachments == nil {
		mf.SessionAttachments = map[string]SessionAttachment{}
	}
	mf.SessionAttachments[id] = sa
}

// LookupSessionAttachment returns the raw entry for a session id, if present.
// The caller applies the Recoverable policy (tombstone + TTL).
func (mf *MappingsFile) LookupSessionAttachment(id string) (SessionAttachment, bool) {
	if mf == nil || mf.SessionAttachments == nil || id == "" {
		return SessionAttachment{}, false
	}
	sa, ok := mf.SessionAttachments[id]
	return sa, ok
}

// TombstoneSessionAttachment marks a session's attachment as deliberately
// detached, so a later resume does NOT auto-recover it. No-op if absent.
func (mf *MappingsFile) TombstoneSessionAttachment(id string) {
	if mf == nil || mf.SessionAttachments == nil {
		return
	}
	if sa, ok := mf.SessionAttachments[id]; ok {
		sa.Detached = true
		mf.SessionAttachments[id] = sa
	}
}

// PruneSessionAttachments deletes entries older than ttl. Returns the count
// removed. Bounds growth of the store.
func (mf *MappingsFile) PruneSessionAttachments(now time.Time, ttl time.Duration) int {
	if mf == nil || mf.SessionAttachments == nil {
		return 0
	}
	n := 0
	for id, sa := range mf.SessionAttachments {
		if now.Sub(sa.LastAttachedAt) >= ttl {
			delete(mf.SessionAttachments, id)
			n++
		}
	}
	return n
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/mappings/ -run TestSessionAttachment` → PASS.
- [ ] **Step 5: Full build + vet + race** — `go build ./... && go vet ./... && go test -race ./internal/mappings/`.
- [ ] **Step 6: Commit** — `git commit -am "mappings: session_attachments store (session-id → last route) + TTL"`.

---

### Task 2: IPC fields — `HelloMsg.SessionID`, `HelloAckMsg.QueuedCount`

**Files:**
- Modify: `internal/ipc/messages.go`
- Test: `internal/ipc/session_id_test.go` (create)

**Interfaces produced:** `HelloMsg.SessionID string`, `HelloAckMsg.QueuedCount int`.

- [ ] **Step 1: Failing test** `internal/ipc/session_id_test.go`:

```go
package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelloMsg_SessionIDRoundTrip(t *testing.T) {
	b, _ := json.Marshal(HelloMsg{Op: OpHello, CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"})
	if !strings.Contains(string(b), `"session_id":"sess-1"`) {
		t.Fatalf("missing session_id: %s", b)
	}
	var m HelloMsg
	_ = json.Unmarshal(b, &m)
	if m.SessionID != "sess-1" {
		t.Fatalf("round-trip session_id = %q", m.SessionID)
	}
	// omitempty: empty id absent from the wire.
	b2, _ := json.Marshal(HelloMsg{Op: OpHello})
	if strings.Contains(string(b2), "session_id") {
		t.Fatalf("empty session_id must be omitted: %s", b2)
	}
}

func TestHelloAckMsg_QueuedCount(t *testing.T) {
	b, _ := json.Marshal(HelloAckMsg{Op: OpHelloAck, QueuedCount: 3})
	if !strings.Contains(string(b), `"queued_count":3`) {
		t.Fatalf("missing queued_count: %s", b)
	}
	b2, _ := json.Marshal(HelloAckMsg{Op: OpHelloAck})
	if strings.Contains(string(b2), "queued_count") {
		t.Fatalf("zero queued_count must be omitted: %s", b2)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/ipc/ -run 'SessionID|QueuedCount'`.
- [ ] **Step 3: Implement.** In `HelloMsg` add (after `Capabilities`):

```go
	// SessionID is the host CLI's stable per-session id (Claude:
	// CLAUDE_CODE_SESSION_ID), used for auto-attach-on-resume. Empty for
	// hosts that don't expose one; additive-omitempty.
	SessionID string `json:"session_id,omitempty"`
```

In `HelloAckMsg` add (after `NoMapping`):

```go
	// QueuedCount is the number of inbound held for an auto-recovered route at
	// hello time, so the boot instructions can nudge fetch_queue. Set only
	// alongside AutoAttached; additive-omitempty.
	QueuedCount int `json:"queued_count,omitempty"`
```

- [ ] **Step 4: Run, verify pass.** **Step 5:** `go build ./...`. **Step 6: Commit** — `git commit -am "ipc: HelloMsg.SessionID + HelloAckMsg.QueuedCount (auto-attach-on-resume)"`.

---

### Task 3: `Stub.SessionID` threaded from hello

**Files:**
- Modify: `internal/broker/stubs.go` (field + `Register` signature)
- Modify: `internal/broker/handler.go` (both `Register` call sites: lines ~51, ~59)
- Test: `internal/broker/stubs_test.go` (add or create)

**Interfaces produced:** `Stub.SessionID string`; `Register(cli string, pid int, cwd, sessionID string, conn any) *Stub`.

- [ ] **Step 1: Failing test** (add to `internal/broker/stubs_test.go`):

```go
func TestStubRegistry_RegisterSessionID(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 42, "/x", "sess-1", nil)
	if s.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", s.SessionID)
	}
}
```

- [ ] **Step 2: Run, verify fail** (compile error — extra arg).
- [ ] **Step 3: Implement.** In `stubs.go`: add `SessionID string` to `Stub` (after `CWD`); change `Register`:

```go
func (r *StubRegistry) Register(cli string, pid int, cwd, sessionID string, conn any) *Stub {
	id := r.next.Add(1)
	s := &Stub{CLI: cli, PID: pid, CWD: cwd, SessionID: sessionID, ConnID: id, Conn: conn}
	r.mu.Lock()
	r.byConn[id] = s
	r.mu.Unlock()
	return s
}
```

In `handler.go`, update BOTH call sites to pass `hello.SessionID`:
```go
stub = b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, hello.SessionID, conn)
```
(Grep the package for any other `.Register(` call — e.g. tests — and update to the 5-arg form.)

- [ ] **Step 4: Run, verify pass.** **Step 5:** `go build ./... && go test ./internal/broker/ -run Stub`. **Step 6: Commit** — `git commit -am "broker: thread SessionID from hello onto Stub"`.

---

### Task 4: `persistMapping` records the session attachment

**Files:**
- Modify: `internal/broker/attach.go` (`persistMapping`, ~691-721; add `sessionAttachmentTTL` const)
- Test: `internal/broker/attach_test.go` (add)

**Interfaces consumed:** Task 1 accessors; Task 3 `Stub.SessionID`.
**Interfaces produced:** `const sessionAttachmentTTL = 30 * 24 * time.Hour` (broker pkg); `persistMapping` now also upserts `SessionAttachments[stub.SessionID]`.

**Note for implementer:** preserve the existing cwd rebind-guard behavior and log message EXACTLY. Record the session attachment in the SAME `mutateMappings` closure so it shares the lock + the single `SaveMappings`, and record it INDEPENDENTLY of the cwd outcome (so a refused cwd-rebind, or an empty cwd, still records the recovery entry).

- [ ] **Step 1: Failing test** — drive `persistMapping` through a real attach and assert the session attachment is recorded. Prefer an existing broker test harness that builds a `*Broker` with an in-memory mappings file; mirror the nearest existing `attach_test.go` setup. Minimal shape:

```go
func TestPersistMapping_RecordsSessionAttachment(t *testing.T) {
	b := newTestBroker(t) // existing helper; or construct as neighboring tests do
	stub := b.Stubs.Register("claude", os.Getpid(), t.TempDir(), "sess-xyz", nil)
	b.persistMapping(stub, "telegram", -100, 914, "c3", "main")
	sa, ok := b.Mappings().LookupSessionAttachment("sess-xyz")
	if !ok {
		t.Fatal("session attachment not recorded")
	}
	if sa.Name != "c3" || sa.ChatID != -100 || sa.TopicID == nil || *sa.TopicID != 914 || sa.Detached {
		t.Fatalf("session attachment = %+v", sa)
	}
}

func TestPersistMapping_EmptySessionIDNoOp(t *testing.T) {
	b := newTestBroker(t)
	stub := b.Stubs.Register("claude", os.Getpid(), t.TempDir(), "", nil)
	b.persistMapping(stub, "telegram", -100, 914, "c3", "main")
	if len(b.Mappings().SessionAttachments) != 0 {
		t.Fatal("empty SessionID must not record an attachment")
	}
}
```

(If no `newTestBroker` helper exists, build the broker the way the nearest `attach_test.go`/`*_test.go` in `internal/broker` does — match the established pattern; do not invent a new harness.)

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Add the const near the top of `attach.go`:
```go
// sessionAttachmentTTL bounds how long a recorded session→route mapping stays
// eligible for auto-attach-on-resume.
const sessionAttachmentTTL = 30 * 24 * time.Hour
```
Restructure `persistMapping` so the session attachment is recorded unconditionally (when `stub.SessionID != ""`) and the existing cwd logic keeps its rebind guard (convert the early `return` to an `if/else` so the session-attachment write isn't skipped):

```go
func (b *Broker) persistMapping(stub *Stub, chanName string, chatID, topicID int64, name, group string) {
	now := time.Now().UTC()
	cwd := resolveAttachCWD(stub.CWD, name)
	var tidPtr *int64
	if topicID != 0 {
		t := topicID
		tidPtr = &t
	}
	var persisted bool
	b.mutateMappings(func(mf *mappings.MappingsFile) {
		// Session-id recovery store — keyed on the session id, independent of
		// cwd (so DM / no-launch-dir routes still recover). Clears any tombstone.
		if stub.SessionID != "" {
			mf.UpsertSessionAttachment(stub.SessionID, mappings.SessionAttachment{
				Channel: chanName, ChatID: chatID, TopicID: tidPtr,
				Name: name, Group: group, CWD: cwd, LastAttachedAt: now,
			})
			persisted = true
		}
		// cwd → topic default (existing behavior incl. the rebind guard).
		if cwd != "" {
			if existing, ok := mf.LookupByCwd(cwd); ok && (existing.ChatID != chatID || existing.TopicID != topicID) {
				log.Printf("attach: REFUSED to rebind cwd=%q (saved=topic-%d %q → requested=topic-%d %q); live claim proceeds but saved default unchanged. To rebind, edit ~/.config/c3/mappings.json.",
					cwd, existing.TopicID, existing.Name, topicID, name)
			} else {
				mf.UpsertMapping(cwd, mappings.Mapping{
					Channel: chanName, ChatID: chatID, TopicID: topicID,
					Name: name, Group: group, LastAttachedAt: now,
				})
				persisted = true
			}
		}
	})
	if persisted {
		_ = b.SaveMappings()
	}
}
```

- [ ] **Step 4: Run, verify pass.** **Step 5:** `go build ./... && go test -race ./internal/broker/ -run 'PersistMapping|Attach'`. **Step 6: Commit** — `git commit -am "broker: persistMapping records the session→route recovery entry"`.

---

### Task 5: Hello recovery (the core)

**Files:**
- Modify: `internal/broker/handler.go` (the hello ack block, ~89-106)
- Add helper in `internal/broker/attach.go` (or handler.go): `wireMappingFromSessionAttachment(sa) *ipc.Mapping` and route-key derivation.
- Test: `internal/broker/handler_test.go` (add) — or the nearest broker hello test harness.

**Interfaces consumed:** Tasks 1–4.

**Behavior:** In the hello ack block, BEFORE the cwd `LookupByCwd`/`NoMapping` logic, attempt session-id recovery:

```go
ack := ipc.HelloAckMsg{Op: ipc.OpHelloAck, ConnID: stub.ConnID}
recovered := false
switch {
case len(b.Mappings().Channels) == 0:
	ack.NoConfig = true
default:
	// Session-id recovery takes precedence over the cwd mapping (so a resume
	// from any dir — including the shared root, which itself has a cwd mapping —
	// returns to the session's own last topic).
	if sa, ok := b.Mappings().LookupSessionAttachment(hello.SessionID); ok && sa.Recoverable(time.Now(), sessionAttachmentTTL) {
		key := routeKeyFromSessionAttachment(sa)
		if _, held := b.heldByDifferentLiveSession(key, stub); !held {
			if _, claimed := b.Routes.Claim(key, stub); claimed {
				stub.SetRoute(&key)
				cnt, _ := b.backlogSummary(key)
				ack.AutoAttached = true
				ack.Mapping = wireMappingFromSessionAttachment(sa)
				ack.QueuedCount = cnt
				recovered = true
				// Refresh LastAttachedAt so an active session doesn't TTL-expire.
				b.mutateMappings(func(mf *mappings.MappingsFile) {
					if cur, ok := mf.LookupSessionAttachment(hello.SessionID); ok {
						cur.LastAttachedAt = time.Now().UTC()
						cur.Detached = false
						mf.UpsertSessionAttachment(hello.SessionID, cur)
					}
				})
				_ = b.SaveMappings()
				log.Printf("hello: session-id recovery cli=%s pid=%d session=%s → %s (queued=%d)",
					hello.CLI, hello.PID, hello.SessionID, sa.Name, cnt)
			}
		} else {
			log.Printf("hello: session-id recovery SKIPPED (topic %q held by another live session); falling through", sa.Name)
		}
	}
	if !recovered {
		if _, ok := b.Mappings().LookupByCwd(hello.CWD); !ok {
			ack.NoMapping = true
		}
	}
}
```

Then the existing capability block. NOTE: keep the cap resolution preferring the recovered/cwd-mapped channel; recovered route's channel is `sa.Channel`.

Helpers (in `attach.go`):

```go
func routeKeyFromSessionAttachment(sa mappings.SessionAttachment) RouteKey {
	return MakeRouteKey(sa.Channel, sa.ChatID, sa.TopicID)
}

func wireMappingFromSessionAttachment(sa mappings.SessionAttachment) *ipc.Mapping {
	return &ipc.Mapping{
		Channel: sa.Channel, ChatID: sa.ChatID,
		TopicID: sa.TopicID, Name: sa.Name, Group: sa.Group,
	}
}
```

(Verify `MakeRouteKey(channel string, chatID int64, topicID *int64) RouteKey` signature against the codebase — it's the same constructor used at `attach.go:328`.)

- [ ] **Step 1: Failing tests** (`handler_test.go`). Drive `HandleConn` (or factor the ack-building into a testable `func (b *Broker) buildHelloAck(hello ipc.HelloMsg, stub *Stub) ipc.HelloAckMsg` and test that directly — preferred, smaller surface). Cases:
  - recovery hit: stored attachment + free route + `SessionID` set → ack `AutoAttached==true`, `Mapping.Name==stored`, `stub.CurrentRoute()` set.
  - precedence: a cwd mapping ALSO exists for `hello.CWD` but session-id recovery still wins (ack.Mapping is the session's topic, not the cwd's).
  - tombstoned / expired attachment → no recovery (ack falls through; `AutoAttached==false`).
  - empty `SessionID` → no recovery.
  - held by another live session → no recovery, no claim; falls through to NoMapping/cwd.
  - `QueuedCount` reflects `backlogSummary` for the recovered route.

(If extracting `buildHelloAck` is cleaner for testing, do it — move the ack-building logic out of `HandleConn` into a method that takes `(hello, stub)` and returns the ack, and have `HandleConn` call it. This keeps `HandleConn` thin and the logic unit-testable without a socket.)

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** as above (extract `buildHelloAck` if chosen).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5:** `go build ./... && go vet ./... && go test -race ./internal/broker/`.
- [ ] **Step 6: Commit** — `git commit -am "broker: hello-time session-id recovery (claim + ack + backlog count)"`.

---

### Task 6: `OpRelease` tombstones the session attachment

**Files:**
- Modify: `internal/broker/handler.go` (`OpRelease` case, ~134-136)
- Test: `internal/broker/handler_test.go` (add)

**Behavior:** an explicit detach (`OpRelease`) tombstones the session's attachment so a later resume stays unattached. The dead-PID conn-drop defer (handler.go:76-86) must NOT tombstone (that's a process exit, not a user detach).

```go
case ipc.OpRelease:
	b.Routes.ReleaseAllByConnID(stub.ConnID)
	stub.SetRoute(nil)
	if stub.SessionID != "" {
		b.mutateMappings(func(mf *mappings.MappingsFile) {
			mf.TombstoneSessionAttachment(stub.SessionID)
		})
		_ = b.SaveMappings()
	}
```

- [ ] **Step 1: Failing test** — register a stub with a SessionID, seed a recoverable attachment, simulate `OpRelease` handling (call the extracted release logic or drive the dispatch), assert the attachment is now `Detached`. Also assert the conn-drop path (call the defer's release logic / `ReleaseAllByConnID`) does NOT tombstone. If `OpRelease` is only reachable via the dispatch loop, extract a `func (b *Broker) handleRelease(stub *Stub)` and test it directly.
- [ ] **Step 2: Run, verify fail.** **Step 3: Implement** (extract `handleRelease` if needed). **Step 4: Run, verify pass.** **Step 5:** `go build ./... && go test -race ./internal/broker/`. **Step 6: Commit** — `git commit -am "broker: detach tombstones the session recovery entry (conn-drop does not)"`.

---

### Task 7: Adapter — send `SessionID`; render recovery + backlog in boot instructions

**Files:**
- Modify: `cmd/c3-claude-adapter/main.go` (`hello()` ~284; `buildInstructions()` ~1089)
- Test: `cmd/c3-claude-adapter/main_test.go` (add)

**Behavior:**
- `hello()` sets `SessionID: os.Getenv("CLAUDE_CODE_SESSION_ID")` on the `HelloMsg`.
- `buildInstructions()` AutoAttached branch appends a backlog nudge when `QueuedCount > 0`.

```go
// hello():
if err := a.conn.WriteJSON(ipc.HelloMsg{
	Op: ipc.OpHello, CLI: "claude", PID: os.Getpid(), CWD: cwd,
	Capabilities: []string{"claude/channel"},
	SessionID:    os.Getenv("CLAUDE_CODE_SESSION_ID"),
}); err != nil {
```

```go
// buildInstructions() AutoAttached branch:
case a.helloAck.AutoAttached && a.helloAck.Mapping != nil:
	m := a.helloAck.Mapping
	head = fmt.Sprintf("Auto-attached to %q (%s). Inbound messages render here as `<channel>` blocks.", m.Name, m.Channel)
	if a.helloAck.QueuedCount > 0 {
		noun := "message"
		if a.helloAck.QueuedCount > 1 {
			noun = "messages"
		}
		head += fmt.Sprintf(" %d %s held while detached — call `fetch_queue` to retrieve.", a.helloAck.QueuedCount, noun)
	}
```

- [ ] **Step 1: Failing tests** (`main_test.go`):
  - A test that builds an `adapter` with `helloAck = ipc.HelloAckMsg{AutoAttached: true, Mapping: &ipc.Mapping{Name: "c3", Channel: "telegram"}, QueuedCount: 2}` and asserts `buildInstructions()` contains `Auto-attached to "c3"` AND `2 messages held` AND `fetch_queue`.
  - A second with `QueuedCount: 0` asserts the held-clause is absent.
  - (SessionID-on-hello is covered indirectly; if a unit test for `hello()` is impractical without a broker, assert the env read via a tiny extracted helper `sessionIDFromEnv()` returning `os.Getenv("CLAUDE_CODE_SESSION_ID")`, and use it in `hello()`. Prefer the helper so it's unit-testable.)
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** (add `sessionIDFromEnv()` helper if used).
- [ ] **Step 4: Run, verify pass.** **Step 5:** `go build ./... && go vet ./... && go test -race ./cmd/c3-claude-adapter/`. **Step 6: Commit** — `git commit -am "adapter: send CLAUDE_CODE_SESSION_ID on hello; surface recovery + held-backlog in boot instructions"`.

---

### Task 8: Prune on broker start (bound store growth)

**Files:**
- Modify: broker startup (where mappings are first loaded / `RecoverOnStartup`-adjacent — grep for where the broker reads mappings at boot).
- Test: covered by Task 1's `PruneSessionAttachments`; add a thin startup test only if a natural seam exists.

- [ ] **Step 1:** Locate the broker-start path that has the loaded `*MappingsFile`. Call `mf.PruneSessionAttachments(time.Now().UTC(), sessionAttachmentTTL)` once at start; if it pruned >0, `SaveMappings()` and log the count.
- [ ] **Step 2:** `go build ./... && go test -race ./internal/broker/`.
- [ ] **Step 3: Commit** — `git commit -am "broker: prune expired session attachments on start"`.

(If no clean startup seam exists, fold pruning into the first `SaveMappings` after recovery and note it; do not invent a new lifecycle hook.)

---

## Self-Review

- **Spec coverage:** session_attachments store (T1) ✓; SessionID/QueuedCount IPC (T2) ✓; stub threading (T3) ✓; record-on-attach (T4) ✓; hello recovery + precedence + collision-skip + QueuedCount (T5) ✓; detach tombstone, conn-drop doesn't clear (T6) ✓; adapter hello + boot-instruction surfacing (T7) ✓; TTL prune (T1 + T8) ✓.
- **No placeholders:** every code step has concrete code; test harness steps say "mirror the nearest existing test" where the exact helper name must be read from the tree.
- **Type consistency:** `SessionAttachment.TopicID` is `*int64` (matches `ipc.Mapping.TopicID` + `MakeRouteKey`); `mappings.Mapping.TopicID` stays `int64` (the cwd path uses `topicID int64` and converts to `tidPtr` for the route key, as the existing code does at attach.go:323-328).
- **Codex:** untouched (no `SessionID` → all new paths no-op).
