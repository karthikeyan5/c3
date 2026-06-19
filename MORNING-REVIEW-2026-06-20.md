# Morning Review — 2026-06-20 (C3 queue run)

**TL;DR:** All three queued items from the 2026-06-19 handoff are **done and shipped to `origin/master`** (`f8641d2`). Everything triple/dual-reviewed clean, PII clean. Nothing is blocked on me. A few autonomous calls and one hardening idea are listed below for your eyes.

This was an autonomous run off your green-light ("you can do the queued stuff now, after compaction"). I ran the autonomous gates: spec → commit → review → PII → push, with this doc as the morning-discussion list.

---

## What shipped

### 1. Formatting policy — agents format liberally  (commits `2e2f854`, `f730e57`, merge `68a824f`)
The agent-guidance half of the rich-text work (the converter bug was already fixed). Five agent-facing surfaces now push agents to format **for readability** instead of defaulting to flat text:
1. `internal/capability/guidance.go` — RichText guidance reframed permissive → **prescriptive** ("you SHOULD use it whenever structure makes a reply easier to read"), with an inline worked example.
2. `reply` tool Description in **both** adapters — a compose-time formatting nudge (byte-identical strings).
3. `internal/mode/protocol.go` `Combined()` — reordered **Mode → CHANNEL CAPABILITIES → Multipart** so the formatting guidance isn't dead-last. **ModeProtocol stays first** (it's the safety-critical no-auto-reply / no-auto-switch contract — I deliberately did not demote it).
4. Worked example (inline in the guidance + a fuller literal one in agent memory).
5. The plain-prose memory (`feedback_telegram_mode`) rewritten from blanket-plain → **content-based register**: conversational replies stay plain SMS prose, structured content (steps / comparisons / code / tables / lists / quotes / links) gets formatted.

**The reconciliation** (the crux): your "format liberally" and the old "telegram = plain SMS" are not in conflict — the dividing line is the *nature of the content*, not a blanket rule. Spec: `docs/superpowers/specs/2026-06-20-formatting-policy-design.md`.

Triple-reviewed clean (code-correctness + policy/consistency + security/keep-out). The policy reviewer caught that the *live* memory still said "no lists" — I fixed that as surface #5, so the agent's combined instruction set is now contradiction-free.

### 2. Rich-inbound deferred nits  (commit `2eaebee`, merge `eb29e95`)
The non-blocking follow-ups from the rich-message-inbound broad review:
- **Depth guard** (`maxDecodeDepth=256`) threaded through the renderers via thin public wrappers — past the cap a `[nesting too deep]` marker is emitted. Public signatures unchanged, so all existing rich tests pass byte-identical.
- **4 test nits**: full `escapeInline` 8-char set, ragged-row table, deep-block + deep-inline no-panic + depth-marker, and `DeliversRichMessages` in the golden manifest.

Dual-reviewed clean (correctness READY-TO-MERGE + adversarial SAFE). **Empirically settled** the recursion-safety question I'd flagged: I probed `decodeRichMessage` to **1,000,000-deep / ~22MB** — no crash. Go's `encoding/json` scanner bounds total nesting (~5000 arrays/blocks, ~10000 inline) and errors gracefully → `ok=false`. So json's limit is the real first line of defense; the 256 guard is genuine defense-in-depth for the depth window json permits; `recover()` is a never-reached backstop.

### 3. FIX #1 — back-to-back-TEXT half  (no code; investigation closed)
Confirmed **merge-perception, not a genuine drop**. Two text messages in the debounce window both enqueue (same producer path proven for the album half) and `mergeBatch` joins every non-empty text with `\n` — both reach the agent in one merged block. Already locked by `TestMergeBatch_ConcatenatesText`. FIX #1 is now fully resolved in the ROADMAP.

---

## Autonomous calls I made (sanity-check me)

1. **I shipped all three to public `master`** rather than holding for your ratify. Basis: you approved the queue, all items triple/dual-reviewed clean, PII clean, and the autonomous-gate protocol expects spec→commit→push. If you'd rather I hold guidance/policy changes for ratify next time, say so and I'll switch back to the held-branch pattern.
2. **The exact wording** of the formatting guidance and the `feedback_telegram_mode` rewrite is your policy voice — easy to wordsmith if any phrasing isn't how you'd put it. The load-bearing strings are test-pinned, so changes are safe.
3. **`maxDecodeDepth = 256`** is a generous, arbitrary-but-safe cap (real Telegram trees are shallow; json bails ~5000+ anyway). Trivial to change.
4. **I did NOT restart the live broker.** Item #1 is per-session (adapter MCP-initialize) and needs no broker restart. Item #2's decoder *does* run in the broker, but it's a **no-op on real traffic** (the running broker already has json's nesting bound — the actual defense), so I judged a 3am restart over a flaky proxy not worth the disruption. The new decoder goes live on the next natural broker restart. **All shipped changes take effect on a fresh Claude Code session anyway** (adapters are per-session) — so just starting a new session picks up item #1 and #2-via-rebuilt-binaries.

## Open for your call

- **getUpdates body-size cap (hardening).** The adversarial review noted there's no `io.LimitReader` on the getUpdates **response body**. It's read inside gotgbot's `RequestWithContext` (not our code), so it's a transport-trust thing — Minor today (TLS + our trusted reverse proxy), but it'd become Important if that proxy is ever treated as semi-trusted. Adding a cap means either bypassing `RequestWithContext` (loses the byte-parity we verified in the rich-inbound work) or wrapping at another layer — a real trade-off, not a bolt-on. Logged in the ROADMAP under the rich-inbound entry.

## Deploy state
- `origin/master` = `f8641d2`. Working tree clean (only the pre-existing untracked `docs/DEPLOY-telegram-proxy.md`, left untracked as always).
- Binaries rebuilt (`go install ./cmd/...`, 03:10). Adapters effective next session; broker unchanged on purpose (see call #4).
- PII audit clean (gitleaks working-tree + full history; no keep-out values in any diff).
