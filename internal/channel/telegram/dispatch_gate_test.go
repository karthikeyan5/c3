package telegram

import (
	"sync"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// fakeHost is a test double for channel.Host. Records every call so the
// dispatch-gate tests can assert "Emit was never called when gate dropped".
type fakeHost struct {
	mu       sync.Mutex
	emitted  []*c3types.Inbound
	decision channel.GateInboundDecision
	logs     []string
}

func (h *fakeHost) Config(name string, target any) error { return nil }

func (h *fakeHost) Emit(in *c3types.Inbound) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitted = append(h.emitted, in)
}

func (h *fakeHost) Logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = append(h.logs, format)
}

func (h *fakeHost) Done() <-chan struct{} { return nil }

func (h *fakeHost) GateInbound(in *c3types.Inbound) channel.GateInboundDecision {
	return h.decision
}

func (h *fakeHost) emitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.emitted)
}

func makeChannel(host channel.Host) *Channel {
	return &Channel{host: host, cfg: Config{}}
}

func textMsg(text string, userID int64) *gotgbot.Message {
	return &gotgbot.Message{
		MessageId: 100,
		From:      &gotgbot.User{Id: userID},
		Chat:      gotgbot.Chat{Id: userID}, // positive = DM
		Date:      1715151931,
		Text:      text,
	}
}

func TestDispatchMessage_GateDrop_DoesNotEmit(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundDrop}
	c := makeChannel(h)
	c.dispatchMessage(1, textMsg("stranger", 99), false)
	if got := h.emitCount(); got != 0 {
		t.Errorf("Emit called %d times for dropped inbound; want 0", got)
	}
}

func TestDispatchMessage_GatePairConsumed_DoesNotEmit(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundPairConsumed}
	c := makeChannel(h)
	c.dispatchMessage(1, textMsg("5829", 99), false)
	if got := h.emitCount(); got != 0 {
		t.Errorf("Emit called %d times after pair-consumed; want 0 (control-plane signal, not content)", got)
	}
}

func TestDispatchMessage_GateAllow_Emits(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannel(h)
	c.dispatchMessage(1, textMsg("hi", 42), false)
	if got := h.emitCount(); got != 1 {
		t.Errorf("Emit called %d times for allowed inbound; want 1", got)
	}
}
