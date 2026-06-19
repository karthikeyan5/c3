# Formatting Policy — make agents format liberally (design)

**Date:** 2026-06-20
**Branch:** `feat/formatting-policy`
**Status:** approved (policy decided by Karthi 2026-06-19) — this captures the exact wording

## Goal

One sentence: make c3-driven agents **format replies for readability** instead of
defaulting to flat plain text.

Karthi (2026-06-19): *"anything that makes a wall of text easier to read should be
done; no reason to keep it plain."*

The converter bug that silently stripped formatting (mixed `**`/`__` → illegal
same-type nested tags → 400 → plaintext) was fixed 2026-06-19. This is the
**agent-guidance half**: the surfaces that tell the agent *how* to compose were
permissive/passive ("you CAN write markdown") and buried, so agents kept writing
walls of text. This change makes them prescriptive ("you SHOULD format when it
aids readability") and raises their salience.

## The reconciliation (the crux)

There is an apparent tension with the long-standing plain-prose preference
(`feedback_telegram_mode`: "in telegram mode replies look like SMS — no bold,
headers, lists, code blocks unless necessary"). These are **not** contradictory;
the dividing line is the **nature of the content**, not a blanket rule:

- **Conversational / short answers → plain prose.** Don't bullet-point a one-line
  reply, don't header a two-sentence answer. SMS register stays.
- **Structured / dense content → format it.** Steps, enumerations, comparisons,
  code, commands, paths, quoted material, multiple distinct points — render with
  the matching markdown (lists, tables, code blocks, block quotes) so the reader
  isn't parsing a wall of text.

The anti-pattern this kills: a reply that is *really* a list-of-things or a set of
steps, left as a run-on paragraph because "telegram mode = plain." Format is a
**tool for readability, not decoration** — use it in service of the reader.

## The five surfaces (exact final wording)

### 1. `internal/capability/guidance.go` — RichText line: permissive → prescriptive

The capability guidance is delivered to **both** Claude and Codex (via
`mode.Combined()` → MCP `instructions` / Codex `AGENTS.md`). Reframe the rich-text
block so it prescribes formatting-for-readability and carries one inline worked
example, while preserving the existing convert/escape + split-message lines.

Keeps the substrings `Rich text: YES` and `Write standard markdown` contiguous
(existing golden assertions). New text (verbatim in the plan):

> - Rich text: YES — and you SHOULD use it whenever structure makes a reply easier
>   to read. Write standard markdown: bold/italic for emphasis, lists for steps or
>   enumerations, tables for comparisons, inline code and fenced blocks for
>   code/commands/paths, block quotes for quoted text, plus links, strikethrough,
>   spoilers. Keep a one-line answer plain, but never leave structured content
>   (steps, comparisons, multiple points, code) as an unbroken wall of text — e.g.
>   render three findings as a numbered list, not one run-on sentence. C3 converts +
>   escapes for you — do NOT hand-write HTML or channel tags.

(The trailing "do NOT hand-write HTML or channel tags." sentence moves to the end
of the rich-text paragraph so the split-message lines that follow no longer need
the awkward "channel tags." prefix.)

### 2. `reply` tool Description — formatting nudge (BOTH adapters)

The `reply` tool Description is the **compose-time** surface (the agent reads it at
the moment it forms the reply) and is currently silent on formatting. Identical
string in `cmd/c3-claude-adapter/main.go` and `cmd/c3-codex-adapter/main.go`. Add
one sentence after the first:

> Send a Telegram reply to the currently-attached topic. The `text` is markdown —
> use formatting (lists, tables, code blocks, bold, block quotes) whenever it makes
> the reply easier to read; keep one-line answers plain. Attach media via the
> `media` array: …(unchanged)…

### 3. `internal/mode/protocol.go` — `Combined()` reorder

Today: `ModeProtocol → MultipartProtocol → GuidanceFor(c)` — so CHANNEL
CAPABILITIES (which carries the formatting guidance) is **dead-last**, after the
niche voice-dictation convention. Move it ahead of MultipartProtocol:

`"\n\n" + ModeProtocol + "\n\n" + GuidanceFor(c) + "\n\n" + MultipartProtocol`

- `ModeProtocol` stays **first** — it is the safety-critical no-auto-reply /
  no-auto-switch contract (`feedback_no_auto_switch_output_mode`); it must not be
  demoted.
- Capabilities/formatting guidance moves to the **middle** (right after the mode
  contract, where it is read while the rules are fresh), no longer the forgotten
  tail.
- `MultipartProtocol` (a narrow voice-burst convention) moves **last**.

Leading `"\n\n"` and ModeProtocol-first byte-shape are preserved, so existing wire
tests still pass. Propagates to all three consumers automatically (both adapters +
the Codex AGENTS.md installer all call `Combined()`).

### 4. Worked example

A compact, concrete before/after so the agent has a pattern, not just a rule:

- **Agent-facing (cross-CLI):** the inline `e.g. render three findings as a
  numbered list, not one run-on sentence` folded into surface #1 (reaches Claude +
  Codex via `Combined()`; token-cost-conscious for a per-session string).
- **Behavioral memory (fuller literal before/after):** in `feedback_telegram_mode`
  (surface #5), where there is no per-message token cost.

### 5. Plain-prose memory carve-out (`feedback_telegram_mode.md`)

Update the memory so it no longer reads as a blanket "always plain." Add the
structural-content carve-out: conversational prose stays SMS-like, but structured
content (code, tables, lists, quotes, links, multi-point/step answers) **should**
be formatted for readability. Include the literal worked before/after. Link the
policy date and the converter-fix context.

## Testing

Additive — no existing test changes required (all assertions are `strings.Contains`;
the reframe keeps `Rich text: YES` + `Write standard markdown`; the reorder keeps
ModeProtocol first + leading `\n\n`). New assertions lock in the change:

- `guidance_test.go`: assert the prescriptive phrasing (`you SHOULD use it`,
  `unbroken wall of text`) appears on a RichText=true manifest.
- `protocol_test.go`: assert the new order — `index(GuidanceFor output)` <
  `index(MultipartProtocol)` in `Combined()` — so capabilities is no longer last.
- adapter wire tests: assert the `reply` Description carries the formatting nudge
  (e.g. `whenever it makes the reply easier to read`).

## Out of scope / non-goals

- No converter changes (the same-type-nesting bug is already fixed + shipped).
- No change to the mode protocol's reply-suppression rules or auto-switch ban.
- No new capability flags; this is wording + ordering only.
- Codex `reply` Description is edited for parity, but Codex's separate inbox/forward
  path is untouched.

## Security / keep-out

Pure guidance text. No keep-out values (proxy subdomains, GCP project, IP, region)
appear in any edited surface. Run `~/arogara/pii-audit/scan.sh` before any push.
