// c3-desktop-adapter is the Claude Desktop MCP server that bridges Claude
// Desktop's MCP stdio protocol to the C3 broker over $XDG_RUNTIME_DIR/c3.sock.
//
// Claude Desktop is a GUI chat app that is POLL-ONLY: it cannot render
// unsolicited server notifications and cannot start a turn on its own, and it
// exposes NO per-conversation id, NO project cwd, and NO session-start hook to
// its MCP servers. So this adapter is a pull bridge — structurally the Antigravity
// (agy) adapter, NOT the Claude Code adapter (which relies on live channel push
// Desktop lacks). Since the host cannot render channel frames the adapter sends
// `cannot_render_channels: true` in hello; the broker holds inbound in its durable
// queue and the agent retrieves it with the `fetch_queue` tool.
//
// Outbound tools (attach, reply, …) are broker-forwarded like the Codex/Grok/agy
// adapters.
//
// MCP wire layer: github.com/modelcontextprotocol/go-sdk.
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
	"github.com/karthikeyan5/c3/internal/osutil"
	"github.com/karthikeyan5/c3/internal/spawn"
	"github.com/karthikeyan5/c3/internal/termtitle"
)

const (
	// adapterName MUST match the MCP server key in the Claude Desktop config
	// (claude_desktop_config.json mcpServers.c3).
	adapterName    = "c3"
	adapterVersion = "0.1.0"

	idleStartupTimeout = 60 * time.Second // mirror cmd/c3-claude-adapter behavior
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-desktop-adapter: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Persistent adapter log at $XDG_STATE_HOME/c3/adapter.log.
	if path, err := setupAdapterLog(); err == nil {
		fmt.Fprintf(os.Stderr, "c3-desktop-adapter: log file %s\n", path)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := newAdapter()
	a.runCtx = ctx
	installSignalHandlers(cancel)

	if err := a.connectBroker(); err != nil {
		log.Printf("adapter: exit pid=%d reason=connect-broker err=%v", os.Getpid(), err)
		return fmt.Errorf("connect broker: %w", err)
	}
	if err := a.hello(); err != nil {
		log.Printf("adapter: exit pid=%d reason=hello err=%v", os.Getpid(), err)
		return fmt.Errorf("hello: %w", err)
	}

	srv := a.buildMCPServer()
	a.transport = newLogNotifyTransport(&mcp.StdioTransport{})

	go a.brokerReader(ctx)
	go a.idleStartupWatchdog(ctx, cancel)
	// No auto-attach: Claude Desktop provides no per-conversation id and no
	// session-start hook, so there is nothing to key an on-resume recovery on.
	// Attachment is explicit-only (the user calls the `attach` tool); the stable
	// session id is registered lazily at that first attach — see toolAttach.

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

func installSignalHandlers(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, append([]os.Signal{syscall.SIGTERM, syscall.SIGINT}, osutil.ReloadSignals()...)...)
	go func() {
		sig := <-ch
		log.Printf("adapter: received signal=%v pid=%d", sig, os.Getpid())
		cancel()
	}()
}

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
	log.Printf("adapter: started pid=%d cli=desktop", os.Getpid())
	return path, nil
}

// desktopCWD resolves the cwd sent in hello and used for the picker seed. Claude
// Desktop does not hand its MCP servers a project cwd, so this is best-effort
// (often the app's working directory) and MUST NOT be relied on for a mapping.
// An explicit C3_DESKTOP_CWD override wins (used for testing / power users).
func desktopCWD() string {
	if cwd := strings.TrimSpace(os.Getenv("C3_DESKTOP_CWD")); cwd != "" {
		return cwd
	}
	cwd, _ := os.Getwd()
	return cwd
}

// sessionID is the stable id used for attach/claim continuity. Claude Desktop
// gives its MCP servers no per-conversation id, so we let the user pin one via
// C3_DESKTOP_SESSION (stable across restarts → the same claim can be reclaimed on
// an explicit re-attach). Unset → a per-process id, which is distinct every launch
// (no cross-process continuity, by design).
func sessionID() string {
	if sid := strings.TrimSpace(os.Getenv("C3_DESKTOP_SESSION")); sid != "" {
		return sid
	}
	return fmt.Sprintf("desktop-%d", os.Getpid())
}

