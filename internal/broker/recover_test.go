package broker

import "testing"

// TestRecoverGoroutine_CatchesPanic: a panic under `defer recoverGoroutine` must
// not propagate (a broker goroutine panic would otherwise crash the process).
func TestRecoverGoroutine_CatchesPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped recoverGoroutine: %v", r)
		}
	}()
	func() {
		defer recoverGoroutine("test.unit")
		panic("boom")
	}()
	// Reaching here means the panic was recovered.
}

// TestRecoverGoroutineThen_RunsCleanupOnPanic: the onPanic cleanup (e.g.
// dispatchOutbound's "unblock the caller's ResultCh") runs when a panic is
// recovered.
func TestRecoverGoroutineThen_RunsCleanupOnPanic(t *testing.T) {
	ran := false
	func() {
		defer recoverGoroutineThen("test.unit", func() { ran = true })
		panic("boom")
	}()
	if !ran {
		t.Fatal("onPanic cleanup did not run after a recovered panic")
	}
}

// TestRecoverGoroutineThen_NoCleanupWhenNoPanic: the cleanup must NOT run on a
// normal return (else dispatchOutbound would double-send to ResultCh).
func TestRecoverGoroutineThen_NoCleanupWhenNoPanic(t *testing.T) {
	ran := false
	func() {
		defer recoverGoroutineThen("test.unit", func() { ran = true })
	}()
	if ran {
		t.Fatal("onPanic cleanup ran without a panic")
	}
}
