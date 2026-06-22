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

## P0.5 — Channel completeness batch — `in-progress` (2026-06-16)

The P0 build shipped, but a live smoke test surfaced **missed Telegram features**: the agent
could not **read poll results**, polls were send-only/regular (no quiz/explanation/timed),
the expandable **"show more"** blockquote was absent, and wide **tables** rendered as literal
pipe-text. Root-caused: the build pipeline had **no completeness gate** — capabilities were
carried as prose and re-summarized at each hop with no end-to-end ledger, so anything dropped
by omission left no trace. Full analysis + a 79-row capability coverage matrix + hardened fix
design: [`docs/specs/2026-06-16-capability-gaps-rootcause-safeguard.md`](docs/specs/2026-06-16-capability-gaps-rootcause-safeguard.md).
The pipeline fix is the **completeness gate** (coverage matrix + pre-build sign-off +
completeness-vs-research review lens + live-verify every rendering claim) — to be folded into
`~/arogara/AGENTS.md`.

**Build phases (signed off 2026-06-16):**
- **P1** ✅ opportunistic in-channel fixes (reaction allowed-set validation + video streaming) — committed `a59e1bb`.
- **P2** — full polls (send): quiz / explanation / timed (open_period·close_date) / correct-option.
- **P3** — expandable show-more blockquote (`<blockquote expandable>`).
- **P4** — reading poll results (`poll`/`poll_answer` inbound → agent; **aggregate + final-on-close**) + `stopPoll`.
- **P5** — chunk HTML-overflow guard. **Typing-cap redesign DEFERRED (Karthi 2026-06-16)** — current ~60s-then-stops behavior stays until revisited.
- **P6** — wide tables: auto-wrap pipe tables into aligned monospace `<pre>` (scrolls on Android, wraps desktop/web — **live-verify on phone**).
- **P7** — inline keyboards + callbacks (tap-to-act / approve-deny / expand-next) — **`ship now`** per Karthi 2026-06-16.

### To build later — discuss after this batch (Karthi 2026-06-16)
Karthi wants these built; parked for a post-batch discussion (do **not** silently drop):
- Inbound **+** outbound **albums** (media-group assembly + `sendMediaGroup`).
- **Echo media by `file_id`** (zero-cost re-send of inbound media; sidesteps 20MB download / 50MB upload caps).
- **Underline** + inline **user-mention** (`tg://user?id=`) formatting.
- **Forwarding** messages.
- **Location** sends (and likely venue/contact).
- _(also deferred in the matrix, same bucket: link-preview control, partial-quote highlighting, `entities[]` path.)_

### Shipped 2026-06-19 (merged to master)
- **Connectivity notifications** — `done` — desktop popup is the PRIMARY outage alert; the CLI turn-injection is a fallback only when the popup didn't deliver (and says so); new ambient Claude Code **status-line indicator** (`⚠ TG offline HH:MM`, auto-clears on reconnect via `refreshInterval`); `notifications.invasive` toggle (default true, SIGHUP-reloadable) silences the invasive surfaces while keeping the status line. Two cross-edge concurrency bugs caught + fixed in final triple review. Spec: [`docs/superpowers/specs/2026-06-18-c3-connectivity-notifications-design.md`](docs/superpowers/specs/2026-06-18-c3-connectivity-notifications-design.md). **Broker restart required to take effect.**
- **Markdown same-type-nesting fix** — `done` — mixed `**`/`__` (or `*`/`_`) spellings no longer emit Telegram-illegal same-type nested HTML tags (was → 400 → all formatting silently stripped to plaintext, which trained agents to avoid formatting); `***x***`/`___x___` → bold+italic. Added a property-test guard. This was a top cause of agents not using rich text.

