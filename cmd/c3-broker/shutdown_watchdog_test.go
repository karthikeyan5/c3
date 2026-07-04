package main

import (
	"testing"
	"time"
)

// TestRunShutdown_CompletesWithinDeadline is the happy path: a shutdown that
// returns promptly reports success (true), so main takes the clean return rather
// than the forced-exit backstop.
func TestRunShutdown_CompletesWithinDeadline(t *testing.T) {
	if !runShutdown(func() {}, time.Second) {
		t.Fatal("runShutdown returned false for an instant shutdown; want true (clean exit path)")
	}
}

// TestRunShutdown_TimesOutOnWedgedDrain is the regression guard for the SIGTERM
// drain-wedge: when shutdown blocks forever (the old srv.Stop() wg.Wait() on a
// parked ReadFrame), the watchdog must fire — runShutdown returns false PROMPTLY
// (not after the wedged drain), so main can force exit(0). This exercises the
// watchdog DECISION without a real os.Exit by testing the factored helper.
func TestRunShutdown_TimesOutOnWedgedDrain(t *testing.T) {
	block := make(chan struct{}) // never closed → the fake shutdown wedges forever
	start := time.Now()
	if runShutdown(func() { <-block }, 50*time.Millisecond) {
		t.Fatal("runShutdown returned true for a wedged shutdown; want false so the watchdog forces exit")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("runShutdown blocked %v — it must return at its %v deadline, not wait on the wedged drain", elapsed, 50*time.Millisecond)
	}
	close(block) // release the parked goroutine so it doesn't leak into later tests
}