type adapter struct {
	transport *logNotifyTransport

	bmu  sync.Mutex
	conn *ipc.Conn

	pmu     sync.Mutex
	pending map[string]chan ipc.ToolResultMsg
	nextID  atomic.Uint64

	fqmu      sync.Mutex
	fqPending map[string]chan ipc.FetchQueueResp
	rtmu      sync.Mutex
	rtPending map[string]chan ipc.RetranscribeResp

	rsmu      sync.Mutex
	rsPending chan ipc.RecoverSessionResp

	recoverFired atomic.Bool
	runCtx       context.Context

	helloAck ipc.HelloAckMsg

	amu           sync.Mutex
	lastAttach    *ipc.AttachReq
	attachedTopic string

	brokerDownAdvised atomic.Bool
	dispatched        atomic.Bool
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

func spawnBroker() error {
	return spawn.Detached(exec.Command("c3-broker"))
}

func (a *adapter) hello() error {
	if err := a.conn.WriteJSON(ipc.HelloMsg{
		Op: ipc.OpHello, CLI: "desktop", PID: os.Getpid(), CWD: desktopCWD(),
		Capabilities:         []string{"fetch_queue"},
		CannotRenderChannels: true,
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

func (a *adapter) currentConn() *ipc.Conn {
	a.bmu.Lock()
	defer a.bmu.Unlock()
	return a.conn
}

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
		case ipc.OpInbound:
			var msg ipc.InboundMsg
			if err := json.Unmarshal(raw, &msg); err == nil {
				if msg.Inbound.Kind == c3types.InboundSystem {
					a.handleSystemInbound(&msg.Inbound)
				}
			}
		case ipc.OpError:
			var errMsg ipc.ErrorMsg
			_ = json.Unmarshal(raw, &errMsg)
			log.Printf("broker error: %s", errMsg.Err)
		}
	}
}

func (a *adapter) handleSystemInbound(in *c3types.Inbound) {
	if in.Event == nil || in.Event.System == nil {
		return
	}
	sys := in.Event.System
	body := fmt.Sprintf("⚠️ [%s] %s: %s", sys.Level, sys.Title, sys.Message)
	if a.transport != nil {
		_ = a.transport.Notify(context.Background(), "notifications/message", map[string]any{
			"level":  "warning",
			"logger": "c3",
			"data":   body,
		})
	}
	log.Printf("system inbound: %s", body)
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

const recoverBrokerAdviseAfter = 6

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
		Message: fmt.Sprintf("C3 lost its connection to the broker and could not reconnect after %d attempts. Inbound Telegram messages will NOT arrive until this recovers. Your phone messages won't reach this session meanwhile.", attempt),
	}
	body := "⚠️ SYSTEM: " + sysev.Message
	if err := a.transport.Notify(context.Background(), "notifications/message", map[string]any{
		"level":  "warning",
		"logger": "c3",
		"data":   body,
	}); err != nil {
		log.Printf("broker-down advisory notify failed: %v — %s", err, body)
	}
	log.Printf("broker-down advisory surfaced (attempt %d)", attempt)
}

func (a *adapter) clearBrokerDownAdvisory() {
	a.brokerDownAdvised.Store(false)
}

