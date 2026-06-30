# Connectivity Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework C3's "cannot reach Telegram" alerts so the desktop popup is primary, the CLI turn-injection is a fallback only when the popup didn't deliver, and an ambient Claude Code status-line indicator shows/clears the outage — gated by a new `notifications.invasive` toggle.

**Architecture:** Inside `BrokerHost.NotifyHealth`, split the existing four always-on sinks into an **ambient tier** (status cache + broker log + new `health.json` state file — always written) and an **invasive tier** (desktop popup primary; CLI broadcast fallback only when the popup reports not-delivered, down-edge only) gated by `MappingsFile.InvasiveNotifications()`. A fail-safe segment in the personal `~/.claude` status-line script reads `health.json`.

**Tech Stack:** Go (module `github.com/karthikeyan5/c3`), standard library only; bash + jq for the status-line script; `~/.claude/settings.json` for `refreshInterval`.

## Global Constraints

- Module path: `github.com/karthikeyan5/c3`. All `go test` commands run from repo root `~/arogara/c3`.
- Work on branch `feat/connectivity-notifications` (already created; the design spec is committed there). **Do NOT push and do NOT merge to master** — Karthi ratifies in the morning.
- Toggle: `notifications.invasive`, represented as `*bool` (nil ⇒ default `true`). A bare `bool` would zero-value to `false` and silently disable alerts — forbidden.
- Status file: `$XDG_STATE_HOME/c3/health.json` (fallback `$HOME/.local/state/c3/health.json`), resolved by `HealthFilePath()`; env `C3_HEALTH_FILE` overrides (tests only). Dir mode `0700`, file mode `0600`.
- `health.json` write is **atomic** (`os.CreateTemp` in the target's own dir + `rename`); **no fsync** (best-effort status file — a lost write only means a briefly-stale indicator).
- "delivered" = the notifier binary was resolved AND `cmd.Run()` returned zero exit within the 2s timeout. Missing binary ⇒ not delivered. It is a proxy for "spawned," NOT proof the user saw it.
- CLI fallback fires only when `!delivered` AND `state == down`. **No CLI injection on recovery.**
- Ambient tier (cache, log, `health.json`) is always written, even when `invasive:false`.
- `refreshInterval: 5` goes **inside** the `statusLine` object in `~/.claude/settings.json` (a top-level key is ignored).
- Follow existing code patterns. TDD. Commit after each task. Each commit message ends with the trailer:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

---

### Task 1: Config toggle in `internal/mappings`

**Files:**
- Modify: `internal/mappings/types.go` (add `Notifications` field to `MappingsFile`; add `NotificationsConfig` + `InvasiveNotifications()`)
- Modify: `internal/mappings/clone.go` (deep-copy `Notifications`)
- Test: `internal/mappings/clone_test.go` (add a clone-preservation test)
- Test: `internal/mappings/notifications_test.go` (new — default matrix + JSON parse)

**Interfaces:**
- Produces: `mappings.NotificationsConfig{Invasive *bool}`; `(*mappings.MappingsFile).InvasiveNotifications() bool` (nil-safe, default true). Task 6 consumes `InvasiveNotifications()`.

- [ ] **Step 1: Write the failing tests** — create `internal/mappings/notifications_test.go`:

```go
package mappings

import (
	"encoding/json"
	"testing"
)

func TestInvasiveNotifications_Default(t *testing.T) {
	// absent block ⇒ true
	if got := (&MappingsFile{SchemaVersion: 1}).InvasiveNotifications(); !got {
		t.Errorf("absent notifications: got %v, want true", got)
	}
	// present but empty ⇒ true
	if got := (&MappingsFile{Notifications: &NotificationsConfig{}}).InvasiveNotifications(); !got {
		t.Errorf("empty notifications: got %v, want true", got)
	}
	// explicit false ⇒ false
	no := false
	if got := (&MappingsFile{Notifications: &NotificationsConfig{Invasive: &no}}).InvasiveNotifications(); got {
		t.Errorf("explicit false: got %v, want false", got)
	}
	// explicit true ⇒ true
	yes := true
	if got := (&MappingsFile{Notifications: &NotificationsConfig{Invasive: &yes}}).InvasiveNotifications(); !got {
		t.Errorf("explicit true: got %v, want true", got)
	}
	// nil receiver ⇒ true (defensive)
	var nilmf *MappingsFile
	if got := nilmf.InvasiveNotifications(); !got {
		t.Errorf("nil receiver: got %v, want true", got)
	}
}

func TestInvasiveNotifications_JSONParse(t *testing.T) {
	var absent MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"channels":{},"mappings":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if !absent.InvasiveNotifications() {
		t.Error("absent notifications block parsed: want invasive=true")
	}
	var off MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"notifications":{"invasive":false}}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.InvasiveNotifications() {
		t.Error("invasive:false parsed: want false")
	}
}
```

Also append to `internal/mappings/clone_test.go` (it already has `package mappings`, imports `testing` + `time`):

```go
func TestClone_PreservesNotifications(t *testing.T) {
	no := false
	original := &MappingsFile{SchemaVersion: 1, Notifications: &NotificationsConfig{Invasive: &no}}
	clone := original.Clone()
	if clone.Notifications == nil || clone.Notifications.Invasive == nil {
		t.Fatal("clone dropped Notifications")
	}
	if *clone.Notifications.Invasive != false {
		t.Errorf("clone Invasive = %v, want false", *clone.Notifications.Invasive)
	}
	// Mutating the clone must not touch the original (deep copy).
	*clone.Notifications.Invasive = true
	if *original.Notifications.Invasive != false {
		t.Error("clone leak: mutating clone changed original Invasive")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mappings/ -run 'Notifications' -v`
Expected: FAIL — compile errors (`NotificationsConfig` undefined, `InvasiveNotifications` undefined).

- [ ] **Step 3: Add the type + field + helper** — in `internal/mappings/types.go`, add `Notifications` to the `MappingsFile` struct so it reads:

```go
type MappingsFile struct {
	SchemaVersion int                       `json:"schema_version"`
	Channels      map[string]ChannelConfig  `json:"channels"`
	Codex         *CodexConfig              `json:"codex,omitempty"`
	Mappings      map[string]Mapping        `json:"mappings"`
	Plugins       map[string]map[string]any `json:"plugins,omitempty"`
	Allowlist     *Allowlist                `json:"allowlist,omitempty"`
	Notifications *NotificationsConfig      `json:"notifications,omitempty"`
}
```

and add (anywhere in the file, e.g. after the `Allowlist` type):

```go
// NotificationsConfig governs the "invasive" health-notification surfaces
// (desktop popup + CLI turn-injection). The ambient status-line indicator is
// always on and is NOT governed here.
type NotificationsConfig struct {
	// Invasive gates the desktop popup + CLI fallback. nil ⇒ default true.
	// (A plain bool would zero-value to false and silently disable alerts for
	// every user who never set it.)
	Invasive *bool `json:"invasive,omitempty"`
}

// InvasiveNotifications reports whether invasive health notifications (desktop
// popup + CLI fallback) are enabled. Absent config ⇒ true (preserve behavior).
func (mf *MappingsFile) InvasiveNotifications() bool {
	if mf == nil || mf.Notifications == nil || mf.Notifications.Invasive == nil {
		return true
	}
	return *mf.Notifications.Invasive
}
```

- [ ] **Step 4: Teach `Clone()` to copy it** — in `internal/mappings/clone.go`, inside `Clone()`, immediately before the final `return out`, add:

```go
	if mf.Notifications != nil {
		nc := NotificationsConfig{}
		if mf.Notifications.Invasive != nil {
			v := *mf.Notifications.Invasive
			nc.Invasive = &v
		}
		out.Notifications = &nc
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/mappings/ -v`
Expected: PASS (new tests + the existing `TestClone_DeepCopyIsolatesMutations`).

- [ ] **Step 6: Commit**

```bash
git add internal/mappings/types.go internal/mappings/clone.go internal/mappings/clone_test.go internal/mappings/notifications_test.go
git commit -m "$(printf 'feat(mappings): add notifications.invasive toggle (default true)\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 2: `HealthFilePath()` helper

**Files:**
- Modify: `internal/broker/log.go` (add `HealthFilePath()` next to `LogPath()`)
- Test: `internal/broker/healthpath_test.go` (new)

**Interfaces:**
- Produces: `broker.HealthFilePath() string`. Tasks 3 + 6 + the status-line script depend on this path.

- [ ] **Step 1: Write the failing test** — create `internal/broker/healthpath_test.go`:

```go
package broker

import "testing"

func TestHealthFilePath_XDGStateHome(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "") // override off
	t.Setenv("XDG_STATE_HOME", "/tmp/xdgstate")
	if got := HealthFilePath(); got != "/tmp/xdgstate/c3/health.json" {
		t.Errorf("HealthFilePath with XDG set = %q, want /tmp/xdgstate/c3/health.json", got)
	}
}

