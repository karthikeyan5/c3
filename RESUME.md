# RESUME

## Current session handoff — 2026-06-01 (Ram, picking up on laptop)

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
- Note: the 1.5s debounce was copied from OpenClaw (Karthi confirmed).
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

- Home is `/home/claw` (NOT `/home/karthi`); some docs still say the legacy path.
- STT: the `gemini-3-flash-openrouter → sarvam-saaras-v3` chain is running on
  the SARVAM fallback only. `gemini-3-flash-openrouter` is dead — no
  `OPENROUTER_API_KEY` anywhere it reads (env, gateway.systemd.env, openclaw.json
  at `models.providers.openrouter.apiKey`). Sarvam is actually MORE accurate on
  Karthi's accent/proper-nouns. Karthi wanted to copy the key from another
  OpenClaw instance on the box, but the auto-mode classifier blocked the cross-
  user credential read; needs CLI-level approval (Telegram auth doesn't satisfy
  the gate). Low priority — STT works.
- Separate, non-c3: `~/arogara/voice-notes/` holds a catalog (TODO.md) of 5
  ideas Karthi dictated (utopia white-paper, long-running-Forge mode, Continuum-
  suit master plan, "predict what Karthikeyan would say" corpus, + an idea-
  backlog sweep). Not lost; unrelated to the c3 work above.

---

## Current state — 2026-05-14

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
[`CLAUDE.md`](CLAUDE.md) for why.

**Next concrete step:** finish first-run install validation, decide
whether to do the project-rename migration before or after the public
push.

## Key references

- **Spec (locked):** [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md) — v5
- **Plans:** [`docs/plans/`](docs/plans) — 2026-05-08 foundation,
  2026-05-09 broker+ipc, 2026-05-09 channel+worker
- **Decisions:** [`DECISIONS.md`](DECISIONS.md) — D009 (Go implementation
  landed) and D011 (Codex bridge in Go) are the most recent
- **User guide:** [`docs/USAGE.md`](docs/USAGE.md)
- **Authoring:** [`docs/PLUGINS.md`](docs/PLUGINS.md),
  [`docs/CHANNELS.md`](docs/CHANNELS.md),
  [`docs/ADAPTERS.md`](docs/ADAPTERS.md)
- **Research notes:** [`docs/research/`](docs/research/) — Go MCP SDK
  evaluation, stdio protocol notes (2026-04-15, context for D004/D006).