// fireRecover registers the stable session id with the broker (the only IPC lever
// that binds a stub → stable id) and, if that session had a prior route, re-claims
// it. Called ONCE, lazily, from the first explicit attach — there is no startup or
// on-resume auto-fire, because Claude Desktop offers no session-start hook or
// conversation id to key one on. With C3_DESKTOP_SESSION unset the id is
// per-process, so recovery is always a no-op (nothing prior to reclaim) and this
// simply binds the claim to the id for the life of the process.
func (a *adapter) fireRecover(ctx context.Context, stableID, cwd string) {
	if stableID == "" {
		return
	}
	if !a.recoverFired.CompareAndSwap(false, true) {
		return
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
	req := ipc.RecoverSessionReq{Op: ipc.OpRecoverSession, StableSessionID: stableID, CWD: cwd}
	if err := conn.WriteJSON(req); err != nil {
		log.Printf("recover session write failed: %v", err)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(8 * time.Second):
		log.Printf("recover session timed out")
	case resp := <-respCh:
		if resp.Err != "" {
			log.Printf("recover session failed: %s", resp.Err)
			return
		}
		if !resp.Recovered {
			log.Printf("recover-session: session=%s registered (no prior attachment to re-claim)", stableID)
			return
		}
		a.rememberAttach(rememberedIdentityReq(cwd, resp.ChatID, resp.TopicID, resp.Group))
		a.setAttachedTopic(resp.Name)
		log.Printf("recover-session: re-attached to %q (queued=%d)", resp.Name, resp.QueuedCount)
		if text := renderDesktopRecoverNotice(resp); text != "" {
			a.emitRecoverNotice(text)
		}
	}
}

func renderDesktopRecoverNotice(resp ipc.RecoverSessionResp) string {
	name := resp.Name
	if name == "" {
		return ""
	}
	if resp.QueuedCount > 0 {
		noun := "message"
		if resp.QueuedCount != 1 {
			noun = "messages"
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "C3: re-attached to %q (session continuity). ~%d %s held — call fetch_queue (limit:\"all\") to drain:",
			name, resp.QueuedCount, noun)
		for _, it := range resp.QueuedSummary {
			preview := it.Preview
			if preview == "" {
				preview = "(" + it.Kind + ")"
			}
			fmt.Fprintf(&sb, "\n  • [%d] %s %s: %s", it.MessageID, it.Sender, it.Kind, preview)
		}
		return sb.String()
	}
	return fmt.Sprintf("C3: re-attached to %q (session continuity). Inbound Telegram messages are held in C3's durable queue; call `fetch_queue` to read them.", name)
}

func (a *adapter) emitRecoverNotice(text string) {
	if a.transport == nil || text == "" {
		return
	}
	_ = a.transport.Notify(context.Background(), "notifications/message", map[string]any{
		"level":  "info",
		"logger": "c3",
		"data":   text,
	})
}

func (a *adapter) rememberAttach(req ipc.AttachReq) {
	a.amu.Lock()
	defer a.amu.Unlock()
	cp := req
	cp.Steal = false
	a.lastAttach = &cp
}

func (a *adapter) setAttachedTopic(name string) {
	a.amu.Lock()
	defer a.amu.Unlock()
	a.attachedTopic = name
}

func (a *adapter) currentTopicName() string {
	a.amu.Lock()
	defer a.amu.Unlock()
	return a.attachedTopic
}

func isBareAttachReq(req ipc.AttachReq) bool {
	return req.Expr == "" && req.Target == "" && req.Name == "" && req.TopicID == nil && !req.Create
}

func rememberedIdentityReq(cwd string, chatID int64, topicID *int64, group string) ipc.AttachReq {
	req := ipc.AttachReq{Op: ipc.OpAttach, CWD: cwd}
	if topicID == nil {
		req.Target = "dm"
		return req
	}
	tid := *topicID
	req.TopicID = &tid
	req.Group = group
	req.ChatID = chatID
	return req
}

func resolvedAttachReq(req ipc.AttachReq, attached ipc.AttachedMsg) ipc.AttachReq {
	if !isBareAttachReq(req) {
		return req
	}
	return rememberedIdentityReq(req.CWD, attached.ChatID, attached.TopicID, attached.Group)
}

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

func (a *adapter) dispatchToolResult(raw []byte) {
	var msg ipc.ToolResultMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	a.pmu.Lock()
	ch, ok := a.pending[msg.ID]
	delete(a.pending, msg.ID)
	a.pmu.Unlock()
	if ok {
		ch <- msg
	}
}

func (a *adapter) dispatchAttached(raw []byte) {
	var msg ipc.AttachedMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	a.pmu.Lock()
	ch, ok := a.pending["attached"]
	delete(a.pending, "attached")
	a.pmu.Unlock()
	if ok {
		ch <- ipc.ToolResultMsg{Result: map[string]any{"_attached": msg}}
	}
}

func (a *adapter) dispatchTopicsList(raw []byte) {
	var msg ipc.TopicsListMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	a.pmu.Lock()
	ch, ok := a.pending["topics_list"]
	delete(a.pending, "topics_list")
	a.pmu.Unlock()
	if ok {
		ch <- ipc.ToolResultMsg{Result: map[string]any{"_topics_list": msg}}
	}
}

func (a *adapter) dispatchFetchQueueResult(raw []byte) {
	var resp ipc.FetchQueueResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.fqmu.Lock()
	ch, ok := a.fqPending[resp.ID]
	delete(a.fqPending, resp.ID)
	a.fqmu.Unlock()
	if ok {
		ch <- resp
	}
}

func (a *adapter) dispatchRetranscribeResult(raw []byte) {
	var resp ipc.RetranscribeResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.rtmu.Lock()
	ch, ok := a.rtPending[resp.ID]
	delete(a.rtPending, resp.ID)
	a.rtmu.Unlock()
	if ok {
		ch <- resp
	}
}

func (a *adapter) dispatchRecoverSessionResult(raw []byte) {
	var resp ipc.RecoverSessionResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	a.rsmu.Lock()
	ch := a.rsPending
	a.rsmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

func (a *adapter) idleStartupWatchdog(ctx context.Context, cancel context.CancelFunc) {
	timer := time.NewTimer(idleStartupTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		if !a.dispatched.Load() {
			log.Printf("adapter: idle-startup timeout pid=%d (no MCP frame in %v) — exiting so host can respawn",
				os.Getpid(), idleStartupTimeout)
			cancel()
		}
	}
}

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
	a.registerPrompts(srv)
	return srv
}

// registerPrompts declares C3's MCP prompts. Claude Desktop surfaces MCP prompts
// as slash commands, so `fetchq` gives the user a one-keystroke `/fetchq` that
// pulls the durable queue in a single deterministic step — no "please check my
// messages" sentence and no tool-call reasoning turn to trigger the fetch. The
// returned message is injected by the client for the model to read/act on.
// Registering a prompt auto-advertises the server `prompts` capability.
func (a *adapter) registerPrompts(srv *mcp.Server) {
	srv.AddPrompt(&mcp.Prompt{
		Name:        "fetchq",
		Title:       "Fetch C3 queue",
		Description: "Pull inbound Telegram messages held in C3's durable queue for the attached topic and drop them straight into the chat — a one-keystroke alternative to asking Claude to call fetch_queue. Drains everything by default; pass limit=N for the N oldest, or ack=false to peek without consuming.",
		Arguments: []*mcp.PromptArgument{
			{Name: "limit", Description: "How many oldest messages to pull: a number, or \"all\" (default)."},
			{Name: "ack", Description: "\"false\" to peek without consuming (leaves them queued). Default \"true\" — drain."},
		},
	}, a.promptFetchq)
}

// promptFetchq backs the `fetchq` slash command. It defaults to draining the whole
// queue (a slash command is an explicit "show me what's waiting"); `limit` narrows
// it and `ack=false` peeks. The queued messages are returned as a user-role prompt
// message so the client injects them for the model to read and act on.
func (a *adapter) promptFetchq(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	limit, all := 0, true // default: drain all
	ack := true
	if req != nil && req.Params != nil {
		if v, ok := req.Params.Arguments["limit"]; ok {
			limit, all = parseFetchLimitStr(v)
		}
		if v, ok := req.Params.Arguments["ack"]; ok {
			v = strings.TrimSpace(v)
			ack = !(strings.EqualFold(v, "false") || v == "0" || strings.EqualFold(v, "no"))
		}
	}

	body, _ := a.doFetchQueue(ctx, ack, limit, all)
	text := "📨 C3 queue (via /fetchq):\n\n" + body
	if ack {
		text += "\n\nRead these and respond or act as needed — use the `reply` tool to answer on Telegram."
	} else {
		text += "\n\n(peeked — still queued; run without ack=false to consume.)"
	}

	return &mcp.GetPromptResult{
		Description: "C3 queued Telegram messages",
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: text}},
		},
	}, nil
}

