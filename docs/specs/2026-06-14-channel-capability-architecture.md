# Spec — C3 Channel Capability Manifest + Gate (CMG)

**Date:** 2026-06-14 · **Status:** DRAFT (critique pass 1 folded; passes 2–3 pending) · **Author:** Ram (orchestrated; synthesized from a 10-agent research+design+judge workflow, then critique-hardened)

**Revision history**
- v1 — base synthesis.
- v2 — **critique pass 1 folded** (findings F1–F12 + the Claude-Code reasoning-source investigation). Material changes: streaming (R4) deferred (no source frame exists for Claude Code); `Capabilities()` takes no arg in v1 (avoids a `channel→broker` import cycle); typing relay re-specified at the real goroutine boundary with a CLI-mode-noise guard; chunking made construct-boundary-aware; P1 shim mapping pinned; album mixing kept wholly in-channel; honest Claude re-attach story.

Supersedes the scattered channel/formatting assumptions in the v5 rearch spec for the parts it
touches. This is the architecture for C3's **rich-content + channel-capability** work (new P0 in
[ROADMAP.md](../../ROADMAP.md)), sequenced ahead of terminal-control.

## Goal & requirements

C3 must support the full Telegram feature surface, through a channel-capability system that
doesn't break when other channels are added, and that tells the agent exactly what it can do.

- **R1 — Rich-text formatting** for Telegram (top priority).
- **R2 — Full media / file / poll support** (compressed photo vs original file, video/audio/voice/animation, albums, polls) honoring all Bot API limits.
- **R3 — Deterministic typing indicator** — relayed programmatically by C3 while the agent works mid-turn; NOT an LLM-invoked tool. **(Built in v1.)**
- **R4 — Deterministic streaming of reasoning/thinking** — relayed programmatically. **DEFERRED in v1** — see "Streaming reality" below; no reasoning source frame is observable today.
- **R5 — Per-channel capability declaration** — each channel declares exactly what it supports.
- **R6 — Agent-facing capability + prompt surface** — for Claude **and** Codex.
- **R7 — No Telegram leak into core.**
- **R8 — Codex/Claude parity.**
- **R9 — Forward-looking + simplest-possible**, graceful degradation.

## Streaming reality (R4) — why it's deferred

The 2026-06-14 capability investigation (Claude Code docs + Agent SDK) is decisive: **Claude
Code exposes no in-flight reasoning/assistant text to any external process** — hooks fire at
discrete points with tool I/O only (no assistant/thinking text); an MCP server sees only its own
tool calls; only the **raw Messages API** (`thinking_delta`) or the **Agent SDK** (completed
messages, not deltas) expose reasoning. Since C3's Claude adapter is an MCP server, it has **no
source frame** for streaming. For **Codex**, the source frames DO exist on the app-server, but
the C3 forwarder deliberately opts out of them (`forwarder.go:51-55`:
`item/agentMessage/delta`, `item/reasoning/textDelta`, `item/reasoning/summaryTextDelta`).

