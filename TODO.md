# TODO

Status as of 2026-05-09. Locked spec is
[`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md).
Re-prioritize against the spec when picking next work.

## In flight (user-driven)

- [ ] **First-run validation of the Go broker.** Paste the install
  one-liner into a fresh Claude Code session, walk through `INSTALL.md`,
  then `cd` into a project, `attach`, and confirm a real Telegram
  round-trip. Surfaces any rough edges before public GitHub push.

## Deferred (D010 — Plan 7: Codex bridge in Go)

Until this lands, the Go broker supports Claude Code only. The Python POC
keeps working standalone for Codex but can't coexist with the Go broker
(Telegram one-poller-per-token).

- [ ] **`cmd/c3-codex-adapter`** — finish the WS forwarder. Scaffold +
  9 tool definitions are in place; the actual codex websocket bridge is
  stubbed.
- [ ] **`cmd/codex/main.go`** — launcher binary that intercepts the `codex`
  command and shims to the adapter (parallels how the Claude adapter is
  loaded as an MCP server).
- [ ] **`c3-broker install-codex-shim`** — subcommand that symlinks the
  launcher into the user's PATH.

## Broker — small follow-ups

- [ ] **`c3-broker release <cwd>` runtime IPC op.** Currently stubbed. Lets
  a project free its attached topic without restarting the broker.
- [ ] **Adapter auto-recover beyond reconnect-once.** Today, after one failed
  reconnect, the adapter is dead until the CLI session restarts. Background
  reconnect with backoff would let a long-running session survive a broker
  cycle without restarting Claude Code. Surfaced live during the 2026-05-09
  polling-bug debug — when the broker was killed for the rebuild, both
  attached sessions became permanently disconnected.

## Telegram resilience — OpenClaw parity

Surfaced 2026-05-09 after the polling-timeout bug fix
(`internal/channel/telegram/poll.go`). Source: OpenClaw's `extensions/telegram/`
(grammy-based). Priority order — small/medium-effort items first.

- [ ] **Honor `parameters.retry_after` on Telegram 429.** Parse the error,
  sleep that many seconds (cap 60s), then retry — instead of our generic
  exponential backoff. Telegram explicitly tells us the cooldown; ignoring
  it earns more 429s and risks bot deletion. (Small.)
- [ ] **401 circuit-breaker.** After N (e.g. 10) consecutive 401s on
  getUpdates / send, suspend polling globally and surface a clear
  "token invalid, fix it" error. Reset on any success. Today we'd
  retry-storm a revoked token. Ref:
  `extensions/telegram/src/sendchataction-401-backoff.ts`. (Small.)
- [ ] **409 Conflict detection.** Telegram returns 409 when two pollers
  race the same bot token. Detect specifically, log "another poller holds
  this token", and exit (don't backoff-retry — that just fights the other
  process). Real footgun for C3 because adapter auto-attach can spawn
  racing pollers. OpenClaw doesn't handle this either; we add ourselves.
  (Small.)
- [ ] **Per-method timeout policy.** Today our fix bumped *all* gotgbot
  calls to a 35s HTTP timeout. Right answer: getUpdates gets `pollTimeout +
  10s`; control calls (`getMe`, `setMyCommands`) get ~10s; sends get ~20s.
  Faster failure detection on the hot path. Ref: `bot-core.ts`
  `resolveTelegramRequestTimeoutMs`. (Medium.)
- [ ] **Error classification: transient-network vs permanent-API.** Walk
  the error chain collecting codes; only ETIMEDOUT / ENETUNREACH /
  EHOSTUNREACH / connection-refused trigger backoff. Permanent errors
  (HTTP 401, chat-not-found, etc.) propagate immediately instead of
  retry-storming. Ref: `fetch.ts` `FALLBACK_RETRY_ERROR_CODES`,
  `collectErrorCodes`. (Medium.)
- [ ] **Persisted update-id watermark.** Persist
  `highestCompletedUpdateId` to disk; on restart set
  `offset = persisted+1`. Today we either re-process or drop updates if
  the broker dies between "received" and "completed". Ref:
  `bot-update-tracker.ts` `highestAcceptedUpdateId` /
  `highestCompletedUpdateId` / `safeCompletedUpdateId`. (Medium.)
- [ ] **Sequentialize per-chat handler dispatch.** Per-chat-id mutex so
  two updates from the same chat run in-order, but different chats run in
  parallel. Already-partial in our per-route worker (which serializes per
  RouteKey); compare against grammy's `sequentialize(getKey)`. (Medium.)
- [ ] **Outbound rate-limiting** (`golang.org/x/time/rate` token bucket):
  30 req/s global, 20/min per group, 1/sec per private chat. Most 429s
  come from outbound storms (typing indicators, edit cascades). Ref:
  `@grammyjs/transformer-throttler`. (Medium.)
- [ ] **Per-update semantic dedup** (5-min TTL LRU on
  `update_id+chat+message_id+media_group_id`). Defense-in-depth on top of
  the watermark; catches Telegram's occasional repeat deliveries. (Small.)

Skipped:
- Transport-level fallback chain (IPv4-sticky / pinned IP) — almost
  certainly overkill for our deployment shape.
- Media-group debounce (500ms hold-and-merge) — only matters for
  multi-photo album sends.

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

Kept short for reference; full detail in
[`docs/.loop/state.json`](docs/.loop/state.json) and the c3-v3 git history.

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
- Plan 10: doc pass — D009/D010 added, deviation banners retired, README +
  RESUME + TODO rewritten to current state
