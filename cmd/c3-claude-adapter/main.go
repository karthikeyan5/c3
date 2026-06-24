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
	"github.com/karthikeyan5/c3/internal/sessionhandoff"
	"github.com/karthikeyan5/c3/internal/spawn"
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
	go a.recoverSessionOnResume(ctx)
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

	// fetch_queue / retranscribe have their own pending maps keyed by request
	// id so their typed responses don't overload the ToolResultMsg pending map.
	fqmu      sync.Mutex
	fqPending map[string]chan ipc.FetchQueueResp
	rtmu      sync.Mutex
	rtPending map[string]chan ipc.RetranscribeResp

	// recover-session is one-shot per session (a session resumes at most once),
	// so a single buffered channel suffices instead of a pending map. Set by
	// recoverSessionOnResume before it writes RecoverSessionReq; resolved by
	// dispatchRecoverSessionResult. Guarded by rsmu.
	rsmu      sync.Mutex
	rsPending chan ipc.RecoverSessionResp

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

	// brokerDownAdvised guards the D5 one-shot "broker unreachable" advisory so
	// it surfaces once per outage, not on every recovery cycle. Cleared on a
	// successful reconnect so a later outage re-advises. Mirrors the Codex adapter.
	brokerDownAdvised atomic.Bool

	// dispatched is set the first time the SDK runs a method handler — i.e.
	// Claude Code has sent at least one MCP frame. The idle-startup watchdog
	// uses this to distinguish "live session" from "orphaned spawn that
	// Claude Code never drove". The receiving-middleware in buildMCPServer
	// flips this on first call.
	dispatched atomic.Bool
}

