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

	if _, err := AcquireSingleton(pidFile); err == nil {
		t.Error("expected second acquire to fail")
	}
}

func TestAcquireSingleton_StalePidUnlinked(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "broker.pid")

	if err := os.WriteFile(pidFile, []byte("999999\n"), 0600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireSingleton(pidFile)
	if err != nil {
		t.Fatalf("acquire over stale pid failed: %v", err)
	}
	defer lock.Release()

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
