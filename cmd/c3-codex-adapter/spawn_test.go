package main

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSpawnDetached_ReapsExitedChild mirrors the Claude adapter's test: it
// guards the zombie-broker fix in the Codex adapter's spawnDetached. A broker
// that loses the singleton flock race exits within milliseconds; setsid does
// not reparent it to init, so without async reaping it lingers as a <defunct>
// zombie child of the adapter (observed 2026-06-22). We spawn a fast-exiting
// child and assert the kernel reports it gone (ESRCH) — i.e. reaped.
func TestSpawnDetached_ReapsExitedChild(t *testing.T) {
	cmd := exec.Command("true") // resolved from PATH; exits 0 immediately, no shell
	if err := spawnDetached(cmd); err != nil {
		t.Fatalf("spawnDetached: %v", err)
	}
	pid := cmd.Process.Pid

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return // reaped — success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pid=%d was not reaped within 3s (zombie leak)", pid)
}
