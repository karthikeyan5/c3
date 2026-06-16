# C3 — Product Roadmap

**Status as of 2026-06-15.** The channel rich-content + capability architecture (the P0 below) is **BUILT and committed** on `master` — 8 phases (P0–P7), designed via a 10-agent workflow, hardened by 3 critique passes, and triple-reviewed. Pending a live Telegram smoke test (the one check that needs a phone — checklist in `docs/specs/2026-06-14-channel-capability-architecture.md`). Version line: v0.1.0 (pre-public-push).

This is the **single consolidated roadmap** for C3. It was reconciled on 2026-06-14 from every source — `TODO.md`, `RESUME.md`, `MORNING-REVIEW-2026-05-19.md`, `DECISIONS.md`, `DEBUGGING.md`, `docs/plans/` + `docs/specs/`, the live Go codebase, and a mining sweep of every past C3 session transcript (incl. cross-project sessions). **Nothing was dropped:** ideas that previously lived only in voice notes are now captured here and flagged `risk-of-loss: was-untracked`.

C3 is a Go end-to-end Telegram multiplexer for multiple Claude Code / Codex CLI sessions: one broker daemon, per-CLI MCP adapters, topic-based routing. **Code health:** `go build ./...` and `go vet ./...` pass clean; all packages test green **except** 2 environment-flaky broker tests (a test-fixture defect, not a production bug — see P2). The big open items below are **features that were never built**, not regressions.

