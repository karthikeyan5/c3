# C3 rich_message inbound decode — design

- **Date:** 2026-06-19
- **Status:** Approved (design), pending spec review
- **Author:** Claude (orchestrator) for Karthi
- **Topic area:** `internal/channel/telegram/` inbound path

## 1. Problem

Telegram Bot API 10.1 (June 11, 2026) introduced **rich messages**. When a user
or another bot sends one into a chat C3 is in, the body arrives in
`Message.rich_message` as a structured `RichBlock` tree — **not** as
`Message.text`. C3 currently reads only `msg.Text` (and media captions), so a
rich message surfaces to the agent as an **empty message**.

Karthi's goal, verbatim: *"The rich message inbound needs to work because
increasingly Telegram will start receiving rich messages and I want to make sure
we don't see empty messages when a rich message is sent."*

The hard success criterion is therefore: **a rich inbound message never reaches
the agent empty, and never crashes the poll loop.** Beyond that, we decode it
into faithful, readable markdown plus real attachments.

## 2. Scope

**In scope** (Karthi's fidelity decision: "combination of 1 & 3" — Faithful GFM
base plus the high-value Maximal extras):

- Decode the inbound `RichBlock` / `RichText` tree into **GFM markdown** placed
  in `Inbound.Text`.
- Faithful structural rendering: headings, bold/italic/strikethrough/spoiler/
  underline/code, links, lists (ordered/unordered/checkbox), blockquotes,
  fenced code, and **tables → GFM pipe tables**.
- **Media blocks → real downloadable `c3types.Attachment`s** (photo/video/audio/
  voice/animation), reusing the existing inbound-media pattern, with an inline
  text marker at the block's position.
- Text-level Maximal extras (low cost, "don't silently lose information"):
  LaTeX math preserved verbatim, custom-emoji → its `alternative_text`,
  footnotes/references/anchors → markdown links, `details`/collapsible expanded
  (summary + body), and an `is_rtl` marker when true.
- A **config kill-switch** (default ON) so the decoder can be disabled without a
  redeploy, plus a `DeliversRichMessages` inbound capability flag that mirrors it.
- Graceful degradation for unknown/future/exotic block types.

**Out of scope (future):**

- Outbound `sendRichMessageDraft` streaming (`RichBlockThinking`) — unrelated.
- Re-rendering inbound rich content back out as native rich (round-trip).
- Perfect representation of constructs GFM cannot express (e.g. table cell
  `colspan`/`rowspan`, RTL layout) — these degrade gracefully (see §6).
- `map`, `collage`, `slideshow` as anything richer than their textual / contained
  rendering described in §5.

## 3. Background facts (from live Bot API 10.1 docs + codebase recon)

These are the facts the design depends on. Wire knowledge stays inside the
`telegram` package (the existing **R7 no-leak rule**).

**Inbound shape:**

- `Message.rich_message` → `RichMessage { blocks: []RichBlock, is_rtl: bool }`.
  There is **no** `text`, **no** `entities`, **no** markdown/html field inbound —
  only the structured block tree. (Outbound is asymmetric: we *send*
  `rich_message.markdown` and Telegram parses it; inbound we get the parsed tree
  back and must re-serialize it ourselves.)
- `RichBlock` is a union discriminated by a `type` string. Concrete block types
  (21): `paragraph`, `heading` (`size` 1–6, 1 = largest), `pre` (`language`),
  `footer`, `divider`, `mathematical_expression` (LaTeX), `anchor`, `list`,
  `blockquote` (+ `credit`), `pullquote` (+ `credit`), `collage`, `slideshow`,
  `table`, `details` (+ `summary`, `is_open`), `map`, `animation`, `audio`,
  `photo`, `video`, `voice_note`, and `thinking`. **`thinking` is outbound-only —
  it is never received inbound** (docs: *"can't be received in messages"*).
