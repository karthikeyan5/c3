# C3 v3 Broker Core + IPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the broker daemon scaffolding + IPC layer + per-route serial executor pattern + connection-lifecycle ops (hello / release / bye / list_topics / error). After this plan, an adapter can connect to a running broker, complete a `hello` handshake, list topics, and disconnect cleanly. The broker is singleton-per-machine via flock, listens on a multi-user-safe socket, and has the route-worker plumbing in place — but no channel implementation yet (so no inbound message routing, no attach proposal flow, no tool_call forwarding). Those land in subsequent plans.

**Architecture:** Plain Go (stdlib only — no third-party deps in this plan). The broker is a long-running daemon spawned by adapters via flock; it owns `~/.config/c3/mappings.json` (read-only in this plan; broker doesn't yet write back), a unix socket server, an in-memory `ROUTES` map, and a per-route serial executor pattern. IPC is newline-delimited JSON over the socket, using a `internal/ipc` types package with Op constants and JSON-tagged structs.

**Tech Stack:** Go ≥1.22, stdlib only (`net`, `os`, `sync`, `syscall`, `bufio`, `encoding/json`, `context`, `testing`).

**Spec reference:** `docs/specs/2026-05-08-c3-rearch-design.md` — sections §4.2 (routing key + per-route executor + claim lifecycle), §4.2.2 (broker process model), §4.4.1 (IPC types).

---

## Phase 3a: IPC types package

### Task 3.1: Create `internal/ipc` package with Op constants

**Files:**
- Create: `internal/ipc/ops.go`

- [ ] **Step 1: Write the package**

Create `internal/ipc/ops.go` with:

```go
// Package ipc defines the wire types for broker ↔ adapter communication
// over the unix socket at $XDG_RUNTIME_DIR/c3.sock (or /tmp/c3-$UID.sock).
//
// Schema reference: docs/specs/2026-05-08-c3-rearch-design.md §4.4.1.
package ipc

// Op is the op-code present on every IPC message. Adapters and broker
// dispatch on Op.
type Op string

const (
	// adapter → broker
	OpHello      Op = "hello"
	OpServerInfo Op = "server_info"
	OpToolsList  Op = "tools_list"
	OpAttach     Op = "attach"
	OpRelease    Op = "release"
	OpListTopics Op = "list_topics"
	OpToolCall   Op = "tool_call"
	OpBye        Op = "bye"

	// broker → adapter
	OpHelloAck   Op = "hello_ack"
	OpAttached   Op = "attached"
	OpToolResult Op = "tool_result"
	OpInbound    Op = "inbound"
	OpTopicsList Op = "topics_list"
	OpError      Op = "error"
)
```

- [ ] **Step 2: Verify build**

Run:
```bash
cd /home/karthi/arogara/c3 && go build ./internal/ipc/...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/ipc/ops.go
git commit -m "c3-v3: ipc package with Op constants"
```

---

### Task 3.2: IPC envelope + message structs (TDD)

**Files:**
- Create: `internal/ipc/messages.go`
- Create: `internal/ipc/messages_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ipc/messages_test.go`:

```go
package ipc

import (
	"encoding/json"
	"testing"
)

func TestHelloMsg_Roundtrip(t *testing.T) {
	in := HelloMsg{
		Op:           OpHello,
		CLI:          "claude",
		PID:          12345,
		CWD:          "/home/u/proj",
		Capabilities: []string{"claude/channel"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out HelloMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Op != OpHello || out.CLI != "claude" || out.PID != 12345 {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestParseEnvelope_OpDispatch(t *testing.T) {
	raw := `{"op":"hello","cli":"claude","pid":1,"cwd":"/x"}`
	op, err := PeekOp([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if op != OpHello {
		t.Errorf("got op=%q, want %q", op, OpHello)
	}
}

func TestParseEnvelope_MissingOp(t *testing.T) {
	raw := `{"cli":"claude"}`
	_, err := PeekOp([]byte(raw))
	if err == nil {
		t.Error("expected error for missing op, got nil")
	}
}

func TestErrorMsg_Roundtrip(t *testing.T) {
	in := ErrorMsg{Op: OpError, Err: "broker unavailable"}
	data, _ := json.Marshal(in)
	var out ErrorMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Err != "broker unavailable" {
		t.Errorf("Err=%q, want broker unavailable", out.Err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/ipc/... -v
```

Expected: compile error, `HelloMsg`/`ErrorMsg`/`PeekOp` undefined.

- [ ] **Step 3: Implement the structs and PeekOp**

Create `internal/ipc/messages.go`:

```go
package ipc

import (
	"encoding/json"
	"fmt"
)

// HelloMsg is sent by the adapter on connect.
type HelloMsg struct {
	Op           Op       `json:"op"` // = OpHello
	CLI          string   `json:"cli"`
	PID          int      `json:"pid"`
	CWD          string   `json:"cwd"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// HelloAckMsg is the broker's response to HelloMsg.
type HelloAckMsg struct {
	Op           Op       `json:"op"` // = OpHelloAck
	ConnID       uint64   `json:"conn_id"`
	AutoAttached bool     `json:"auto_attached"`
	Mapping      *Mapping `json:"mapping,omitempty"`
	ClaimHolder  *Holder  `json:"claim_holder,omitempty"`
	NoConfig     bool     `json:"no_config,omitempty"`
	NoMapping    bool     `json:"no_mapping,omitempty"`
}

// Holder identifies a claim holder for diagnostic responses.
type Holder struct {
	CLI string `json:"cli"`
	PID int    `json:"pid"`
	CWD string `json:"cwd"`
}

// Mapping is the wire-shape mirror of mappings.Mapping (avoiding a circular
// import). Populated by the broker on hello_ack when an auto-attach is
// possible.
type Mapping struct {
	Channel string `json:"channel"`
	ChatID  int64  `json:"chat_id"`
	TopicID *int64 `json:"topic_id,omitempty"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
}

// ReleaseReq is sent by the adapter to drop its claim without disconnecting.
type ReleaseReq struct {
	Op Op `json:"op"` // = OpRelease
}

// ByeReq is sent by the adapter for clean disconnect.
type ByeReq struct {
	Op Op `json:"op"` // = OpBye
}

// ListTopicsReq is sent by the adapter to fetch the topics registry.
type ListTopicsReq struct {
	Op Op `json:"op"` // = OpListTopics
}

// TopicsListMsg is the broker's response to ListTopicsReq.
type TopicsListMsg struct {
	Op     Op           `json:"op"` // = OpTopicsList
	Topics []TopicEntry `json:"topics"`
}

// TopicEntry is one row in TopicsListMsg.Topics.
type TopicEntry struct {
	Channel    string `json:"channel"`
	ChatID     int64  `json:"chat_id"`
	TopicID    int64  `json:"topic_id"`
	Name       string `json:"name"`
	Group      string `json:"group,omitempty"`
	ClaimedBy  *Holder `json:"claimed_by,omitempty"`
}

// ErrorMsg is sent by either side on an unrecoverable error.
type ErrorMsg struct {
	Op  Op     `json:"op"` // = OpError
	Err string `json:"err"`
}

// PeekOp parses the "op" field from a raw JSON envelope without unmarshaling
// the full payload. Used by the dispatcher to route to the right handler.
func PeekOp(raw []byte) (Op, error) {
	var env struct {
		Op Op `json:"op"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("ipc: parse envelope: %w", err)
	}
	if env.Op == "" {
		return "", fmt.Errorf("ipc: missing op field")
	}
	return env.Op, nil
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/ipc/... -v
```

Expected: all four pass.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/messages.go internal/ipc/messages_test.go
git commit -m "c3-v3: ipc message structs + PeekOp envelope dispatcher"
```

---

### Task 3.3: Conn type — newline-JSON reader/writer with mutex

**Files:**
- Create: `internal/ipc/conn.go`
- Create: `internal/ipc/conn_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ipc/conn_test.go`:

```go
package ipc

import (
	"net"
	"sync"
	"testing"
)

func newPipePair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	return NewConn(a), NewConn(b)
}

func TestConn_RoundtripFrame(t *testing.T) {
	a, b := newPipePair(t)
	defer a.Close()
	defer b.Close()

	go func() {
		_ = a.WriteJSON(HelloMsg{Op: OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	}()

	raw, err := b.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	op, err := PeekOp(raw)
	if err != nil {
		t.Fatal(err)
	}
	if op != OpHello {
		t.Errorf("op=%q, want %q", op, OpHello)
	}
}

func TestConn_ConcurrentWritesAreFramed(t *testing.T) {
	a, b := newPipePair(t)
	defer a.Close()
	defer b.Close()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = a.WriteJSON(HelloMsg{Op: OpHello, CLI: "claude", PID: i, CWD: "/x"})
		}(i)
	}

	seen := 0
	for seen < N {
		raw, err := b.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		op, err := PeekOp(raw)
		if err != nil {
			t.Fatalf("frame %d malformed: %v (raw=%s)", seen, err, raw)
		}
		if op != OpHello {
			t.Fatalf("frame %d op=%q, want %q", seen, op, OpHello)
		}
		seen++
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/ipc/... -run TestConn -v
```

Expected: compile error, `Conn`/`NewConn`/`WriteJSON`/`ReadFrame` undefined.

- [ ] **Step 3: Implement Conn**

Create `internal/ipc/conn.go`:

```go
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Conn wraps a net.Conn with newline-JSON framing and a write mutex. Multiple
// goroutines can safely call WriteJSON concurrently; only one frame at a time
// reaches the wire.
//
// Spec §4.4.1: "the IPC socket is duplex and a single connection carries
// both request/response (synchronous tool calls) and unsolicited push
// (broker → adapter inbound). Both sides use a single bufio.Writer guarded
// by a sync.Mutex; line-by-line frames are atomic."
type Conn struct {
	c    net.Conn
	w    *bufio.Writer
	wmu  sync.Mutex
	r    *bufio.Reader
}

// NewConn wraps a net.Conn. Owner is responsible for calling Close.
func NewConn(c net.Conn) *Conn {
	return &Conn{
		c: c,
		w: bufio.NewWriter(c),
		r: bufio.NewReader(c),
	}
}

// WriteJSON marshals v and writes one newline-terminated frame to the wire.
// Safe for concurrent use.
func (c *Conn) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(data); err != nil {
		return err
	}
	if err := c.w.WriteByte('\n'); err != nil {
		return err
	}
	return c.w.Flush()
}

// ReadFrame reads one \n-terminated frame and returns its bytes (without the
// trailing newline). Returns io.EOF when the peer closes cleanly.
//
// NOT safe for concurrent use — only one reader goroutine per Conn.
func (c *Conn) ReadFrame() ([]byte, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			// final line with no trailing \n; honor it
			return line, nil
		}
		return nil, err
	}
	// Strip trailing \n and any \r before it.
	n := len(line)
	if n > 0 && line[n-1] == '\n' {
		n--
		if n > 0 && line[n-1] == '\r' {
			n--
		}
	}
	return line[:n], nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.c.Close()
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/ipc/... -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/conn.go internal/ipc/conn_test.go
git commit -m "c3-v3: ipc.Conn newline-JSON framing with writer mutex"
```

---

## Phase 3b: Broker process model

### Task 3.4: Socket path resolution + flock helpers

**Files:**
- Create: `internal/broker/paths.go`
- Create: `internal/broker/paths_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/paths_test.go`:

```go
package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPath_XDGRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	got := SocketPath()
	want := filepath.Join(dir, "c3.sock")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSocketPath_FallbackPerUID(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	got := SocketPath()
	if !strings.HasPrefix(got, "/tmp/c3-") || !strings.HasSuffix(got, ".sock") {
		t.Errorf("got %q, expected /tmp/c3-<uid>.sock", got)
	}
}

func TestPidFilePath_XDGRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	got := PidFilePath()
	want := filepath.Join(dir, "c3-broker.pid")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPidFilePath_FallbackToHomeCacheC3(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := PidFilePath()
	want := filepath.Join(home, ".cache", "c3", "c3-broker.pid")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnsureParentDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c", "file.txt")
	if err := ensureParentDir(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Errorf("parent dir not created: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: compile error, `SocketPath`/`PidFilePath`/`ensureParentDir` undefined.

- [ ] **Step 3: Implement paths**

Create `internal/broker/paths.go`:

```go
package broker

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the broker's listening socket path:
//
//	$XDG_RUNTIME_DIR/c3.sock  (preferred — user-private tmpfs)
//	/tmp/c3-$UID.sock         (fallback)
//
// Spec §4.2.2: never bare /tmp/c3.sock, to avoid multi-user clobbering.
func SocketPath() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "c3.sock")
	}
	return fmt.Sprintf("/tmp/c3-%d.sock", os.Getuid())
}

