// c3-codex-adapter is the Codex MCP server that bridges Codex's MCP stdio
// protocol to the C3 broker over /tmp/c3.sock (or $XDG_RUNTIME_DIR/c3.sock).
//
// Spec §4.4. The adapter:
//
//  1. On stdin: accept JSON-RPC 2.0 requests from Codex (initialize, tools/list,
//     tools/call, ping, notifications/initialized).
//  2. On the broker socket: connect, send hello (with C3_ATTACH_NAME if the
//     launcher set it), listen for inbound / tool_result / topics_list frames.
//  3. For tools/call: route adapter-local tools (`attach`, `topics`, `inbox`,
//     `codex_forward`) directly; forward all other tools to the broker.
//  4. For broker-side ipc.OpInbound frames:
//     - Buffer in a ring (cap 100) that `inbox` tool drains.
//     - Emit `notifications/message` log notification (cheap; future-proofs
//     for when Codex starts surfacing them — issues #18056/#17543/#15299).
//     - When C3_CODEX_REMOTE_BRIDGE=1 (set by the codex launcher), forward
//     to the Codex app-server via WebSocket so the inbound becomes a
//     real Codex turn.
//
// Spec §4.4.5 env contract from launcher → adapter:
//
//	C3_ATTACH_NAME              topic name inferred from cwd
//	C3_CODEX_REMOTE_BRIDGE      "1" iff launcher started us; gates forwarding
//	C3_CODEX_CWD                absolute cwd; used for thread/list filtering
//	C3_CODEX_APP_SERVER_WS      ws://host:port of the Codex app-server
//	C3_CODEX_ALLOW_MANUAL_FORWARD  debug bypass for split-brain guard
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mcp"
)

const (
	mcpProtocolVersion = "2024-11-05"
	// adapterName MUST match the mcp_servers.<key>.* registration the
	// launcher writes into Codex's config (cmd/codex/main.go uses
	// `c3_codex`). Codex's channel/notification dispatch keys on this
	// name; using a different one (e.g. the binary name) silently
	// drops channel frames the same way Claude Code does
	// (see cmd/c3-claude-adapter/main.go's same comment).
	adapterName    = "c3_codex"
	adapterVersion = "0.1.0"
	inboxCap           = 100             // ring buffer max
	idleStartupTimeout = 60 * time.Second // mirror cmd/c3-claude-adapter behavior
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-codex-adapter: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installSignalHandlers(cancel)

	a := newAdapter()
	if err := a.connectBroker(); err != nil {
		log.Printf("adapter: exit pid=%d reason=connect-broker err=%v", os.Getpid(), err)
		return fmt.Errorf("connect broker: %w", err)
	}
	if err := a.hello(); err != nil {
		log.Printf("adapter: exit pid=%d reason=hello err=%v", os.Getpid(), err)
		return fmt.Errorf("hello: %w", err)
	}

	// Auto-attach if the launcher set C3_ATTACH_NAME.
	if name := os.Getenv("C3_ATTACH_NAME"); name != "" {
		go a.autoAttach(name)
	}

	mcpSrv := mcp.New(os.Stdin, os.Stdout, a)
	a.mcp = mcpSrv

	go a.brokerReader()
	go a.idleStartupWatchdog(ctx, cancel)

	err := mcpSrv.Run(ctx)
	switch {
	case err == nil:
		log.Printf("adapter: exit pid=%d reason=stdin-eof (clean)", os.Getpid())
		return nil
	case errors.Is(err, context.Canceled):
		log.Printf("adapter: exit pid=%d reason=context-canceled (signal or idle-startup) (clean)", os.Getpid())
		return nil
	default:
		log.Printf("adapter: exit pid=%d reason=mcp-error err=%v", os.Getpid(), err)
		return err
	}
}

// installSignalHandlers cancels ctx on SIGTERM/SIGINT/SIGHUP. Without this,
// Go's default behavior is to terminate immediately, leaving no log line
// explaining why the adapter died — exact same rationale as the Claude
// adapter (cmd/c3-claude-adapter/main.go installSignalHandlers).
func installSignalHandlers(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		sig := <-ch
		log.Printf("adapter: received signal=%v pid=%d", sig, os.Getpid())
		cancel()
	}()
}