- `RichBlockTable` carries `cells: [][]RichBlockTableCell` (row-major 2-D). A
  cell has `text: RichText` (optional; omitted ⇒ invisible cell), `is_header`,
  `colspan`, `rowspan`, `align` (`left`/`center`/`right`), `valign`. **There is no
  separate header row** — header-ness is per cell.
- `RichText` is a recursive union: a **bare JSON string**, an **array of
  RichText**, or a **tagged object** `{type, text, ...}`. Formatting **nests by
  wrapping** (e.g. bold-italic = `{"type":"bold","text":{"type":"italic",
  "text":"hi"}}`). **There are no offsets/lengths** anywhere (unlike
  `MessageEntity`). Inline types (~25): `bold`, `italic`, `underline`,
  `strikethrough`, `spoiler`, `code`, `marked`, `subscript`, `superscript`,
  `url` (+`url`), `text_mention` (+`user`), `custom_emoji` (+`alternative_text`),
  `mathematical_expression` (LaTeX), `date_time`, `mention`, `hashtag`,
  `cashtag`, `bot_command`, `email_address`, `phone_number`,
  `bank_card_number`, `anchor`, `anchor_link`, `reference`, `reference_link`.

**gotgbot constraint:**

- C3 pins `gotgbot/v2 v2.0.0-rc.34` (targets Bot API 10.0, *before* rich
  messages). `gotgbot.Message` has **no `RichMessage` field**, so gotgbot's JSON
  unmarshal **silently discards `rich_message`**. By the time we hold a
  `*gotgbot.Message`, the field is already gone. We cannot recover it from the
  typed struct — we must capture the raw JSON before/around gotgbot's parse.
- Existing outbound rich code already uses the raw `*gotgbot.Bot.
  RequestWithContext(ctx, method, params, opts)` bridge for the same reason
  (`sendrich.go`). We reuse that bridge for inbound capture.

## 4. Architecture

Three pieces, all inside `internal/channel/telegram/` (R7):

| Piece | File | Responsibility |
|---|---|---|
| **Raw capture** | `poll.go` (modify) | Get `rich_message` raw JSON past gotgbot's drop |
| **Decoder** | `richdecode.go` (new) + `richdecode_test.go` | `RichBlock`/`RichText` tree → GFM markdown + `[]Attachment` |
| **Integration** | `inbound.go` (modify) | Invoke decoder in `convertInbound`, populate `Text` + `Attachments` |
| **Config + caps** | `internal/mappings/{types,clone}.go`, `capabilities.go` | Kill-switch (default on) + capability flag |

Data flow:

```
getUpdates (raw JSON)
  → []gotgbot.Update      (existing typed path, unchanged downstream)
  + []updateProbe         (NEW: captures update_id + message.rich_message raw)
  → dispatchMessage(update, richRaw)
  → convertInbound(channel, msg, sttPrefix, richRaw)
  → decodeRichMessage(richRaw) → (markdown, []Attachment, ok)
  → Inbound{ Text: markdown, Attachments: media }
```

## 5. The decoder (`richdecode.go`)

Self-contained; all 10.1 inbound wire knowledge lives here. It defines minimal
Go structs for the union types, with:

- A `RichText` type with a **custom `UnmarshalJSON`** handling its three shapes
  (string · array · tagged object), producing a normalized in-memory node.
- A block probe that reads the `type` discriminator, then unmarshals into the
  concrete block struct.

Top-level entry:

```go
// decodeRichMessage parses a Bot API 10.1 rich_message payload into GFM markdown
// plus any embedded media as downloadable attachments. ok=false on malformed
// JSON or panic (caller falls back to a non-empty marker). Pure: no network.
func decodeRichMessage(raw json.RawMessage) (markdown string, atts []c3types.Attachment, ok bool)
```

Recursive helpers `renderBlock(b) (md string, atts []Attachment)` and
`renderRichText(rt) string`.

### 5.1 Block → markdown mapping