func newAdapter() *adapter {
	return &adapter{
		pending:   map[string]chan ipc.ToolResultMsg{},
		fqPending: map[string]chan ipc.FetchQueueResp{},
		rtPending: map[string]chan ipc.RetranscribeResp{},
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
// it survives our shutdown. The detached-launch semantics (setsid, no stdio,
// async reap) live in internal/spawn, shared with the Codex adapter (D7).
func spawnBroker() error {
	return spawn.Detached(exec.Command("c3-broker"))
}

// instanceIDFromEnv returns Claude Code's EPHEMERAL per-MCP-spawn id, exported
// to stdio MCP servers as CLAUDE_CODE_SESSION_ID. Despite the name, this is NOT
// the stable --resume id — it equals the UUID directory in the SessionStart
// hook's $CLAUDE_ENV_FILE path, so the adapter uses it to look up its own
// SessionStart-hook handoff (which carries the real stable id). Empty when unset
// (non-Claude-Code host / no hook) → recovery is skipped (fail-closed).
func instanceIDFromEnv() string { return os.Getenv("CLAUDE_CODE_SESSION_ID") }

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
		case ipc.OpFetchQueueResult:
			a.dispatchFetchQueueResult(raw)
		case ipc.OpRetranscribeResult:
			a.dispatchRetranscribeResult(raw)
		case ipc.OpRecoverSessionResult:
			a.dispatchRecoverSessionResult(raw)
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

// recoverBrokerAdviseAfter bounds how long recovery retries silently before the
// D5 loud advisory. ~30s at the 0.5→30s backoff (0.5+1+2+4+8+16 ≈ 31.5s), so
// attempt 6 trips it — long enough to ride out an ordinary broker rebuild, short
// enough that a real outage is surfaced before the user assumes inbound works.
const recoverBrokerAdviseAfter = 6

// recoverBroker loops with exponential backoff until reconnectBroker
// succeeds, or ctx cancellation aborts the loop (returns false).
// After a successful reconnect, replays the last successful attach
// (best-effort) so the route claim is restored. If the broker is still
// unreachable after recoverBrokerAdviseAfter attempts (~30s), it surfaces a
// one-shot loud advisory so the user knows inbound is down (D5 / adapter-ipc-5)
// — the session otherwise looks alive.
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

// adviseBrokerDown surfaces a LOUD one-shot advisory to the Claude session when
// the broker can't be re-established after several recovery cycles (D5 /
// adapter-ipc-5). It synthesizes the SAME broker-originated InboundSystem event
// the broker's health broadcast uses (ChatID-less, rendered via
// buildClaudeChannelFrame's System case — the proven-visible path), but emits it
// adapter-side since the whole point is that the broker is unreachable.
// brokerDownAdvised keeps it from spamming on every retry; it is cleared on the
// next successful reconnect. (Batch E's status line also shows "C3 broker down"
// when the broker PROCESS is dead; this covers the case where THIS adapter can't
// reach an otherwise-live broker.)
func (a *adapter) adviseBrokerDown(attempt int) {
	if !a.brokerDownAdvised.CompareAndSwap(false, true) {
		return
	}
	if a.notifyTx == nil {
		return
	}
	sysev := &c3types.SystemEvent{
		Source:  "c3",
		Level:   "warn",
		Title:   "C3 broker unreachable",
		Message: fmt.Sprintf("C3 lost its connection to the broker and could not reconnect after %d attempts. Inbound Telegram messages will NOT arrive until this recovers (the adapter is still retrying in the background).", attempt),
	}
	in := &c3types.Inbound{
		Channel: "c3",
		Kind:    c3types.InboundSystem,
		Event:   &c3types.InboundEvent{System: sysev},
	}
	frame := buildClaudeChannelFrame(in)
	if err := a.notifyTx.Notify(context.Background(), "notifications/claude/channel", frame); err != nil {
		log.Printf("broker-down advisory notify failed: %v", err)
		return
	}
	log.Printf("broker-down advisory surfaced (attempt %d)", attempt)
}

// clearBrokerDownAdvisory re-arms the D5 advisory after a successful reconnect.
func (a *adapter) clearBrokerDownAdvisory() {
	a.brokerDownAdvised.Store(false)
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
	if in.Inbound.IsEvent() {
		kind = string(in.Inbound.Kind) // poll_result / reaction / callback
	} else if len(in.Inbound.Attachments) > 0 && in.Inbound.Attachments[0].Kind != "" {
		kind = in.Inbound.Attachments[0].Kind
	}
	topic := "-"
	if in.Inbound.TopicID != nil {
		topic = strconv.FormatInt(*in.Inbound.TopicID, 10)
	}
	frame := buildClaudeChannelFrame(&in.Inbound)

	// Push-path recovery nudge (spec Component 3 — the push half of the recovery
	// net). When the broker reports remaining backlog (in.Pending > 0), append a
	// "(N pending — call fetch_queue)" suffix so a stuck backlog item surfaces on
	// THIS successful push, not only at the next re-attach.
	if s, ok := frame["content"].(string); ok {
		frame["content"] = decoratePushContent(s, in.Pending)
	}

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
		// D4 (adapter-ipc-4): the broker already counted this inbound as
		// "delivered" the moment it wrote it to our IPC socket — if the
		// adapter→CLI notify now fails, the message is otherwise lost with no
		// record anywhere. Log the FULL content (not just metadata) so it's
		// recoverable from adapter.log. This is the same "don't lose
		// undelivered content" rule the broker's failure paths follow
		// (DEBUGGING.md / worker.go fallbackSummary). A broker-side nack op
		// to bounce to the Telegram fallback is out of scope here.
		log.Printf("notify FAIL chan=%s chat=%d topic=%s msg=%d kind=%s: %v — LOST CONTENT: %s",
			in.Inbound.Channel, in.Inbound.ChatID, topic, in.Inbound.MessageID, kind, err,
			inboundContentSummary(&in.Inbound))
		return
	}
	log.Printf("notified chan=%s chat=%d topic=%s msg=%d kind=%s",
		in.Inbound.Channel, in.Inbound.ChatID, topic, in.Inbound.MessageID, kind)

	// Tell the broker we accepted this push so it Consumes the queued copy/copies.
	// Echo Covered back as Count so a MERGED push of N stored lines consumes all N
	// (not just 1, which would orphan N-1 as phantom backlog). This is broker↔
	// adapter plumbing the agent never sees (lifecycle B). On the notify-FAIL
	// branch above we returned WITHOUT acking — the message stays queued as
	// backlog, exactly as the recovery-nudge design requires.
	//
	// C1: a synthesized EVENT (poll_result / reaction / callback) is NEVER queued,
	// so it covers zero stored lines — do NOT send a delivered-ack for one. The
	// broker stamps Covered=1 via covEffective on a push (overridden to 0 for
	// events broker-side too), and handleConsume would otherwise Consume a real
	// queued backlog message the event never delivered, silently dropping it.
	if conn := a.currentConn(); conn != nil && !in.Inbound.IsEvent() {
		_ = conn.WriteJSON(ipc.InboundDeliveredMsg{Op: ipc.OpInboundDelivered, UpdateID: in.Inbound.MessageID, OK: true, Count: in.Covered})
	}
}

// inboundContentSummary renders a one-line, content-bearing summary of an
// inbound for the notify-FAIL log path (D4). UNLIKE the success path (which
// logs metadata only, per DEBUGGING.md), this is a failure path where the
// message is otherwise lost with no record — so we DO include content
// (sender, text, attachment summary) so it's recoverable from adapter.log.
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

// renderBacklogSummary renders the on-attach backlog notification text. Empty
// string when nothing is queued. Instructs the agent to call fetch_queue.
//
// The broker may report count>0 with an EMPTY items slice (it degrades to
// count-only when Peek fails) — this still renders the count line + fetch_queue
// hint so the agent knows to drain, just without per-item previews.
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
// nothing is pending. Appended to pushes + the attach summary so Claude can
// always recover even after a failed push.
func pendingNudge(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("(%d pending — call `fetch_queue`)", n)
}

// decoratePushContent appends the recovery nudge to a push's content string when
// the broker reports remaining backlog. Pure + unit-testable. (spec Component 3
// — the push half of the recovery net: a stuck backlog item surfaces on the next
// successful push, not only at the next re-attach.)
func decoratePushContent(content string, pending int) string {
	if nudge := pendingNudge(pending); nudge != "" {
		return content + "\n\n" + nudge
	}
	return content
}

// renderFetchedMessages turns pulled inbound into agent-readable text, one block
// per message with full content + each attachment's file_id/mime/size/name so
// the agent can act on backlog voice/media (download_attachment / retranscribe).
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
// (spec Component 4 — load-bearing for the STT-failure recovery of backlog items,
// Component 6c: the agent must be able to call download_attachment/retranscribe
// on a queued voice/media message).
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

// capRunes truncates s to at most n runes (rune-safe, so the log can't emit a
// split UTF-8 sequence), appending an ellipsis when it trims. Matches the
// broker's 200-char content-log cap (worker.go) so the failure-path logs are
// consistent and a multi-KB paste can't dump in full into adapter.log.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
	// P4: a synthesized channel event (poll_result / reaction / callback) renders
	// from its neutral Event payload rather than the message text/attachment path.
	// meta values stay strings (the frame contract requires Record<string,string>).
	if in.IsEvent() {
		content, evMeta := buildEventFrame(in)
		evMeta["chat_id"] = strconv.FormatInt(in.ChatID, 10)
		evMeta["ts"] = in.Timestamp.Format("2006-01-02T15:04:05.000Z")
		if in.MessageID != 0 {
			evMeta["message_id"] = strconv.FormatInt(in.MessageID, 10)
		}
		if in.TopicID != nil {
			evMeta["message_thread_id"] = strconv.FormatInt(*in.TopicID, 10)
		}
		return map[string]any{
			"content": content,
			"meta":    evMeta,
		}
	}

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
		// First attachment keeps the canonical unsuffixed keys (backward
		// compatible: single-attachment output is unchanged).
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
		// Multiple attachments (album / media-group, or a rich message with
		// several media blocks): surface EVERY attachment so the agent can
		// download each one. The first stays on the unsuffixed keys above;
		// extras get an _N suffix (N starts at 2). attachment_count is emitted
		// ONLY when there is more than one, so single-attachment frames are
		// byte-identical to before.
		if len(in.Attachments) > 1 {
			meta["attachment_count"] = strconv.Itoa(len(in.Attachments))
			for i := 1; i < len(in.Attachments); i++ {
				a := in.Attachments[i]
				n := strconv.Itoa(i + 1)
				if a.Kind != "" {
					meta["attachment_kind_"+n] = a.Kind
				}
				if a.FileID != "" {
					meta["attachment_file_id_"+n] = a.FileID
				}
				if a.Size > 0 {
					meta["attachment_size_"+n] = strconv.FormatInt(a.Size, 10)
				}
				if a.MIME != "" {
					meta["attachment_mime_"+n] = a.MIME
				}
				if a.Name != "" {
					meta["attachment_name_"+n] = a.Name
				}
			}
		}
	}

	text := in.Text
	if text == "" && len(in.Attachments) > 0 {
		// Channel may have left text empty for voice (STT plugin not yet
		// substituting). Fall back to a label so the agent at least sees
		// something. With several attachments (album/media-group), report the
		// count so the agent knows more than one arrived.
		if len(in.Attachments) > 1 {
			text = fmt.Sprintf("(%d attachments)", len(in.Attachments))
		} else {
			text = fmt.Sprintf("(%s message)", in.Attachments[0].Kind)
		}
	}

	return map[string]any{
		"content": text, // STRING — matches official plugin shape
		"meta":    meta,
	}
}

