# C3 v3 Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the Phase 1 + Phase 2 slice of the C3 v3 rebuild — a Go module skeleton with the mappings registry implemented, fully tested, plus the migration tool that pulls the bot token + chat ids out of the existing Python MVP into the new `~/.config/c3/mappings.json` location. After this plan, no broker, no channels, no adapters yet — but the data layer is solid and the next plan (broker + IPC) builds directly on top.

**Architecture:** Plain Go (stdlib + `gotgbot/v2` only when channel work starts in a later plan). The mappings package is a self-contained library: `Read`, `Write` (atomic), and a small set of lookup functions over the schema defined in spec §4.3. No global state — callers pass a `*MappingsFile` around. Migration tool is a separate `cmd/migrate-legacy` that reads the legacy Python MVP's `.env` + `config.json` and writes a fresh `mappings.json` skeleton.

**Tech Stack:** Go ≥1.22, stdlib only (`encoding/json`, `os`, `path/filepath`, `sync`, `testing`). No third-party deps in this plan — they get added when channels and IPC arrive.

**Spec reference:** `docs/specs/2026-05-08-c3-rearch-design.md` — sections §1, §3, §4.3, §9.

---

## Phase 1: Repo skeleton

### Task 1.1: Initialize Go module

**Files:**
- Create: `go.mod`
- Create: `.gitignore` (extend if exists)

- [ ] **Step 1: Initialize Go module**

Run:
```bash
cd /home/karthi/arogara/c3 && go mod init github.com/karthikeyan5/c3
```

Expected output: `go: creating new go.mod: module github.com/karthikeyan5/c3`

- [ ] **Step 2: Verify go.mod**

Read `go.mod`. Should contain:
```
module github.com/karthikeyan5/c3

go 1.22
```

If the Go version line is missing or older, edit it to `go 1.22`.

- [ ] **Step 3: Update .gitignore**

Read existing `.gitignore`. Append (if not already present):
```
# Go
*.exe
*.dll
*.so
*.dylib
*.test
*.out
/bin/
/coverage.out
```

- [ ] **Step 4: Commit**

```bash
git add go.mod .gitignore
git commit -m "c3-v3: init Go module"
```

---

### Task 1.2: Create directory layout

**Files:**
- Create: `cmd/c3-broker/main.go` (placeholder)
- Create: `cmd/c3-claude-adapter/main.go` (placeholder)
- Create: `cmd/c3-codex-adapter/main.go` (placeholder)
- Create: `cmd/migrate-legacy/main.go` (placeholder)
- Create: `internal/mappings/mappings.go` (placeholder)

- [ ] **Step 1: Create the broker placeholder main**

Create `cmd/c3-broker/main.go` with:

```go
package main

import "fmt"

func main() {
	fmt.Println("c3-broker: not implemented yet")
}
```

- [ ] **Step 2: Create the Claude adapter placeholder main**

Create `cmd/c3-claude-adapter/main.go` with:

```go
package main

import "fmt"

func main() {
	fmt.Println("c3-claude-adapter: not implemented yet")
}
```

- [ ] **Step 3: Create the Codex adapter placeholder main**

Create `cmd/c3-codex-adapter/main.go` with:

```go
package main

import "fmt"

func main() {
	fmt.Println("c3-codex-adapter: not implemented yet")
}
```

- [ ] **Step 4: Create the migrate-legacy placeholder main**

Create `cmd/migrate-legacy/main.go` with:

```go
package main

import "fmt"

func main() {
	fmt.Println("migrate-legacy: not implemented yet")
}
```

- [ ] **Step 5: Create the mappings package placeholder**

Create `internal/mappings/mappings.go` with:

```go
// Package mappings reads and writes ~/.config/c3/mappings.json.
//
// The schema is documented in docs/specs/2026-05-08-c3-rearch-design.md §4.3.
package mappings
```

- [ ] **Step 6: Verify build**

Run:
```bash
cd /home/karthi/arogara/c3 && go build ./...
```

Expected: no output (success). All four placeholder mains compile.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/
git commit -m "c3-v3: scaffold cmd/ + internal/mappings package layout"
```

---

### Task 1.3: Add Makefile

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Write the Makefile**

Create `Makefile` with:

```makefile
.PHONY: build test clean install

BIN_DIR := bin

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/c3-broker ./cmd/c3-broker
	go build -o $(BIN_DIR)/c3-claude-adapter ./cmd/c3-claude-adapter
	go build -o $(BIN_DIR)/c3-codex-adapter ./cmd/c3-codex-adapter
	go build -o $(BIN_DIR)/migrate-legacy ./cmd/migrate-legacy

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

install:
	go install ./cmd/...
