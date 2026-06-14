# Spec — C3 Channel Capability Manifest + Gate (CMG)

**Date:** 2026-06-14 · **Status:** DRAFT (critique passes 1–2 folded; pass 3 pending) · **Author:** Ram (orchestrated; 10-agent research+design+judge workflow, then critique-hardened)

**Revision history**
- v1 — base synthesis.
- v2 — critique pass 1 folded (code-grounding F1–F12): streaming deferred (no Claude-Code reasoning source); `Capabilities()` no-arg (avoids `channel→broker` cycle); typing relay re-specified; construct-aware chunking; pinned shim; album mixing in-channel; honest Claude init-only instructions.
- v4 — critique pass 3 folded (GO verdict): fix the 2 pre-existing red attach-collision tests so phase gates mean something (gate = build clean + no NEW failures vs baseline); corrected the typing-flag anchor; noted the `ReplyArgs = Outbound` alias ripple; named the broker-setup caps source; split P2 into P2a/P2b; `sendPoll` in its own file.
- v3 — **critique pass 2 folded.** Blockers: typing has-replied flag anchored on the per-connection holder (not the per-route worker); multi-part send contract defined + existing silent-success bug fixed. Majors: media is first-class new surface (reply tool gains a `media` arg); `edit_message` joins the markup system; `MarkdownV2` shim → reject+note; one concrete chunking algorithm; concrete `GuidanceFor` template + no per-turn Codex injection in v1. Scope trims: **albums descoped to sequential sends** (`Albums=false` in v1); **client-side reaction validation dropped** (Telegram is the authority).

Supersedes the scattered channel/formatting assumptions in the v5 rearch spec for the parts it touches.
This is C3's **rich-content + channel-capability** architecture (new P0 in [ROADMAP.md](../../ROADMAP.md)),
sequenced ahead of terminal-control.

## Requirements

- **R1 — Rich-text formatting** for Telegram (top priority).
- **R2 — Media / file / poll support** (compressed photo vs original file, video/audio/voice/animation, polls) honoring Bot API limits. *(Albums descoped to sequential sends in v1.)*
- **R3 — Deterministic typing indicator**, programmatic, mid-turn — NOT an LLM tool. **(Built in v1.)**
- **R4 — Deterministic streaming of reasoning.** **DEFERRED** — no reasoning source frame is observable to an MCP adapter (see below).
- **R5 — Per-channel capability declaration.**
- **R6 — Agent-facing capability + prompt surface** (Claude **and** Codex).
- **R7 — No Telegram leak into core.**
- **R8 — Codex/Claude parity.**
- **R9 — Forward-looking + simplest-possible**, graceful degradation.

## Streaming reality (R4) — deferred, with evidence

The 2026-06-14 capability investigation is decisive: **Claude Code exposes no in-flight reasoning/assistant
text to any external process** — hooks fire at discrete points with tool I/O only; an MCP server sees only
its own tool calls; only the raw Messages API (`thinking_delta`) or the Agent SDK (completed messages)
expose reasoning. C3's Claude adapter is an MCP server → **no source frame**. For Codex the frames exist on
the app-server but the C3 forwarder deliberately opts out (`forwarder.go:51-55`). So R4 needs a Karthi
decision: (a) reverse the Codex opt-out → Codex-only streaming (asymmetric); or (b) pivot C3 to host the
agent via the Agent SDK / Messages-API proxy → both CLIs (large change). v1 ships **typing only**; the
manifest reports `Stream.StreamViaEdit=false`, the guidance says streaming is unavailable, and **no relay
code / no new IPC ops are added** (no dead wire surface, consistent with the already-unused `OpServerInfo`).

## Architecture: Capability Manifest + Gate (CMG)

One flat, JSON-serializable `Capabilities` struct returned by `Channel.Capabilities()`, carried to both
adapters over the existing `hello_ack` AND the existing `attached` payload, rendered once by a shared
`capability.GuidanceFor(caps)`. Core carries only the manifest plus a typed, channel-neutral `Outbound`
(a `Markup` intent enum + `Media []MediaItem` + optional `Poll`); the `ParseMode` leaks are deleted. A
single broker-side `capability.Gate` is the one validate+degrade choke point. All Telegram-specific
rendering stays inside `internal/channel/telegram`.