// buildEventFrame renders a synthesized channel event (poll_result / reaction /
// callback) into the channel-frame content string + a string-only meta map (the
// caller adds the common chat_id/ts/message_id keys). Keeps payloads simple
// strings — no structured Telegram types reach Claude Code (it would drop the
// notification). Returns a safe fallback for an unknown/empty event shape.
func buildEventFrame(in *c3types.Inbound) (string, map[string]any) {
	meta := map[string]any{"kind": string(in.Kind)}
	ev := in.Event
	switch {
	case ev != nil && ev.PollResult != nil:
		pr := ev.PollResult
		var b strings.Builder
		fmt.Fprintf(&b, "Poll results: %q — %d vote", pr.Question, pr.TotalVoters)
		if pr.TotalVoters != 1 {
			b.WriteString("s")
		}
		parts := make([]string, 0, len(pr.Options))
		for _, o := range pr.Options {
			parts = append(parts, fmt.Sprintf("%s:%d", o.Text, o.VoterCount))
		}
		if len(parts) > 0 {
			b.WriteString(" — ")
			b.WriteString(strings.Join(parts, " "))
		}
		if pr.IsClosed {
			b.WriteString(" (closed)")
		}
		meta["poll_id"] = pr.PollID
		meta["total_voters"] = strconv.Itoa(pr.TotalVoters)
		meta["is_closed"] = strconv.FormatBool(pr.IsClosed)
		return b.String(), meta

	case ev != nil && ev.Reaction != nil:
		r := ev.Reaction
		actor := senderLabel(r.Actor)
		var b strings.Builder
		fmt.Fprintf(&b, "%s reacted on message %d", actor, r.MessageID)
		if len(r.Added) > 0 {
			fmt.Fprintf(&b, " — added %s", strings.Join(r.Added, " "))
		}
		if len(r.Removed) > 0 {
			fmt.Fprintf(&b, " — removed %s", strings.Join(r.Removed, " "))
		}
		meta["message_id"] = strconv.FormatInt(r.MessageID, 10)
		if len(r.Added) > 0 {
			meta["added"] = strings.Join(r.Added, " ")
		}
		if len(r.Removed) > 0 {
			meta["removed"] = strings.Join(r.Removed, " ")
		}
		return b.String(), meta

	case ev != nil && ev.Callback != nil:
		cb := ev.Callback
		actor := senderLabel(cb.Actor)
		content := fmt.Sprintf("%s pressed a button (data=%q) on message %d", actor, cb.Data, cb.MessageID)
		meta["callback_id"] = cb.CallbackID
		meta["message_id"] = strconv.FormatInt(cb.MessageID, 10)
		meta["data"] = cb.Data
		return content, meta

	case ev != nil && ev.System != nil:
		// Broker-originated system advisory (e.g. a channel-health alert
		// broadcast to every CLI session). Surfaced loud so the user sees that
		// their phone messages won't arrive while the channel is down. NOT user
		// content — purely operational.
		sysev := ev.System
		content := "⚠️ SYSTEM: " + sysev.Message
		if sysev.Source != "" {
			meta["source"] = sysev.Source
		}
		if sysev.Level != "" {
			meta["level"] = sysev.Level
		}
		if sysev.Title != "" {
			meta["title"] = sysev.Title
		}
		return content, meta

	default:
		return fmt.Sprintf("(%s event)", in.Kind), meta
	}
}