// idleStartupWatchdog cancels ctx if Codex never sends an MCP frame within
// idleStartupTimeout. Codex's MCP host may abandon a spawned adapter
// without driving stdin (similar to Claude Code on `--resume`); the
// watchdog lets us exit cleanly rather than hold a broker conn forever.
func (a *adapter) idleStartupWatchdog(ctx context.Context, cancel context.CancelFunc) {
	timer := time.NewTimer(idleStartupTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if !a.dispatched.Load() {
			log.Printf("adapter: idle-startup timeout pid=%d (no MCP frame in %v) — exiting so Codex can respawn",
				os.Getpid(), idleStartupTimeout)
			cancel()
		}
	}
}

type adapter struct {
	mcp *mcp.Server

	bmu  sync.Mutex
	conn *ipc.Conn

	pmu     sync.Mutex
	pending map[string]chan ipc.ToolResultMsg
	nextID  atomic.Uint64

	// Inbox ring buffer for the `inbox` tool fallback path.
	imu   sync.Mutex
	inbox []c3types.Inbound

	helloAck ipc.HelloAckMsg

	// dispatched is set the first time Dispatch is called — i.e. Codex
	// has sent at least one MCP frame. The idle-startup watchdog uses
	// this to distinguish "live session" from "orphaned spawn".
	dispatched atomic.Bool
}

func newAdapter() *adapter {
	return &adapter{
		pending: map[string]chan ipc.ToolResultMsg{},
	}
}

func (a *adapter) connectBroker() error {
	sockPath := broker.SocketPath()
	for attempt := 0; attempt < 50; attempt++ {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			a.bmu.Lock()
			a.conn = ipc.NewConn(c)
			a.bmu.Unlock()
			return nil
		}
		if attempt == 0 {
			_ = spawnBroker()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("could not reach broker at %s after 10s", sockPath)
}

func spawnBroker() error {
	cmd := exec.Command("c3-broker")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = sysSetsid()
	return cmd.Start()
}

func (a *adapter) hello() error {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := a.conn.WriteJSON(ipc.HelloMsg{
		Op: ipc.OpHello, CLI: "codex", PID: os.Getpid(), CWD: cwd,
		Capabilities: []string{"log-notification", "inbox", "ws-forwarder"},
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
	return nil
}

// autoAttach is fired in a goroutine when the launcher provided C3_ATTACH_NAME
// via env. Sends an attach to the broker; broker either silently claims (if
// the topic exists in the default group) or returns a proposal that the agent
// can act on via the `attach` tool.
func (a *adapter) autoAttach(name string) {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending["attached"] = ch
	a.pmu.Unlock()
	conn := a.currentConn()
	if conn == nil {
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
		log.Printf("auto-attach skipped: broker not yet connected")
		return
	}
	if err := conn.WriteJSON(ipc.AttachReq{
		Op: ipc.OpAttach, CWD: cwd, Name: name,
	}); err != nil {
		log.Printf("auto-attach write failed: %v", err)
		return
	}
	// Drain (the brokerReader routes to this channel via dispatchAttached).
	select {
	case <-time.After(15 * time.Second):
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
	case <-ch:
		// no-op; auto-attach result reflected in helloAck on next reconnect
	}
}

func (a *adapter) currentConn() *ipc.Conn {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return a.conn
}

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
				fmt.Fprintf(os.Stderr, "c3-codex-adapter: broker read err: %v — reconnecting once\n", err)
				if rerr := a.reconnectBroker(); rerr != nil {
					fmt.Fprintf(os.Stderr, "c3-codex-adapter: reconnect failed: %v\n", rerr)
					a.wakePendingWithErr("broker disconnected: " + err.Error())
					return
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "c3-codex-adapter: broker read err after reconnect: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "c3-codex-adapter: broker error: %s\n", errMsg.Err)
		}
	}
}

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

// handleInbound: buffer in ring + emit log notification + (if remote-bridge) WS-forward.
func (a *adapter) handleInbound(raw []byte) {
	var msg ipc.InboundMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	// Ring buffer (drop oldest at cap).
	a.imu.Lock()
	a.inbox = append(a.inbox, msg.Inbound)
	if len(a.inbox) > inboxCap {
		dropped := len(a.inbox) - inboxCap
		a.inbox = a.inbox[dropped:]
		fmt.Fprintf(os.Stderr, "c3-codex-adapter: inbox cap exceeded; dropped %d oldest\n", dropped)
	}
	a.imu.Unlock()

	// Log notification — cheap; future-proofs for Codex native rendering.
	_ = a.mcp.Notify("notifications/message", map[string]any{
		"level":  "info",
		"logger": "c3",
		"data":   formatLogLine(&msg.Inbound),
	})

	// WS forwarder (gated by env, see split-brain guard).
	if codexForwardingAllowed() {
		go a.forwardToCodexAppServer(&msg.Inbound)
	}
}

func formatLogLine(in *c3types.Inbound) string {
	thread := "0"
	if in.TopicID != nil {
		thread = strconv.FormatInt(*in.TopicID, 10)
	}
	sender := in.Sender.Username
	if sender == "" {
		sender = strconv.FormatInt(in.Sender.UserID, 10)
	}
	return fmt.Sprintf("Telegram message from %s (chat=%d thread=%s)\n%s",
		sender, in.ChatID, thread, in.Text)
}

func codexForwardingAllowed() bool {
	return os.Getenv("C3_CODEX_REMOTE_BRIDGE") == "1" ||
		os.Getenv("C3_CODEX_ALLOW_MANUAL_FORWARD") == "1"
}

// forwardToCodexAppServer pushes one inbound as a Codex turn via WebSocket.
// Runs in a goroutine; failure logged but doesn't error the whole adapter.
//
// WS protocol per spec §4.4 Codex section:
//
//	initialize → notifications/initialized → thread/loaded/list →
//	(thread/list filtered by cwd if multiple loaded) → thread/resume →
//	thread/turn/start
//
// Each inbound opens a fresh short-lived WebSocket (Codex app-server expects
// new turns this way; long-lived sessions would conflict with the visible
// TUI's connection).
//
// v0.1.0 stub: the WebSocket dance is documented in the spec; concrete
// implementation is left as a TODO with a warning log so the adapter still
// compiles and the inbox-poll fallback works. Plan 7-followup will fill this
// in — see the Python POC's CodexAppServerForwarder for the reference shape.
func (a *adapter) forwardToCodexAppServer(in *c3types.Inbound) {
	if err := forwardInboundToCodexAppServer(context.Background(), in, codexForwardConfigFromEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "c3-codex-adapter: WS forward failed for inbound id=%d: %v\n", in.MessageID, err)
	}
}

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

