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
//
// MCP wire layer: github.com/modelcontextprotocol/go-sdk (v1.6.0+).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mcptools"
	"github.com/karthikeyan5/c3/internal/mode"
	"github.com/karthikeyan5/c3/internal/termtitle"
)

const (
	// adapterName MUST match the mcp_servers.<key>.* registration the
	// launcher writes into Codex's config (cmd/codex/main.go uses
	// `c3_codex`). Codex's channel/notification dispatch keys on this
	// name; using a different one (e.g. the binary name) silently
	// drops channel frames the same way Claude Code does
	// (see cmd/c3-claude-adapter/main.go's same comment).
	adapterName    = "c3_codex"
	adapterVersion = "0.1.0"

	inboxCap           = 100              // ring buffer max
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

	srv := a.buildMCPServer()
	a.transport = newLogNotifyTransport(&mcp.StdioTransport{})

	go a.brokerReader()
	go a.idleStartupWatchdog(ctx, cancel)

	err := srv.Run(ctx, a.transport)
	switch {
	case err == nil:
		log.Printf("adapter: exit pid=%d reason=stdin-eof (clean)", os.Getpid())
		return nil
	case errors.Is(err, context.Canceled) || errors.Is(err, io.EOF):
		log.Printf("adapter: exit pid=%d reason=context-canceled-or-eof (signal or idle-startup) (clean)", os.Getpid())
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
	// transport wraps the stdio transport to expose Notify for the
	// `notifications/message` log frame the SDK's Log API would normally
	// emit but with our own shape (no level filtering).
	transport *logNotifyTransport

	bmu  sync.Mutex
	conn *ipc.Conn

	pmu     sync.Mutex
	pending map[string]chan ipc.ToolResultMsg
	nextID  atomic.Uint64

	// Inbox ring buffer for the `inbox` tool fallback path.
	imu   sync.Mutex
	inbox []c3types.Inbound

	helloAck ipc.HelloAckMsg

	// dispatched is set the first time the SDK routes a method through the
	// receiving middleware — i.e. Codex has sent at least one MCP frame.
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

// spawnBroker forks a `c3-broker` process detached from our process group
// so it survives our shutdown.
//
// Stderr is explicitly NOT inherited from the adapter. Matches the Claude
// adapter (cmd/c3-claude-adapter/main.go::spawnBroker) verbatim per
// Karthi's "every flow must work the same in Codex" principle: the
// adapter's stderr is piped to the host's plugin host; piping broker log
// lines through that channel can make the host appear distressed (lots
// of unexplained stderr noise during normal broker bounces). The broker
// has its own structured log at $XDG_STATE_HOME/c3/broker.log via
// SetupLogging; the host has no reason to see broker stderr.
//
// Closes report MINOR m3 (2026-05-19).
func spawnBroker() error {
	cmd := exec.Command("c3-broker")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
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
	if a.transport != nil {
		_ = a.transport.Notify(context.Background(), "notifications/message", map[string]any{
			"level":  "info",
			"logger": "c3",
			"data":   formatLogLine(&msg.Inbound),
		})
	}

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
	// P4: a synthesized channel event (poll_result / reaction / callback) renders
	// from its neutral Event payload rather than message text. String-only — no
	// structured Telegram types reach the Codex turn.
	if in.IsEvent() {
		return fmt.Sprintf("Telegram %s event (chat=%d thread=%s)\n%s",
			in.Kind, in.ChatID, thread, formatEventBody(in))
	}
	return fmt.Sprintf("Telegram message from %s (chat=%d thread=%s)\n%s",
		sender, in.ChatID, thread, in.Text)
}

// formatEventBody renders a channel event's neutral payload into a one-line
// string body for the Codex log/turn forwarder. Mirrors the Claude adapter's
// buildEventFrame content (kept simple strings).
func formatEventBody(in *c3types.Inbound) string {
	ev := in.Event
	switch {
	case ev != nil && ev.PollResult != nil:
		pr := ev.PollResult
		parts := make([]string, 0, len(pr.Options))
		for _, o := range pr.Options {
			parts = append(parts, fmt.Sprintf("%s:%d", o.Text, o.VoterCount))
		}
		closed := ""
		if pr.IsClosed {
			closed = " (closed)"
		}
		return fmt.Sprintf("Poll results: %q — %d votes — %s%s",
			pr.Question, pr.TotalVoters, strings.Join(parts, " "), closed)
	case ev != nil && ev.Reaction != nil:
		r := ev.Reaction
		return fmt.Sprintf("reaction on message %d — added %s removed %s",
			r.MessageID, strings.Join(r.Added, " "), strings.Join(r.Removed, " "))
	case ev != nil && ev.Callback != nil:
		cb := ev.Callback
		return fmt.Sprintf("button pressed (data=%q) on message %d", cb.Data, cb.MessageID)
	default:
		return fmt.Sprintf("(%s event)", in.Kind)
	}
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
		// A successful attach may carry the just-claimed channel's manifest.
		// Store it as the latest caps so any subsequent instructions rebuild
		// reflects the attached channel (multi-channel turn-time-refresh seam,
		// spec §L5). v1 single-channel: the hello_ack caps already cover the
		// live session — kept for parity with the Claude adapter.
		if attached.OK && attached.Capabilities != nil {
			a.helloAck.Capabilities = attached.Capabilities
		}
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

// ─── MCP server wiring (modelcontextprotocol/go-sdk) ────────────────────────

// buildMCPServer constructs the SDK-backed MCP server with Codex-specific
// initialize fields, registers all tools, and installs the receiving
// middleware that disarms the idle-startup watchdog on first frame.
func (a *adapter) buildMCPServer() *mcp.Server {
	opts := &mcp.ServerOptions{
		Instructions: a.buildInstructions(),
		Capabilities: &mcp.ServerCapabilities{
			Tools:   &mcp.ToolCapabilities{ListChanged: false},
			Logging: &mcp.LoggingCapabilities{},
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    adapterName,
		Version: adapterVersion,
	}, opts)

	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			a.dispatched.Store(true)
			return next(ctx, method, req)
		}
	})

	a.registerTools(srv)
	return srv
}