func TestHealthFilePath_FallbackHome(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/tester")
	want := "/home/tester/.local/state/c3/health.json"
	if got := HealthFilePath(); got != want {
		t.Errorf("HealthFilePath fallback = %q, want %q", got, want)
	}
}

func TestHealthFilePath_EnvOverride(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "/custom/h.json")
	if got := HealthFilePath(); got != "/custom/h.json" {
		t.Errorf("HealthFilePath override = %q, want /custom/h.json", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/ -run HealthFilePath -v`
Expected: FAIL — `HealthFilePath` undefined.

- [ ] **Step 3: Add the helper** — in `internal/broker/log.go`, after `LogPath()`, add (the file already imports `os` and `path/filepath`):

```go
// HealthFilePath returns the path of the connectivity status file the Claude
// Code status line reads. Mirrors LogPath()'s XDG_STATE_HOME convention so it
// sits next to broker.log ($XDG_STATE_HOME/c3/health.json, fallback
// $HOME/.local/state/c3/health.json). C3_HEALTH_FILE overrides it (tests).
// IMPORTANT: do NOT use plugin_host.go:stateRoot() — it appends /state, which
// the status-line script does not look in.
func HealthFilePath() string {
	if env := os.Getenv("C3_HEALTH_FILE"); env != "" {
		return env
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "c3", "health.json")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/broker/ -run HealthFilePath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/log.go internal/broker/healthpath_test.go
git commit -m "$(printf 'feat(broker): add HealthFilePath() for the status-line state file\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 3: `WriteHealthFile()` — atomic status-file writer

**Files:**
- Create: `internal/broker/healthfile.go`
- Test: `internal/broker/healthfile_test.go` (new)

**Interfaces:**
- Consumes: `HealthFilePath()` (Task 2); `Broker.lastHealthSnapshot()` (existing, returns `map[string]c3types.HealthEvent`).
- Produces: `(*Broker).WriteHealthFile()` (exported — also called from `cmd/c3-broker/main.go`); `healthFileEntry` struct.

- [ ] **Step 1: Write the failing tests** — create `internal/broker/healthfile_test.go`:

```go
package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func TestWriteHealthFile_EmptySnapshot(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.WriteHealthFile()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatalf("read health file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "{}" {
		t.Errorf("empty snapshot health file = %q, want {}", string(data))
	}
}

func TestWriteHealthFile_DownEntry(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.setLastHealth(c3types.HealthEvent{
		Channel: "telegram", State: c3types.HealthStateDown,
		Since: time.Unix(1718722680, 0), Consec: 3, Reason: "dial failures",
	})
	b.WriteHealthFile()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("health file not valid JSON: %v (%s)", err, data)
	}
	tg, ok := got["telegram"]
	if !ok || tg.State != "down" || tg.SinceUnix != 1718722680 || tg.Consec != 3 {
		t.Errorf("telegram entry = %+v, want down/1718722680/3", tg)
	}
	if tg.SinceHHMM == "" {
		t.Error("since_hhmm should be populated")
	}
}

func TestWriteHealthFile_ConcurrentEdgesProduceValidJSON(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b := newTestBroker()
	b.setLastHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.WriteHealthFile() }()
	}
	wg.Wait()
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("after concurrent writes, health file not valid JSON: %v (%s)", err, data)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/broker/ -run WriteHealthFile -v`
Expected: FAIL — `healthFileEntry` undefined, `WriteHealthFile` undefined.

- [ ] **Step 3: Implement the writer** — create `internal/broker/healthfile.go`:

```go
package broker

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// healthFileEntry is the per-channel shape written to health.json for the
// Claude Code status line to read. JSON-tagged for the bash reader.
type healthFileEntry struct {
	State     string `json:"state"` // "up" | "down"
	SinceUnix int64  `json:"since_unix,omitempty"`
	SinceHHMM string `json:"since_hhmm,omitempty"` // ev.Since.Format("15:04"), local time
	Reason    string `json:"reason,omitempty"`
	Consec    int    `json:"consec,omitempty"`
}

// WriteHealthFile atomically writes the current per-channel health snapshot to
// HealthFilePath() for the status line to read. Best-effort: any error is
// logged and ignored (the status cache + broker log are the backstops). At
// startup lastHealth is empty, so this writes "{}" — clearing any stale file
// from a prior crash and reading as "no outage" to the status-line script.
// Atomic via CreateTemp-in-same-dir + rename; no fsync (best-effort).
func (b *Broker) WriteHealthFile() {
	snap := b.lastHealthSnapshot()
	out := make(map[string]healthFileEntry, len(snap))
	for ch, ev := range snap {
		out[ch] = healthFileEntry{
			State:     string(ev.State),
			SinceUnix: ev.Since.Unix(),
			SinceHHMM: ev.Since.Format("15:04"),
			Reason:    ev.Reason,
			Consec:    ev.Consec,
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		log.Printf("health-file: marshal failed: %v", err)
		return
	}
	path := HealthFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("health-file: mkdir %s failed: %v", dir, err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".health.*.tmp")
	if err != nil {
		log.Printf("health-file: create temp failed: %v", err)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		log.Printf("health-file: write temp failed: %v", err)
		return
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		log.Printf("health-file: chmod temp failed: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("health-file: close temp failed: %v", err)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("health-file: rename failed: %v", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/broker/ -run WriteHealthFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/healthfile.go internal/broker/healthfile_test.go
git commit -m "$(printf 'feat(broker): atomic health.json writer for the ambient status line\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 4: `Notify` returns a delivery result + popup body reword

**Files:**
- Modify: `internal/broker/desktopnotify.go` (`healthNotifier` interface + `Notify` return `bool`; reword DOWN popup body)
- Modify: `internal/broker/health_notify_test.go` (extend `fakeNotifier` to return a controllable bool — required so the package compiles)
- Test: `internal/broker/desktopnotify_test.go` (new — no-binary ⇒ not delivered)

**Interfaces:**
- Produces: `healthNotifier.Notify(ev c3types.HealthEvent) (delivered bool)`; `fakeNotifier{ch, delivered}`. Task 6 consumes the bool.

- [ ] **Step 1: Write the failing test** — create `internal/broker/desktopnotify_test.go`:

```go
package broker

import (
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func TestDesktopNotifier_NoBinaryReturnsNotDelivered(t *testing.T) {
	dn := &desktopNotifier{} // bin == "" — no notifier resolved
	got := dn.Notify(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now()})
	if got {
		t.Error("Notify with no binary should report not delivered")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/ -run DesktopNotifier -v`
Expected: FAIL — `dn.Notify(...)` used as value (current signature returns nothing).

- [ ] **Step 3: Change the interface + impl** — in `internal/broker/desktopnotify.go`:

Change the interface:
```go
type healthNotifier interface {
	Notify(ev c3types.HealthEvent) (delivered bool)
}
```

Replace `Notify` with the version that returns `delivered`:
```go
// Notify fires a desktop popup for the given health event and reports whether
// it was DELIVERED — meaning the notifier binary was resolved and exec'd to a
// zero exit within the 2s timeout. NOTE: "delivered" is a proxy for "spawned
// successfully", NOT proof the user saw it (notify-send/zenity exit 0 even with
// the screen locked, DND on, or no notification daemon running). The reliable
// not-delivered signal is a missing binary (the common headless/SSH case). The
// CLI fallback + status line are the backstops. Runs with the snapshotted env;
// recovers from panic (a notify failure must never propagate).
func (dn *desktopNotifier) Notify(ev c3types.HealthEvent) (delivered bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("health-notify: desktop notify panic recovered: %v", r)
			delivered = false
		}
	}()
	if dn == nil || dn.bin == "" {
		log.Printf("health-notify: no desktop notifier (notify-send/zenity) found; relying on CLI fallback + status")
		return false
	}

	title, body, urgency := formatHealthPopup(ev)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch dn.tool {
	case "notify-send":
		cmd = exec.CommandContext(ctx, dn.bin, "-u", urgency, "-a", "C3", title, body)
	case "zenity":
		cmd = exec.CommandContext(ctx, dn.bin, "--notification", "--text", title+" — "+body)
	default:
		return false
	}
	cmd.Env = dn.env
	if err := cmd.Run(); err != nil {
		log.Printf("health-notify: desktop notify exec failed (tool=%s): %v", dn.tool, err)
		return false
	}
	return true
}
```

In `formatHealthPopup`, reword the DOWN `body` so it doesn't contradict the new CLI-fallback wording — change:
```go
		body = fmt.Sprintf("Cannot reach %s since %s (%d %s). Inbound offline — alerts here only.",
			ch, ev.Since.Format("15:04"), ev.Consec, reasonOr(ev.Reason, "failures"))
```
to:
```go
		body = fmt.Sprintf("Cannot reach %s since %s (%d %s). Inbound offline until it recovers.",
			ch, ev.Since.Format("15:04"), ev.Consec, reasonOr(ev.Reason, "failures"))
```

- [ ] **Step 4: Update `fakeNotifier` so the package compiles** — in `internal/broker/health_notify_test.go`, replace the `fakeNotifier` definition + constructor + method with:

```go
// fakeNotifier records desktop-notify calls instead of spawning a real popup,
// and reports a controllable delivered result.
type fakeNotifier struct {
	ch        chan c3types.HealthEvent
	delivered bool
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{ch: make(chan c3types.HealthEvent, 4), delivered: true}
}

func (f *fakeNotifier) Notify(ev c3types.HealthEvent) bool {
	f.ch <- ev
	return f.delivered
}
```

(The existing `TestNotifyHealth_FanOut` still compiles and passes here — `NotifyHealth` is unchanged until Task 6; the fake defaults `delivered:true`, and the current `NotifyHealth` broadcasts unconditionally.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/broker/ -run 'DesktopNotifier|NotifyHealth_FanOut' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/desktopnotify.go internal/broker/desktopnotify_test.go internal/broker/health_notify_test.go
git commit -m "$(printf 'feat(broker): desktop Notify reports delivery result\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 5: `systemEventForHealth` desktop-unavailable note

**Files:**
- Modify: `internal/broker/host.go` (`systemEventForHealth` gains a `desktopUnavailable bool` param; update its one existing caller to pass `false` — behavior preserved until Task 6)
- Test: `internal/broker/host_test.go` (new)

**Interfaces:**
- Produces: `systemEventForHealth(ev c3types.HealthEvent, desktopUnavailable bool) *c3types.SystemEvent`. Task 6 calls it with `true` from the fallback path.

- [ ] **Step 1: Write the failing test** — create `internal/broker/host_test.go`:

```go
package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func TestSystemEventForHealth_DesktopUnavailableNote(t *testing.T) {
	ev := c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"}
	withNote := systemEventForHealth(ev, true)
	if !strings.Contains(withNote.Message, "desktop notification unavailable") {
		t.Errorf("desktopUnavailable=true message missing note: %q", withNote.Message)
	}
	noNote := systemEventForHealth(ev, false)
	if strings.Contains(noNote.Message, "desktop notification unavailable") {
		t.Errorf("desktopUnavailable=false message should not have note: %q", noNote.Message)
	}
	if noNote.Level != "warn" {
		t.Errorf("down event level = %q, want warn", noNote.Level)
	}
}

func TestSystemEventForHealth_RecoveryHasNoNote(t *testing.T) {
	ev := c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateUp, DownFor: 5 * time.Minute}
	up := systemEventForHealth(ev, true) // even with true, a recovery message carries no down note
	if strings.Contains(up.Message, "desktop notification unavailable") {
		t.Errorf("recovery message should never have the desktop note: %q", up.Message)
	}
	if up.Level != "info" {
		t.Errorf("recovery level = %q, want info", up.Level)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/ -run SystemEventForHealth -v`
Expected: FAIL — too many arguments to `systemEventForHealth`.

- [ ] **Step 3: Add the param** — in `internal/broker/host.go`, replace `systemEventForHealth` with:

```go
func systemEventForHealth(ev c3types.HealthEvent, desktopUnavailable bool) *c3types.SystemEvent {
	ch := ev.Channel
	if ch == "" {
		ch = "channel"
	}
	if ev.State == c3types.HealthStateDown {
		msg := fmt.Sprintf("Cannot reach %s since %s (%d consecutive %s). Your phone messages won't arrive until this recovers.",
			ch, ev.Since.Format("15:04"), ev.Consec, strings.TrimSpace(ev.Reason))
		if desktopUnavailable {
			msg += " (desktop notification unavailable — shown here instead)"
		}
		return &c3types.SystemEvent{
			Source:  ev.Channel,
			Level:   "warn",
			Title:   fmt.Sprintf("%s fetch DOWN", ch),
			Message: msg,
		}
	}
	return &c3types.SystemEvent{
		Source:  ev.Channel,
		Level:   "info",
		Title:   fmt.Sprintf("%s fetch RECOVERED", ch),
		Message: fmt.Sprintf("%s is reachable again (was down %s). Phone messages will arrive normally now.", ch, ev.DownFor.Round(time.Second)),
	}
}
```

In the current `NotifyHealth` (the `(b) CLI broadcast` goroutine), update the one existing call site from `systemEventForHealth(ev)` to `systemEventForHealth(ev, false)` so the build stays green (no behavior change — Task 6 rewrites this path).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/broker/ -v`
Expected: PASS (all broker tests, including the unchanged `TestNotifyHealth_FanOut`).

- [ ] **Step 5: Commit**

```bash
git add internal/broker/host.go internal/broker/host_test.go
git commit -m "$(printf 'feat(broker): systemEventForHealth gains desktop-unavailable note\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 6: Rewire `NotifyHealth` (ambient/invasive tiers) + startup write

**Files:**
- Modify: `internal/broker/host.go` (`NotifyHealth` body)
- Modify: `internal/broker/health_notify_test.go` (replace `TestNotifyHealth_FanOut` with the new behavior tests; add helpers)
- Modify: `cmd/c3-broker/main.go` (one-time startup `br.WriteHealthFile()` after channel registration)

**Interfaces:**
- Consumes: `Broker.WriteHealthFile()` (Task 3), `MappingsFile.InvasiveNotifications()` (Task 1), `healthNotifier.Notify(ev) bool` (Task 4), `systemEventForHealth(ev, bool)` (Task 5).

- [ ] **Step 1: Replace the old fan-out test with the new behavior tests** — in `internal/broker/health_notify_test.go`: delete the entire `TestNotifyHealth_FanOut` function, and add the helpers + four tests below. Keep `TestBroadcastSystemEvent_GateBypassIsBrokerOriginated` unchanged. Ensure the import block includes `os`, `path/filepath`, and `strings` (add them):

```go
func brokerWithAgent(t *testing.T) (*Broker, *fakeNotifier, *ipc.Conn) {
	t.Helper()
	b := newTestBroker()
	fn := newFakeNotifier()
	b.desktopNotifier = fn
	agentSide, brokerSide := net.Pipe()
	t.Cleanup(func() { agentSide.Close(); brokerSide.Close() })
	agentConn := ipc.NewConn(agentSide)
	b.Stubs.Register("claude", 4242, "/work", ipc.NewConn(brokerSide))
	return b, fn, agentConn
}

// readBroadcastWithin returns (msg, true) if an InboundMsg arrives within d,
// else (zero, false). Used to assert both presence and ABSENCE of a broadcast.
func readBroadcastWithin(agentConn *ipc.Conn, d time.Duration) (ipc.InboundMsg, bool) {
	type rr struct {
		m   ipc.InboundMsg
		err error
	}
	ch := make(chan rr, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			ch <- rr{err: err}
			return
		}
		var m ipc.InboundMsg
		err = json.Unmarshal(raw, &m)
		ch <- rr{m: m, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return ipc.InboundMsg{}, false
		}
		return r.m, true
	case <-time.After(d):
		return ipc.InboundMsg{}, false
	}
}

func assertHealthFileState(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read health file: %v", err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("health file invalid JSON: %v (%s)", err, data)
	}
	if got["telegram"].State != want {
		t.Errorf("health file telegram.state = %q, want %q", got["telegram"].State, want)
	}
}

func TestNotifyHealth_DesktopDelivered_NoCLIBroadcast(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = true
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked")
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired even though desktop delivered")
	}
	if b.lastHealthSnapshot()["telegram"].State != c3types.HealthStateDown {
		t.Error("status cache not set")
	}
	assertHealthFileState(t, hf, "down")
}

func TestNotifyHealth_DesktopUnavailable_CLIFallbackWithNote(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = false
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked")
	}
	msg, got := readBroadcastWithin(agentConn, 2*time.Second)
	if !got {
		t.Fatal("CLI fallback did not fire when desktop unavailable")
	}
	sys := msg.Inbound.Event.System
	if sys == nil || !strings.Contains(sys.Message, "desktop notification unavailable") {
		t.Errorf("fallback message missing note: %+v", sys)
	}
}

