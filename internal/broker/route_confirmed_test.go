package broker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/queue"
)

// Phase 4 (spec §5): the routeConfirmed tripwire. A route bound WITHOUT a
// legitimate claim (a bare SetRoute, standing in for a future silent-bind
// regression) must not be able to drain the queue via either destructive consume
// path; a real claim (tryClaim / recoverSession) confirms the route and lets both
// paths through.

// A legitimate claim via tryClaim confirms the route; a bare SetRoute does not.
func TestRouteConfirmed_SetByTryClaimNotBareSetRoute(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)

	// A bare SetRoute (no claim) leaves the tripwire armed — not confirmed.
	bare := &Stub{CLI: "claude", PID: 1}
	bare.SetRoute(&key)
	if bare.RouteConfirmed() {
		t.Fatal("bare SetRoute must NOT confirm the route (tripwire must stay armed)")
	}

	// A real claim through tryClaim confirms it.
	holder := &Stub{CLI: "claude", PID: 2, CWD: "/home/u/proj"}
	if !b.tryClaim(nil, holder, key, "c3", false /*steal*/, true /*replay: suppress welcome*/) {
		t.Fatal("tryClaim should succeed on a free key")
	}
	if !holder.RouteConfirmed() {
		t.Fatal("tryClaim must confirm the route")
	}
}

// The destructive Ack=true fetch is refused while the route is unconfirmed, then
// proceeds after a real claim confirms it.
func TestHandleFetchQueue_AckRefusedUntilRouteConfirmed(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 3; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}

	// Route bound but NOT confirmed (simulates a silent-bind regression).
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)
	if stub.RouteConfirmed() {
		t.Fatal("precondition: route should be unconfirmed")
	}

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", All: true, Ack: true})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if resp.Err == "" {
		t.Fatalf("Ack=true fetch on an unconfirmed route must return an Err; got %+v", resp)
	}
	if n, _ := b.Queue.Pending(qrk); n != 3 {
		t.Fatalf("refused fetch must consume nothing; pending=%d, want 3", n)
	}

	// Confirm the route (as a real claim would) — the fetch now proceeds and drains.
	stub.MarkRouteConfirmed()
	agentSide2, brokerSide2 := newConnPair(t)
	raw2, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "2", All: true, Ack: true})
	go b.handleFetchQueue(brokerSide2, stub, raw2)
	resp2 := readFetchResp(t, agentSide2)
	if resp2.Err != "" {
		t.Fatalf("confirmed-route fetch should succeed; got Err %q", resp2.Err)
	}
	if len(resp2.Messages) != 3 {
		t.Fatalf("confirmed-route fetch returned %d messages, want 3", len(resp2.Messages))
	}
	if n, _ := b.Queue.Pending(qrk); n != 0 {
		t.Fatalf("confirmed fetch must consume all; pending=%d, want 0", n)
	}
}

// The NON-destructive peek (Ack=false) is unaffected by the tripwire — it consumes
// nothing, so it needs no confirmed claim.
func TestHandleFetchQueue_PeekAllowedWhenRouteNotConfirmed(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 2; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key) // unconfirmed

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Limit: 2, Ack: false})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if resp.Err != "" {
		t.Fatalf("peek (Ack=false) must be allowed on an unconfirmed route; got Err %q", resp.Err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("peek returned %d messages, want 2", len(resp.Messages))
	}
	if n, _ := b.Queue.Pending(qrk); n != 2 {
		t.Fatalf("peek must not consume; pending=%d, want 2", n)
	}
}

// The live-push ack (handleInboundDelivered → JobConsume) is the OTHER destructive
// path: it must drop the consume while the route is unconfirmed, then consume once
// confirmed.
func TestHandleInboundDelivered_DropsConsumeUntilRouteConfirmed(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 3; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key) // unconfirmed

	// Ack covering 2 lines — must be DROPPED (route not confirmed).
	raw, _ := json.Marshal(ipc.InboundDeliveredMsg{Op: ipc.OpInboundDelivered, UpdateID: 2, OK: true, Count: 2})
	b.handleInboundDelivered(stub, raw)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n, _ := b.Queue.Pending(qrk); n != 3 {
			t.Fatalf("unconfirmed live-ack consumed backlog; pending dropped to %d, want 3", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Confirm the route — a re-ack now consumes off the head.
	stub.MarkRouteConfirmed()
	b.handleInboundDelivered(stub, raw)
	deadline = time.Now().Add(2 * time.Second)
	for {
		if n, _ := b.Queue.Pending(qrk); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			n, _ := b.Queue.Pending(qrk)
			t.Fatalf("confirmed live-ack(Count=2) should consume 2; pending=%d, want 1", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// handleRelease (detach) clears routeConfirmed — the tripwire re-arms, so a route
// bound again without a fresh claim is once more refused the destructive consume.
func TestHandleRelease_ClearsRouteConfirmed(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)
	stub.MarkRouteConfirmed()
	if !stub.RouteConfirmed() {
		t.Fatal("precondition: route should be confirmed")
	}

	b.handleRelease(stub)

	if stub.RouteConfirmed() {
		t.Fatal("handleRelease must clear routeConfirmed (tripwire must re-arm)")
	}
	if stub.CurrentRoute() != nil {
		t.Fatal("handleRelease must clear the route")
	}
}