func (a *adapter) buildInstructions() string {
	var head string
	switch {
	case a.helloAck.NoConfig:
		head = "C3 not yet configured. Run `c3-broker setup` from a shell to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart this Codex session."
	case a.helloAck.NoMapping:
		cwd := os.Getenv("C3_CODEX_CWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		head = fmt.Sprintf("No C3 mapping for %q. Use the `attach` tool to set one up. Inbound Telegram messages are buffered for the `inbox` tool until your TUI is bound (`--remote`).", cwd)
	default:
		head = "C3 connected. Use `attach` to claim a Telegram topic, `inbox` to drain buffered inbound, `reply` to send. Codex doesn't render unsolicited MCP notifications today; check `inbox` periodically."
	}
	return head + mode.Combined(a.capsOrDefault())
}

// capsOrDefault returns the channel capability manifest the broker delivered
// on hello_ack (or a fresh attach), falling back to a zero Capabilities when
// the broker predates the CMG build (Capabilities==nil) or no channel was
// resolvable. GuidanceFor renders the zero value as honest all-NO guidance —
// no panic, no fabricated capability. Parity with the Claude adapter: caps
// ride the once-delivered init/setup instructions, NOT per-turn text.
func (a *adapter) capsOrDefault() c3types.Capabilities {
	if a.helloAck.Capabilities != nil {
		return *a.helloAck.Capabilities
	}
	return c3types.Capabilities{}
}

// registerTools adds all adapter tools to srv (user-facing tools + the
// `codex_forward` debug tool).
func (a *adapter) registerTools(srv *mcp.Server) {
	caps := a.capsOrDefault()
	tools := []struct {
		tool    *mcp.Tool
		handler mcp.ToolHandler
	}{
		{
			tool: &mcp.Tool{
				Name:        "attach",
				Description: "Attach this Codex session to a Telegram topic. Same proposal-flow semantics as Claude Code's attach. If your CLI host's policy layer rejects this call (e.g. Codex approvals_reviewer=auto_review surfacing 'unacceptable risk rejection'), re-invoke with `policy_rejected=true` so the user sees the actionable next-step (tenant admin approval) rather than a silent failure.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target":          map[string]any{"type": "string"},
						"name":            map[string]any{"type": "string"},
						"topic_id":        map[string]any{"type": "integer"},
						"group":           map[string]any{"type": "string"},
						"create":          map[string]any{"type": "boolean"},
						"steal":           map[string]any{"type": "boolean"},
						"policy_rejected": map[string]any{"type": "boolean"},
					},
				},
			},
			handler: a.toolAttach,
		},
		{
			tool: &mcp.Tool{
				Name:        "topics",
				Description: "List known Telegram topics + claim state.",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			handler: a.toolTopics,
		},
		{
			tool: &mcp.Tool{
				Name:        "inbox",
				Description: "Drain buffered inbound Telegram messages. Codex-only fallback path until Codex renders unsolicited MCP notifications natively.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
						"ack":   map[string]any{"type": "boolean", "default": true},
					},
				},
			},
			handler: a.toolInbox,
		},
		{
			tool: &mcp.Tool{
				Name:        "reply",
				Description: "Send a Telegram reply to the currently-attached topic. Attach media via the `media` array: kind=\"file\" delivers the ORIGINAL bytes (PDFs, logs); kind=\"photo\" is a COMPRESSED in-chat preview; also video/audio/voice/animation. Each item is sent as its own message after the text.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":     map[string]any{"type": "string"},
						"reply_to": map[string]any{"type": "integer"},
						"media":    mcptools.ReplyMediaSchema(caps),
					},
					"required": []string{"text"},
				},
			},
			handler: a.toolForward("reply"),
		},
		{
			tool: &mcp.Tool{
				Name:        "react",
				Description: "Set a single-emoji reaction on a Telegram message.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message_id": map[string]any{"type": "integer"},
						"emoji":      map[string]any{"type": "string"},
					},
					"required": []string{"message_id", "emoji"},
				},
			},
			handler: a.toolForward("react"),
		},
		{
			tool: &mcp.Tool{
				Name:        "edit_message",
				Description: "Edit a previously-sent Telegram message.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message_id": map[string]any{"type": "integer"},
						"text":       map[string]any{"type": "string"},
					},
					"required": []string{"message_id", "text"},
				},
			},
			handler: a.toolForward("edit_message"),
		},
		// NOTE: `send_typing` is intentionally NOT registered as an agent tool
		// (P5 / spec R3). The typing indicator is relayed PROGRAMMATICALLY by
		// the broker's per-route RouteWorker (a ticker armed on inbound delivery
		// once the session is in Telegram mode), never by an LLM tool call. The
		// broker dispatch still HANDLES a `send_typing` op (legacy-in-flight
		// callers + the validate_topic piggyback) and the channel keeps its
		// SendTyping method — only the agent-facing tool is gone.
		{
			tool: &mcp.Tool{
				Name:        "poll",
				Description: "Send a Telegram poll to the attached topic. Provide a `question` and 2+ `options`. `anonymous` (default true) and `multiple` (default false) tune the poll.",
				InputSchema: mcptools.PollToolSchema(),
			},
			handler: a.toolForward("poll"),
		},
		{
			tool: &mcp.Tool{
				Name:        "stop_poll",
				Description: "Force-close a poll you sent and read its final aggregate tally (counts per option + total voters). Pass the `message_id` returned when you sent the poll. Aggregate results also arrive automatically as a <channel> event when a poll closes; stop_poll is the deterministic early read.",
				InputSchema: mcptools.StopPollToolSchema(),
			},
			handler: a.toolForward("stop_poll"),
		},
		{
			tool: &mcp.Tool{
				Name:        "download_attachment",
				Description: "Download a Telegram file by file_id; returns the local path.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_id": map[string]any{"type": "string"},
					},
					"required": []string{"file_id"},
				},
			},
			handler: a.toolForward("download_attachment"),
		},
		{
			tool: &mcp.Tool{
				Name:        "codex_forward",
				Description: "Debugging/manual override for the Codex app-server WebSocket forwarder. Refused unless C3_CODEX_REMOTE_BRIDGE=1 (set by the codex launcher) or C3_CODEX_ALLOW_MANUAL_FORWARD=1.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"app_server_ws": map[string]any{"type": "string"},
						"thread_id":     map[string]any{"type": "string"},
					},
					"required": []string{"app_server_ws"},
				},
			},
			handler: a.toolCodexForward,
		},
	}
	for _, t := range tools {
		srv.AddTool(t.tool, t.handler)
	}
}

