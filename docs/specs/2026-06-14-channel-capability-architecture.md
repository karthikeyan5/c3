# Spec — C3 Channel Capability Manifest + Gate (CMG)

**Date:** 2026-06-14 · **Status:** DRAFT (to be hardened by 3 critique passes, then built) · **Author:** Ram (orchestrated; synthesized from a 10-agent research+design+judge workflow)

Supersedes the scattered channel/formatting assumptions in the v5 rearch spec for the
parts it touches. This is the architecture for C3's **rich-content + channel-capability**
work (the new P0 in [ROADMAP.md](../../ROADMAP.md)), sequenced ahead of terminal-control.

## Goal & requirements

C3 must support the full Telegram feature surface, delivered through a channel-capability
system that does not break when other channels are added, and that tells the agent exactly
what it can do.

- **R1 — Rich-text formatting** for Telegram (top priority).
- **R2 — Full media / file / poll support** (compressed photo vs original file, video/audio/voice/animation, albums, polls) honoring all Bot API limits.
- **R3 — Deterministic typing indicator** — relayed programmatically by C3 while the agent works mid-turn; NOT an LLM-invoked tool.
- **R4 — Deterministic streaming of reasoning/thinking** — relayed programmatically (message editing), NOT LLM-driven.
- **R5 — Per-channel capability declaration** — each channel declares exactly what it supports; a new channel never gets fed features it can't render.
- **R6 — Agent-facing capability + prompt surface** — C3 exposes the active channel's capabilities + formatting guidance to the agent (Claude **and** Codex) so it formulates a correct message.
- **R7 — No Telegram leak into core** — core stays channel-agnostic; only feature-level supports/does-not-support crosses the boundary.
- **R8 — Codex/Claude parity** — identical mechanism for both CLIs.
- **R9 — Forward-looking + simplest-possible**, with graceful degradation.

## Architecture: Capability Manifest + Gate (CMG)

One flat, JSON-serializable `Capabilities` struct returned by `Channel.Capabilities(key)`,
carried to **both** adapters over the existing `hello_ack` **and** re-sent on every
`attached` (so a cross-channel re-attach refreshes the agent surface). It is rendered once
by a shared `capability.GuidanceFor(caps)` into the Claude MCP instructions and the Codex
guidance. Core carries only the manifest plus a typed, channel-neutral `Outbound` (a
`Markup` intent enum + `Media []MediaItem` + optional `Poll`); the existing `ParseMode`
leak is deleted. A single broker-side `capability.Gate(caps, outbound)` is the **one choke
point** that validates + degrades before any channel method runs. All Telegram-specific
rendering stays inside `internal/channel/telegram`.

### Layers

- **L0 `internal/channel/telegram` (impl expanded, boundary unchanged).** Owns ALL Telegram
  specifics: the static `Capabilities()` literal; `mdToTelegramHTML` **expanded** from today's
  bold+code-only to italic/underline/strike/spoiler/links/lists/quotes (the R1 fix); a media
  branch replacing the `Files` hard-error (`outbound.go:29-31`) with `sendPhoto`(compressed) /
  `sendDocument`(original) / `sendVideo` / `sendAudio` / `sendVoice` / `sendAnimation` by Kind,
  and `sendMediaGroup` for albums (2–10 + mixing rules); a new `sendPoll` path; 4096/1024
  chunking; 50MB-send / 20MB-download caps; single-emoji reaction validated against Telegram's
  allowed set. **Nothing above this layer names `parse_mode`, `gotgbot`, `4096`, or `sendPhoto`.**