// PidFilePath returns the broker's flock pid-file path:
//
//	$XDG_RUNTIME_DIR/c3-broker.pid  (preferred)
//	$HOME/.cache/c3/c3-broker.pid   (fallback)
//
// Spec §4.2.2.
func PidFilePath() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "c3-broker.pid")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "c3", "c3-broker.pid")
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0700)
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all five pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/paths.go internal/broker/paths_test.go
git commit -m "c3-v3: broker socket + pid-file path resolution (multi-user safe)"
```

---

### Task 3.5: flock-based singleton acquire

**Files:**
- Create: `internal/broker/singleton.go`
- Create: `internal/broker/singleton_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/singleton_test.go`:

```go
package broker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireSingleton_FirstWins(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "broker.pid")

	lock1, err := AcquireSingleton(pidFile)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer lock1.Release()

	// Same-process second acquire should fail (already held).
	if _, err := AcquireSingleton(pidFile); err == nil {
		t.Error("expected second acquire to fail")
	}
}

func TestAcquireSingleton_StalePidUnlinked(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "broker.pid")

	// Plant a pid file pointing at a definitely-dead pid.
	deadPid := 999999 // PID this high almost certainly does not exist
	if err := os.WriteFile(pidFile, []byte("999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = deadPid

	lock, err := AcquireSingleton(pidFile)
	if err != nil {
		t.Fatalf("acquire over stale pid failed: %v", err)
	}
	defer lock.Release()

	// Verify our pid is now in the file.
	data, _ := os.ReadFile(pidFile)
	if len(data) == 0 {
		t.Error("pid file empty after acquire")
	}
}

func TestAcquireSingleton_WritesOurPid(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "broker.pid")

	lock, err := AcquireSingleton(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("pid file empty")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -run TestAcquireSingleton -v
```

Expected: compile error, `AcquireSingleton`/`Release` undefined.

- [ ] **Step 3: Implement singleton**

Create `internal/broker/singleton.go`:

```go
package broker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// SingletonLock represents a held flock. Caller must Release on shutdown.
type SingletonLock struct {
	fd       int
	pidFile  string
}

// AcquireSingleton ensures only one broker runs. Spec §4.2.2:
//
//   - flock(LOCK_EX | LOCK_NB) on pidFile.
//   - On EWOULDBLOCK: read pid; if alive, return error (sibling won race).
//     If dead, unlink stale pid file and retry once.
//   - On success: write own pid, do NOT close fd (closing releases flock).
func AcquireSingleton(pidFile string) (*SingletonLock, error) {
	if err := ensureParentDir(pidFile); err != nil {
		return nil, fmt.Errorf("ensure pid-file dir: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		fd, err := syscall.Open(pidFile, syscall.O_CREAT|syscall.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("open pid file %s: %w", pidFile, err)
		}

		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = syscall.Close(fd)
			if !isLocked(err) {
				return nil, fmt.Errorf("flock %s: %w", pidFile, err)
			}
			// Held by another process. Read its pid and check liveness.
			alive, _ := pidAlive(pidFile)
			if alive {
				return nil, fmt.Errorf("broker already running (pid file %s held)", pidFile)
			}
			// Stale lock — unlink and retry.
			if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("remove stale pid file: %w", err)
			}
			continue
		}

		// Locked. Write our pid.
		_ = syscall.Ftruncate(fd, 0)
		ourPid := os.Getpid()
		_, err = syscall.Write(fd, []byte(strconv.Itoa(ourPid)+"\n"))
		if err != nil {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("write pid: %w", err)
		}
		// Do NOT close fd — closing releases the flock.
		return &SingletonLock{fd: fd, pidFile: pidFile}, nil
	}

	return nil, fmt.Errorf("could not acquire singleton lock on %s", pidFile)
}

