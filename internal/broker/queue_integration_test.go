package broker

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/queue"
)

// No session attached → the inbound is queued (not dropped) AND a held-count
// auto-reply is sent (reusing the 5-min fallback cooldown).
func TestForwardOrFallback_NoSession_QueuesAndHeldReply(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "hello", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in, 1)

	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	if n, _ := b.Queue.Pending(qrk); n != 1 {
		t.Fatalf("no-session inbound should be queued; pending=%d, want 1", n)
	}
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Fatalf("expected one held-count auto-reply, got %d sends", got)
	}
	// Second message within cooldown: queued silently (no second reply).
	in2 := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 2, Text: "again", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in2, 1)
	if n, _ := b.Queue.Pending(qrk); n != 2 {
		t.Fatalf("second inbound should also queue; pending=%d, want 2", n)
	}
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Fatalf("second inbound within cooldown must NOT send a second reply; got %d sends", got)
	}
}

// BUG #3 regression: on an edit-capable channel, several unattached inbounds for
// ONE route must drive the held-reply to the TRUE queued count by EDITING a
// single message in place — not freeze at the first count behind the 5-min
// cooldown (which sent one "1 queued" reply and then silently dropped the rest).
func TestForwardOrFallback_NoSession_HeldReplyEditsInPlace(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{editMessages: true, replyReturnID: 7001}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	// Three unattached inbounds for the SAME route, back to back (well within the
	// 5-min cooldown that used to suppress everything after the first).
	for i := 1; i <= 3; i++ {
		in := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: int64(i), Text: "hi", Timestamp: time.Now()}
		w.forwardOrFallback(context.Background(), in, 1)
	}

	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	if n, _ := b.Queue.Pending(qrk); n != 3 {
		t.Fatalf("all three inbounds should be queued; pending=%d, want 3", n)
	}
	// Exactly ONE held-reply message is SENT (the first hold); the rest EDIT it.
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Fatalf("expected exactly one held-reply SEND, got %d", got)
	}
	edits := fc.editCallsSnapshot()
	if len(edits) != 2 {
		t.Fatalf("expected two in-place EDITS of the held-reply, got %d", len(edits))
	}
	last := edits[len(edits)-1]
	if last.MessageID != 7001 {
		t.Fatalf("edit should target the tracked held-reply message id 7001; got %d", last.MessageID)
	}
	// The held-reply must reflect the TRUE count (3), not a frozen "1 queued".
	if !strings.Contains(last.Text, "3 messages queued") {
		t.Fatalf("held-reply edit should reflect the true count; got %q", last.Text)
	}
}

// Once the route goes live again (a delivered inbound), the held-reply cycle
// ends: a later detach + hold must SEND a fresh message rather than edit the
// now-buried earlier one.
func TestForwardOrFallback_LiveDelivery_ClearsHeldReplyTracking(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{editMessages: true, replyReturnID: 8001}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	// First hold while unattached → SENDS a held-reply and tracks its id.
	in1 := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "hi", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in1, 1)
	if _, ok := b.Fallbacks.HeldMessageID(key); !ok {
		t.Fatal("first hold should track a held-reply message id")
	}

	// Attach a live holder. Drain whatever the broker pushes so the synchronous
	// net.Pipe write in the delivery path doesn't block.
	adapterEnd, holderEnd := net.Pipe()
	defer adapterEnd.Close()
	defer holderEnd.Close()
	adapterConn := ipc.NewConn(adapterEnd)
	go func() {
		for {
			if _, err := adapterConn.ReadFrame(); err != nil {
				return
			}
		}
	}()
	stub := &Stub{CLI: "claude", PID: os.Getpid(), CWD: "/home/u/proj", ConnID: 1}
	stub.Reattach(ipc.NewConn(holderEnd), 1)
	b.Routes.Claim(key, stub)

	// A live delivery ends the hold cycle → tracking cleared.
	in2 := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 2, Text: "live", Timestamp: time.Now()}
	w.forwardOrFallback(context.Background(), in2, 1)
	if _, ok := b.Fallbacks.HeldMessageID(key); ok {
		t.Fatal("a live delivery must clear the held-reply tracking")
	}
}

// A resumed session that auto-re-attaches (recoverSession) must end the
// held-reply cycle: the route is live again, so the tracked held-reply message
// id is cleared. The resume flow drains the backlog via fetch_queue (a pull,
// not a live push), so a live delivery — the only OTHER clear path — may never
// fire. Without this clear, the NEXT detach would EDIT the now-buried
// pre-resume held-reply far up-thread instead of SENDing a fresh one: the exact
// "invisible/frozen count" symptom BUG #3 killed, regressing for the new cycle
// (review finding, 2026-06-25).
func TestRecoverSession_ClearsHeldReplyTracking(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{editMessages: true})
	defer b.Shutdown()

	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	// Simulate a held-reply that was SENT + tracked during the detached window.
	b.Fallbacks.SetHeldMessageID(key, 7001)

	stub := b.Stubs.Register("claude", 1, "/x", nil)
	stub.SetStableSessionID("sess-1")
	if _, _, ok := b.recoverSession(stub); !ok {
		t.Fatal("expected recoverSession to succeed")
	}
	if _, ok := b.Fallbacks.HeldMessageID(key); ok {
		t.Fatal("recoverSession claim must clear the held-reply tracking (BUG #3 regression)")
	}
}

// A normal attach (tryClaim) also ends the held-reply cycle: claiming the route
// clears the tracked held-reply id so a later detach SENDs a fresh held-reply
// instead of editing a buried one (review finding, 2026-06-25).
func TestTryClaim_ClearsHeldReplyTracking(t *testing.T) {
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{editMessages: true})
	defer b.Shutdown()

	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	b.Fallbacks.SetHeldMessageID(key, 7001)

	stub := b.Stubs.Register("claude", 1, "/x", nil)
	if !b.tryClaim(nil, stub, key, "c3", false, false) {
		t.Fatal("precondition: tryClaim should succeed on a free route")
	}
	if _, ok := b.Fallbacks.HeldMessageID(key); ok {
		t.Fatal("tryClaim must clear the held-reply tracking on a successful claim")
	}
}

func TestHeldReplyText_CarriesCount(t *testing.T) {
	got := heldReplyText(3)
	// Pin a specific count-bearing phrase, not a stray '3'.
	if !strings.Contains(got, "3 messages queued") {
		t.Fatalf("heldReplyText(3) = %q, want '3 messages queued'", got)
	}
	if !strings.Contains(heldReplyText(1), "1 message queued") {
		t.Fatalf("heldReplyText(1) should use the singular '1 message queued'; got %q", heldReplyText(1))
	}
}

func TestFlushInbounds_DedupesReplayedMessageID(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100}

	in := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 7, Text: "hi", Timestamp: time.Now()}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})
	// Simulate a crash-recovery replay of the SAME message_id.
	w.flushInbounds(context.Background(), []*c3types.Inbound{{Channel: "telegram", ChatID: -100, MessageID: 7, Text: "hi", Timestamp: time.Now()}})

	if n, _ := b.Queue.Pending(qrk); n != 1 {
		t.Fatalf("replayed message_id should be deduped; pending=%d, want 1", n)
	}
}
