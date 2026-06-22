package broker

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/queue"
)

func TestHandleFetchQueue_ConsumesOldest(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 4; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	// Stub holding the route. claimedHolder calls Routes.Claim but does NOT set
	// the stub's CurrentRoute; handleFetchQueue resolves the route via
	// stub.CurrentRoute(), so set it explicitly (mirroring the retranscribe test
	// below) — otherwise the handler returns the no-route Err branch and the
	// assertions below fail.
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	agentSide, brokerSide := newConnPair(t)
	_ = brokerSide
	req := ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Limit: 2, Ack: true}
	raw, _ := json.Marshal(req)
	go b.handleFetchQueue(brokerSide, stub, raw)

	resp := readFetchResp(t, agentSide)
	if len(resp.Messages) != 2 || resp.Messages[0].MessageID != 1 {
		t.Fatalf("fetch_queue returned %+v, want 2 oldest", resp.Messages)
	}
	if resp.Remaining != 2 {
		t.Fatalf("remaining = %d, want 2", resp.Remaining)
	}
	if n, _ := b.Queue.Pending(qrk); n != 2 {
		t.Fatalf("ack=true should consume; pending=%d, want 2", n)
	}
}

func TestHandleFetchQueue_NoRoute(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	stub := &Stub{CLI: "claude"} // no route claimed
	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Ack: true})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if resp.Err == "" {
		t.Fatal("fetch_queue before attach should return an Err")
	}
}

// ack=false PEEKS: returns the oldest batch WITHOUT advancing the cursor, and
// Remaining reflects what is still queued after this (non-consuming) batch.
func TestHandleFetchQueue_PeekDoesNotConsume(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 4; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "1", Limit: 2, Ack: false})
	go b.handleFetchQueue(brokerSide, stub, raw)
	resp := readFetchResp(t, agentSide)
	if len(resp.Messages) != 2 || resp.Messages[0].MessageID != 1 {
		t.Fatalf("peek returned %+v, want 2 oldest", resp.Messages)
	}
	if resp.Remaining != 2 {
		t.Fatalf("peek remaining = %d, want 2 (after this non-consuming batch of 2)", resp.Remaining)
	}
	if n, _ := b.Queue.Pending(qrk); n != 4 {
		t.Fatalf("ack=false must NOT consume; pending=%d, want 4", n)
	}
}

func TestHandleRetranscribe_ReRunsSTT(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	b.Plugins.OnVoiceReceived(func(_ context.Context, p c3types.VoicePayload) (string, error) {
		if p.FileID == "vf" {
			return "fresh transcript", nil
		}
		return "", nil
	})
	stub := &Stub{CLI: "claude"}
	stub.SetRoute(&RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 914})

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: "1", FileID: "vf"})
	go b.handleRetranscribe(brokerSide, stub, raw)
	resp := readRetranscribeResp(t, agentSide)
	if resp.Text != "fresh transcript" {
		t.Fatalf("retranscribe text = %q, want 'fresh transcript'", resp.Text)
	}
}

// message_id with NO matching queued message is a clean queue no-op: it must not
// error and must still return the fresh transcript. (The in-place refresh only
// fires when the message is still queued; here nothing is queued.)
func TestHandleRetranscribe_AbsentMessageIDStillReturnsTranscript(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	b.Plugins.OnVoiceReceived(func(_ context.Context, p c3types.VoicePayload) (string, error) {
		return "fresh transcript", nil
	})
	stub := &Stub{CLI: "claude"}
	stub.SetRoute(&RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 914})

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: "2", FileID: "vf", MessageID: 999})
	go b.handleRetranscribe(brokerSide, stub, raw)
	resp := readRetranscribeResp(t, agentSide)
	if resp.Err != "" {
		t.Fatalf("retranscribe with absent message_id should not error; got %q", resp.Err)
	}
	if resp.Text != "fresh transcript" {
		t.Fatalf("retranscribe text = %q, want 'fresh transcript' (absent message_id is a queue no-op)", resp.Text)
	}
}

