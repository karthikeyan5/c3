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
