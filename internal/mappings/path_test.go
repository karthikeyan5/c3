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

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(dir, "c3", "mappings.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultPath_FallsBackToHome(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv("XDG_CONFIG_HOME")
	t.Setenv("HOME", dir)

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(dir, ".config", "c3", "mappings.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
