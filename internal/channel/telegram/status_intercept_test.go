package telegram

import (
	"testing"

	"github.com/karthikeyan5/c3/internal/channel"
)

func TestIsStatusCommand(t *testing.T) {
	c := &Channel{}
	cases := map[string]bool{
		"/status":        true,
		" /status ":      true,
		"/STATUS":        true,
		"/status@c3bot":  true,
		"/statusly":      false,
		"hello":          false,
		"please /status": false,
	}
	for text, want := range cases {
		if got := c.isStatusCommand(text); got != want {
			t.Errorf("isStatusCommand(%q) = %v, want %v", text, got, want)
		}
	}
}

// Spec invariant: an intercepted "/status" must NEVER be Emitted (routed to an
// agent) — the broker handles it and the channel returns early. Drive
// dispatchMessage directly the same way the existing dispatch_gate_test.go cases
// do (hand-built *gotgbot.Message), with a fakeHost whose HandleCommand claims
// "/status". The recover guards the intercept's SendReply call (the bare
// makeChannel has a nil bot); the assertion that matters is Emit==0.
func TestStatusCommand_NotRouted(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow, cmdHandled: true}
	c := makeChannel(h)
	func() {
		defer func() { _ = recover() }() // tolerate nil-bot SendReply in the intercept
		c.dispatchMessage(1, textMsg("/status", 42), false, nil)
	}()
	if got := h.emitCount(); got != 0 {
		t.Errorf("/status must not be routed: Emit called %d times, want 0", got)
	}
}