```

- [ ] **Step 2: Verify make build works**

Run:
```bash
cd /home/karthi/arogara/c3 && make build
```

Expected: four binaries in `bin/`. Verify with `ls bin/`.

- [ ] **Step 3: Verify make test works (no tests yet, exits 0)**

Run:
```bash
cd /home/karthi/arogara/c3 && make test
```

Expected: `ok` lines for any package with tests; `?  no test files` for packages without; exit code 0.

- [ ] **Step 4: Commit**

```bash
git add Makefile
git commit -m "c3-v3: add Makefile with build/test/clean/install targets"
```

---

## Phase 2: Mappings registry

### Task 2.1: Define schema types

**Files:**
- Modify: `internal/mappings/mappings.go`
- Create: `internal/mappings/types.go`

- [ ] **Step 1: Write the types file**

Create `internal/mappings/types.go` with:

```go
package mappings

import "time"

// MappingsFile is the root structure of ~/.config/c3/mappings.json.
type MappingsFile struct {
	SchemaVersion int                       `json:"schema_version"`
	Channels      map[string]ChannelConfig  `json:"channels"`
	Mappings      map[string]Mapping        `json:"mappings"`
	Plugins       map[string]map[string]any `json:"plugins,omitempty"`
}

// ChannelConfig holds per-channel state. v1 only uses telegram.
type ChannelConfig struct {
	BotToken      string                 `json:"bot_token,omitempty"`
	DefaultGroup  string                 `json:"default_group,omitempty"`
	Groups        map[string]GroupConfig `json:"groups,omitempty"`
	DMChatID      int64                  `json:"dm_chat_id,omitempty"`
	MasterUserID  int64                  `json:"master_user_id,omitempty"`
	Topics        []Topic                `json:"topics,omitempty"`
	DebounceMS    int                    `json:"debounce_ms,omitempty"`
}

// GroupConfig identifies a Telegram supergroup the bot can create topics in.
type GroupConfig struct {
	ChatID int64  `json:"chat_id"`
	Title  string `json:"title,omitempty"`
}

// Topic is one entry in the per-channel topic registry.
type Topic struct {
	ChatID  int64  `json:"chat_id"`
	TopicID int64  `json:"topic_id"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
}

// Mapping is one absolute-cwd-keyed entry.
type Mapping struct {
	Channel        string    `json:"channel"`
	ChatID         int64     `json:"chat_id"`
	TopicID        int64     `json:"topic_id"`
	Name           string    `json:"name"`
	Group          string    `json:"group,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastAttachedAt time.Time `json:"last_attached_at,omitempty"`
}
```

- [ ] **Step 2: Verify the types file compiles**

Run:
```bash
cd /home/karthi/arogara/c3 && go build ./internal/mappings/...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/mappings/types.go
git commit -m "c3-v3: define mappings schema types"
```

---

### Task 2.2: Read function with TDD

**Files:**
- Create: `internal/mappings/io_test.go`
- Create: `internal/mappings/io.go`

- [ ] **Step 1: Write the failing test**

Create `internal/mappings/io_test.go` with:

```go
package mappings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	contents := `{
		"schema_version": 1,
		"channels": {
			"telegram": {
				"bot_token": "abc",
				"default_group": "main",
				"groups": {"main": {"chat_id": -100, "title": "G"}},
				"dm_chat_id": 42,
				"topics": [{"chat_id": -100, "topic_id": 281, "name": "c3", "group": "main"}]
			}
		},
		"mappings": {
			"/home/u/proj": {
				"channel": "telegram",
				"chat_id": -100,
				"topic_id": 281,
				"name": "c3",
				"group": "main",
				"created_at": "2026-04-21T22:00:00Z"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	mf, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if mf.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", mf.SchemaVersion)
	}
	tg, ok := mf.Channels["telegram"]
	if !ok {
		t.Fatal("missing telegram channel")
	}
	if tg.BotToken != "abc" {
		t.Errorf("BotToken = %q, want %q", tg.BotToken, "abc")
	}
	if len(tg.Topics) != 1 || tg.Topics[0].TopicID != 281 {
		t.Errorf("topics = %+v, want one entry with TopicID=281", tg.Topics)
	}
	m, ok := mf.Mappings["/home/u/proj"]
	if !ok {
		t.Fatal("missing /home/u/proj mapping")
	}
	if m.TopicID != 281 {
		t.Errorf("Mappings[/home/u/proj].TopicID = %d, want 281", m.TopicID)
	}
}

func TestRead_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")

	_, err := Read(path)
	if !os.IsNotExist(err) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestRead_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v
```

Expected: compile error or `Read undefined`. The test must fail because `Read` does not exist yet.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/mappings/io.go` with:

```go
package mappings

import (
	"encoding/json"
	"fmt"
	"os"
)

// Read parses the mappings.json file at path. Returns os.IsNotExist-friendly
// error if the file is missing.
func Read(path string) (*MappingsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf MappingsFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &mf, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestRead
```

Expected: `--- PASS: TestRead_ValidFile`, `--- PASS: TestRead_FileNotFound`, `--- PASS: TestRead_MalformedJSON`. All three pass.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/io.go internal/mappings/io_test.go
git commit -m "c3-v3: mappings.Read parses ~/.config/c3/mappings.json"
```

---

### Task 2.3: Write function (atomic)

**Files:**
- Modify: `internal/mappings/io.go`
- Modify: `internal/mappings/io_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `internal/mappings/io_test.go`:

```go
func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]ChannelConfig{
			"telegram": {BotToken: "tok", DefaultGroup: "main"},
		},
		Mappings: map[string]Mapping{},
	}

	if err := Write(path, mf); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read after Write failed: %v", err)
	}
	if got.Channels["telegram"].BotToken != "tok" {
		t.Errorf("round-trip lost bot token; got %+v", got)
	}
}

func TestWrite_AtomicCreatesNoTempfileOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Make the directory read-only so the temp file write fails.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)

	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{SchemaVersion: 1}

	if err := Write(path, mf); err == nil {
		t.Error("expected Write to fail on read-only dir, got nil")
	}

	// Verify no leftover temp file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("leftover file %q in dir", e.Name())
		}
	}
}

func TestWrite_FileMode600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{SchemaVersion: 1}

	if err := Write(path, mf); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestWrite
```

Expected: `Write undefined` compile error.

- [ ] **Step 3: Implement Write (atomic, mode 600)**

Append to `internal/mappings/io.go`:

```go
// Write atomically rewrites the mappings file at path. The file is created
// (or replaced) at mode 0600 because it contains the bot token. Atomicity is
// achieved by writing to a sibling tempfile and then renaming.
func Write(path string, mf *MappingsFile) error {
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mappings: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mappings.*.tmp")
	if err != nil {
		return fmt.Errorf("create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails.
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}
```

Add the import for `path/filepath` to the io.go imports if not present:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v
```

Expected: all `TestRead_*` and `TestWrite_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/io.go internal/mappings/io_test.go
git commit -m "c3-v3: mappings.Write atomic-rewrites at mode 0600"
```

---

### Task 2.4: Lookup by cwd

**Files:**
- Create: `internal/mappings/lookup.go`
- Create: `internal/mappings/lookup_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mappings/lookup_test.go` with:

```go
package mappings

import "testing"

func newTestFile() *MappingsFile {
	return &MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]ChannelConfig{
			"telegram": {
				DefaultGroup: "main",
				Groups: map[string]GroupConfig{
					"main": {ChatID: -100, Title: "Main"},
					"work": {ChatID: -200, Title: "Work"},
				},
				DMChatID: 42,
				Topics: []Topic{
					{ChatID: -100, TopicID: 281, Name: "c3", Group: "main"},
					{ChatID: -100, TopicID: 207, Name: "sthapati", Group: "main"},
					{ChatID: -200, TopicID: 412, Name: "feature-x", Group: "work"},
				},
			},
		},
		Mappings: map[string]Mapping{
			"/home/u/c3": {
				Channel: "telegram", ChatID: -100, TopicID: 281,
				Name: "c3", Group: "main",
			},
		},
	}
}

func TestLookupByCwd_Found(t *testing.T) {
	mf := newTestFile()
	m, ok := mf.LookupByCwd("/home/u/c3")
	if !ok {
		t.Fatal("expected mapping to be found")
	}
	if m.TopicID != 281 {
		t.Errorf("TopicID = %d, want 281", m.TopicID)
	}
}

func TestLookupByCwd_NotFound(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupByCwd("/home/u/other")
	if ok {
		t.Error("expected mapping to be missing")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupByCwd
```

Expected: compile error `LookupByCwd undefined`.

- [ ] **Step 3: Implement LookupByCwd**

Create `internal/mappings/lookup.go` with:

```go
package mappings

// LookupByCwd returns the mapping for an absolute, resolved cwd. The bool is
// false if no mapping exists.
func (mf *MappingsFile) LookupByCwd(cwd string) (Mapping, bool) {
	if mf == nil || mf.Mappings == nil {
		return Mapping{}, false
	}
	m, ok := mf.Mappings[cwd]
	return m, ok
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupByCwd
```

Expected: both `TestLookupByCwd_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/lookup.go internal/mappings/lookup_test.go
git commit -m "c3-v3: mappings.LookupByCwd"
```

---

### Task 2.5: Lookup by topic name in default group

**Files:**
- Modify: `internal/mappings/lookup.go`
- Modify: `internal/mappings/lookup_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mappings/lookup_test.go`:

```go
func TestLookupTopicInDefaultGroup_Found(t *testing.T) {
	mf := newTestFile()
	tp, ok := mf.LookupTopicInDefaultGroup("telegram", "c3")
	if !ok {
		t.Fatal("expected to find c3 in default group main")
	}
	if tp.TopicID != 281 || tp.Group != "main" {
		t.Errorf("got %+v, want TopicID=281 Group=main", tp)
	}
}

func TestLookupTopicInDefaultGroup_NotInDefaultButInOther(t *testing.T) {
	mf := newTestFile()
	// feature-x is only in work group, not main (default).
	_, ok := mf.LookupTopicInDefaultGroup("telegram", "feature-x")
	if ok {
		t.Error("expected NOT to find feature-x in default group main")
	}
}

func TestLookupTopicInDefaultGroup_UnknownChannel(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicInDefaultGroup("slack", "c3")
	if ok {
		t.Error("expected miss for unknown channel")
	}
}

func TestLookupTopicInDefaultGroup_ChannelHasNoDefault(t *testing.T) {
	mf := &MappingsFile{
		Channels: map[string]ChannelConfig{
			"telegram": {Topics: []Topic{{Name: "c3", Group: "main"}}},
		},
	}
	_, ok := mf.LookupTopicInDefaultGroup("telegram", "c3")
	if ok {
		t.Error("expected miss when default_group is empty")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicInDefaultGroup
```

Expected: compile error `LookupTopicInDefaultGroup undefined`.

- [ ] **Step 3: Implement LookupTopicInDefaultGroup**

Append to `internal/mappings/lookup.go`:

```go
// LookupTopicInDefaultGroup searches the channel's topic registry for a topic
// whose Name matches and whose Group equals the channel's DefaultGroup. The
// bool is false if the channel doesn't exist, has no default group, or the
// name isn't present in that group.
func (mf *MappingsFile) LookupTopicInDefaultGroup(channel, name string) (Topic, bool) {
	if mf == nil {
		return Topic{}, false
	}
	cc, ok := mf.Channels[channel]
	if !ok || cc.DefaultGroup == "" {
		return Topic{}, false
	}
	for _, tp := range cc.Topics {
		if tp.Name == name && tp.Group == cc.DefaultGroup {
			return tp, true
		}
	}
	return Topic{}, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicInDefaultGroup
```

Expected: all four `TestLookupTopicInDefaultGroup_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/lookup.go internal/mappings/lookup_test.go
git commit -m "c3-v3: mappings.LookupTopicInDefaultGroup"
```

---

### Task 2.6: Cross-group topic search

**Files:**
- Modify: `internal/mappings/lookup.go`
- Modify: `internal/mappings/lookup_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mappings/lookup_test.go`:

```go
func TestLookupTopicAcrossGroups_FoundInOne(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("telegram", "feature-x")
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Group != "work" {
		t.Errorf("hit group = %q, want work", hits[0].Group)
	}
}

func TestLookupTopicAcrossGroups_FoundInMultiple(t *testing.T) {
	mf := newTestFile()
	// Add a duplicate-named entry in work group.
	cc := mf.Channels["telegram"]
	cc.Topics = append(cc.Topics, Topic{ChatID: -200, TopicID: 999, Name: "c3", Group: "work"})
	mf.Channels["telegram"] = cc

	hits := mf.LookupTopicAcrossGroups("telegram", "c3")
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
}

func TestLookupTopicAcrossGroups_None(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("telegram", "nonexistent")
	if len(hits) != 0 {
		t.Errorf("got %d hits, want 0", len(hits))
	}
}

func TestLookupTopicAcrossGroups_UnknownChannel(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("slack", "anything")
	if len(hits) != 0 {
		t.Errorf("got %d hits for unknown channel, want 0", len(hits))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicAcrossGroups
```

Expected: compile error `LookupTopicAcrossGroups undefined`.

- [ ] **Step 3: Implement LookupTopicAcrossGroups**

Append to `internal/mappings/lookup.go`:

```go
// LookupTopicAcrossGroups returns all topics in the channel whose Name
// matches, regardless of Group. Order is the order of the underlying slice.
// Returns an empty slice if the channel is unknown or no match is found.
func (mf *MappingsFile) LookupTopicAcrossGroups(channel, name string) []Topic {
	if mf == nil {
		return nil
	}
	cc, ok := mf.Channels[channel]
	if !ok {
		return nil
	}
	var hits []Topic
	for _, tp := range cc.Topics {
		if tp.Name == name {
			hits = append(hits, tp)
		}
	}
	return hits
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicAcrossGroups
```

Expected: all four `TestLookupTopicAcrossGroups_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/lookup.go internal/mappings/lookup_test.go
git commit -m "c3-v3: mappings.LookupTopicAcrossGroups"
```

---

### Task 2.7: Lookup topic by id

**Files:**
- Modify: `internal/mappings/lookup.go`
- Modify: `internal/mappings/lookup_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/mappings/lookup_test.go`:

```go
func TestLookupTopicByID_Found(t *testing.T) {
	mf := newTestFile()
	tp, ok := mf.LookupTopicByID("telegram", -100, 281)
	if !ok {
		t.Fatal("expected to find topic 281 in chat -100")
	}
	if tp.Name != "c3" {
		t.Errorf("Name = %q, want c3", tp.Name)
	}
}

func TestLookupTopicByID_NotFound(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicByID("telegram", -100, 99999)
	if ok {
		t.Error("expected miss for nonexistent topic id")
	}
}

func TestLookupTopicByID_WrongChat(t *testing.T) {
	mf := newTestFile()
	// topic 281 is in chat -100, not -200
	_, ok := mf.LookupTopicByID("telegram", -200, 281)
	if ok {
		t.Error("expected miss when topic_id matches but chat_id doesn't")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicByID
```

Expected: compile error `LookupTopicByID undefined`.

- [ ] **Step 3: Implement LookupTopicByID**

Append to `internal/mappings/lookup.go`:

```go
// LookupTopicByID returns the topic registered for (chat_id, topic_id) in the
// given channel.
func (mf *MappingsFile) LookupTopicByID(channel string, chatID, topicID int64) (Topic, bool) {
	if mf == nil {
		return Topic{}, false
	}
	cc, ok := mf.Channels[channel]
	if !ok {
		return Topic{}, false
	}
	for _, tp := range cc.Topics {
		if tp.ChatID == chatID && tp.TopicID == topicID {
			return tp, true
		}
	}
	return Topic{}, false
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestLookupTopicByID
```

Expected: all three `TestLookupTopicByID_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/lookup.go internal/mappings/lookup_test.go
git commit -m "c3-v3: mappings.LookupTopicByID"
```

---

### Task 2.8: Insert/update operations

**Files:**
- Create: `internal/mappings/mutate.go`
- Create: `internal/mappings/mutate_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mappings/mutate_test.go` with:

```go
package mappings

import (
	"testing"
	"time"
)

func TestUpsertTopic_New(t *testing.T) {
	mf := newTestFile()
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 917, Name: "widget-foo", Group: "main"})

	tp, ok := mf.LookupTopicByID("telegram", -100, 917)
	if !ok {
		t.Fatal("expected new topic to be present after Upsert")
	}
	if tp.Name != "widget-foo" {
		t.Errorf("Name = %q, want widget-foo", tp.Name)
	}
}

func TestUpsertTopic_Update(t *testing.T) {
	mf := newTestFile()
	// 281 already exists with name "c3", group "main". Rename to "C3".
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 281, Name: "C3", Group: "main"})

	tp, _ := mf.LookupTopicByID("telegram", -100, 281)
	if tp.Name != "C3" {
		t.Errorf("Name = %q, want C3", tp.Name)
	}
	// Should not have duplicated.
	hits := mf.LookupTopicAcrossGroups("telegram", "C3")
	if len(hits) != 1 {
		t.Errorf("got %d entries with name C3, want 1", len(hits))
	}
}

func TestUpsertTopic_NewChannel(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 1, Name: "x", Group: "g"})

	if mf.Channels == nil {
		t.Fatal("Channels map should have been created")
	}
	if len(mf.Channels["telegram"].Topics) != 1 {
		t.Errorf("expected 1 topic, got %d", len(mf.Channels["telegram"].Topics))
	}
}

func TestUpsertMapping_New(t *testing.T) {
	mf := newTestFile()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/widget-foo", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 917,
		Name: "widget-foo", Group: "main",
		CreatedAt: now, LastAttachedAt: now,
	})

	m, ok := mf.LookupByCwd("/home/u/widget-foo")
	if !ok {
		t.Fatal("expected mapping to be present")
	}
	if m.TopicID != 917 {
		t.Errorf("TopicID = %d, want 917", m.TopicID)
	}
}

func TestUpsertMapping_UpdatePreservesCreatedAt(t *testing.T) {
	mf := newTestFile()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/proj", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
		CreatedAt: created,
	})

	updated := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/proj", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
		LastAttachedAt: updated,
		// CreatedAt left zero — Upsert should keep the original.
	})

	m, _ := mf.LookupByCwd("/home/u/proj")
	if !m.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want preserved %v", m.CreatedAt, created)
	}
	if !m.LastAttachedAt.Equal(updated) {
		t.Errorf("LastAttachedAt = %v, want %v", m.LastAttachedAt, updated)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run "TestUpsertTopic|TestUpsertMapping"
```

Expected: compile error — `UpsertTopic` / `UpsertMapping` undefined.

- [ ] **Step 3: Implement UpsertTopic and UpsertMapping**

Create `internal/mappings/mutate.go` with:

```go
package mappings

// UpsertTopic inserts a new topic or updates an existing one (matched by
// channel + chat_id + topic_id). Creates the channel entry if missing.
func (mf *MappingsFile) UpsertTopic(channel string, t Topic) {
	if mf.Channels == nil {
		mf.Channels = map[string]ChannelConfig{}
	}
	cc := mf.Channels[channel]
	for i, existing := range cc.Topics {
		if existing.ChatID == t.ChatID && existing.TopicID == t.TopicID {
			cc.Topics[i] = t
			mf.Channels[channel] = cc
			return
		}
	}
	cc.Topics = append(cc.Topics, t)
	mf.Channels[channel] = cc
}

// UpsertMapping inserts a new cwd → mapping or updates an existing one. When
// updating, a zero CreatedAt on the new value is replaced with the existing
// entry's CreatedAt so update-flows can leave that field unset.
func (mf *MappingsFile) UpsertMapping(cwd string, m Mapping) {
	if mf.Mappings == nil {
		mf.Mappings = map[string]Mapping{}
	}
	if existing, ok := mf.Mappings[cwd]; ok {
		if m.CreatedAt.IsZero() {
			m.CreatedAt = existing.CreatedAt
		}
	}
	mf.Mappings[cwd] = m
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run "TestUpsertTopic|TestUpsertMapping"
```

Expected: all five `TestUpsert*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/mutate.go internal/mappings/mutate_test.go
git commit -m "c3-v3: mappings UpsertTopic and UpsertMapping"
```

---

### Task 2.9: Validate function

**Files:**
- Create: `internal/mappings/validate.go`
- Create: `internal/mappings/validate_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mappings/validate_test.go` with:

```go
package mappings

import (
	"strings"
	"testing"
)

func TestValidate_Ok(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	if err := mf.Validate(); err != nil {
		t.Errorf("Validate failed on valid file: %v", err)
	}
}

func TestValidate_BadSchemaVersion(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 99
	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("expected schema_version error, got %v", err)
	}
}

func TestValidate_DefaultGroupNotInGroups(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	cc := mf.Channels["telegram"]
	cc.DefaultGroup = "ghost"
	mf.Channels["telegram"] = cc

	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "default_group") {
		t.Errorf("expected default_group error, got %v", err)
	}
}

func TestValidate_TopicGroupNotInGroups(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	cc := mf.Channels["telegram"]
	cc.Topics = append(cc.Topics, Topic{ChatID: -300, TopicID: 5, Name: "x", Group: "phantom"})
	mf.Channels["telegram"] = cc

	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "phantom") {
		t.Errorf("expected phantom-group error, got %v", err)
	}
}

func TestValidate_MappingChannelMissing(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	mf.Mappings["/home/u/orphan"] = Mapping{
		Channel: "ghost-channel", ChatID: -100, TopicID: 1,
	}
	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "ghost-channel") {
		t.Errorf("expected unknown-channel error, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestValidate
```

Expected: compile error `Validate undefined`.

- [ ] **Step 3: Implement Validate**

Create `internal/mappings/validate.go` with:

```go
package mappings

import "fmt"

// Validate returns nil if the MappingsFile is internally consistent, or a
// concrete error describing the first inconsistency found.
//
// Checks:
// - schema_version is recognized.
// - For each channel: default_group, if set, exists in groups.
// - For each topic: its group, if set, exists in groups.
// - For each mapping: its channel exists.
//
// This does NOT validate against Telegram (e.g. that chat_ids are real groups
// the bot has access to). Network validation lives in the channel module.
func (mf *MappingsFile) Validate() error {
	if mf == nil {
		return fmt.Errorf("mappings: nil file")
	}
	if mf.SchemaVersion != 1 {
		return fmt.Errorf("mappings: unsupported schema_version %d (want 1)", mf.SchemaVersion)
	}
	for chanName, cc := range mf.Channels {
		if cc.DefaultGroup != "" {
			if _, ok := cc.Groups[cc.DefaultGroup]; !ok {
				return fmt.Errorf("mappings: channel %q default_group %q not in groups", chanName, cc.DefaultGroup)
			}
		}
		for _, tp := range cc.Topics {
			if tp.Group == "" {
				continue
			}
			if _, ok := cc.Groups[tp.Group]; !ok {
				return fmt.Errorf("mappings: channel %q topic %q references unknown group %q", chanName, tp.Name, tp.Group)
			}
		}
	}
	for cwd, m := range mf.Mappings {
		if _, ok := mf.Channels[m.Channel]; !ok {
			return fmt.Errorf("mappings: cwd %q maps to unknown channel %q", cwd, m.Channel)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestValidate
```

Expected: all five `TestValidate_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/validate.go internal/mappings/validate_test.go
git commit -m "c3-v3: mappings.Validate checks internal consistency"
```

---

### Task 2.10: Default-config-path helper

**Files:**
- Create: `internal/mappings/path.go`
- Create: `internal/mappings/path_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/mappings/path_test.go` with:

```go
package mappings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath_UsesXDGConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", "/tmp/should-not-be-used")

	got := DefaultPath()
	want := filepath.Join(dir, "c3", "mappings.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultPath_FallsBackToHome(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv("XDG_CONFIG_HOME")
	t.Setenv("HOME", dir)

	got := DefaultPath()
	want := filepath.Join(dir, ".config", "c3", "mappings.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestDefaultPath
```

Expected: compile error `DefaultPath undefined`.

- [ ] **Step 3: Implement DefaultPath**

Create `internal/mappings/path.go` with:

```go
package mappings

import (
	"os"
	"path/filepath"
)

// DefaultPath returns the canonical mappings.json location:
//   $XDG_CONFIG_HOME/c3/mappings.json  (if set)
//   $HOME/.config/c3/mappings.json     (otherwise)
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "c3", "mappings.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "c3", "mappings.json")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./internal/mappings/... -v -run TestDefaultPath
```

Expected: both `TestDefaultPath_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mappings/path.go internal/mappings/path_test.go
git commit -m "c3-v3: mappings.DefaultPath resolves XDG location"
```

---

### Task 2.11: Migration tool

**Files:**
- Modify: `cmd/migrate-legacy/main.go`
- Create: `cmd/migrate-legacy/main_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/migrate-legacy/main_test.go` with:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestMigrate_FreshConfig(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "telegram.env")
	cfgFile := filepath.Join(dir, "config.json")
	outFile := filepath.Join(dir, "out.json")

	if err := os.WriteFile(envFile, []byte("TELEGRAM_BOT_TOKEN=tok123\nOTHER=xx\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgFile, []byte(`{"group_chat_id": -100, "dm_chat_id": 42}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := migrate(envFile, cfgFile, outFile); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	var mf mappings.MappingsFile
	if err := json.Unmarshal(data, &mf); err != nil {
		t.Fatal(err)
	}
	if mf.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", mf.SchemaVersion)
	}
	tg := mf.Channels["telegram"]
	if tg.BotToken != "tok123" {
		t.Errorf("BotToken = %q, want tok123", tg.BotToken)
	}
	if tg.DMChatID != 42 {
		t.Errorf("DMChatID = %d, want 42", tg.DMChatID)
	}
	if tg.DefaultGroup != "main" {
		t.Errorf("DefaultGroup = %q, want main", tg.DefaultGroup)
	}
	if tg.Groups["main"].ChatID != -100 {
		t.Errorf("Groups[main].ChatID = %d, want -100", tg.Groups["main"].ChatID)
	}

	// Mode 600.
	info, _ := os.Stat(outFile)
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestMigrate_RefusesIfOutputExists(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "e.env")
	cfgFile := filepath.Join(dir, "c.json")
	outFile := filepath.Join(dir, "out.json")

	os.WriteFile(envFile, []byte("TELEGRAM_BOT_TOKEN=t"), 0600)
	os.WriteFile(cfgFile, []byte(`{"group_chat_id": -1, "dm_chat_id": 1}`), 0600)
	os.WriteFile(outFile, []byte("{}"), 0600)

	err := migrate(envFile, cfgFile, outFile)
	if err == nil {
		t.Error("expected refusal when output already exists, got nil")
	}
}

func TestMigrate_MissingEnv(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "c.json")
	outFile := filepath.Join(dir, "out.json")
	os.WriteFile(cfgFile, []byte(`{"group_chat_id": -1, "dm_chat_id": 1}`), 0600)

	err := migrate(filepath.Join(dir, "missing.env"), cfgFile, outFile)
	if err == nil {
		t.Error("expected error for missing env file, got nil")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./cmd/migrate-legacy/... -v
```

Expected: compile error — `migrate` undefined.

- [ ] **Step 3: Implement the migrate command**

Replace `cmd/migrate-legacy/main.go` with:

```go
// migrate-legacy ports the Python MVP's bot token (.env) and chat ids
// (mvp/config.json) into a fresh ~/.config/c3/mappings.json. Idempotent —
// refuses to overwrite an existing output file.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/karthikeyan5/c3/internal/mappings"
)

func main() {
	envPath := flag.String("env", os.Getenv("HOME")+"/.claude/channels/telegram/.env", "path to legacy .env")
	cfgPath := flag.String("config", "mvp/config.json", "path to legacy mvp/config.json")
	outPath := flag.String("out", mappings.DefaultPath(), "path to write new mappings.json")
	flag.Parse()

	if err := migrate(*envPath, *cfgPath, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "migrate-legacy: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("migrate-legacy: wrote %s (mode 0600). Verify, then you can delete the old mvp/config.json.\n", *outPath)
}

func migrate(envPath, cfgPath, outPath string) error {
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing %s", outPath)
	}

	token, err := readEnvKey(envPath, "TELEGRAM_BOT_TOKEN")
	if err != nil {
		return fmt.Errorf("read env: %w", err)
	}
	if token == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN missing from %s", envPath)
	}

	cfg, err := readLegacyConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("read legacy config: %w", err)
	}

	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {
				BotToken:     token,
				DefaultGroup: "main",
				Groups: map[string]mappings.GroupConfig{
					"main": {ChatID: cfg.GroupChatID, Title: "(migrated)"},
				},
				DMChatID: cfg.DMChatID,
				Topics:   nil,
			},
		},
		Mappings: map[string]mappings.Mapping{},
	}

	if err := os.MkdirAll(parentDir(outPath), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	return mappings.Write(outPath, mf)
}

