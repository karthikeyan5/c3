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
	"github.com/karthikeyan5/c3/internal/spawn"
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

	go a.brokerReader(ctx)
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

	// fetch_queue / retranscribe have their own pending maps keyed by request
	// ID so the typed broker responses don't have to share the generic
	// pending[ToolResultMsg] slot. Mirrors the Claude adapter.
	fqmu      sync.Mutex
	fqPending map[string]chan ipc.FetchQueueResp
	rtmu      sync.Mutex
	rtPending map[string]chan ipc.RetranscribeResp

	helloAck ipc.HelloAckMsg

	// Last successful attach request — replayed on broker reconnect so a
	// session that survives a broker restart auto-reclaims its route (D3 /
	// adapter-ipc-3). Nil until the session attaches. (Codex has no detach tool
	// today, so it is never cleared once set.) Mirrors the Claude adapter's
	// rememberAttach/replayLastAttach machinery.
	amu        sync.Mutex
	lastAttach *ipc.AttachReq

	// dispatched is set the first time the SDK routes a method through the
	// receiving middleware — i.e. Codex has sent at least one MCP frame.
	dispatched atomic.Bool

	// brokerDownAdvised guards the D5 loud advisory so the "inbound is down"
	// SystemEvent is surfaced once per outage, not on every recovery cycle.
	// Reset on a successful reconnect so a later outage re-advises.
	brokerDownAdvised atomic.Bool
}