Legend — **status**: `planned` · `in-progress` · `idea` (not yet committed to) · `done`. **priority**: P0 (Karthi's stated #1) → P4 (nice-to-have).

---

## P0 — Channel rich-content + capability architecture — ✅ BUILT (2026-06-15)

Architecture: **Capability Manifest + Gate (CMG)** — each channel returns a flat
capability manifest; one pure broker-side `Gate` validates + degrades every outbound; the
agent receives capability + formatting guidance (Claude **and** Codex); no Telegram code
leaks into core (enforced by a CI grep-guard, `internal/archguard`). Spec:
[`docs/specs/2026-06-14-channel-capability-architecture.md`](docs/specs/2026-06-14-channel-capability-architecture.md).
**Remaining:** a live Telegram round-trip smoke test (needs Karthi's phone — checklist in the spec).

- **Rich-text formatting for Telegram** — `done` — the agent writes standard markdown;
  C3 converts to Telegram HTML and escapes (bold/italic/strike/spoiler/links/lists/quotes/
  inline+fenced code). The agent never hand-writes channel tags.
- **Full media / file / poll support** — `done` — `kind=photo` (compressed preview) vs
  `kind=file` (byte-for-byte original), video/audio/voice/animation, polls; in-channel
  size/existence validation; Bot API limits (4096 text, 1024 caption, 50MB send, 20MB
  download). **Albums descoped to sequential single sends in v1** (full grouping later).
- **Channel-capability declaration system** — `done` — flat `Capabilities` manifest per
  channel, delivered over `hello_ack`/`attached`; a single `GuidanceFor` feeds both the
  degrade gate and the agent surface (can't drift); Telegram specifics confined to the
  telegram package. Works identically for Claude + Codex.
- **Deterministic typing indicator** — `done` — broker-relayed programmatically (not an
  LLM tool); shown turns 2..N of a Telegram-mode session. (Absorbed FIX #2's auto-ticker.)
- **Deterministic streaming of reasoning/thinking** — `deferred` — **build right AFTER terminal-control** (Karthi 2026-06-15); the path (Codex-opt-out vs SDK-host) is still TBD.
  Verified: Claude Code exposes no in-flight reasoning to an MCP adapter (hooks/MCP see no
  reasoning frames; only the raw Messages API / Agent SDK do). Options: (a) reverse the
  Codex forwarder opt-out → Codex-only streaming (asymmetric); (b) pivot C3 to host the
  agent via the SDK/Messages-API → both CLIs (large). Manifest reports `StreamViaEdit=false`.

## P1 — Remote terminal-control (the main build feature — sequenced *after* the channel architecture above)

- **Remote terminal-control of coding agents from Telegram** — `in-progress`
  Bring up a terminal connected to a DM and spawn/control other coding agents (TUI or not). Needs a dedicated PTY subsystem (can't ride the MCP channel surface). Karthi's reference: `github.com/helvesec/rmux`. Mid-design.
  _Source: RESUME.md §THE MAIN FEATURE (2026-06-01)._
- **Terminal-control design — DECIDED (Karthi 2026-06-15):**
  - Q1 → **C3 stays the brain** — it drives the terminal engine and reuses the per-route-worker model.
  - Q2 → **all-Go PTY stack** — `creack/pty` + `Netflix/go-expect` + a VT emulator as a new broker worker type; single-language, no Rust dependency.
  - Q3 → **arbitrary TUIs** — the raw PTY path; control anything in a terminal, not just MCP-aware agents.
  - Next concrete step: prototype the Go PTY worker (snapshot-on-idle rendering + send-keys), per the 2026-06-01 handoff in RESUME.md.
  _Source: RESUME.md §Q1/Q2/Q3; decided 2026-06-15._

---

## P1 — Near-term (push-blockers & parked fixes)

- **Permission relay** — `planned`
  Forward Claude Code permission prompts to Telegram for remote approve/deny. The one supported remote-approval path (the channel's 3rd surface). Build prompt exists; relay returns GO/DENY as a **string, not bool**; does NOT catch auto-mode classifier hard-denies (that's the trusted-operator item below).
  _Source: RESUME.md §Sub-feature permission relay (assessed GO)._
- **Trusted-operator DM authorization (PreToolUse hook) — ratify + build** — `planned`
  Let an authenticated owner-DM authorize classifier-blocked actions via a PreToolUse hook (an out-of-band per-action approval model, one layer up). **Spec is written** at `docs/specs/2026-06-14-trusted-operator-dm-authorization.md`. Blocked on: §9 Phase-0 hard gate (empirically verify a hook "allow" actually bypasses the auto-mode classifier) + §10 decisions awaiting Karthi's ratification.
  _Source: docs/specs/2026-06-14-…; MEMORY c3_trusted_operator_authz_spec.md._
- **FIX #1 (parked): inbound delivery-drop + album/media-group drop** — `in-progress`
  Back-to-back messages 186/187 logged ~33µs apart but only one reached the agent; two files sent as an album → only one arrived. C3 has **no media-group assembly** (relies on the 1.5s debounce). NEXT: read `internal/channel/telegram/poll.go` dispatch path for where a same-poll-batch update is dropped before enqueue. Needs from Karthi: rough send-time of the two-files album.
  _Source: RESUME.md §FIX #1 (parked)._
- **FIX #2 (parked): typing indicator never shows while the agent works** — `in-progress`
  Typing is **manual-only** (fires only on the `send_typing` MCP call); the 2026-05-08 rearch specced an auto-ticker that was never built. FIX: per-route typing ticker in the route worker. **Now absorbed into the P0 "deterministic typing indicator" item above** (programmatic relay, not LLM-driven) — keep this entry for the repro/history.
  _Source: RESUME.md §FIX #2 (parked)._
- **C3 name is FINAL — no rename** (Karthi 2026-06-14). The earlier rename plan is
  dropped. C3 = "Claude Code Claw"; an origin note lives in the README. Previously listed
  as a public-push blocker — no longer.
- **First-run install validation on a fresh machine** — `in-progress` (public-push blocker)
  Paste the install one-liner into a fresh Claude Code session, walk `INSTALL.md`, cd into a project, attach, confirm a real Telegram round-trip. Surfaces rough edges before the public GitHub push.
  _Source: TODO.md §In flight (user-driven)._

---

## P2

- **`c3-broker release <cwd>` runtime IPC op** — `in-progress` (stubbed)
  The **only genuinely unimplemented user-facing command**: `cmd/c3-broker/status.go:153` returns "not yet implemented". Frees an attached topic without restarting the broker. Needs a new release-by-cwd IPC op; workaround today is `/exit` the holding session.
  _Source: code; TODO.md §Broker follow-ups._
- **Eliminate `--dangerously-load-development-channels` / register a private trusted plugin store** — `idea` · _risk-of-loss: was-untracked_
  Karthi: ready to sign a certificate / do whatever to drop the dangerous flag; wants to officially register his own trusted plugin store since he'll maintain many private plugins. Status unverified against current Claude Code capabilities.
  _Source: session 274227fa (2026-05-18); not in TODO.md or docs._
- **Programmatic (non-chat) channel extension** — `idea` · _risk-of-loss: was-untracked_
  Make C3 a pluggable platform beyond Telegram: a programmatic channel extension so deterministic code can inject context into an LLM via C3 and get a **fixed-format response** back (a programmatic channel, not chat).
  _Source: session d1d95247 (2026-06-04); not tracked anywhere prior._
- **STT multi-provider modularity + retry/fallback + "how to add a provider" README** — `in-progress` · _risk-of-loss: was-untracked_
  The chain exists (elevenlabs-scribe-v2 [opt-in], gemini-3-flash-openrouter, sarvam-saaras-v3) with fallback, but the explicit how-to-add-a-provider README Karthi asked for is unverified / likely missing.
  _Source: session abdfd714 (2026-05-15)._
- **Codex ↔ Claude install/setup parity** — `planned` · _risk-of-loss: was-untracked (specifics)_
  Codex MCP install hiccups; Codex didn't prompt for STT keys; Codex unaware of the CLI/Telegram output-mode protocol; Codex adapter lacks a `detach` tool (uses inbox/forwarder). Confirm which asymmetries are intentional vs gaps.
  _Source: session 274227fa (2026-05-18); code.gaps._
- **Auto-attach-to-c3-by-default bug** — `in-progress` · _risk-of-loss: was-untracked, fix unverified_
  Sessions always default-attach to the c3 topic even when not working on c3. A session summary marks it FIXED, but there's no C3 repo commit since 2026-06-04 — the fix may be config/mappings-only and is unverified in-repo. **Re-verify.**
  _Source: session 8c155174 (2026-06-13)._
- **Phase 3 — Per-user access control enforcement** — `in-progress`
  Who can talk to which CLI. Pairing/allowlist primitives exist; full per-user→per-CLI enforcement is partial.
  _Source: TODO.md Phase 3; spec §4.3._
- **Phase 3 — Master Telegram user / admin-from-Telegram** — `planned`
  An admin who can configure the system from Telegram itself. Pairing + per-user allowlist landed 2026-05-18; master-user enforcement remains.
  _Source: TODO.md Phase 3._
- **MCP-resume lifecycle hardening (heartbeat + singleton-PID guard)** — `planned`
  Deeper Claude Code MCP lifecycle on resume is poorly understood; surface a heartbeat + singleton-PID guard if symptoms recur. Karthi: "want this UX really smooth, no breakages."
  _Source: TODO.md follow-up; session abdfd714 (2026-05-14)._
- **Fix the 2 flaky broker tests (hardcoded synthetic PID 9823)** — `planned`
  `TestAttach_CwdDefault_HeldByDifferentLiveSession_WarnsCollision` + `TestAttach_ExplicitName_HeldTopic_StillForceSteal` require `syscall.Kill(9823,0)` to report alive; the test registers an UNCONNECTED holder so `IsAlive()` falls through to a dead PID. **Test-isolation defect, NOT production breakage** — fix the fixture (e.g. use a live PID or a connected stub).
  _Source: code.gaps; internal/broker/attach_cwd_collision_test.go._

---

## P3 — Phase 4 (advanced) & smaller backlog

Phase 4 advanced features (all `planned`, not started) — _Source: TODO.md §Phase 4:_
- Inter-CLI messaging (CLI-1 → CLI-2 via broker).
- Topic creation via API (beyond the interactive attach proposal).
- Monitoring dashboard (adapters, message counts, STT health, broker resilience). _Several "c3 down/broken" incidents argue for real value here._
- Persistent message history (context recovery across restarts).
- Slash commands handled in the broker (`/status`, `/list`, …).
- Stream thinking / tool calls to Telegram (research best UX).
- Web chat channel (second `Channel` impl; the abstraction is multi-channel-ready).
- Voice mode channel (continuous, hands-free/driving).
- Live CLI view (web live-view; overlaps with terminal-control's snapshot capability).

Smaller backlog:
- **Cross-CLI duplication audit follow-ups b1/b2** — `planned` (await Karthi review): b1 tool-description divergence (Codex 2 extra tools + paraphrased attach desc); b2 broker reconnection error strings — design call on a shared error helper. _Source: docs/research/2026-05-18-cross-cli-duplication-audit.md._
- **Tighter concurrent-inbound interleaving test for per-route dispatch** — `planned`: sequentialization already works (per-route worker pool, one goroutine per RouteKey); a tighter interleaving test is still worth writing. No new prod code.
- **TODO #19(e) — CWD-fallback session matching** — `planned`: `/c3:sessions` is PID-primary; `ListSessionsReq` carries a CWD field framed as a "match by cwd when PID walk fails" hook — verify whether the fallback path is wired or just a placeholder field (`internal/proctree/proctree.go`).
- **Codex policy 3-state error messaging** — `in-progress`: plan `docs/plans/2026-05-19-codex-policy-3state.md` is complete and `AttachStatus` enum + `PolicyRejected` hint landed; confirm the Codex side is fully wired.
- **5 code-review guideline-file edits** — `planned` · _risk-of-loss: only-in-MORNING-REVIEW_: Karthi's rubric files awaiting his voice on each (subjective rubric changes, not code). _Source: MORNING-REVIEW-2026-05-19.md._
- **n3 — Unicode bullets in user output** — `planned` (P4) · _risk-of-loss: only-in-MORNING-REVIEW_: keep Unicode bullets in user-facing output? Karthi decides; subjective.
- **STT gemini-3-flash-openrouter provider dead** — `planned` (P3, low — STT works): no `OPENROUTER_API_KEY` where the handler reads, so the chain runs on the Sarvam fallback only. Karthi wanted to copy the key from another instance of the predecessor bot but the auto-mode classifier blocked the cross-user read — needs CLI-level approval. (Ops/env state.) _Source: RESUME.md §Environment/ops._
- **A sibling project's swappable alert-delivery seam → C3 transport** — `idea` (P4) · _low-confidence; risk-of-loss: only-an-agent-comment_: a sibling project's alert-delivery seam is intentionally swappable so a future C3 transport can deliver alert verdicts ("we deliberately do NOT build C3 here"). Agent-authored comment in another repo, NOT a Karthi quote — surfaced only so the seam isn't lost. _Source: that project's monitoring/verdict_seam.py._

---

## Half-implemented / hidden gaps (invisible from the docs)

These are real behaviors a reader can't discover from the docs. Doc fixes for several are being applied in this same 2026-06-14 cleanup (see "Doc fixes" below).

- `c3-broker release <cwd>` is wired but **stubbed** (errors) — was not flagged in user docs. (→ doc fix + P2 above.)
- **Auto typing-ticker missing** — typing is manual-only; docs imply it works during background work. (→ FIX #2 + doc fix.)
- **Album/media-group assembly missing** — relies on debounce; there's a real drop bug. Note: `TODO.md` previously called media-group "Skipped (intentional)" while `RESUME.md` documents it as a known bug — contradiction reconciled in this cleanup. (→ FIX #1 + doc fix.)
- **Codex adapter has no `detach` MCP tool** (Claude has 8 incl. detach; Codex has 9 = 8 shared − detach + `inbox` + `codex_forward`). Intentional-by-design but was undocumented. (→ doc fix F9.)
- **`plugins/c3/.mcp.json` wires only the Claude adapter** — Codex wiring lives in the codex launcher / Codex config, so the packaged plugin is Claude-Code-first. Not stated in plugin docs.
- **`ListSessionsReq` CWD field** is a forward-looking placeholder (TODO #19e) — undocumented whether wired.
- **STT gemini provider silently dead** in the deployed env (see P3).

## Doc fixes applied in the 2026-06-14 cleanup

From the doc-vs-code reconciliation (each verified against the actual file):
- `docs/USAGE.md` — replace invented `attach --topic=`/`--group=` flags with the real `attach <name>|<id>|dm|create <name>` interface; narrow "multi-message bursts" to text + disclose album gap; correct the `release` "workaround" to note it's stubbed.
- `docs/ADAPTERS.md` — `/tmp/c3.sock` → `$XDG_RUNTIME_DIR/c3.sock` (fallback `/tmp/c3-$UID/c3.sock`) in all 3 places.
- `docs/PLUGINS.md` — "two providers" → three (add elevenlabs-scribe-v2, opt-in via `--chain`).
- `docs/COMMANDS.md` — add the `release` row (marked stubbed).
- `README.md` — note the Claude/Codex tool asymmetry; soften the typing-indicator wording.
- `cmd/c3-broker/status.go:20` — remove the stale "Claims: (TODO …)" comment (live claims ARE implemented via `OpListClaims`).
- `docs/plans/2026-05-19-mcp-test-race-patch.md` — mark **SUPERSEDED** (targets `internal/mcp/`, deleted by the go-sdk migration; do not resurrect).

---

## Done — verified complete in this audit

So these are not re-litigated. (Detailed checklist lives in `TODO.md`.)

- **#17 claude-shim idempotency** — preserves an existing `--dangerously-load-development-channels` flag, appends `plugin:c3@c3` only when absent; uninstall idempotent.
- **#4 / #5 onboarding** — preamble + consent gate, background `go install`, `C3_NO_PROMPT`.
- **Pairing flow** — 4-digit code, 10-min window, default-deny enrollment.
- **Per-route dispatch sequentialization** — per-route worker pool (one goroutine per RouteKey).
- **The four pre-release UX bugs** (May), the May-19 trivial-fixes sweep + 3 MAJOR code-review fixes.
- **go-sdk migration** (`internal/mcp/` removed in favor of `modelcontextprotocol/go-sdk v1.6.0`).
- **Output-mode + multi-part protocol** single-source-of-truth (`internal/mode`).
- **/c3:ping, /c3:sessions, terminal title, /c3:reload-config** (the 2026-05-19 + 2026-06-01 batches).

---

## Corrections to earlier notes (so they don't recur)

- **No committed `.pyc` cruft.** `__pycache__/` and `*.pyc` are gitignored; `git ls-files plugins/c3/stt/` shows none committed. (An earlier note claimed otherwise.)
- **STT ships three providers, not two** (the docs undercounted).