func TestNotifyHealth_Recovery_NoCLIBroadcast_FileSaysUp(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = false // even "unavailable" desktop must not cause a recovery injection
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateUp, DownFor: 3 * time.Minute})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked on recovery")
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired on recovery (must never)")
	}
	assertHealthFileState(t, hf, "up")
}

func TestNotifyHealth_InvasiveOff_NeitherButAmbientWritten(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	off := false
	b.SetMappings(&mappings.MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]mappings.ChannelConfig{},
		Mappings:      map[string]mappings.Mapping{},
		Notifications: &mappings.NotificationsConfig{Invasive: &off},
	})
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3})
	select {
	case <-fn.ch:
		t.Fatal("desktop notifier invoked despite invasive:false")
	case <-time.After(300 * time.Millisecond):
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired despite invasive:false")
	}
	if b.lastHealthSnapshot()["telegram"].State != c3types.HealthStateDown {
		t.Error("status cache not set under invasive:false")
	}
	assertHealthFileState(t, hf, "down")
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/broker/ -run 'TestNotifyHealth_' -v`
Expected: FAIL — the old `NotifyHealth` always broadcasts and never writes `health.json`, so `DesktopDelivered_NoCLIBroadcast`, `Recovery_*`, `InvasiveOff_*` fail (and the file-state asserts fail: no file).

- [ ] **Step 3: Rewrite `NotifyHealth`** — in `internal/broker/host.go`, replace the whole `NotifyHealth` function with:

```go
func (h *BrokerHost) NotifyHealth(ev c3types.HealthEvent) {
	// --- Ambient tier: always on, synchronous, never gated. ---
	// (c) status cache for `c3-broker status`.
	h.broker.setLastHealth(ev)

	// (d) broker log — one loud edge line.
	if ev.State == c3types.HealthStateDown {
		log.Printf("HEALTH chan=%s state=DOWN since=%s consec=%d reason=%q — inbound offline; desktop primary, CLI fallback, status line",
			ev.Channel, ev.Since.Format("15:04:05"), ev.Consec, ev.Reason)
	} else {
		log.Printf("HEALTH chan=%s state=UP (recovered, was down %s) — inbound restored",
			ev.Channel, ev.DownFor.Round(time.Second))
	}

	// (e) status file the Claude Code status line reads.
	h.broker.WriteHealthFile()

	// --- Invasive tier: desktop popup primary, CLI broadcast fallback. ---
	// Gated by notifications.invasive (default true). Read the toggle ONCE
	// here (a single atomic snapshot load) — never inside the goroutine, so a
	// concurrent SIGHUP SetMappings can't tear the read.
	if !h.broker.Mappings().InvasiveNotifications() {
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("health-notify: invasive sink panic recovered: %v", r)
			}
		}()
		// Desktop popup is the primary surface.
		delivered := false
		if h.broker.desktopNotifier != nil {
			delivered = h.broker.desktopNotifier.Notify(ev)
		}
		// CLI turn-injection is the FALLBACK: only when the popup did not
		// deliver, and only on a DOWN edge. Recovery never injects into the
		// CLI — the status line clearing is the closure.
		if !delivered && ev.State == c3types.HealthStateDown {
			h.broker.broadcastSystemEvent(systemEventForHealth(ev, true))
		}
	}()
}
```

- [ ] **Step 4: Add the startup write** — in `cmd/c3-broker/main.go`, immediately after the telegram `RegisterChannel` block (the `if cc, ok := mf.Channels["telegram"]; ok && cc.BotToken != "" { ... } else { ... }` block) and before the DM-pairing check, add:

```go
	// One-time startup write of the connectivity status file the Claude Code
	// status line reads. At boot lastHealth is empty ⇒ writes "{}", which
	// clears any stale file left by a prior crash without falsely asserting
	// up/down. Ambient-only (never via NotifyHealth — no spurious popup).
	br.WriteHealthFile()
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go build ./... && go test ./internal/broker/ -v`
Expected: PASS (all four new `TestNotifyHealth_*`, plus the unchanged gate-bypass test). `go build ./...` confirms `main.go` compiles.

- [ ] **Step 6: Run the full suite + race detector on the broker**

Run: `go test ./... && go test -race ./internal/broker/ ./internal/mappings/`
Expected: PASS, no race warnings.

- [ ] **Step 7: Commit**

```bash
git add internal/broker/host.go internal/broker/health_notify_test.go cmd/c3-broker/main.go
git commit -m "$(printf 'feat(broker): desktop-primary health alerts with CLI fallback + ambient status file\n\nNotifyHealth now splits ambient (cache/log/health.json, always on) from\ninvasive (desktop popup primary; CLI broadcast only when the popup did not\ndeliver, down-edge only) gated by notifications.invasive. Startup writes an\nempty health.json to clear stale state.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 7: Status-line script + settings.json (personal dotfiles, OUTSIDE the repo)