func newAdapter() *adapter {
	return &adapter{
		pending:   map[string]chan ipc.ToolResultMsg{},
		fqPending: map[string]chan ipc.FetchQueueResp{},
		rtPending: map[string]chan ipc.RetranscribeResp{},
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
// so it survives our shutdown. The detached-launch semantics (setsid, no
// stdio, async reap) live in internal/spawn, shared with the Claude adapter
// (D7) per Karthi's "every flow must work the same in Codex" principle.
//
// Closes report MINOR m3 (2026-05-19).
func spawnBroker() error {
	return spawn.Detached(exec.Command("c3-broker"))
}

func (a *adapter) hello() error {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := a.conn.WriteJSON(ipc.HelloMsg{
		Op: ipc.OpHello, CLI: "codex", PID: os.Getpid(), CWD: cwd,
		Capabilities: []string{"log-notification", "fetch_queue", "ws-forwarder"},
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
//
// D6 (adapter-ipc-7): this is FIRE-AND-FORGET. It deliberately does NOT
// register a waiter in the shared pending["attached"] slot. Previously both
// autoAttach and the `attach` tool registered under the same fixed key, so a
// startup race (auto-attach in flight when the agent calls `attach`) could
// strand one waiter or mis-route the AttachedMsg to the wrong one. AttachedMsg
// carries no correlation id (unlike tool_result, which echoes ToolCallReq.ID),
// so the two cannot be disambiguated by id without a broker protocol change.
// Since autoAttach never needs the response value (the result is reflected in
// helloAck on the next reconnect), we simply don't claim the slot — only the
// `attach` tool registers a waiter, eliminating the collision. The AttachedMsg
// is absorbed harmlessly by dispatchAttached (no waiter → no-op). We still
// remember the attach so D3 replay re-claims the route after a broker restart.
func (a *adapter) autoAttach(name string) {
	cwd := os.Getenv("C3_CODEX_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	conn := a.currentConn()
	if conn == nil {
		log.Printf("auto-attach skipped: broker not yet connected")
		return
	}
	req := ipc.AttachReq{Op: ipc.OpAttach, CWD: cwd, Name: name}
	if err := conn.WriteJSON(req); err != nil {
		log.Printf("auto-attach write failed: %v", err)
		return
	}
	// Record for D3 replay so a broker restart re-claims this auto-attached
	// route even though the session never called the `attach` tool.
	a.rememberAttach(req)
}

func (a *adapter) currentConn() *ipc.Conn {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return a.conn
}

// brokerReader runs in a goroutine, draining frames from the broker. On any
// read error it runs the recovery loop (exponential backoff, no give-up) until
// either ctx is cancelled or a usable connection is re-established (D1 /
// adapter-ipc-2). Previously this reconnected exactly ONCE (a one-shot flag),
// so the next broker bounce left the adapter silently dead. After recovery it
// replays the last successful attach so the route claim is restored without
// user intervention (D3 / adapter-ipc-3). Mirrors the Claude adapter's
// brokerReader/recoverBroker structure.
func (a *adapter) brokerReader(ctx context.Context) {
	for {
		conn := a.currentConn()
		if conn == nil {
			return
		}
		raw, err := conn.ReadFrame()
		if err != nil {
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
			a.handleInbound(raw)
		case ipc.OpToolResult:
			a.dispatchToolResult(raw)
		case ipc.OpAttached:
			a.dispatchAttached(raw)
		case ipc.OpTopicsList:
			a.dispatchTopicsList(raw)
		case ipc.OpFetchQueueResult:
			a.dispatchFetchQueueResult(raw)
		case ipc.OpRetranscribeResult:
			a.dispatchRetranscribeResult(raw)
		case ipc.OpError:
			var errMsg ipc.ErrorMsg
			_ = json.Unmarshal(raw, &errMsg)
			log.Printf("broker error: %s", errMsg.Err)
		}
	}
}

// reconnectBroker tears down the dead conn, dials a fresh one, sends hello.
// Pending tool calls are woken with an error so callers don't hang during the
// reconnect window. Single attempt; recoverBroker is the retry-loop wrapper.
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

// recoverBrokerAdviseAfter bounds how long the recovery loop retries silently
// before surfacing the D5 loud advisory. ~30s at the 0.5→30s backoff schedule
// is roughly 6 attempts (0.5+1+2+4+8+16 ≈ 31.5s), so attempt 6 is the trip
// point — long enough to ride out an ordinary broker rebuild, short enough that
// a real outage is surfaced before the user assumes inbound is working.
const recoverBrokerAdviseAfter = 6

// recoverBroker loops with exponential backoff until reconnectBroker succeeds,
// or ctx cancellation aborts the loop (returns false). After a successful
// reconnect it replays the last successful attach (best-effort) so the route
// claim is restored (D3). If the broker is still unreachable after
// recoverBrokerAdviseAfter attempts (~30s), it surfaces a one-shot loud
// advisory so the user knows inbound is down (D5) — the session otherwise looks
// alive. Mirrors the Claude adapter's recoverBroker.
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
			a.clearBrokerDownAdvisory()
			a.replayLastAttach()
			return true
		}
		log.Printf("broker reconnect attempt %d failed: %v (retry in %v)", attempt, err, backoff)
		if attempt >= recoverBrokerAdviseAfter {
			a.adviseBrokerDown(attempt)
		}
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

// adviseBrokerDown surfaces a LOUD one-shot advisory to the Codex session when
// the broker can't be re-established after several recovery cycles (D5 /
// adapter-ipc-5). It reuses the broker's SystemEvent shape (the same channel a
// broker-side health alert rides on) but synthesizes it adapter-side, since the
// whole point is that the broker is unreachable. brokerDownAdvised keeps it
// from spamming on every retry; it is cleared on the next successful reconnect
// so a later outage re-advises.
func (a *adapter) adviseBrokerDown(attempt int) {
	if !a.brokerDownAdvised.CompareAndSwap(false, true) {
		return
	}
	if a.transport == nil {
		return
	}
	sysev := &c3types.SystemEvent{
		Source:  "c3",
		Level:   "warn",
		Title:   "C3 broker unreachable",
		Message: fmt.Sprintf("C3 lost its connection to the broker and could not reconnect after %d attempts. Inbound Telegram messages will NOT arrive until this recovers (the adapter is still retrying in the background). Your phone messages won't reach this session meanwhile.", attempt),
	}
	body := "⚠️ SYSTEM: " + sysev.Message
	// The ring is retired; the broker's durable queue is the source of truth.
	// But a broker-DOWN advisory can't be queued (the broker is exactly what's
	// unreachable), so it is surfaced ONLY via the best-effort notify frame. If
	// that notify fails we log the FULL advisory so it is recoverable from
	// adapter.log — the same "don't lose undelivered content" rule the broker
	// follows (DEBUGGING.md).
	if err := a.transport.Notify(context.Background(), "notifications/message", map[string]any{
		"level":  "warning",
		"logger": "c3",
		"data":   body,
	}); err != nil {
		log.Printf("broker-down advisory notify failed: %v — %s", err, body)
	}
	log.Printf("broker-down advisory surfaced (attempt %d)", attempt)
}

// clearBrokerDownAdvisory re-arms the D5 advisory after a successful reconnect
// so a future outage surfaces a fresh advisory.
func (a *adapter) clearBrokerDownAdvisory() {
	a.brokerDownAdvised.Store(false)
}

// rememberAttach stores the last successful attach request for replay on
// reconnect (D3). The pointer captures all dimensions (target/name/topic_id/
// group/create) the session originally chose. Mirrors the Claude adapter.
func (a *adapter) rememberAttach(req ipc.AttachReq) {
	a.amu.Lock()
	defer a.amu.Unlock()
	cp := req
	a.lastAttach = &cp
}

// replayLastAttach re-sends the saved attach request to the (just-reconnected)
// broker so the route claim is re-established without user intervention (D3).
// Best-effort — failures are logged and not surfaced. The Replay flag tells the
// broker to suppress the on-attach welcome (a bounce isn't a user-initiated
// attach). The broker's AttachedMsg response flows back through brokerReader →
// dispatchAttached; no pending channel is registered for a replay, so it's
// simply absorbed. The point is to re-claim the route, not to confirm.
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

// handleInbound: emit a lightweight "new message — call fetch_queue" nudge +
// (if remote-bridge) WS-forward. The in-memory ring is RETIRED — the broker's
// durable queue is the single source of truth; Codex polls it via fetch_queue.
func (a *adapter) handleInbound(raw []byte) {
	var msg ipc.InboundMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	// Codex cannot render unsolicited content reliably, so it polls. Replace the
	// retired in-memory ring with a lightweight "N pending" nudge — the durable
	// queue in the broker is the source of truth; the agent calls fetch_queue.
	if a.transport != nil {
		if err := a.transport.Notify(context.Background(), "notifications/message", map[string]any{
			"data": "c3: new Telegram message — call `fetch_queue` to read it. " + pendingNudge(1),
		}); err != nil {
			// D4 (adapter-ipc-4): the broker already DURABLY QUEUED this inbound
			// before pushing it to us, so the content is never lost — it stays in
			// the queue until fetch_queue(ack=true) consumes it. The nudge is
			// best-effort; a notify failure is logged (with a content summary) so a
			// delivery problem is visible and recoverable from adapter.log, and the
			// agent can still call fetch_queue to drain the durable copy.
			thread := "-"
			if msg.Inbound.TopicID != nil {
				thread = strconv.FormatInt(*msg.Inbound.TopicID, 10)
			}
			log.Printf("notify FAIL chan=%s chat=%d thread=%s msg=%d: %v — content durably queued; call fetch_queue. %s",
				msg.Inbound.Channel, msg.Inbound.ChatID, thread, msg.Inbound.MessageID, err,
				inboundContentSummary(&msg.Inbound))
		}
	}

	// WS forwarder (gated by env, see split-brain guard).
	if codexForwardingAllowed() {
		go a.forwardToCodexAppServer(&msg.Inbound)
	}
}

// inboundContentSummary renders a one-line, content-bearing summary of an
// inbound for the notify-FAIL log path (D4). It includes content (sender,
// text, attachment summary) so a dropped notify is recoverable from
// adapter.log. Mirrors the Claude adapter's helper of the same name.
func inboundContentSummary(in *c3types.Inbound) string {
	var parts []string
	switch {
	case in.Sender.Username != "":
		parts = append(parts, "from=@"+in.Sender.Username)
	case in.Sender.UserID != 0:
		parts = append(parts, fmt.Sprintf("from=uid=%d", in.Sender.UserID))
	}
	if in.Text != "" {
		parts = append(parts, fmt.Sprintf("text=%q", capRunes(in.Text, 200)))
	}
	if in.ReplyTo != nil {
		parts = append(parts, fmt.Sprintf("reply_to=%d", in.ReplyTo.MessageID))
	}
	for _, att := range in.Attachments {
		parts = append(parts, fmt.Sprintf("attach=%s/%d", att.Kind, att.Size))
	}
	if in.IsEvent() {
		parts = append(parts, fmt.Sprintf("event=%s", in.Kind))
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " ")
}

// capRunes truncates s to at most n runes (rune-safe), appending an ellipsis
// when it trims — matches the broker's 200-char content-log cap so a multi-KB
// paste can't dump in full into adapter.log on a notify-fail.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
		head = fmt.Sprintf("No C3 mapping for %q. Use the `attach` tool to set one up. Inbound Telegram messages are held in C3's durable queue; call `fetch_queue` to read them.", cwd)
	default:
		head = "C3 connected. Use `attach` to claim a Telegram topic, `fetch_queue` to read held/new inbound, `reply` to send. Codex doesn't render unsolicited MCP notifications today; call `fetch_queue` when you see a 'new Telegram message' nudge or periodically."
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
				Name:        "fetch_queue",
				Description: "Retrieve inbound Telegram messages held in the durable queue for the attached topic (messages that arrived while no session was attached, or that a live push didn't confirm). `limit` is how many oldest messages to pull (default 3; or pass the string \"all\" to drain everything). `ack` (default true) consumes them (advances the cursor); ack=false peeks without consuming. Drain all at once for bulk catch-up, or pull in small batches (default 3) to process carefully one group at a time. Returns full content (text/transcript, sender, attachments with file_id) plus how many remain.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit": map[string]any{"description": "integer (default 3, max 50) or the string \"all\""},
						"ack":   map[string]any{"type": "boolean", "default": true},
					},
				},
			},
			handler: a.toolFetchQueue,
		},
		{
			tool: &mcp.Tool{
				Name:        "retranscribe",
				Description: "Re-run speech-to-text on a voice message by file_id (downloading the audio if not cached) and return the fresh transcript. Use this after a '[voice transcription failed]' message once the STT provider is healthy again — the audio is saved, so the user never has to resend. Optional `message_id` refreshes the stored transcript when that message is still queued.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_id":    map[string]any{"type": "string"},
						"message_id": map[string]any{"type": "integer"},
					},
					"required": []string{"file_id"},
				},
			},
			handler: a.toolRetranscribe,
		},
		{
			tool: &mcp.Tool{
				Name:        "reply",
				Description: "Send a Telegram reply to the currently-attached topic. The `text` is markdown — use formatting (lists, tables, code blocks, bold, block quotes) whenever it makes the reply easier to read; keep one-line answers plain. Attach media via the `media` array: kind=\"file\" delivers the ORIGINAL bytes (PDFs, logs); kind=\"photo\" is a COMPRESSED in-chat preview; also video/audio/voice/animation. Each item is sent as its own message after the text.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":     map[string]any{"type": "string"},
						"reply_to": map[string]any{"type": "integer"},
						"media":    mcptools.ReplyMediaSchema(caps),
						"buttons":  mcptools.ReplyButtonsSchema(),
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
			// D3 (adapter-ipc-3): remember the successful attach so a broker
			// restart re-claims this route via replayLastAttach. Parity with
			// the Claude adapter's toolAttach.
			a.rememberAttach(attachReq)
			// Side-effect surface: OSC-0 title-bar escape to stderr
			// for the currently-attached topic. Closes TODO #19(a).
			// Cross-CLI parity with the Claude adapter; same gates
			// (tty + C3_NO_TERMINAL_TITLE). See internal/termtitle.
			termtitle.EmitAttach(&attached)
		}
		text := ipc.FormatAttached(&attached)
		// Backlog summary on attach (spec Component 6): if the just-claimed route
		// has held inbound, tell the agent to call fetch_queue. Handles the
		// broker's degraded count-only case (QueuedCount>0 with empty
		// QueuedSummary) gracefully. Parity with the Claude adapter.
		if summary := renderBacklogSummary(attached.QueuedCount, attached.QueuedSummary); summary != "" {
			text += "\n\n" + summary
		}
		return toolTextResult(text), nil
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

// toolFetchQueue forwards a fetch_queue pull to the broker and renders the
// returned messages. The agent sees full content; the broker advanced the
// cursor (ack=true) before replying. The in-memory ring is RETIRED — the
// durable broker queue is the single source of truth (parity with the Claude
// adapter's toolFetchQueue).
func (a *adapter) toolFetchQueue(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	fq := ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: strconv.FormatUint(a.nextID.Add(1), 10), Ack: true}
	if v, ok := args["ack"].(bool); ok {
		fq.Ack = v
	}
	switch v := args["limit"].(type) {
	case string:
		if strings.EqualFold(v, "all") {
			fq.All = true
		}
	case float64:
		fq.Limit = int(v)
	}
	if !fq.All && fq.Limit <= 0 {
		fq.Limit = 3 // spec default
	}
	if fq.Limit > 50 {
		fq.Limit = 50
	}

	ch := make(chan ipc.FetchQueueResp, 1)
	a.fqmu.Lock()
	a.fqPending[fq.ID] = ch
	a.fqmu.Unlock()
	defer func() { a.fqmu.Lock(); delete(a.fqPending, fq.ID); a.fqmu.Unlock() }()

	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry fetch_queue in a moment"), nil
	}
	if err := conn.WriteJSON(fq); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case <-time.After(120 * time.Second):
		return toolErrorResult("fetch_queue timeout"), nil
	case resp := <-ch:
		if resp.Err != "" {
			return toolErrorResult(resp.Err), nil
		}
		return toolTextResult(renderFetchedMessages(resp.Messages, resp.Remaining)), nil
	}
}

