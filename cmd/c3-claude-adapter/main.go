// c3-claude-adapter is the Claude Code MCP server that bridges Claude Code's
// MCP stdio protocol to the C3 broker over /tmp/c3.sock (or
// $XDG_RUNTIME_DIR/c3.sock).
//
// Spec §4.4. The adapter:
//
//  1. On stdin: accept JSON-RPC 2.0 requests from Claude Code (initialize,
//     tools/list, tools/call, ping, notifications/initialized).
//  2. On the broker socket: connect, send hello, listen for inbound /
//     tool_result / topics_list frames asynchronously.
//  3. For tools/call: route adapter-local tools (`attach`, `topics`)
//     directly; forward all other tools to the broker as ipc.OpToolCall and
//     return the result.
//  4. For broker-side ipc.OpInbound frames: emit `notifications/claude/channel`
//     manually framed JSON-RPC over the same stdout the MCP server uses
//     (writer-mutex shared via mcp.Server.Notify).
//
// Reconnect-once policy: if the broker socket drops, attempt one reconnect +
// re-handshake before bubbling errors to in-flight tool callers. This is
// captured in spec §4.4 "reconnect once" semantics.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mcp"
)

const (
	mcpProtocolVersion = "2024-11-05"
	adapterName        = "c3-claude-adapter"
	adapterVersion     = "0.1.0"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-claude-adapter: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	a := newAdapter()
	if err := a.connectBroker(); err != nil {
		return fmt.Errorf("connect broker: %w", err)
	}
	if err := a.hello(); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	mcpSrv := mcp.New(os.Stdin, os.Stdout, a)
	a.mcp = mcpSrv

	go a.brokerReader()
	return mcpSrv.Run(context.Background())
}

type adapter struct {
	mcp *mcp.Server

	// Broker connection state.
	bmu    sync.Mutex
	conn   *ipc.Conn
	connID uint64

	// Pending tool calls awaiting broker response, keyed by request id.
	pmu     sync.Mutex
	pending map[string]chan ipc.ToolResultMsg
	nextID  atomic.Uint64

	// Hello-ack response state, captured on connect.
	helloAck ipc.HelloAckMsg
}

func newAdapter() *adapter {
	return &adapter{
		pending: map[string]chan ipc.ToolResultMsg{},
	}
}