func (a *adapter) buildInstructions() string {
	var head string
	switch {
	case a.helloAck.NoConfig:
		head = "C3 is not yet configured. Run `c3-broker setup` from a shell to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart Claude Desktop."
	default:
		// Claude Desktop is poll-only (no per-conversation id, no session-start
		// hook, cannot render unsolicited pushes), so the NoMapping and mapped
		// cases collapse to one honest instruction: attach explicitly, then poll.
		head = "C3 connected. Claude Desktop is POLL-ONLY — it cannot render unsolicited Telegram pushes, so nothing arrives on its own. Attach to a topic explicitly with `attach(name=\"<topic>\")` (or `attach(target=\"dm\")` for your DM); `attach` with no argument returns a picker to surface for the user. Inbound Telegram messages are held in C3's durable queue — call `fetch_queue` to read new/held messages (Desktop will not pop them for you), or the user can run the `/fetchq` slash command to drop the queue straight into the chat without asking. Send replies and reactions with the `reply`/`react` tools when the user asks."
	}
	return head + mode.Combined(a.capsOrDefault())
}

func (a *adapter) capsOrDefault() c3types.Capabilities {
	if a.helloAck.Capabilities != nil {
		return *a.helloAck.Capabilities
	}
	return c3types.Capabilities{}
}

