# TODO

## Phase 1: MVP

- [ ] **Design IPC protocol** — Define the message format between daemon and MCP stubs over unix socket. What gets sent, how routing info is encoded.
- [ ] **Decide tech stack** — Go for daemon (D006). MCP stubs need to speak MCP stdio protocol — check if Go MCP SDK exists or if Bun is needed for stubs only.
- [ ] **Build C3 daemon** — Single process: poll Telegram, run STT, route messages, listen on unix socket.
- [ ] **Build MCP stub** — Thin stdio adapter. Registers with daemon, receives routed messages, forwards outbound replies.
- [ ] **Routing table** — JSON config file mapping topic_id/chat_id -> stub instance ID. Hot-reloadable.
- [ ] **Port existing tools** — reply, download_attachment, react, edit_message. Same interface as official plugin.
- [ ] **Typing indicator** — Show typing indicator in Telegram when the agent is working.
- [ ] **Port STT pipeline** — Move STT from patched server.ts into daemon. Use existing stt-pkg.
- [ ] **Test with 2 CLIs** — One group, two topics, two terminals. Verify bidirectional routing.

## Phase 2: CLI Management

- [ ] **CLI auto-spawn** — Spawn Claude Code in tmux sessions. Daemon creates session, starts CLI with MCP stub config.
- [ ] **CLI registration** — Manual CLI connects and registers with an instance ID. Daemon assigns routing.
- [ ] **Master CLI** — One CLI designated as admin. Can list instances, create topics, modify routing, spawn new CLIs.
- [ ] **Foreground/background** — Attach to any tmux session from terminal.

## Phase 3: User & Access Management

- [ ] **User access control** — Per-user permissions. Who can talk to which CLI.
- [ ] **Pairing flow** — New users get a pairing code, approved by master CLI or admin.
- [ ] **Master Telegram user** — Admin user who can configure the system via Telegram itself.

## Phase 4: Advanced

- [ ] **Inter-CLI messaging** — CLI-1 sends a message to CLI-2 through the daemon.
- [ ] **Topic creation via API** — Create topics programmatically, auto-assign to new CLI instances.
- [ ] **Monitoring dashboard** — Which CLIs are connected, message counts, STT stats.
- [ ] **Persistent message history** — Store messages for context recovery after CLI restart.
- [ ] **Slash commands** — Handle commands in the daemon without sending to the LLM. E.g. /status, /list, /route, /read <file>. Some run shell commands or read filesystem and return formatted output. Like OpenClaw's slash commands that bypass the LLM for fast operations.
- [ ] **Stream thinking/tool calls** — Stream agent's thinking and background tool calls to Telegram. Research what others are doing, find the best UX.
- [ ] **Web chat interface** — Magic-link URL to attach to a CLI session via browser. Pluggable transport layer so Telegram and web coexist.
- [ ] **Voice mode** — Continuous voice interaction (record -> send -> read aloud response). For driving/hands-free use.
- [ ] **Live CLI view** — See what's happening in the CLI terminal from the remote interface.

## Research

- [ ] **MCP stdio protocol** — Understand the exact protocol for channel notifications, tool calls, tool results. Need to replicate what the official Telegram plugin does.
- [ ] **Go MCP SDK** — Check if there's a Go implementation of MCP server (stdio transport). If not, evaluate Bun for stubs only.
- [ ] **grammy topics API** — How to create/manage topics in Telegram groups via Bot API.
- [ ] **tmux scripting** — Programmatic session creation, window management, attach/detach.
- [ ] **Claude Code CLI flags** — What flags/env vars control plugin loading, MCP server config, channel wiring.
- [ ] **Thinking/tool streaming UX** — Research what other Claude Code Telegram setups do. Find best patterns for streaming agent activity.
- [ ] **Pluggable transport design** — Architecture for swappable frontends (Telegram, web, voice). Define the interface between daemon core and transport adapters.
