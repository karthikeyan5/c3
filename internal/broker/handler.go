package broker

import (
	"encoding/json"
	"errors"
	"io"
	"log"
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

	// Reconnect detection: if we have a disconnected stub for the same
	// (CLI, PID, CWD), this is the same adapter coming back after a brief
	// drop. Transfer its claims to a fresh ConnID instead of registering a
	// new stub and racing the original's claims. Per the "broker is the
	// authority" principle, claims survive conn drops as long as PID lives.
	var stub *Stub
	if existing := b.Routes.FindByLogicalSession(hello.CLI, hello.PID, hello.CWD); existing != nil {
		stub = b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, conn)
		oldConnID := existing.ConnID
		// Unregister the OLD stub (now superseded) and transfer its claims.
		b.Stubs.Unregister(oldConnID)
		b.Routes.TransferAllByConnID(oldConnID, stub)
		log.Printf("hello: RECONNECT cli=%s pid=%d cwd=%q old-conn=%d new-conn=%d (claims transferred)",
			hello.CLI, hello.PID, hello.CWD, oldConnID, stub.ConnID)
	} else {
		stub = b.Stubs.Register(hello.CLI, hello.PID, hello.CWD, conn)
		log.Printf("hello: NEW cli=%s pid=%d cwd=%q conn=%d",
			hello.CLI, hello.PID, hello.CWD, stub.ConnID)
	}

	// Defer: mark the stub as disconnected and decide whether to release
	// its claims based on PID liveness. The "claims preserved while PID
	// alive" rule covers the common case where the adapter is briefly
	// reconnecting (network blip, broker bounce). When the PID is
	// already dead at conn-drop time — e.g. Claude Code killed the
	// adapter for /mcp reconnect, the user quit the CLI — preserving
	// the claim would only block fallback delivery and future attaches
	// from competing PIDs.
	//
	// Defense-in-depth: forwardOrFallback also checks IsAlive on every
	// dispatch and releases dead-holder claims (kernel may not reap the
	// process by the time this defer runs).
	defer func() {
		stub.MarkDisconnected()
		if isPIDAlive(stub.PID) {
			log.Printf("conn-drop: cli=%s pid=%d cwd=%q conn=%d (claims preserved while pid alive)",
				stub.CLI, stub.PID, stub.CWD, stub.ConnID)
			return
		}
		released := b.Routes.ReleaseAllByConnID(stub.ConnID)
		log.Printf("conn-drop: cli=%s pid=%d cwd=%q conn=%d (PID dead — released %d claim(s))",
			stub.CLI, stub.PID, stub.CWD, stub.ConnID, len(released))
	}()
	defer b.Stubs.Unregister(stub.ConnID)

	ack := ipc.HelloAckMsg{Op: ipc.OpHelloAck, ConnID: stub.ConnID}
	if len(b.Mappings().Channels) == 0 {
		ack.NoConfig = true
	} else if _, ok := b.Mappings().LookupByCwd(hello.CWD); !ok {
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
		case ipc.OpAttach:
			b.handleAttach(conn, stub, raw)
		case ipc.OpListTopics:
			b.handleListTopics(conn)
		case ipc.OpListClaims:
			b.handleListClaims(conn)
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
	for chanName, cc := range b.Mappings().Channels {
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

// handleListClaims returns a snapshot of every live route claim. Used by
// `c3-broker status` (transient client) to render the live-claims section
// without dropping the apologetic note we used to ship.
func (b *Broker) handleListClaims(conn *ipc.Conn) {
	resp := ipc.ClaimsListMsg{Op: ipc.OpClaimsList}
	for _, e := range b.Routes.Snapshot() {
		entry := ipc.ClaimEntry{
			Channel:   e.Key.Channel,
			ChatID:    e.Key.ChatID,
			HasTopic:  e.Key.HasTopic,
			TopicID:   e.Key.TopicID,
			HolderCLI: e.Stub.CLI,
			HolderPID: e.Stub.PID,
			HolderCWD: e.Stub.CWD,
			ConnID:    e.Stub.ConnID,
			Connected: e.Stub.IsConnected(),
		}
		if e.Key.HasTopic {
			if tp, ok := b.Mappings().LookupTopicByID(e.Key.Channel, e.Key.ChatID, e.Key.TopicID); ok {
				entry.TopicName = tp.Name
				entry.GroupName = tp.Group
			}
		}
		resp.Claims = append(resp.Claims, entry)
	}
	_ = conn.WriteJSON(resp)
}

func ptrI64Val(v int64) *int64 { return &v }