// senderLabel renders a Sender into a short display label for event content.
func senderLabel(s c3types.Sender) string {
	if s.Username != "" {
		return "@" + s.Username
	}
	if s.UserID != 0 {
		return "user " + strconv.FormatInt(s.UserID, 10)
	}
	return "someone"
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
		// A successful attach may carry the just-claimed channel's manifest.
		// Store it as the latest caps so any subsequent instructions rebuild
		// (e.g. on a broker reconnect) reflects the attached channel. The MCP
		// `instructions` field itself is delivered only at initialize, so in
		// v1's single-channel world the hello_ack caps already cover the live
		// session; this keeps the adapter's stored caps fresh for the future
		// multi-channel turn-time-refresh seam (spec §L5).
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
		// Vestigial: the broker no longer auto-attaches at hello (recovery moved
		// to the post-hello RecoverSessionReq path, surfaced via a notification).
		// Kept harmless in case a future hello-time auto-attach is reintroduced.
		m := a.helloAck.Mapping
		head = fmt.Sprintf("Auto-attached to %q (%s) — resumed session. Inbound messages render here as `<channel>` blocks.", m.Name, m.Channel)
	default:
		head = "C3 connected. Use the `attach` tool to claim a Telegram topic for this session."
	}
	return head + mode.Combined(a.capsOrDefault())
}

