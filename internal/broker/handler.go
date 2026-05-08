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
			stub.SetRoute(nil)
		case ipc.OpToolCall:
			b.handleToolCall(conn, stub, raw)
		case ipc.OpBye:
			return
		default:
			_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "op not implemented yet: " + string(op)})
		}
	}
}

// handleToolCall dispatches a tool-call to the worker for the stub's
// currently-claimed route. The result returns asynchronously via the worker's
// OutboundJob.ResultCh; we block this connection's read loop on it (which is
// fine because the writer mutex on the Conn allows other goroutines —
// inbound forwarding — to write concurrently).
func (b *Broker) handleToolCall(conn *ipc.Conn, stub *Stub, raw []byte) {
	var req ipc.ToolCallReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.ErrorMsg{Op: ipc.OpError, Err: "malformed tool_call: " + err.Error()})
		return
	}

	route := stub.CurrentRoute()
	if route == nil {
		_ = conn.WriteJSON(ipc.ToolResultMsg{
			Op: ipc.OpToolResult, ID: req.ID,
			Error: &ipc.ErrorPayload{Code: -32000, Message: "tool_call before attach: no route claimed"},
		})
		return
	}

	resultCh := make(chan OutboundResult, 1)
	job := Job{Kind: JobOutbound, Outbound: &OutboundJob{
		Tool:     req.Name,
		Args:     req.Args,
		ResultCh: resultCh,
	}}
	if !b.Workers.Submit(*route, job) {
		_ = conn.WriteJSON(ipc.ToolResultMsg{
			Op: ipc.OpToolResult, ID: req.ID,
			Error: &ipc.ErrorPayload{Code: -32000, Message: "worker queue full or stopped"},
		})
		return
	}

	res := <-resultCh
	resp := ipc.ToolResultMsg{Op: ipc.OpToolResult, ID: req.ID}
	if res.Err != nil {
		resp.Error = &ipc.ErrorPayload{Code: -32000, Message: res.Err.Error()}
	} else {
		resp.Result = res.Result
	}
	_ = conn.WriteJSON(resp)
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
