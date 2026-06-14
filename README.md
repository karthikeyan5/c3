# C3

A Telegram bridge for CLI coding assistants (Claude Code, Codex, anything that speaks MCP). One Telegram bot, many CLI sessions, with topic-based routing so each project gets its own thread on your phone.

Built around three ideas:

- **Multiplexer, not a wrapper.** One broker daemon owns the Telegram bot token and polls the API. N CLI instances connect over a unix socket as thin MCP adapters. No per-CLI token, no per-CLI poller, no fighting for `getUpdates`.
- **Routing as a first-class primitive.** Each project directory binds to one Telegram forum topic. `cd` into a project, run `claude` (or `codex`), the adapter auto-attaches to that project's topic. Multiple projects = multiple terminals = multiple topics, all on one bot.
- **Pluggable everywhere.** New CLI? Drop in an adapter. New transport (Slack, web chat)? Drop in a channel. New behavior (speech-to-text, custom commands)? Drop in a plugin. The seams are documented in [`docs/ADAPTERS.md`](docs/ADAPTERS.md), [`docs/CHANNELS.md`](docs/CHANNELS.md), [`docs/PLUGINS.md`](docs/PLUGINS.md).

## What you get on day one

- Send voice notes from your phone — they're transcribed (Gemini 3 Flash → Sarvam Saaras v3, fallback chain) and arrive in the CLI as `[Transcribed voice]: ...`. ElevenLabs Scribe v2 also bundled, opt-in via `--chain`.
- Reply from the CLI — the message lands in the right Telegram topic on your phone.
- Multiple terminals running simultaneously, each bound to its own topic. No double-replies, no message scattering.
- Claude Code and Codex on the same project — they coordinate via the broker so only one holds the topic claim at a time.
- Quote-replies, attachments, edits, reactions — all surface in the CLI as structured channel events.

## About the name

**C3** originally stood for **"Claude Code Claw"** — the project started as a way to remote-control a single Claude Code instance from Telegram. It's grown well beyond that:

- Multiple CLIs (Claude Code today, Codex today, future CLIs via the adapter interface)
- Multiple channels-ready (Telegram today, Slack/web chat/voice-mode as roadmap items)
- Plugin host for cross-cutting concerns (STT, future translation, scheduling, etc.)

"Claude Code" is no longer the whole story, but the name has stuck — **C3 is the final name** (no rename planned). Pronounced "C-cubed".

## Install

In any Claude Code session, paste:

```
follow https://github.com/karthikeyan5/c3/blob/main/INSTALL.md to install c3
```

The agent runs the playbook end-to-end. You'll be asked for a Telegram bot token (from `@BotFather`) and your group/DM chat ids during configuration. The whole install is ~5 minutes.

See [`INSTALL.md`](INSTALL.md) for the agent script (what the agent reads) and [`docs/INSTALL.md`](docs/INSTALL.md) for the human-readable walkthrough.

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
 (Claude) (Claude)           (Codex)
   │      │                  │
   ▼      ▼                  ▼
  CLI-1  CLI-2              codex