// capsOrDefault returns the channel capability manifest the broker delivered
// on hello_ack (or a fresh attach), falling back to a sensible default when
// the broker is older than the CMG build (Capabilities==nil) or no channel was
// resolvable for this connection. The default is a zero Capabilities, which
// GuidanceFor renders as honest all-NO guidance — never a panic, never a
// fabricated capability.
func (a *adapter) capsOrDefault() c3types.Capabilities {
	if a.helloAck.Capabilities != nil {
		return *a.helloAck.Capabilities
	}
	return c3types.Capabilities{}
}

// registerTools adds all adapter tools to srv. Each tool uses the SDK's
// raw ToolHandler (json.RawMessage args) so the schemas remain pure
// map[string]any — no struct-tag reflection. This matches the
// pre-migration hand-rolled wire shape.
func (a *adapter) registerTools(srv *mcp.Server) {
	caps := a.capsOrDefault()
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
		text := ipc.FormatAttached(&attached)
		// Backlog-on-attach: surface any inbound held while no session was
		// attached (spec backlog-on-attach). renderBacklogSummary degrades
		// gracefully when the broker reports QueuedCount>0 with an EMPTY
		// QueuedSummary (count-only fallback when Peek failed): the agent still
		// gets the count + a fetch_queue hint.
		if summary := renderBacklogSummary(attached.QueuedCount, attached.QueuedSummary); summary != "" {
			text += "\n\n" + summary
		}
		return toolTextResult(text), nil
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
// (reply / react / edit_message / poll / download_attachment). Note:
// `send_typing` is NOT among these — the typing indicator is relayed
// programmatically by the broker (P5), not via an LLM tool call.
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

// parseFetchLimit normalizes the `limit` tool argument into (limit, all). The
// agent may pass "all" (drain everything), a JSON number, OR a numeric STRING
// like "5" (some MCP clients serialize an integer field as a string) — the last
// case previously matched neither the "all" nor the float64 arm and silently fell
// back to the default 3. A parseable numeric string is honored and clamped to
// [1,50]; "all" sets All; anything unparseable (or absent) yields the spec
// default of 3. Pure + unit-tested.
func parseFetchLimit(v any) (limit int, all bool) {
	switch t := v.(type) {
	case string:
		if strings.EqualFold(t, "all") {
			return 0, true
		}
		// A parseable numeric string ("5", "0", "999") is honored and clamped to
		// [1,50]; an unparseable string leaves limit 0 so it falls back to the
		// default below.
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			if n < 1 {
				n = 1
			}
			if n > 50 {
				n = 50
			}
			return n, false
		}
	case float64:
		limit = int(t)
	}
	if limit <= 0 {
		limit = 3 // spec default
	}
	if limit > 50 {
		limit = 50
	}
	return limit, false
}