- **L1 `internal/channel/channel.go` + `internal/c3types` (the firewall).** Adds
  `Capabilities(key RouteKey) Capabilities` to the `Channel` interface (RouteKey arg now so
  per-route variance is non-breaking later). Replaces `Outbound.Files []string` with
  `Media []MediaItem` and `Outbound.ParseMode` (the live leak) with `Markup` (`none|markdown|native`).
  Adds `Poll *PollSpec`. `Capabilities` is feature-level only (booleans, small enums, int limits)
  plus an `Inbound` sub-section (download cap, inbound kinds, reply-context). Telegram-shaped
  methods that remain (`CreateTopic`/`ValidateTopic`, single-emoji `React`, file_id
  `DownloadAttachment`) stay but are **gated** by manifest booleans — leak made inert, not rewritten.
- **L2 `internal/capability` (NEW; pure; no channel/broker imports).** Owns
  `Gate(caps, Outbound) ([]Outbound, notes, error)` — the single choke point: validates
  (hard-reject poll when `!Polls`; reaction emoji against allowed set), down-converts
  (`Markup→none` when `!RichText`; `photo→document` when `!CompressedPhoto`; album→sequential
  when `!Albums`; split text > `MaxMessageRunes`; trim/split caption > `MaxCaptionRunes`; demote
  unsupported media Kind), emits human-readable notes **and durably logs silent down-converts**
  (reconciling with the existing don't-lose-content rule). Also owns `GuidanceFor(caps) string`
  so the SAME data drives degradation AND what the agent is told — they cannot drift. Pure +
  table-tested; reused by every future channel for free.
- **L3 `internal/broker` (dispatch + RouteWorker relays).** `dispatchReply` builds the neutral
  `Outbound` from tool args, runs `capability.Gate`, then calls the channel (chunking/markup
  coercion moves OUT of literals into the gate). The per-route serial `RouteWorker` drives the
  two deterministic relays — see below. Relays and the agent's real reply are serialized by the
  existing single worker (no new concurrency); a final reply pre-empts a pending stream edit.
- **L4 `internal/ipc`.** Adds `Capabilities` to the EXISTING `hello_ack` (optional field) AND to
  the EXISTING `attached` payload (`AttachedMsg` already carries `Channel`). Adds optional,
  additive, back-compat `OpTurnActivity` (adapter→broker) and `OpStreamDelta` (adapter→broker,
  turn-path). No content semantics added to core beyond the neutral `Outbound` + `Capabilities`.
- **L5 adapters + `internal/mode` (shared agent surface).** Both adapters call ONE shared mode
  helper to build tool Descriptions/InputSchemas from the manifest (kills the inline-literal
  duplication in `registerTools`) and call `mode.Combined(caps)` = `ModeProtocol` +
  `MultipartProtocol` + `capability.GuidanceFor(caps)`. Claude: spliced into MCP instructions
  (rebuilt on `hello_ack`/`attach`). Codex: the AGENTS.md block stays the static base, but the
  **active-channel caps are injected per-session via the inbound turn-text head line** so Codex
  is not stuck on a stale default. Tool PRESENCE stays STABLE across channels (`ListChanged:false`
  is wired today); only descriptions/enums vary, and unsupported whole-features are rejected at
  dispatch with a clean note — never by removing a registered tool mid-session.

### Deterministic relays (R3, R4)

Both relays are **broker-owned**, run in the existing per-route serial `RouteWorker`, and are
driven by signals that **already exist** — never by a tool the LLM chooses (the agent-facing
`send_typing` tool is removed from the default set; it stays as an internal primitive).

- **Typing (R3) — fully deterministic, day one.** Armed on the signal C3 already has: an
  inbound delivered to a claimed route (`worker.go` `forwardOrFallback`, the delivered path)
  and re-armed on each `OpToolCall` arrival (`handler.go:120`); a ~4s `SendTyping` ticker runs
  until the first real reply is dispatched or an idle timeout fires. Zero adapter cooperation.
- **Streaming reasoning (R4) — wired but honestly gated OFF.** The relay machinery (open a
  placeholder via `SendReply` on first delta → coalesce → `EditMessage` on `≥MinEditInterval`
  with no-op diffing → roll to a new message past `MaxMessageRunes` → final edit on turn end)
  is fully built in the worker, BUT gated behind a **per-CLI-delivery** `Stream` flag reported
  `FALSE` until a real source frame exists. **Verified ground truth:** the Claude adapter (an
  MCP server) sees only `ping`/`initialize`/`tools/list`/`tools/call` — no reasoning frames; the
  Codex forwarder explicitly opts out of `item/agentMessage/delta`, `item/reasoning/textDelta`,
  `item/reasoning/summaryTextDelta`. So R4 ships wired-but-OFF; flip the flag when a source is
  added (a Claude Code hook/notification, or reversing the Codex opt-out + consuming the deltas)
  and streaming lights up with **zero core change**. The architecture refuses to advertise
  `StreamViaEdit:true` while the source is empty (no silent no-op).

### Rich text (R1)

Agent writes ONE dialect — **standard markdown** — and the channel converts; the agent never
escapes special chars. `Markup=markdown` maps to Telegram **HTML** inside the telegram package
(HTML's 3-char escape surface `< > &` is far safer for machine output than MarkdownV2's 18-char
surface), and the existing plaintext-fallback on parse error (`outbound.go:66-76`) prevents
dropped messages. The R1 fix: `mdToTelegramHTML` today handles only bold + inline-code +
code-blocks — italic/underline/strike/spoiler/links/lists/quotes silently pass through as
literals. Expand it to the full common-markdown set with golden tests, and the manifest's
`RichText` advertisement must match exactly what the converter renders. The escaping-free
`entities[]` path is a deliberately-**deferred** future `RichTextDialect` seam (it needs UTF-16
offset math for a second channel that doesn't exist yet).

### Media / files / polls (R2)

`Outbound.Files []string` → `Media []MediaItem{Kind, Path, URL, Caption, Spoiler}`. `Kind` is
semantic: `photo` = compressed in-chat preview (`sendPhoto`), `file` = byte-for-byte original
(`sendDocument`) — the load-bearing user distinction. `video/audio/voice/animation` map to their
methods. Single item → channel picks the method by Kind; multiple → `sendMediaGroup` when
`Albums` (2–10 + mixing rules enforced in-channel; fall back to sequential when no legal group
forms). Limits live in the channel and are surfaced via the manifest: 4096 text, 1024 caption
(over-limit demoted to a follow-up message by the gate), 50MB send (over-cap = clear error),
20MB download (gates `DownloadAttachment`). The `Files` hard-error is removed. Polls get a tiny
`poll` tool mapping question/options/quiz/correct-idx/explanation to a new `sendPoll`; on a
channel without `Polls` the gate hard-rejects with a render-as-numbered-text note (tool stays
registered).

### Degradation (R9)

Centralized in `capability.Gate` — data-driven, identical for every channel. `!RichText`→plain;
unsupported Kind→demote (`photo`→`document` if `OriginalFile`, else drop + note + durable log);
`!Albums`→sequential; illegal mixing→split legal groups; text/caption over-limit→split/trim+
follow-up; over send-cap→hard error; over download-cap→skip+tell agent; `!Polls`→reject+note;
bad reaction emoji→reject+note; `!Typing`→relay no-ops; per-CLI `!StreamViaEdit`→buffer, only
final reply shown. Every down-convert emits an agent-visible note AND a durable log line when
content is altered/dropped.

### No-leak strategy (R7)

Three enforced layers: (1) **Vocabulary** — core references only `Capabilities` fields + neutral
`Outbound{Markup, Media, Poll}`. (2) **Delete the one real leak** — `c3types.Outbound.ParseMode`
(read at `dispatch.go:38-39` and `:70`) removed, replaced by `Markup`. (3) **Mechanical
enforcement** — a CI grep-guard test asserts no `gotgbot`/`MarkdownV2`/`message_thread_id`/`4096`/
`parse_mode`/`sendPhoto` literals appear outside `internal/channel/telegram`, and that
`internal/capability` + `internal/channel` (the firewall files) never import `gotgbot`. `Markup=native`
is a restricted opaque pass-through the channel re-validates, and the gate flags its use so a
re-leak is visible. Leftover Telegram-shaped methods are gated-inert (renaming them buys nothing
until a 2nd channel exists).

### Codex/Claude parity (R8)

Same struct, same `GuidanceFor`, same shared tool-description helper for both adapters. Claude:
guidance via MCP instructions (rebuilt per `hello_ack`/`attach`). Codex: the AGENTS.md block (static
at setup) PLUS a per-session active-channel head line in the turn-text. Streaming reported
per-CLI-delivery so both honestly report OFF until wired. Claude's `notifications/claude/channel`
inbound STRING wire shape untouched (caps ride `hello_ack`/`attached`).

## Key interfaces (sketch)

```go
// internal/channel/channel.go
type Channel interface {
  Name() string
  Start(ctx context.Context, host Host) error
  Stop() error
  Capabilities(key c3types.RouteKey) c3types.Capabilities // NEW
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

// internal/c3types/caps.go — flat manifest, crosses every boundary
type Capabilities struct {
  Channel string; RichText bool; RichTextDialect string
  MaxMessageRunes, MaxCaptionRunes int; AutoChunks bool
  MediaKinds []MediaKind; CompressedPhoto, OriginalFile, Albums bool
  MaxAlbumItems int; MaxSendBytes int64
  Polls, Reactions, ReactionsSingle, EditMessages, Threads bool
  ReactionAllowedSet []string
  Inbound InboundCaps // MaxDownloadBytes, InboundKinds, SupportsReplyContext
  Stream  StreamCaps  // PER-CLI-DELIVERY: StreamViaEdit bool, MinEditInterval
}

// internal/capability — single choke point + single agent-surface source
func Gate(c c3types.Capabilities, out c3types.Outbound) (parts []c3types.Outbound, notes []string, err error)
func GuidanceFor(c c3types.Capabilities) string

// internal/mode — shared, caps-driven, both CLIs
func Combined(c c3types.Capabilities) string // = ModeProtocol + MultipartProtocol + capability.GuidanceFor(c)

// internal/ipc — caps on EXISTING hello_ack + attached; two additive turn-path ops
type HelloAckMsg struct { /*existing*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
type AttachedMsg  struct { /*existing Channel field*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
type TurnActivityMsg struct { Op Op `json:"op"`; State string `json:"state"` } // optional refinement
type StreamDeltaMsg  struct { Op Op `json:"op"`; Text string `json:"text"`; Final bool `json:"final,omitempty"` }
```

## Decisions on the open questions (defaults chosen for the autonomous build)

These were flagged for Karthi; I'm proceeding with the recommended defaults and listing them for review.

1. **Streaming source (R4):** ship the relay **wired but gated OFF** (no source frame exists today). A parallel investigation checks whether a Claude Code hook/notification can surface reasoning; if a clean source exists it's noted but not required for this build.
2. **Codex forwarder opt-out:** **do NOT reverse** in this pass (it was deliberate — noise/cost). Flag for Karthi.
3. **Rich-text fidelity:** **HTML-now** (expand `mdToTelegramHTML`); `entities[]` deferred as a dialect seam.
4. **`Markup=native` escape hatch:** **keep**, restricted + gate-flagged.
5. **Inbound `Attachment.Kind`:** **defer** generalizing the vocabulary; add `InboundCaps` only.
6. **Tool-presence stability:** **keep fixed** across channels (gate descriptions/enums; reject whole-feature unsupport at dispatch). No per-channel un-registration.
7. **Forcing-function date:** none imposed autonomously; build P0–P5 + P7 now, P6 wired-OFF, R4 a separate milestone.

## Implementation plan

- **P0 — Manifest type + Telegram literal (no behavior change).** Add `c3types/caps.go`
  (`Capabilities` + `Inbound`/`Stream` subs, `Markup`, `MediaKind`, `MediaItem`, `PollSpec`);
  add `Capabilities(key)` to the `Channel` interface; implement the static Telegram literal with
  `Stream.StreamViaEdit=false`. Pure additive; compiles + passes.
- **P1 — Kill the ParseMode leak + neutral Outbound.** `ParseMode`→`Markup`, `Files`→`Media`
  (+`Poll`). Keep `Files→Media{Kind:file}` and `parse_mode→Markup` shims for ONE release inside
  `dispatchReply` (no flag-day). Stop reading `parse_mode` into a Telegram string. Assert
  raw-Telegram rejected unless `Markup==native`.
- **P2 — `capability.Gate` + R1 converter expansion.** Build `internal/capability`
  (`Gate`+`GuidanceFor`, pure, table-tested). Route `dispatchReply` through `Gate`; move 4096
  chunking + caption logic into `Gate` (golden tests vs current boundaries). Expand
  `mdToTelegramHTML` to the full common-markdown set with golden tests; manifest `RichText` must
  match what it renders. Add the golden test that guidance derives from caps.
- **P3 — Media + poll send paths.** Replace the `Files` hard-error with the per-Kind send methods
  + `sendMediaGroup` (2–10 + mixing in-channel). Add `sendPoll` + the gated `poll` tool. Validate
  reaction emoji against `ReactionAllowedSet`. Surface 50MB/20MB caps via the manifest.
- **P4 — Caps delivery + shared agent surface.** Add `Capabilities` to `hello_ack` AND `attached`.
  `mode.Combined` takes caps + folds in `GuidanceFor`. ONE shared helper generates tool
  Descriptions/InputSchemas from the manifest (both adapters). Inject live active-channel caps into
  the Codex per-session turn-text head line. Keep tool PRESENCE stable; update the 3 mode-consumer
  tests + add a per-channel golden manifest test.
- **P5 — Deterministic typing relay.** `armTyping` on the `RouteWorker` (inbound-delivered +
  `OpToolCall`), ~4s pulse, disarm on first reply/idle, gated by `Capabilities.Typing`. Remove
  `send_typing` from the default tool set (keep internal). `OpTurnActivity` optional refinement,
  not depended on.
- **P6 — Streaming relay, wired-but-gated.** `onStreamDelta` in the worker
  (placeholder→coalesce→`EditMessage` diff+throttle+rollover→final), gated by per-CLI `Stream`
  (FALSE default). Add `OpStreamDelta` on the turn path. Document the flip-the-flag follow-up.
- **P7 — Enforcement + cleanup.** Add the CI grep-guard test. Delete the back-compat shims. Full
  test suite + a live Telegram smoke checklist (rich text marks, photo-vs-document, album, poll,
  reaction validation, deterministic typing, cross-channel re-attach guidance refresh).

## Residual / sanctioned leaks (documented, not silent)

1. `Markup=native` — sanctioned opaque pass-through (restricted + gate-flagged).
2. Gated Telegram-named interface methods (`CreateTopic`/single-emoji `React`/file_id download) —
   inert until a 2nd channel forces a rename.
3. `c3types.Inbound.Attachment.Kind` — still an open enum on Telegram terms (`types.go:39`);
   inbound vocabulary generalization deferred. The CI grep-guard catches NEW outbound leaks, not
   these three known ones.

## Items for Karthi (review after the build)

- R4 streaming needs a real reasoning-source frame to actually fire (see Decision 1 + the parallel
  investigation result). Day-one: typing is fully deterministic; streaming is wired-but-OFF.
- Confirm Decisions 2 (Codex opt-out), 4 (`native` hatch), 5 (inbound enum), 6 (tool presence).
- Live Telegram round-trip from your phone is the one check I can't run myself.
