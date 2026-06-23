package main

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/ipc"
)

func TestBuildInstructions_RecoveryWithBacklog(t *testing.T) {
	a := &adapter{helloAck: ipc.HelloAckMsg{
		AutoAttached: true,
		Mapping:      &ipc.Mapping{Name: "c3", Channel: "telegram"},
		QueuedCount:  2,
	}}
	out := a.buildInstructions()
	if !strings.Contains(out, `Auto-attached to "c3"`) {
		t.Fatalf("missing auto-attach line: %q", out)
	}
	if !strings.Contains(out, "2 messages held") || !strings.Contains(out, "fetch_queue") {
		t.Fatalf("missing backlog nudge: %q", out)
	}
}

func TestBuildInstructions_RecoveryNoBacklog(t *testing.T) {
	a := &adapter{helloAck: ipc.HelloAckMsg{
		AutoAttached: true,
		Mapping:      &ipc.Mapping{Name: "c3", Channel: "telegram"},
		QueuedCount:  0,
	}}
	out := a.buildInstructions()
	if !strings.Contains(out, `Auto-attached to "c3"`) {
		t.Fatalf("missing auto-attach line: %q", out)
	}
	if strings.Contains(out, "held while detached") {
		t.Fatalf("must NOT show the backlog clause when count==0: %q", out)
	}
}

func TestBuildInstructions_SingularMessage(t *testing.T) {
	a := &adapter{helloAck: ipc.HelloAckMsg{
		AutoAttached: true, Mapping: &ipc.Mapping{Name: "c3", Channel: "telegram"}, QueuedCount: 1,
	}}
	out := a.buildInstructions()
	if !strings.Contains(out, "1 message held") {
		t.Fatalf("expected singular '1 message held': %q", out)
	}
}

func TestSessionIDFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-abc")
	if got := sessionIDFromEnv(); got != "sess-abc" {
		t.Fatalf("sessionIDFromEnv = %q, want sess-abc", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if got := sessionIDFromEnv(); got != "" {
		t.Fatalf("unset session id should be empty, got %q", got)
	}
}
