// Leader ACP client for live Telegram → Grok turn inject.
//
// Protocol (proven 2026-07-10, see docs/GROK-INJECT.md):
//
//	unix socket $GROK_HOME/leader.sock
//	frame = u32 BE length + JSON body
//	first message: {"type":"register","client_type":"stdio","mode":"stdio","capabilities":{}}
//	ACP wrap:      {"type":"acp","payload":"<stringified json-rpc>"}
//	inject:        session/prompt on the bound session id
//
// Leader mode is REQUIRED for C3 Grok support. Without a leader socket there
// is no external inject path into the TUI session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

const (
	leaderProtocolVersion = 1
	defaultLeaderDialTO   = 5 * time.Second
	defaultPromptTO       = 120 * time.Second
	maxFrameBytes         = 64 << 20 // 64 MiB — matches Grok's advertised max
)

// errLeaderUnavailable means no leader socket / connect failed. Callers must
// NOT ack the inbound as delivered (content stays in the durable queue).
var errLeaderUnavailable = errors.New("grok leader unavailable (C3 requires [cli] use_leader = true)")

// errNoSessionID means we could not resolve which Grok session to inject into.
var errNoSessionID = errors.New("could not resolve Grok session id (set C3_GROK_SESSION_ID or ensure active_sessions.json lists this process)")

type leaderClient struct {
	mu sync.Mutex

	conn        net.Conn
	buf         []byte
	nextID      atomic.Uint64
	sessionID   string
	cwd         string
	sockPath    string
	initialized bool
	// pendingDrainID is a session/prompt request whose user text already
	// landed (we acked C3's queue) but whose agent turn has not finished.
	// The next Inject drains it first so ACP frames stay ordered.
	pendingDrainID uint64
}

func leaderSocketPath() string {
	if p := os.Getenv("C3_GROK_LEADER_SOCK"); p != "" {
		return p
	}
	if p := os.Getenv("GROK_LEADER_SOCKET"); p != "" {
		return p
	}
	home := os.Getenv("GROK_HOME")
	if home == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(uh, ".grok")
	}
	return filepath.Join(home, "leader.sock")
}

func resolveGrokSessionID() string {
	if s := strings.TrimSpace(os.Getenv("C3_GROK_SESSION_ID")); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("GROK_SESSION_ID")); s != "" {
		return s
	}
	// active_sessions.json is written by the TUI; match our ancestor PIDs.
	if sid := sessionIDFromActiveSessions(os.Getpid()); sid != "" {
		return sid
	}
	return ""
}

// resolveGrokSessionIDForCWD prefers the active_sessions entry whose cwd matches
// the attach cwd (multi-session: MCP under leader cannot use TUI PID ancestry).
func resolveGrokSessionIDForCWD(cwd string) string {
	if s := strings.TrimSpace(os.Getenv("C3_GROK_SESSION_ID")); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("GROK_SESSION_ID")); s != "" {
		return s
	}
	cwd = filepath.Clean(cwd)
	sessions := readActiveSessions()
	var match []activeSession
	for _, s := range sessions {
		if s.SessionID != "" && filepath.Clean(s.CWD) == cwd {
			match = append(match, s)
		}
	}
	if len(match) == 1 {
		return match[0].SessionID
	}
	// Multiple sessions in same cwd: prefer the one whose PID is still alive.
	for _, s := range match {
		if processAlive(s.PID) {
			return s.SessionID
		}
	}
	if len(match) > 0 {
		return match[0].SessionID
	}
	return sessionIDFromActiveSessions(os.Getpid())
}

func readActiveSessions() []activeSession {
	home := os.Getenv("GROK_HOME")
	if home == "" {
		uh, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		home = filepath.Join(uh, ".grok")
	}
	data, err := os.ReadFile(filepath.Join(home, "active_sessions.json"))
	if err != nil {
		return nil
	}
	var sessions []activeSession
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil
	}
	return sessions
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Linux: /proc/<pid> is the reliable existence check.
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

type activeSession struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
}