### Shipped 2026-06-20 (merged to master)
- **Rich-message inbound decode** — `done` — Bot-API-10.1 rich messages arrive in `Message.rich_message` (a recursive `RichBlock`/`RichText` tree) which gotgbot rc.34 silently drops → empty `.Text`. The poll loop now captures the raw `rich_message` JSON (raw `getUpdates` + a probe unmarshal, verified byte-equivalent to gotgbot's own `GetUpdates`) and a new decoder (`internal/channel/telegram/richdecode.go`) renders the tree → GFM markdown (headings, emphasis, links, lists, blockquotes, **tables**, code/math) + **media blocks → downloadable attachments**. Hard invariants: never empty when a rich message is present (`[rich message]`/`[unsupported block:…]` markers), never panics the poll loop (`recover()`). Per-channel `rich_inbound` toggle (default on; **broker restart to change** — startup snapshot, not SIGHUP) + `DeliversRichMessages` capability flag. Built TDD across 8 tasks; per-task + broad whole-branch review clean (untrusted-input recursion risk empirically disproven — Go's JSON decoder errors out before any stack overflow). Spec: [`docs/superpowers/specs/2026-06-19-c3-rich-message-inbound-decode-design.md`](docs/superpowers/specs/2026-06-19-c3-rich-message-inbound-decode-design.md); plan: [`docs/superpowers/plans/2026-06-19-rich-message-inbound.md`](docs/superpowers/plans/2026-06-19-rich-message-inbound.md).
  - **Deferred follow-ups: ✅ DONE 2026-06-20** (merged via `feat/rich-inbound-nits`, eb29e95) — (1) defensive depth-counter (`maxDecodeDepth=256`) threaded through the renderers via thin public wrappers (signatures unchanged); past the cap a `[nesting too deep]` marker is emitted instead of recursing; (2) the 4 test nits — full `escapeInline` 8-char set, ragged-row table, deep-block + deep-inline no-panic + depth-marker, and `DeliversRichMessages` in the golden manifest. Dual-reviewed clean (correctness READY-TO-MERGE + adversarial SAFE). **Empirically settled** (probed to 1M-deep/22MB, no crash): Go's `encoding/json` scanner bounds total nesting at ~5000 (arrays/blocks) / ~10000 (inline wrappers) and errors gracefully → `ok=false`; the 256 render guard is genuine defense-in-depth for the depth window json permits; `recover()` is a never-reached backstop.
  - **Hardening idea (Karthi's call — morning review):** no `io.LimitReader`/size cap on the **getUpdates response body** (the read lives inside gotgbot's `RequestWithContext`, not our code; `MaxDownloadBytes=20MiB` only caps media downloads). Transport-trust only (TLS + our trusted reverse proxy), Minor today — but it would become Important if the proxy is ever treated as semi-trusted. Adding a cap means either bypassing `RequestWithContext` (loses the byte-parity we verified) or wrapping at another layer; real trade-off, not a bolt-on. Surfaced by the rich-inbound-nits adversarial review.