func (a *adapter) dispatchAttached(raw []byte) {
	a.pmu.Lock()
	ch, ok := a.pending["attached"]
	if ok {
		delete(a.pending, "attached")
	}
	a.pmu.Unlock()
	if ok {
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

func (a *adapter) Dispatch(ctx context.Context, req *mcp.Request) *mcp.Response {
	a.dispatched.Store(true) // disarm idle-startup watchdog
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
	return &mcp.Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools":   map[string]any{},
				"logging": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    adapterName,
				"version": adapterVersion,
			},
			"instructions": a.buildInstructions(),
		},
	}
}

func (a *adapter) buildInstructions() string {
	switch {
	case a.helloAck.NoConfig:
		return "C3 not yet configured. Run `c3-broker setup` from a shell to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart this Codex session."
	case a.helloAck.NoMapping:
		cwd := os.Getenv("C3_CODEX_CWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		return fmt.Sprintf("No C3 mapping for %q. Use the `attach` tool to set one up. Inbound Telegram messages are buffered for the `inbox` tool until your TUI is bound (`--remote`).", cwd)
	default:
		return "C3 connected. Use `attach` to claim a Telegram topic, `inbox` to drain buffered inbound, `reply` to send. Codex doesn't render unsolicited MCP notifications today; check `inbox` periodically."
	}
}

func (a *adapter) toolsListResponse(req *mcp.Request) *mcp.Response {
	tools := []map[string]any{
		{
			"name":        "attach",
			"description": "Attach this Codex session to a Telegram topic. Same proposal-flow semantics as Claude Code's attach.",
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
			"description": "List known Telegram topics + claim state.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "inbox",
			"description": "Drain buffered inbound Telegram messages. Codex-only fallback path until Codex renders unsolicited MCP notifications natively.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
					"ack":   map[string]any{"type": "boolean", "default": true},
				},
			},
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
		{
			"name":        "codex_forward",
			"description": "Debugging/manual override for the Codex app-server WebSocket forwarder. Refused unless C3_CODEX_REMOTE_BRIDGE=1 (set by the codex launcher) or C3_CODEX_ALLOW_MANUAL_FORWARD=1.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_server_ws": map[string]any{"type": "string"},
					"thread_id":     map[string]any{"type": "string"},
				},
				"required": []string{"app_server_ws"},
			},
		},
	}
	return &mcp.Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{"tools": tools},
	}
}

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
	case "inbox":
		return a.handleInboxLocal(ctx, req, params.Arguments)
	case "codex_forward":
		return a.handleCodexForwardLocal(req, params.Arguments)
	default:
		return a.forwardToBroker(req, params.Name, params.Arguments)
	}
}