// Release releases the flock and removes the pid file.
func (s *SingletonLock) Release() {
	if s == nil {
		return
	}
	_ = syscall.Flock(s.fd, syscall.LOCK_UN)
	_ = syscall.Close(s.fd)
	_ = os.Remove(s.pidFile)
}

func isLocked(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}

func pidAlive(pidFile string) (bool, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, err
	}
	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false, fmt.Errorf("invalid pid in file: %q", pidStr)
	}
	// kill(pid, 0) reports liveness without sending a signal.
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			return false, nil
		}
		// EPERM means it exists but we can't signal it — still alive.
		if err == syscall.EPERM {
			return true, nil
		}
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all three TestAcquireSingleton tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/singleton.go internal/broker/singleton_test.go
git commit -m "c3-v3: broker singleton via flock with stale-pid recovery"
```

---

## Phase 3c: Routing layer

### Task 3.6: RouteKey + MakeRouteKey

**Files:**
- Create: `internal/broker/route.go`
- Create: `internal/broker/route_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/route_test.go`:

```go
package broker

import "testing"

func TestMakeRouteKey_NilTopic(t *testing.T) {
	k := MakeRouteKey("telegram", -100, nil)
	if k.HasTopic {
		t.Error("HasTopic should be false for nil")
	}
	if k.TopicID != 0 {
		t.Errorf("TopicID = %d, want 0 for nil topic", k.TopicID)
	}
}

func TestMakeRouteKey_GeneralTopic(t *testing.T) {
	one := int64(1)
	k := MakeRouteKey("telegram", -100, &one)
	if !k.HasTopic {
		t.Error("HasTopic should be true for &1")
	}
	if k.TopicID != 1 {
		t.Errorf("TopicID = %d, want 1", k.TopicID)
	}
}

func TestMakeRouteKey_CustomTopic(t *testing.T) {
	id := int64(281)
	k := MakeRouteKey("telegram", -100, &id)
	if !k.HasTopic || k.TopicID != 281 {
		t.Errorf("got %+v, want HasTopic=true TopicID=281", k)
	}
}

func TestRouteKey_MapKeyEqualityForGeneralTopic(t *testing.T) {
	// Two separate *int64 pointing to 1 must hash and compare equal as map
	// keys for "General forum topic". This is the bug RouteKey exists to fix.
	a := int64(1)
	b := int64(1)

	m := map[RouteKey]string{}
	m[MakeRouteKey("telegram", -100, &a)] = "first"
	m[MakeRouteKey("telegram", -100, &b)] = "second"

	if len(m) != 1 {
		t.Errorf("expected 1 entry (collision), got %d (RouteKey not value-comparable)", len(m))
	}
	if m[MakeRouteKey("telegram", -100, &a)] != "second" {
		t.Errorf("expected second to overwrite first, got %q", m[MakeRouteKey("telegram", -100, &a)])
	}
}