func (a *adapter) toolForward(name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req.Params.Arguments)
		if err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		id := strconv.FormatUint(a.nextID.Add(1), 10)
		ch := make(chan ipc.ToolResultMsg, 1)
		a.pmu.Lock()
		a.pending[id] = ch
		a.pmu.Unlock()
		defer func() { a.pmu.Lock(); delete(a.pending, id); a.pmu.Unlock() }()

		conn := a.currentConn()
		if conn == nil {
			return toolErrorResult("broker reconnecting — retry " + name + " in a moment"), nil
		}
		if err := conn.WriteJSON(ipc.ToolCallReq{Op: ipc.OpToolCall, ID: id, Name: name, Args: args}); err != nil {
			return toolErrorResult("broker write: " + err.Error()), nil
		}
		select {
		case <-ctx.Done():
			return toolErrorResult("canceled"), nil
		case <-time.After(120 * time.Second):
			return toolErrorResult(name + " timeout"), nil
		case res := <-ch:
			if res.Error != nil {
				return toolErrorResult(res.Error.Message), nil
			}
			return mapResult(res.Result), nil
		}
	}
}

func (a *adapter) toolAttach(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	cwd := desktopCWD()
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
	// Always clear the pending entry on return (incl. the ctx.Done() path), so a
	// canceled attach can't leave a stale channel that a later same-key attach's
	// broker response is misdelivered to. Matches toolForward/toolFetchQueue.
	defer func() {
		a.pmu.Lock()
		delete(a.pending, "attached")
		a.pmu.Unlock()
	}()

	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry attach in a moment"), nil
	}

	// Register the stable session id before attach so the broker binds this
	// session → route for attach/claim continuity. sessionID() is always
	// non-empty; fireRecover fires at most once per process.
	a.fireRecover(ctx, sessionID(), cwd)

	if err := conn.WriteJSON(attachReq); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	select {
	case <-ctx.Done():
		return toolErrorResult("canceled"), nil
	case res := <-ch:
		attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
		if attached.OK {
			a.rememberAttach(resolvedAttachReq(attachReq, attached))
			a.setAttachedTopic(attached.Name)
			termtitle.EmitAttach(&attached)
		}
		text := ipc.FormatAttached(&attached)
		if summary := renderBacklogSummary(attached.QueuedCount, attached.QueuedSummary, attached.Name); summary != "" {
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
	defer func() {
		a.pmu.Lock()
		delete(a.pending, "topics_list")
		a.pmu.Unlock()
	}()
	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry topics in a moment"), nil
	}
	if err := conn.WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics}); err != nil {
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

func (a *adapter) toolDetach(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	conn := a.currentConn()
	if conn == nil {
		return toolErrorResult("broker reconnecting — retry detach in a moment"), nil
	}
	req := ipc.ReleaseReq{Op: ipc.OpRelease}
	if err := conn.WriteJSON(req); err != nil {
		return toolErrorResult("broker write: " + err.Error()), nil
	}
	a.amu.Lock()
	a.lastAttach = nil
	a.attachedTopic = ""
	a.amu.Unlock()
	termtitle.Clear()
	return toolTextResult("detached successfully"), nil
}

func (a *adapter) toolFetchQueue(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	ack := true
	if v, ok := args["ack"].(bool); ok {
		ack = v
	}
	limit, all := parseFetchLimit(args["limit"])

	text, isErr := a.doFetchQueue(ctx, ack, limit, all)
	if isErr {
		return toolErrorResult(text), nil
	}
	return toolTextResult(text), nil
}

// doFetchQueue runs one fetch_queue broker round-trip and returns the rendered
// text. Shared by the `fetch_queue` TOOL (an LLM-driven call) and the `fetchq`
// PROMPT (a user-driven slash command) so both consume the queue identically.
// isErr distinguishes a broker/transport failure (surface as an error) from a
// successful fetch (which may legitimately be "queue is empty").
func (a *adapter) doFetchQueue(ctx context.Context, ack bool, limit int, all bool) (text string, isErr bool) {
	fq := ipc.FetchQueueReq{
		Op:    ipc.OpFetchQueue,
		ID:    strconv.FormatUint(a.nextID.Add(1), 10),
		Ack:   ack,
		Limit: limit,
		All:   all,
	}

	ch := make(chan ipc.FetchQueueResp, 1)
	a.fqmu.Lock()
	a.fqPending[fq.ID] = ch
	a.fqmu.Unlock()
	defer func() { a.fqmu.Lock(); delete(a.fqPending, fq.ID); a.fqmu.Unlock() }()

	conn := a.currentConn()
	if conn == nil {
		return "broker reconnecting — retry in a moment", true
	}
	if err := conn.WriteJSON(fq); err != nil {
		return "broker write: " + err.Error(), true
	}
	select {
	case <-ctx.Done():
		return "canceled", true
	case <-time.After(120 * time.Second):
		return "fetch_queue timeout", true
	case resp := <-ch:
		if resp.Err != "" {
			return resp.Err, true
		}
		return renderFetchedMessages(resp.Messages, resp.Remaining, a.currentTopicName()), false
	}
}

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
		return toolTextResult("re-transcribed: Fresh transcript: " + resp.Text), nil
	}
}

