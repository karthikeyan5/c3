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
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	mcpProtocolVersion = "2024-11-05"
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
// session resume teardown). Without this, the adapter sits in os.Stdin.Read
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

	// Last successful attach request — replayed on broker reconnect so a
	// session that survives a broker restart auto-reclaims its route. Nil
	// if the user hasn't attached yet (or detached).
	amu        sync.Mutex
	lastAttach *ipc.AttachReq

	// firstInbound triggers a one-shot wire dump of the first
	// notifications/claude/channel frame for live debugging.
	firstInbound atomic.Bool

	// dispatched is set the first time Dispatch is called — i.e. Claude
	// Code has sent at least one MCP frame. The idle-startup watchdog
	// uses this to distinguish "live session" from "orphaned spawn that
	// Claude Code never drove".
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
//
// Reconnect-once semantics from spec §4.4 are upgraded to reconnect-forever
// (with backoff) — the original behavior turned a 30-second broker rebuild
// into a permanently dead adapter that required restarting the CLI session.
func (a *adapter) brokerReader() {
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
			if !a.recoverBroker() {
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
// succeeds (or returns false on persistent ctx-cancel — currently unused
// since the adapter has no top-level ctx). After a successful reconnect,
// replays the last successful attach (best-effort) so the route claim is
// restored. Returns true on success, false if recovery is impossible.
func (a *adapter) recoverBroker() bool {
	const (
		base = 500 * time.Millisecond
		cap  = 30 * time.Second
	)
	backoff := base
	for attempt := 1; ; attempt++ {
		err := a.reconnectBroker()
		if err == nil {
			log.Printf("broker reconnected (attempt %d)", attempt)
			a.replayLastAttach()
			return true
		}
		log.Printf("broker reconnect attempt %d failed: %v (retry in %v)", attempt, err, backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > cap {
			backoff = cap
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
		// Copy and mark as replay so the broker can suppress the
		// on-attach welcome message — this isn't a user-initiated
		// attach, it's transparent recovery.
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
func (a *adapter) handleInbound(raw []byte) {
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

	if err := a.mcp.Notify("notifications/claude/channel", frame); err != nil {
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
//   - `chat_id` is a raw int64; everything else (ids, sizes) is stringified.
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
	// Mark the adapter as "driven" — disarms the idle-startup watchdog.
	a.dispatched.Store(true)

	// Log every method Claude Code sends — diagnosing "channel notification
	// silently dropped" requires knowing whether Claude Code is invoking some
	// method we reject. tools/call and tools/list are noisy but useful for
	// confirming basic flow.
	if req.Method != "ping" {
		log.Printf("mcp recv: method=%s id=%s notif=%v", req.Method, string(req.ID), req.IsNotification())
	}
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
			log.Printf("mcp recv: UNKNOWN notification method=%s (ignored)", req.Method)
			return nil
		}
		log.Printf("mcp recv: UNKNOWN request method=%s (returning -32601)", req.Method)
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
					// Match fakechat (the reference channel impl)
					// EXACTLY — only claude/channel. We previously also
					// declared claude/channel/permission to match the
					// official telegram plugin, but we don't implement
					// the permission protocol. Declaring an unfulfilled
					// capability may be why Claude Code silently drops
					// our channel notifications. fakechat declares ONLY
					// claude/channel and that's what we're testing.
					"claude/channel": map[string]any{},
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
			"description": "Attach this session to a Telegram topic. Either pass `expr` (raw user-supplied string the broker parses: empty=cwd-default, 'dm'=DM, '<int>'=topic-id, 'create <name>' or '-y <name>'=create that name, '<other>'=name) OR structured args. `target='dm'` for the user's DM. `name='X'` for a specific name. `topic_id=N` to claim a known thread id. `create=true` to confirm a creation proposal. `steal=true` to displace an existing alive holder (only after user-confirmed force_steal proposal).",
			"inputSchema": map[string]any{
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
		{
			"name":        "detach",
			"description": "Release this session's current Telegram topic claim. After detach, inbound messages on that route fall through to the broker's fallback. No-op if not attached.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
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
	case "detach":
		return a.handleDetachLocal(req)
	case "topics":
		return a.handleTopicsLocal(ctx, req)
	default:
		return a.forwardToBroker(req, params.Name, params.Arguments)
	}
}

// handleDetachLocal sends OpRelease so the broker frees this stub's
// claims. Stub.SetRoute(nil) is also performed broker-side. Idempotent
// (no-op if not attached).
func (a *adapter) handleDetachLocal(req *mcp.Request) *mcp.Response {
	conn := a.currentConn()
	if conn == nil {
		return errResp(req.ID, -32000, "broker not connected")
	}
	if err := conn.WriteJSON(struct {
		Op ipc.Op `json:"op"`
	}{Op: ipc.OpRelease}); err != nil {
		return errResp(req.ID, -32000, "broker write: "+err.Error())
	}
	a.amu.Lock()
	a.lastAttach = nil
	a.amu.Unlock()
	return mcpTextResp(req.ID, "detached")
}

func (a *adapter) handleAttachLocal(ctx context.Context, req *mcp.Request, args map[string]any) *mcp.Response {
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
		if attached.OK {
			a.rememberAttach(attachReq)
		}
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
		case "disambiguate_dm":
			ex := a.Proposal.Existing
			return fmt.Sprintf("Ambiguous: a topic named %q exists in group %q (thread %d). Did you mean attach to that topic, or to your actual Telegram DM? Confirm by calling attach(topic_id=%d) for the topic, or attach(target=\"dm\", steal=true) for the actual DM.",
				ex.Name, ex.Group, ex.TopicID, ex.TopicID)
		case "force_steal":
			h := a.Proposal.Holder
			return fmt.Sprintf("Topic %q is currently held by %s pid %d (cwd %q). Re-invoke attach with steal=true to evict that session and take the claim. Only do this if the user explicitly confirms.",
				a.Proposal.Name, h.CLI, h.PID, h.CWD)
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

// forwardToBroker forwards a tool call as ipc.OpToolCall and waits for the
// matching ipc.OpToolResult.
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