func TestRouteKey_NilDistinctFromZero(t *testing.T) {
	zero := int64(0)
	kNil := MakeRouteKey("telegram", -100, nil)
	kZero := MakeRouteKey("telegram", -100, &zero)

	if kNil == kZero {
		t.Error("RouteKey for nil topic_id MUST NOT equal RouteKey for &0")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -run RouteKey -v
```

Expected: compile error, `RouteKey`/`MakeRouteKey` undefined.

- [ ] **Step 3: Implement RouteKey**

Create `internal/broker/route.go`:

```go
package broker

// RouteKey is the value-typed routing key. Spec §4.2 — replaces map[*int64]X
// (which would compare pointer identity, not value).
//
// nil topic-id distinguishes "no topic" (DM, non-forum group) from "General
// forum topic" (which is *int64(1) in user-visible types). At the map-key
// level this is encoded as HasTopic=false vs HasTopic=true with TopicID=1.
type RouteKey struct {
	Channel  string
	ChatID   int64
	HasTopic bool
	TopicID  int64
}

// MakeRouteKey converts a (channel, chat_id, *topic_id) triple into a
// value-typed, comparable, hashable RouteKey.
func MakeRouteKey(channel string, chatID int64, topicID *int64) RouteKey {
	if topicID == nil {
		return RouteKey{Channel: channel, ChatID: chatID, HasTopic: false}
	}
	return RouteKey{Channel: channel, ChatID: chatID, HasTopic: true, TopicID: *topicID}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all five RouteKey tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/route.go internal/broker/route_test.go
git commit -m "c3-v3: RouteKey value-type fixes *int64 map-key bug"
```

---

### Task 3.7: Stub registry with monotonic ConnID

**Files:**
- Create: `internal/broker/stubs.go`
- Create: `internal/broker/stubs_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/stubs_test.go`:

```go
package broker

import "testing"

func TestStubRegistry_AssignsMonotonicConnID(t *testing.T) {
	r := NewStubRegistry()
	a := r.Register("claude", 1, "/x", nil)
	b := r.Register("codex", 2, "/y", nil)
	c := r.Register("claude", 3, "/x", nil)

	if !(a.ConnID < b.ConnID && b.ConnID < c.ConnID) {
		t.Errorf("ConnIDs not monotonic: %d, %d, %d", a.ConnID, b.ConnID, c.ConnID)
	}
}

func TestStubRegistry_GetByConnID(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 1, "/x", nil)

	got, ok := r.Get(s.ConnID)
	if !ok {
		t.Fatal("expected to find by ConnID")
	}
	if got.PID != 1 || got.CWD != "/x" {
		t.Errorf("got %+v", got)
	}
}

func TestStubRegistry_Unregister(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 1, "/x", nil)
	r.Unregister(s.ConnID)
	if _, ok := r.Get(s.ConnID); ok {
		t.Error("expected stub gone after Unregister")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -run TestStubRegistry -v
```

Expected: compile error, `StubRegistry`/`NewStubRegistry`/`Register`/`Get`/`Unregister` undefined.

- [ ] **Step 3: Implement StubRegistry**

Create `internal/broker/stubs.go`:

```go
package broker

import (
	"sync"
	"sync/atomic"
)

// Stub is the broker's view of a connected adapter. ConnID is the
// late-result-discard token described in spec §4.5.1.
type Stub struct {
	CLI    string
	PID    int
	CWD    string
	ConnID uint64

	// Conn is opaque from the registry's POV — broker package wires it after
	// constructing, used by route workers to write inbound to the right
	// adapter. Type is *ipc.Conn but kept as any here to avoid the import
	// cycle in the registry file.
	Conn any
}

// StubRegistry holds connected adapters keyed by ConnID. Concurrent-safe.
type StubRegistry struct {
	mu     sync.RWMutex
	next   atomic.Uint64
	byConn map[uint64]*Stub
}

// NewStubRegistry returns an empty registry. The first ConnID handed out is 1
// (uint64 0 is reserved for "no stub").
func NewStubRegistry() *StubRegistry {
	return &StubRegistry{byConn: map[uint64]*Stub{}}
}

// Register creates a new Stub with a monotonic ConnID and returns it.
func (r *StubRegistry) Register(cli string, pid int, cwd string, conn any) *Stub {
	id := r.next.Add(1)
	s := &Stub{CLI: cli, PID: pid, CWD: cwd, ConnID: id, Conn: conn}
	r.mu.Lock()
	r.byConn[id] = s
	r.mu.Unlock()
	return s
}

// Get returns the stub for connID and whether it's present.
func (r *StubRegistry) Get(connID uint64) (*Stub, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byConn[connID]
	return s, ok
}

// Unregister removes the stub. No-op if not present.
func (r *StubRegistry) Unregister(connID uint64) {
	r.mu.Lock()
	delete(r.byConn, connID)
	r.mu.Unlock()
}

// Snapshot returns a copy of all currently-registered stubs. Used by status.
func (r *StubRegistry) Snapshot() []*Stub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Stub, 0, len(r.byConn))
	for _, s := range r.byConn {
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all three TestStubRegistry pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/stubs.go internal/broker/stubs_test.go
git commit -m "c3-v3: StubRegistry with monotonic ConnID"
```

---

### Task 3.8: ROUTES map with claim/release

**Files:**
- Create: `internal/broker/routes.go`
- Create: `internal/broker/routes_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/routes_test.go`:

```go
package broker

import "testing"

func TestRoutes_ClaimSucceedsOnFreeRoute(t *testing.T) {
	r := NewRoutes()
	stub := &Stub{ConnID: 1, CLI: "claude", PID: 1, CWD: "/x"}
	key := MakeRouteKey("telegram", -100, ptrI64(281))

	holder, ok := r.Claim(key, stub)
	if !ok {
		t.Fatalf("first claim should succeed; got holder %+v", holder)
	}
}

func TestRoutes_ClaimFailsWhenHeld(t *testing.T) {
	r := NewRoutes()
	first := &Stub{ConnID: 1, CLI: "claude", PID: 1, CWD: "/x"}
	second := &Stub{ConnID: 2, CLI: "codex", PID: 2, CWD: "/x"}
	key := MakeRouteKey("telegram", -100, ptrI64(281))

	r.Claim(key, first)
	holder, ok := r.Claim(key, second)
	if ok {
		t.Error("second claim should fail")
	}
	if holder == nil || holder.ConnID != first.ConnID {
		t.Errorf("expected holder = first stub, got %+v", holder)
	}
}

func TestRoutes_ReleaseFreesRoute(t *testing.T) {
	r := NewRoutes()
	first := &Stub{ConnID: 1, CLI: "claude", PID: 1, CWD: "/x"}
	second := &Stub{ConnID: 2, CLI: "codex", PID: 2, CWD: "/x"}
	key := MakeRouteKey("telegram", -100, ptrI64(281))

	r.Claim(key, first)
	r.Release(key, first.ConnID)

	if _, ok := r.Claim(key, second); !ok {
		t.Error("after release, second claim should succeed")
	}
}

func TestRoutes_ReleaseByWrongOwnerIsNoop(t *testing.T) {
	r := NewRoutes()
	first := &Stub{ConnID: 1}
	key := MakeRouteKey("telegram", -100, ptrI64(281))

	r.Claim(key, first)
	r.Release(key, 999) // wrong ConnID

	holder, _ := r.Holder(key)
	if holder == nil || holder.ConnID != 1 {
		t.Error("release by wrong owner should be no-op")
	}
}

func TestRoutes_ReleaseAllByConnID(t *testing.T) {
	r := NewRoutes()
	stub := &Stub{ConnID: 1}
	k1 := MakeRouteKey("telegram", -100, ptrI64(281))
	k2 := MakeRouteKey("telegram", -100, ptrI64(207))
	r.Claim(k1, stub)
	r.Claim(k2, stub)

	released := r.ReleaseAllByConnID(1)
	if len(released) != 2 {
		t.Errorf("expected 2 routes released, got %d", len(released))
	}
}

func ptrI64(v int64) *int64 { return &v }
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -run TestRoutes -v
```

Expected: compile error, `Routes`/`NewRoutes`/`Claim`/`Release`/`Holder`/`ReleaseAllByConnID` undefined.

- [ ] **Step 3: Implement Routes**

Create `internal/broker/routes.go`:

```go
package broker

import "sync"

// Routes is the in-memory ROUTES map. Single-claim-per-route invariant
// (spec §4.2.1).
type Routes struct {
	mu sync.RWMutex
	m  map[RouteKey]*Stub
}

// NewRoutes returns an empty routes table.
func NewRoutes() *Routes {
	return &Routes{m: map[RouteKey]*Stub{}}
}

// Claim attempts to insert (key → stub). Returns (current_holder, false) if
// the route is held by a different stub; (stub, true) on success or no-op
// re-claim by the same stub.
func (r *Routes) Claim(key RouteKey, stub *Stub) (*Stub, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.m[key]; ok {
		if existing.ConnID == stub.ConnID {
			return existing, true // idempotent
		}
		return existing, false
	}
	r.m[key] = stub
	return stub, true
}

// Release drops the claim for key, but only if the holder matches connID. No-op
// if the route is held by someone else or unheld.
func (r *Routes) Release(key RouteKey, connID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.m[key]; ok && existing.ConnID == connID {
		delete(r.m, key)
	}
}

// ReleaseAllByConnID drops every claim held by connID. Used on adapter
// disconnect (clean or unclean). Returns the released keys.
func (r *Routes) ReleaseAllByConnID(connID uint64) []RouteKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var released []RouteKey
	for k, s := range r.m {
		if s.ConnID == connID {
			delete(r.m, k)
			released = append(released, k)
		}
	}
	return released
}

// Holder returns the current holder of key, or nil if unheld.
func (r *Routes) Holder(key RouteKey) (*Stub, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.m[key]
	return s, ok
}

// Snapshot returns a slice of (key, stub) pairs for diagnostics.
func (r *Routes) Snapshot() []RouteEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RouteEntry, 0, len(r.m))
	for k, s := range r.m {
		out = append(out, RouteEntry{Key: k, Stub: s})
	}
	return out
}

