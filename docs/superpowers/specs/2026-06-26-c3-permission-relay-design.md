# C3 — Permission relay over Telegram (design)

**Date:** 2026-06-26 · **Branch:** `feat/telegram-permission-relay` (off `feat/telegram-ask-roundtrip`) · **Status:** approved-for-autonomous-build (Karthi 2026-06-26)

## Problem

When a bridged Claude Code session needs tool-use approval (default / acceptEdits mode), the permission prompt appears only in the local TUI. Karthi (on Telegram) can't approve it — so a remote session stalls. The official Claude Telegram plugin solves this: it declares the `claude/channel/permission` capability, receives the harness's `permission_request`, shows an Allow/Deny inline keyboard, and returns the verdict. **This is the "trust" Karthi keeps asking for** (Thread B of the 2026-06-26 roadmap). C3 has the channel but has never implemented permission relay.

**Boundary (do not conflate):** permission relay handles NORMAL permission prompts only. It does NOT catch **auto-mode classifier hard-denies** (where no prompt ever opens — e.g. a `sudo` authorized purely over Telegram). That case needs the separate **trusted-operator PreToolUse hook** (`docs/specs/2026-06-14-trusted-operator-dm-authorization.md`; Phase-0 gate now resolved YES by the 2026-06-26 probe). The two are complementary, built separately.

## Feasibility (probed + code-grounded 2026-06-26)

BUILDABLE. ~80% is a direct mirror of the just-shipped `ask` round-trip (this branch builds on it). **One genuinely novel piece** — the inbound interception:

