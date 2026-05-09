package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path-resolution invariant (Karthi 2026-05-09): SocketPath() and
// PidFilePath() MUST live in the same directory and MUST be deterministic
// across invocations regardless of the calling process's env. Two brokers
// with different XDG_RUNTIME_DIR ended up on different sockets, both
// polled Telegram, both 409'd, adapter conns scattered → claims landed on
// the wrong broker → messages fell to fallback even with valid claims.
func TestSocketAndPidFile_SameDirectory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	sock := SocketPath()
	pid := PidFilePath()
	if filepath.Dir(sock) != filepath.Dir(pid) {
		t.Errorf("SocketPath dir %q != PidFilePath dir %q",
			filepath.Dir(sock), filepath.Dir(pid))
	}
}

func TestSocketPath_XDGRuntimeHonored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	got := SocketPath()
	want := filepath.Join(dir, "c3.sock")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSocketPath_NoXDG_FallsBackDeterministically(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir()) // make sure HOME is unrelated
	got := SocketPath()
	if !strings.HasSuffix(got, "/c3.sock") {
		t.Errorf("got %q, want path ending in /c3.sock", got)
	}
	// Calling twice in the same env must return the same value.
	if got2 := SocketPath(); got2 != got {
		t.Errorf("non-deterministic: %q vs %q", got, got2)
	}
}

func TestPidFilePath_XDGRuntimeHonored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	got := PidFilePath()
	want := filepath.Join(dir, "c3-broker.pid")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPidFilePath_NoXDG_FallsBackDeterministically(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())
	got := PidFilePath()
	if !strings.HasSuffix(got, "/c3-broker.pid") {
		t.Errorf("got %q, want path ending in /c3-broker.pid", got)
	}
	if got2 := PidFilePath(); got2 != got {
		t.Errorf("non-deterministic: %q vs %q", got, got2)
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
