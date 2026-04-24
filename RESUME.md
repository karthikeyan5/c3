# RESUME

> **⚠ DEVIATION NOTE (2026-04-22)** — The plan in this doc (and TODO.md, DECISIONS.md D006/D008) calls for a Go rewrite from scratch. We did not do that. Instead we shipped a **Python wrapper MVP** (`mvp/broker.py` + `mvp/stub.py` + `mvp/patch_server.py`) that wraps the official bun Telegram plugin rather than reimplementing it. It's running in production and is what this terminal is talking through right now. Phase 1 MVP items are largely done in spirit (topic routing, reply threading via patch P4, STT via the wrapped plugin, typing indicator, the four core tools).
>
> We have **not** updated the plan docs to reflect this. Leaving them as-is for now. When we're ready, the refresh needs: add D009 superseding D006/D008 (Python wrapper over Go rewrite), tick off completed Phase 1 items, reassess Phase 2/3/4 against the new architecture. **Handle later.**

## Current State
- Project: C3 (Claude Code Claw)
- Date updated: 2026-04-15
- Phase: Pre-build — research and planning complete, ready to code MVP

## Session 2026-04-15 — What Happened

### Project Created
- C3 conceived as a Telegram multiplexer for multiple Claude Code CLI instances
- Architecture designed: daemon + MCP stubs over unix socket
- Named C3 (C-cubed = Claude Code Claw)

### Research Completed
- MCP stdio protocol fully documented (JSON-RPC 2.0 format, tool schemas, notification structures)
- Go MCP SDK evaluated: official Tier 1 SDK exists (`github.com/modelcontextprotocol/go-sdk` v1.0.0)
- OpenClaw Message tool features catalogued for reference

### Decisions Made (D001-D008)
- D001: Daemon + MCP stubs architecture
- D002: Telegram topics as primary routing
- D003: STT built into daemon
- D004: OpenClaw as spec reference
- D005: Project name C3
- D006: Go for daemon and stubs
- D007: Pluggable transport layer
- D008: Official Go MCP SDK

### MVP Scope Defined
- Daemon, MCP stubs, topic routing, STT, basic tools, typing indicator
- Day-one requirements: message queuing (no lost messages), reply threading
- Full feature roadmap in TODO.md (4 phases)

## Where to Resume

1. **Start building MVP** — Set up Go project structure, initialize modules, start with the MCP stub (since that's the interface Claude Code talks to).
2. **Build order:** MCP stub (Go, official SDK) -> daemon core (unix socket listener, routing table) -> Telegram poller (grammy equivalent in Go) -> STT integration -> test with 2 CLIs

## Also Done This Session (Outside C3)

### STT Patch Issue Fixed
- Telegram plugin updated from 0.0.5 to 0.0.6, overwriting our STT voice patch
- Patch reapplied to 0.0.6
- Built SessionStart hook (`~/.claude/hooks/telegram-stt-patch-guard.py`) that auto-reapplies the patch on every CLI startup
- Discovered UserPromptSubmit hooks don't fire for MCP channel notifications

### Arogara Folder Restructure
- Shared CLAUDE.md and PERSONA.md moved to ~/arogara/ (parent of all projects)
- Project-specific CLAUDE.md stays in each project folder
