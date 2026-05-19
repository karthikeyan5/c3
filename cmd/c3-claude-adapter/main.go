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
//  3. For tools/call: route adapter-local tools (`attach`, `topics`, etc.)
//     directly; forward all other tools to the broker as ipc.OpToolCall and
//     return the result.
//  4. For broker-side ipc.OpInbound frames: emit `notifications/claude/channel`
//     via the custom notifyTransport (see notify_transport.go) so the
//     notification ends up on the same stdout the SDK uses, with a single
//     newline-terminated JSON frame matching the official Telegram plugin's
//     wire shape (CC's channel-dispatch validator silently drops malformed
//     frames; the wire shape must be exact).
//
// Reconnect-once policy from spec §4.4 has been upgraded to reconnect-forever
// with exponential backoff. The original 1-shot policy turned a 30s broker
// rebuild into a permanently dead adapter that required restarting the CLI
// session.
//
// MCP wire layer: github.com/modelcontextprotocol/go-sdk (v1.6.0+). All
// hand-rolled JSON-RPC framing has been migrated to the SDK; the only
// adapter-owned framing is the custom `notifications/claude/channel`
// notification, which the SDK's typed Notify API doesn't support (it locks
// outgoing methods to the spec's known set). For that single path we synthesise
// `*jsonrpc.Request` notifications via notifyTransport.Notify (see
// notify_transport.go for the rationale and wire-shape guarantees).
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
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mode"
	"github.com/karthikeyan5/c3/internal/termtitle"
)

// idleStartupTimeout bounds how long the adapter waits for Claude Code to
// send its first MCP frame (initialize). If Claude Code spawns the adapter
// but then abandons it without driving stdin — observed during `--resume`
// flows where CC respawns MCP servers and the prior process is orphaned
// alive — the adapter exits cleanly after this window rather than living
// as a zombie holding a broker conn. 60s is well past the millisecond-scale
// MCP handshake budget; any real Claude Code session is past initialize
// long before this fires.
const idleStartupTimeout = 60 * time.Second

const (
	// adapterName MUST match the .mcp.json key so Claude Code's channel
	// dispatch recognises this server as the same one it spawned. Reference
	// implementations (~/.claude/plugins/.../fakechat/server.ts:60,
	// .../telegram/0.0.6/server.ts:371) all set serverInfo.name == .mcp.json
	// key. Using the binary name here was a guess that broke channel
	// notification surfacing in 2026-05-09 testing — broker delivered, this
	// frame went out correctly, but Claude Code never injected it as a
	// channel event.
	adapterName    = "c3"
	adapterVersion = "0.1.0"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-claude-adapter: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Persistent adapter log at $XDG_STATE_HOME/c3/adapter.log. Adapter stderr
	// is socket-paired to Claude Code's plugin host and inaccessible from
	// outside; the file is the durable signal for "did the adapter send the
	// notification, and what did it look like." Same content policy as the
	// broker (DEBUGGING.md): metadata only on success, content on failure.
	if path, err := setupAdapterLog(); err == nil {
		fmt.Fprintf(os.Stderr, "c3-claude-adapter: log file %s\n", path)
	}

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

	server := a.buildMCPServer()
	a.notifyTx = newNotifyTransport(&mcp.StdioTransport{})

	go a.brokerReader(ctx)
	go a.idleStartupWatchdog(ctx, cancel)

	err := server.Run(ctx, a.notifyTx)
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
// explaining why the adapter died. We need that breadcrumb to diagnose
// "MCP server disconnected" incidents — was it a signal from Claude Code,
// stdin EOF, or an internal error? Cancellation propagates through the
// MCP server loop so its Run() returns and main() logs the exit reason.
func installSignalHandlers(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		sig := <-ch
		log.Printf("adapter: received signal=%v pid=%d", sig, os.Getpid())
		cancel()
	}()
}

// idleStartupWatchdog cancels ctx if Claude Code never sends an MCP frame
// within idleStartupTimeout. This handles the observed `--resume` failure
// mode where CC spawns an adapter, the adapter completes broker handshake,
// but CC never sends `initialize` (presumed: CC orphans the spawn during
// session resume teardown). Without this, the adapter sits reading stdin
// forever, holding a broker conn, and the user sees the plugin as
// "disconnected" until they manually `/mcp` reconnect.
func (a *adapter) idleStartupWatchdog(ctx context.Context, cancel context.CancelFunc) {
	timer := time.NewTimer(idleStartupTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if !a.dispatched.Load() {
			log.Printf("adapter: idle-startup timeout pid=%d (no MCP frame in %v) — exiting so Claude Code can respawn",
				os.Getpid(), idleStartupTimeout)
			cancel()
		}
	}
}