- **Multi-attachment surfacing** — `done` — the Claude adapter's channel frame only emitted `Attachments[0]`, so the agent could reach only the FIRST attachment of any multi-attachment inbound (album/media-group, AND the extra media of a decoded rich message — the real cause of the rich-inbound "media" caveat). The broker was never the problem (`mergeBatch` already concatenates all attachments). Now: first attachment keeps the canonical unsuffixed keys (single-attachment frames byte-identical), extras get `attachment_*_N` (N≥2) + `attachment_count`; uncaptioned albums label as `(N attachments)`. Numbered flat keys chosen over a JSON array (no nested-quote escaping in a `key="…"` attribute; directly agent-legible). 3 tests, reviewed clean. **Adapter deploys per-session** → takes effect on a fresh Claude Code session, not a live restart. Resolves the surfacing half of FIX #1 below.
- **Formatting policy — agents format liberally** — `done` — the agent-guidance half of the rich-text work (the converter bug was fixed earlier). Five agent-facing surfaces changed so agents format replies for **readability** instead of defaulting to flat plain text (Karthi 2026-06-19: "anything that makes a wall of text easier to read should be done; no reason to keep it plain"): (1) `internal/capability/guidance.go` RichText line reframed permissive→prescriptive ("you SHOULD use it whenever structure makes a reply easier to read") with an inline worked example; (2) a compose-time formatting nudge added to the `reply` tool Description in BOTH adapters (byte-identical strings); (3) `internal/mode/protocol.go` `Combined()` reordered Mode→CHANNEL CAPABILITIES→Multipart so the formatting guidance is no longer dead-last (ModeProtocol stays first — it's the safety-critical no-auto-reply/no-auto-switch contract); (4) worked example folded into the guidance + a fuller literal one into agent memory; (5) the plain-prose memory (`feedback_telegram_mode`) rewritten from blanket-plain → **content-based register** (conversational stays plain SMS prose; structured content — steps/comparisons/code/tables/lists/quotes/links — gets formatted). Reconciles the apparent plain-prose vs format-liberally tension via a content-based dividing line. Triple-reviewed clean (code-correctness + policy/consistency + security/keep-out); PII audit clean. **Deploys per-session** (adapters splice the guidance into their MCP `instructions` at initialize) → takes effect on a fresh session, not a live broker restart. Spec: [`docs/superpowers/specs/2026-06-20-formatting-policy-design.md`](docs/superpowers/specs/2026-06-20-formatting-policy-design.md).

### Shipped 2026-06-22 (merged to master)
- **409 conflict self-heals (no more dead-broker-needs-restart)** — `done` (`f817ee8`) — a Telegram `409 Conflict` ("another getUpdates is active for this token") was treated as **terminal**: `pollLoop` logged "Kill the other process and restart c3-broker" and `return`ed. The broker *process* stayed up (socket/adapters/health all served) but **never polled again** — inbound silently dead until a human killed + restarted it. Root cause of the 2026-06-22 fresh-laptop incident: wake → flaky-proxy `getUpdates` timeout storm → the next poll raced Telegram's still-open prior long-poll → 409 → poll loop exited (inbound dead 08:56→09:03 until manual restart). Fix: on 409, drive fetch-health (a **persistent** conflict still raises the out-of-band `FETCH DOWN` alert past `downAfterFails`), back off with an escalating delay (5s→60s cap), and **RETRY** — never exit. First success auto-recovers + resets; a genuine off-box second poller just stays in slow-retry + DOWN-alerting until it clears (60s cap = no retry-storm). The broker singleton (flock + listen socket) already prevents a second *local* broker, so the old "kill the other broker" assumption almost never applied. TDD: 2 tests drive the real `pollLoop` with a scripted fake bot (transient-409→recovers-no-alert; persistent-409→alerts-and-keeps-polling). Whole module green + `-race` clean; PII clean. **Deployed live** (broker rebuilt + restarted; running binary sha-verified to contain the fix). `DEBUGGING.md` "Two brokers fighting" section updated.
- **Zombie-broker spawn reaped** — `done` — dead `c3-broker` processes were becoming **zombies parented by the adapter** that spawned them: `spawnBroker` did `cmd.Start()` with `setsid` but never `Wait()`d, and `setsid` creates a new SESSION without reparenting to init — so a broker that exits (the common case: it lost the singleton-flock race, which is exactly what every losing adapter's spawn does) lingered as a `<defunct>` child until the session ended. Fix: extracted `spawnDetached(cmd)` in **both** adapters (verbatim parity) which, after `Start()`, runs `go func(){ _ = cmd.Wait() }()` to reap on exit — the winning broker's goroutine just blocks for the daemon's lifetime, losers are reaped in milliseconds, and if the adapter exits first the broker reparents to init which reaps it. TDD: a reaping test in each adapter package (spawn `true`, assert kernel reports ESRCH). **Adapters deploy per-session** → live on the next fresh Claude Code/Codex session, not a hot-swap; the few existing zombies clear when their current terminals close.

### Next round — Karthi 2026-06-19 (do NOT drop)
- **Formatting policy — make agents format liberally** — ✅ `done` (shipped 2026-06-20, see below). The 5-surface agent-guidance change is merged to master.

### Roadmap discussion — shared Telegram adapter for SSHGate + C3 (Karthi 2026-06-16)
Both SSHGate and C3 talk to Telegram (bot send/receive + approvals). Explore whether they can
share **one Telegram adapter/implementation** instead of two parallel ones. Design + decision
to discuss later — not yet scoped.

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
- **FIX #1 (parked): inbound delivery-drop + album/media-group drop** — ✅ `resolved` (2026-06-20)
  **Album/media-group half: RESOLVED 2026-06-20** — root cause was NOT a broker pre-enqueue drop. Verified live (msgs 2381+2382, one media group): both updates enqueue, the debounce batch merges them, and `mergeBatch` (`internal/broker/worker.go:345`) concatenates all attachments — so the broker keeps both. The loss was the **Claude adapter emitting only `Attachments[0]`** in the channel frame; fixed by "Multi-attachment surfacing" above. So the old hypothesis ("a same-poll-batch update is dropped before enqueue in poll.go") was wrong — no such drop exists.
  **Back-to-back TEXT half: RESOLVED 2026-06-20 — merge-perception, NOT a drop.** Confirmed by reading the path end-to-end: two text messages within the debounce window both enqueue via the identical producer path proven for the album half (`worker.go:179-209`, no pre-enqueue drop), both land in the debounce batch, and `mergeBatch` (`worker.go:340-350`) joins **every** non-empty `in.Text` with `"\n"` — the only skip conditions are a `nil` inbound or an `IsEvent()` (neither applies to a genuine text). Both texts reach the agent in one merged block; the historical "msgs 186/187 ~33µs apart, only one reached the agent" was that merged block read as a single message. Already locked by `TestMergeBatch_ConcatenatesText` (`internal/broker/debounce_test.go:21`: three back-to-back texts → `"first\nsecond\nthird"`). No code change needed.
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
