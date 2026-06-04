package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
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
		case ipc.OpPairModeStart:
			b.handlePairModeStart(conn, raw)
		case ipc.OpPingThisSession:
			b.handlePingThisSession(conn, raw)
		case ipc.OpListSessions:
			b.handleListSessions(conn, raw)
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

// handlePairModeStart arms a pairing window and returns the generated
// 4-digit code so the CLI can display it. Idempotent in the sense that
// re-arming the same surface generates a NEW code and extends the TTL —
// users running /c3:pair twice get fresh codes, no leftover stale ones.
func (b *Broker) handlePairModeStart(conn *ipc.Conn, raw []byte) {
	var req ipc.PairModeStartReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.PairModeReplyMsg{
			Op: ipc.OpPairModeReply, OK: false,
			Err: "malformed pair_mode_start: " + err.Error(),
		})
		return
	}
	resp := ipc.PairModeReplyMsg{Op: ipc.OpPairModeReply, Target: req.Target, ChatID: req.ChatID}
	switch req.Target {
	case "dm":
		code, err := b.Pairing.StartDM()
		if err != nil {
			resp.Err = err.Error()
			_ = conn.WriteJSON(resp)
			return
		}
		resp.OK = true
		resp.Code = code
		resp.TTLSec = int(PairTTL.Seconds())
		log.Printf("pairing: DM ARMED via IPC — code=%s ttl=%v", code, PairTTL)
	case "group":
		if req.ChatID == 0 {
			resp.Err = "group pair requires chat_id"
			_ = conn.WriteJSON(resp)
			return
		}
		code, err := b.Pairing.StartGroup(req.ChatID)
		if err != nil {
			resp.Err = err.Error()
			_ = conn.WriteJSON(resp)
			return
		}
		resp.OK = true
		resp.Code = code
		resp.TTLSec = int(PairTTL.Seconds())
		log.Printf("pairing: group ARMED via IPC — chat=%d code=%s ttl=%v", req.ChatID, code, PairTTL)
	default:
		resp.Err = "target must be \"dm\" or \"group\""
	}
	_ = conn.WriteJSON(resp)
}