### Layers

- **L0 `internal/channel/telegram`.** Owns ALL Telegram specifics: the static `Capabilities()` literal;
  `mdToTelegramHTML` expanded from bold+code-only (`format.go:32-106`) to italic/underline/strike/spoiler/
  links/lists/quotes (R1); a media branch replacing the `Files` hard-error (`outbound.go:29-31`) with
  `sendPhoto`(compressed) / `sendDocument`(original) / `sendVideo` / `sendAudio` / `sendVoice` /
  `sendAnimation` by Kind (**single-item; multiple media → sequential sends — no album grouping in v1**);
  a new `sendPoll`; file existence + `MaxSendBytes` validation before send (channel is impure; the pure
  gate cannot stat files); the 4096/1024 limits + UTF-16 length accounting; 50MB-send/20MB-download caps.
  Nothing above this layer names `parse_mode`, `gotgbot`, `4096`, or `sendPhoto`.
- **L1 `internal/channel/channel.go` + `internal/c3types` (the firewall).** Adds `Capabilities() Capabilities`
  (no arg in v1 — `RouteKey` lives only in `internal/broker`, confirmed; a `RouteKey` arg would cycle).
  Replaces `Outbound.Files []string` with `Media []MediaItem`, `Outbound.ParseMode` (`types.go:69`) with
  `Markup`, and **adds `Markup` to `EditArgs`** (so edits join the markup system). Removes `EditArgs.ParseMode`
  (read at `dispatch.go:70`, never advertised — a dead leak). Adds `Poll *PollSpec`. `Capabilities` is
  feature-level only + an `Inbound` sub-section. Telegram-shaped methods that remain (`CreateTopic`/
  `ValidateTopic`, single-emoji `React`, file_id `DownloadAttachment`) stay, gated by manifest booleans.
- **L2 `internal/capability` (NEW; pure; imports only `c3types` + stdlib).** `Gate(caps, Outbound)
  (parts []Outbound, notes []string, alts []Alteration, err error)` — the choke point: validates
  (hard-reject poll when `!Polls`), down-converts (`Markup→none` when `!RichText`; `photo→document`
  when `!CompressedPhoto`; **multiple media → sequential single sends** always in v1; construct-aware
  split when text > limit; trim/split caption > limit; demote unsupported Kind), returns human notes +
  a structured `[]Alteration`. **The gate is pure — it does NOT log or stat; the broker dispatch (impure)
  writes the durable outbound-alteration log line from `alts`.** Also owns `GuidanceFor(caps) string`,
  the single source feeding both degradation and the agent surface.
- **L3 `internal/broker` (dispatch + RouteWorker typing relay).** `dispatchReply` builds the neutral
  `Outbound`, runs `Gate`, sends the parts **sequentially** (multi-part contract below), logs alterations.
  The per-route serial `RouteWorker` drives the typing relay.
- **L4 `internal/ipc`.** Adds optional, additive `Capabilities` to `HelloAckMsg` (`messages.go:51-59`)
  AND `AttachedMsg` (`messages.go:252`). No new turn-path ops in v1. (Note: IPC has no version field;
  v1 relies on additive-`omitempty` + single-host lockstep `/c3:build`; cross-version semantic compat is
  out of scope.)
- **L5 adapters + `internal/mode` (shared agent surface).** Both adapters call ONE shared helper to build
  tool Descriptions/InputSchemas from the manifest (kills `registerTools` inline duplication) and call
  `mode.Combined(caps)` = `ModeProtocol` + `MultipartProtocol` + `capability.GuidanceFor(caps)`. Delivery:
  Claude's MCP `instructions` are delivered only at `initialize` (`main.go:638,681`) — in v1 there is ONE
  channel so the channel never changes mid-session and init-time delivery is correct. Codex gets the
  AGENTS.md block at setup (`ensureCodexAgentsMd`, static) — **identical once-delivery; NO per-turn caps
  injection in v1** (it would be redundant token bloat at one channel and would break parity by giving
  Codex per-turn / Claude per-init). The `attached` payload carries caps for the future multi-channel
  turn-time-refresh seam. Tool PRESENCE stays stable (`ListChanged:false`); only descriptions/enums vary.

### Multi-part send contract (the Gate→dispatch boundary)