// connectBroker dials the broker socket, spawning the broker if unreachable.
func (a *adapter) connectBroker() error {
	sockPath := broker.SocketPath()
	for attempt := 0; attempt < 50; attempt++ { // ~10s with 200ms sleep
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			a.bmu.Lock()
			a.conn = ipc.NewConn(c)
			a.bmu.Unlock()
			return nil
		}
		if attempt == 0 {
			// First failure: spawn a broker, then retry.
			_ = spawnBroker()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("could not reach broker at %s after 10s", sockPath)
}

// spawnBroker forks a `c3-broker` process detached from our process group so
// it survives our shutdown.
func spawnBroker() error {
	cmd := exec.Command("c3-broker")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = sysSetsid()
	return cmd.Start()
}

// hello sends the hello frame and reads hello_ack.
func (a *adapter) hello() error {
	cwd, _ := os.Getwd()
	if err := a.conn.WriteJSON(ipc.HelloMsg{
		Op: ipc.OpHello, CLI: "claude", PID: os.Getpid(), CWD: cwd,
		Capabilities: []string{"claude/channel"},
	}); err != nil {
		return err
	}
	raw, err := a.conn.ReadFrame()
	if err != nil {
		return err
	}
	var ack ipc.HelloAckMsg
	if err := json.Unmarshal(raw, &ack); err != nil {
		return err
	}
	a.helloAck = ack
	a.connID = ack.ConnID
	return nil
}

// brokerReader runs in a goroutine, draining frames from the broker. On
// read error, attempts ONE reconnect before giving up. Pending tool calls
// are woken with an error during the reconnect window so callers don't
// hang. Spec §4.4 "reconnect once" semantics.
func (a *adapter) brokerReader() {
	reconnected := false
	for {
		conn := a.currentConn()
		if conn == nil {
			return
		}
		raw, err := conn.ReadFrame()
		if err != nil {
			if !reconnected {
				reconnected = true
				fmt.Fprintf(os.Stderr, "c3-claude-adapter: broker read err: %v — reconnecting once\n", err)
				if rerr := a.reconnectBroker(); rerr != nil {
					fmt.Fprintf(os.Stderr, "c3-claude-adapter: reconnect failed: %v\n", rerr)
					a.wakePendingWithErr("broker disconnected: " + err.Error())
					return
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "c3-claude-adapter: broker read err after reconnect: %v\n", err)
			a.wakePendingWithErr("broker disconnected after retry")
			return
		}
		op, err := ipc.PeekOp(raw)
		if err != nil {
			continue
		}
		switch op {
		case ipc.OpInbound:
			a.handleInbound(raw)
		case ipc.OpToolResult:
			a.dispatchToolResult(raw)
		case ipc.OpAttached:
			a.dispatchAttached(raw)
		case ipc.OpTopicsList:
			a.dispatchTopicsList(raw)
		case ipc.OpError:
			var errMsg ipc.ErrorMsg
			_ = json.Unmarshal(raw, &errMsg)
			fmt.Fprintf(os.Stderr, "c3-claude-adapter: broker error: %s\n", errMsg.Err)
		}
	}
}

// reconnectBroker tears down the dead conn, dials a fresh one, sends hello.
// Pending tool calls are woken with an error so callers don't hang during
// the reconnect window.
func (a *adapter) reconnectBroker() error {
	a.wakePendingWithErr("broker reconnect — request canceled")

	a.bmu.Lock()
	if a.conn != nil {
		_ = a.conn.Close()
		a.conn = nil
	}
	a.bmu.Unlock()

	if err := a.connectBroker(); err != nil {
		return err
	}
	return a.hello()
}

// wakePendingWithErr resolves every pending entry with an error.
func (a *adapter) wakePendingWithErr(msg string) {
	a.pmu.Lock()
	pending := a.pending
	a.pending = map[string]chan ipc.ToolResultMsg{}
	a.pmu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- ipc.ToolResultMsg{Error: &ipc.ErrorPayload{Code: -32000, Message: msg}}:
		default:
		}
	}
}

func (a *adapter) currentConn() *ipc.Conn {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return a.conn
}

// handleInbound translates an ipc.OpInbound into notifications/claude/channel.
func (a *adapter) handleInbound(raw []byte) {
	var in ipc.InboundMsg
	if err := json.Unmarshal(raw, &in); err != nil {
		return
	}
	frame := buildClaudeChannelFrame(&in.Inbound)
	_ = a.mcp.Notify("notifications/claude/channel", frame)
}

// buildClaudeChannelFrame converts a c3types.Inbound into the params for
// notifications/claude/channel. All meta values are stringified per spec §6.1.
func buildClaudeChannelFrame(in *c3types.Inbound) map[string]any {
	meta := map[string]any{
		"source":     in.Channel,
		"chat_id":    strconv.FormatInt(in.ChatID, 10),
		"message_id": strconv.FormatInt(in.MessageID, 10),
		"ts":         in.Timestamp.Format("2006-01-02T15:04:05.000Z"),
	}
	if in.Sender.Username != "" {
		meta["user"] = in.Sender.Username
	}
	if in.Sender.UserID != 0 {
		meta["user_id"] = strconv.FormatInt(in.Sender.UserID, 10)
	}
	if in.TopicID != nil {
		meta["message_thread_id"] = strconv.FormatInt(*in.TopicID, 10)
	}
	if in.ReplyTo != nil {
		meta["reply_to_message_id"] = strconv.FormatInt(in.ReplyTo.MessageID, 10)
		if in.ReplyTo.User.Username != "" {
			meta["reply_to_user"] = in.ReplyTo.User.Username
		} else if in.ReplyTo.User.UserID != 0 {
			meta["reply_to_user"] = strconv.FormatInt(in.ReplyTo.User.UserID, 10)
		}
		if in.ReplyTo.Text != "" {
			meta["reply_to_text"] = in.ReplyTo.Text
		}
	}
	if len(in.Attachments) > 0 {
		att := in.Attachments[0]
		if att.Kind != "" {
			meta["attachment_kind"] = att.Kind
		}
		if att.FileID != "" {
			meta["attachment_file_id"] = att.FileID
		}
		if att.Size > 0 {
			meta["attachment_size"] = strconv.FormatInt(att.Size, 10)
		}
		if att.MIME != "" {
			meta["attachment_mime"] = att.MIME
		}
	}

	text := in.Text
	if text == "" && len(in.Attachments) > 0 {
		// Channel may have left text empty for voice (STT plugin not yet
		// substituting). Fall back to a kind-based label so the agent at
		// least sees something.
		text = fmt.Sprintf("(%s message)", in.Attachments[0].Kind)
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"meta": meta,
	}
}

