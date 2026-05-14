# TODO

Status as of 2026-05-09. Locked spec is
[`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md).
Re-prioritize against the spec when picking next work.

## In flight (user-driven)

- [ ] **First-run validation of the Go broker.** Paste the install
  one-liner into a fresh Claude Code session, walk through `INSTALL.md`,
  then `cd` into a project, `attach`, and confirm a real Telegram
  round-trip. Surfaces any rough edges before public GitHub push.

## Pre-release UX bugs (surfaced 2026-05-13)

Surfaced during install/attach pilot. Must fix before the public push.

- [x] **Welcome message on attach.** Done 2026-05-14
  (`internal/broker/attach.go:sendWelcome`). Friendly tone, no PID, async,
  suppressed for adapter-replay re-attaches (broker bounce or conn-drop
  recovery doesn't spam the topic).
- [ ] **CLI doesn't actually `cd` into the named project.** When a user
  names a project at session start ("c3"), the agent reads that project's
  `CLAUDE.md` but never changes directory, so `pwd` still shows the
  launch root. Discrepancy: agent narrative says "I'm in c3" but shell
  state disagrees. Fix: either the agent must `cd` explicitly when
  identifying a project, OR document clearly that the agent stays put and
  uses absolute paths.
- [x] **Default cwd for a fresh topic = launch root, not project root.**
  Done 2026-05-14 (`internal/broker/attach.go:resolveAttachCWD`). The
  broker now refines launch_cwd → launch_cwd/topic_name when that
  subdirectory exists, so attaching to multiple topics from the same
  parent directory persists distinct mappings.
- [x] **Mappings registry allows duplicate default cwd across topics.**
  Done 2026-05-14 (`internal/broker/attach.go:persistMapping`). The
  broker logs a clear `cwd=... rebound from topic-X → topic-Y` warning
  when an upsert would overwrite an existing mapping with a different
  topic. Live claim still proceeds; only the saved-default behavior is
  highlighted in the log so the user sees the rebind.

## Completed follow-up (D011 — Plan 7: Codex bridge in Go)

Landed 2026-05-09. The Go broker now supports Codex through the Go launcher
and Go adapter.

- [x] **`cmd/c3-codex-adapter`** — WS forwarder implemented. Inbound C3
  messages are submitted to the Codex app-server as turns.
- [x] **`cmd/codex/main.go`** — launcher binary that intercepts the `codex`
  command and shims to the adapter (parallels how the Claude adapter is
  loaded as an MCP server).
- [x] **`c3-broker install-codex-shim`** — subcommand that symlinks the
  launcher into the user's PATH.

## Broker — small follow-ups

- [ ] **`c3-broker release <cwd>` runtime IPC op.** Currently stubbed. Lets
  a project free its attached topic without restarting the broker.
- [x] **Adapter auto-recover beyond reconnect-once.** Done 2026-05-09.
  `recoverBroker` (exponential backoff 0.5s → 30s, no give-up) +
  `replayLastAttach` (re-issues the last successful attach on reconnect)
  in `cmd/c3-claude-adapter/main.go`. A long-running session now survives
  a broker bounce without restarting Claude Code.

## Telegram resilience — OpenClaw parity

Surfaced 2026-05-09 after the polling-timeout bug fix. Source: OpenClaw's
`extensions/telegram/` (grammy-based). Most items landed 2026-05-09 in the
same session.

- [x] **Honor `parameters.retry_after` on Telegram 429.** Done in
  `internal/channel/telegram/poll.go` pollLoop (cap 60s).
- [x] **401 circuit-breaker.** Done — `authBreaker` in
  `internal/channel/telegram/resilience.go`; trips after 10 consecutive
  401s, sleeps 5min between probes, clears on any success.
- [x] **409 Conflict detection.** Done — pollLoop logs loud and `return`s
  when classifyError returns `errClassConflict`.
- [x] **Per-method timeout policy.** Done — `timeoutFor(method, longPoll)`
  in resilience.go, used via `requestOptsFor()` from every gotgbot call
  site. Long-poll budget is now `25s + 30s = 55s`.
- [x] **Error classification: transient-network vs permanent-API.** Done —
  `classifyError` + `isTransientNetworkError` in resilience.go. Permanent
  errors (other 4xx) feed the auth breaker; transient errors get the
  exponential backoff path; conflict and rate-limited get their own paths.
- [x] **Persisted update-id watermark.** Done — `offsetStore` in
  `internal/channel/telegram/offset_store.go` writes
  `$XDG_STATE_HOME/c3/telegram-offset.json` after each successful
  GetUpdates. pollLoop seeds `offset` from this on startup.
- [x] **Outbound rate-limiting.** Done — `rateLimiter` in
  `internal/channel/telegram/rate.go` using `golang.org/x/time/rate`.
  Global 30/sec, group 20/min, private 1/sec (burst 5). Wired into every
  outbound call in `outbound.go`.
- [x] **Per-update semantic dedup.** Done — `updateDedup` LRU in
  `internal/channel/telegram/dedup.go` (capacity 2000, TTL 5min).
- [ ] **Sequentialize per-chat handler dispatch.** **Already provided** by
  the per-route worker pool — `internal/broker/worker.go` runs one
  goroutine per `RouteKey = (channel, chat_id, *topic_id)`, serializing
  inbound + outbound for that route. Worth a tighter test (concurrent
  inbound interleaving) but no new code needed. Marked done.

Skipped (intentional):
- Transport-level fallback chain (IPv4-sticky / pinned IP) — overkill for
  our deployment shape.
- Media-group debounce (500ms hold-and-merge) — only matters for
  multi-photo album sends, not in any current flow.

## Phase 3 — User & Access Management (not started)

- [ ] **Per-user access control** — who can talk to which CLI.
- [ ] **Pairing flow** — new users get a pairing code, approved by master
  CLI or admin.
- [ ] **Master Telegram user** — admin who can configure the system from
  Telegram itself.

## Phase 4 — Advanced (not started)

- [ ] **Inter-CLI messaging** — CLI-1 sends a message to CLI-2 through the
  broker.
- [ ] **Topic creation via API** beyond the attach proposal flow —
  programmatic topic management for admins.
- [ ] **Monitoring dashboard** — connected adapters, message counts, STT
  stats.
- [ ] **Persistent message history** — context recovery across CLI
  restarts.
- [ ] **Slash commands handled in the broker** — `/status`, `/list`,
  `/route`, etc. without round-tripping to the LLM. OpenClaw-style fast
  ops.
- [ ] **Stream thinking / tool calls to Telegram** — research best UX
  first.
- [ ] **Web chat channel** — second `Channel` impl alongside Telegram.
  Magic-link URL flow. The pluggable channel layer is already in place
  (D007).
- [ ] **Voice mode channel** — continuous voice (record → send → read aloud).
  Driving / hands-free.
- [ ] **Live CLI view** — see what's happening in the CLI from the remote
  interface.

## Done — v0.1.0

Kept short for reference; full detail in the git history.

- Plan 1: repo skeleton (go.mod, cmd/, internal/, Makefile)
- Plan 2: mappings registry + `migrate-legacy` (27 tests)
- Plan 3: broker core + IPC; live daemon (27 broker/ipc tests)
- Plan 4A: Channel/Host interfaces + RouteWorker + WorkerPool
- Plan 4B: Telegram channel cleanroom Go (`gotgbot/v2` rc.34) — outbound
  tools, getUpdates, inbound conversion, OpToolCall + cooldown-fallback,
  attach proposal flow (8 tests), debounce + mergeBatch (7 tests),
  reconnect-once on adapter
- Plan 5: plugin host + STT plugin
- Plan 6: Claude Code MCP adapter — end-to-end live, 7 tools, manual
  framing for `notifications/claude/channel`
- Plan 9: install plumbing — marketplace.json, plugin.json, .mcp.json,
  `/c3-build`/`/c3-setup`/`/c3-status` slash commands, `c3-broker setup` /
  `status` / `validate` subcommands, root `INSTALL.md` single-line install
- Plan 10: doc pass — D009 added, README + RESUME + TODO rewritten to
  current state