// decodeArgs unmarshals raw tool arguments into a generic map.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func (a *adapter) toolAttach(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
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
	if v, ok := args["steal"].(bool); ok {
		attachReq.Steal = v
	}
	if v, ok := args["policy_rejected"].(bool); ok {
		attachReq.PolicyRejected = v
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
		return toolErrorResult("broker reconnecting — retry attach in a moment"), nil
	}
	if err := conn.WriteJSON(attachReq); err != nil {
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case res := <-ch:
		attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
		if attached.OK {
			// Side-effect surface: OSC-0 title-bar escape to stderr
			// for the currently-attached topic. Closes TODO #19(a).
			// Cross-CLI parity with the Claude adapter; same gates
			// (tty + C3_NO_TERMINAL_TITLE). See internal/termtitle.
			termtitle.EmitAttach(&attached)
		}
		return toolTextResult(ipc.FormatAttached(&attached)), nil
	}
}

func (a *adapter) toolTopics(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch := make(chan ipc.ToolResultMsg, 1)
	a.pmu.Lock()
	a.pending["topics_list"] = ch
	a.pmu.Unlock()
	conn := a.currentConn()
	if conn == nil {
		a.pmu.Lock()
		delete(a.pending, "topics_list")
		a.pmu.Unlock()
		return toolErrorResult("broker reconnecting — retry topics in a moment"), nil
	}
	if err := conn.WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics}); err != nil {
		a.pmu.Lock()
		delete(a.pending, "topics_list")
		a.pmu.Unlock()
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case res := <-ch:
		list, _ := res.Result["_topics_list"].(ipc.TopicsListMsg)
		return toolTextResult(ipc.FormatTopics(&list)), nil
	}
}