func readEnvKey(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	prefix := key + "="
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):]), nil
		}
	}
	return "", sc.Err()
}

type legacyConfig struct {
	GroupChatID int64 `json:"group_chat_id"`
	DMChatID    int64 `json:"dm_chat_id"`
}

func readLegacyConfig(path string) (*legacyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg legacyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./cmd/migrate-legacy/... -v
```

Expected: all three `TestMigrate_*` PASS.

- [ ] **Step 5: Verify the binary builds**

Run:
```bash
cd /home/karthi/arogara/c3 && make build
```

Expected: `bin/migrate-legacy` exists.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate-legacy/main.go cmd/migrate-legacy/main_test.go
git commit -m "c3-v3: migrate-legacy ports Python MVP config to mappings.json"
```

---

### Task 2.12: Final integration check

**Files:** none (verification only)

- [ ] **Step 1: Run the full test suite**

Run:
```bash
cd /home/karthi/arogara/c3 && go test ./... -v
```

Expected: every test passes. The output should include `ok` for `internal/mappings` and `cmd/migrate-legacy`, and `?  no test files` for the placeholder cmd packages.

- [ ] **Step 2: Run the build**

Run:
```bash
cd /home/karthi/arogara/c3 && make build
```

Expected: all four binaries built into `bin/`.

- [ ] **Step 3: Smoke test migrate-legacy with the real legacy files (dry run)**

```bash
cd /home/karthi/arogara/c3 && ./bin/migrate-legacy --env=$HOME/.claude/channels/telegram/.env --config=mvp/config.json --out=/tmp/c3-test-mappings.json
```

Expected: `migrate-legacy: wrote /tmp/c3-test-mappings.json (mode 0600). ...`

Inspect output:
```bash
cat /tmp/c3-test-mappings.json
```

Expected: schema_version=1, channels.telegram.bot_token populated, groups.main.chat_id matches, dm_chat_id matches.

Cleanup:
```bash
rm /tmp/c3-test-mappings.json
```

- [ ] **Step 4: Final commit (if anything was tweaked during smoke test)**

If nothing changed, skip. Otherwise:

```bash
git add -A
git commit -m "c3-v3: smoke-test fixes from migrate-legacy live run"
```

---

## Out of scope for this plan

The following come in subsequent plans, in order:

- **Plan 2 (Phase 3 of spec):** Broker core + IPC. Unix socket server, flock singleton, IPC types and ops (`hello`, `attach`, `tool_call`, `inbound`), routing map. No channel yet — just the IPC plumbing and a stubbed tool_call path.
- **Plan 3 (Phase 4):** Telegram channel cleanroom Go implementation using `gotgbot/v2`. Connects into the broker's channel interface.
- **Plan 4 (Phase 5):** Plugin host + STT plugin (Go shim → existing Python whisper).
- **Plan 5 (Phase 6):** Claude Code adapter, `.mcp.json`, `${CLAUDE_PLUGIN_ROOT}` resolution, attach proposal flow surfacing.
- **Plan 6 (Phase 7):** Debounce, dedup, typing indicator, edit_progress.
- **Plan 7 (Phase 8):** **Codex bridge integration — no rewrite.** Move the existing Python POC (`mvp/codex` shim, `codex_supervisor.py`, `codex_stub.py`, related tests) to a top-level `codex/` directory. Update its broker IPC calls to match the new IPC v2 protocol (particularly the `attach` proposal flow vs the old `attach_auto`). Add `codex/install.sh` (idempotent PATH + NVM symlink installer). Author the Codex marketplace plugin manifest pointing at `SETUP.md` + `install.sh`. Operational truth for the bridge stays in `mvp/CODEX_BRIDGE_SPEC.md` — keep that file as the source for behavior, ports, env contract, ops checks. Do NOT rewrite this in Go.
- **Plan 8 (Phase 9):** `/c3-setup`, `/c3-build`, `/c3-status` slash commands (Claude) + Codex `SETUP.md` author.
- **Plan 9 (Phase 10):** Documentation pass, deviation banner retirement, D009 + D010 entries, public release tag.

After this plan, the broker doesn't exist yet — but the data layer is complete, tested, and the migration tool works against the real Python MVP files. The Codex POC files under `mvp/` remain untouched and continue to function on Karthi's machine; their migration to top-level `codex/` is part of Plan 7 (Phase 8), not this plan.

---

## Self-review checklist (run after the plan is written)

- [x] **Spec coverage:** §4.3 (mappings schema) covered by tasks 2.1-2.9. §9 (migration) covered by task 2.11. §1 (Go everywhere, Phase 1 skeleton) covered by Phase 1. §11 (resolved decisions) reflected in lib choice (no third-party libs in foundation), schema shape, default group / multi-group support.
- [x] **No placeholders:** every code step has full code; every command has expected output; no "TBD" or "fill in later".
- [x] **Type consistency:** `MappingsFile.Channels` is `map[string]ChannelConfig`, used the same way across `LookupTopicInDefaultGroup`, `LookupTopicAcrossGroups`, `LookupTopicByID`, `UpsertTopic`, `Validate`. `Topic` fields (`ChatID`, `TopicID`, `Name`, `Group`) match across all callers.
- [x] **TDD discipline:** every implementation task is preceded by a failing test (Step 1) → run-and-fail (Step 2) → implement (Step 3) → run-and-pass (Step 4) → commit (Step 5).
- [x] **Granularity:** each task's steps are 2-5 minutes of work each. No step bundles multiple file creations + edits + tests.
- [x] **Frequent commits:** every task ends with a commit. The plan is 12 tasks → 12 commits.
