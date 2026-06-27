# C3 — Interactive `ask` round-trip over Telegram (design)

**Date:** 2026-06-26 · **Branch:** `feat/telegram-ask-roundtrip` · **Status:** approved-for-autonomous-build (Karthi 2026-06-26 "build all you can")

## Problem

When Claude asks a question with options while in Telegram mode, the options render as Telegram inline-keyboard buttons (the agent calls the existing `reply` tool with `buttons`). **Tapping a button does not deliver the answer back to Claude.** Root cause (diagnosed 2026-06-26, classification (d)): the callback tap IS plumbed end-to-end to the agent (`internal/channel/telegram/poll.go:dispatchCallback` → `CallbackEvent` → `internal/broker/worker.go:flushEvent`/`forwardOrFallback` → adapter `cmd/c3-claude-adapter/main.go:buildEventFrame` → `notifications/claude/channel` frame), **but**:
1. `reply` is fire-and-forget — `toolForward("reply")` returns once the *send* is confirmed; the agent's turn is not awaiting an answer.
2. The tap arrives as informational prose ("`<actor> pressed a button (data="X")`") with **no correlation** to the question and **no tool-result** that resolves a pending ask.
3. Claude Code only reliably renders channel frames during an active turn; after a fire-and-forget reply the agent is idle, so even the delivered frame may not surface.

**Feasibility (probed 2026-06-26, live docs):** Claude Code's native `AskUserQuestion` has **no channel/MCP surface** — the channel contract is exactly {push, reply, permission-relay}, and the permission verdict is binary `allow`/`deny` (can't carry an option). So the fix is **C3's own application-level `ask` primitive**, not interception of the native tool. This is ROADMAP "Thread A".

## Goal

A new blocking, correlated **`ask` MCP tool**: the agent asks a question (with/without options, single/multi-select, with Other/Skip/free-text), C3 renders it on Telegram, the human answers (tap or text), and the chosen answer is returned **as the tool's result** so the agent proceeds deterministically. No more hangs; no more lost taps.

## Architecture (reuses the shipped P7 substrate)

Mirrors the existing request→wait→verdict patterns (permission-relay shape; `fqPending` blocking pattern at `cmd/c3-claude-adapter/main.go:221`).

```
agent calls ask(question, options, …)               [adapter, blocking]
  → adapter generates askID (8-char base32), registers a local pending channel keyed by askID
  → OpAskRegister{askID, question, options, multi, other, skip, freetext}  → broker
        (NO route in the message — broker derives it from stub.CurrentRoute();
         route==nil → immediate OpAskRegistered{ok:false, err:"ask before attach"})
        broker registers pendingAsk{askID → route, options, selstate} BEFORE the send (fast-tap race),
        then sends the Telegram message + inline keyboard via ch.SendReply(ReplyArgs{...Buttons})
          (callback_data = "ask:<askID>:<optIdx>"; ≤64 bytes, validated by buildInlineKeyboard)
        broker patches pendingAsk.messageID with the returned id, and replies
          OpAskRegistered{ok:true, message_id} (synchronous ack so a send failure
          — oversized keyboard / Telegram error — returns the tool fast, not after 600s)
  ← human taps a button (or sends text for free-text/Other)
        poll.go dispatchCallback → CallbackEvent{Data="ask:<askID>:<idx>"} → host.Emit → worker
        broker, in RouteWorker.flushEvent (NOT poll.go — telegram can't import broker):
          if InboundCallback && Data has "ask:" prefix → resolveAsk(w.key, cb); on success RETURN
          (suppress the generic event). RESOLVE (push OpAskResult to the holder conn):
           single-select → answer = options[idx]; edit message to show choice
           multi-select  → toggle idx, edit keyboard (✓), wait for "Done"; answer = list
           Other         → edit msg "type your answer"; next text inbound on route = answer
           Skip          → answer = {skipped:true}
        free-text (no options) → next text inbound on route resolves the pendingAsk
  → OpAskResult{askID, answer}  → holder adapter
  → adapter pending channel unblocks → ask tool returns answer as its result
```

Non-`ask` callbacks (agent-rendered `reply` buttons) keep the existing generic `CallbackEvent` → `<channel>` event path unchanged. Only `ask:`-prefixed callbacks are intercepted/resolved.

