# RESUME

## Current state — 2026-05-09

**Phase: v0.1.0 functionally complete.** The Go rewrite landed during
2026-05-08 → 2026-05-09. Plans 1–6 + 9 + plugin host (5) shipped. Plan 7
(Codex bridge in Go) is deferred per D010. ~7100 lines, ~40 commits in the
`c3-v3` series, all tests green.

### What's working live

- **Broker** (`c3-broker`) — runs as a daemon under flock at
  `$XDG_RUNTIME_DIR/c3.sock` (fallback `/tmp/c3-$UID.sock`). Singleton with
  stale-pid recovery. Subcommands: `setup`, `status`, `validate`, `release`
  (release is stubbed).
- **Telegram channel** — cleanroom Go via `gotgbot/v2` rc.34. getUpdates
  polling, outbound tools, attach proposal flow with cross-group
  disambiguation, debounce + mergeBatch (1.5s, 50-msg cap), cooldown-fallback
  (300s dedup per `RouteKey`).
- **Claude Code adapter** (`c3-claude-adapter`) — END-TO-END VERIFIED
  against `@OCDWaterBot`. 7 tools, manual JSON-RPC framing for
  `notifications/claude/channel`, broker auto-spawn on first connect,
  reconnect-once with pending-tool-call wake-with-error.
- **Plugin host** — 5 hook points (`OnInbound`, `OnVoiceReceived`,
  `OnOutbound`, `OnAttach`, `RegisterTools`). STT plugin loads on boot,
  shells out to `~/.claude/channels/telegram/stt-handler.py`.
- **Install plumbing** — `karthikeyan5/c3` marketplace, `c3@c3` plugin,
  `/c3-build`, `/c3-setup`, `/c3-status` slash commands. Single-line install
  via [`INSTALL.md`](INSTALL.md) at repo root.
- **Migration** — `migrate-legacy` converts the Python POC's
  `mvp/config.json` + `~/.claude/channels/telegram/.env` triplet into the
  new `~/.config/c3/mappings.json`.

### What's NOT done

- **Live end-to-end against the Go broker** — the MCP exchange has been
  verified, but Karthi has not yet stopped the running Python broker, started
  the Go broker, and done a real round-trip Telegram send/receive in a normal
  workflow. The new `INSTALL.md` flow makes this trivial.
- **Codex bridge in Go** (Plan 7, D010) — `cmd/c3-codex-adapter` exists as
  scaffold (9 tools defined, WS forwarder stubbed); `cmd/codex/` launcher
  binary not written; `c3-broker install-codex-shim` subcommand not written.
  Python POC continues to work standalone but can't coexist with the Go
  broker (Telegram one-poller-per-token).
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

**Most likely next step (user-driven):** paste the install one-liner into a
fresh Claude Code session and walk through it as the first real user. This
flushes out any rough edges in the playbook before pushing the repo to
GitHub publicly.

```
follow /home/karthi/arogara/c3/INSTALL.md to install c3
```

Then `cd` into any project, type `attach`, confirm the proposal, and send a
message from Telegram to verify the round-trip.

**Build-side options when picking back up:**

1. **Plan 7 — Codex bridge in Go.** Spec section 12 of
   `docs/specs/2026-05-08-c3-rearch-design.md`. Three pieces: full
   `c3-codex-adapter` impl (the WS forwarder is currently stubbed), the
   `cmd/codex/` launcher binary that intercepts `codex` and shims to the
   adapter, and `c3-broker install-codex-shim` to symlink the launcher.
2. **`c3-broker release <cwd>`** runtime IPC op (currently stubbed). Lets
   a project free its attached topic without restarting the broker.
3. **Phase 3 (access control)** — pairing, master user enforcement.
4. **Phase 4 items** — pick from `TODO.md` against value/effort.

## Key references

- **Spec (locked):** [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md) — v5
- **Plans:** [`docs/plans/`](docs/plans) — 2026-05-08 foundation, 2026-05-09 broker+ipc, 2026-05-09 channel+worker
- **Decisions:** [`DECISIONS.md`](DECISIONS.md) — D009 (Go rewrite landed), D010 (Codex deferred) are the most recent
- **User guide:** [`docs/USAGE.md`](docs/USAGE.md)
- **Authoring:** [`docs/PLUGINS.md`](docs/PLUGINS.md), [`docs/CHANNELS.md`](docs/CHANNELS.md), [`docs/ADAPTERS.md`](docs/ADAPTERS.md)

---

## Appendix — historical session notes

### 2026-04-15 — Project created

Architecture designed (daemon + MCP stubs, topic routing, STT in daemon).
Decisions D001–D008 recorded. Original Python wrapper MVP scope set.

### 2026-04-15 → 2026-05-07 — Python wrapper MVP

Bun-plugin-wrapping + `patch_server.py` machinery. Ran in production on
`@OCDWaterBot` for ~3 weeks. Validated the architecture end-to-end and
surfaced the rough edges that drove the rearch (e.g. `*int64` map-key
pointer-identity bug, multi-channel data model, attach proposal flow,
cooldown-fallback). Lives in [`mvp/`](mvp).

### 2026-05-08 → 2026-05-09 — Go rewrite

Spec written and refined through 5 versions + 3 review rounds. Implementation
landed Plans 1–6 + 9 + plugin host. Live MCP exchange verified. Single-line
install playbook written. Codex bridge deferred per D010.
