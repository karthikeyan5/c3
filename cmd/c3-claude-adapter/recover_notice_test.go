package main

import (
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
)

// TestRenderRecoverNotice_SurfacesBacklogPreview covers BUG #2: a recovered
// resume that carries a backlog preview (QueuedSummary) must actively SURFACE
// the held messages (sender + kind + preview + total) and instruct the agent to
// drain the rest via fetch_queue — not just a bare count. And it must survive
// the deferred-notice (setPending → takePending) path the resume idle gap relies
// on (channel frames in the idle gap are dropped by Claude Code).
func TestRenderRecoverNotice_SurfacesBacklogPreview(t *testing.T) {
	resp := ipc.RecoverSessionResp{
		Recovered: true, Name: "c3", QueuedCount: 3,
		QueuedSummary: []ipc.QueuedItem{
			{MessageID: 1, Sender: "@k", Kind: "text", Preview: "deploy the thing"},
			{MessageID: 2, Sender: "@k", Kind: "voice", Preview: "(voice)"},
		},
	}
	out := renderRecoverNotice(resp)
	for _, want := range []string{"deploy the thing", "@k", "voice", "fetch_queue", "and 1 more"} {
		if !strings.Contains(out, want) {
			t.Fatalf("recover notice missing %q:\n%s", want, out)
		}
	}
	// The preview must survive the deferred-notice flush path.
	a := &adapter{}
	a.setPendingRecoverNotice(out)
	got, ok := a.takePendingRecoverNotice()
	if !ok || !strings.Contains(got, "deploy the thing") {
		t.Fatalf("flushed deferred notice lost the backlog preview: ok=%v\n%s", ok, got)
	}
}

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
