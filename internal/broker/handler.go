package broker

import (
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/karthikeyan5/c3/internal/ipc"
)

// HandleConn drives one adapter connection through its lifecycle. Owns the
// connection — closes it on return.
func (b *Broker) HandleConn(nc net.Conn) {
	conn := ipc.NewConn(nc)
	defer conn.Close()

	// Stage 1: hello.
	raw, err := conn.ReadFrame()
	if err != nil {
		return
	}
	op, err := ipc.PeekOp(raw)
	if err != nil || op != ipc.OpHello {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "expected hello first"})
		return
	}
	var hello ipc.HelloMsg
	if err := json.Unmarshal(raw, &hello); err != nil {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "malformed hello"})
		return
	}

	stub := b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, conn)
	defer b.Stubs.Unregister(stub.ConnID)
	defer b.Routes.ReleaseAllByConnID(stub.ConnID)

	ack := ipc.HelloAckMsg{Op: ipc.OpHelloAck, ConnID: stub.ConnID}
	if len(b.Mappings.Channels) == 0 {
		ack.NoConfig = true
	} else if _, ok := b.Mappings.LookupByCwd(hello.CWD); !ok {
		ack.NoMapping = true
	}
	if err := conn.WriteJSON(ack); err != nil {
		return
	}

	// Stage 2: dispatch loop.
	for {
		raw, err := conn.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// transient — let the connection close.
			}
			return
		}
		op, err := ipc.PeekOp(raw)
		if err != nil {
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: err.Error()})
			continue
		}
		switch op {
		case ipc.OpListTopics:
			b.handleListTopics(conn)
		case ipc.OpRelease:
			b.Routes.ReleaseAllByConnID(stub.ConnID)
		case ipc.OpBye:
			return
		default:
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "op not implemented in phase 3: " + string(op)})
		}
	}
}

func (b *Broker) handleListTopics(conn *ipc.Conn) {
	resp := ipc.TopicsListMsg{Op: ipc.OpTopicsList}
	for chanName, cc := range b.Mappings.Channels {
		for _, tp := range cc.Topics {
			entry := ipc.TopicEntry{
				Channel: chanName, ChatID: tp.ChatID,
				TopicID: tp.TopicID, Name: tp.Name, Group: tp.Group,
			}
			key := MakeRouteKey(chanName, tp.ChatID, ptrI64Val(tp.TopicID))
			if holder, ok := b.Routes.Holder(key); ok {
				entry.ClaimedBy = &ipc.Holder{CLI: holder.CLI, PID: holder.PID, CWD: holder.CWD}
			}
			resp.Topics = append(resp.Topics, entry)
		}
	}
	_ = conn.WriteJSON(resp)
}

func ptrI64Val(v int64) *int64 { return &v }