`Gate` may split one logical reply into ordered `parts`. Dispatch sends them **sequentially, in order**.
On part-k failure: **stop (fail-fast), do NOT continue**, and return a structured result to the agent
stating exactly how many landed — e.g. `"sent 2 of 4; part 3 failed: <err>"`. **Never report silent
success** — this also fixes the existing bug at `outbound.go:82-84` where a chunk-k>0 failure logs, breaks,
and returns `firstID, nil` (success). The agent-visible `sentMessageID` is the **first** part's id;
reply-threading/edits target the first part — documented in `GuidanceFor` ("a long reply may arrive as
several messages; edits/replies reference the first"). The typing relay disarms on the **first** part
dispatched.

### Deterministic typing relay (R3) — built

Broker-owned, driven by signals that already exist; never an LLM tool (the agent-facing `send_typing` tool
is removed from the default set — claude `main.go:790-794`, codex `:615-619`). **Mechanics:** the
`RouteWorker.run` select loop (`worker.go:83-154`) gains a `<-ticker.C` arm + a per-route timer field
(new per-route state, **no new goroutine**). **Session anchoring (blocker fix):** `RouteWorker` is
per-RouteKey and outlives individual sessions, so the "this session talks to Telegram" signal is a
`hasReplied` field on the **per-connection holder `Stub`** (the route is set via `Stub.SetRoute` at
`attach.go:572`; the `RouteWorker` reads the holder via `Routes.Holder(key)`, `worker.go:267`), NOT on
the worker — resets naturally per connection. **Arming rule:** arm the ~4s `SendTyping` pulse on inbound-delivered-to-
a-claimed-route (`forwardOrFallback` delivered path, `worker.go:303`) **only once the current holder has
dispatched ≥1 `reply`** (the deterministic "this session is in Telegram mode" proxy — avoids pulsing
"typing…" for default CLI-mode sessions that never reply to Telegram). Re-arm on non-reply tool-calls
(`edit_message`/`react`/`download_attachment` via `dispatchOutbound`); **disarm on the first `reply`** of
the turn or idle timeout. Gated by `Capabilities.Typing`. **Known limitation (documented):** the first
turn of a session gets no typing (the holder hasn't replied yet); typing covers turns 2..N. Acceptable
for v1; the fully-general fix is a real mode signal forwarded by the adapter (future).

### Rich text (R1)

Agent writes standard markdown; the channel converts; the agent never escapes. `Markup=markdown` → Telegram
**HTML** (3-char escape surface `< > &` vs MarkdownV2's 18). Existing plaintext-fallback on parse error
(`outbound.go:66-76`) prevents drops. Expand `mdToTelegramHTML` to the full common-markdown set with golden
tests; manifest `RichText` must match what it renders. **`EditMessage` routes through the SAME converter**
(today it does not convert at all — `outbound.go:135-156`), so edited messages render rich too.

**Chunking algorithm (one strategy, specified):** split the SOURCE markdown on safe boundaries
(paragraph, then line) **never inside a fenced code block, a `[label](url)` link, or a blockquote run**;
convert each part; assert each converted part ≤ `MaxMessageRunes` measured in **UTF-16 code units** (what
Telegram counts). If a single indivisible construct exceeds the limit, hard-split it with an agent note +
durable log. Golden tests MUST include: a link, a blockquote, and a fenced block each straddling the 4096
boundary, AND a single construct > 4096. `entities[]` remains a deferred future `RichTextDialect` seam.

### Media / files / polls (R2)

`Outbound.Files []string` → `Media []MediaItem{Kind, Path, URL, Caption, Spoiler}`. `Kind` semantic:
`photo` = compressed in-chat preview (`sendPhoto`), `file` = byte-for-byte original (`sendDocument`) — the
load-bearing distinction; `video/audio/voice/animation` map to their methods. **Single item → channel
picks by Kind; multiple items → sequential single sends in v1 (no album grouping — descoped; manifest
`Albums=false`).** This is genuinely new end-to-end surface: the **reply tool InputSchema gains a `media`
array** (both adapters), `dispatchReply` parses it, neutral→channel mapping is new, the send methods are
new. **Single-host invariant (load-bearing):** broker, adapters, and the Telegram client share one host +
filesystem (unix socket `c3.sock`; `download_attachment` already returns local cache paths), so a
`MediaItem.Path` produced by the agent resolves at send. `URL` is fetched server-side by Telegram. The
**channel** validates `Path` existence + `MaxSendBytes` (impure); `URL` size is unvalidatable → rely on
Telegram's rejection surfaced as a note. Limits surfaced via the manifest: 4096 text, 1024 caption (over →
follow-up message), 50MB send, 20MB download (gates `DownloadAttachment`). Polls: a tiny `poll` tool →
`sendPoll`; on `!Polls` the gate hard-rejects with a render-as-numbered-text note (tool stays registered).

### Degradation (R9)

Centralized in `Gate`, data-driven: `!RichText`→plain; unsupported Kind→demote (`photo`→`document` if
`OriginalFile`, else drop + note + alteration); multiple media→sequential; text/caption over-limit→
construct-aware split / trim+follow-up; over send-cap→error (channel); over download-cap→skip+note;
`!Polls`→reject+note; `!Typing`→relay no-ops. Every alteration → agent-visible note + (in dispatch)
durable log.

### No-leak strategy (R7)

(1) Core references only `Capabilities` + neutral `Outbound{Markup, Media, Poll}`. (2) Delete `Outbound.ParseMode`
(`dispatch.go:38-39`) AND `EditArgs.ParseMode` (`dispatch.go:70`), replaced by `Markup`. (3) CI grep-guard:
no `gotgbot`/`MarkdownV2`/`message_thread_id`/`4096`/`parse_mode`/`sendPhoto` literals outside the telegram
package; `internal/channel` never imports `gotgbot`; `internal/capability` imports only `c3types`+stdlib;
no TCP dial is introduced (defends the single-host invariant). `Markup=native` is a restricted opaque
pass-through the channel re-validates; the gate flags its use.

### Codex/Claude parity (R8)

Same struct, same `GuidanceFor`, same shared tool-description helper. v1 (one channel): both get caps once
(Claude MCP `initialize` instructions; Codex AGENTS.md at setup) — identical. The future cross-channel
refresh (turn-time delivery: Claude via the inbound `<channel>` block head line, Codex via its turn head
line) is the documented multi-channel seam, moot at one channel. Claude's `notifications/claude/channel`
inbound STRING wire shape untouched. Streaming reported per-CLI-delivery (both OFF in v1).

## Key interfaces (sketch)

```go
// internal/channel/channel.go — no-arg in v1
type Channel interface {
  Name() string
  Start(ctx context.Context, host Host) error
  Stop() error
  Capabilities() c3types.Capabilities // NEW
  SendReply(args c3types.ReplyArgs) (sentMessageID int64, err error)
  SendTyping(chatID int64, threadID *int64) error
  EditMessage(args c3types.EditArgs) (*c3types.EditResult, error)
  React(args c3types.ReactArgs) error
  DownloadAttachment(fileID string) (path string, err error)
  CreateTopic(chatID int64, name string) (topicID int64, err error) // gated by Capabilities.Threads
  ValidateTopic(chatID, threadID int64) error
}

// internal/c3types — neutral Outbound + EditArgs gains Markup
type Outbound struct {
  Channel string; ChatID int64; TopicID *int64
  Text string; Markup Markup; Media []MediaItem; Poll *PollSpec; ReplyTo *int64
}
type Markup string // none|markdown|native
type MediaKind string // photo|file|video|audio|voice|animation
type MediaItem struct { Kind MediaKind; Path, URL, Caption string; Spoiler bool }
// EditArgs gains: Markup Markup

// internal/c3types/caps.go — flat manifest (Albums=false, no ReactionAllowedSet in v1)
type Capabilities struct {
  Channel string; RichText bool
  MaxMessageRunes, MaxCaptionRunes int; AutoChunks bool
  MediaKinds []MediaKind; CompressedPhoto, OriginalFile, Albums bool
  MaxSendBytes int64
  Polls, Reactions, ReactionsSingle, EditMessages, Threads, Typing bool
  Inbound InboundCaps // MaxDownloadBytes, InboundKinds, SupportsReplyContext
  Stream  StreamCaps  // StreamViaEdit bool (FALSE v1), MinEditInterval
}

// internal/capability — pure; returns parts + notes + structured alterations
type Alteration struct { Kind, Detail string }
func Gate(c c3types.Capabilities, out c3types.Outbound) (parts []c3types.Outbound, notes []string, alts []Alteration, err error)
func GuidanceFor(c c3types.Capabilities) string

// internal/mode
func Combined(c c3types.Capabilities) string // = ModeProtocol + MultipartProtocol + capability.GuidanceFor(c)

// internal/ipc — additive caps; no new ops in v1
type HelloAckMsg struct { /*existing*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
type AttachedMsg  struct { /*existing Channel*/ Capabilities *c3types.Capabilities `json:"capabilities,omitempty"` }
```

### `GuidanceFor` template (concrete — what the agent is told)

Rendered from the manifest, e.g. for Telegram v1:

```
CHANNEL CAPABILITIES (telegram):
- Rich text: YES. Write standard markdown (bold, italic, links, lists, inline code, code blocks,
  block quotes, strikethrough, spoilers). C3 converts + escapes for you — do NOT hand-write HTML or
  Telegram tags. A reply longer than ~4096 chars is split automatically into several messages
  (edits/replies reference the first).
- Media: send via the `media` arg. kind="file" delivers the ORIGINAL bytes (PDFs, logs, originals);
  kind="photo" is a COMPRESSED in-chat preview (loses original bytes/EXIF). Also: video, audio, voice,
  animation. Max ~50MB per item. Multiple items are sent one after another.
- Polls: supported via the `poll` tool.
- Typing: shown automatically while you work — do NOT call any typing tool.
- Streaming of reasoning: NOT available on this channel.
```

Negative guidance (e.g. "polls: NOT supported — render as numbered text") is emitted for any false cap so
the agent never formulates undeliverable content. The P4 golden test asserts the rendered guidance matches
the manifest (anti-drift).

## Decisions

1. Streaming (R4): DEFERRED; manifest false; no relay/ops. Two future paths flagged for Karthi.
2. Codex forwarder opt-out: NOT reversed in v1.
3. Rich-text: HTML-now; `entities[]` deferred.
4. `Markup=native`: keep, restricted + gate-flagged.
5. Inbound `Attachment.Kind`: defer generalizing; add `InboundCaps` only.
6. Tool-presence: fixed across channels.
7. `Capabilities()` no-arg in v1.
8. **Albums:** descoped — sequential sends in v1 (`Albums=false`); full grouping is a later milestone.
9. **Reaction validation:** dropped from v1 — Telegram remains the authority (no stale local allow-set).
10. No forcing-function date imposed autonomously; build P0–P5 + P7; R4 a separate milestone.

## Implementation plan

- **P0 — Manifest type + Telegram literal + interface method (no behavior change).** Add `c3types/caps.go`
  (`Capabilities` + `Inbound`/`Stream`, `Markup`, `MediaKind`, `MediaItem`, `PollSpec`, `Alteration`). Add
  no-arg `Capabilities()` to the interface; implement the static Telegram literal (`Typing=true`,
  `Albums=false`, `Stream.StreamViaEdit=false`). **Grep ALL `channel.Channel` implementers + test doubles
  (incl. `fakeChannel`, `attach_test.go:44`) and add the method in the SAME commit.** **Pre-step:** the
  suite has 2 pre-existing red attach-collision tests (hardcoded synthetic PID 9823,
  `attach_cwd_collision_test.go:79,230`) unrelated to this work — fix the fixture (use a live PID / a
  connected holder stub) first so phase gates are meaningful. **Every phase gate = `go build ./...` clean +
  `go vet` clean + NO NEW test failures vs the P0 baseline.**
- **P1 — Kill ParseMode leaks + neutral Outbound/EditArgs.** `Outbound.ParseMode`→`Markup`; add
  `EditArgs.Markup`; remove `EditArgs.ParseMode` read + the unadvertised arg; `Files`→`Media` (+`Poll`).
  One-release back-compat shims in `dispatchReply`: `parse_mode=""`→`markdown`; `"HTML"`→`native`;
  **`"MarkdownV2"`→reject with a clear note** (converter now handles markdown natively; MarkdownV2-from-
  agent is rare); `Files`→`Media{Kind:file}`. Assert raw-Telegram rejected unless `native`. **Note:
  `ReplyArgs = Outbound` (type alias, `types.go:77`), so this rename also changes the reply-tool arg shape,
  the `SendReply(args ReplyArgs)` signatures (real channel + `fakeChannel`), and must replace the
  `len(Files)>0` hard-error (`outbound.go:29-31`) atomically in the same commit.**
- **P2a — Converter expansion (R1).** Expand `mdToTelegramHTML` to the full common-markdown set
  (italic/underline/strike/spoiler/links/lists/quotes) with golden tests; lands green independently of the
  gate. Make the manifest `RichText` advertisement match exactly what it renders.
- **P2b — `capability.Gate` + chunking + multi-part contract.** Build `internal/capability` (pure; `Gate`
  returns parts+notes+alts; `GuidanceFor` per the template; table-tested, imports only `c3types`). Route
  BOTH `dispatchReply` and `dispatchEditMessage` through converter+gate. Implement the construct-aware
  chunking algorithm (golden tests incl. straddling constructs + a single >4096 construct, UTF-16
  counting). Implement the multi-part send contract in dispatch (sequential, fail-fast, "sent k of N") and
  **fix the existing silent-success-on-partial-failure bug** (`outbound.go:82-84`). Add the
  guidance-derives-from-caps golden test.
- **P3 — Media + poll send paths.** Add the `media` array arg to the reply tool InputSchema (both adapters)
  + parse in `dispatchReply`. Replace the `Files` hard-error with per-Kind single-item sends + sequential
  multi-item (no album grouping). File existence + `MaxSendBytes` validation in-channel. Add `sendPoll` +
  the gated `poll` tool (`sendPoll` in a new `sendpoll.go` — the existing `poll.go` is the unrelated
  getUpdates loop). Surface 50MB/20MB caps.
- **P4 — Caps delivery + shared agent surface.** Add `Capabilities` to `hello_ack` AND `attached`.
  `mode.Combined(caps)` folds in `GuidanceFor` (ripples to all 3 consumers + `protocol_test.go`'s tests).
  ONE shared helper generates tool Descriptions/InputSchemas (both adapters). The broker setup path
  (`codexAgentsMdBlock`, `cli_host.go:322`) has no live connection — it sources caps from the static
  channel `Capabilities()` literal. **No per-turn Codex injection in v1.** Add CONTENT assertions to the two adapter wire tests (today only `Instructions != ""`) + a
  per-channel golden manifest test.
- **P5 — Deterministic typing relay.** Per-connection has-replied flag on the holder/stub; ticker select-arm
  + per-route timer on `RouteWorker`; arm on delivered-to-claimed-route once has-replied, re-arm on
  non-reply tool-calls, disarm on first `reply`/idle, gated by `Capabilities.Typing`. Remove `send_typing`
  from the default tool set; **keep the dispatch `send_typing` case** (legacy tolerance + internal relay +
  `validate_topic` piggyback) + a test that `validate_topic` still works.
- **P6 — Streaming: NOT built.** Manifest false; guidance says unavailable. No relay, no new ops. Milestone.
- **P7 — Enforcement + cleanup.** CI grep-guard (telegram literals; no `gotgbot` in `internal/channel`;
  `internal/capability` imports only `c3types`+stdlib; no new TCP dial). Delete the back-compat shims after
  the window. Full test suite + live Telegram smoke checklist (rich-text marks incl. links/quotes/lists,
  rich edit, photo-vs-document, multi-media sequential, poll, deterministic typing turns 2..N).

## Residual / sanctioned leaks (documented)

1. `Markup=native` — restricted opaque pass-through (gate-flagged).
2. Gated Telegram-named interface methods — inert until a 2nd channel forces a rename.
3. `c3types.Inbound.Attachment.Kind` — open enum on Telegram terms (`types.go:39/41`); deferred.

## Items for Karthi (review after the build)

- **Streaming (R4):** which path, if any — reverse the Codex opt-out (Codex-only) or pivot C3 to an
  SDK/Messages-API host (both, large)? v1 ships typing only.
- Confirm Decisions 2 (Codex opt-out), 4 (`native`), 5 (inbound enum), 6 (tool presence), 8 (albums
  sequential), 9 (no reaction validation).
- Live Telegram round-trip from your phone is the one check I can't run myself.