func (a *adapter) toolInbox(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
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
		return toolTextResult("c3 inbox is empty"), nil
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
	return toolTextResult(strings.Join(chunks, "\n\n")), nil
}

func (a *adapter) toolCodexForward(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if !codexForwardingAllowed() {
		return toolErrorResult(
			"codex_forward refused: requires C3_CODEX_REMOTE_BRIDGE=1 (set by the codex launcher) or C3_CODEX_ALLOW_MANUAL_FORWARD=1 (debug). Split-brain guard."), nil
	}
	wsURL, _ := args["app_server_ws"].(string)
	if wsURL == "" {
		wsURL = os.Getenv("C3_CODEX_APP_SERVER_WS")
	}
	if wsURL == "" {
		return toolErrorResult("app_server_ws is required (or set C3_CODEX_APP_SERVER_WS)"), nil
	}
	return toolTextResult(fmt.Sprintf("codex_forward registered ws=%s", wsURL)), nil
}

func (a *adapter) toolForward(name string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req.Params.Arguments)
		if err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
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
			return toolErrorResult("broker reconnecting — retry tool call in a moment"), nil
		}
		if err := conn.WriteJSON(tcReq); err != nil {
			a.pmu.Lock()
			delete(a.pending, id)
			a.pmu.Unlock()
			return toolErrorResult("broker write: " + err.Error()), nil
		}

		select {
		case <-time.After(120 * time.Second):
			a.pmu.Lock()
			delete(a.pending, id)
			a.pmu.Unlock()
			return toolErrorResult("tool timeout"), nil
		case res := <-ch:
			if res.Error != nil {
				return toolErrorResult(res.Error.Message), nil
			}
			return toolResultFromMap(res.Result), nil
		}
	}
}

func toolTextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func toolErrorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func toolResultFromMap(result map[string]any) *mcp.CallToolResult {
	if result == nil {
		return toolTextResult("")
	}
	if contentRaw, ok := result["content"]; ok {
		if items, ok := contentRaw.([]any); ok {
			var blocks []mcp.Content
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				text, _ := m["text"].(string)
				blocks = append(blocks, &mcp.TextContent{Text: text})
			}
			if len(blocks) > 0 {
				return &mcp.CallToolResult{Content: blocks}
			}
		}
	}
	enc, err := json.Marshal(result)
	if err != nil {
		return toolErrorResult("marshal result: " + err.Error())
	}
	return toolTextResult(string(enc))
}