- The Go MCP SDK (`go-sdk@v1.6.0`) will NOT deliver `notifications/claude/channel/permission_request` to a receiving-middleware or handler: `checkRequest` rejects any method not in `serverMethodInfos` (`mcp/shared.go:157-189`) BEFORE middleware runs, and the SDK exposes no API to register a custom channel notification (unlike the TS SDK's `setNotificationHandler` the reference plugin uses). A notification (no id) is then silently dropped — harmless, but unhandled.
- **Fix (the seam):** `notifyTransport.Connect` (`cmd/c3-claude-adapter/notify_transport.go:49`) already wraps the *send* side (it adds the `notifications/claude/channel` send method the SDK lacks). Extend it to also wrap the returned `mcp.Connection`'s **Read**: peek each `jsonrpc.Message`; if it's a request with empty ID and `Method == "notifications/claude/channel/permission_request"`, divert it (→ broker) and loop to read the next frame so the SDK never sees it; otherwise return it untouched. Copy the wrapper shape from the SDK's `loggingConn` (`go-sdk@v1.6.0/mcp/transport.go:272-300`); the `Connection` interface is `transport.go:52-73` (Read/Write/Close/SessionID). This is the receive-side twin of the existing send-side escape hatch.

This is the only piece with no in-repo precedent; everything else mirrors `ask`. **It also cannot be fully verified without a live CC session firing a real permission prompt** — so this feature ships unit-tested with a mandatory live-verify gate (see Testing).

## Architecture

```
CC harness needs approval
  → notifications/claude/channel/permission_request {request_id, tool_name, description, input_preview}
       intercepted in notifyTransport's wrapped Connection.Read (adapter)
  → OpPermissionRequest{request_id, tool_name, preview} → broker
       broker registers pendingPerm{request_id → route, messageID}, sends Telegram message
         "🔐 Permission: <tool_name>" + [✅ Allow][❌ Deny]  (callback_data perm:allow:<id> / perm:deny:<id>)
  ← human taps (gated to the operator)
       poll.go dispatchCallback → CallbackEvent{Data="perm:allow:<id>"} → host.Emit → worker
       broker, in RouteWorker.flushEvent, next to the ask divert:
         if Data has "perm:" prefix → resolvePerm(route, cb); on match RETURN (suppress generic event)
       resolvePerm: take pendingPerm, edit message to record outcome + clear keyboard,
                    push OpPermissionVerdict{request_id, behavior:"allow"|"deny"} → holder conn
  → adapter emits notifications/claude/channel/permission {request_id, behavior}  (fire-and-forget into CC)
```

Reuses from the `ask` feature (this branch is on top of it): the callback transport (taps already arrive as auto-acked `InboundCallback` events routed to `flushEvent`), `editAskMessage` (edit-message-on-answer + clear keyboard — generic enough to reuse or lift to a shared helper), the holder-delivery mechanics of `deliverAskResult` (push to `Routes.Holder(route).ConnValue().(*ipc.Conn)`), and the registry/parse/resolve-once patterns from `internal/broker/ask.go`.

**Difference from `ask`:** permission relay is **fire-and-forget into CC**, not a blocking tool. There is no blocking MCP tool call to unblock — the verdict just goes out via `notifyTx.Notify`. So no answer-timeout / registration-ack-to-unblock-a-caller is needed (though the broker still expires stale pending perms + clears their keyboards, like the ask reaper). If no verdict ever comes, CC keeps waiting in the TUI as it would anyway.

## Phase 0 — receive interceptor (the novel risk; build + unit-test first)

- Extend `notifyTransport` (`cmd/c3-claude-adapter/notify_transport.go`) to wrap `Connection.Read` (mirror `loggingConn`). Divert ONLY the exact method `notifications/claude/channel/permission_request`; **every other frame passes through byte-for-byte** (critical — this is on the inbound path the whole session depends on).
- Logging-only first cut: on a diverted permission_request, log `{request_id, tool_name}` and immediately emit `behavior:"deny"` via `notifyTx.Notify("notifications/claude/channel/permission", {request_id, behavior:"deny"})`.
- **Tests:** feed a permission_request frame through the wrapped Read → assert it's diverted (not returned to the SDK) + a deny is emitted; feed a normal `tools/call` / `notifications/claude/channel` / arbitrary frame → assert it passes through UNCHANGED (regression guard for the whole inbound path).

## Phase 1 — full Allow/Deny round-trip

- `internal/ipc/ops.go`: `OpPermissionRequest` (adapter→broker), `OpPermissionVerdict` (broker→adapter). Structs in `messages.go` mirroring `AskRegisterReq`/`AskResultMsg`: `PermissionReq{Op, RequestID, ToolName, Preview string}`, `PermissionVerdictMsg{Op, RequestID, Behavior string}` (behavior is a STRING `"allow"|"deny"`).
- `internal/broker/perm.go` (new, mirrors `ask.go`): `pendingPerm` registry (request_id → route, messageID, createdAt), `register`/`take`, `permKeyboard(id)` → `[✅ Allow][❌ Deny]` (+ optional `[ℹ️ See more]` `perm:more:<id>`), `parsePermCallback`, `(*Broker).resolvePerm(route, cb) bool`, and a reaper (or fold into the ask reaper) that expires stale perms + clears their keyboards.
- `internal/broker/handler.go`: `case ipc.OpPermissionRequest: b.handlePermissionRequest(...)` — mirror `handleAskRegister`: route via `stub.CurrentRoute()` (nil → drop + log; there's no tool call to error), capability gate (`InlineKeyboards`), register-before-send, `ch.SendReply` with `permKeyboard`, store messageID.
- `internal/broker/worker.go` `flushEvent`: add `strings.HasPrefix(cb.Data, "perm:")` → `if w.broker.resolvePerm(w.key, cb) { return }` next to the existing `ask:` divert.
- `resolvePerm`: take the pending, **sender-gate** (honor only if `cb.Actor.UserID` is the operator — see Security), edit message to "🔐 <tool>: ✅ Allowed / ❌ Denied" + clear keyboard, push `OpPermissionVerdict` to the holder conn.
- Adapter (`cmd/c3-claude-adapter/main.go`): `brokerReader` `case ipc.OpPermissionVerdict: a.dispatchPermissionVerdict(raw)` → emit `notifyTx.Notify("notifications/claude/channel/permission", {request_id, behavior})`. No pending-tool map (fire-and-forget). Wire the Phase-0 interceptor to send `OpPermissionRequest` instead of auto-denying.
- Capability: add `"claude/channel/permission": map[string]any{}` to the Experimental map in `buildMCPServer` (`cmd/c3-claude-adapter/main.go:1062-1064`). (RESUME.md's "~L646" is stale.)
- Instructions string: carry the reference's security contract — "declaring this capability asserts you authenticate the replier" — and the note that an approval over Telegram authorizes a tool use.

## Phase 2 — niceties (optional, may defer)
- `perm:more:<id>` "See more" expansion (full `{tool_name, description, input_preview}` pretty-printed; input_preview is truncated ~200 chars by the harness) — mirror `server.ts:744-768`.
- `y/n <request_id>` text fallback — mirror `server.ts:84` regex `/^\s*(y|yes|n|no)\s+([a-km-z]{5})\s*$/i`.
- Codex adapter parity.

## Reference behaviors to copy (`~/.claude/plugins/cache/claude-plugins-official/telegram/0.0.6/server.ts`)
- Keyboard shape `🔐 Permission: <tool>` + `[See more][✅ Allow][❌ Deny]`, callback_data `perm:more|allow|deny:<id>` (`server.ts:419-423`).
- Re-check the allowlist on the tap before honoring (`server.ts:736-741`).
- Edit the message to the outcome on answer (`server.ts:777-782`) — prevents double-answer; substitutes for any "cancel" (there is NO cancel notification; CC silently drops a late/unknown verdict).
- `behavior` is a string (`server.ts:770-773`).
- Single-user / DM-tighter stance for tool approvals (`server.ts:402-404`).

## Security
- **Sender-gating:** taps already pass `host.GateInbound` (allowlist) before reaching the broker (`internal/channel/telegram/poll.go:654`). For permission specifically (higher trust than a chat reply), `resolvePerm` MUST additionally restrict to the operator `user_id` (`cb.Actor.UserID`), not the whole route allowlist. A non-operator tap is ignored (still auto-acked).
- No secrets/PII in callback_data (opaque `perm:<verb>:<5-letter-id>`) or logs (log request_id + tool_name + verdict only, never the preview body).
- behavior defaults to nothing on a dropped/unknown id (no implicit allow).

## Testing & the live-verify gate
- Unit: the Phase-0 pass-through regression (non-permission frames unchanged) is the most important test. Plus: interception+divert, broker register→resolve (mirror the ask tests), verdict emit shape, sender-gate rejects a non-operator tap, expiry clears the keyboard.
- **LIVE-VERIFY (mandatory before trusting — Karthi):** a real CC session in default mode, bridged to Telegram, hits a tool that needs approval → the Allow/Deny keyboard appears in the topic → tapping Allow lets the tool run; Deny blocks it. This cannot be exercised autonomously (needs the real harness firing a permission_request + accepting our verdict). Until then the feature is "built + unit-tested, live-unverified."

## Non-goals
- Auto-mode classifier hard-denies (→ trusted-operator hook, separate).
- Changing permission MODE remotely (no supported surface).
