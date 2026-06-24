package main

import (
	"testing"
	"time"
)

// TestTakePendingRecoverNotice covers the deferred-CLI-notice logic added for
// auto-attach-on-resume (2026-06-24): the notice must emit at most once, and a
// notice that waited longer than pendingRecoverTTL must be dropped rather than
// surfaced minutes late.
func TestTakePendingRecoverNotice(t *testing.T) {
	a := &adapter{}

	// Nothing pending → no emit.
	if text, ok := a.takePendingRecoverNotice(); ok || text != "" {
		t.Fatalf("empty: got (%q, %v), want (\"\", false)", text, ok)
	}

	// Fresh pending → returned exactly once, then cleared.
	a.setPendingRecoverNotice("hello")
	if text, ok := a.takePendingRecoverNotice(); !ok || text != "hello" {
		t.Fatalf("fresh: got (%q, %v), want (\"hello\", true)", text, ok)
	}
	if text, ok := a.takePendingRecoverNotice(); ok || text != "" {
		t.Fatalf("second take must not re-emit: got (%q, %v), want (\"\", false)", text, ok)
	}

	// Stale pending → dropped (not returned) and cleared.
	a.setPendingRecoverNotice("stale")
	a.pnmu.Lock()
	a.pendingRecoverAt = time.Now().Add(-pendingRecoverTTL - time.Second)
	a.pnmu.Unlock()
	if text, ok := a.takePendingRecoverNotice(); ok || text != "" {
		t.Fatalf("stale: got (%q, %v), want (\"\", false)", text, ok)
	}
	a.pnmu.Lock()
	leftover := a.pendingRecoverNotice
	a.pnmu.Unlock()
	if leftover != "" {
		t.Fatalf("stale notice not cleared: %q", leftover)
	}
}