```

**Broker.** Single long-running Go process. One Telegram poller (Bot API constraint), N MCP adapters connected over a unix socket. Per-route serial executor (one goroutine per `RouteKey = {channel, chat_id, topic_id?}`) owns the inbound pipeline, outbound calls, placeholder state (typing is signalled on demand via `send_typing`), and debounce/merge. `flock` singleton with stale-pid recovery.

**Adapters.** Thin MCP stdio servers. Each looks like a normal MCP plugin to its CLI. Receives only messages routed to its attached topics/chats. Tools: `attach`, `detach`, `topics`, `reply`, `react`, `edit_message`, `send_typing`, `download_attachment`. Codex's adapter omits `detach` and adds `inbox` (drain buffered inbound) plus an env-gated `codex_forward` debug tool — see [`docs/ADAPTERS.md`](docs/ADAPTERS.md). Survives a broker bounce via exponential-backoff reconnect plus replay of the last successful attach (no manual re-attach needed).

**Channels.** Pluggable transport layer. v0.1 ships Telegram only (`internal/channel/telegram`, cleanroom Go via `gotgbot/v2` rc.34) with resilience hardening: 401 circuit-breaker, 429 retry-after, 409 conflict detection, persisted update-id watermark, outbound rate-limiting, per-update semantic dedup. The `Channel` interface is the seam for adding Slack/web/voice/etc.

**Plugins.** Five hook points: `OnInbound`, `OnVoiceReceived`, `OnOutbound`, `OnAttach`, `RegisterTools`. Built-in: STT — Go shim under `internal/plugin/builtins/stt/` plus a bundled Python pipeline at `plugins/c3/stt/` with a provider-chain runner. Override the handler via `mappings.json:plugins.stt.handler_path`.

**Config.** `~/.config/c3/mappings.json` (mode 0600, atomic-rewrite with one `.bak`). The bot token lives here; treat it like a password.

## Routing

- **Topic-based** (primary) — Telegram supergroup with topics enabled. Each topic binds to one CLI session. The natural way to start work is `cd <project-dir> && claude` — the adapter looks up the cwd's saved mapping and silent-claims; first time in a directory, you confirm the proposal and the topic gets created.
- **DM-based** — your personal DMs with the bot route to whichever CLI claims `dm`. Works anywhere, no project binding.
- **Group-based** — whole group without topics maps to one CLI. Useful for shared rooms.

## What's in the repo

| Path | What |
|---|---|
| [`cmd/c3-broker`](cmd/c3-broker) | The broker daemon + `setup` / `status` / `topics` / `validate` / `install-codex-shim` subcommands. |
| [`cmd/c3-claude-adapter`](cmd/c3-claude-adapter) | MCP stdio adapter for Claude Code. |
| [`cmd/c3-codex-adapter`](cmd/c3-codex-adapter) | Codex MCP adapter — WS-to-IPC bridge. |
| [`cmd/codex`](cmd/codex) | Codex CLI launcher — symlinked over `which codex` to route through the C3 bridge. |
| [`cmd/migrate-legacy`](cmd/migrate-legacy) | One-shot migrator from a legacy Python-prototype config layout into `~/.config/c3/mappings.json`. |
| [`internal/`](internal) | broker, channel/telegram, plugin host, mappings, IPC, MCP server. |
| [`plugins/c3/`](plugins/c3) | Plugin manifest + slash commands + bundled STT pipeline. |
| [`docs/USAGE.md`](docs/USAGE.md) | Day-to-day user guide. |
| [`docs/INSTALL.md`](docs/INSTALL.md) | Human-readable install walkthrough. |
| [`docs/COMMANDS.md`](docs/COMMANDS.md) | Cross-CLI verb spec — single source of truth for `/c3:*` semantics. |
| [`docs/ADAPTERS.md`](docs/ADAPTERS.md), [`CHANNELS.md`](docs/CHANNELS.md), [`PLUGINS.md`](docs/PLUGINS.md) | Authoring docs for extension points. |
| [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md) | **Locked spec (v5).** Source of truth for the v0.1 architecture. |
| [`docs/plans/`](docs/plans) | Phase-by-phase implementation plans (historical). |
| [`docs/research/`](docs/research) | Decision-context research notes (Go MCP SDK eval, stdio protocol). |
| [`DECISIONS.md`](DECISIONS.md) | Full decision log. |
| [`DEBUGGING.md`](DEBUGGING.md) | Where the logs live and how to read them. |
| [`RESUME.md`](RESUME.md), [`TODO.md`](TODO.md) | Current state and pending work. |

## Status

**v0.1.0** — first public release. Plans 1–7 + 9 functionally complete; pre-release UX bugs resolved (welcome on attach, stale-claim sweep, MCP-disconnect-on-resume hardening, install path math, SIGHUP-driven config reload). Live broker verified end-to-end against a Telegram bot; MCP exchange round-trip confirmed; voice STT bundled.

Roadmap (not yet started): rich-text + channel-capability architecture (next up), remote terminal-control, per-user access control, inter-CLI messaging, monitoring dashboard, web/voice channel impls. See [`ROADMAP.md`](ROADMAP.md) for the full prioritized list.

## License

MIT — see [`LICENSE`](LICENSE).