func (a *adapter) registerTools(srv *mcp.Server) {
	caps := a.capsOrDefault()
	tools := []struct {
		tool    *mcp.Tool
		handler mcp.ToolHandler
	}{
		{
			tool: &mcp.Tool{
				Name:        "attach",
				Description: "Attach this session to a Telegram topic. Empty = show a picker of suggested topics for the user to choose (never guess). `target='dm'` for DM. `name='X'` for a topic name. `topic_id=N` to claim a known thread. `create=true` to confirm creation. `steal=true` only after user-confirmed force_steal.",
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
				Name:        "detach",
				Description: "Release this session's current Telegram topic claim. After detach, inbound messages on that route fall through to the broker's fallback. No-op if not attached.",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			handler: a.toolDetach,
		},
	}
	for _, t := range tools {
		srv.AddTool(t.tool, t.handler)
	}
}

func parseFetchLimit(val any) (int, bool) {
	if s, ok := val.(string); ok && s == "all" {
		return 0, true
	}
	if f, ok := val.(float64); ok {
		return int(f), false
	}
	return 3, false
}

// parseFetchLimitStr parses the `limit` argument of the fetchq PROMPT, whose
// arguments arrive as strings (MCP prompt args are always strings). Empty or
// "all" ⇒ drain everything (the slash-command default); a positive integer ⇒ that
// many oldest; anything else falls back to draining all.
func parseFetchLimitStr(s string) (limit int, all bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "all") {
		return 0, true
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, false
	}
	return 0, true
}

func renderBacklogSummary(count int, items []ipc.QueuedItem, route string) string {
	if count <= 0 {
		return ""
	}
	var sb strings.Builder
	if route != "" {
		fmt.Fprintf(&sb, "📨 %d message(s) for topic %q were held while no session was attached. Call `fetch_queue` (limit:3 or \"all\") to retrieve them.", count, route)
	} else {
		fmt.Fprintf(&sb, "📨 %d message(s) were held while no session was attached. Call `fetch_queue` (limit:3 or \"all\") to retrieve them.", count)
	}
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

func pendingNudge(n int, route string) string {
	if route != "" {
		return fmt.Sprintf("(%d pending for topic %q — call `fetch_queue`)", n, route)
	}
	return fmt.Sprintf("(%d pending — call `fetch_queue`)", n)
}

func renderFetchedMessages(msgs []c3types.Inbound, remaining int, route string) string {
	if len(msgs) == 0 {
		return "c3 queue is empty"
	}
	blocks := make([]string, 0, len(msgs))
	for i := range msgs {
		blocks = append(blocks, renderQueuedInbound(&msgs[i]))
	}
	out := strings.Join(blocks, "\n\n")
	if remaining > 0 {
		out += "\n\n" + pendingNudge(remaining, route)
	}
	return out
}

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
	parts = append(parts, c3types.ReplyContextFields(in.ReplyTo)...)
	for _, att := range in.Attachments {
		parts = append(parts, c3types.AttachmentField(att))
	}
	if in.IsEvent() {
		parts = append(parts, fmt.Sprintf("event=%s", in.Kind))
	}
	if len(parts) == 0 {
		return "(no content)"
	}
	return strings.Join(parts, " ")
}

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

func toolErrorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: "Error: " + msg,
			},
		},
		IsError: true,
	}
}

func toolTextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: text,
			},
		},
	}
}

func mapResult(m map[string]any) *mcp.CallToolResult {
	content := []mcp.Content{}
	if text, ok := m["text"].(string); ok {
		content = append(content, &mcp.TextContent{Text: text})
	} else {
		data, _ := json.Marshal(m)
		content = append(content, &mcp.TextContent{Text: string(data)})
	}
	return &mcp.CallToolResult{Content: content}
}