// RouteEntry is one row of Routes.Snapshot.
type RouteEntry struct {
	Key  RouteKey
	Stub *Stub
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all five TestRoutes tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/routes.go internal/broker/routes_test.go
git commit -m "c3-v3: Routes with claim/release/Holder + by-conn cleanup"
```

---

## Phase 3d: Connection lifecycle ops

### Task 3.9: Broker struct + per-connection handler

**Files:**
- Create: `internal/broker/broker.go`
- Create: `internal/broker/handler.go`
- Create: `internal/broker/handler_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/handler_test.go`:

```go
package broker

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// runHandlerWithPeer wires up an in-process broker handler against a
// net.Pipe pair. Returns the peer Conn that simulates the adapter.
func runHandlerWithPeer(t *testing.T, mf *mappings.MappingsFile) (*ipc.Conn, func()) {
	t.Helper()
	a, b := net.Pipe()
	br := &Broker{
		Mappings: mf,
		Stubs:    NewStubRegistry(),
		Routes:   NewRoutes(),
	}
	go br.HandleConn(a)
	return ipc.NewConn(b), func() {
		_ = a.Close()
		_ = b.Close()
	}
}

func emptyMappings() *mappings.MappingsFile {
	return &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]mappings.ChannelConfig{},
		Mappings:      map[string]mappings.Mapping{},
	}
}

func TestHandle_HelloAck_NoConfig(t *testing.T) {
	mf := &mappings.MappingsFile{SchemaVersion: 1} // no channels
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.HelloAckMsg
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Op != ipc.OpHelloAck {
		t.Errorf("op=%q, want hello_ack", ack.Op)
	}
	if !ack.NoConfig {
		t.Error("expected NoConfig=true when channels map is empty")
	}
	if ack.ConnID == 0 {
		t.Error("ConnID should be assigned")
	}
}

func TestHandle_HelloAck_NoMapping(t *testing.T) {
	mf := emptyMappings()
	mf.Channels["telegram"] = mappings.ChannelConfig{DefaultGroup: "main"}
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/unknown"})
	raw, _ := peer.ReadFrame()
	var ack ipc.HelloAckMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.NoConfig {
		t.Error("NoConfig should be false when channel exists")
	}
	if !ack.NoMapping {
		t.Error("NoMapping should be true for unknown cwd")
	}
}

