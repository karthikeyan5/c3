package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/queue"
)

func TestHandleCommand_StatusInTopic(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "x", Timestamp: time.Now().Add(-2 * time.Hour)})

	host := NewBrokerHost(b, "telegram")
	in := &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, Text: "/status"}
	reply, handled := host.HandleCommand(in)
	if !handled {
		t.Fatal("/status in a topic should be handled")
	}
	if !strings.Contains(reply, "1 queued") {
		t.Errorf("in-topic status = %q, want '1 queued'", reply)
	}
}

func TestHandleCommand_GlobalInDM(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid}
	_ = b.Queue.Append(qrk, &c3types.Inbound{Channel: "telegram", ChatID: -1001234567890, TopicID: &tid, MessageID: 1, Text: "x", Timestamp: time.Now()})

	host := NewBrokerHost(b, "telegram")
	in := &c3types.Inbound{Channel: "telegram", ChatID: 555, TopicID: nil, Text: "/status"} // DM
	reply, handled := host.HandleCommand(in)
	if !handled {
		t.Fatal("/status in DM should be handled")
	}
	if !strings.Contains(reply, "Broker up") {
		t.Errorf("global status = %q, want 'Broker up'", reply)
	}
}

func TestHandleCommand_NonStatusNotHandled(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := brokerWithChannel(t, mfWithTelegram(), &fakeChannel{})
	defer b.Shutdown()
	host := NewBrokerHost(b, "telegram")
	if _, handled := host.HandleCommand(&c3types.Inbound{Channel: "telegram", Text: "hello"}); handled {
		t.Error("non-command text must not be handled")
	}
}
