# Claude-Shim Safety Fix — EvalSymlinks-and-Remember

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent `c3-broker install-claude-shim` (run unconditionally by `c3-broker setup` under Claude Code) from orphaning the user's real `claude` binary when `~/.local/bin/claude` is already a user-curated symlink to the real binary. When an existing non-shim symlink is found at install time, resolve it via `EvalSymlinks` and persist the real-claude path to `~/.config/c3/claude-shim.json`; shim runtime consults that file before falling back to a PATH walk.

**Architecture:** Two surfaces change.
1. **Install side** (`cmd/c3-broker/install_claude_shim.go::installClaudeShim`): when the existing entry at `installPath` is a symlink, `Readlink` + `EvalSymlinks` it. If the resolved path is NOT our launcher and IS executable, persist `{"real_claude": "<resolved>"}` to `~/.config/c3/claude-shim.json` (mode 0600). Then replace the symlink with one pointing at the launcher, as today. The function signature stays `installClaudeShim(installPath, launcher string, force bool) error`.
2. **Shim runtime** (`cmd/claude-shim/main.go::findRealClaude`): lookup order becomes (1) `$C3_CLAUDE_REAL`, (2) `~/.config/c3/claude-shim.json` real_claude (must exist, be executable, NOT resolve to the shim itself), (3) PATH walk skipping the shim, (4) error. Corrupt/missing config falls through silently.

**Tech Stack:** Go 1.x stdlib only (`encoding/json`, `os`, `path/filepath`, `errors`). New file at `internal/shimconfig/shimconfig.go` holds the shared path/read/write helpers so both binaries import the same logic. No new third-party deps.

---

## File Structure

- **Create** `internal/shimconfig/shimconfig.go` — `Path() (string, error)`, `Load(path string) (realClaude string, ok bool)`, `Save(path, realClaude string) error`. Single source of truth for the JSON shape and XDG-aware path resolution (mirrors `internal/mappings/path.go`). Both `cmd/c3-broker` and `cmd/claude-shim` import this; no code duplication.
- **Create** `internal/shimconfig/shimconfig_test.go` — unit tests for path resolution, round-trip Save→Load, corrupt-JSON tolerance, missing-file tolerance.
- **Modify** `cmd/c3-broker/install_claude_shim.go` — in the existing-symlink branch of `installClaudeShim`, resolve via `Readlink` + `EvalSymlinks`, decide whether to persist via `shimconfig.Save`, then replace the symlink as today. Signature unchanged.
- **Modify** `cmd/c3-broker/install_claude_shim_test.go` — add a test asserting that install-over-existing-symlink-to-non-shim writes the config file with the resolved target.
- **Modify** `cmd/claude-shim/main.go` — extend `findRealClaude` to consult `shimconfig.Load` between `$C3_CLAUDE_REAL` and the PATH walk; reject the config-supplied path if it doesn't exist, isn't executable, or resolves to the shim itself.
- **Modify** `cmd/claude-shim/main_test.go` — add tests for: (a) config-supplied path beats PATH walk, (b) corrupt/missing config falls through to PATH walk, (c) env var still beats config, (d) config path that resolves to shim itself is rejected.

---

### Task 1: shimconfig package — path resolver

**Files:**
- Create: `internal/shimconfig/shimconfig.go`
- Create: `internal/shimconfig/shimconfig_test.go`

- [ ] **Step 1: Write the failing test for Path()**

`internal/shimconfig/shimconfig_test.go`:

```go
package shimconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPath_UsesXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(dir, "c3", "claude-shim.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPath_FallsBackToHomeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(home, ".config", "c3", "claude-shim.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPath_NoXDGNoHome_Error(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// On some platforms UserHomeDir checks USERPROFILE etc.; this test
	// only asserts the public contract: when both XDG and HOME are
	// unresolvable, Path returns an error rather than "/.config/c3/...".
	if _, err := Path(); err == nil {
		// Allow success only if UserHomeDir somehow still resolves
		// (e.g. on darwin via /etc/passwd). The unix path here is
		// HOME-driven so this should error.
		if os.Getenv("HOME") == "" {
			t.Skip("UserHomeDir resolved despite unset HOME; platform-dependent")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/shimconfig/ -run TestPath -v`