// setupAdapterLog opens $XDG_STATE_HOME/c3/adapter.log (append) and tees
// stdlib log there + stderr.
func setupAdapterLog() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(state, "c3")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "adapter.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("adapter: started pid=%d", os.Getpid())
	return path, nil
}

type adapter struct {
	// notifyTx wraps the stdio transport to permit emitting custom
	// `notifications/claude/channel` frames. Set in run() before Server.Run.
	notifyTx *notifyTransport

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

	// Last successful attach request — replayed on broker reconnect so a
	// session that survives a broker restart auto-reclaims its route. Nil
	// if the user hasn't attached yet (or detached).
	amu        sync.Mutex
	lastAttach *ipc.AttachReq

	// firstInbound triggers a one-shot wire dump of the first
	// notifications/claude/channel frame for live debugging.
	firstInbound atomic.Bool

	// dispatched is set the first time the SDK runs a method handler — i.e.
	// Claude Code has sent at least one MCP frame. The idle-startup watchdog
	// uses this to distinguish "live session" from "orphaned spawn that
	// Claude Code never drove". The receiving-middleware in buildMCPServer
	// flips this on first call.
	dispatched atomic.Bool
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
//
// Stderr is explicitly NOT inherited from the adapter. The adapter's stderr
// is piped to Claude Code's plugin host; piping broker log lines through
// that channel made the plugin host appear distressed (lots of unexplained
// stderr noise during normal broker bounces), which we suspect contributes
// to CC closing the adapter's stdin during /c3:restart-broker. The broker
// has its own structured log at $XDG_STATE_HOME/c3/broker.log via
// SetupLogging; CC has no reason to see broker stderr.
func spawnBroker() error {
	cmd := exec.Command("c3-broker")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
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

// brokerReader runs in a goroutine, draining frames from the broker. On any
// read error, runs the recovery loop (exponential backoff, no give-up) until
// either ctx is canceled or we re-establish a usable connection. After
// recovery, replays the last successful attach so the route claim is
// re-established without user intervention.
func (a *adapter) brokerReader(ctx context.Context) {
	for {
		conn := a.currentConn()
		if conn == nil {
			return
		}
		raw, err := conn.ReadFrame()
		if err != nil {
			// File-only log: CC's plugin host treats noisy stderr as
			// "distressed plugin → recycle me". Broker bounces are
			// expected and recoverable; don't telegraph them to CC.
			log.Printf("broker read err: %v — recovering", err)
			if !a.recoverBroker(ctx) {
				log.Printf("broker recovery aborted")
				return
			}
			continue
		}
		op, err := ipc.PeekOp(raw)
		if err != nil {
			continue
		}
		switch op {
		case ipc.OpInbound:
			a.handleInbound(ctx, raw)
		case ipc.OpToolResult:
			a.dispatchToolResult(raw)
		case ipc.OpAttached:
			a.dispatchAttached(raw)
		case ipc.OpTopicsList:
			a.dispatchTopicsList(raw)
		case ipc.OpError:
			var errMsg ipc.ErrorMsg
			_ = json.Unmarshal(raw, &errMsg)
			log.Printf("broker error: %s", errMsg.Err)
		}
	}
}

// reconnectBroker tears down the dead conn, dials a fresh one, sends hello.
// Pending tool calls are woken with an error so callers don't hang during
// the reconnect window. Single attempt; recoverBroker is the retry-loop
// wrapper.
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

// recoverBroker loops with exponential backoff until reconnectBroker
// succeeds, or ctx cancellation aborts the loop (returns false).
// After a successful reconnect, replays the last successful attach
// (best-effort) so the route claim is restored.
func (a *adapter) recoverBroker(ctx context.Context) bool {
	const (
		base       = 500 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := base
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			log.Printf("broker recovery canceled (ctx done): %v", err)
			return false
		}
		err := a.reconnectBroker()
		if err == nil {
			log.Printf("broker reconnected (attempt %d)", attempt)
			a.replayLastAttach()
			return true
		}
		log.Printf("broker reconnect attempt %d failed: %v (retry in %v)", attempt, err, backoff)
		select {
		case <-ctx.Done():
			log.Printf("broker recovery canceled mid-backoff: %v", ctx.Err())
			return false
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// rememberAttach stores the last successful attach request for replay on
// reconnect. The pointer captures all dimensions (target/name/topic_id/
// group/create) the user originally chose.
func (a *adapter) rememberAttach(req ipc.AttachReq) {
	a.amu.Lock()
	defer a.amu.Unlock()
	cp := req
	a.lastAttach = &cp
}

// replayLastAttach sends the saved attach request to the (just-reconnected)
// broker. Best-effort — failures are logged to stderr and not surfaced.
// The broker will respond with AttachedMsg which brokerReader processes
// (no pending channel registered, so the response is discarded). The point
// is to re-establish the route claim, not to confirm.
func (a *adapter) replayLastAttach() {
	a.amu.Lock()
	req := a.lastAttach
	a.amu.Unlock()
	if req == nil {
		return
	}
	if conn := a.currentConn(); conn != nil {
		replay := *req
		replay.Replay = true
		if err := conn.WriteJSON(replay); err != nil {
			log.Printf("replay attach failed: %v", err)
			return
		}
		log.Printf("replayed attach (target=%q name=%q)", req.Target, req.Name)
	}
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
//
// Logging policy: log delivery metadata only (chan / chat / topic / msg /
// kind / outcome). NEVER content, NEVER sender username. See DEBUGGING.md.
func (a *adapter) handleInbound(ctx context.Context, raw []byte) {
	var in ipc.InboundMsg
	if err := json.Unmarshal(raw, &in); err != nil {
		log.Printf("handleInbound unmarshal: %v", err)
		return
	}
	kind := "text"
	if len(in.Inbound.Attachments) > 0 && in.Inbound.Attachments[0].Kind != "" {
		kind = in.Inbound.Attachments[0].Kind
	}
	topic := "-"
	if in.Inbound.TopicID != nil {
		topic = strconv.FormatInt(*in.Inbound.TopicID, 10)
	}
	frame := buildClaudeChannelFrame(&in.Inbound)

	// One-shot wire dump for diagnosing "broker delivers but CLI silent" —
	// captures the exact bytes we send so we can prove the shape from outside
	// the adapter. Logged on FIRST inbound only to avoid noise.
	if a.firstInbound.CompareAndSwap(false, true) {
		if raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/claude/channel",
			"params":  frame,
		}); err == nil {
			log.Printf("notify FIRST-WIRE-DUMP: %s", string(raw))
		}
	}

	if err := a.notifyTx.Notify(ctx, "notifications/claude/channel", frame); err != nil {
		log.Printf("notify FAIL chan=%s chat=%d topic=%s msg=%d kind=%s: %v",
			in.Inbound.Channel, in.Inbound.ChatID, topic, in.Inbound.MessageID, kind, err)
		return
	}
	log.Printf("notified chan=%s chat=%d topic=%s msg=%d kind=%s",
		in.Inbound.Channel, in.Inbound.ChatID, topic, in.Inbound.MessageID, kind)
}

// buildClaudeChannelFrame converts a c3types.Inbound into the params for
// notifications/claude/channel.
//
// Shape MUST match the official Telegram plugin's frame
// (~/.claude/plugins/cache/claude-plugins-official/telegram/0.0.6/server.ts
// around line 978) — Claude Code silently drops malformed channel
// notifications. Cross-checked 2026-05-09:
//   - `content` is a STRING, not an array of MCP-style content blocks. We
//     had it as the array shape originally and Claude Code dropped every
//     inbound on the floor (broker logged "delivered", user saw nothing).
//   - `chat_id` is stringified (matching channels-reference.md docs).
//   - `message_thread_id` only present when non-nil; same for the optional
//     attachment / reply_to fields.
func buildClaudeChannelFrame(in *c3types.Inbound) map[string]any {
	meta := map[string]any{
		// Per channels-reference.md, `meta` is typed Record<string, string>.
		// All values must be strings; non-string values may cause Claude
		// Code to silently drop the field (or the whole notification).
		// Official Telegram plugin's TypeScript happens to serialize chat_id
		// as a number due to gotgbot's Long type, but the contract is string.
		// fakechat (the reference impl) sends "web" — clearly a string.
		"chat_id": strconv.FormatInt(in.ChatID, 10),
		"ts":      in.Timestamp.Format("2006-01-02T15:04:05.000Z"),
	}
	if in.MessageID != 0 {
		meta["message_id"] = strconv.FormatInt(in.MessageID, 10)
	}
	if in.Sender.Username != "" {
		meta["user"] = in.Sender.Username
	} else if in.Sender.UserID != 0 {
		meta["user"] = strconv.FormatInt(in.Sender.UserID, 10)
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
		if att.Name != "" {
			meta["attachment_name"] = att.Name
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
		"content": text, // STRING — matches official plugin shape
		"meta":    meta,
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

// ─── MCP server wiring (modelcontextprotocol/go-sdk) ────────────────────────

// buildMCPServer constructs the SDK-backed MCP server, wires the
// `instructions` / `experimental.claude/channel` initialize-response
// fields, registers all 8 tool handlers, and installs a receiving
// middleware that flips the idle-startup watchdog disarm flag on the
// first incoming MCP frame.
//
// helloAck must be populated before calling — the `instructions` text
// depends on whether the broker reports no-config / no-mapping / auto-
// attached state.
func (a *adapter) buildMCPServer() *mcp.Server {
	opts := &mcp.ServerOptions{
		Instructions: a.buildInstructions(),
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{ListChanged: false},
			// Experimental.claude/channel matches the fakechat reference
			// (~/.claude/plugins/.../fakechat/server.ts) — and matches
			// what the hand-rolled MCP server declared pre-migration.
			// Without it, Claude Code's channel-dispatch validator may
			// silently drop our `notifications/claude/channel` frames.
			Experimental: map[string]any{
				"claude/channel": map[string]any{},
			},
		},
	}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    adapterName,
		Version: adapterVersion,
	}, opts)

	// Receiving middleware flips `dispatched` on first frame. This is the
	// integration point for the idle-startup watchdog: once any MCP frame
	// arrives, the watchdog is disarmed for the lifetime of the session.
	// ping/initialize/tools/list/tools/call all flow through here.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			a.dispatched.Store(true)
			if method != "ping" {
				log.Printf("mcp recv: method=%s", method)
			}
			return next(ctx, method, req)
		}
	})

	// Register the 8 tools. Schemas are passed as map[string]any (SDK
	// accepts any value that JSON-marshals to a valid object schema).
	a.registerTools(srv)
	return srv
}