// toolFetchQueue forwards a fetch_queue pull to the broker and renders the
// returned messages. The agent sees full content; the broker advanced the
// cursor (ack=true) before replying.
func (a *adapter) toolFetchQueue(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	fq := ipc.FetchQueueReq{Op: ipc.OpFetchQueue, ID: strconv.FormatUint(a.nextID.Add(1), 10), Ack: true}
	if v, ok := args["ack"].(bool); ok {
		fq.Ack = v
	}
	fq.Limit, fq.All = parseFetchLimit(args["limit"])

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

// dispatchFetchQueueResult routes an OpFetchQueueResult frame to the waiting
// toolFetchQueue caller, keyed by the request ID.
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

// dispatchRetranscribeResult routes an OpRetranscribeResult frame to the waiting
// toolRetranscribe caller, keyed by the request ID.
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

// recoverSessionParams tunes the post-hello handoff poll. The SessionStart hook
// fires AFTER the adapter spawns, with a VARIABLE delay — observed ~1.7s on one
// resume and ~2.6s on the next. The original 6×500ms (~2.5s) window raced that:
// when the hook write landed at ~2.6s the adapter's last read had already passed,
// so it gave up ~120ms early and auto-attach-on-resume silently no-op'd
// (2026-06-24). Poll generously — up to ~10s, matching recoverRespTimeout — so a
// slow hook can't lose the recovery; 10s is ~4× the observed worst case while
// still bounded. All values stay bounded: the poll runs in its own goroutine and
// never blocks hello / server start, and a non-c3 / fresh-non-resumed session
// simply polls the full window (cheap file stats) then returns silently.
const (
	recoverPollAttempts = 20
	recoverPollInterval = 500 * time.Millisecond
	recoverRespTimeout  = 10 * time.Second
)

// recoverSessionOnResume runs in a goroutine after hello. It looks up this
// adapter's SessionStart-hook handoff (keyed on the ephemeral instance id), and
// if found, asks the broker to re-attach the resumed session to its last topic
// (keyed on the STABLE session id from the handoff). On a Recovered response
// with held backlog, it emits ONE notification telling the agent it was
// auto-attached and to call fetch_queue.
//
// Fail-closed throughout: no instance id (non-Claude-Code host), no handoff
// (non-c3 / fresh non-resumed session), broker write/timeout — all return
// silently → today's no-recovery behavior, zero regression. The bounded poll
// lives entirely in this goroutine; it never delays hello or the MCP server.
func (a *adapter) recoverSessionOnResume(ctx context.Context) {
	inst := instanceIDFromEnv()
	if inst == "" {
		return
	}
	var entry sessionhandoff.Entry
	found := false
	for i := 0; i < recoverPollAttempts; i++ {
		if e, ok := sessionhandoff.Read(inst); ok {
			entry, found = e, true
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(recoverPollInterval):
		}
	}
	if !found {
		return // non-hook / non-c3 / non-resumed session — nothing to recover.
	}

	respCh := make(chan ipc.RecoverSessionResp, 1)
	a.rsmu.Lock()
	a.rsPending = respCh
	a.rsmu.Unlock()
	defer func() {
		a.rsmu.Lock()
		if a.rsPending == respCh {
			a.rsPending = nil
		}
		a.rsmu.Unlock()
	}()

	conn := a.currentConn()
	if conn == nil {
		return
	}
	if err := conn.WriteJSON(ipc.RecoverSessionReq{
		Op: ipc.OpRecoverSession, StableSessionID: entry.StableSessionID, CWD: entry.CWD,
	}); err != nil {
		log.Printf("recover-session: write failed: %v", err)
		return
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(recoverRespTimeout):
		log.Printf("recover-session: no response within %v", recoverRespTimeout)
		return
	case resp := <-respCh:
		if resp.Err != "" {
			log.Printf("recover-session: broker err: %s", resp.Err)
			return
		}
		if !resp.Recovered {
			return // already attached, or nothing recoverable — stay quiet.
		}
		// Remember the recovered attach so a later broker reconnect replays it.
		a.rememberAttach(ipc.AttachReq{
			Op: ipc.OpAttach, CWD: entry.CWD, Name: resp.Name, Group: resp.Group,
		})
		log.Printf("recover-session: auto-attached to %q (queued=%d)", resp.Name, resp.QueuedCount)
		if text := renderRecoverNotice(resp); text != "" {
			a.emitRecoverNotice(text)
		}
	}
}

// dispatchRecoverSessionResult resolves the one-shot recover-session pending
// channel. Safe to call when no recover is in flight (the channel is nil).
func (a *adapter) dispatchRecoverSessionResult(raw []byte) {
	var resp ipc.RecoverSessionResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.rsmu.Lock()
	ch := a.rsPending
	a.rsPending = nil
	a.rsmu.Unlock()
	if ch != nil {
		select {
		case ch <- resp:
		default:
		}
	}
}

// renderRecoverNotice builds the one-shot auto-attach notice. With held backlog
// it nudges fetch_queue; with none it's a minimal "auto-attached" line. Returns
// "" only when nothing useful can be said (no name).
func renderRecoverNotice(resp ipc.RecoverSessionResp) string {
	name := resp.Name
	if name == "" {
		return ""
	}
	if resp.QueuedCount > 0 {
		noun := "message"
		if resp.QueuedCount > 1 {
			noun = "messages"
		}
		return fmt.Sprintf("📨 Auto-attached to %q (resumed session). %d %s held — call `fetch_queue` to retrieve.",
			name, resp.QueuedCount, noun)
	}
	return fmt.Sprintf("📨 Auto-attached to %q (resumed session). Inbound messages render here as `<channel>` blocks.", name)
}

// emitRecoverNotice surfaces the auto-attach notice to the Claude session via
// the same broker-originated system-event path the broker-down advisory uses
// (the proven-visible channel frame). No-op when the notify transport isn't up.
func (a *adapter) emitRecoverNotice(text string) {
	if a.notifyTx == nil {
		return
	}
	in := &c3types.Inbound{
		Channel: "c3",
		Kind:    c3types.InboundSystem,
		Event: &c3types.InboundEvent{System: &c3types.SystemEvent{
			Source: "c3", Level: "info", Title: "C3 auto-attached", Message: text,
		}},
	}
	frame := buildClaudeChannelFrame(in)
	if err := a.notifyTx.Notify(context.Background(), "notifications/claude/channel", frame); err != nil {
		log.Printf("recover-session: notify failed: %v", err)
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
