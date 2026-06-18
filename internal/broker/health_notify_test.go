package broker

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func brokerWithAgent(t *testing.T) (*Broker, *fakeNotifier, *ipc.Conn) {
	t.Helper()
	b := newTestBroker()
	fn := newFakeNotifier()
	b.desktopNotifier = fn
	agentSide, brokerSide := net.Pipe()
	t.Cleanup(func() { agentSide.Close(); brokerSide.Close() })
	agentConn := ipc.NewConn(agentSide)
	b.Stubs.Register("claude", 4242, "/work", ipc.NewConn(brokerSide))
	return b, fn, agentConn
}

// readBroadcastWithin returns (msg, true) if an InboundMsg arrives within d,
// else (zero, false). Used to assert both presence and ABSENCE of a broadcast.
func readBroadcastWithin(agentConn *ipc.Conn, d time.Duration) (ipc.InboundMsg, bool) {
	type rr struct {
		m   ipc.InboundMsg
		err error
	}
	ch := make(chan rr, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			ch <- rr{err: err}
			return
		}
		var m ipc.InboundMsg
		err = json.Unmarshal(raw, &m)
		ch <- rr{m: m, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return ipc.InboundMsg{}, false
		}
		return r.m, true
	case <-time.After(d):
		return ipc.InboundMsg{}, false
	}
}

func assertHealthFileState(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read health file: %v", err)
	}
	var got map[string]healthFileEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("health file invalid JSON: %v (%s)", err, data)
	}
	if got["telegram"].State != want {
		t.Errorf("health file telegram.state = %q, want %q", got["telegram"].State, want)
	}
}

func TestNotifyHealth_DesktopDelivered_NoCLIBroadcast(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = true
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked")
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired even though desktop delivered")
	}
	if b.lastHealthSnapshot()["telegram"].State != c3types.HealthStateDown {
		t.Error("status cache not set")
	}
	assertHealthFileState(t, hf, "down")
}

func TestNotifyHealth_DesktopUnavailable_CLIFallbackWithNote(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = false
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked")
	}
	msg, got := readBroadcastWithin(agentConn, 2*time.Second)
	if !got {
		t.Fatal("CLI fallback did not fire when desktop unavailable")
	}
	sys := msg.Inbound.Event.System
	if sys == nil || !strings.Contains(sys.Message, "desktop notification unavailable") {
		t.Errorf("fallback message missing note: %+v", sys)
	}
}

func TestNotifyHealth_Recovery_NoCLIBroadcast_FileSaysUp(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	fn.delivered = false // even "unavailable" desktop must not cause a recovery injection
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateUp, DownFor: 3 * time.Minute})
	select {
	case <-fn.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop notifier not invoked on recovery")
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired on recovery (must never)")
	}
	assertHealthFileState(t, hf, "up")
}

func TestNotifyHealth_InvasiveOff_NeitherButAmbientWritten(t *testing.T) {
	hf := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("C3_HEALTH_FILE", hf)
	b, fn, agentConn := brokerWithAgent(t)
	off := false
	b.SetMappings(&mappings.MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]mappings.ChannelConfig{},
		Mappings:      map[string]mappings.Mapping{},
		Notifications: &mappings.NotificationsConfig{Invasive: &off},
	})
	host := NewBrokerHost(b, "telegram")
	host.NotifyHealth(c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3})
	select {
	case <-fn.ch:
		t.Fatal("desktop notifier invoked despite invasive:false")
	case <-time.After(300 * time.Millisecond):
	}
	if _, got := readBroadcastWithin(agentConn, 300*time.Millisecond); got {
		t.Fatal("CLI broadcast fired despite invasive:false")
	}
	if b.lastHealthSnapshot()["telegram"].State != c3types.HealthStateDown {
		t.Error("status cache not set under invasive:false")
	}
	assertHealthFileState(t, hf, "down")
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