// buildInstructions assembles the `instructions` text returned in the MCP
// initialize response. Composed of (a) a head line that reflects current
// helloAck state and (b) the shared mode.Combined() suffix (CLI/Telegram
// mode protocol + multi-part reply protocol). mode.Combined() is the
// single source of truth shared with the Codex adapter.
func (a *adapter) buildInstructions() string {
	var head string
	switch {
	case a.helloAck.NoConfig:
		head = "C3 not yet configured. Run `/c3:setup` (or `c3-broker setup`) to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart this session."
	case a.helloAck.NoMapping:
		cwd, _ := os.Getwd()
		head = fmt.Sprintf("No C3 mapping for %q. Use the `attach` tool to set one up — the broker proposes a topic named %q in the default group; confirm to create.", cwd, filepath.Base(cwd))
	case a.helloAck.AutoAttached && a.helloAck.Mapping != nil:
		m := a.helloAck.Mapping
		head = fmt.Sprintf("Auto-attached to %q (%s). Inbound messages render here as `<channel>` blocks.", m.Name, m.Channel)
	default:
		head = "C3 connected. Use the `attach` tool to claim a Telegram topic for this session."
	}
	return head + mode.Combined()
}

// registerTools adds all 8 adapter tools to srv. Each tool uses the SDK's
// raw ToolHandler (json.RawMessage args) so the schemas remain pure
// map[string]any — no struct-tag reflection. This matches the
// pre-migration hand-rolled wire shape.
func (a *adapter) registerTools(srv *mcp.Server) {
	tools := []struct {
		tool    *mcp.Tool
		handler mcp.ToolHandler
	}{
		{
			tool: &mcp.Tool{
				Name:        "attach",
				Description: "Attach this session to a Telegram topic. Either pass `expr` (raw user-supplied string the broker parses: empty=cwd-default, 'dm'=DM, '<int>'=topic-id, 'create <name>' or '-y <name>'=create that name, '<other>'=name) OR structured args. `target='dm'` for the user's DM. `name='X'` for a specific name. `topic_id=N` to claim a known thread id. `create=true` to confirm a creation proposal. `steal=true` to displace an existing alive holder (only after user-confirmed force_steal proposal).",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expr":     map[string]any{"type": "string"},
						"target":   map[string]any{"type": "string"},
						"name":     map[string]any{"type": "string"},
						"topic_id": map[string]any{"type": "integer"},
						"group":    map[string]any{"type": "string"},
						"create":   map[string]any{"type": "boolean"},
						"steal":    map[string]any{"type": "boolean"},
					},
				},
			},
			handler: a.toolAttach,
		},
		{
			tool: &mcp.Tool{
				Name:        "detach",
				Description: "Release this session's current Telegram topic claim. After detach, inbound messages on that route fall through to the broker's fallback. No-op if not attached.",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			handler: a.toolDetach,
		},
		{
			tool: &mcp.Tool{
				Name:        "topics",
				Description: "List known Telegram topics across all groups, with claim state.",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			handler: a.toolTopics,
		},
		{
			tool: &mcp.Tool{
				Name:        "reply",
				Description: "Send a Telegram reply to the currently-attached topic.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":       map[string]any{"type": "string"},
						"reply_to":   map[string]any{"type": "integer"},
						"parse_mode": map[string]any{"type": "string"},
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
		{
			tool: &mcp.Tool{
				Name:        "send_typing",
				Description: "Send a typing indicator to the attached topic.",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			handler: a.toolForward("send_typing"),
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
	}
	for _, t := range tools {
		srv.AddTool(t.tool, t.handler)
	}
}

// decodeArgs unmarshals the raw tool arguments into a generic map.
// Empty/null arguments are tolerated and yield an empty map.
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

// toolAttach implements the `attach` tool: send AttachReq to the broker
// and wait for an AttachedMsg. Mirrors the pre-migration handleAttachLocal.
func (a *adapter) toolAttach(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	cwd, _ := os.Getwd()
	attachReq := ipc.AttachReq{Op: ipc.OpAttach, CWD: cwd}
	if v, ok := args["expr"].(string); ok {
		attachReq.Expr = v
	}
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
			a.rememberAttach(attachReq)
			// Side-effect surface: write OSC-0 title-bar escape to
			// stderr so the user's terminal-emulator title reflects
			// the currently-attached topic. Closes TODO #19(a).
			// Gated on tty + C3_NO_TERMINAL_TITLE — see
			// internal/termtitle for the contract. Failure paths
			// (NeedsConfirmation, Status=no_topics_configured,
			// Status=policy_rejected, Err-set) never reach this
			// branch because they leave OK=false.
			termtitle.EmitAttach(&attached)
		}
		return toolTextResult(ipc.FormatAttached(&attached)), nil
	}
}

// toolDetach implements the `detach` tool: send OpRelease and forget the
// last-attach replay.
func (a *adapter) toolDetach(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker not connected"), nil
	}
	if err := conn.WriteJSON(struct {
		Op ipc.Op `json:"op"`
	}{Op: ipc.OpRelease}); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	a.amu.Lock()
	a.lastAttach = nil
	a.amu.Unlock()
	// Restore the terminal-emulator's default title — see
	// EmitAttach call-site comment in toolAttach for context.
	termtitle.Clear()
	return toolTextResult("detached"), nil
}

// toolTopics implements the `topics` tool.
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

// toolForward returns a ToolHandler that forwards the named tool call to
// the broker via ipc.OpToolCall and surfaces the broker's
// ipc.OpToolResult to the caller. Used for the broker-side tools
// (reply / react / edit_message / send_typing / download_attachment).
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

// toolTextResult wraps a string into the standard CallToolResult shape
// (one TextContent block). IsError stays false.
func toolTextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// toolErrorResult wraps an error message as an in-band tool error (IsError
// true). Per the SDK's guidance, errors that originate inside the tool
// should be reported as IsError=true in the result, not as protocol-level
// errors, so the LLM can see and self-correct.
func toolErrorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// toolResultFromMap converts a broker-returned result map into a
// CallToolResult. The broker may return either the standard MCP-style
// `{"content":[{"type":"text","text":…}]}` shape, in which case we
// translate it back into SDK content blocks; or a plain object, in which
// case we emit a single JSON-encoded text block.
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
	// Fallback: JSON-encode the whole result map.
	enc, err := json.Marshal(result)
	if err != nil {
		return toolErrorResult("marshal result: " + err.Error())
	}
	return toolTextResult(string(enc))
}
