package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOffsetStore_LoadEmptyReturnsZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s, err := newOffsetStore("telegram")
	if err != nil {
		t.Fatalf("newOffsetStore: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file should be (0,nil), got err: %v", err)
	}
	if got != 0 {
		t.Errorf("Load on missing file = %d, want 0", got)
	}
}

func TestOffsetStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s, err := newOffsetStore("telegram")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(483746793); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Reopen via a fresh store — simulates broker restart.
	s2, _ := newOffsetStore("telegram")
	got, err := s2.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != 483746793 {
		t.Errorf("round-trip: got %d, want 483746793", got)
	}
}

func TestOffsetStore_AtomicRewriteSurvivesMidWrite(t *testing.T) {
	// The atomic rewrite uses .tmp + rename. Verify the .tmp file isn't
	// left behind after a successful Save (would leak files over time).
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s, _ := newOffsetStore("telegram")
	for i := int64(1); i <= 10; i++ {
		if err := s.Save(i); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "c3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("found leftover .tmp file after successful Saves: %s", e.Name())
		}
	}
}

func TestOffsetStore_FileMode0600(t *testing.T) {
	// The offset file may sit alongside the bot-token mappings; mode 0600
	// is defense-in-depth (offset itself isn't sensitive, but we don't
	// want to set a weaker precedent in the c3 state dir).
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s, _ := newOffsetStore("telegram")
	if err := s.Save(42); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestOffsetStore_LoadCorruptedReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s, _ := newOffsetStore("telegram")
	// Write a corrupt JSON directly to the path.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Error("corrupted file should produce a parse error, got nil")
	}
}

func TestOffsetStore_PerChannelKeyedSeparately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	tg, _ := newOffsetStore("telegram")
	web, _ := newOffsetStore("web")
	if err := tg.Save(100); err != nil {
		t.Fatal(err)
	}
	if err := web.Save(200); err != nil {
		t.Fatal(err)
	}
	got, _ := tg.Load()
	if got != 100 {
		t.Errorf("telegram offset = %d, want 100 (cross-channel leak suspected)", got)
	}
	got, _ = web.Load()
	if got != 200 {
		t.Errorf("web offset = %d, want 200", got)
	}
}