**Consequence:** R4 cannot be delivered in v1 without one of two changes that need Karthi's call:
(a) **Codex-only:** reverse the forwarder opt-out and consume the reasoning deltas (asymmetric —
Codex would stream, Claude wouldn't; the opt-out was deliberate re: noise/cost); or (b) **Claude:**
a fundamental pivot where C3 hosts the agent via the Agent SDK / a Messages-API proxy (out of
scope here). v1 therefore ships **typing only** (R3); the manifest reports `Stream.StreamViaEdit=false`
for both CLIs and the agent guidance says streaming isn't available — no silent no-op, no dead
relay code. See "Items for Karthi."

## Architecture: Capability Manifest + Gate (CMG)

One flat, JSON-serializable `Capabilities` struct returned by `Channel.Capabilities()`, carried to
**both** adapters over the existing `hello_ack` **and** the existing `attached` payload, rendered
once by a shared `capability.GuidanceFor(caps)` into the agent surface. Core carries only the
manifest plus a typed, channel-neutral `Outbound` (a `Markup` intent enum + `Media []MediaItem` +
optional `Poll`); the existing `ParseMode` leaks are deleted. A single broker-side
`capability.Gate(caps, outbound)` is the **one choke point** that validates + degrades before any
channel method runs. All Telegram-specific rendering stays inside `internal/channel/telegram`.

### Layers

- **L0 `internal/channel/telegram`.** Owns ALL Telegram specifics: the static `Capabilities()`
  literal; `mdToTelegramHTML` **expanded** from today's bold+code-only (`format.go:32-106`) to
  italic/underline/strike/spoiler/links/lists/quotes (the R1 fix); a media branch replacing the
  `Files` hard-error (`outbound.go:29-31`) with `sendPhoto`(compressed) / `sendDocument`(original)
  / `sendVideo` / `sendAudio` / `sendVoice` / `sendAnimation` by Kind, and `sendMediaGroup` for
  albums **incl. all album mixing / legal-group rules and the sequential fallback** (Telegram-specific,
  stays here); a new `sendPoll` path; the 4096/1024 limits; 50MB-send / 20MB-download caps; single-emoji
  reaction validated against Telegram's allowed set. Nothing above this layer names `parse_mode`,
  `gotgbot`, `4096`, or `sendPhoto`.
- **L1 `internal/channel/channel.go` + `internal/c3types` (the firewall).** Adds
  `Capabilities() Capabilities` to the `Channel` interface — **no arg in v1** (per-route variance is
  YAGNI today and adding `RouteKey` now would force a `channel→broker` import cycle since `RouteKey`
  lives in `internal/broker`; when per-route caps are real, a neutral key type moves into `c3types`
  then — a bounded one-implementer change). Replaces `Outbound.Files []string` with `Media []MediaItem`
  and `Outbound.ParseMode` (live leak, `c3types/types.go:69`) with `Markup`. **Also removes
  `EditArgs.ParseMode`** (read at `dispatch.go:70`, never even advertised on the `edit_message` tool
  schema — a latent dead leak). Adds `Poll *PollSpec`. `Capabilities` is feature-level only (booleans,
  small enums, int limits) plus an `Inbound` sub-section. Telegram-shaped methods that remain
  (`CreateTopic`/`ValidateTopic`, single-emoji `React`, file_id `DownloadAttachment`) stay but are
  **gated** by manifest booleans — leak made inert, not rewritten.
- **L2 `internal/capability` (NEW; pure; imports only `c3types` + stdlib).** Owns
  `Gate(caps, Outbound) ([]Outbound, notes, error)` — the single choke point: validates (hard-reject
  poll when `!Polls`; reaction emoji against allowed set), down-converts (`Markup→none` when
  `!RichText`; `photo→document` when `!CompressedPhoto`; **`!Albums`→sequential sends** — album
  *mixing/legal-group* logic is NOT here, it's in the channel; construct-boundary-aware split when
  text > `MaxMessageRunes`; trim/split caption > `MaxCaptionRunes`; demote unsupported Kind), emits
  human-readable notes AND **durably logs altered/dropped outbound content via the broker logger**
  (a NEW outbound log path, distinct from the inbound don't-lose-content rule in `worker.go`). Also
  owns `GuidanceFor(caps) string` so the SAME data drives degradation AND the agent surface — they
  can't drift. Pure + table-tested.
- **L3 `internal/broker` (dispatch + RouteWorker typing relay).** `dispatchReply` builds the neutral
  `Outbound`, runs `capability.Gate`, then calls the channel (chunking/markup coercion moves OUT of
  literals into the gate/channel). The per-route serial `RouteWorker` drives the deterministic typing
  relay (below).
- **L4 `internal/ipc`.** Adds `Capabilities` (optional, additive) to the EXISTING `HelloAckMsg`
  (`messages.go:51-59`) AND the EXISTING `AttachedMsg` (`messages.go:252`, already carries `Channel`).
  **No new turn-path ops in v1** (the streaming ops are not added until a source exists — avoids
  producer-less dead wire surface like the existing unused `OpServerInfo`).
- **L5 adapters + `internal/mode` (shared agent surface).** Both adapters call ONE shared helper to
  build tool Descriptions/InputSchemas from the manifest (kills the inline-literal duplication in
  `registerTools`) and call `mode.Combined(caps)` = `ModeProtocol` + `MultipartProtocol` +
  `capability.GuidanceFor(caps)`. **Delivery (honest about the MCP constraint):** Claude's MCP
  `instructions` are delivered only at `initialize` (`buildInstructions` → `buildMCPServer`,
  `main.go:638,681`), so they cannot be refreshed on a mid-session re-attach. In v1 there is exactly
  ONE channel (Telegram), so the channel never changes mid-session and init-time delivery is correct
  and sufficient. Codex gets the AGENTS.md block at setup (`ensureCodexAgentsMd`, static) PLUS the
  active caps in the per-session turn-text head line. The `attached` payload also carries caps so the
  data is present the day we wire turn-time refresh. **Cross-channel re-attach caps refresh is
  deferred to when a 2nd channel exists** — at that point the active-channel guidance must be delivered
  at TURN time for BOTH CLIs (Claude via the inbound `<channel>` block head line, Codex via its turn
  head line); this is the documented multi-channel seam, not a v1 deliverable. Tool PRESENCE stays
  STABLE across channels (`ListChanged:false`, `main.go:640`/codex `:482`); only descriptions/enums
  vary; unsupported whole-features are rejected at dispatch with a clean note.

### Deterministic typing relay (R3) — built, fully deterministic

Broker-owned, driven by signals that already exist; never by a tool the LLM chooses (the agent-facing
`send_typing` tool is removed from the default set — claude `main.go:790-794`, codex `:615-619` — and
kept only as an internal primitive). **Mechanics (corrected for the real goroutine boundary):** the
`RouteWorker.run` select loop (`worker.go:83-154`) gains a new `<-ticker.C` arm and the worker gains a
per-route `*time.Timer`/ticker field — this adds **per-route state and one select-arm, but no new
goroutine**. Armed in the inbound-delivered-to-a-claimed-route branch (`forwardOrFallback`, the
`delivered` path at `worker.go:303`); a ~4s `SendTyping` pulse runs until **disarmed on the first
`reply` JobOutbound** dispatched for the route, or an idle timeout. Non-reply tool-calls
(`edit_message`/`react`/`download_attachment`, which arrive as JobOutbound via `handler.go:120` →
`dispatchOutbound`) re-arm but do NOT disarm. **CLI-mode-noise guard (F2):** the default output mode
is CLI, where the agent answers in the terminal and never replies to Telegram; pulsing "typing…" then
is pure noise. The broker doesn't track mode, so the relay **only arms once the route has dispatched
≥1 `reply` this session** (a lazy "this session actually talks to Telegram" signal) — and disarms per
above. Gated by `Capabilities.Typing`.

### Rich text (R1)

Agent writes ONE dialect — standard markdown — and the channel converts; the agent never escapes.
`Markup=markdown` maps to Telegram **HTML** inside the telegram package (HTML's 3-char escape surface
`< > &` is far safer for machine output than MarkdownV2's 18-char surface), and the existing
plaintext-fallback on parse error (`outbound.go:66-76`) prevents dropped messages. The R1 fix: expand
`mdToTelegramHTML` (today bold/inline-code/code-block only — `format_test.go:15-16` asserts underscores
and links pass through as literals) to the full common-markdown set with golden tests; the manifest's
`RichText` advertisement must match exactly what the converter renders. **Chunking hazard (F3):** today
`SendReply` chunks RAW markdown at 4096 FIRST then converts per chunk — safe only because every current
construct opens+closes within a chunk. Once links/quotes/lists/code-fences exist, a raw 4096 split can
land *inside* a `[label](url)` / blockquote / fenced block and emit half-a-construct. **Fix:** chunking
must be construct-boundary-aware — never split inside a link, fenced code block, or blockquote run
(convert-then-chunk on tag-safe boundaries, or boundary-aware raw split). Golden tests MUST include a
link/quote/code-fence straddling the 4096 boundary. `entities[]` is a deferred future seam (UTF-16
offset math for a 2nd channel that doesn't exist).

### Media / files / polls (R2)

`Outbound.Files []string` → `Media []MediaItem{Kind, Path, URL, Caption, Spoiler}`. `Kind` is
semantic: `photo` = compressed in-chat preview (`sendPhoto`), `file` = byte-for-byte original
(`sendDocument`) — the load-bearing user distinction. `video/audio/voice/animation` map to their
methods. Single item → channel picks the method by Kind; multiple → `sendMediaGroup` when `Albums`,
with **all 2–10 count + mixing rules and the sequential fallback handled IN the telegram package**
(the gate only does the channel-neutral `!Albums→sequential` degrade). Limits live in the channel,
surfaced via the manifest: 4096 text, 1024 caption (over-limit → follow-up message by the gate), 50MB
send (over-cap → clear error), 20MB download (gates `DownloadAttachment`). The `Files` hard-error is
removed. Polls get a tiny `poll` tool → `sendPoll`; on a channel without `Polls` the gate hard-rejects
with a render-as-numbered-text note (tool stays registered).

### Degradation (R9)

Centralized in `capability.Gate` — data-driven, identical for every channel: `!RichText`→plain;
unsupported Kind→demote (`photo`→`document` if `OriginalFile`, else drop + note + durable log);
`!Albums`→sequential; text/caption over-limit→construct-aware split / trim+follow-up; over send-cap→
hard error; over download-cap→skip+tell agent; `!Polls`→reject+note; bad reaction emoji→reject+note;
`!Typing`→relay no-ops. Every down-convert emits an agent-visible note AND a durable outbound log line
when content is altered/dropped.

### No-leak strategy (R7)

(1) **Vocabulary** — core references only `Capabilities` fields + neutral `Outbound{Markup, Media,
Poll}`. (2) **Delete the leaks** — `Outbound.ParseMode` (`dispatch.go:38-39`) AND `EditArgs.ParseMode`
(`dispatch.go:70`) removed, replaced by `Markup`. (3) **Mechanical enforcement** — a CI grep-guard
test asserts no `gotgbot`/`MarkdownV2`/`message_thread_id`/`4096`/`parse_mode`/`sendPhoto` literals
appear outside `internal/channel/telegram`, that `internal/channel` never imports `gotgbot`, **and that
`internal/capability` imports nothing but `c3types` + stdlib** (purity invariant). `Markup=native` is a
restricted opaque pass-through the channel re-validates; the gate flags its use so a re-leak is visible.

### Codex/Claude parity (R8)

Same struct, same `GuidanceFor`, same shared tool-description helper for both. v1 has one channel, so
init-time delivery (Claude MCP instructions; Codex AGENTS.md + per-turn head line) gives both CLIs the
correct, identical guidance. The only honest asymmetry is the future cross-channel-refresh path (Claude
needs turn-time delivery to refresh, Codex already has it) — moot at one channel, documented as the
multi-channel seam. Claude's `notifications/claude/channel` inbound STRING wire shape is untouched (caps
ride `hello_ack`/`attached`). Streaming reported per-CLI-delivery (both OFF in v1).

## Key interfaces (sketch)

```go
// internal/channel/channel.go — ONE additive method, NO arg in v1 (avoids channel->broker cycle)
type Channel interface {
  Name() string
  Start(ctx context.Context, host Host) error
  Stop() error
  Capabilities() c3types.Capabilities // NEW (no-arg in v1; per-route variance later via a c3types key)
  SendReply(args c3types.ReplyArgs) (sentMessageID int64, err error)
  SendTyping(chatID int64, threadID *int64) error
  EditMessage(args c3types.EditArgs) (*c3types.EditResult, error)
  React(args c3types.ReactArgs) error
  DownloadAttachment(fileID string) (path string, err error)
  CreateTopic(chatID int64, name string) (topicID int64, err error) // gated by Capabilities.Threads
  ValidateTopic(chatID, threadID int64) error
}

// internal/c3types — neutral Outbound (delete ParseMode leak + Files hard-error)
type Outbound struct {
  Channel string; ChatID int64; TopicID *int64
  Text    string
  Markup  Markup       // none|markdown|native — intent, NOT a Telegram tag
  Media   []MediaItem  // typed kinds (replaces Files []string)
  Poll    *PollSpec
  ReplyTo *int64
}
type MediaKind string // photo|file|video|audio|voice|animation
type MediaItem struct { Kind MediaKind; Path, URL, Caption string; Spoiler bool }

// internal/c3types/caps.go — flat manifest (RichTextDialect dropped from v1 — RichText bool is enough)
type Capabilities struct {
  Channel string; RichText bool
  MaxMessageRunes, MaxCaptionRunes int; AutoChunks bool
  MediaKinds []MediaKind; CompressedPhoto, OriginalFile, Albums bool
  MaxAlbumItems int; MaxSendBytes int64
  Polls, Reactions, ReactionsSingle, EditMessages, Threads, Typing bool
  ReactionAllowedSet []string
  Inbound InboundCaps // MaxDownloadBytes, InboundKinds, SupportsReplyContext
  Stream  StreamCaps  // PER-CLI-DELIVERY: StreamViaEdit bool (FALSE in v1), MinEditInterval
}

// internal/capability — single choke point + single agent-surface source (imports only c3types)
func Gate(c c3types.Capabilities, out c3types.Outbound) (parts []c3types.Outbound, notes []string, err error)
func GuidanceFor(c c3types.Capabilities) string

// internal/mode — shared, caps-driven, both CLIs (Combined gains a caps arg)
func Combined(c c3types.Capabilities) string // = ModeProtocol + MultipartProtocol + capability.GuidanceFor(c)

// internal/ipc — caps on EXISTING hello_ack + attached (no new turn-path ops in v1)
type HelloAckMsg struct { /*existing*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
type AttachedMsg  struct { /*existing Channel field*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
```

## Decisions on the open questions

1. **Streaming source (R4):** DEFERRED in v1 — no Claude Code source frame exists (verified); manifest
   `Stream.StreamViaEdit=false`, guidance says unavailable, no relay code shipped. Two future paths
   flagged for Karthi (Codex opt-out reversal; Claude SDK/Messages-API host pivot).
2. **Codex forwarder opt-out:** NOT reversed in v1 (deliberate). Flagged for Karthi.
3. **Rich-text fidelity:** HTML-now (expand converter); `entities[]` deferred as a seam.
4. **`Markup=native`:** keep, restricted + gate-flagged.
5. **Inbound `Attachment.Kind`:** defer generalizing the vocabulary; add `InboundCaps` only.
6. **Tool-presence stability:** keep fixed across channels; reject whole-feature unsupport at dispatch.
7. **`Capabilities()` signature:** no-arg in v1 (avoids the import cycle); RouteKey-arg deferred.
8. **Forcing-function date:** none imposed autonomously; build P0–P5 + P7 now; R4 a separate milestone.

## Implementation plan

- **P0 — Manifest type + Telegram literal + interface method (no behavior change).** Add `c3types/caps.go`
  (`Capabilities` + `Inbound`/`Stream` subs, `Markup`, `MediaKind`, `MediaItem`, `PollSpec`). Add
  **no-arg** `Capabilities()` to the `Channel` interface; implement the static Telegram literal
  (`Stream.StreamViaEdit=false`, `Typing=true`). **Grep ALL `channel.Channel` implementers + test
  doubles and add the method in the SAME commit** (build breaks otherwise). Compiles + passes.
- **P1 — Kill the ParseMode leaks + neutral Outbound.** `Outbound.ParseMode`→`Markup`;
  remove `EditArgs.ParseMode` read (`dispatch.go:70`) + drop the unadvertised arg; `Files`→`Media`
  (+`Poll`). Back-compat shims for ONE release inside `dispatchReply` with a PINNED mapping:
  `parse_mode=""`→`Markup=markdown`; `parse_mode="HTML"|"MarkdownV2"`→`Markup=native` (gate-flagged
  pass-through); anything else→reject. `Files`→`Media{Kind:file}`. Assert raw-Telegram rejected unless
  `Markup==native`.
- **P2 — `capability.Gate` + R1 converter expansion.** Build `internal/capability` (`Gate`+`GuidanceFor`,
  pure, table-tested, imports only `c3types`). Route `dispatchReply` through `Gate`. Move chunking +
  caption logic into `Gate` with **construct-boundary-aware splitting** (never split inside link/code-
  fence/blockquote) + golden tests incl. constructs straddling 4096. Expand `mdToTelegramHTML` to the
  full common-markdown set with golden tests; manifest `RichText` matches what it renders. Add the
  golden test that guidance derives from caps.
- **P3 — Media + poll send paths.** Replace the `Files` hard-error with per-Kind send methods +
  `sendMediaGroup` (2–10 + mixing + sequential fallback all in-channel). Add `sendPoll` + the gated
  `poll` tool. Validate reaction emoji against `ReactionAllowedSet`. Surface 50MB/20MB caps.
- **P4 — Caps delivery + shared agent surface.** Add `Capabilities` to `hello_ack` AND `attached`.
  `mode.Combined(caps)` folds in `GuidanceFor` (ripples to all 3 consumers + `protocol_test.go`'s 6
  tests). ONE shared helper generates tool Descriptions/InputSchemas from the manifest (both adapters).
  Inject live caps into the Codex per-session turn-text head line. Keep tool PRESENCE stable. **Add
  CONTENT assertions** to the two adapter wire tests (today they only assert `Instructions != ""`) +
  a per-channel golden manifest test.
- **P5 — Deterministic typing relay.** Add the ticker select-arm + per-route timer state to
  `RouteWorker`; arm in the `forwardOrFallback` delivered branch, re-arm on non-reply tool-calls via
  `dispatchOutbound`, **disarm on first `reply`**, only arm after the route has dispatched ≥1 reply
  this session (CLI-mode guard), gated by `Capabilities.Typing`. Remove `send_typing` from the default
  tool set (keep internal).
- **P6 — Streaming: NOT built in v1.** Manifest `Stream.StreamViaEdit=false`; guidance states it's
  unavailable. No relay code, no new IPC ops (avoids producer-less dead surface). Tracked as a separate
  milestone gated on a real reasoning source (see Decisions 1–2).
- **P7 — Enforcement + cleanup.** Add the CI grep-guard test (no Telegram literals outside the telegram
  package; `internal/channel` no `gotgbot`; `internal/capability` imports only `c3types`+stdlib). Delete
  the back-compat shims. Full test suite + a live Telegram smoke checklist (rich-text marks, photo-vs-
  document, album, poll, reaction validation, deterministic typing).

## Residual / sanctioned leaks (documented, not silent)

1. `Markup=native` — sanctioned opaque pass-through (restricted + gate-flagged).
2. Gated Telegram-named interface methods (`CreateTopic`/single-emoji `React`/file_id download) — inert
   until a 2nd channel forces a rename.
3. `c3types.Inbound.Attachment.Kind` — still an open enum on Telegram terms (`types.go:39/41`); inbound
   vocabulary generalization deferred. The CI grep-guard catches NEW outbound leaks, not these three.

## Items for Karthi (review after the build)

- **Streaming (R4) is the big one.** Verified: Claude Code exposes no reasoning frames to an MCP server
  or hooks, so C3 (as an MCP adapter) cannot stream Claude's thinking. Options: (a) reverse the Codex
  forwarder opt-out → Codex-only streaming (asymmetric; opt-out was deliberate); (b) pivot C3 to host
  the agent via the Agent SDK / a Messages-API proxy → real streaming for both (large architectural
  change). v1 ships typing only. **Which path (if any) do you want?**
- Confirm Decisions 2 (Codex opt-out), 4 (`native` hatch), 5 (inbound enum), 6 (tool presence).
- Live Telegram round-trip from your phone is the one check I can't run myself.
