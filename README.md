# C3 — Claude Code Claw

**C3** (pronounced "C-cubed") = **C**laude **C**ode **C**law

A Telegram multiplexer that connects multiple Claude Code CLI instances to a single Telegram bot, with routing by group topics, chat IDs, and users. Built entirely on Claude Code's ecosystem — uses its tools, skills, MCPs, and capabilities as-is.

---

> ### Current working version
>
> **The shipping implementation is the Go rewrite landed in this repo.** Source
> at [`cmd/`](cmd/) (binaries) + [`internal/`](internal/) (packages). The
> Telegram channel uses [`gotgbot/v2`](https://pkg.go.dev/github.com/PaulSonOfLars/gotgbot/v2);
> the only Python in the stack is the optional STT plugin shim that subprocesses
> a user-provided whisper handler.
>
> - **Spec:** [`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md)
>   (v5, locked).
> - **Install on a new machine:** [`docs/INSTALL.md`](docs/INSTALL.md). Short
>   form: `/plugin marketplace add karthikeyan5/c3`, `/plugin install c3@c3`,
>   `/c3-build`, `/c3-setup`, restart.
> - **Daily-use guide:** [`docs/USAGE.md`](docs/USAGE.md).
> - **Authoring extensions:** [`docs/PLUGINS.md`](docs/PLUGINS.md),
>   [`docs/CHANNELS.md`](docs/CHANNELS.md), [`docs/ADAPTERS.md`](docs/ADAPTERS.md).
>
> The original Python wrapper MVP lives in [`mvp/`](mvp/) — it ran in production
> from April through May 2026 and is what bootstrapped the spec. Once you've
> verified the Go broker works end-to-end on your machine, you can delete
> `mvp/` (or keep it for reference). The previous deviation banners in
> `RESUME.md` / `TODO.md` / `DECISIONS.md` are now retired; D009 records the
> Go rewrite as the active implementation.

---

## The Big Idea

C3 turns Claude Code into a multi-agent system. One Telegram bot, many CLI terminals, each handling different projects or contexts. A master CLI orchestrates the others. No custom agent framework needed — Claude Code already has everything (tools, file access, code execution, MCP plugins). C3 just adds the messaging layer.

Think of it as OpenClaw-like behavior, but running entirely on Claude Code CLIs.

## Architecture

```
Telegram Bot API
       |
  C3 Daemon
  (single process, owns bot token, polls Telegram, runs STT)
       |                |                |
  MCP-stub-1      MCP-stub-2      MCP-stub-3
  (stdio)         (stdio)         (stdio)
       |                |                |
  CLI-1            CLI-2            CLI-3
  (forge-on-forge) (project-Y)     (admin)
```

### Components

1. **C3 Daemon** — Single long-running process. Owns the bot token, polls Telegram (one poller per token — Telegram constraint). Holds the routing table. Runs STT pipeline for voice messages. Listens on a unix socket for MCP stub connections.

2. **MCP Stubs** — Thin stdio adapters, one per CLI instance. Each stub looks like a normal Telegram MCP plugin to Claude Code. Connects to the daemon via unix socket. Receives only messages routed to its assigned topics/chats.

3. **Routing Table** — Maps Telegram destinations to CLI instances:
   - Group topic -> CLI instance
   - Chat ID (DM) -> CLI instance
   - User ID -> CLI instance

### Message Flow

**Inbound (Telegram -> CLI):**
1. User sends message in a group topic or DM
2. Daemon receives it via bot polling
3. If voice: run STT pipeline, get transcript
4. Look up routing table: which MCP stub owns this topic/chat?
5. Forward to that stub via unix socket
6. Stub delivers to Claude Code as MCP channel notification

**Outbound (CLI -> Telegram):**
1. Claude Code calls the reply tool on its MCP stub
2. Stub forwards to daemon with chat_id/topic_id
3. Daemon sends via Telegram Bot API

## Routing Modes

- **Topic-based**: Create a Telegram group, enable topics. Each topic maps to one CLI instance. Most useful mode.
- **DM-based**: Route by user ID. User X's DMs go to CLI-1, User Y's to CLI-2.
- **Group-based**: Entire group (no topics) maps to one CLI.

## Key Features (Full Vision)

### Messaging
- Bidirectional routing (Telegram <-> CLI)
- STT built into daemon (not patched into plugin)
- Voice transcription with custom vocabulary
- Message deduplication and debouncing
- Attachment forwarding (photos, documents, audio)

### CLI Management
- Auto-spawn CLI terminals (tmux sessions or background processes)
- Manual CLI connection with ID-based registration
- Foreground any background CLI on demand
- Master/admin CLI that can configure and spawn others

### Topic Management
- Create topics via Telegram Bot API
- Auto-assign new topics to CLI instances
- List and manage topic-to-CLI mappings

### User Management
- Per-user access control
- Pairing flow for new users
- Master Telegram user ID for admin operations

## Inspiration: OpenClaw's Message Tool

Key features from OpenClaw's messaging system to consider:

- **Session-based routing** — deterministic routing by peer/channel/thread
- **Fire-and-forget vs wait modes** — configurable timeout for inter-agent messages
- **Multi-turn ping-pong** — controlled conversation depth between agents
- **Message deduplication** — prevents redundant processing from reconnects
- **Debouncing** — batches rapid messages (1.5-5s configurable)
- **Access control** — agents isolated by default, explicit opt-in for inter-agent communication
- **Thread-bound sessions** — agents post directly to threads without double-posting to parent
- **Sender identification** — cross-channel messages arrive with source prefix

We adapt these concepts for C3's Telegram-centric model. We don't need all-platform support — just Telegram done well.

## MVP Scope

**Build first:**
1. C3 daemon — polls Telegram, holds routing table, listens on unix socket
2. MCP stub — stdio adapter per CLI, connects to daemon
3. Route by topic_id within one group
4. Manual routing config (JSON file)
5. STT built into daemon
6. Basic tools: reply, download_attachment, react, edit_message

**Build next:**
- Topic creation via API
- CLI auto-spawn (tmux)
- Master CLI commands
- User access management
- Inter-CLI messaging (CLI-1 talks to CLI-2)

**Build later:**
- Auto-spawning with appropriate permissions
- Full user management system
- Dashboard/monitoring
- Persistent message history

## Tech Stack

- **Daemon**: Go — efficient, low memory/CPU, single binary (D006)
- **MCP stubs**: Go (if Go MCP SDK exists) or TypeScript (Bun) — must speak MCP stdio protocol
- **IPC**: Unix domain socket
- **Bot library**: Go Telegram bot library (telebot, gotgbot, or telegram-bot-api)
- **STT**: Existing stt-pkg pipeline (called from Go via subprocess)
- **Transport**: Pluggable interface — Telegram first, web chat and voice mode later (D007)

## Status

**2026-04-15**: Project created. Architecture designed. MVP scope defined.