**Files:**
- Modify: `~/.claude/statusline-command.sh` (append a fail-safe connectivity segment)
- Modify: `~/.claude/settings.json` (add `refreshInterval: 5` inside `statusLine`)
- Backups: `~/.claude/statusline-command.sh.bak` and `~/.claude/settings.json.bak`

**Interfaces:**
- Consumes: `health.json` at `${XDG_STATE_HOME:-$HOME/.local/state}/c3/health.json` (Tasks 2/3/6). No repo files; no Go.

- [ ] **Step 1: Back up both files**

```bash
cp ~/.claude/statusline-command.sh ~/.claude/statusline-command.sh.bak
cp ~/.claude/settings.json ~/.claude/settings.json.bak
```

- [ ] **Step 2: Insert the connectivity segment** — in `~/.claude/statusline-command.sh`, the last two statements are:

```bash
[ -n "$ctx" ] && printf " · ctx:%.0f%%" "$ctx"
printf "\033[0m"
```

Insert the segment BETWEEN them so the file ends:

```bash
[ -n "$ctx" ] && printf " · ctx:%.0f%%" "$ctx"

# C3 telegram connectivity (ambient, fail-safe): red segment only while DOWN.
# Missing file / jq error / non-down state => prints nothing. jq is already a
# hard dependency of this script; the script has no `set -e`, so this is safe.
__c3_health="${XDG_STATE_HOME:-$HOME/.local/state}/c3/health.json"
if [ -f "$__c3_health" ]; then
  IFS=$'\t' read -r __c3_state __c3_since < <(jq -r '[.telegram.state // "", .telegram.since_hhmm // ""] | @tsv' "$__c3_health" 2>/dev/null)
  if [ "$__c3_state" = "down" ]; then
    printf " · \033[31m⚠ TG offline%s\033[0m" "${__c3_since:+ $__c3_since}"
  fi
fi

printf "\033[0m"
```