### Corrections folded from the code-grounding pass (2026-06-26)
- **Resolution is broker-side only** — in `RouteWorker.flushEvent` (`internal/broker/worker.go:470`), before `forwardOrFallback`. `poll.go` cannot reach the registry (the `telegram` package can't import `broker` — cycle).
- **`OpAskRegister` carries no route** — the adapter has no `RouteKey`; the broker derives it via `stub.CurrentRoute()` (mirror `handleToolCall`, `handler.go:164`). `route==nil` → return an error fast.
- **Adapter blocks fire-then-push:** the adapter-side wait mirrors `fqPending` (`main.go:218`, `toolFetchQueue` `main.go:1515`), but the broker side mirrors **`OpInbound` delivery** (`worker.go:610`) — `handleAskRegister` returns immediately (after the synchronous `OpAskRegistered` ack); the *answer* is pushed later as an unsolicited `OpAskResult`. Do NOT build `handleAskRegister` as a blocking inline-reply.
- **`OpAskResult` reaches the right adapter** via `Routes.Holder(route).ConnValue().(*ipc.Conn).WriteJSON(...)` — identical to `OpInbound`. Survives same-process broker reconnect (`TransferAllByConnID`).
- **Stale-tap protection** = the broker ignores taps for a resolved/expired askID **and still auto-acks the callback** (Telegram stops spinning). A *text* edit does NOT remove live buttons.
- **`EditMessage` cannot set/clear an inline keyboard today** (`EditArgs` has no Buttons; `EditMessage` sets no `ReplyMarkup`). Phase 1 adds a small enabling change: `EditArgs.Buttons [][]Button` + set `opts.ReplyMarkup` in `internal/channel/telegram/outbound.go:EditMessage` (pass empty `InlineKeyboardMarkup{}` to clear). This lets Phase 1 clear the keyboard on answer and unblocks all of Phase 2.
- **`ask` is registered unconditionally** but only functions where `Capabilities.InlineKeyboards==true` (telegram: yes). Future text-only channels degrade (return a tool error).
- **Runtime check:** confirm Claude Code tolerates a long-blocking (≤600s) MCP tool call. If its tool timeout is shorter, lower the default and document it. (Config/runtime risk, not a code defect.)

## Phase 1 — single-select round-trip (the bug fix, minimum-correct)

**Files (exact seams from the grounding cheat-sheet):**
- `internal/ipc/ops.go` — `OpAskRegister Op = "ask_register"` (adapter→broker block), `OpAskResult Op = "ask_result"` + `OpAskRegistered Op = "ask_registered"` (broker→adapter block).
- `internal/ipc/messages.go` — every struct's first field is `Op Op json:"op"`. `AskRegisterReq{Op; AskID, Question string; Options []string; Multi, AllowOther, AllowSkip, FreeText bool}` (**no route field**); `AskRegisteredMsg{Op; AskID string; OK bool; Err string; MessageID int64}`; `AskResultMsg{Op; AskID string; Answer AskAnswer; Err string}`; `AskAnswer{Selected []string; Text string; Skipped, TimedOut bool}`.
- `internal/broker/ask.go` (new) — `askRegistry` (map askID→`pendingAsk{route RouteKey; options []string; messageID int64; selstate …}`, mutex-guarded): `register` (called BEFORE send), `setMessageID`, `delete`, `resolveAsk(route, *CallbackEvent) bool`. Reject/regenerate on the rare existing-key collision.
- `internal/broker/handler.go` — `HandleConn` switch: `case ipc.OpAskRegister: b.handleAskRegister(conn, stub, raw)`. `handleAskRegister` mirrors `handleToolCall` (`handler.go:164`) for route resolution: `route := stub.CurrentRoute()` (nil → `OpAskRegistered{OK:false,Err}`); `register` pendingAsk; `ch.SendReply(ReplyArgs{Channel,ChatID,TopicID,Text:Question,Markup,Buttons:askKeyboard(askID,options)})`; on err → delete + `OpAskRegistered{OK:false,Err}`; on ok → `setMessageID` + `OpAskRegistered{OK:true,MessageID}`.
- `internal/broker/worker.go` — in `flushEvent` (`:470`), before `forwardOrFallback`: if `InboundCallback` && `Data` has `"ask:"` prefix → `if w.broker.resolveAsk(w.key, cb) { return }`. `resolveAsk` pushes `OpAskResult` to `Routes.Holder(route).ConnValue().(*ipc.Conn)` (same as `OpInbound`, `worker.go:610`) and (Phase 1) `ch.EditMessage` to mark `✓ <chosen>` and clear the keyboard.
- `internal/channel/channel.go` + `internal/channel/telegram/outbound.go` — **enabling change:** add `Buttons [][]c3types.Button` to `EditArgs` (`c3types/types.go`) and set `opts.ReplyMarkup` in `EditMessage` (empty `InlineKeyboardMarkup{}` clears). Reuse `buildInlineKeyboard`.
- `cmd/c3-claude-adapter/main.go` — pending map `askPending map[string]chan ipc.AskResultMsg` + `askmu` (mirror `fqPending` `:217`, init in `newAdapter` `:248`). `brokerReader` switch (`:332`): `case ipc.OpAskResult: a.dispatchAskResult(raw)`, `case ipc.OpAskRegistered: a.dispatchAskRegistered(raw)`. `toolAsk` (mirror `toolFetchQueue` `:1515` with a 600s timer): gen askID → register pending → `WriteJSON(AskRegisterReq)` → wait `OpAskRegistered` (bail fast on `!OK`) → `select{ctx.Done / time.After(600s) / res:=<-pending}` → return `res.Answer` (or `TimedOut`). Register the tool in `registerTools` (`:1115`) with `InputSchema: mcptools.AskToolSchema()`.
- `internal/mcptools/schema.go` — `func AskToolSchema() map[string]any` (mirror `PollToolSchema` `:101`): `question` required; `options` array; `multi`/`allow_other`/`allow_skip`/`free_text` bools.
- Guidance — add the "use `ask`, not AskUserQuestion or fire-and-forget reply buttons" line to `internal/mode` (`mode.Combined()`, preferred) or `internal/capability/guidance.go:18` (**update the golden test in `guidance_test.go`** if so).

**Behavior (Phase 1):** `options` required + non-empty; single-select; blocks until tap or timeout. On tap: answer = chosen option string; broker edits the message to show "✓ <chosen>" (prevents double-answers / stale taps). Timeout default **600s** (configurable later) → returns `{TimedOut:true}` so the agent can recover. askID = 8-char base32 (collision-safe within a route's lifetime).

**Tests (TDD, write failing first):**
- `internal/broker/ask_test.go` — `TestResolveAsk_SingleSelect`: register a pendingAsk, feed a `CallbackEvent{Data:"ask:<id>:1"}`, assert it resolves with `options[1]` and an `OpAskResult` is written to the holder; a non-matching `Data` does NOT resolve and falls through to the generic event.
- `cmd/c3-claude-adapter/ask_test.go` — `TestAskTool_ReturnsTappedAnswer`: invoke the `ask` handler, simulate the broker's `OpAskResult`, assert the tool result equals the tapped option; `TestAskTool_Timeout` returns `TimedOut`.
- `internal/channel/telegram/*_test.go` — callback_data round-trips through `buildInlineKeyboard` within the 64-byte cap for the max option count.

**Done when:** `go build ./... && go vet ./... && go test ./... && go test -race ./internal/broker/... ./cmd/...` all green; the round-trip is covered by a test that fails on master.

## Phase 2 — full taxonomy

Adds, on the same correlation mechanism:
- **`multi` (multi-select):** toggle buttons show `✓`; a trailing **"✅ Done"** button (`callback_data "ask:<id>:done"`) resolves with the selected list. Broker tracks per-ask selection state; each toggle edits the keyboard. Empty-selection + Done allowed (returns `[]`) unless `min=1` later.
- **`free_text` / no options:** no keyboard; the pendingAsk resolves from the **next text inbound on that route** (broker correlates the route's next non-event text to the oldest open free-text ask). Phase-2 mechanism: a per-route "awaiting-text ask" pointer. **Grounding note:** this interception sits in `flushInbounds` (the text/STT/durable-queue path, `worker.go:333`), NOT `flushEvent` — more invasive; the design must decide whether the consumed answer text is ALSO queued/delivered as a normal message, and how that interacts with offset/durable-queue accounting. Treat this as a Phase-2 design sub-task before building it.
- **`allow_other`:** appends an "✏️ Other…" button; tapping it switches the ask to awaiting-text mode (edit message "Type your answer"); next text = answer with `{Selected:["Other"], Text:…}`.
- **`allow_skip`:** appends a "⏭ Skip" button → `{Skipped:true}`.
- **optional comment:** `allow_comment` — after a selection, prompt "add a comment? (or /skip)"; next text appended as `{Text:…}`. (Keep simple; can defer if it complicates Phase 2.)

**Tests:** one per taxonomy branch (multi-select Done returns list; free-text next-message resolves; Other→text; Skip; toggle edits state).

**Edge cases to cover (both phases):** two open asks on one route (FIFO for text resolution); a tap for an already-resolved/expired askID (ignore, ack the callback so Telegram stops spinning); adapter disconnect while an ask is pending (broker expires it; on reconnect the agent's tool call has already errored/returned); option count > Telegram keyboard limits (validate up-front, return a tool error, don't send a malformed keyboard).

## Phase 3 — parity & polish (may defer to follow-up)
- Codex adapter `ask` parity (`cmd/c3-codex-adapter`).
- Align the generic event frame to include `user`/`user_id` (the reference plugin sets these; C3's `buildEventFrame` omits them).
- `/status`-style introspection of open asks.

## Non-goals
- Intercepting native `AskUserQuestion` (no channel surface — proven infeasible).
- Web-app / mini-app rendering (DM-only per Telegram; topics are the primary surface).
- Changing the existing `reply`-with-buttons path (kept for fire-and-forget tap-to-act).

## Security / keep-out
- No infra/PII values anywhere. callback_data carries only an opaque askID + index (no secrets).
- Sender-gating: a tap/text resolving an ask is honored only from the route's allowlisted actor (reuse existing gating; note in guidance that anyone who can post to the topic can answer).
