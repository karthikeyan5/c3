package telegram

import (
	"testing"

	"github.com/karthikeyan5/c3/internal/channel"
)

// I4: when Emit DROPS an inbound (worker queue full / stopped), dispatchMessage
// records msgToUpdate + the tracker's in-flight Register BEFORE Emit. A drop must
// not strand that update_id in-flight forever (which wedges the contiguous-prefix
// offset for ALL inbound on a >64 burst). On a drop, dispatchMessage must clear
// the seam and MarkDone the update so the committed offset advances past it.
func TestDispatchMessage_EmitDrop_DoesNotStrandOffset(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow, emitDrops: true}
	c := makeChannel(h)
	c.offTrk = newOffsetTracker(100)
	c.msgToUpdate = map[int64]int64{}

	// Update 101 is the next contiguous id; its Emit will be DROPPED.
	c.offTrk.Register(101)
	c.dispatchMessage(101, textMsg("hi", 42), false, nil)

	// Emit was attempted (and dropped).
	if got := h.emitCount(); got != 1 {
		t.Fatalf("Emit should be attempted once; got %d", got)
	}
	// The committed offset MUST advance past the dropped update — not stall at 100.
	if got := c.offTrk.Committed(); got != 101 {
		t.Fatalf("dropped update stranded the offset: committed=%d, want 101 (advanced past the drop)", got)
	}
	// The orphaned msgToUpdate seam must be cleared (no leak).
	c.mu.Lock()
	_, leaked := c.msgToUpdate[textMsg("hi", 42).MessageId]
	c.mu.Unlock()
	if leaked {
		t.Fatal("msgToUpdate entry for a dropped inbound must be cleared (leak)")
	}
}

// I4 companion: a SUCCESSFUL Emit must NOT mark the update done itself — that is
// the persist callback's job (offset advances only after Append+fsync). The seam
// stays staged and committed holds until the persist callback fires.
func TestDispatchMessage_EmitOK_LeavesUpdateInFlight(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow, emitDrops: false}
	c := makeChannel(h)
	c.offTrk = newOffsetTracker(100)
	c.msgToUpdate = map[int64]int64{}

	c.offTrk.Register(101)
	c.dispatchMessage(101, textMsg("hi", 42), false, nil)

	// Emit accepted ⇒ the update is still in-flight (NOT done) until the broker's
	// persist callback MarkDone-s it. The offset must hold at 100.
	if got := c.offTrk.Committed(); got != 100 {
		t.Fatalf("a successfully-emitted (not-yet-persisted) update must keep the offset at 100; got %d", got)
	}
	// The seam must be staged for the persist callback to resolve.
	c.mu.Lock()
	uid, staged := c.msgToUpdate[textMsg("hi", 42).MessageId]
	c.mu.Unlock()
	if !staged || uid != 101 {
		t.Fatalf("msgToUpdate must stage msg→update (got staged=%v uid=%d) so the persist callback can MarkDone it", staged, uid)
	}
}

// I-SEC: a /status from a NON-allowlisted sender must be DROPPED — no reply, no
// Emit, no global summary leak — and the update marked done (no stall). The gate
// runs BEFORE the /status intercept, so a stranger's "/status" hits the silent
// default-deny drop and never reaches HandleCommand.
func TestDispatchMessage_StatusFromStranger_DroppedNotAnswered(t *testing.T) {
	// cmdHandled=true would answer /status IF the intercept were reached — it must
	// NOT be, because the gate DROPS first.
	h := &fakeHost{decision: channel.GateInboundDrop, cmdHandled: true}
	c := makeChannel(h)
	c.offTrk = newOffsetTracker(200)
	c.msgToUpdate = map[int64]int64{}

	c.offTrk.Register(201)
	// No recover needed: the gate drops before any SendReply, so the nil bot in
	// makeChannel is never touched.
	c.dispatchMessage(201, textMsg("/status", 99), false, nil)

	if got := h.emitCount(); got != 0 {
		t.Fatalf("a dropped /status must not Emit; got %d", got)
	}
	// HandleCommand must NOT have been called — the gate dropped first, so the
	// stranger never reaches the /status intercept (no reply, no summary leak).
	if h.handleCommandCalled() {
		t.Fatal("a non-allowlisted /status must NOT reach HandleCommand (no reply, no global summary leak)")
	}
	// The dropped update must be marked done so it doesn't wedge the offset.
	if got := c.offTrk.Committed(); got != 201 {
		t.Fatalf("dropped /status must MarkDone its update; committed=%d, want 201", got)
	}
}

// I-SEC companion: an allowlisted /status IS still answered (handled by the
// broker, never routed to an agent), and its update is marked done.
func TestDispatchMessage_StatusFromAllowlisted_StillAnswered(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow, cmdHandled: true}
	c := makeChannel(h)
	c.offTrk = newOffsetTracker(300)
	c.msgToUpdate = map[int64]int64{}

	c.offTrk.Register(301)
	func() {
		defer func() { _ = recover() }() // tolerate nil-bot SendReply in the intercept
		c.dispatchMessage(301, textMsg("/status", 42), false, nil)
	}()

	if !h.handleCommandCalled() {
		t.Fatal("an allowlisted /status must reach HandleCommand (answered, not routed)")
	}
	if got := h.emitCount(); got != 0 {
		t.Fatalf("an answered /status must NOT be routed to an agent; Emit=%d, want 0", got)
	}
	if got := c.offTrk.Committed(); got != 301 {
		t.Fatalf("an answered /status must MarkDone its update; committed=%d, want 301", got)
	}
}