func TestHandle_ListTopics(t *testing.T) {
	mf := emptyMappings()
	mf.Channels["telegram"] = mappings.ChannelConfig{
		DefaultGroup: "main",
		Groups:       map[string]mappings.GroupConfig{"main": {ChatID: -100}},
		Topics: []mappings.Topic{
			{ChatID: -100, TopicID: 281, Name: "c3", Group: "main"},
		},
	}
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	_, _ = peer.ReadFrame() // consume hello_ack

	_ = peer.WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics})
	raw, _ := peer.ReadFrame()

	var resp ipc.TopicsListMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Op != ipc.OpTopicsList {
		t.Errorf("op=%q, want topics_list", resp.Op)
	}
	if len(resp.Topics) != 1 || resp.Topics[0].Name != "c3" {
		t.Errorf("topics = %+v, want one entry name=c3", resp.Topics)
	}
}

func TestHandle_ByeClosesCleanly(t *testing.T) {
	mf := emptyMappings()
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	_, _ = peer.ReadFrame()

	_ = peer.WriteJSON(ipc.ByeReq{Op: ipc.OpBye})

	// After bye, the broker should close its side. Reading should hit io.EOF
	// within a reasonable time.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := peer.ReadFrame()
		if err == io.EOF {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("broker did not close conn after bye within 2s")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: compile error, `Broker`/`HandleConn` undefined.

- [ ] **Step 3: Implement Broker + HandleConn**

Create `internal/broker/broker.go`:

```go
package broker

import (
	"github.com/karthikeyan5/c3/internal/mappings"
)

// Broker holds the in-memory state shared by all connections: stubs registry,
// routes table, and a snapshot of the mappings.json config.
//
// Phase 3 scope: read-only mappings. Write-back lands when channels create
// topics. See spec §4.2.
type Broker struct {
	Mappings *mappings.MappingsFile
	Stubs    *StubRegistry
	Routes   *Routes
}

// New returns a Broker with empty registries and the given mappings config.
func New(mf *mappings.MappingsFile) *Broker {
	return &Broker{
		Mappings: mf,
		Stubs:    NewStubRegistry(),
		Routes:   NewRoutes(),
	}
}
```

Create `internal/broker/handler.go`:

```go
package broker

import (
	"errors"
	"io"
	"net"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// HandleConn drives one adapter connection through its lifecycle. Owns the
// connection — closes it on return.
func (b *Broker) HandleConn(nc net.Conn) {
	conn := ipc.NewConn(nc)
	defer conn.Close()

	// Stage 1: expect hello first.
	raw, err := conn.ReadFrame()
	if err != nil {
		return
	}
	op, err := ipc.PeekOp(raw)
	if err != nil || op != ipc.OpHello {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "expected hello first"})
		return
	}
	var hello ipc.HelloMsg
	if err := unmarshalInto(raw, &hello); err != nil {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "malformed hello"})
		return
	}

	stub := b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, conn)
	defer b.Stubs.Unregister(stub.ConnID)
	defer b.Routes.ReleaseAllByConnID(stub.ConnID)

	// Build hello_ack. Phase 3 scope: report no_config / no_mapping flags
	// only — actual auto-attach claiming lands in Phase 4 (channels).
	ack := ipc.HelloAckMsg{Op: ipc.OpHelloAck, ConnID: stub.ConnID}
	if len(b.Mappings.Channels) == 0 {
		ack.NoConfig = true
	} else if _, ok := b.Mappings.LookupByCwd(hello.CWD); !ok {
		ack.NoMapping = true
	}
	if err := conn.WriteJSON(ack); err != nil {
		return
	}

	// Stage 2: dispatch loop.
	for {
		raw, err := conn.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// transient errors: log and exit — the adapter will reconnect.
			}
			return
		}
		op, err := ipc.PeekOp(raw)
		if err != nil {
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: err.Error()})
			continue
		}
		switch op {
		case ipc.OpListTopics:
			b.handleListTopics(conn)
		case ipc.OpRelease:
			b.Routes.ReleaseAllByConnID(stub.ConnID)
		case ipc.OpBye:
			return
		default:
			// Ops not yet implemented in Phase 3.
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "op not implemented in phase 3: " + string(op)})
		}
	}
}

func (b *Broker) handleListTopics(conn *ipc.Conn) {
	resp := ipc.TopicsListMsg{Op: ipc.OpTopicsList}
	for chanName, cc := range b.Mappings.Channels {
		for _, tp := range cc.Topics {
			entry := ipc.TopicEntry{
				Channel: chanName,
				ChatID:  tp.ChatID,
				TopicID: tp.TopicID,
				Name:    tp.Name,
				Group:   tp.Group,
			}
			key := MakeRouteKey(chanName, tp.ChatID, ptrI64Val(tp.TopicID))
			if holder, ok := b.Routes.Holder(key); ok {
				entry.ClaimedBy = &ipc.Holder{CLI: holder.CLI, PID: holder.PID, CWD: holder.CWD}
			}
			resp.Topics = append(resp.Topics, entry)
		}
	}
	_ = conn.WriteJSON(resp)
}

// ptrI64Val returns &v.
func ptrI64Val(v int64) *int64 { return &v }

// unmarshalInto is a tiny helper to keep test-side and broker-side parsing
// consistent. Lives here (not ipc) because broker-only.
func unmarshalInto(raw []byte, v any) error {
	return ipcUnmarshal(raw, v)
}

// ipcUnmarshal is a thin wrapper to avoid importing encoding/json directly in
// every handler call site.
func ipcUnmarshal(raw []byte, v any) error {
	return jsonUnmarshal(raw, v)
}

func jsonUnmarshal(raw []byte, v any) error {
	return _jsonUnmarshal(raw, v)
}
```

Wait — the chained helper indirection is ugly. Replace the bottom-of-file helpers with a single direct call. Edit `handler.go` to use `encoding/json` directly:

Replace the trailing helpers with:

```go
import (
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// ... (keep HandleConn and handleListTopics as written above, but)
// replace the call `unmarshalInto(raw, &hello)` with `json.Unmarshal(raw, &hello)`
// and DELETE the unmarshalInto / ipcUnmarshal / jsonUnmarshal / _jsonUnmarshal helpers.
```

Final `handler.go`:

```go
package broker

import (
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings" //nolint:unused // kept for future ops
)

var _ = mappings.Mapping{} // silence unused import in phase 3

// HandleConn drives one adapter connection through its lifecycle.
func (b *Broker) HandleConn(nc net.Conn) {
	conn := ipc.NewConn(nc)
	defer conn.Close()

	// Stage 1: hello.
	raw, err := conn.ReadFrame()
	if err != nil {
		return
	}
	op, err := ipc.PeekOp(raw)
	if err != nil || op != ipc.OpHello {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "expected hello first"})
		return
	}
	var hello ipc.HelloMsg
	if err := json.Unmarshal(raw, &hello); err != nil {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "malformed hello"})
		return
	}

	stub := b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, conn)
	defer b.Stubs.Unregister(stub.ConnID)
	defer b.Routes.ReleaseAllByConnID(stub.ConnID)

	ack := ipc.HelloAckMsg{Op: ipc.OpHelloAck, ConnID: stub.ConnID}
	if len(b.Mappings.Channels) == 0 {
		ack.NoConfig = true
	} else if _, ok := b.Mappings.LookupByCwd(hello.CWD); !ok {
		ack.NoMapping = true
	}
	if err := conn.WriteJSON(ack); err != nil {
		return
	}

	// Stage 2: dispatch loop.
	for {
		raw, err := conn.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// transient — let the connection close.
			}
			return
		}
		op, err := ipc.PeekOp(raw)
		if err != nil {
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: err.Error()})
			continue
		}
		switch op {
		case ipc.OpListTopics:
			b.handleListTopics(conn)
		case ipc.OpRelease:
			b.Routes.ReleaseAllByConnID(stub.ConnID)
		case ipc.OpBye:
			return
		default:
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "op not implemented in phase 3: " + string(op)})
		}
	}
}

func (b *Broker) handleListTopics(conn *ipc.Conn) {
	resp := ipc.TopicsListMsg{Op: ipc.OpTopicsList}
	for chanName, cc := range b.Mappings.Channels {
		for _, tp := range cc.Topics {
			entry := ipc.TopicEntry{
				Channel: chanName, ChatID: tp.ChatID,
				TopicID: tp.TopicID, Name: tp.Name, Group: tp.Group,
			}
			key := MakeRouteKey(chanName, tp.ChatID, ptrI64Val(tp.TopicID))
			if holder, ok := b.Routes.Holder(key); ok {
				entry.ClaimedBy = &ipc.Holder{CLI: holder.CLI, PID: holder.PID, CWD: holder.CWD}
			}
			resp.Topics = append(resp.Topics, entry)
		}
	}
	_ = conn.WriteJSON(resp)
}

func ptrI64Val(v int64) *int64 { return &v }
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all four TestHandle_* tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/broker.go internal/broker/handler.go internal/broker/handler_test.go
git commit -m "c3-v3: broker connection handler — hello/list_topics/release/bye"
```

---

### Task 3.10: Socket server + main loop

**Files:**
- Create: `internal/broker/server.go`
- Create: `internal/broker/server_test.go`

- [ ] **Step 1: Write the failing test (integration-style)**

Create `internal/broker/server_test.go`:

```go
package broker

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestServer_AcceptsAndHandlesHello(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	srv, err := Listen(sockPath, b)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Connect.
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn := ipc.NewConn(c)

	if err := conn.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := conn.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.HelloAckMsg
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Op != ipc.OpHelloAck {
		t.Errorf("op=%q, want hello_ack", ack.Op)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -run TestServer -v
```

Expected: compile error, `Listen`/`Stop` undefined.

- [ ] **Step 3: Implement the server**

Create `internal/broker/server.go`:

```go
package broker

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// Server is a unix-socket accept loop driven by a Broker.
type Server struct {
	ln    net.Listener
	br    *Broker
	wg    sync.WaitGroup
	stop  atomic.Bool
}

// Listen binds a unix socket at path (mode 0600) and starts accepting
// connections. Caller must Stop() at shutdown to drain in-flight handlers.
func Listen(path string, br *Broker) (*Server, error) {
	// Best-effort: remove a stale socket from a previous broker that crashed
	// without unlinking. The flock-singleton path prevents concurrent
	// brokers, so racing is not a concern here.
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		return nil, err
	}

	s := &Server{ln: ln, br: br}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.stop.Load() {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient accept error — log via stderr and keep going. The
			// listener doesn't typically recover from this in practice, so
			// in production we'd want a backoff; in this scaffold, just
			// return and let the broker exit.
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.br.HandleConn(c)
		}(c)
	}
}

// Stop closes the listener and drains in-flight handlers.
func (s *Server) Stop() {
	if !s.stop.CompareAndSwap(false, true) {
		return
	}
	_ = s.ln.Close()
	s.wg.Wait()
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/karthi/arogara/c3 && go test ./internal/broker/... -v
```

Expected: all broker tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/server.go internal/broker/server_test.go
git commit -m "c3-v3: broker socket server with accept loop + clean shutdown"
```

---

### Task 3.11: Wire up `cmd/c3-broker/main.go`

**Files:**
- Modify: `cmd/c3-broker/main.go`

- [ ] **Step 1: Replace placeholder main with real boot**

Replace `cmd/c3-broker/main.go` with:

```go
// c3-broker is the C3 daemon. It owns the unix socket, the in-memory
// routes/stubs registries, and (in subsequent phases) the channel modules.
//
// Singleton-per-machine via flock on $XDG_RUNTIME_DIR/c3-broker.pid (or
// fallback). Spawned by adapters via exec.Command + setsid; runs until its
// parent process group is killed or it receives SIGTERM/SIGINT.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	pidFile := broker.PidFilePath()
	lock, err := broker.AcquireSingleton(pidFile)
	if err != nil {
		// Sibling broker already running; this is the expected path when an
		// adapter racing-spawns and we lose. Exit silently.
		return nil
	}
	defer lock.Release()

	mfPath, err := mappings.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve mappings path: %w", err)
	}
	var mf *mappings.MappingsFile
	mf, err = mappings.Read(mfPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Spec §5.1 first-install path: write a minimal skeleton, keep running.
			mf = &mappings.MappingsFile{
				SchemaVersion: 1,
				Channels:      map[string]mappings.ChannelConfig{},
				Mappings:      map[string]mappings.Mapping{},
			}
			if err := os.MkdirAll(directoryOf(mfPath), 0700); err != nil {
				return fmt.Errorf("mkdir mappings parent: %w", err)
			}
			if err := mappings.Write(mfPath, mf); err != nil {
				return fmt.Errorf("write skeleton mappings: %w", err)
			}
			fmt.Fprintf(os.Stderr, "c3-broker: wrote skeleton %s — run /c3-setup to configure\n", mfPath)
		} else {
			// Corruption recovery (spec §4.3): log and exit. No silent fallback.
			return fmt.Errorf("read mappings %s: %w", mfPath, err)
		}
	}
	if err := mf.Validate(); err != nil {
		return fmt.Errorf("validate mappings: %w", err)
	}

	br := broker.New(mf)
	srv, err := broker.Listen(broker.SocketPath(), br)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	fmt.Fprintf(os.Stderr, "c3-broker: listening on %s (pid %d)\n", broker.SocketPath(), os.Getpid())

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGTERM, syscall.SIGINT)
	<-sigC

	srv.Stop()
	return nil
}

func directoryOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
```

- [ ] **Step 2: Build**

```bash
cd /home/karthi/arogara/c3 && go build ./cmd/c3-broker
```

Expected: success.

- [ ] **Step 3: Smoke test — start broker, connect via socat, send hello, see ack**

Manual test:

```bash
cd /home/karthi/arogara/c3 && ./bin/c3-broker &
BROKER=$!
sleep 0.3
SOCK=$(ls /run/user/$UID/c3.sock 2>/dev/null || echo /tmp/c3-$UID.sock)
echo '{"op":"hello","cli":"smoketest","pid":1,"cwd":"/tmp"}' | socat - UNIX-CONNECT:$SOCK
# Expected stdout: a hello_ack JSON with conn_id and either no_config or no_mapping
kill $BROKER
```

If `socat` isn't available, skip — the integration test in Task 3.10 covers this.

- [ ] **Step 4: Commit**

```bash
git add cmd/c3-broker/main.go
git commit -m "c3-v3: c3-broker main — singleton + mappings load + socket server"
```

---

### Task 3.12: Final integration check

**Files:** none (verification only)

- [ ] **Step 1: Full test suite**

```bash
cd /home/karthi/arogara/c3 && go test ./... -v
```

Expected: every test passes.

- [ ] **Step 2: Build**

```bash
cd /home/karthi/arogara/c3 && make build
```

Expected: all four binaries built.

- [ ] **Step 3: Live broker smoke test (optional)**

```bash
cd /home/karthi/arogara/c3 && ./bin/c3-broker &
sleep 0.3
ls -la "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" 2>/dev/null || ls -la /tmp/c3-$UID.sock
ls -la "${XDG_RUNTIME_DIR:-$HOME/.cache/c3}/c3-broker.pid" 2>/dev/null
pkill c3-broker
```

Expected: socket file at expected path with mode 0600, pid file with our pid.

- [ ] **Step 4: Final commit (if smoke test prompted any tweaks)**

If nothing changed, skip. Otherwise:

```bash
git add -A
git commit -m "c3-v3: smoke-test fixes from broker live run"
```

---

## Out of scope for this plan

The following come in subsequent plans:

- **Plan 4 (spec §4.1, §6):** Telegram channel cleanroom Go implementation using `gotgbot/v2`. `Channel` interface, getUpdates loop, allowed_updates opt-in, inbound emission, outbound tools (reply/react/edit_message/download_attachment/send_typing/edit_progress), `notifications/claude/channel` payload (manual JSON-RPC framing per spec §4.4.4), `validate_topic` via sendChatAction, `create_topic` with rate-limit retry, attach proposal flow (cross-group search, validate-by-id, create+register), `OnInbound` plugin chain, debounce window with cap, cooldown-fallback reply.

- **Plan 5 (spec §4.5, §8):** Plugin host + STT plugin. `internal/plugin/host.go` Host interface, builtin registry, hook firing order, STT shim with `//go:embed handler.py` + write-to-XDG_DATA pattern, integration with channel layer.