Expected: FAIL with "undefined: Path" (package doesn't exist yet).

- [ ] **Step 3: Implement Path()**

`internal/shimconfig/shimconfig.go`:

```go
// Package shimconfig is the single source of truth for the
// claude-shim.json file shared between the c3-broker installer and
// the claude-shim runtime. Lives outside both binaries' main packages
// so each can import it without circular references.
package shimconfig

import (
	"os"
	"path/filepath"
)

// Path returns the canonical claude-shim.json location:
//
//	$XDG_CONFIG_HOME/c3/claude-shim.json     (if set)
//	<UserHomeDir>/.config/c3/claude-shim.json (otherwise)
//
// Mirrors internal/mappings.DefaultPath so both config files live
// next to each other.
func Path() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "c3", "claude-shim.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "c3", "claude-shim.json"), nil
}
```

- [ ] **Step 4: Run tests to verify Path() tests pass**

Run: `go test ./internal/shimconfig/ -run TestPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shimconfig/shimconfig.go internal/shimconfig/shimconfig_test.go
git commit -m "shimconfig: add Path() helper for ~/.config/c3/claude-shim.json"
```

NOTE: per task contract Karthi said "DO NOT commit." If running this plan under Karthi's overnight authorization, SKIP every commit step at the end of each task and instead leave the working tree dirty for him to review. The commit steps are listed for completeness so the plan stays self-contained.

---

### Task 2: shimconfig package — Save / Load

**Files:**
- Modify: `internal/shimconfig/shimconfig.go`
- Modify: `internal/shimconfig/shimconfig_test.go`

- [ ] **Step 1: Write failing tests for Save/Load round trip and tolerance**

Append to `internal/shimconfig/shimconfig_test.go`:

```go
func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := Save(path, "/usr/local/bin/claude-real"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := Load(path)
	if !ok {
		t.Fatal("Load: ok=false after Save")
	}
	if got != "/usr/local/bin/claude-real" {
		t.Fatalf("got %q, want /usr/local/bin/claude-real", got)
	}
}

func TestLoad_MissingFile_OkFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")
	if _, ok := Load(path); ok {
		t.Fatal("ok=true for missing file")
	}
}

func TestLoad_CorruptJSON_OkFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(path); ok {
		t.Fatal("ok=true for corrupt JSON")
	}
}

func TestLoad_EmptyRealClaude_OkFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := os.WriteFile(path, []byte(`{"real_claude": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(path); ok {
		t.Fatal("ok=true for empty real_claude field")
	}
}

func TestSave_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "claude-shim.json")
	if err := Save(path, "/x"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file missing after Save: %v", err)
	}
}

func TestSave_FileMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := Save(path, "/x"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/shimconfig/ -v`
Expected: FAIL with "undefined: Save" / "undefined: Load".

- [ ] **Step 3: Implement Save and Load**

Append to `internal/shimconfig/shimconfig.go`:

```go
import (
	"encoding/json"
)

// File is the on-disk JSON shape. Forward-compatible: unknown fields
// are ignored on read so future broker versions can add fields without
// breaking older shims.
type File struct {
	RealClaude string `json:"real_claude"`
}

// Load reads the shim config at path. Returns ("", false) for any
// error (missing file, corrupt JSON, empty real_claude). NEVER returns
// an error — the shim's contract is "silent fallback to PATH walk on
// config issues; never hard-fail at runtime."
func Load(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return "", false
	}
	if f.RealClaude == "" {
		return "", false
	}
	return f.RealClaude, true
}