// dispatchToolResult routes the result to the waiting caller.
func (a *adapter) dispatchToolResult(raw []byte) {
	var msg ipc.ToolResultMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	a.pmu.Lock()
	ch, ok := a.pending[msg.ID]
	if ok {
		delete(a.pending, msg.ID)
	}
	a.pmu.Unlock()
	if ok {
		ch <- msg
	}
}

// dispatchAttached / dispatchTopicsList are routed via the same pending map
// using fixed keys ("attached", "topics_list") since at most one of each is
// in flight at a time per adapter (attach is synchronous from the agent's
// perspective).
func (a *adapter) dispatchAttached(raw []byte) {
	a.pmu.Lock()
	ch, ok := a.pending["attached"]
	if ok {
		delete(a.pending, "attached")
	}
	a.pmu.Unlock()
	if ok {
		// Route attached as a fake ToolResultMsg.Result with the raw payload
		// preserved under "_attached".
		var attached ipc.AttachedMsg
		_ = json.Unmarshal(raw, &attached)
		ch <- ipc.ToolResultMsg{Result: map[string]any{"_attached": attached}}
	}
}

func (a *adapter) dispatchTopicsList(raw []byte) {
	a.pmu.Lock()
	ch, ok := a.pending["topics_list"]
	if ok {
		delete(a.pending, "topics_list")
	}
	a.pmu.Unlock()
	if ok {
		var list ipc.TopicsListMsg
		_ = json.Unmarshal(raw, &list)
		ch <- ipc.ToolResultMsg{Result: map[string]any{"_topics_list": list}}
	}
}

// ─── MCP dispatch ───────────────────────────────────────────────────────────

// Dispatch implements mcp.Handler.
func (a *adapter) Dispatch(ctx context.Context, req *mcp.Request) *mcp.Response {
	switch req.Method {
	case "initialize":
		return a.initializeResponse(req)
	case "notifications/initialized":
		return nil
	case "tools/list":
		return a.toolsListResponse(req)
	case "tools/call":
		return a.toolsCallResponse(ctx, req)
	case "ping":
		return &mcp.Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		if req.IsNotification() {
			return nil
		}
		return &mcp.Response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &mcp.Error{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (a *adapter) initializeResponse(req *mcp.Request) *mcp.Response {
	instructions := a.buildInstructions()
	return &mcp.Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
				"experimental": map[string]any{
					"claude/channel":            map[string]any{},
					"claude/channel/permission": map[string]any{},
				},
			},
			"serverInfo": map[string]any{
				"name":    adapterName,
				"version": adapterVersion,
			},
			"instructions": instructions,
		},
	}
}

func (a *adapter) buildInstructions() string {
	switch {
	case a.helloAck.NoConfig:
		return "C3 not yet configured. Run `/c3-setup` (or `c3-broker setup`) to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart this session."
	case a.helloAck.NoMapping:
		cwd, _ := os.Getwd()
		return fmt.Sprintf("No C3 mapping for %q. Type `attach` to set one up — broker proposes a topic named %q in the default group; confirm to create.", cwd, filepath.Base(cwd))
	case a.helloAck.AutoAttached && a.helloAck.Mapping != nil:
		m := a.helloAck.Mapping
		return fmt.Sprintf("Auto-attached to %q (%s). Inbound messages render here as `<channel>` blocks.", m.Name, m.Channel)
	default:
		return "C3 connected. Use the `attach` tool to claim a Telegram topic for this session."
	}
}