func (a *adapter) handleAttachLocal(ctx context.Context, req *mcp.Request, args map[string]any) *mcp.Response {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
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

	conn := a.currentConn()
	if conn == nil {
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker reconnecting — retry attach in a moment")
	}
	if err := conn.WriteJSON(attachReq); err != nil {
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
			return fmt.Sprintf("No mapping for this directory. I'd create a new topic %q in the %q group. Call attach(create=true) to proceed, or attach(topic_id=<n>) to use an existing topic.",
				a.Proposal.Name, a.Proposal.Group)
		case "use_existing_other_group":
			alt := ""
			if a.Proposal.Alternative != nil {
				alt = fmt.Sprintf(" or attach(create=true) to create a new topic in %q",
					a.Proposal.Alternative.Group)
			}
			return fmt.Sprintf("Found %q in group %q (thread %d). Reply yes to claim it%s.",
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
	conn := a.currentConn()
	if conn == nil {
		a.pmu.Lock()
		delete(a.pending, "topics_list")
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker reconnecting — retry topics in a moment")
	}
	if err := conn.WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics}); err != nil {
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

func (a *adapter) handleInboxLocal(_ context.Context, req *mcp.Request, args map[string]any) *mcp.Response {
	limit := 10
	if v, ok := args["limit"]; ok {
		if f, ok := v.(float64); ok {
			limit = int(f)
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	ack := true
	if v, ok := args["ack"].(bool); ok {
		ack = v
	}

	a.imu.Lock()
	defer a.imu.Unlock()

	if len(a.inbox) == 0 {
		return mcpTextResp(req.ID, "c3 inbox is empty")
	}
	n := limit
	if n > len(a.inbox) {
		n = len(a.inbox)
	}
	chunks := make([]string, 0, n)
	for i, in := range a.inbox[:n] {
		chunks = append(chunks, fmt.Sprintf("[%d] %s", i+1, formatLogLine(&in)))
	}
	if ack {
		a.inbox = a.inbox[n:]
	}
	return mcpTextResp(req.ID, strings.Join(chunks, "\n\n"))
}

func (a *adapter) handleCodexForwardLocal(req *mcp.Request, args map[string]any) *mcp.Response {
	if !codexForwardingAllowed() {
		return errResp(req.ID, -32000,
			"codex_forward refused: requires C3_CODEX_REMOTE_BRIDGE=1 (set by the codex launcher) or C3_CODEX_ALLOW_MANUAL_FORWARD=1 (debug). Split-brain guard.")
	}
	wsURL, _ := args["app_server_ws"].(string)
	if wsURL == "" {
		wsURL = os.Getenv("C3_CODEX_APP_SERVER_WS")
	}
	if wsURL == "" {
		return errResp(req.ID, -32602, "app_server_ws is required (or set C3_CODEX_APP_SERVER_WS)")
	}
	return mcpTextResp(req.ID,
		fmt.Sprintf("codex_forward registered ws=%s", wsURL))
}

func (a *adapter) forwardToBroker(req *mcp.Request, name string, args map[string]any) *mcp.Response {
	id := strconv.FormatUint(a.nextID.Add(1), 10)
	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending[id] = ch
	a.pmu.Unlock()

	tcReq := ipc.ToolCallReq{Op: ipc.OpToolCall, ID: id, Name: name, Args: args}
	conn := a.currentConn()
	if conn == nil {
		a.pmu.Lock()
		delete(a.pending, id)
		a.pmu.Unlock()
		return errResp(req.ID, -32000, "broker reconnecting — retry tool call in a moment")
	}
	if err := conn.WriteJSON(tcReq); err != nil {
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
