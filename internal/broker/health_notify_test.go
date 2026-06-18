package broker

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// fakeNotifier records desktop-notify calls instead of spawning a real popup,
// and reports a controllable delivered result.
type fakeNotifier struct {
	ch        chan c3types.HealthEvent
	delivered bool
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{ch: make(chan c3types.HealthEvent, 4), delivered: true}
}

func (f *fakeNotifier) Notify(ev c3types.HealthEvent) bool {
	f.ch <- ev
	return f.delivered
}

func newTestBroker() *Broker {
	return New(&mappings.MappingsFile{SchemaVersion: 1, Channels: map[string]mappings.ChannelConfig{}, Mappings: map[string]mappings.Mapping{}})
}

// TestNotifyHealth_FanOut asserts the four-sink fan-out: (a) the desktop
// notifier is invoked, (b) the system advisory is broadcast to the live CLI
// session conn (and ONLY there — not to a transient client or a disconnected
// stub), and (c) the status cache records the event.
func TestNotifyHealth_FanOut(t *testing.T) {
	b := newTestBroker()
	fn := newFakeNotifier()
	b.desktopNotifier = fn

	// A live agent CLI session with a real conn (net.Pipe).
	agentSide, brokerSide := net.Pipe()
	defer agentSide.Close()
	defer brokerSide.Close()
	agentConn := ipc.NewConn(agentSide)
	b.Stubs.Register("claude", 4242, "/work", ipc.NewConn(brokerSide))

	// A transient status-client stub — must be SKIPPED by the broadcast.
	b.Stubs.Register("c3-broker-cli", 9999, "/tmp", struct{}{})

	// A disconnected stub — must be SKIPPED (no live conn).
	dead := b.Stubs.Register("codex", 7, "/dead", nil)
	dead.MarkDisconnected()

	host := NewBrokerHost(b, "telegram")
	ev := c3types.HealthEvent{
		Channel: "telegram",
		State:   c3types.HealthStateDown,
		Since:   time.Now(),
		Consec:  3,
		Reason:  "transient (network/timeout/5xx)",
	}

	// Read the broadcast frame off the agent side concurrently (the broadcast
	// runs in its own goroutine inside NotifyHealth).
	type readResult struct {
		msg ipc.InboundMsg
		err error
	}
	got := make(chan readResult, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			got <- readResult{err: err}
			return
		}
		var m ipc.InboundMsg
		err = json.Unmarshal(raw, &m)
		got <- readResult{msg: m, err: err}
	}()

	host.NotifyHealth(ev)

	// (c) status cache set synchronously.
	snap := b.lastHealthSnapshot()
	if cached, ok := snap["telegram"]; !ok || cached.State != c3types.HealthStateDown {
		t.Fatalf("status cache: got %+v ok=%v, want a DOWN telegram entry", cached, ok)
	}

	// (a) desktop notifier invoked.
	select {
	case ne := <-fn.ch:
		if ne.Channel != "telegram" || ne.State != c3types.HealthStateDown {
			t.Fatalf("desktop notify got %+v, want DOWN telegram", ne)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier was not invoked")
	}

	// (b) broadcast delivered to the live agent CLI session only.
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("agent read broadcast frame: %v", r.err)
		}
		if r.msg.Op != ipc.OpInbound {
			t.Fatalf("broadcast op = %s, want %s", r.msg.Op, ipc.OpInbound)
		}
		in := r.msg.Inbound
		if in.Kind != c3types.InboundSystem {
			t.Fatalf("broadcast Kind = %q, want %q", in.Kind, c3types.InboundSystem)
		}
		if in.Event == nil || in.Event.System == nil {
			t.Fatalf("broadcast missing System event payload: %+v", in.Event)
		}
		if in.Event.System.Level != "warn" {
			t.Fatalf("system event Level = %q, want warn", in.Event.System.Level)
		}
		if in.Event.System.Message == "" {
			t.Fatalf("system event Message empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast was not delivered to the live agent CLI session")
	}
}

// TestBroadcastSystemEvent_GateBypassIsBrokerOriginated documents + asserts the
// security boundary: the broadcast is a broker-originated InboundSystem event
// that intentionally does NOT pass through the allowlist gate. We assert it is
// delivered even though no allowlist is configured (a user message would be
// dropped), and that it carries no user content (ChatID/Sender zero).
func TestBroadcastSystemEvent_GateBypassIsBrokerOriginated(t *testing.T) {
	b := newTestBroker()
	// No allowlist at all — a user inbound would be default-denied.

	agentSide, brokerSide := net.Pipe()
	defer agentSide.Close()
	defer brokerSide.Close()
	agentConn := ipc.NewConn(agentSide)
	b.Stubs.Register("claude", 1, "/w", ipc.NewConn(brokerSide))

	got := make(chan ipc.InboundMsg, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			return
		}
		var m ipc.InboundMsg
		_ = json.Unmarshal(raw, &m)
		got <- m
	}()

	b.broadcastSystemEvent(&c3types.SystemEvent{
		Source:  "telegram",
		Level:   "warn",
		Title:   "telegram fetch DOWN",
		Message: "Cannot reach telegram.",
	})

	select {
	case m := <-got:
		in := m.Inbound
		if in.Kind != c3types.InboundSystem {
			t.Fatalf("Kind = %q, want system", in.Kind)
		}
		// No user content rode along on this broker-originated event.
		if in.ChatID != 0 || in.Sender.UserID != 0 || in.Text != "" {
			t.Fatalf("broker-originated event must carry no user content, got chat=%d sender=%d text=%q",
				in.ChatID, in.Sender.UserID, in.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker-originated system event was not delivered (gate bypass failed)")
	}
}
