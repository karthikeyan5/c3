# C3 — Build Log (archived history)

This is the archived build history of C3 — the shipped logs, done-lists, doc-fix records, superseded designs, and corrections **moved verbatim out of `ROADMAP.md` on 2026-06-27** when the roadmap was rebuilt to be forward-only. Nothing here is forward work; the live roadmap lives in [`../ROADMAP.md`](../ROADMAP.md). Sections are kept in their original wording and roughly their original order.

---

## Original ROADMAP preamble (Status as of 2026-06-15)

**Status as of 2026-06-15.** The channel rich-content + capability architecture (the P0 below) is **BUILT and committed** on `master` — 8 phases (P0–P7), designed via a 10-agent workflow, hardened by 3 critique passes, and triple-reviewed. Pending a live Telegram smoke test (the one check that needs a phone — checklist in `docs/specs/2026-06-14-channel-capability-architecture.md`). Version line: v0.1.0 (pre-public-push).

This is the **single consolidated roadmap** for C3. It was reconciled on 2026-06-14 from every source — `TODO.md`, `RESUME.md`, `MORNING-REVIEW-2026-05-19.md`, `DECISIONS.md`, `DEBUGGING.md`, `docs/plans/` + `docs/specs/`, the live Go codebase, and a mining sweep of every past C3 session transcript (incl. cross-project sessions). **Nothing was dropped:** ideas that previously lived only in voice notes are now captured here and flagged `risk-of-loss: was-untracked`. **(2026-06-26: four threads from Karthi's roadmap discussion folded in as the dated section directly below — interactive Q&A, trust/permissions, remote spawn+control, programmatic spawn-and-attach. Existing items are unchanged; the new section cross-links them.)**

C3 is a Go end-to-end Telegram multiplexer for multiple Claude Code / Codex CLI sessions: one broker daemon, per-CLI MCP adapters, topic-based routing. **Code health:** `go build ./...` and `go vet ./...` pass clean; all packages test green **except** 2 environment-flaky broker tests (a test-fixture defect, not a production bug — see P2). The big open items below are **features that were never built**, not regressions.

Legend — **status**: `planned` · `in-progress` · `idea` (not yet committed to) · `done`. **priority**: P0 (Karthi's stated #1) → P4 (nice-to-have).

---

## 2026-06-26 — Roadmap discussion (Karthi): four threads to build next

*(Archived as provenance — the forward content of all four threads now lives in `ROADMAP.md`; the feasibility probes referenced here are RESOLVED per `MORNING-REVIEW-2026-06-26.md`.)*

Captured from Karthi's 2026-06-26 voice roadmap session. **Nothing here replaces the items below** — each thread cross-links to its existing home in this roadmap; this section adds the new specifics, the live bug, the fresh-research asks, and my recommended sequencing. Karthi's stated "build now" = Thread C/D (remote spawn + control).

### Thread A — Interactive Q&A over Telegram (Claude's questions → buttons/polls → answer round-trip) — **`bug + feature`** · NEW
**Karthi:** "Architect this complete feature — whatever questions Claude asks: with options / without options / with additional comments / Other / Skip / multi-select — all supported. Research what Telegram bots support **in groups/topics** (buttons, polls, dynamic inline keyboards; mini-app likely DM-only). Take inspiration from the official Claude Telegram plugin. Build it solid so Claude knows it asks questions *here* when in Telegram mode."
- **Live bug (reported by a developer):** Claude's option-questions render as buttons on Telegram via C3, but **tapping a button does not deliver the answer back to Claude** — the agent never receives the choice. Root cause not yet investigated.
  - Clue (from a code map, unverified): inline-keyboard **send** + callback **inbound** routing already exist — `buildInlineKeyboard` (`internal/channel/telegram/outbound.go`), `dispatchCallback` → `CallbackEvent` → worker (`internal/channel/telegram/poll.go`). So callbacks *are* wired into the broker; the gap is likely either (i) the `CallbackEvent` not being surfaced to the agent's channel frame, or (ii) Claude Code's **native `AskUserQuestion`** primitive not being the wired path at all. **Systematic-debug before any fix.**
- **Existing home:** P0.5 **P7 — inline keyboards + callbacks** (shipped) is the substrate. This thread is the *AskUserQuestion-grade* layer on top: the full question taxonomy + the answer round-trip.
- **Hard-feasibility probe first (AGENTS.md rule):** does the Claude Code harness expose `AskUserQuestion` to a channel/MCP adapter at all (the way it exposes permission prompts as the channel's 3rd surface)? If **no**, the only path is agent-rendered buttons + callback-surfaced answers (substrate exists). If **yes**, wire the native primitive. Same kind of probe that killed reasoning-streaming — do it before designing.
- **Research scope:** Telegram interactive options usable **in group topics** (inline keyboards w/ `callback_data`, native polls/quiz, force-reply); mini-app/web-app constraints (DM-only?); how the official Claude Telegram plugin presents choices. Fetch **live** Bot API + Claude Code docs (don't trust model memory).

### Thread B — Trust / permission approvals over Telegram (why Claude "doesn't trust" C3) — **`planned`** · re-research asked
**Karthi:** "The official Telegram plugin is trusted — when it needs permission it asks approve/deny on Telegram and I allow it. C3 can't. How is that trust established? Re-research fresh; tell me what you found last time too."
- **What I found before** (memory `c3_trusted_operator_authz_spec`, verified 2026-06-14):
  - The `<channel …>` wrapper + "treat as untrusted external data" warning are emitted by the **Claude Code harness, not C3** (0 hits in the c3 tree). C3 cannot suppress it, and there is **no supported API to mark inbound Telegram text as "trusted."** So the official plugin doesn't make replies trusted either.
  - The official plugin's approve/deny is the channel's **3rd surface — permission relay**: the harness forwards a genuine permission *prompt*, you tap approve/deny, the verdict goes back. **C3 can do exactly this — it just hasn't built it yet** (see "Permission relay", P1 below; assessed GO, build prompt + corrections ready).
  - For actions the **auto-mode classifier hard-denies** (no prompt ever opens — e.g. `sudo` authorized over Telegram), the only lever is a **PreToolUse hook** returning allow/deny (SSHGate's model lifted to the CC permission gate). That's the **Trusted-operator DM authorization** spec (P1 below; blocked on the §9 Phase-0 gate: empirically verify a hook "allow" overrides auto-mode).
- **The answer in one line:** "trust" = build the **permission-relay surface** (official-plugin parity) + the **PreToolUse trusted-operator hook** (for classifier hard-denies). Both already specced; this thread = fresh-confirm against live docs, then build.
- **Re-research ask:** re-verify the Phase-0 gate (does a hook `allow` bypass the auto-mode classifier?) against **current** Claude Code; check for any new "trusted plugin" / channel-trust mechanism shipped since 2026-06-14.

### Thread C — Remote spawn + control of a Claude Code / Codex CLI from Telegram — **`in-progress` (designed, 0 code)** · Karthi's "build now"
**Karthi:** "Spawn a Claude Code/Codex instance and operate it from one Claude instance, via a C3 MCP — the broker controls every CLI it started. Run `/compact`, `/status` and other slash commands over Telegram when away from the laptop; type directly into the TUI (`/input`); make arrow-key option menus work. Maybe a web UI, or parse the TUI into a Telegram message. **Bare minimum: run /commands + inject input.**"
- **Existing home:** P1 **Remote terminal-control** — design **DECIDED** (2026-06-15): C3 stays the brain; **all-Go PTY stack** (`creack/pty` + `Netflix/go-expect` + a VT emulator) as a new broker worker type; **arbitrary TUIs** (raw PTY). Next step was: prototype the Go PTY worker (snapshot-on-idle + send-keys). Full design notes in `RESUME.md §THE MAIN FEATURE`.
- **New specifics from 2026-06-26 to fold into the MVP scope:** explicit `/compact` + `/status` over Telegram; an `/input` (or `/tui`) command that types straight into the running TUI; arrow-key menu navigation; snapshot-on-idle rendering (already in the design) as the "see the screen" path.
- **Thread C-lite (Karthi: "if there's a way without meddling with the TUI, I'd be happy"):** re-check **live** Claude Code docs for any supported headless / programmatic slash-command or remote-control path (native **Remote Control** via claude.ai + mobile already exists as an aside — zero build, but separate from Telegram). If a supported control API now exists, it may beat the PTY route for the MCP-aware CLIs (Claude/Codex), leaving the PTY path only for arbitrary TUIs.

### Thread D — Programmatic spawn-and-attach API (call C3 to bring up a CLI + attach it to a channel+topic) — **`idea`, design needed** · NEW
**Karthi:** "Make spawning+attaching a CLI callable by *me or another agent* — via the MCP directly, or a broker CLI / API. Pass parameters: which channel, which topic (name/ID). Since channels are pluggable, plug in other chat APIs too. This is the same 'bring up and control a CLI' mechanism, exposed programmatically."
- **This is the agent/programmatic twin of Thread C.** C = human-from-Telegram drives a spawned CLI; D = code/agent drives the spawn-and-attach via a stable interface (MCP tool / `c3-broker` subcommand / local API).
- **Existing substrate:** the `Channel` interface is fully pluggable and proven (Telegram is the only impl); `attach`/topic-routing already maps a session→channel+topic. **Net-new:** a control surface to *spawn a CLI process detached, wire its adapter, and attach it to a named channel+topic* — no design exists yet.
- **Related existing items (don't duplicate):** P2 "Programmatic (non-chat) channel extension", Phase-4 "Topic creation via API", Phase-4 "Inter-CLI messaging", Phase-4 "Web/voice channels". Thread D is the spawn-and-attach *control plane*; those are channels/messaging built on top.

### Recommended sequencing (my read — for discussion)
1. **Thread A bug first** — it's a *live* defect a developer hit; root-cause it (cheap, the substrate exists) before the bigger Thread-A feature. Likely a small fix that also confirms the substrate for the full feature.
2. **Thread C/D together** — Karthi's "build now." They share the spawn-and-control engine; design them as one subsystem (PTY worker + a programmatic spawn-and-attach surface). MVP = `/status` + `/compact` + `/input` over Telegram. Do **Thread C-lite's live-docs probe first** — a supported control API could shrink the whole build.
3. **Thread B** — permission relay (official-plugin parity) + trusted-operator hook; specs exist, gated on the Phase-0 probe. High value, well-understood, can run in parallel with C/D (different subsystem).
4. **Thread A full feature** — after the bug + the feasibility probe, architect the full question taxonomy.

**Open feasibility probes to run before committing build effort:** (A) does the harness expose `AskUserQuestion` to a channel? (B) does a PreToolUse `allow` override auto-mode? (C-lite) is there a supported programmatic slash-command / remote-control API in current Claude Code? All three are "fetch live docs + a 5-min empirical test," per AGENTS.md.

---

## P0 — Channel rich-content + capability architecture — ✅ BUILT (2026-06-15)

*(Note: the "deterministic streaming of reasoning/thinking — deferred" bullet is forward work and now lives in `ROADMAP.md`; it is retained here verbatim as part of the shipped-architecture record.)*

Architecture: **Capability Manifest + Gate (CMG)** — each channel returns a flat
capability manifest; one pure broker-side `Gate` validates + degrades every outbound; the
agent receives capability + formatting guidance (Claude **and** Codex); no Telegram code
leaks into core (enforced by a CI grep-guard, `internal/archguard`). Spec:
[`docs/specs/2026-06-14-channel-capability-architecture.md`](specs/2026-06-14-channel-capability-architecture.md).
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

*(Note: the "To build later" subsection and P5's deferred typing-cap are forward work and now live in `ROADMAP.md`; retained here verbatim as part of the batch record.)*

The P0 build shipped, but a live smoke test surfaced **missed Telegram features**: the agent
could not **read poll results**, polls were send-only/regular (no quiz/explanation/timed),
the expandable **"show more"** blockquote was absent, and wide **tables** rendered as literal
pipe-text. Root-caused: the build pipeline had **no completeness gate** — capabilities were
carried as prose and re-summarized at each hop with no end-to-end ledger, so anything dropped
by omission left no trace. Full analysis + a 79-row capability coverage matrix + hardened fix
design: [`docs/specs/2026-06-16-capability-gaps-rootcause-safeguard.md`](specs/2026-06-16-capability-gaps-rootcause-safeguard.md).
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
- **P7** — inline keyboards + callbacks (tap-to-act / approve-deny / expand-next) — **`ship now`** per Karthi 2026-06-16. **→ 2026-06-26 (Karthi, Thread A):** this is the substrate; a live bug ("tapping a button doesn't deliver the answer back to Claude") + the full AskUserQuestion-grade Q&A feature sit on top of it. See the "2026-06-26 — Roadmap discussion" section at the top.

### To build later — discuss after this batch (Karthi 2026-06-16)
Karthi wants these built; parked for a post-batch discussion (do **not** silently drop):
- Inbound **+** outbound **albums** (media-group assembly + `sendMediaGroup`).
- **Echo media by `file_id`** (zero-cost re-send of inbound media; sidesteps 20MB download / 50MB upload caps).
- **Underline** + inline **user-mention** (`tg://user?id=`) formatting.
- **Forwarding** messages.
- **Location** sends (and likely venue/contact).
- _(also deferred in the matrix, same bucket: link-preview control, partial-quote highlighting, `entities[]` path.)_

### Shipped 2026-06-19 (merged to master)
- **Connectivity notifications** — `done` — desktop popup is the PRIMARY outage alert; the CLI turn-injection is a fallback only when the popup didn't deliver (and says so); new ambient Claude Code **status-line indicator** (`⚠ TG offline HH:MM`, auto-clears on reconnect via `refreshInterval`); `notifications.invasive` toggle (default true, SIGHUP-reloadable) silences the invasive surfaces while keeping the status line. Two cross-edge concurrency bugs caught + fixed in final triple review. Spec: [`docs/superpowers/specs/2026-06-18-c3-connectivity-notifications-design.md`](superpowers/specs/2026-06-18-c3-connectivity-notifications-design.md). **Broker restart required to take effect.**
- **Markdown same-type-nesting fix** — `done` — mixed `**`/`__` (or `*`/`_`) spellings no longer emit Telegram-illegal same-type nested HTML tags (was → 400 → all formatting silently stripped to plaintext, which trained agents to avoid formatting); `***x***`/`___x___` → bold+italic. Added a property-test guard. This was a top cause of agents not using rich text.

### Shipped 2026-06-20 (merged to master)
- **Rich-message inbound decode** — `done` — Bot-API-10.1 rich messages arrive in `Message.rich_message` (a recursive `RichBlock`/`RichText` tree) which gotgbot rc.34 silently drops → empty `.Text`. The poll loop now captures the raw `rich_message` JSON (raw `getUpdates` + a probe unmarshal, verified byte-equivalent to gotgbot's own `GetUpdates`) and a new decoder (`internal/channel/telegram/richdecode.go`) renders the tree → GFM markdown (headings, emphasis, links, lists, blockquotes, **tables**, code/math) + **media blocks → downloadable attachments**. Hard invariants: never empty when a rich message is present (`[rich message]`/`[unsupported block:…]` markers), never panics the poll loop (`recover()`). Per-channel `rich_inbound` toggle (default on; **broker restart to change** — startup snapshot, not SIGHUP) + `DeliversRichMessages` capability flag. Built TDD across 8 tasks; per-task + broad whole-branch review clean (untrusted-input recursion risk empirically disproven — Go's JSON decoder errors out before any stack overflow). Spec: [`docs/superpowers/specs/2026-06-19-c3-rich-message-inbound-decode-design.md`](superpowers/specs/2026-06-19-c3-rich-message-inbound-decode-design.md); plan: [`docs/superpowers/plans/2026-06-19-rich-message-inbound.md`](superpowers/plans/2026-06-19-rich-message-inbound.md).
  - **Deferred follow-ups: ✅ DONE 2026-06-20** (merged via `feat/rich-inbound-nits`, eb29e95) — (1) defensive depth-counter (`maxDecodeDepth=256`) threaded through the renderers via thin public wrappers (signatures unchanged); past the cap a `[nesting too deep]` marker is emitted instead of recursing; (2) the 4 test nits — full `escapeInline` 8-char set, ragged-row table, deep-block + deep-inline no-panic + depth-marker, and `DeliversRichMessages` in the golden manifest. Dual-reviewed clean (correctness READY-TO-MERGE + adversarial SAFE). **Empirically settled** (probed to 1M-deep/22MB, no crash): Go's `encoding/json` scanner bounds total nesting at ~5000 (arrays/blocks) / ~10000 (inline wrappers) and errors gracefully → `ok=false`; the 256 render guard is genuine defense-in-depth for the depth window json permits; `recover()` is a never-reached backstop.
  - **Hardening idea (Karthi's call — morning review):** no `io.LimitReader`/size cap on the **getUpdates response body** (the read lives inside gotgbot's `RequestWithContext`, not our code; `MaxDownloadBytes=20MiB` only caps media downloads). Transport-trust only (TLS + our trusted reverse proxy), Minor today — but it would become Important if the proxy is ever treated as semi-trusted. Adding a cap means either bypassing `RequestWithContext` (loses the byte-parity we verified) or wrapping at another layer; real trade-off, not a bolt-on. Surfaced by the rich-inbound-nits adversarial review. *(Forward — now tracked in `ROADMAP.md`.)*
- **Multi-attachment surfacing** — `done` — the Claude adapter's channel frame only emitted `Attachments[0]`, so the agent could reach only the FIRST attachment of any multi-attachment inbound (album/media-group, AND the extra media of a decoded rich message — the real cause of the rich-inbound "media" caveat). The broker was never the problem (`mergeBatch` already concatenates all attachments). Now: first attachment keeps the canonical unsuffixed keys (single-attachment frames byte-identical), extras get `attachment_*_N` (N≥2) + `attachment_count`; uncaptioned albums label as `(N attachments)`. Numbered flat keys chosen over a JSON array (no nested-quote escaping in a `key="…"` attribute; directly agent-legible). 3 tests, reviewed clean. **Adapter deploys per-session** → takes effect on a fresh Claude Code session, not a live restart. Resolves the surfacing half of FIX #1 below.
- **Formatting policy — agents format liberally** — `done` — the agent-guidance half of the rich-text work (the converter bug was fixed earlier). Five agent-facing surfaces changed so agents format replies for **readability** instead of defaulting to flat plain text (Karthi 2026-06-19: "anything that makes a wall of text easier to read should be done; no reason to keep it plain"): (1) `internal/capability/guidance.go` RichText line reframed permissive→prescriptive ("you SHOULD use it whenever structure makes a reply easier to read") with an inline worked example; (2) a compose-time formatting nudge added to the `reply` tool Description in BOTH adapters (byte-identical strings); (3) `internal/mode/protocol.go` `Combined()` reordered Mode→CHANNEL CAPABILITIES→Multipart so the formatting guidance is no longer dead-last (ModeProtocol stays first — it's the safety-critical no-auto-reply/no-auto-switch contract); (4) worked example folded into the guidance + a fuller literal one into agent memory; (5) the plain-prose memory (`feedback_telegram_mode`) rewritten from blanket-plain → **content-based register** (conversational stays plain SMS prose; structured content — steps/comparisons/code/tables/lists/quotes/links — gets formatted). Reconciles the apparent plain-prose vs format-liberally tension via a content-based dividing line. Triple-reviewed clean (code-correctness + policy/consistency + security/keep-out); PII audit clean. **Deploys per-session** (adapters splice the guidance into their MCP `instructions` at initialize) → takes effect on a fresh session, not a live broker restart. Spec: [`docs/superpowers/specs/2026-06-20-formatting-policy-design.md`](superpowers/specs/2026-06-20-formatting-policy-design.md).

### Shipped 2026-06-22 → durable inbound queue + backlog delivery (branch `feat/inbound-queue`)
- **Durable inbound queue + backlog delivery** — `done` (T1–T11; on `feat/inbound-queue`, pending merge to master) — once C3 has *received* a Telegram message it is never lost: inbound messages that arrive with no session attached (or that the broker caught up on after being down) are held in a durable, per-route, append-only on-disk queue (`$XDG_STATE_HOME/c3/queue/`, fallback `~/.local/state/c3/queue/`) and delivered on attach. The Telegram read offset now advances **only after** a message is `fsync`'d to the queue (was: advanced on dispatch, before persist — the root loss bug), so a crash mid-flight is loss-free (Telegram redelivers within its 24h retention). The no-session **drop** is replaced by a "📨 Held — nothing lost" auto-reply carrying the running count (cooldown'd to once per 5-min window). On attach the session gets a backlog summary (count + previews) and pulls via the new **`fetch_queue`** MCP tool (`limit` default 3 / max 50 / `"all"`; `ack` default true = consume, false = peek; both adapters). The new **`retranscribe`** MCP tool re-runs the STT chain on saved audio by `file_id` and, with an optional `message_id`, refreshes a still-queued message's transcript **in place**; STT failures now emit a self-documenting recovery message (audio saved, no resend, how to `download_attachment`/`retranscribe`). A **`/status`** Telegram bot command (registered via `setMyCommands`, intercepted before gating, never routed) reports per-topic depth in a topic and a broker-wide summary in DM/General. Per-route caps 1000 messages / 14 days, drop-oldest, never silent (broker.log + Telegram notice). Claude is push-for-live + summary+pull-for-backlog (gains `fetch_queue`); Codex's fragile in-memory cap-100 ring is **retired** in favor of the broker-backed durable queue. Spec: [`docs/superpowers/specs/2026-06-22-c3-durable-inbound-queue-design.md`](superpowers/specs/2026-06-22-c3-durable-inbound-queue-design.md); plan: [`docs/superpowers/plans/2026-06-22-c3-durable-inbound-queue.md`](superpowers/plans/2026-06-22-c3-durable-inbound-queue.md). User docs: `docs/USAGE.md` "Durable inbound queue & backlog"; `docs/CHANNELS.md` Telegram `/status` + persisted-offset notes. **Broker rebuild+restart needed for the broker-side changes; adapter changes (the `fetch_queue`/`retranscribe` tools) take effect on a fresh CLI session.**

### Shipped 2026-06-22 (merged to master)
*(Note: Batch **G** — 2nd Bot-API proxy failover — is code-done but infra-deferred; that infra item is forward and now lives in `ROADMAP.md`. Retained here verbatim as part of the batch record.)*
- **Recovery-hardening program (batches A–H)** — `done` — a 38-agent recovery-posture audit
  (24 confirmed findings) followed by an ultracode multi-batch build, all merged to master, each
  batch adversarially reviewed + a final triple-lens whole-diff review. Spec:
  [`docs/superpowers/specs/2026-06-22-recovery-hardening-design.md`](superpowers/specs/2026-06-22-recovery-hardening-design.md).
  - **A** (`74e2b0a`): panic-supervised poll/silence/heartbeat goroutines (recover→log→health-DOWN→restart);
    conflict-aware health (a getMe heartbeat can't clear DOWN while a getUpdates 409 is active — fixes a
    follow-on gap in the 409 fix); silence-arm consec consistency; offset-save advisory; per-update dispatch guard.
  - **B** (`bb0fa5f`): offline-safe boot (`DisableTokenCheck` so a flaky-wake network can't abort startup);
    fatal startup errors → broker.log (were discarded stderr); resilient mappings load (`.bak` fallback on a
    corrupt edit; never seed a skeleton over real-but-broken config). (Investigated + dropped a pidAlive comm-check.)
  - **C** (`c94f01e`): STT dedicated venv (`~/.config/c3/stt-venv`, auto-detected) — fixes the LIVE
    `ModuleNotFoundError: sarvamai` on every >30s note; graceful ImportError, ffprobe→REST fallback,
    download-time budget, Telegram failure notice; requirements.txt + `setup-venv.sh` + install wiring.
  - **E** (`fd730c4`): health.json broker-liveness wrapper (`broker_pid`+`written_unix`+45s refresh ticker)
    so the status line shows "C3 broker down" when the broker process dies (was frozen green). Status-line
    reader updated out-of-repo (`~/.claude/statusline-command.sh`).
  - **D** (`74597d7`): adapter↔broker IPC robustness — Codex reconnect-forever parity; worker SKIP→Telegram
    fallback bounce (no inbound dropped in the reconnect window); route-claim replay after a broker restart;
    notify-fail content logging; broker-down advisory (both adapters); autoAttach race fix; `spawnDetached`→`internal/spawn`.
  - **F** (`1446ee6`): opt-in `systemd --user` supervisor (`docs/systemd/`) — auto-restarts a crashed broker
    with no session open. NOT enabled (operator's choice). STT caveat documented (set `plugins.stt.handler_path`).
  - **H** (this commit): broker-side panic supervision (the worker goroutine running the plugin pipeline +
    outbound, the conn handler, and the health ticker now recover→log→continue instead of crashing the whole
    broker — the silent-death class Batch A only fixed for the telegram goroutines); plus the systemd-STT
    handler_path docs gap and the `c3:build` STT reminder.
  - **G** (2nd Bot-API proxy for endpoint-failover): **code done + tested (deferred infra)** — failover engages
    when `channels.telegram.api_base_urls` has >1 endpoint; provisioning a 2nd maintainer-owned proxy + adding it
    is a future task (Karthi deferred 2026-06-22; GCP-Singapore or Oracle-Always-Free). *(Forward — now tracked in `ROADMAP.md`.)*
  - Deploy notes: broker-side changes (A/B/C/E/H + D's worker change) need a broker rebuild+restart (done,
    sha-verified live); adapter changes (D) take effect on a fresh CLI session.
- **409 conflict self-heals (no more dead-broker-needs-restart)** — `done` (`f817ee8`) — a Telegram `409 Conflict` ("another getUpdates is active for this token") was treated as **terminal**: `pollLoop` logged "Kill the other process and restart c3-broker" and `return`ed. The broker *process* stayed up (socket/adapters/health all served) but **never polled again** — inbound silently dead until a human killed + restarted it. Root cause of the 2026-06-22 fresh-laptop incident: wake → flaky-proxy `getUpdates` timeout storm → the next poll raced Telegram's still-open prior long-poll → 409 → poll loop exited (inbound dead 08:56→09:03 until manual restart). Fix: on 409, drive fetch-health (a **persistent** conflict still raises the out-of-band `FETCH DOWN` alert past `downAfterFails`), back off with an escalating delay (5s→60s cap), and **RETRY** — never exit. First success auto-recovers + resets; a genuine off-box second poller just stays in slow-retry + DOWN-alerting until it clears (60s cap = no retry-storm). The broker singleton (flock + listen socket) already prevents a second *local* broker, so the old "kill the other broker" assumption almost never applied. TDD: 2 tests drive the real `pollLoop` with a scripted fake bot (transient-409→recovers-no-alert; persistent-409→alerts-and-keeps-polling). Whole module green + `-race` clean; PII clean. **Deployed live** (broker rebuilt + restarted; running binary sha-verified to contain the fix). `DEBUGGING.md` "Two brokers fighting" section updated.
- **Zombie-broker spawn reaped** — `done` — dead `c3-broker` processes were becoming **zombies parented by the adapter** that spawned them: `spawnBroker` did `cmd.Start()` with `setsid` but never `Wait()`d, and `setsid` creates a new SESSION without reparenting to init — so a broker that exits (the common case: it lost the singleton-flock race, which is exactly what every losing adapter's spawn does) lingered as a `<defunct>` child until the session ended. Fix: extracted `spawnDetached(cmd)` in **both** adapters (verbatim parity) which, after `Start()`, runs `go func(){ _ = cmd.Wait() }()` to reap on exit — the winning broker's goroutine just blocks for the daemon's lifetime, losers are reaped in milliseconds, and if the adapter exits first the broker reparents to init which reaps it. TDD: a reaping test in each adapter package (spawn `true`, assert kernel reports ESRCH). **Adapters deploy per-session** → live on the next fresh Claude Code/Codex session, not a hot-swap; the few existing zombies clear when their current terminals close.

### Next round — Karthi 2026-06-19 (do NOT drop)
- **Formatting policy — make agents format liberally** — ✅ `done` (shipped 2026-06-20, see below). The 5-surface agent-guidance change is merged to master.

---

## P1 — Remote terminal-control (the main build feature — sequenced *after* the channel architecture above)

*(SUPERSEDED by `docs/superpowers/specs/2026-06-26-c3-remote-cli-control-design.md` §0, which explicitly supersedes this section and `RESUME.md §THE MAIN FEATURE`. Archived here as provenance; the forward home is the "Remote CLI control (Thread C/D)" item in `ROADMAP.md`.)*

- **Remote terminal-control of coding agents from Telegram** — `in-progress`
  Bring up a terminal connected to a DM and spawn/control other coding agents (TUI or not). Needs a dedicated PTY subsystem (can't ride the MCP channel surface). Karthi's reference: `github.com/helvesec/rmux`. Mid-design.
  _Source: RESUME.md §THE MAIN FEATURE (2026-06-01)._
- **Terminal-control design — DECIDED (Karthi 2026-06-15):**
  - Q1 → **C3 stays the brain** — it drives the terminal engine and reuses the per-route-worker model.
  - Q2 → **all-Go PTY stack** — `creack/pty` + `Netflix/go-expect` + a VT emulator as a new broker worker type; single-language, no Rust dependency.
  - Q3 → **arbitrary TUIs** — the raw PTY path; control anything in a terminal, not just MCP-aware agents.
  - Next concrete step: prototype the Go PTY worker (snapshot-on-idle rendering + send-keys), per the 2026-06-01 handoff in RESUME.md.
  _Source: RESUME.md §Q1/Q2/Q3; decided 2026-06-15._
  - **→ 2026-06-26 (Karthi, Thread C/D):** "build now." Fold into the MVP: `/compact`+`/status` over Telegram, an `/input` (or `/tui`) command to type into the running TUI, arrow-key menu nav. Plus **Thread D** — expose spawn-and-attach programmatically (MCP tool / `c3-broker` subcommand / API) so an agent can bring up a CLI and attach it to a named channel+topic. Probe **C-lite** first: any supported headless/remote-control API in current Claude Code may shrink this. See the "2026-06-26 — Roadmap discussion" section at the top.

---

## P1 — Near-term (push-blockers & parked fixes) — resolved/superseded entries

*(The "Trusted-operator DM authorization" and "First-run install validation" items from this section are forward work and now live in `ROADMAP.md`. The entries below are the shipped / resolved / decided ones, archived verbatim.)*

- **Permission relay** — `planned` *(now SHIPPED as Thread B; the planned entry is archived — remaining live-verify + Phase-2 niceties are in `ROADMAP.md`.)*
  Forward Claude Code permission prompts to Telegram for remote approve/deny. The one supported remote-approval path (the channel's 3rd surface). Build prompt exists; relay returns GO/DENY as a **string, not bool**; does NOT catch auto-mode classifier hard-denies (that's the trusted-operator item below).
  _Source: RESUME.md §Sub-feature permission relay (assessed GO)._
  **→ 2026-06-26 (Karthi, Thread B):** this IS the "official-Telegram-plugin trust" Karthi asked about — the official plugin uses this same 3rd surface; C3 just hasn't built it. Pair with the trusted-operator hook below. See the "2026-06-26 — Roadmap discussion" section at the top.
- **FIX #1 (parked): inbound delivery-drop + album/media-group drop** — ✅ `resolved` (2026-06-20)
  **Album/media-group half: RESOLVED 2026-06-20** — root cause was NOT a broker pre-enqueue drop. Verified live (msgs 2381+2382, one media group): both updates enqueue, the debounce batch merges them, and `mergeBatch` (`internal/broker/worker.go:345`) concatenates all attachments — so the broker keeps both. The loss was the **Claude adapter emitting only `Attachments[0]`** in the channel frame; fixed by "Multi-attachment surfacing" above. So the old hypothesis ("a same-poll-batch update is dropped before enqueue in poll.go") was wrong — no such drop exists.
  **Back-to-back TEXT half: RESOLVED 2026-06-20 — merge-perception, NOT a drop.** Confirmed by reading the path end-to-end: two text messages within the debounce window both enqueue via the identical producer path proven for the album half (`worker.go:179-209`, no pre-enqueue drop), both land in the debounce batch, and `mergeBatch` (`worker.go:340-350`) joins **every** non-empty `in.Text` with `"\n"` — the only skip conditions are a `nil` inbound or an `IsEvent()` (neither applies to a genuine text). Both texts reach the agent in one merged block; the historical "msgs 186/187 ~33µs apart, only one reached the agent" was that merged block read as a single message. Already locked by `TestMergeBatch_ConcatenatesText` (`internal/broker/debounce_test.go:21`: three back-to-back texts → `"first\nsecond\nthird"`). No code change needed. (Albums-as-a-feature remains forward in `ROADMAP.md`.)
  _Source: RESUME.md §FIX #1 (parked)._
- **FIX #2 (parked): typing indicator never shows while the agent works** — `in-progress` *(absorbed into the P0 "deterministic typing indicator", done; kept for repro history.)*
  Typing is **manual-only** (fires only on the `send_typing` MCP call); the 2026-05-08 rearch specced an auto-ticker that was never built. FIX: per-route typing ticker in the route worker. **Now absorbed into the P0 "deterministic typing indicator" item above** (programmatic relay, not LLM-driven) — keep this entry for the repro/history.
  _Source: RESUME.md §FIX #2 (parked)._
- **C3 name is FINAL — no rename** (Karthi 2026-06-14). The earlier rename plan is
  dropped. C3 = "Claude Code Claw"; an origin note lives in the README. Previously listed
  as a public-push blocker — no longer.

---

## Half-implemented / hidden gaps (invisible from the docs) — 2026-06-14 discovery list

These are real behaviors a reader can't discover from the docs. Doc fixes for several are being applied in this same 2026-06-14 cleanup (see "Doc fixes" below). *(Exceptions still live — `release <cwd>` stub, album assembly, STT gemini dead, `ListSessionsReq` CWD field — are forward in `ROADMAP.md`.)*

- `c3-broker release <cwd>` is wired but **stubbed** (errors) — was not flagged in user docs. (→ doc fix + P2 above.)
- **Auto typing-ticker missing** — typing is manual-only; docs imply it works during background work. (→ FIX #2 + doc fix.)
- **Album/media-group assembly missing** — relies on debounce; there's a real drop bug. Note: `TODO.md` previously called media-group "Skipped (intentional)" while `RESUME.md` documents it as a known bug — contradiction reconciled in this cleanup. (→ FIX #1 + doc fix.)
- **Codex adapter has no `detach` MCP tool** (Claude has 8 incl. detach; Codex has 9 = 8 shared − detach + `inbox` + `codex_forward`). Intentional-by-design but was undocumented. (→ doc fix F9.) **[Correction 2026-06-27: this tool-count line is stale — both adapters now expose 11 tools, and the in-memory `inbox` ring/tool is retired in favor of the durable queue + `fetch_queue`.]**
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

## Done — verified complete in this audit (2026-06-14)

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