// handlePingThisSession dispatches a one-shot "this is me" reply on
// behalf of the slash command `/c3:ping` (TODO #19(b)). The calling
// client is the transient `c3-broker ping` subcommand, NOT the user's
// adapter — so we match the user's actual session by CWD against the
// live stub registry. The matched stub's claimed route is the target.
//
// Synchronous on the channel send so the slash command can surface
// failures (channel down, send error). Ping is rare; latency is fine.
func (b *Broker) handlePingThisSession(conn *ipc.Conn, raw []byte) {
	var req ipc.PingThisSessionReq
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = conn.WriteJSON(ipc.PingThisSessionReplyMsg{
			Op: ipc.OpPingThisSessionReply, OK: false,
			Err: "malformed ping_this_session: " + err.Error(),
		})
		return
	}

	// Find the attached user session. Skip the transient client itself by
	// requiring CurrentRoute != nil; the c3-broker-cli stub never attaches.
	//
	// Three-tier match (FIX 2, 2026-06-04), shared with /c3:sessions via
	// stubMatchesPID:
	//
	//  Tier 1 (primary, PID): if the caller supplied a PID hint (its
	//  best-effort walk up the PPID chain — see proctree.BestEffortCallerPID),
	//  match the stub whose PID equals it OR whose CLI-session ancestor pid
	//  equals it. The CLI-ancestor arm is essential: a Claude stub registers
	//  under its ADAPTER's pid (comm "c3-claude-adapt"), while the caller
	//  resolves the real claude pid — the adapter's PARENT. Without the
	//  ancestor arm, req.PID(claude) never equals stub.PID(adapter) and the
	//  ping reports "not attached" even when attached. PID is also the stable
	//  identity that survives the CWD collapse (claude launched from a parent
	//  dir, slash command run from a project subdir).
	//
	//  Tier 3 (tertiary fallback, CWD): when a PID hint WAS supplied but NO
	//  stub matched by pid-or-CLI-ancestor (the walk failed, or the stub
	//  registered under a pid we can't bridge), fall back to CWD-equality
	//  matching before giving up — a robustness add so a failed walk still
	//  has a chance. Also used when no PID hint was supplied at all (PID==0).
	//
	// Determinism: in every tier, when >1 stub matches (rare — a reconnect
	// re-registered the same logical session under a new ConnID before the
	// old stub was reaped, or two adapters share a project dir), pick the
	// one with the highest ConnID. The registry mints monotonic ConnIDs via
	// atomic.Uint64, so "highest" == "most recently registered" == "the
	// session the user most likely meant". Closes report MINOR m1
	// (2026-05-19) for the CWD tier; the same tiebreak holds in the PID tier.
	var target *Stub
	candidateCount := 0
	matchRule := "none"
	if req.PID != 0 {
		for _, s := range b.Stubs.Snapshot() {
			if s.CurrentRoute() == nil {
				continue
			}
			rule, ok := b.stubMatchesPID(s, req.PID)
			if !ok {
				continue
			}
			candidateCount++
			if target == nil || s.ConnID > target.ConnID {
				target = s
				matchRule = rule
			}
		}
	}
	// Tertiary CWD fallback: no PID hint, or PID hint matched nothing.
	if target == nil {
		for _, s := range b.Stubs.Snapshot() {
			if s.CurrentRoute() == nil {
				continue
			}
			if s.CWD != req.CWD {
				continue
			}
			candidateCount++
			if target == nil || s.ConnID > target.ConnID {
				target = s
				matchRule = "cwd"
			}
		}
	}
	if candidateCount > 1 && target != nil {
		log.Printf("ping: multiple stubs matched (rule=%s); targeting most recent (conn=%d pid=%d cwd=%q)",
			matchRule, target.ConnID, target.PID, target.CWD)
	}
	if target != nil {
		log.Printf("ping: matched by %s (req.pid=%d → conn=%d pid=%d cwd=%q)",
			matchRule, req.PID, target.ConnID, target.PID, target.CWD)
	}
	if target == nil {
		_ = conn.WriteJSON(ipc.PingThisSessionReplyMsg{
			Op: ipc.OpPingThisSessionReply, OK: false,
			Err: "not attached; use /c3:attach first",
		})
		return
	}

	key := target.CurrentRoute()
	ch, err := b.Channel(key.Channel)
	if err != nil {
		_ = conn.WriteJSON(ipc.PingThisSessionReplyMsg{
			Op: ipc.OpPingThisSessionReply, OK: false,
			Err: fmt.Sprintf("channel lookup: %v", err),
		})
		return
	}

	label := pingTopicLabel(b, *key)
	text := pingText(target, label)
	var topicID *int64
	if key.HasTopic {
		t := key.TopicID
		topicID = &t
	}
	if _, err := ch.SendReply(c3types.ReplyArgs{
		Channel: key.Channel,
		ChatID:  key.ChatID,
		TopicID: topicID,
		Text:    text,
	}); err != nil {
		log.Printf("ping: send failed for %s: %v", routeKeyStr(*key), err)
		_ = conn.WriteJSON(ipc.PingThisSessionReplyMsg{
			Op: ipc.OpPingThisSessionReply, OK: false,
			Err: fmt.Sprintf("send: %v", err),
		})
		return
	}
	log.Printf("ping: sent for %s cli=%s cwd=%q", routeKeyStr(*key), target.CLI, target.CWD)
	_ = conn.WriteJSON(ipc.PingThisSessionReplyMsg{
		Op: ipc.OpPingThisSessionReply, OK: true,
		Channel: key.Channel, Topic: label, SentText: text,
	})
}

// stubMatchesPID reports whether the stub corresponds to the caller's
// resolved CLI session pid (reqPID), and names the rule that matched. A stub
// matches when EITHER:
//
//   - reqPID == stub.PID — the direct case (stub registered under the CLI
//     pid itself, or a non-adapter stub), OR
//   - reqPID == sessionPIDResolver(stub.PID) — the CLI-ancestor case: the
//     Claude adapter registers under its OWN pid (comm "c3-claude-adapt"),
//     so we walk up from stub.PID (strict predicate skips the adapter) to the
//     real claude/codex ancestor and compare that. This is THE bridge that
//     makes /c3:ping and /c3:sessions work for Claude stubs.
//
// reqPID==0 never matches (caller had no usable hint). The shared resolver is
// proctree.CLISessionPID by default; injectable for tests.
func (b *Broker) stubMatchesPID(s *Stub, reqPID int) (rule string, ok bool) {
	if reqPID == 0 {
		return "", false
	}
	if s.PID == reqPID {
		return "pid", true
	}
	if resolve := b.sessionPIDResolver; resolve != nil {
		if resolve(s.PID) == reqPID {
			return "cli-ancestor", true
		}
	}
	return "", false
}