// toolRetranscribe forwards a retranscribe request and returns the transcript.
func (a *adapter) toolRetranscribe(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	fileID, _ := args["file_id"].(string)
	if fileID == "" {
		return toolErrorResult("retranscribe: file_id is required"), nil
	}
	rt := ipc.RetranscribeReq{Op: ipc.OpRetranscribe, ID: strconv.FormatUint(a.nextID.Add(1), 10), FileID: fileID}
	if v, ok := args["message_id"].(float64); ok {
		rt.MessageID = int64(v)
	}
	ch := make(chan ipc.RetranscribeResp, 1)
	a.rtmu.Lock()
	a.rtPending[rt.ID] = ch
	a.rtmu.Unlock()
	defer func() { a.rtmu.Lock(); delete(a.rtPending, rt.ID); a.rtmu.Unlock() }()

	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry retranscribe in a moment"), nil
	}
	if err := conn.WriteJSON(rt); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case <-time.After(120 * time.Second):
		return toolErrorResult("retranscribe timeout"), nil
	case resp := <-ch:
		if resp.Err != "" {
			return toolErrorResult(resp.Err), nil
		}
		return toolTextResult(resp.Text), nil
	}
}

// dispatchFetchQueueResult routes a broker FetchQueueResp back to the waiting
// toolFetchQueue call by request ID.
func (a *adapter) dispatchFetchQueueResult(raw []byte) {
	var resp ipc.FetchQueueResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.fqmu.Lock()
	ch, ok := a.fqPending[resp.ID]
	if ok {
		delete(a.fqPending, resp.ID)
	}
	a.fqmu.Unlock()
	if ok {
		ch <- resp
	}
}

