# TODO

## Phase 1: MVP

- [ ] **Design IPC protocol** — Define the message format between daemon and MCP stubs over unix socket. What gets sent, how routing info is encoded.
- [ ] **Decide tech stack** — Python vs TypeScript (Bun) for daemon. Bun required for MCP stubs (must speak MCP stdio protocol with grammy compatibility).
- [ ] **Build C3 daemon** — Single process: poll Telegram, run STT, route messages, listen on unix socket.
- [ ] **Build MCP stub** — Thin stdio adapter. Registers with daemon, receives routed messages, forwards outbound replies.
- [ ] **Routing table** — JSON config file mapping topic_id/chat_id -> stub instance ID. Hot-reloadable.
- [ ] **Port existing tools** — reply, download_attachment, react, edit_message. Same interface as official plugin.
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

## Research

- [ ] **MCP stdio protocol** — Understand the exact protocol for channel notifications, tool calls, tool results. Need to replicate what the official Telegram plugin does.
- [ ] **grammy topics API** — How to create/manage topics in Telegram groups via Bot API.
- [ ] **tmux scripting** — Programmatic session creation, window management, attach/detach.
- [ ] **Claude Code CLI flags** — What flags/env vars control plugin loading, MCP server config, channel wiring.