func (a *adapter) toolsListResponse(req *mcp.Request) *mcp.Response {
	tools := []map[string]any{
		{
			"name":        "attach",
			"description": "Attach this session to a Telegram topic. With no args, proposes a topic from cwd basename. `target='dm'` for the user's DM. `name='X'` for a specific name. `topic_id=N` to claim a known thread id. `create=true` to confirm a creation proposal.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target":   map[string]any{"type": "string"},
					"name":     map[string]any{"type": "string"},
					"topic_id": map[string]any{"type": "integer"},
					"group":    map[string]any{"type": "string"},
					"create":   map[string]any{"type": "boolean"},
				},
			},
		},
		{
			"name":        "topics",
			"description": "List known Telegram topics across all groups, with claim state.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "reply",
			"description": "Send a Telegram reply to the currently-attached topic.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":       map[string]any{"type": "string"},
					"reply_to":   map[string]any{"type": "integer"},
					"parse_mode": map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
		{
			"name":        "react",
			"description": "Set a single-emoji reaction on a Telegram message.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message_id": map[string]any{"type": "integer"},
					"emoji":      map[string]any{"type": "string"},
				},
				"required": []string{"message_id", "emoji"},
			},
		},
		{
			"name":        "edit_message",
			"description": "Edit a previously-sent Telegram message.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message_id": map[string]any{"type": "integer"},
					"text":       map[string]any{"type": "string"},
				},
				"required": []string{"message_id", "text"},
			},
		},
		{
			"name":        "send_typing",
			"description": "Send a typing indicator to the attached topic.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "download_attachment",
			"description": "Download a Telegram file by file_id; returns the local path.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file_id": map[string]any{"type": "string"},
				},
				"required": []string{"file_id"},
			},
		},
	}
	return &mcp.Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{"tools": tools},
	}
}

// toolsCallResponse handles MCP tools/call. attach and topics are
// adapter-local; other tools forward to the broker as ipc.OpToolCall.
func (a *adapter) toolsCallResponse(ctx context.Context, req *mcp.Request) *mcp.Response {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, -32602, "invalid params: "+err.Error())
	}

	switch params.Name {
	case "attach":
		return a.handleAttachLocal(ctx, req, params.Arguments)
	case "topics":
		return a.handleTopicsLocal(ctx, req)
	default:
		return a.forwardToBroker(req, params.Name, params.Arguments)
	}
}

func (a *adapter) handleAttachLocal(ctx context.Context, req *mcp.Request, args map[string]any) *mcp.Response {
	cwd, _ := os.Getwd()
	attachReq := ipc.AttachReq{Op: ipc.OpAttach, CWD: cwd}
	if v, ok := args["target"].(string); ok {
		attachReq.Target = v
	}
	if v, ok := args["name"].(string); ok {
		attachReq.Name = v
	}
	if v, ok := args["group"].(string); ok {
		attachReq.Group = v
	}
	if v, ok := args["create"].(bool); ok {
		attachReq.Create = v
	}
	if v, ok := args["topic_id"]; ok {
		switch x := v.(type) {
		case float64:
			id := int64(x)
			attachReq.TopicID = &id
		case int64:
			attachReq.TopicID = &x
		}
	}

	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending["attached"] = ch
	a.pmu.Unlock()

	if err := a.currentConn().WriteJSON(attachReq); err != nil {
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker write: "+err.Error())
	}

	select {
	case <-ctx.Done():
		return errResp(req.ID, -32000, "canceled")
	case res := <-ch:
		attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
		return mcpTextResp(req.ID, formatAttached(&attached))
	}
}