// dispatchRetranscribeResult routes a broker RetranscribeResp back to the
// waiting toolRetranscribe call by request ID.
func (a *adapter) dispatchRetranscribeResult(raw []byte) {
	var resp ipc.RetranscribeResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.rtmu.Lock()
	ch, ok := a.rtPending[resp.ID]
	if ok {
		delete(a.rtPending, resp.ID)
	}
	a.rtmu.Unlock()
	if ok {
		ch <- resp
	}
}

// renderBacklogSummary renders the on-attach backlog notification text. Empty
// string when nothing is queued. Instructs the agent to call fetch_queue.
//
// The broker may report count>0 with an EMPTY items slice (it degrades to
// count-only when Peek fails) — this still renders the count line + fetch_queue
// hint so the agent knows to drain, just without per-item previews. Byte-
// identical to the Claude adapter's renderBacklogSummary (Go has no cross-
// main-package sharing).
func renderBacklogSummary(count int, items []ipc.QueuedItem) string {
	if count <= 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📨 %d message(s) were held while no session was attached. Call `fetch_queue` (limit:3 or \"all\") to retrieve them.", count)
	for _, it := range items {
		preview := it.Preview
		if preview == "" {
			preview = "(" + it.Kind + ")"
		}
		fmt.Fprintf(&sb, "\n  • [%d] %s %s: %s", it.MessageID, it.Sender, it.Kind, preview)
	}
	if count > len(items) {
		fmt.Fprintf(&sb, "\n  …and %d more", count-len(items))
	}
	return sb.String()
}