| Block `type` | Rendering |
|---|---|
| `paragraph` | text + blank line |
| `heading` (size 1–6) | `#`×size (size 1 → `#`, size 6 → `######`) + text |
| `pre` (`language`) | fenced code block ```` ```lang ```` … ```` ``` ```` |
| `blockquote` | `> ` prefix on each rendered child line; `credit` → `> — credit` |
| `pullquote` | same as blockquote + credit |
| `list` | items: ordered (`value`/`type` → `1.`/`a.`/`A.`/`i.`/`I.`), unordered → `-`; `has_checkbox` → `- [ ]`/`- [x]`; nested `blocks` indented |
| `table` | **GFM pipe table** — see §5.3 |
| `details` | `**summary**` then expanded body blocks (always expanded regardless of `is_open`) |
| `footer` | `---` then italic text |
| `divider` | `---` |
| `mathematical_expression` | `$$expr$$` (LaTeX preserved verbatim) |
| `anchor` | dropped (no readable text) |
| `photo`/`video`/`audio`/`voice_note`/`animation` | **`c3types.Attachment`** (file_id, size, MIME; best `PhotoSize` for photo) **+** inline marker `[photo: caption]` at position |
| `collage`/`slideshow` | render contained `blocks` in order |
| `map` | `[map: lat,lng]` text marker |
| `thinking` | skipped (never inbound) |
| unknown / future | §6 graceful degradation |

### 5.2 Inline RichText → markdown

| Inline `type` | Rendering |
|---|---|
| bare string | escaped text (§5.4) |
| array | concatenation of children |
| `bold` | `**…**` |
| `italic` | `*…*` |
| `underline` | `__…__` |
| `strikethrough` | `~~…~~` |
| `spoiler` | `\|\|…\|\|` |
| `code` | `` `…` `` |
| `marked` | `==…==` |
| `subscript`/`superscript` | passthrough text (GFM has no syntax) |
| `url` | `[text](url)` |
| `text_mention` | `[name](tg://user?id=<id>)` |
| `custom_emoji` | `alternative_text` |
| `mathematical_expression` | `$expr$` |
| `date_time` | the human `text` |
| `mention`/`hashtag`/`cashtag`/`bot_command`/`email_address`/`phone_number`/`bank_card_number` | literal `text` (auto-detected entities — render plainly) |
| `anchor`/`anchor_link`/`reference`/`reference_link` | `[text](#name)` footnote-style link |

### 5.3 Tables

- Walk `cells[row][col]`. The header row is the **first row whose cells are
  `is_header`** (commonly row 0); render it as the GFM header, then the
  delimiter row using per-column `align` (`left`→`:--`, `center`→`:-:`,
  `right`→`--:`), then the remaining rows.
- If **no** cell is a header, synthesize an empty header row (GFM requires one)
  so the table still renders.
- `text`-omitted cells → empty cell.
- `colspan`/`rowspan` **cannot be expressed in GFM**: render the cell's content
  in its primary position and leave spanned positions blank (documented lossy
  degradation, not a failure).
- `caption` → a line rendered above the table.

### 5.4 Escaping