func sessionIDFromActiveSessions(selfPID int) string {
	sessions := readActiveSessions()
	ancestors := ancestorPIDs(selfPID)
	for _, s := range sessions {
		if s.SessionID == "" || s.PID == 0 {
			continue
		}
		if ancestors[s.PID] {
			return s.SessionID
		}
	}
	// Single live session in this GROK_HOME — common when only one TUI is open.
	alive := 0
	var only string
	for _, s := range sessions {
		if s.SessionID == "" {
			continue
		}
		if s.PID == 0 || processAlive(s.PID) {
			alive++
			only = s.SessionID
		}
	}
	if alive == 1 {
		return only
	}
	return ""
}

func ancestorPIDs(start int) map[int]bool {
	out := map[int]bool{}
	pid := start
	for i := 0; i < 32 && pid > 1; i++ {
		out[pid] = true
		ppid, err := parentPID(pid)
		if err != nil || ppid <= 0 || ppid == pid {
			break
		}
		pid = ppid
	}
	return out
}

func parentPID(pid int) (int, error) {
	// Linux /proc/<pid>/stat: field 4 is ppid.
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// Format: pid (comm) state ppid ...
	s := string(b)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 || rparen+2 >= len(s) {
		return 0, fmt.Errorf("parse stat")
	}
	fields := strings.Fields(s[rparen+2:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("stat fields")
	}
	// fields[0]=state, fields[1]=ppid
	return strconv.Atoi(fields[1])
}

func (c *leaderClient) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && c.initialized && c.sessionID != "" {
		return nil
	}
	return c.connectLocked(ctx)
}

func (c *leaderClient) connectLocked(ctx context.Context) error {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
		c.buf = nil
		c.initialized = false
	}
	sock := c.sockPath
	if sock == "" {
		sock = leaderSocketPath()
		c.sockPath = sock
	}
	if sock == "" {
		return errLeaderUnavailable
	}
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("%w: %s: %v", errLeaderUnavailable, sock, err)
	}

	d := net.Dialer{Timeout: defaultLeaderDialTO}
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return fmt.Errorf("%w: dial %s: %v", errLeaderUnavailable, sock, err)
	}
	c.conn = conn
	c.buf = nil

	if err := c.writeJSON(map[string]any{
		"type":         "register",
		"client_type":  "stdio",
		"mode":         "stdio",
		"capabilities": map[string]any{},
	}); err != nil {
		_ = conn.Close()
		c.conn = nil
		return err
	}

	// Wait for leader_ready (registered may arrive first with ready=false).
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		msg, err := c.readJSON(deadline.Sub(time.Now()))
		if err != nil {
			_ = conn.Close()
			c.conn = nil
			return fmt.Errorf("leader register: %w", err)
		}
		switch msg["type"] {
		case "registered", "leader_ready":
			if msg["type"] == "leader_ready" || msg["ready"] == true {
				ready = true
			}
			if msg["type"] == "leader_ready" {
				ready = true
			}
		case "error":
			_ = conn.Close()
			c.conn = nil
			return fmt.Errorf("leader register error: %v", msg)
		}
		if ready {
			break
		}
	}
	if !ready {
		// Proceed after registered even if ready flag lagged — initialize will fail loudly if not ready.
	}

	// ACP initialize
	initResp, err := c.acpRequest(ctx, "initialize", map[string]any{
		"protocolVersion": leaderProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "c3-grok-adapter",
			"version": adapterVersion,
		},
	})
	if err != nil {
		_ = conn.Close()
		c.conn = nil
		return fmt.Errorf("leader initialize: %w", err)
	}
	_ = initResp
	_ = c.acpNotify("notifications/initialized", map[string]any{})

	sid := c.sessionID
	if sid == "" {
		sid = resolveGrokSessionID()
	}
	if sid == "" {
		_ = conn.Close()
		c.conn = nil
		return errNoSessionID
	}
	cwd := c.cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Prefer session/load (existing TUI session). Fall back to error — we must
	// not session/new a parallel conversation the user cannot see.
	if _, err := c.acpRequest(ctx, "session/load", map[string]any{
		"cwd":        cwd,
		"sessionId":  sid,
		"mcpServers": []any{},
	}); err != nil {
		_ = conn.Close()
		c.conn = nil
		return fmt.Errorf("session/load %s: %w", sid, err)
	}
	c.sessionID = sid
	c.initialized = true
	return nil
}