- [ ] **Step 3: Add `refreshInterval` inside `statusLine`**

```bash
tmp=$(mktemp ~/.claude/settings.XXXXXX.json) && jq '.statusLine.refreshInterval = 5' ~/.claude/settings.json > "$tmp" && mv "$tmp" ~/.claude/settings.json
```

- [ ] **Step 4: Verify the script behaves (down / up / missing)**

```bash
mkdir -p /tmp/c3test/c3
# DOWN -> expect the red "TG offline 14:38" segment
echo '{"telegram":{"state":"down","since_hhmm":"14:38"}}' > /tmp/c3test/c3/health.json
echo '{"workspace":{"current_dir":"/tmp"},"model":{"display_name":"X"}}' | XDG_STATE_HOME=/tmp/c3test bash ~/.claude/statusline-command.sh; echo
# UP -> expect NO "TG offline"
echo '{"telegram":{"state":"up"}}' > /tmp/c3test/c3/health.json
echo '{"workspace":{"current_dir":"/tmp"}}' | XDG_STATE_HOME=/tmp/c3test bash ~/.claude/statusline-command.sh; echo
# MISSING -> expect NO "TG offline"
rm /tmp/c3test/c3/health.json
echo '{"workspace":{"current_dir":"/tmp"}}' | XDG_STATE_HOME=/tmp/c3test bash ~/.claude/statusline-command.sh; echo
rm -rf /tmp/c3test
```
Expected: line 1 contains `⚠ TG offline 14:38` (in red); lines 2 and 3 contain no `TG offline`. Also confirm `jq '.statusLine' ~/.claude/settings.json` shows `refreshInterval: 5` and the original `type`/`command`.

