# RESUME

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