// pingTopicLabel returns the human label for a route key — "dm" for
// non-topic routes, the topic's mapped name when known, else a
// "topic-<id>" fallback so we never surface a bare integer.
func pingTopicLabel(b *Broker, key RouteKey) string {
	if !key.HasTopic {
		return "dm"
	}
	if tp, ok := b.Mappings().LookupTopicByID(key.Channel, key.ChatID, key.TopicID); ok && tp.Name != "" {
		return tp.Name
	}
	return fmt.Sprintf("topic-%d", key.TopicID)
}

// pingText renders the one-shot identification message. Same
// home-shorten + cli-fallback conventions as welcomeText so the two
// messages look like siblings in the Telegram view.
func pingText(stub *Stub, label string) string {
	cwd := stub.CWD
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(cwd, home) {
		cwd = "~" + cwd[len(home):]
	}
	cli := stub.CLI
	if cli == "" {
		cli = "cli"
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if cwd == "" {
		return fmt.Sprintf("📍 c3-ping — %s attached to **%s** · pid %d · %s", cli, label, stub.PID, ts)
	}
	return fmt.Sprintf("📍 c3-ping\n📁 `%s`\n🤖 `%s` → **%s**\nPID %d · %s", cwd, cli, label, stub.PID, ts)
}

// handleListSessions returns a snapshot of every live adapter stub
// for the `/c3:sessions` slash command (TODO #19e, 2026-05-19). The
// transient client itself — CLI=="c3-broker-cli", set by dialBroker —
// is filtered out so the caller doesn't see itself listed. Entries
// are ordered descending by ConnID (most-recently-registered first)
// so the listing is deterministic regardless of map-iteration order.
//
// The handler also tags the entry whose PID matches the caller's PID
// hint (set by the CLI to its best-effort walk up the PPID chain from
// the slash command's shell-out — see cmd/c3-broker/sessions.go) with
// IsThisSession=true so the rendered table can mark "you are here".
func (b *Broker) handleListSessions(conn *ipc.Conn, raw []byte) {
	var req ipc.ListSessionsReq
	// Tolerate empty body: PID and CWD are both optional hints.
	_ = json.Unmarshal(raw, &req)

	snap := b.Stubs.Snapshot()
	entries := make([]ipc.SessionEntry, 0, len(snap))
	for _, s := range snap {
		// The transient sessions/topics/status/pair CLI connections
		// all hello as "c3-broker-cli" (see cmd/c3-broker/client.go::
		// dialBroker). We never want to list those — including the
		// /c3:sessions invocation itself, which is exactly such a
		// transient.
		if s.CLI == "c3-broker-cli" {
			continue
		}
		cli := s.CLI
		if cli == "" {
			// Defensive: an adapter that sent a blank CLI string would
			// otherwise render as a blank table column. Normalize to
			// "?" so the column always has SOMETHING.
			cli = "?"
		}
		e := ipc.SessionEntry{
			CLI:    cli,
			PID:    s.PID,
			CWD:    s.CWD,
			ConnID: s.ConnID,
		}
		if rk := s.CurrentRoute(); rk != nil {
			e.AttachedTo = sessionTopicLabel(b, *rk)
		}
		// "you are here" marker. Same PID-match as /c3:ping (FIX 2,
		// 2026-06-04): direct stub.PID equality OR the stub's CLI-session
		// ancestor pid (so a Claude stub registered under its adapter pid is
		// marked when the caller resolved the real claude pid).
		if _, ok := b.stubMatchesPID(s, req.PID); ok {
			e.IsThisSession = true
		}
		entries = append(entries, e)
	}
	// Most-recent first.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ConnID > entries[j].ConnID
	})
	_ = conn.WriteJSON(ipc.ListSessionsReplyMsg{
		Op:       ipc.OpListSessionsReply,
		Sessions: entries,
	})
}

// sessionTopicLabel formats the AttachedTo cell for /c3:sessions.
// Same conventions as pingTopicLabel for the "dm" / "topic-<id>"
// fallbacks; adds a "(group)" suffix when the topic has a non-empty
// Group set in mappings — so the user-visible cell looks like
// `c3 (main)` instead of just `c3`. Keeps pingTopicLabel unchanged
// (its consumer doesn't want the group qualifier).
func sessionTopicLabel(b *Broker, key RouteKey) string {
	if !key.HasTopic {
		return "dm"
	}
	tp, ok := b.Mappings().LookupTopicByID(key.Channel, key.ChatID, key.TopicID)
	if !ok || tp.Name == "" {
		return fmt.Sprintf("topic-%d", key.TopicID)
	}
	if tp.Group != "" {
		return fmt.Sprintf("%s (%s)", tp.Name, tp.Group)
	}
	return tp.Name
}

func ptrI64Val(v int64) *int64 { return &v }
