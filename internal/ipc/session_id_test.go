package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelloMsg_SessionIDRoundTrip(t *testing.T) {
	b, _ := json.Marshal(HelloMsg{Op: OpHello, CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"})
	if !strings.Contains(string(b), `"session_id":"sess-1"`) {
		t.Fatalf("missing session_id on wire: %s", b)
	}
	var m HelloMsg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.SessionID != "sess-1" {
		t.Fatalf("round-trip session_id = %q", m.SessionID)
	}
	// omitempty: empty id absent from the wire (older brokers unaffected).
	b2, _ := json.Marshal(HelloMsg{Op: OpHello})
	if strings.Contains(string(b2), "session_id") {
		t.Fatalf("empty session_id must be omitted: %s", b2)
	}
}

func TestHelloAckMsg_QueuedCount(t *testing.T) {
	b, _ := json.Marshal(HelloAckMsg{Op: OpHelloAck, QueuedCount: 3})
	if !strings.Contains(string(b), `"queued_count":3`) {
		t.Fatalf("missing queued_count: %s", b)
	}
	b2, _ := json.Marshal(HelloAckMsg{Op: OpHelloAck})
	if strings.Contains(string(b2), "queued_count") {
		t.Fatalf("zero queued_count must be omitted: %s", b2)
	}
}
