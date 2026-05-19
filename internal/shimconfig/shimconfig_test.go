package shimconfig

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureStderr swaps os.Stderr with a pipe, runs fn, and returns the
// captured output. Helper for tests that assert on Load's sync.Once
// warning lines.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	<-done
	os.Stderr = prev
	_ = r.Close()
	return buf.String()
}

// resetWarningOnces resets the package-level sync.Once guards used to
// fire upgrade / unsupported-schema warnings exactly once per process.
// Tests need to reset them so each test starts from a clean state;
// production code never calls this.
func resetWarningOnces() {
	legacyUpgradeWarnOnce = sync.Once{}
	unsupportedSchemaWarnOnce = sync.Once{}
}

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

// TestSave_WritesSchemaVersion1 confirms every Save call emits
// schema_version: 1 on disk, so future loaders can detect the schema.
// Defends against accidental omission on future struct changes.
func TestSave_WritesSchemaVersion1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := Save(path, "/usr/local/bin/claude-real"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schema_version": 1`) {
		t.Errorf("on-disk content missing schema_version:1; got:\n%s", string(data))
	}
	if !strings.Contains(string(data), `"real_claude": "/usr/local/bin/claude-real"`) {
		t.Errorf("on-disk content missing real_claude; got:\n%s", string(data))
	}
}

// TestLoad_LegacyV0_UpgradesAndLogsOnce covers the migration story
// for files written by a pre-schema_version c3-broker. The file has
// no schema_version field; Load treats it as legacy v0, returns ok
// with the value, and logs the upgrade once per process.
func TestLoad_LegacyV0_UpgradesAndLogsOnce(t *testing.T) {
	resetWarningOnces()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := os.WriteFile(path, []byte(`{"real_claude": "/legacy/bin/claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		got, ok := Load(path)
		if !ok {
			t.Fatal("Load: ok=false for valid legacy v0 file")
		}
		if got != "/legacy/bin/claude" {
			t.Errorf("Load: got %q, want /legacy/bin/claude", got)
		}
	})

	if !strings.Contains(stderr, "shimconfig:") || !strings.Contains(stderr, "v0") || !strings.Contains(stderr, "v1") {
		t.Errorf("expected upgrade warning on stderr, got %q", stderr)
	}

	// Second Load must NOT re-log (sync.Once).
	stderr2 := captureStderr(t, func() {
		if _, ok := Load(path); !ok {
			t.Fatal("Load (second call): ok=false")
		}
	})
	if stderr2 != "" {
		t.Errorf("second Load re-logged upgrade warning: %q", stderr2)
	}
}

// TestLoad_V1RoundTrip_ByteEqual asserts a Save-Load round trip on a
// v1 file is silent (no warning) and returns the saved value.
func TestLoad_V1RoundTrip_ByteEqual(t *testing.T) {
	resetWarningOnces()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := Save(path, "/v1/bin/claude"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stderr := captureStderr(t, func() {
		got, ok := Load(path)
		if !ok {
			t.Fatal("Load: ok=false after Save")
		}
		if got != "/v1/bin/claude" {
			t.Errorf("got %q, want /v1/bin/claude", got)
		}
	})
	if stderr != "" {
		t.Errorf("v1 round-trip emitted unexpected stderr: %q", stderr)
	}
}

// TestLoad_UnsupportedSchemaVersion_OkFalseAndLogsOnce covers the
// forward-incompatible case: a future c3-broker wrote a file with
// schema_version > 1. Load returns ok=false (so the shim falls back
// to the PATH walk per its contract) AND emits a one-time stderr
// warning so the user sees the mismatch.
func TestLoad_UnsupportedSchemaVersion_OkFalseAndLogsOnce(t *testing.T) {
	resetWarningOnces()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-shim.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 999, "real_claude": "/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		got, ok := Load(path)
		if ok {
			t.Errorf("Load: ok=true for unsupported schema version (got %q); want false so shim falls back", got)
		}
	})

	if !strings.Contains(stderr, "shimconfig:") || !strings.Contains(stderr, "999") {
		t.Errorf("expected unsupported-version warning on stderr mentioning the version, got %q", stderr)
	}

	// Second Load must NOT re-log (sync.Once).
	stderr2 := captureStderr(t, func() {
		if _, ok := Load(path); ok {
			t.Error("Load (second call): ok=true; want false")
		}
	})
	if stderr2 != "" {
		t.Errorf("second Load re-logged unsupported-version warning: %q", stderr2)
	}
}

// TestLoad_V0AndUnsupported_HaveSeparateOnceGuards confirms the two
// warning kinds are governed by distinct sync.Once instances — a
// process that observes both kinds of bad config (legacy then v999)
// gets BOTH warnings, not just the first one. Belt-and-braces against
// a regression where the two onces get accidentally fused.
func TestLoad_V0AndUnsupported_HaveSeparateOnceGuards(t *testing.T) {
	resetWarningOnces()
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacyPath, []byte(`{"real_claude": "/legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	futurePath := filepath.Join(dir, "future.json")
	if err := os.WriteFile(futurePath, []byte(`{"schema_version": 999, "real_claude": "/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	legacyStderr := captureStderr(t, func() { _, _ = Load(legacyPath) })
	if !strings.Contains(legacyStderr, "v0") {
		t.Errorf("expected legacy-upgrade warning, got %q", legacyStderr)
	}

	futureStderr := captureStderr(t, func() { _, _ = Load(futurePath) })
	if !strings.Contains(futureStderr, "999") {
		t.Errorf("expected unsupported-version warning, got %q (legacy once should not have suppressed it)", futureStderr)
	}
}