func (c *leaderClient) Inject(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty inject text")
	}
	if err := c.ensure(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Finish prior turn's ACP response before starting a new prompt.
	if err := c.drainPendingLocked(ctx); err != nil {
		c.dropConnLocked()
		return err
	}

	to := defaultPromptTO
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < to {
			to = rem
		}
	}
	promptCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	// Ack C3 as soon as the user text lands in the session — NOT when the
	// agent turn finishes. Waiting for end-of-turn left orphans in the durable
	// queue when the adapter was killed mid-turn after inject already showed
	// in the TUI (msg 4224, 2026-07-10).
	landed, err := c.acpPromptUntilLanded(promptCtx, map[string]any{
		"sessionId": c.sessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": text,
		}},
	})
	if err != nil {
		c.dropConnLocked()
		return err
	}
	if landed.pendingDrain {
		c.pendingDrainID = landed.id
	}
	return nil
}

func (c *leaderClient) dropConnLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.initialized = false
	c.pendingDrainID = 0
}

// drainPendingLocked waits for a previously-landed session/prompt result.
func (c *leaderClient) drainPendingLocked(ctx context.Context) error {
	id := c.pendingDrainID
	if id == 0 {
		return nil
	}
	deadline := time.Now().Add(defaultPromptTO)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			// Don't fail inject forever — drop and re-register next time.
			c.pendingDrainID = 0
			return nil
		}
		frame, err := c.readJSON(rem)
		if err != nil {
			c.pendingDrainID = 0
			return err
		}
		if frame["type"] != "acp" {
			continue
		}
		acp, ok := decodeACPPayload(frame["payload"])
		if !ok {
			continue
		}
		if got, ok := asUint64(acp["id"]); ok && got == id {
			c.pendingDrainID = 0
			if errObj, ok := acp["error"]; ok && errObj != nil {
				// Turn already landed for the user; log-level only at caller.
				_ = errObj
			}
			return nil
		}
	}
}

type promptLand struct {
	id           uint64
	pendingDrain bool // true if we returned before the final result
}

// acpPromptUntilLanded sends session/prompt and returns when the user message
// is observed (or the request completes/errors).
func (c *leaderClient) acpPromptUntilLanded(ctx context.Context, params map[string]any) (promptLand, error) {
	id := c.nextID.Add(1)
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "session/prompt",
		"params":  params,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return promptLand{}, err
	}
	if err := c.writeJSON(map[string]any{"type": "acp", "payload": string(payload)}); err != nil {
		return promptLand{}, err
	}

	deadline := time.Now().Add(defaultPromptTO)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	for {
		if err := ctx.Err(); err != nil {
			return promptLand{}, err
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return promptLand{}, context.DeadlineExceeded
		}
		frame, err := c.readJSON(rem)
		if err != nil {
			return promptLand{}, err
		}
		if frame["type"] != "acp" {
			continue
		}
		acp, ok := decodeACPPayload(frame["payload"])
		if !ok {
			continue
		}
		// Terminal result/error for our request.
		if got, ok := asUint64(acp["id"]); ok && got == id {
			if errObj, ok := acp["error"]; ok && errObj != nil {
				b, _ := json.Marshal(errObj)
				return promptLand{}, fmt.Errorf("session/prompt: %s", b)
			}
			return promptLand{id: id, pendingDrain: false}, nil
		}
		// Streamed updates: user text in the session ⇒ inject succeeded.
		if method, _ := acp["method"].(string); method == "session/update" {
			params, _ := acp["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			kind, _ := update["sessionUpdate"].(string)
			if kind == "" {
				kind, _ = update["session_update"].(string)
			}
			if kind == "user_message_chunk" {
				return promptLand{id: id, pendingDrain: true}, nil
			}
		}
	}
}

func decodeACPPayload(raw any) (map[string]any, bool) {
	switch p := raw.(type) {
	case string:
		var acp map[string]any
		if err := json.Unmarshal([]byte(p), &acp); err != nil {
			return nil, false
		}
		return acp, true
	case map[string]any:
		return p, true
	default:
		return nil, false
	}
}