// Per spec Component 5: when message_id is given AND that message is still queued
// for the route, retranscribe refreshes its stored Text in place so a subsequent
// fetch_queue returns the corrected transcript (not the old STT-failure
// placeholder).
func TestHandleRetranscribe_RefreshesQueuedMessageInPlace(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	b.Plugins.OnVoiceReceived(func(_ context.Context, p c3types.VoicePayload) (string, error) {
		return "fresh transcript", nil
	})

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	// A queued voice message whose stored Text is an STT-failure placeholder.
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: 5, Text: "[STT FAILED]", Timestamp: time.Now()})
	// A second queued message that must be left untouched.
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: 6, Text: "other", Timestamp: time.Now()})

	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	agentSide, brokerSide := newConnPair(t)
	raw, _ := json.Marshal(ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: "3", FileID: "vf", MessageID: 5})
	go b.handleRetranscribe(brokerSide, stub, raw)
	resp := readRetranscribeResp(t, agentSide)
	if resp.Err != "" {
		t.Fatalf("retranscribe should not error; got %q", resp.Err)
	}
	if resp.Text != "fresh transcript" {
		t.Fatalf("retranscribe text = %q, want 'fresh transcript'", resp.Text)
	}

	// A subsequent fetch_queue (peek) must return the refreshed text for msg 5,
	// and the untouched text for msg 6.
	fraw, _ := json.Marshal(ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: "4", All: true, Ack: false})
	go b.handleFetchQueue(brokerSide, stub, fraw)
	fresp := readFetchResp(t, agentSide)
	if len(fresp.Messages) != 2 {
		t.Fatalf("fetch_queue returned %d messages, want 2", len(fresp.Messages))
	}
	for _, m := range fresp.Messages {
		switch m.MessageID {
		case 5:
			if m.Text != "fresh transcript" {
				t.Fatalf("queued msg 5 text = %q, want refreshed 'fresh transcript'", m.Text)
			}
		case 6:
			if m.Text != "other" {
				t.Fatalf("queued msg 6 text = %q, want untouched 'other'", m.Text)
			}
		default:
			t.Fatalf("unexpected queued message id %d", m.MessageID)
		}
	}
}

func TestHandleInboundDelivered_MergedBatchConsumesAllCovered(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 5; i++ {
		_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i, Text: "m", Timestamp: time.Now()})
	}
	// b.Workers.Submit lazily spawns the route worker (WorkerPool.Submit), so the
	// JobConsume the handler submits runs on a live worker — no manual worker
	// setup needed.
	stub := claimedHolder(t, b, key)
	stub.SetRoute(&key)

	// A merged push of 3 lines, acked once with Count=3.
	raw, _ := json.Marshal(ipc.InboundDeliveredMsg{Op: ipc.OpInboundDelivered, UpdateID: 3, OK: true, Count: 3})
	b.handleInboundDelivered(stub, raw)

	// Poll until the async JobConsume drains 3 (oldest 1,2,3), leaving 4,5.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := b.Queue.Pending(qrk); n == 2 {
			break
		}
		if time.Now().After(deadline) {
			n, _ := b.Queue.Pending(qrk)
			t.Fatalf("merged ack(Count=3) should consume 3; pending=%d, want 2", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := b.Queue.Peek(qrk, 5)
	if len(got) != 2 || got[0].MessageID != 4 {
		t.Fatalf("after merged ack, head=%+v, want msgs 4,5", got)
	}
}

func newConnPair(t *testing.T) (agent, broker *ipc.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return ipc.NewConn(a), ipc.NewConn(b)
}

func readFetchResp(t *testing.T, c *ipc.Conn) ipc.FetchQueueResp {
	t.Helper()
	raw, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("read fetch resp: %v", err)
	}
	var r ipc.FetchQueueResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func readRetranscribeResp(t *testing.T, c *ipc.Conn) ipc.RetranscribeResp {
	t.Helper()
	raw, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("read retranscribe resp: %v", err)
	}
	var r ipc.RetranscribeResp
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
