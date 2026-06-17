# RESUME

## 🔵 CHECKPOINT — 2026-06-17 (rich-tables shipped · Telegram India IP-block · proxy plan)

**Mode:** CLI (Telegram is intermittently blocked from Karthi's network — see root cause — so CLI is the reliable channel). **Broker:** pid changes per restart; current one was built ~06:10 with rich-tables ON but does **NOT** have P1/P2 — **a broker restart is HELD until the proxy deploy** so it loads everything at once. Broker log: `~/.local/state/c3/broker.log`. Restart procedure: kill the live `c3-broker` (kill -9 if graceful hangs) + `setsid c3-broker` detached; it re-adopts claims.

**SHIPPED since the 2026-06-16 handoff (all on `master`, unpushed):**
- Completeness batch **P1–P7 + 4-lens review + fixes** (full polls, poll-read aggregate+on-close, expandable show-more, wide tables, inline buttons); race-clean.
- **Native rich-message tables** (Bot API 10.1 `sendRichMessage`/`RichBlockTable`): C3 routes detected GFM tables through `sendRichMessage` (raw via gotgbot `RequestWithContext`, no lib bump); **LIVE-VERIFIED on Karthi's Android — renders as a real native table.** `richTablesEnabled = true` (`0c13abf`). Monospace `<pre>` kept as fallback. This **solved the original wide-table complaint.**
- **Robustness for the block (committed, NOT yet live — needs the held broker restart):**
  - **P1 graceful-fail-notify** (`39f2fc6`): fetch-health state machine (quiet-night = healthy; only transport errors → DOWN; kills the false-positive watchdog spam) + out-of-band fan-out — desktop `notify-send` + "⚠️ SYSTEM" broadcast to CLI sessions + `c3-broker status` health line. Stops the silent failure.
  - **P2 base-url + failover** (`20f3217`): configurable Bot-API base URL (`C3_TELEGRAM_API_URL` env / `api_base_url` in mappings.json) via gotgbot `RequestOpts.APIURL` (no patch) + transient-only endpoint failover + fixed the hardcoded download URL. Default-unset = byte-identical to today. https-only validation; never `InsecureSkipVerify`; token never logged.

**ROOT CAUSE of the whole connectivity saga: Telegram is IP-range-blocked in India.** Karthi's network null-routes `149.154.0.0/16` + `91.108.0.0/16` + IPv6. Probed + confirmed (DNS + general internet fine; all TCP to Telegram IPs times out; NOT SNI-DPI). Caused the overnight "HEARTBEAT FAILED" spam, intermittent/missing inbound, and Karthi's "not attached?" confusion. **Definitive: no zero-infra escape** (api.telegram.org single-homed in the blocked range, not CDN-fronted; the dynamic/DoH/fallback resilience is MTProto-*client*-only, not the Bot API). → **a maintainer-owned reverse proxy is required.**

**PROXY PLAN (Karthi's decisions):**
- **GCP e2-micro VM**, free-tier, **NON-India region** — Mumbai forbidden (Indian network = also blocked). Hosts BOTH: nginx Bot-API reverse proxy (for C3) + **mtg** MTProto proxy (for Karthi's Telegram client apps).
- **Both on port 443, TWO subdomains** (Karthi's directive): a Bot-API subdomain + an MTProto subdomain. **(Actual subdomain values are kept OUT of this repo per Karthi — they live in local agent-memory + the gitignored `~/.config/c3/` + on the VM. Never commit them.)** **DNS = A records to a reserved static IP** (decided: NOT a CNAME — a reserved static IP is fully stable; the CNAME-to-`*.run.app` path is ruled out by Cloud Run's long-poll cost + mtg can't run on Cloud Run). Both subdomains → the one VM static IP; the VM SNI-routes `:443`. OPEN DETAIL to resolve at deploy: mtg fake-TLS makes the client present the *disguise* domain as SNI, complicating subdomain SNI-routing — fix by setting mtg's disguise = the MTProto subdomain, or give the VM a 2nd IP (decide via live mtg docs).
- **Agent runs the deploy itself** via a scoped, budgeted GCP key (Proctor pattern, `proctor/night-run/GCP-SETUP-INSTRUCTIONS.md`).
- **Token-safety:** strict TLS cert-verify on BOTH legs (C3→proxy Let's Encrypt; proxy→Telegram `proxy_ssl_verify on`). Owned proxy only; never a public/third-party proxy.

**DELIVERABLES written:**
- `docs/GCP-KEY-PROVISIONING.md` (committed) — for a full-access instance to mint an isolated project + scoped deployer SA + $15 budget + key → `~/.config/c3/gcp-proxy.env` (key at `~/.config/c3/gcp-proxy-sa.json`, **outside the repo**).
- `docs/DEPLOY-telegram-proxy.md` (**UNTRACKED draft, 8443 version — STALE**; rewrite for both-on-443/two-subdomains + the mtg SNI detail; becomes the agent's deploy script).

**NEXT CONCRETE ACTIONS (resume here):**
1. **Wait for Karthi** to provision the key (via GCP-KEY-PROVISIONING.md) → `~/.config/c3/gcp-proxy.env` exists → he says "key's ready."
2. **Rewrite** `docs/DEPLOY-telegram-proxy.md` for both-on-443 + two subdomains (resolve the mtg SNI detail via live mtg docs) as a runnable script.
3. **Deploy** (agent via the key): reserve static IP → create e2-micro VM (non-India region) → give Karthi the IP → he points 2 subdomains → install/config nginx (Bot-API + Let's Encrypt) + mtg (MTProto) on 443/SNI + firewall.
4. **Wire C3:** `C3_TELEGRAM_API_URL=https://<bot-api-subdomain>` → **rebuild + restart broker** (loads P1+P2 + proxy URL).
5. **Live-verify with Karthi:** a real phone→Telegram message flows through the proxy from the blocked machine; graceful-fail-notify fires on a simulated down; native table renders; Karthi pastes the mtg `tg://` link into his Telegram apps.

**STILL PARKED (not lost):**
- **Inbound forwarded-message empty text** — CONFIRMED cause: forwarded Bot-API-10.1 rich messages carry the body in `Message.rich_message`, which gotgbot rc.34 silently drops → empty `.Text` (NOT the bot-to-bot rule). Karthi wants forwarding to WORK — fix = augmented `getUpdates` decode shim parsing `rich_message` blocks → text (works on rc.34, no bump). Design in research outputs w4ocivw74/wb63isan9.
- Smoke-test tails: expandable show-more (visual confirm), inline-buttons callback (fresh-message tap — old ones stale-dropped).
- Deferred 10.x (deleteMessageReaction, rich HTML tables w/ borders/spans, `sendRichMessageDraft` streaming, guest mode) + the build-later defer list (albums, echo-by-file_id, underline/mention, outbound forwarding, location).
- Push decision pending (PII clean; unpushed).

**Memories added this session:** `feedback_no_canonicalize_offhand_terms`, `feedback_no_auto_switch_output_mode`, `feedback_fetch_live_docs_not_memory`.

---

## 🔴 PAUSE POINT — compaction handoff (2026-06-16)

**Resume map:** `cd ~/arogara/c3`, attach the c3 topic, then read this section +
`docs/specs/2026-06-16-capability-gaps-rootcause-safeguard.md` + `ROADMAP.md`. Karthi is
interacting via **Telegram (phone)**; a live rich-content smoke test is in progress.

**Where we are:** the channel rich-content + capability build (P0–P7) is shipped + committed
on `master` and was **live-smoke-tested on Telegram — 6/7 pass** (rich text, both polls,
photo, file, reaction, long-reply split-into-2). Typing works and is programmatic.

**GAPS the smoke test surfaced — the active work (Karthi wants these fixed):**
1. **Tables** — a wide table in a fenced ``` block WRAPS on Telegram Android (does NOT
   horizontally scroll), so columns fall apart. Need the correct way to render a
   horizontally-scrolling wide table. (Workflow `w31qfwmm6` researching → the 2026-06-16 spec.)
2. **Polls half-baked** — send-only: C3 can't READ poll results/votes (Telegram `poll` /
   `poll_answer` updates aren't handled by inbound), and only regular polls (no quiz /
   explanation / timed / full options). Karthi wants full polls incl. reading results.
3. **Expandable "show more" long text** — NOT implemented (Telegram expandable blockquote
   `<blockquote expandable>`). Karthi wants it.
4. **Typing cap** — correct + programmatic (broker route-worker pulses ~4s; arms on inbound
   once the session has replied; disarms on reply), but a 15-pulse/~60s safety cap stops it
   during genuinely long turns. Tune: keep alive while actively working; stop on reply or true idle.

**ROOT CAUSE (Karthi-requested):** the build pipeline had **no completeness gate**. The
original research DID surface the full Telegram surface, but design chose "minimal-pragmatic,"
scope-trims were approved, the 3 critique passes checked architecture + code-grounding (NOT
feature-completeness-vs-research), and the orchestrator never diffed shipped-vs-researched or
surfaced the deferral list for sign-off. Plus rendering claims (table h-scroll) were asserted
without live verification. Full analysis → the 2026-06-16 spec.

**SAFEGUARD (the completeness gate):** add a completeness gate to the `~/arogara/AGENTS.md`
build pipeline — after design, a capability-coverage matrix (every researched capability →
ship / defer / cut + rationale) surfaced to Karthi for sign-off BEFORE build; a
"completeness-vs-research" lens in the triple review; and live-verify any rendering claim.

**NEXT CONCRETE ACTIONS (post-compaction):**
1. Read `docs/specs/2026-06-16-capability-gaps-rootcause-safeguard.md` (gap report + root-cause
   + safeguard + fix-plan; written by workflow `w31qfwmm6` — commit it if uncommitted).
2. Implement the fixes via the design→harden→build→review pipeline **with the new completeness
   gate**: full polls (read via `poll_answer`/`poll` inbound surfaced to the agent + quiz/options/
   timed), expandable show-more blockquote, table h-scroll (per research), typing-cap tuning.
3. Add the completeness gate to `~/arogara/AGENTS.md`.
4. After fixes: `go install ./cmd/...`, restart the broker (see broker note), re-run the live
   Telegram smoke test with Karthi (incl. typing on turn 2, table, full polls, show-more).
5. Then terminal-control (design DECIDED: C3-brain + all-Go PTY stack + arbitrary TUIs);
   streaming after that.

**Broker note (papercut to fix):** the broker is a persistent flock-singleton daemon;
`/exit`+relaunch reconnects to the OLD broker (stale code after a rebuild). To load a new
binary you must `pkill c3-broker` — a running adapter does NOT auto-respawn it, and graceful
shutdown can hang ~60s (needed SIGKILL). The current broker was started manually after the
rebuild. `/c3:build`'s "relaunch auto-spawns a fresh broker" is misleading. FIX: `/c3:build`
should kill+respawn the broker or detect a binary-version mismatch.

**Other durable notes:** STT keys must now live in an env var or `~/.claude/stt.env` (the
OpenClaw scrub removed the `~/.openclaw` reads). Smoke-test assets: `/tmp/c3-smoke.png`,
`/tmp/c3-smoke.txt`. Everything committed on `master` (build P0–P7, review fixes,
SSHGate/proctor/ContestEval scrub, OpenClaw scrub `ddc0b7a`, ROADMAP/RESUME/spec updates,
AGENTS.md build-pipeline). The only pending file is the 2026-06-16 gap-report (workflow writing it).

---

## Update — 2026-06-15: channel-capability build SHIPPED

The P0 **channel rich-content + capability architecture** is built and committed on
`master` — 8 phases (P0–P7), designed via a 10-agent workflow, hardened by 3 critique
passes, triple-reviewed, review-fixes applied. **Shipped:** Telegram rich text (agent
writes markdown, C3 converts/escapes), media/file/poll sends (compressed photo vs original
file; albums sequential in v1), a per-channel capability manifest + agent guidance (Claude
+ Codex), deterministic broker-relayed typing, and a CI no-leak guard. **Deferred —
needs your call:** streaming of reasoning (R4) — Claude Code exposes no reasoning frame to
an MCP adapter; either reverse the Codex forwarder opt-out (Codex-only) or pivot C3 to an
SDK/Messages-API host (both CLIs). **Remaining:** a live Telegram round-trip smoke test
(needs your phone — checklist at the bottom of the spec). Spec:
[`docs/specs/2026-06-14-channel-capability-architecture.md`](docs/specs/2026-06-14-channel-capability-architecture.md).

**Next up:** terminal-control (settle design Q1/Q2/Q3 — the three questions are waiting on
the CLI) and the deferred items above. Note: the 2 flaky broker tests are now fixed (P0).

---

## Current handoff — 2026-06-14 (state reconciliation)

A full state/docs/code reconciliation ran today — a multi-agent sweep of every state
file, the `docs/plans` + `docs/specs`, the live Go code, and every past C3 session
transcript (incl. cross-project sessions). The consolidated, prioritized roadmap now
lives in **[ROADMAP.md](ROADMAP.md)** — that is the canonical "what's next." Nothing
from old notes or voice memos was dropped; previously-untracked ideas are flagged
`risk-of-loss: was-untracked` there.

**Machine note:** this checkout is Karthi's laptop at `/home/karthi/arogara/c3`. The
2026-06-01 handoff below was written on a **now-defunct server** that was being shut
down — treat its absolute paths and env-key locations (`gateway.systemd.env`, the
legacy bot config file, and that server's home directory) as server-specific, **not
valid on this machine**.

**Where things stand:** the code is mature and healthy — `go build ./...` and
`go vet ./...` are clean, and all packages test green **except 2 environment-flaky
broker tests** (a hardcoded-PID test-fixture defect, not a production bug — see
ROADMAP P2). Last commit 2026-06-04. The big open items are **unbuilt features, not
regressions**.

**Pick up next (priority order):**
1. **Terminal-control** (P0, the main feature) — blocked on design Q1/Q2/Q3 below;
   recommendation is to prototype the Go PTY stack. Settle the Qs first.
2. **Trusted-operator DM authorization** (P1) — spec written at
   `docs/specs/2026-06-14-trusted-operator-dm-authorization.md` (committed in this
   cleanup). Needs the §9 Phase-0 verification + §10 ratification before building.
3. **FIX #1 / FIX #2** (parked, P1) — inbound/album drop + typing auto-ticker; detailed
   repro in the 2026-06-01 handoff below.
4. **Pre-public-push blockers** (P1) — project rename + first-run install validation.

The detailed in-flight design notes (terminal-control architecture + Go libs,
permission-relay corrections, FIX #1/#2 repro) are preserved verbatim below.

---

## (Earlier) Session handoff — 2026-06-01 (Ram, picking up on laptop)

Context was on a server session that's being shut down; this section is the
full handoff so a fresh Ram on the laptop can continue without the chat
history. Three threads in flight: **two fixes** (parked) and **one main
feature** (the priority, mid-design). Plus environment notes.

### THE MAIN FEATURE (priority): remote terminal-control of coding agents from Telegram

**Vision (Karthi's words):** bring up a terminal connected to the DM, and
from that DM control + spawn other coding agents — "reliably control the
terminal, TUI or not." This is C3's original DNA (README: "started as a way
to remote-control a single Claude Code instance") and aligns with Phase 4
(inter-CLI messaging, live CLI view).

**Hard constraint discovered (verified — channels-reference doc + the
claude-code-guide agent):** this CANNOT ride C3's MCP channel surface. A
channel has EXACTLY three surfaces: (1) push events as `<channel>` blocks,
(2) a reply tool, (3) permission relay. There is NO slash-command injection
and NO runtime permission-mode change. (That's literally why `/compact` sent
over Telegram did nothing.) So terminal control is a **dedicated subsystem**,
not a channel bolt-on.

**Reference Karthi gave:** `github.com/helvesec/rmux` — Rust "tmux for the
agentic era." PTY-backed (Unix PTY / ConPTY), daemon + SDK + tmux-compatible
CLI, local Unix socket. Three superpowers that make it RELIABLE (vs naive
screen-scraping): `send-keys` (real keystroke injection → slash commands &
Shift+Tab mode toggles all work), structured cell-grid pane snapshots, and
`wait_for_text` (block until output appears → reliable idle/awaiting-input
detection). Detachable/persistent sessions. This is "path B done right."

**Proposed architecture:** C3 = Telegram control plane; rmux-or-equivalent =
terminal-control engine; broker orchestrates sessions. DM = controller,
sessions = controlled agents. (Adding a "pty-backed agent session" as a new
broker worker type fits the existing per-route-worker model.)

**Live research question — is there a Go equivalent to rmux?** (so we stay
single-language; C3 is Go, rmux is Rust). Answer: no single Go project IS
rmux, but the components are mature and assembling the needed subset in-
process is likely a BETTER fit than bridging a Rust daemon:
- PTY: `creack/pty` (canonical; ConPTY on Windows). Also `danielgatis/go-pty`, `ptyx`.
- send-keys: trivial (write to pty master).
- `wait_for_text`: `Netflix/go-expect` (or `ActiveState/termtest`, `google/goexpect`).
- structured snapshot (cell grid): VT emulator — `hinshun/vt10x` (pairs with go-expect), or newer `charmbracelet/x/vt` / `taigrr/bubbleterm`.
- session/detach/multiplex daemon: BUILD in the broker (no drop-in lib; `Gaurav-Gosain/tuios` is a full Go multiplexer but an end-user app, not embeddable).
- web/live view (optional): `kost/tty2web` or gotty.

**THE PENDING DECISION (resolve first on laptop):**
- Q1: C3-orchestrates-rmux, or rmux replaces part of C3? (Recommend: C3 stays the brain.)
- Q2: batteries-included Rust rmux (shell to its tmux-compat CLI from Go) **vs** single-language Go stack (`creack/pty` + `go-expect` + VT emulator) assembled as a new broker worker type?
- Q3: which agents must we control — only MCP-aware (Claude Code, Codex → clean cooperative path) or arbitrary TUIs too (forces the pty path)? Karthi's "TUI or not" leans toward arbitrary → pty.

**MY RECOMMENDATION:** prototype the **Go stack** (`creack/pty` + `go-expect`
+ a VT emulator) as a new broker worker type before committing to a cross-
language rmux bridge — keeps C3 coherent/single-language; the only thing
given up is rmux's pre-built session plumbing. The genuinely hard part —
rendering a constantly-redrawing TUI pane into a chat message — is IDENTICAL
either way: design it as **snapshot-on-idle** (use wait_for_text/quiescence
to send a clean snapshot when the agent stops and awaits input) + an on-
demand "show me the screen" command. Next concrete step: clone rmux, pin the
exact Go libs + their current maintenance state, and produce an MVP scope for
"pty-backed agent session in the broker."

**Aside:** for full phone-steering TODAY, native Claude Code **Remote Control**
exists (claude.ai + mobile; `claude remote-control --permission-mode <mode>`).
Separate from C3/Telegram — supported, zero build — if the goal is just
"control it when away from the laptop."

### Sub-feature (separate, assessed GO): permission relay

Forward Claude Code permission prompts to Telegram for remote approve/deny.
This IS the one supported remote-approval path (the channel's 3rd surface),
so keep it even though the broader terminal-control feature supersedes the
original framing. Assessed against real c3 code + official docs + the official
Telegram reference (`~/.claude/plugins/.../telegram/server.ts`). Buildable,
additive to the existing `claude/channel` path. Build prompt exists (Karthi
forwarded it); fold these CORRECTIONS in before building:
1. Verdict wire field is `behavior: "allow"|"deny"` (STRING), NOT `allow:bool`. Convert at the adapter when emitting `notifications/claude/channel/permission`. (Internal ipc op may carry a bool.)
2. There is NO cancel notification. Drop the "disable buttons when terminal answers first" sub-task — CC just silently drops a late verdict for an unknown id. Mirror the reference: on OUR tap, edit the message to show the outcome (prevents double-answer); accept stale buttons after a terminal-first answer.
3. `request_id` is exactly 5 letters `[a-km-z]` (no `l`). Use callback_data `perm:allow:<id>` / `perm:deny:<id>` + text fallback regex `/^\s*(y|yes|n|no)\s+([a-km-z]{5})\s*$/i`, lowercased.
Add: a sender-gating security note in the instructions string (anyone who can
reply can approve tool use); optional `perm:more:<id>` "See more" button
(input_preview is truncated to 200 chars). Capability to add:
`"claude/channel/permission": {}` in the Experimental map next to
`"claude/channel"` (main.go ~L646). Emit verdict via existing
`a.notifyTx.Notify(...)`. New ipc ops: OpPermissionRequest / OpPermissionVerdict.
Correlate request_id→session in the broker. CC here is v2.1.159 (floor for
permission relay is v2.1.81 — fine). **Caveat:** permission relay does NOT
catch auto-mode classifier HARD-DENIES (those never open a prompt) — only
normal permission prompts in default/acceptEdits mode.

### FIX #1 (parked): inbound delivery-drop + album/media-group drop

Symptom: Karthi sent two messages (186, 187) back-to-back; only 187 reached
the agent. Earlier, two files sent as a group → only one arrived.
- Broker log: 186 and 187 both logged `telegram: inbound` 33µs apart, but only
  187 got a `delivered` line; 186 produced NO delivered/skip/fail/drop line.
- Ruled OUT: the route-worker merge. The worker's debounce buffer APPENDS
  (worker.go:136) and `mergeBatch` joins all texts + concatenates all
  attachments LOSSLESSLY. A real burst of 186+187 would have arrived as ONE
  block containing both.
- Ruled OUT: dedup. `internal/channel/telegram/dedup.go` keys on
  `(update_id, chat_id, message_id, media_group_id)` — distinct message_ids
  never collide.
- CONCLUSION: 186 was dropped UPSTREAM of the route-worker queue, in the
  Telegram ingestion/dispatch layer (`internal/channel/telegram/poll.go` →
  `dispatchMessage` → update→Job enqueue). NEXT: read poll.go's dispatch path
  and find where a same-poll-batch update gets dropped before enqueue.
- Album bug: C3 has NO media-group assembly — it relies on the 1.5s debounce
  to merge album siblings (each album item is a separate Telegram message with
  a shared media_group_id). To pull the right log window I still need from
  Karthi: ROUGH TIME he sent the two-files-as-a-group (or whether it predates
  the 2026-06-01 session).
- Note: the 1.5s debounce was copied from the predecessor bot (Karthi confirmed).
- #3 reply-quote retrieval turned out to ALREADY WORK for text: Karthi's reply
  quoting the dropped msg 186 delivered 186's FULL text via the quote (186
  genuinely ended at "...silent-drop val" — not truncated). Karthi will test
  the reply-against-a-FILE case next.

### FIX #2 (parked): typing indicator not showing while agent works

- Root cause: C3 typing is MANUAL-ONLY — fires only when the agent calls the
  `send_typing` MCP tool (or as a side-effect of attach/validate_topic). There
  is NO auto ticker. The 2026-05-08 rearch design specced one ("in-flight
  counter, ticker start/stop" as per-route-worker state) but it was NEVER
  implemented (no `time.Ticker`/typing-state in `internal/broker/`).
- Telegram typing actions expire ~5s; with no ticker re-sending and the agent
  not calling send_typing during background work, Karthi sees nothing then a
  reply pops up.
- FIX: per-route typing ticker in the route worker — start a ~4s repeating
  chat-action when an inbound is delivered to the holder, stop on the agent's
  next outbound (reply/react/edit) for that route, or on timeout. Works for DM
  (thread 0) and topic via `SendTyping(chatID, threadID)` which already takes
  an optional threadID.

### Environment / ops notes (carry to laptop)

- Home was the now-defunct server's home dir (NOT `/home/karthi`); some docs still
  say the legacy path.
- STT: the `gemini-3-flash-openrouter → sarvam-saaras-v3` chain is running on
  the SARVAM fallback only. `gemini-3-flash-openrouter` is dead — no
  `OPENROUTER_API_KEY` anywhere it reads (env, gateway.systemd.env, the legacy
  bot config file at `models.providers.openrouter.apiKey`). Sarvam is actually
  MORE accurate on Karthi's accent/proper-nouns. Karthi wanted to copy the key
  from another instance of the predecessor bot on the box, but the auto-mode
  classifier blocked the cross-user credential read; needs CLI-level approval
  (Telegram auth doesn't satisfy the gate). Low priority — STT works.
- Separate, non-c3: `~/arogara/voice-notes/` holds a catalog (TODO.md) of 5
  ideas Karthi dictated (utopia white-paper, long-running-Forge mode, Continuum-
  suit master plan, "predict what Karthikeyan would say" corpus, + an idea-
  backlog sweep). Not lost; unrelated to the c3 work above.

---

## (Historical) Pre-public-push state — 2026-05-14

_Superseded by [ROADMAP.md](ROADMAP.md) (which carries the verified done-list) and the
2026-06-14 handoff above; kept for provenance. Note: "7 tools" below is now **8** on the
Claude adapter._

**Phase: pre-public-push hardening.** v0.1.0 is functionally complete
(Plans 1–7 + 9 + plugin host shipped 2026-05-08 → 2026-05-09). Subsequent
sessions on 2026-05-13 → 2026-05-14 have been about smoothing the UX
before sharing the repo publicly. Tests are green across the board.

### What's working live

- **Broker** (`c3-broker`) — runs as a daemon under flock at
  `$XDG_RUNTIME_DIR/c3.sock` (fallback `/tmp/c3-$UID.sock`). Singleton with
  stale-pid recovery. Subcommands: `setup`, `status`, `topics`, `validate`,
  `install-codex-shim`, `release` (release is wired but stubbed).
- **Telegram channel** — cleanroom Go via `gotgbot/v2` rc.34. getUpdates
  polling, outbound tools, attach proposal flow with cross-group
  disambiguation, debounce + mergeBatch (1.5s, 50-msg cap), cooldown-fallback
  (300s dedup per `RouteKey`). Resilience hardening: 401 circuit-breaker,
  429 retry-after, 409 conflict detection, persisted update-id watermark,
  outbound rate-limiting, per-update semantic dedup.
- **Claude Code adapter** (`c3-claude-adapter`) — end-to-end verified
  against a live Telegram bot. 7 tools, manual JSON-RPC framing for
  `notifications/claude/channel`, broker auto-spawn on first connect,
  exponential-backoff reconnect + replay of last successful attach.
  2026-05-14: added signal handlers, idle-startup watchdog (60s), and
  explicit exit-reason logging to handle Claude Code's `--resume`
  orphaned-spawn pattern (was causing "MCP plugin disconnected" until
  user manually `/mcp` reconnect).
- **Codex bridge** (`codex` + `c3-codex-adapter`) — Go launcher and adapter
  installed via `c3-broker install-codex-shim`. The adapter speaks Go broker
  IPC, supports `attach dm`, `reply`, `inbox`, and forwards inbound Telegram
  messages to the Codex app-server as turns.
- **STT plugin** — first-class bundled at `plugins/c3/stt/`. Gemini 3 Flash
  (via OpenRouter) → Sarvam Saaras v3 chain with vocabulary biasing.
  Handler-path resolution is the one rule (`plugins.stt.handler_path` →
  `${CLAUDE_PLUGIN_ROOT}/stt/stt-handler.py` → empty-with-marker). Long
  transcripts chunk correctly into the right topic (msg_thread_id
  threaded through every chunk).
- **Install plumbing** — `karthikeyan5/c3` marketplace, `c3@c3` plugin,
  `/c3:build`, `/c3:setup`, `/c3:status`, `/c3:attach`, `/c3:detach`,
  `/c3:topics`, `/c3:reload-config` slash commands. Single-line
  install via [`INSTALL.md`](INSTALL.md) at repo root.

### Pre-release fixes since v0.1.0 functional complete

All in [`TODO.md`](TODO.md):

- ✅ Welcome message on fresh attach (friendly tone, no PID,
  Replay-flag-suppressed on adapter replay, no time-based suppression
  after 2026-05-14 fix).
- ✅ CLI doesn't `cd` into named project (hard rule added to
  `~/arogara/AGENTS.md`).
- ✅ Default cwd resolves to launch-cwd/topic-name when subdir exists.
- ✅ Mappings registry refuses silent rebind of saved cwd → topic.
- ✅ MCP-disconnected-on-resume hardening (signal handlers, idle-startup
  watchdog, exit-reason logging).
- ✅ Pre-release doc audit (slash command syntax fixed everywhere;
  stale claims removed from README/USAGE/PLUGINS).

### What's NOT done

- **First-run install validation** (in flight, user-driven). Paste the
  install one-liner into a fresh Claude Code session, walk through
  `INSTALL.md` end-to-end, attach + round-trip a real Telegram message.
- **Project rename + clean migration** (raised by Karthi 2026-05-13,
  voice 1073). The name "C3" / "Claude Code Claw" no longer reflects
  the architecture — Codex + future channels + plugin extensibility
  make it broader than Claude Code. Plan: name → new repo dir → copy
  things over with clean namespace → fresh-install verify → push. Not
  started.
- **Phase 3 (access control)** — pairing flow, master Telegram user
  enforcement, per-user permissions. Not started.
- **Phase 4 (advanced)** — inter-CLI messaging, monitoring dashboard,
  persistent message history, daemon-side slash commands, web chat, voice
  mode, live CLI view. Not started.

## Where to resume

**The launch command matters** — to receive inbound channel notifications,
Claude Code must be started with:

```
claude --dangerously-load-development-channels plugin:c3@c3
```

A plain `claude` leaves notifications silently dropped on the receiving
side (broker log shows `delivered`, conversation sees nothing). See
[`CLAUDE.md`](CLAUDE.md) for why. (Eliminating this flag via a private
trusted plugin store is itself a roadmap item — ROADMAP P2.)

**Canonical roadmap:** [`ROADMAP.md`](ROADMAP.md). **Detailed checklist:**
[`TODO.md`](TODO.md).

## Key references

- **Roadmap (canonical):** [`ROADMAP.md`](ROADMAP.md)
- **Specs:** [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md)
  (v5, locked); [`docs/specs/2026-06-14-trusted-operator-dm-authorization.md`](docs/specs/2026-06-14-trusted-operator-dm-authorization.md)
  (DRAFT — awaiting ratification)
- **Plans:** [`docs/plans/`](docs/plans) — foundation, broker+ipc, channel+worker, plus
  the 2026-05-19 batch. (`2026-05-19-mcp-test-race-patch.md` is **SUPERSEDED** —
  `internal/mcp/` was removed in the go-sdk migration; do not resurrect.)
- **Decisions:** [`DECISIONS.md`](DECISIONS.md) — D009 (Go implementation
  landed) and D011 (Codex bridge in Go) are the most recent.
- **User guide:** [`docs/USAGE.md`](docs/USAGE.md). **Authoring:**
  [`docs/PLUGINS.md`](docs/PLUGINS.md), [`docs/CHANNELS.md`](docs/CHANNELS.md),
  [`docs/ADAPTERS.md`](docs/ADAPTERS.md). **Commands:** [`docs/COMMANDS.md`](docs/COMMANDS.md).
- **Research notes:** [`docs/research/`](docs/research/) — Go MCP SDK
  evaluation, stdio protocol notes (2026-04-15, context for D004/D006).