func (c *leaderClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.initialized = false
}

func (c *leaderClient) writeJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > maxFrameBytes {
		return fmt.Errorf("frame too large: %d", len(raw))
	}
	hdr := []byte{
		byte(len(raw) >> 24),
		byte(len(raw) >> 16),
		byte(len(raw) >> 8),
		byte(len(raw)),
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err = c.conn.Write(raw)
	return err
}

func (c *leaderClient) readJSON(timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if len(c.buf) >= 4 {
			n := int(c.buf[0])<<24 | int(c.buf[1])<<16 | int(c.buf[2])<<8 | int(c.buf[3])
			if n < 0 || n > maxFrameBytes {
				return nil, fmt.Errorf("bad frame length %d", n)
			}
			if len(c.buf) >= 4+n {
				body := c.buf[4 : 4+n]
				c.buf = c.buf[4+n:]
				var msg map[string]any
				if err := json.Unmarshal(body, &msg); err != nil {
					return nil, err
				}
				return msg, nil
			}
		}
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		tmp := make([]byte, 64*1024)
		n, err := c.conn.Read(tmp)
		if err != nil {
			return nil, err
		}
		c.buf = append(c.buf, tmp[:n]...)
	}
}

func (c *leaderClient) acpNotify(method string, params map[string]any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.writeJSON(map[string]any{"type": "acp", "payload": string(payload)})
}

func (c *leaderClient) acpRequest(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	id := c.nextID.Add(1)
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if err := c.writeJSON(map[string]any{"type": "acp", "payload": string(payload)}); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(defaultPromptTO)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return nil, context.DeadlineExceeded
		}
		frame, err := c.readJSON(rem)
		if err != nil {
			return nil, err
		}
		if frame["type"] != "acp" {
			// control / ping / leader_ready noise — ignore
			continue
		}
		rawPayload := frame["payload"]
		var acp map[string]any
		switch p := rawPayload.(type) {
		case string:
			if err := json.Unmarshal([]byte(p), &acp); err != nil {
				continue
			}
		case map[string]any:
			acp = p
		default:
			continue
		}
		if got, ok := asUint64(acp["id"]); ok && got == id {
			if errObj, ok := acp["error"]; ok && errObj != nil {
				b, _ := json.Marshal(errObj)
				return nil, fmt.Errorf("%s: %s", method, b)
			}
			if result, ok := acp["result"].(map[string]any); ok {
				return result, nil
			}
			return map[string]any{}, nil
		}
		// Drop unrelated ACP notifications/updates (session/update etc.).
	}
}

func asUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case float64:
		return uint64(x), true
	case int:
		return uint64(x), true
	case int64:
		return uint64(x), true
	case uint64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return uint64(n), err == nil
	default:
		return 0, false
	}
}

// formatInboundTurnText renders one inbound as a Grok user turn.
// Body first so the TUI sticky/preview shows real text (not "message...").
// Meta last — short, for routing/download only.
func formatInboundTurnText(in *c3types.Inbound) string {
	user := strconv.FormatInt(in.Sender.UserID, 10)
	if in.Sender.Username != "" {
		user = "@" + in.Sender.Username + " (" + user + ")"
	}
	channel := fmt.Sprintf("%d", in.ChatID)
	if in.TopicID != nil {
		channel = fmt.Sprintf("%d/%d", in.ChatID, *in.TopicID)
	}

	body := strings.TrimSpace(in.Text)
	// Prefer the human text as the first line so scrollback previews show it.
	if body == "" && len(in.Attachments) > 0 {
		body = "(" + string(in.Attachments[0].Kind) + " attachment)"
	}

	var b strings.Builder
	if body != "" {
		b.WriteString(body)
	} else {
		b.WriteString("(empty Telegram message)")
	}
	fmt.Fprintf(&b, "\n\n— %s · %s", user, channel)
	for _, att := range in.Attachments {
		fmt.Fprintf(&b, "\nfile: kind=%s file_id=%s", att.Kind, att.FileID)
		if att.Name != "" {
			fmt.Fprintf(&b, " name=%q", att.Name)
		}
	}
	return b.String()
}