- [ ] **Step 5: No commit** — these files are outside the c3 repo. Record in the morning summary that they were changed (with backups at `*.bak`).

---

## Self-Review

**1. Spec coverage** — each spec component maps to a task:
- Component 1 (health.json writer, path, atomic, startup empty-snapshot) → Tasks 2, 3, 6 (startup).
- Component 2 (Notify returns delivered) → Task 4.
- Component 3 (NotifyHealth gating, read-once, recover) → Task 6.
- Component 4 (systemEventForHealth desktop-unavailable note) → Task 5.
- Component 5 (notifications.invasive *bool, Clone, helper, no Validate change) → Task 1.
- Component 6 (status-line segment + refreshInterval nesting) → Task 7.
- Testing list (delivered⇒no broadcast; undelivered⇒fallback+note; recovery⇒no broadcast+file up; invasive off⇒ambient only; default matrix; clone; path resolution; concurrent edges) → Tasks 1, 2, 3, 6. SIGHUP reload flip is exercised implicitly via `SetMappings` in Task 6's invasive-off test (covers the same atomic-pointer swap path); a dedicated SIGHUP integration test is omitted as the reload path is the existing `SetMappings` already covered elsewhere.

**2. Placeholder scan** — no "TBD"/"handle errors"/"similar to" placeholders; every code step shows complete code. ✓

**3. Type consistency** — `WriteHealthFile()` (exported, used in Task 3 test, Task 6 NotifyHealth, Task 6 main.go) consistent; `healthFileEntry` fields (`State/SinceUnix/SinceHHMM/Reason/Consec`) consistent between writer (Task 3) and test asserts (Tasks 3, 6); `healthNotifier.Notify(...) bool` consistent between interface/impl (Task 4), `fakeNotifier` (Task 4), and call site (Task 6); `systemEventForHealth(ev, bool)` consistent between def (Task 5) and callers (Task 5 `false`, Task 6 `true`); `InvasiveNotifications()` consistent between def (Task 1) and call site (Task 6). ✓