// Save writes {"real_claude": realClaude} to path with mode 0600.
// Creates the parent directory (0700) if missing.
func Save(path, realClaude string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(File{RealClaude: realClaude}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
```

NOTE on imports: merge the new `encoding/json` import into the single existing `import (...)` block at the top of the file rather than adding a second import block.

- [ ] **Step 4: Run all shimconfig tests**

Run: `go test ./internal/shimconfig/ -v`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shimconfig/shimconfig.go internal/shimconfig/shimconfig_test.go
git commit -m "shimconfig: Save/Load with silent-fallback contract"
```

(See Task 1 commit note — skip commits under Karthi's no-commit instruction.)

---

### Task 3: install_claude_shim — persist real-claude on symlink takeover

**Files:**
- Modify: `cmd/c3-broker/install_claude_shim.go`
- Modify: `cmd/c3-broker/install_claude_shim_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/c3-broker/install_claude_shim_test.go`:

```go
import (
	// add to existing import block:
	"encoding/json"

	"github.com/karthikeyan5/c3/internal/shimconfig"
)

func TestInstallClaudeShim_SymlinkToRealClaude_PersistsConfig(t *testing.T) {
	dir := t.TempDir()
	// Isolate XDG_CONFIG_HOME so the install writes the shim config
	// inside the test tempdir instead of $HOME/.config.
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	launcher := filepath.Join(dir, "claude-shim")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Existing "real claude" the user installed (e.g. via npm/nvm).
	realClaude := filepath.Join(dir, "versions", "2.1.143", "claude")
	if err := os.MkdirAll(filepath.Dir(realClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realClaude, []byte("#!/bin/sh\necho real\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(dir, "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(installPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realClaude, installPath); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeShim(installPath, launcher, false); err != nil {
		t.Fatalf("installClaudeShim: %v", err)
	}

	// Symlink now points at launcher.
	got, err := os.Readlink(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != launcher {
		t.Fatalf("link target %q, want %q", got, launcher)
	}

	// And the shim config records the resolved real-claude path.
	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("shim config missing: %v", err)
	}
	var parsed shimconfig.File
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse shim config: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(realClaude)
	if parsed.RealClaude != wantResolved {
		t.Fatalf("real_claude in config = %q, want %q", parsed.RealClaude, wantResolved)
	}
}

func TestInstallClaudeShim_SymlinkAlreadyPointsAtLauncher_NoConfigWrite(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	launcher := filepath.Join(dir, "claude-shim")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(dir, "claude")
	// Existing symlink already points at our launcher (re-running install).
	if err := os.Symlink(launcher, installPath); err != nil {
		t.Fatal(err)
	}

	if err := installClaudeShim(installPath, launcher, false); err != nil {
		t.Fatalf("installClaudeShim: %v", err)
	}

	// Final link still points at launcher.
	got, err := os.Readlink(installPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != launcher {
		t.Fatalf("link target %q, want %q", got, launcher)
	}

	// No config should have been written for a self-link.
	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("shim config exists but shouldn't: stat err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./cmd/c3-broker/ -run 'TestInstallClaudeShim_(SymlinkToRealClaude|SymlinkAlreadyPointsAtLauncher)' -v`
Expected: FAIL — the install logic doesn't write config yet.

- [ ] **Step 3: Update installClaudeShim to persist config in the symlink branch**

In `cmd/c3-broker/install_claude_shim.go`, replace the `case info.Mode()&os.ModeSymlink != 0:` branch (currently lines 79-81) with logic that resolves the existing symlink and persists the real-claude path when it isn't our launcher.

Add `"github.com/karthikeyan5/c3/internal/shimconfig"` to the imports.

New case body (note: variable `launcher` is the function argument):

```go
case info.Mode()&os.ModeSymlink != 0:
	// Existing symlink — may be a previous shim install (target ==
	// launcher), or a user-curated link pointing at the real claude
	// binary. In the latter case, persist the resolved real-claude
	// path to ~/.config/c3/claude-shim.json so the shim runtime can
	// re-find it after we replace this symlink. See TODO.md #17 fix
	// option (a), locked 2026-05-18.
	if resolved, err := filepath.EvalSymlinks(installPath); err == nil {
		resolvedLauncher, lerr := filepath.EvalSymlinks(launcher)
		if lerr != nil {
			resolvedLauncher = launcher
		}
		if resolved != resolvedLauncher {
			if info, sErr := os.Stat(resolved); sErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				if cfgPath, pErr := shimconfig.Path(); pErr == nil {
					// Best-effort: a write failure here must not
					// block the install. The shim falls back to
					// the PATH walk if the config is missing.
					_ = shimconfig.Save(cfgPath, resolved)
				}
			}
		}
	}
```

Keep the `os.Remove(installPath)` + `os.Symlink(launcher, installPath)` tail unchanged at the bottom of the function.

- [ ] **Step 4: Run all install_claude_shim tests**

Run: `go test ./cmd/c3-broker/ -run TestInstallClaudeShim -v`
Expected: All PASS, including the two new tests and the five existing tests (FreshInstall, RefusesNonShimFile, ForceOverwritesNonShimFile, ReplacesExistingSymlink, AllowsOverwriteWhenSentinelPresent).

Special attention: `TestInstallClaudeShim_ReplacesExistingSymlink` uses a stale target that isn't the launcher, so the new code path will also write a config entry for it. That test only asserts the final symlink target, so it should still pass. If it fails because the existing test pollutes a per-user XDG path, set `t.Setenv("XDG_CONFIG_HOME", t.TempDir())` at the top of that test as part of this task too.

- [ ] **Step 5: Commit**

```bash
git add cmd/c3-broker/install_claude_shim.go cmd/c3-broker/install_claude_shim_test.go
git commit -m "install-claude-shim: remember real-claude on symlink takeover"
```

(Skip under no-commit.)

---

### Task 4: claude-shim runtime — consult config before PATH walk

**Files:**
- Modify: `cmd/claude-shim/main.go`
- Modify: `cmd/claude-shim/main_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/claude-shim/main_test.go`:

```go
import (
	"github.com/karthikeyan5/c3/internal/shimconfig"
)

func TestFindRealClaude_PrefersConfigOverPathWalk(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("C3_CLAUDE_REAL", "")

	// Config-supplied real-claude.
	cfgClaude := filepath.Join(dir, "real", "claude")
	if err := os.MkdirAll(filepath.Dir(cfgClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := shimconfig.Save(cfgPath, cfgClaude); err != nil {
		t.Fatal(err)
	}

	// PATH-walk decoy: a different `claude` exists on PATH. Config
	// must win.
	decoyDir := filepath.Join(dir, "decoy")
	if err := os.MkdirAll(decoyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(decoyDir, "claude")
	if err := os.WriteFile(decoy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoyDir)

	got, err := findRealClaude("/anywhere/shim/claude")
	if err != nil {
		t.Fatal(err)
	}
	if got != cfgClaude {
		t.Fatalf("got %q, want %q (config should beat PATH walk)", got, cfgClaude)
	}
}

func TestFindRealClaude_EnvVarBeatsConfig(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfgClaude := filepath.Join(dir, "from-config", "claude")
	if err := os.MkdirAll(filepath.Dir(cfgClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := shimconfig.Save(cfgPath, cfgClaude); err != nil {
		t.Fatal(err)
	}

	envClaude := filepath.Join(dir, "from-env", "claude")
	if err := os.MkdirAll(filepath.Dir(envClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("C3_CLAUDE_REAL", envClaude)

	got, err := findRealClaude("/anywhere/shim/claude")
	if err != nil {
		t.Fatal(err)
	}
	if got != envClaude {
		t.Fatalf("got %q, want %q (env var should beat config)", got, envClaude)
	}
}

func TestFindRealClaude_CorruptConfigFallsBackToPathWalk(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("C3_CLAUDE_REAL", "")

	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	pathDir := filepath.Join(dir, "pathdir")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathClaude := filepath.Join(pathDir, "claude")
	if err := os.WriteFile(pathClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got, err := findRealClaude("/anywhere/shim/claude")
	if err != nil {
		t.Fatal(err)
	}
	if got != pathClaude {
		t.Fatalf("got %q, want %q (corrupt config should silently fall back)", got, pathClaude)
	}
}

func TestFindRealClaude_ConfigPathMissing_FallsBackToPathWalk(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("C3_CLAUDE_REAL", "")

	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Config points at a binary that no longer exists.
	if err := shimconfig.Save(cfgPath, filepath.Join(dir, "nonexistent")); err != nil {
		t.Fatal(err)
	}

	pathDir := filepath.Join(dir, "pathdir")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathClaude := filepath.Join(pathDir, "claude")
	if err := os.WriteFile(pathClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got, err := findRealClaude("/anywhere/shim/claude")
	if err != nil {
		t.Fatal(err)
	}
	if got != pathClaude {
		t.Fatalf("got %q, want %q (config-target-missing should fall back)", got, pathClaude)
	}
}

func TestFindRealClaude_ConfigPathResolvesToShim_FallsBackToPathWalk(t *testing.T) {
	// Pathological case: config points at the shim itself (e.g. user
	// re-symlinked something pointing back at the shim). Must NOT cause
	// an exec loop. Fall back to PATH walk.
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("C3_CLAUDE_REAL", "")

	shimBin := filepath.Join(dir, "shim", "claude-shim")
	if err := os.MkdirAll(filepath.Dir(shimBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shimBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shimSelf := filepath.Join(dir, "shim", "claude")
	if err := os.Symlink(shimBin, shimSelf); err != nil {
		t.Fatal(err)
	}

	// Config points at the shim's own resolved binary.
	cfgPath, err := shimconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := shimconfig.Save(cfgPath, shimBin); err != nil {
		t.Fatal(err)
	}

	pathDir := filepath.Join(dir, "pathdir")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pathClaude := filepath.Join(pathDir, "claude")
	if err := os.WriteFile(pathClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got, err := findRealClaude(shimSelf)
	if err != nil {
		t.Fatal(err)
	}
	if got != pathClaude {
		t.Fatalf("got %q, want %q (config-resolves-to-shim must fall back)", got, pathClaude)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./cmd/claude-shim/ -run 'TestFindRealClaude_(PrefersConfigOverPathWalk|EnvVarBeatsConfig|CorruptConfigFallsBackToPathWalk|ConfigPathMissing|ConfigPathResolvesToShim)' -v`
Expected: FAIL — findRealClaude doesn't consult the config yet.

- [ ] **Step 3: Update findRealClaude**

Replace the body of `findRealClaude` in `cmd/claude-shim/main.go` with:

```go
func findRealClaude(self string) (string, error) {
	if explicit := os.Getenv("C3_CLAUDE_REAL"); explicit != "" {
		return explicit, nil
	}

	selfAbs, _ := filepath.Abs(self)
	if resolved, err := filepath.EvalSymlinks(selfAbs); err == nil {
		selfAbs = resolved
	}

	// Config-supplied real-claude: written by `c3-broker
	// install-claude-shim` when it takes over a user-curated
	// symlink. Corrupt/missing config silently falls through to
	// the PATH walk. See TODO.md #17.
	if cfgPath, err := shimconfig.Path(); err == nil {
		if candidate, ok := shimconfig.Load(cfgPath); ok {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				resolved, err := filepath.EvalSymlinks(candidate)
				if err == nil && resolved != selfAbs {
					return candidate, nil
				}
			}
		}
	}

	pathParts := filepath.SplitList(os.Getenv("PATH"))
	for _, dir := range pathParts {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "claude")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		if resolved == selfAbs {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("could not find real claude in $PATH (shim excluded); set C3_CLAUDE_REAL")
}
```

Add `"github.com/karthikeyan5/c3/internal/shimconfig"` to the import block.

- [ ] **Step 4: Run all claude-shim tests**

Run: `go test ./cmd/claude-shim/ -v`
Expected: All PASS, including the new five and the pre-existing `TestFindRealClaude_PrefersC3RealOverride`, `TestFindRealClaude_SkipsShimSelfOnPath`, `TestFindRealClaude_ErrorsWhenNoneFound`, and the end-to-end test.

Note on test isolation: pre-existing tests don't set `XDG_CONFIG_HOME`, so they may inadvertently read the developer's real `~/.config/c3/claude-shim.json`. If `TestFindRealClaude_SkipsShimSelfOnPath` or `TestFindRealClaude_ErrorsWhenNoneFound` regresses on a dev machine where the file exists, add `t.Setenv("XDG_CONFIG_HOME", t.TempDir())` to each as part of this task. (`TestFindRealClaude_PrefersC3RealOverride` is unaffected because the env-var check short-circuits before config.)

- [ ] **Step 5: Commit**

```bash
git add cmd/claude-shim/main.go cmd/claude-shim/main_test.go
git commit -m "claude-shim: consult ~/.config/c3/claude-shim.json before PATH walk"
```

(Skip under no-commit.)

---

### Task 5: End-to-end verification

**Files:** none modified — verification only.

- [ ] **Step 1: Full test suite**

Run: `go test -count=1 -race ./...`
Expected: ok for every package, no FAIL, no race warnings.

- [ ] **Step 2: go vet**

Run: `go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 3: go build**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 4: Manual install-side smoke (optional, do NOT run under Karthi's real $HOME)**

Skip on Karthi's machine. The unit tests cover the install + runtime contract under `t.TempDir()`. Running `c3-broker install-claude-shim` against the real `~/.local/bin/claude` is exactly the scenario we're fixing — only attempt it once Karthi explicitly clears that.

- [ ] **Step 5: Report (no commit)**

Per task instructions, do NOT run `git commit`. Leave working tree dirty and report:
- Plan path
- Files changed
- New test names + outcome
- Self-review findings (anything to flag)
- Open questions

---

## Self-review checklist

**1. Spec coverage** — every contract item from Karthi's brief is covered:
- (a) "If installPath is a symlink, Readlink + EvalSymlinks" → Task 3 step 3.
- (b) "If resolved path NOT our launcher, persist to ~/.config/c3/claude-shim.json" → Task 3 step 3 (Save in the `resolved != resolvedLauncher` branch).
- (c) "Always replace symlink with launcher" → existing tail of `installClaudeShim`, retained.
- (d) Shim lookup order env > config > PATH → Task 4 step 3 reordering of `findRealClaude`.
- (e) "config target must exist, be executable, NOT be the shim itself" → Task 4 step 3 stat-and-EvalSymlinks check.
- (f) "Corrupt/missing config = silent fallback" → `Load` returns `("", false)` on any error; shim handles `ok=false` by falling through.
- (g) "Test install-over-symlink writes config" → Task 3 step 1 (`TestInstallClaudeShim_SymlinkToRealClaude_PersistsConfig`).
- (h) "Test shim reads config and execs saved path" → Task 4 step 1 (`TestFindRealClaude_PrefersConfigOverPathWalk`).
- (i) "Test corrupt/missing fallback" → Task 4 step 1 (CorruptConfig + ConfigPathMissing).
- (j) "Test env beats config" → Task 4 step 1 (EnvVarBeatsConfig).
- (k) "Keep `installClaudeShim(installPath, launcher string, force bool) error` signature" → Task 3 only modifies the body of the symlink branch.

**2. Placeholder scan** — no "TBD", "implement later", "appropriate error handling"; every code block is the complete change.

**3. Type consistency** — `shimconfig.File`, `shimconfig.Path()`, `shimconfig.Load(path)` (returns `(string, bool)`), `shimconfig.Save(path, real string) error` are used consistently across Tasks 1, 2, 3, 4. Module path `github.com/karthikeyan5/c3` matches the existing `internal/mappings` import.

**4. Out-of-scope guards** — no edits to `cmd/c3-broker/setup.go` (parallel agent T's turf), no flag changes, no migration prompts, no other TODO items touched.
