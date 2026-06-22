package main

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSpawnDetached_ReapsExitedChild guards the zombie-broker fix. spawnBroker
// is racy by design: several adapters can spawn a broker at once, but the
// singleton flock lets only ONE win — the losers exit within milliseconds.
// setsid puts the child in a new session but does NOT reparent it to init, so
// without async reaping each loser lingers as a <defunct> zombie child of the
// adapter until the whole session ends (the accumulation observed 2026-06-22).
//
// We spawn a fast-exiting child through spawnDetached and assert the kernel
// reports it gone (ESRCH) — i.e. reaped. A zombie still answers kill(pid,0)
// with nil (the pid slot is held until reaped), so an unreaped child returns
// nil for the whole window and the test fails. (PID reuse hitting this exact
// freed pid within the window is negligible.)
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