- **Plan 6 (spec §4.4):** Claude Code adapter. `cmd/c3-claude-adapter/main.go`. MCP stdio server using `modelcontextprotocol/go-sdk`. Manual JSON-RPC framing for `notifications/claude/channel`. Adapter-local `attach`/`topics` handling. Tool list aggregation.

- **Plan 7 (spec §4.4):** Codex bridge in Go (launcher + adapter + install-codex-shim subcommand).

- **Plan 8 (spec §7):** Per-route serial executor full implementation, typing indicator, `edit_progress` placeholder lifecycle, debounce + dedup. (Some of this may fold into Plan 4 if it's smaller than expected.)

- **Plan 9 (spec §11):** `/c3-setup`, `/c3-build`, `/c3-status` slash commands; Codex `SETUP.md`.

- **Plan 10 (spec §13):** README/INSTALL rewrite, deviation banner retirement, public release tag.

After this plan, the broker daemon runs, accepts connections, replies to hello/list_topics/release/bye, but doesn't yet route messages or speak to Telegram. Those land in Plan 4.

---

## Self-review checklist

- [x] **Spec coverage:** §4.2 (RouteKey, ROUTES, claim lifecycle) → tasks 3.6-3.8. §4.2.2 (broker process model) → tasks 3.4-3.5, 3.10-3.11. §4.4.1 (IPC types, writer mutex) → tasks 3.1-3.3.
- [x] **No placeholders:** every code step has full code.
- [x] **Type consistency:** `Stub.ConnID` (uint64) used identically in StubRegistry, Routes, Broker, ipc.HelloAckMsg. `RouteKey` constructed via MakeRouteKey at every entry point.
- [x] **TDD discipline:** failing-test → run-fail → implement → run-pass → commit on every task except 3.11 (binary main, smoke-tested manually).
- [x] **Granularity:** ~12 tasks, each 3-5 minutes of work plus tests.
- [x] **Frequent commits:** every task ends with a commit. Plan = 12 commits.

The architecture concerns from spec R3 (per-route serial executor, manual JSON-RPC framing, ConnID late-result-discard) are documented in the spec; the *implementation* of the per-route serial executor and ConnID-stamped jobs lands in Plan 4 alongside the channel layer (which is what generates the inbound jobs that need serializing). Phase 3 builds the substrate; Phase 4 starts using it.
