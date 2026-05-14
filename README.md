# C3 — Claude Code Claw

**C3** (pronounced "C-cubed") = **C**laude **C**ode **C**law

A Telegram multiplexer that connects multiple Claude Code CLI instances to a
single Telegram bot. One broker, many adapters, routing by group / topic /
DM. Distributed as a Claude Code plugin; written in Go.

## Install

In any Claude Code session, paste:

```
follow https://github.com/karthikeyan5/c3/blob/main/INSTALL.md to install c3
```

The agent runs the playbook end-to-end. You'll only be asked for your bot
token and chat ids during configuration. See [`INSTALL.md`](INSTALL.md) for
the full agent script and [`docs/INSTALL.md`](docs/INSTALL.md) for the
human-readable walkthrough.

## What's in the repo

| Path | What |
|---|---|
| [`cmd/c3-broker`](cmd/c3-broker) | The broker daemon + `setup` / `status` / `validate` subcommands. |
| [`cmd/c3-claude-adapter`](cmd/c3-claude-adapter) | MCP stdio adapter for Claude Code. |
| [`cmd/c3-codex-adapter`](cmd/c3-codex-adapter) | Codex MCP adapter (scaffold; full impl deferred per D010). |
| [`cmd/migrate-legacy`](cmd/migrate-legacy) | One-shot migrator from a legacy Python-prototype config layout into `~/.config/c3/mappings.json`. |
| [`internal/`](internal) | broker, channel/telegram, plugin host, mappings, IPC, MCP server. |
| [`plugins/c3/`](plugins/c3) | Plugin manifest + slash commands shipped to users. |
| [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md) | **Locked spec (v5).** Source of truth. |
| [`docs/plans/`](docs/plans) | Phase-by-phase implementation plans. |
| [`docs/USAGE.md`](docs/USAGE.md) | Day-to-day user guide. |
| [`DEBUGGING.md`](DEBUGGING.md) | Where the logs live and how to read them. |
| [`docs/COMMANDS.md`](docs/COMMANDS.md) | Cross-CLI verb spec — single source of truth for `/c3:*` semantics. |
| [`docs/PLUGINS.md`](docs/PLUGINS.md), [`CHANNELS.md`](docs/CHANNELS.md), [`ADAPTERS.md`](docs/ADAPTERS.md) | Authoring docs for extension points. |

## Architecture

```
   Telegram Bot API
          │
   ┌──────┴───────┐
   │  c3-broker   │   single daemon, owns bot token, polls Telegram,
   │  (Go)        │   runs plugin host (STT), holds routing table,
   └──────┬───────┘   listens on $XDG_RUNTIME_DIR/c3.sock
          │
   ┌──────┼──────────────────┐
   │      │                  │
   ▼      ▼                  ▼
 adapter  adapter            adapter
 (Claude) (Claude)           (Codex — deferred)
   │      │                  │
   ▼      ▼                  ▼
  CLI-1  CLI-2              codex
```

**Broker.** Single long-running process. One Telegram poller (Bot API
constraint), N MCP adapters connected over a unix socket. Per-route serial
executor (one goroutine per `RouteKey = {channel, chat_id, topic_id?}`)
owns the inbound pipeline, outbound calls, placeholder + typing state, and
debounce/merge. flock singleton with stale-pid recovery.

**Adapters.** Thin MCP stdio servers. Each looks like a normal MCP plugin
to its CLI. Receives only messages routed to its attached topics/chats.
Tools: `attach`, `reply`, `react`, `edit_message`, `download_attachment`,
`topics`. Reconnect-once on broker drop with pending-tool-call wake-with-error.

**Channels.** Pluggable transport. v1 ships Telegram only
(`internal/channel/telegram`, cleanroom Go via `gotgbot/v2` rc.34).

**Plugins.** Five hook points: `OnInbound`, `OnVoiceReceived`, `OnOutbound`,
`OnAttach`, `RegisterTools`. Built-in: STT — Go shim under
`internal/plugin/builtins/stt/` plus a bundled Python pipeline at
`plugins/c3/stt/` (Gemini 3 Flash → Sarvam Saaras v3 chain, vocabulary-biased).
Override the handler via `mappings.json:plugins.stt.handler_path`.

**Config.** `~/.config/c3/mappings.json` (mode 0600, atomic-rewrite with one
`.bak`).

## Routing

- **Topic-based** (primary) — Telegram group with topics enabled. Each topic
  binds to one CLI session. The natural way to start work is `cd
  <project-dir> && claude` — the adapter auto-attaches to a topic
  named after the project, creating it via the attach proposal flow if it
  doesn't exist.
- **DM-based** — User X's DMs route to CLI-1.
- **Group-based** — Whole group (no topics) maps to one CLI.

## Status

**v0.1.0 functionally complete (2026-05-09).** Plans 1–7 + 9 done. Live
broker verified end-to-end against a Telegram bot; MCP exchange round-trip
confirmed; voice STT plugin loads its handler on boot. The Go Codex launcher
and adapter are installed via `c3-broker install-codex-shim`.

What's next: see [`RESUME.md`](RESUME.md) and [`TODO.md`](TODO.md).

See [`DECISIONS.md`](DECISIONS.md) for the full decision log.