func formatAttached(a *ipc.AttachedMsg) string {
	if a.OK {
		s := fmt.Sprintf("attached to %q", a.Name)
		if a.TopicID != nil {
			s += fmt.Sprintf(" (chat %d, thread %d)", a.ChatID, *a.TopicID)
		} else {
			s += fmt.Sprintf(" (chat %d, DM)", a.ChatID)
		}
		return s
	}
	if a.NeedsConfirmation && a.Proposal != nil {
		switch a.Proposal.Action {
		case "create":
			return fmt.Sprintf("No mapping for this directory. I'd create a new topic %q in the %q group. To proceed, call attach(create=true). To use an existing topic instead, call attach(topic_id=<n>).",
				a.Proposal.Name, a.Proposal.Group)
		case "use_existing_other_group":
			alt := ""
			if a.Proposal.Alternative != nil {
				alt = fmt.Sprintf(" or attach(create=true, group=%q) to create a new topic in %q",
					a.Proposal.Alternative.Group, a.Proposal.Alternative.Group)
			}
			return fmt.Sprintf("Found topic %q in group %q (thread %d). Reply yes to claim it%s.",
				a.Proposal.Existing.Name, a.Proposal.Existing.Group, a.Proposal.Existing.TopicID, alt)
		}
	}
	if a.Err != "" {
		return "attach failed: " + a.Err
	}
	return "attach: unspecified failure"
}

func (a *adapter) handleTopicsLocal(ctx context.Context, req *mcp.Request) *mcp.Response {
	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending["topics_list"] = ch
	a.pmu.Unlock()
	if err := a.currentConn().WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics}); err != nil {
		a.pmu.Lock()
		delete(a.pending, "topics_list")
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker write: "+err.Error())
	}
	select {
	case <-ctx.Done():
		return errResp(req.ID, -32000, "canceled")
	case res := <-ch:
		list, _ := res.Result["_topics_list"].(ipc.TopicsListMsg)
		return mcpTextResp(req.ID, formatTopics(&list))
	}
}

func formatTopics(list *ipc.TopicsListMsg) string {
	if len(list.Topics) == 0 {
		return "no topics configured."
	}
	var lines []string
	lines = append(lines, "known topics:")
	for _, t := range list.Topics {
		state := "free"
		if t.ClaimedBy != nil {
			state = fmt.Sprintf("held by %s pid %d", t.ClaimedBy.CLI, t.ClaimedBy.PID)
		}
		lines = append(lines, fmt.Sprintf("  • %s/%s (chat %d, thread %d) — %s",
			t.Group, t.Name, t.ChatID, t.TopicID, state))
	}
	return strings.Join(lines, "\n")
}

// forwardToBroker forwards a tool call as ipc.OpToolCall and waits for the
// matching ipc.OpToolResult.
func (a *adapter) forwardToBroker(req *mcp.Request, name string, args map[string]any) *mcp.Response {
	id := strconv.FormatUint(a.nextID.Add(1), 10)
	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending[id] = ch
	a.pmu.Unlock()

	tcReq := ipc.ToolCallReq{Op: ipc.OpToolCall, ID: id, Name: name, Args: args}
	if err := a.currentConn().WriteJSON(tcReq); err != nil {
		a.pmu.Lock()
		delete(a.pending, id)
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker write: "+err.Error())
	}

	select {
	case <-time.After(120 * time.Second):
		a.pmu.Lock()
		delete(a.pending, id)
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "tool timeout")
	case res := <-ch:
		if res.Error != nil {
			return errResp(req.ID, res.Error.Code, res.Error.Message)
		}
		return &mcp.Response{JSONRPC: "2.0", ID: req.ID, Result: res.Result}
	}
}

func errResp(id json.RawMessage, code int, msg string) *mcp.Response {
	return &mcp.Response{
		JSONRPC: "2.0", ID: id,
		Error: &mcp.Error{Code: code, Message: msg},
	}
}

func mcpTextResp(id json.RawMessage, text string) *mcp.Response {
	return &mcp.Response{
		JSONRPC: "2.0", ID: id,
		Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
}

// recoverError silences ESRCH and similar harmless errors from broker spawn.
func recoverError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("c3-broker not on PATH (run /c3-build to install)")
	}
	return err
}
