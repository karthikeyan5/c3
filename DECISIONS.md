# Decisions

## D001: Architecture — Daemon + MCP Stubs
**Date:** 2026-04-15
**Decision:** Use a single daemon process that owns the bot token and polls Telegram, with thin MCP stubs (one per CLI) connecting via unix socket.
**Why:** Telegram enforces one getUpdates poller per bot token. Can't have multiple plugins polling the same bot. The daemon centralizes polling, and stubs distribute messages.

## D002: Telegram Topics as Primary Routing
**Date:** 2026-04-15
**Decision:** Use Telegram group topics as the primary routing mechanism. One topic = one CLI instance.
**Why:** Topics provide visual separation in Telegram UI. Users can see which "terminal" they're talking to. Group creation is free and topics are lightweight.

## D003: STT Built Into Daemon
**Date:** 2026-04-15
**Decision:** Voice transcription (STT pipeline) runs in the daemon, not patched into each MCP stub.
**Why:** Centralizes STT — one place to maintain, no patching needed. The daemon transcribes before routing, so stubs always receive text.

## D004: Use OpenClaw Message Tool as Spec Reference
**Date:** 2026-04-15
**Decision:** Use OpenClaw's messaging system features as a reference spec, not its code. Adapt concepts (dedup, debouncing, session routing, access control) for our Telegram-centric model.
**Why:** OpenClaw has solved many of the same problems. No need to reinvent — but we only need Telegram, not all-platform support.

## D005: Project Name — C3 (C-cubed)
**Date:** 2026-04-15
**Decision:** Project named C3, standing for Claude Code Claw, pronounced "C-cubed".

## D006: Go for Daemon
**Date:** 2026-04-15
**Decision:** Write the C3 daemon in Go. MCP stubs may need Bun if no Go MCP SDK exists — research needed.
**Why:** Python and JS consume too much memory and CPU for a long-running daemon. Go is efficient, compiles to a single binary, and the team has proven it works well with Claude (entire web framework written in Go by Opus). Low resource footprint matters since this runs alongside multiple CLI instances.

## D007: Pluggable Transport Layer
**Date:** 2026-04-15
**Decision:** Design the daemon with a pluggable transport interface from the start. Telegram is first, but web chat (magic-link URLs) and voice mode are planned.
**Why:** Future use cases include browser-based sessions, voice-only mode (driving), and live CLI view. Architecting the transport boundary now avoids rewriting the core later.