Plain-string text nodes get **light** markdown escaping — enough that literal
content (`*`, `_`, `` ` ``, `|`, `[`) is not misread as structure, but not so
aggressive the text becomes unreadable for the agent. This is agent-facing input
text, so readability is weighted over perfect round-trip fidelity. The exact
escape set is an implementation detail fixed in the plan; the principle is
documented here as a deliberate trade-off.

## 6. Graceful degradation — hard invariants

These directly serve "never see empty messages":

1. **Unknown / future block `type`** → if it has a `text` field, render that; if
   it has child `blocks`, render those; otherwise emit `[unsupported block:
   <type>]`. Never silently empty.
2. **Whole decode yields no text and no attachments** but a `rich_message` was
   present → emit the marker `[rich message]` so `Text` is never empty.
3. **Never panics.** A top-level `recover()` in `decodeRichMessage` turns any
   panic into `ok=false`; the caller then uses the `[rich message]` marker. A
   rich message may degrade in fidelity, but it can never crash the poll loop or
   vanish.
4. **Malformed/partial JSON** → `ok=false` → marker fallback.

## 7. Raw capture (`poll.go`)

The one change to the critical inbound loop:

- Replace the typed `c.bot.GetUpdates(...)` call with a raw
  `c.bot.RequestWithContext(ctx, "getUpdates", params, opts)` returning
  `json.RawMessage`.
- Unmarshal that result **twice**:
  - into `[]gotgbot.Update` — the existing path, fully unchanged downstream;
  - into `[]updateProbe`, where `updateProbe` captures only `update_id` and
    `message.rich_message` / `edited_message.rich_message` as `json.RawMessage`.
- Pair the two by **array index** (same array, same order) — simplest and safe.
  Thread the (possibly `nil`) `rich_message` raw into `dispatchMessage`.

**Known gotchas to handle (called out so the implementation can't miss them):**

- **Long-poll timeout:** gotgbot's `GetUpdatesOpts` sets the per-request HTTP
  timeout to exceed the long-poll `timeout` (25s). The raw call must replicate
  this — the request opts' timeout must be **greater than** the 25s polling
  timeout, or every poll will time out. Mirror what gotgbot's `GetUpdates`
  configures.
- **Param parity:** replicate `offset`, `limit`, `timeout`, and
  `allowed_updates` exactly as the current `GetUpdatesOpts` provides them, so
  offset/ack semantics and subscribed update types are unchanged.
- **Rate limiter:** `getUpdates` must not be throttled by the outbound rate
  limiter; mirror gotgbot's behavior (it does not rate-limit getUpdates).
- The double-unmarshal pairing logic is extracted into a **pure helper** so it
  can be unit-tested without network (§9).

## 8. Config kill-switch + capability flag

**Config** (mirrors the `notifications.invasive` `*bool`-nil-means-true pattern,
including the established traps):

- Add an `Inbound *InboundConfig` field to `MappingsFile` with
  `RichMessages *bool` (`json:"rich_messages,omitempty"`). **nil / absent ⇒
  true** (decoder enabled). A plain `bool` would zero-value to `false` and
  silently disable the feature for everyone who never set it — exactly the trap
  documented for `Invasive`.
- Accessor `(*MappingsFile).RichInboundEnabled() bool` — returns `true` when
  `mf == nil || mf.Inbound == nil || mf.Inbound.RichMessages == nil`.
- **`Clone()` must deep-copy `Inbound`** (new struct + new `*bool`). `Clone()` is
  on the broker's copy-on-write mutation path and **silently drops any field not
  explicitly copied** — this is a known footgun; the plan includes a Clone test
  asserting the field survives a round-trip.

> **Interpretation note for review:** Karthi said *"add the flag default to true
> if not present."* "If not present" maps exactly to the nil-`*bool` default-true
> config pattern, so this is implemented as a **runtime kill-switch** (absent ⇒
> on), not just a static manifest flag — giving a no-redeploy way to disable the
> decoder if it ever misbehaves. If only a static capability flag was intended,
> this can be simplified at review.

**Wiring of the toggle:** the decoder is invoked only when `RichInboundEnabled()`
is true. `convertInbound` is a pure function today; the toggle is read where
config is available (the dispatch layer / `Channel`, which owns config) and the
result threaded to the call site. When disabled, rich messages surface exactly as
today (empty `Text`, i.e. pre-feature behavior). Exact plumbing is finalized in
the plan; the **behavior contract** is what's fixed here.

**Decision (2026-06-19):** the kill-switch gates *decoding only*; the raw
`getUpdates` capture path (§7) is **always active**. A fuller fallback — flag-off
reverting the poll loop to gotgbot's typed `GetUpdates` path — was considered and
**declined** as over-engineering ("keep it sane"). Critical-path reliability
rests on (a) exact request-opts parity with gotgbot's `GetUpdates`, (b) the
`recover()`-guarded decoder that lives off the critical path and cannot panic the
loop, and (c) a live-poll verification after deploy (§12, R-1).

**Capability flag:** add `DeliversRichMessages bool` to `c3types.InboundCaps`,
set in `Capabilities()` to the same value the toggle would yield (true by
default). This advertises inbound rich support symmetrically with the existing
outbound `RichMessages` cap.

## 9. Integration (`inbound.go`)

- `convertInbound` gains a `richRaw json.RawMessage` parameter (existing tests
  updated to pass `nil`).
- **Rich message is checked first** — a rich message *is* the message, so the
  rich branch runs before the existing media-type switch. If `richRaw` is present
  and the toggle is on: call `decodeRichMessage`; set `in.Text` to the markdown
  and append the media attachments; return. If decode `ok=false` or empty, set
  the `[rich message]` marker (invariant §6).
- Media attachments produced by the decoder reuse the **existing inbound
  download path**, so the existing `maxDownloadBytes` cap applies automatically —
  no new size handling.

## 10. Security & R7

- All Bot API 10.1 inbound wire knowledge (type names, field shapes, limits)
  lives only in `richdecode.go` — no raw rich constant leaks to core (R7).
- Decoded text is **untrusted user content**, identical to any other inbound
  text: it is data handed to the agent, never interpreted as C3 control. No
  broker-protocol tokens or routing markers can be injected via decoded content
  because the broker treats `Inbound.Text` as opaque payload.
- Media `file_id`s flow through the existing, already-capped download path.
- No new secrets, endpoints, or config values that touch the public-repo
  keep-out list.

## 11. Testing strategy

- **Unit (decoder):** each inline type; each block type; tables (alignment,
  header detection, no-header synthesis, omitted cells, colspan/rowspan
  degradation); nested formatting (bold-in-italic); lists
  (ordered/unordered/checkbox/nested); media → attachment mapping.
- **Golden:** full `RichMessage` JSON fixtures → expected markdown (realistic
  payloads incl. a table + media + nested inline).
- **Invariant:** unknown block type → non-empty; malformed JSON → `ok=false`;
  empty tree → `[rich message]`; a deliberately panicking input → recovered,
  `ok=false`.
- **Integration:** `convertInbound` with a rich raw → `Text` + `Attachments`
  populated; with toggle off → pre-feature (empty) behavior.
- **Config:** `RichInboundEnabled()` nil-safety; **`Clone()` round-trip
  preserves `Inbound.RichMessages`** (the footgun guard).
- **poll.go:** the double-unmarshal pairing pure helper tested without network
  (raw array → aligned `[]Update` + `[]updateProbe`).

## 12. Risks

- **R-1 (highest): long-poll timeout regression.** Getting the raw `getUpdates`
  request timeout wrong breaks the entire inbound loop. Mitigation: mirror
  gotgbot's `GetUpdatesOpts` timeout exactly; verify a live poll after deploy.
- **R-2: docs sourced via a 2026-06-18 scrape**, not a direct `core.telegram.org`
  fetch (egress to that host is blocked from the build environment). Two
  independent live sources (the scrape + the verbatim changelog HTML) agree on
  the full type list and field shapes. If desired, re-pull
  `core.telegram.org/bots/api` from an open-egress machine and diff §3 before
  implementation. The graceful-degradation invariants (§6) make any residual
  field-shape surprise non-fatal.
- **R-3: GFM cannot express colspan/rowspan/RTL.** Accepted lossy degradation,
  documented; not a correctness failure.

## 13. Out of scope / future

- Inbound → outbound rich round-trip (re-emitting received rich as native rich).
- Streaming drafts (`sendRichMessageDraft`, `RichBlockThinking`).
- Richer rendering of `map`/`collage`/`slideshow` beyond §5.