// pendingNudge returns a "(N pending — call fetch_queue)" suffix, or "" when
// nothing is pending. Byte-identical to the Claude adapter's pendingNudge.
func pendingNudge(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("(%d pending — call `fetch_queue`)", n)
}

// renderFetchedMessages turns pulled inbound into agent-readable text, one block
// per message with full content + each attachment's file_id/mime/size/name so
// the agent can act on backlog voice/media (download_attachment / retranscribe).
// Byte-identical to the Claude adapter's renderFetchedMessages.
func renderFetchedMessages(msgs []c3types.Inbound, remaining int) string {
	if len(msgs) == 0 {
		return "c3 queue is empty"
	}
	blocks := make([]string, 0, len(msgs))
	for i := range msgs {
		blocks = append(blocks, renderQueuedInbound(&msgs[i]))
	}
	out := strings.Join(blocks, "\n\n")
	if remaining > 0 {
		out += "\n\n" + pendingNudge(remaining)
	}
	return out
}

// renderQueuedInbound renders one queued message for fetch_queue output. Unlike
// inboundContentSummary (notify-FAIL log line), this exposes the full attachment
// metadata the agent needs to recover backlog media: file_id, mime, size, name
// (spec Component 4 — load-bearing for the STT-failure recovery of backlog
// items, Component 6c). Byte-identical to the Claude adapter's
// renderQueuedInbound.
func renderQueuedInbound(in *c3types.Inbound) string {
	var parts []string
	switch {
	case in.Sender.Username != "":
		parts = append(parts, "from=@"+in.Sender.Username)
	case in.Sender.UserID != 0:
		parts = append(parts, fmt.Sprintf("from=uid=%d", in.Sender.UserID))
	}
	if in.MessageID != 0 {
		parts = append(parts, fmt.Sprintf("message_id=%d", in.MessageID))
	}
	if in.Text != "" {
		parts = append(parts, fmt.Sprintf("text=%q", in.Text))
	}
	if in.ReplyTo != nil {
		parts = append(parts, fmt.Sprintf("reply_to=%d", in.ReplyTo.MessageID))
	}
	for _, att := range in.Attachments {
		parts = append(parts, fmt.Sprintf("attachment{kind=%s file_id=%q mime=%s size=%d name=%q}",
			att.Kind, att.FileID, att.MIME, att.Size, att.Name))
	}
	if in.IsEvent() {
		parts = append(parts, fmt.Sprintf("event=%s", in.Kind))
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " ")
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
