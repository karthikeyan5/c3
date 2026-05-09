# Decisions

> **2026-04-22 deviation note RETIRED 2026-05-09 by D009.** The April Python
> wrapper served its purpose (validated the architecture end-to-end) and is
> now superseded by the Go rewrite that landed during 2026-05-08–05-09. The
> Python POC under `mvp/` is preserved for reference until the user is
> satisfied with the Go binaries; eventual cleanup is at the user's
> discretion.

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

## D006: Go for Daemon and MCP Stubs
**Date:** 2026-04-15
**Decision:** Write the entire C3 system in Go — daemon and MCP stubs. Official Go MCP SDK exists (Tier 1, v1.0.0).
**Why:** Python and JS consume too much memory and CPU for a long-running daemon. Go is efficient, compiles to a single binary, and the team has proven it works well with Claude (entire web framework written in Go by Opus). Low resource footprint matters since this runs alongside multiple CLI instances.

## D008: Use Official Go MCP SDK
**Date:** 2026-04-15
**Decision:** Use `github.com/modelcontextprotocol/go-sdk` for MCP stub implementation.
**Why:** Official Tier 1 SDK, maintained by Anthropic/MCP org + Google. Supports stdio transport, tool registration, and custom notifications (needed for `notifications/claude/channel`). Maximum compatibility with Claude Code.

## D007: Pluggable Transport Layer
**Date:** 2026-04-15
**Decision:** Design the daemon with a pluggable transport interface from the start. Telegram is first, but web chat (magic-link URLs) and voice mode are planned.
**Why:** Future use cases include browser-based sessions, voice-only mode (driving), and live CLI view. Architecting the transport boundary now avoids rewriting the core later.

## D009: Go rewrite landed — supersedes April Python wrapper MVP
**Date:** 2026-05-09
**Decision:** The full v3 Go implementation per `docs/specs/2026-05-08-c3-rearch-design.md` is the active C3 codebase. It honors D006 (Go for daemon and stubs) and D008 (official Go MCP SDK), reactivates D007 (pluggable transport — multi-channel from day one in the data model), and adds a plugin extension system (D009-extension) the original direction implied but didn't formalize.

**What changed structurally vs the April MVP:**
- Single Go module. Four binaries: `c3-broker`, `c3-claude-adapter`, `c3-codex-adapter` (Plan 7 deferred), `migrate-legacy`.
- Telegram channel: cleanroom Go via `gotgbot/v2` rc.34. No more bun-plugin-wrapping or `patch_server.py` machinery.
- IPC: typed Go structs + Op constants + writer-mutex'd `*ipc.Conn`. Replaces the prose dispatch table the Python broker carried.
- Routing: value-typed `RouteKey` (fixes the `*int64` map-key pointer-identity bug the Python version dodged by treating 0 as General).
- Per-route serial executor: one goroutine per `(channel, chat_id, *topic_id)` owns inbound pipeline + outbound + placeholder/typing state.
- Mappings file: single XDG path `~/.config/c3/mappings.json` (mode 0600, atomic-rewrite with one-generation `.bak`). Replaces the old `mvp/config.json` + `mvp/topics.json` + `~/.claude/channels/telegram/.env` triplet.
- Multi-group, attach proposal flow with cross-group disambiguation, cooldown-fallback, debounce + cap, manual JSON-RPC framing for `notifications/claude/channel`.
- Plugin host with five hook points; STT is the only built-in plugin in v1, shelling out to the user's existing `~/.claude/channels/telegram/stt-handler.py`.

**Why now:** the wrapper was always a temporary scaffold. The April-through-May 2026 cycle proved the architecture; the Go rewrite is the distributable form.

## D010: Codex bridge deferred to a follow-up release
**Date:** 2026-05-09
**Decision:** The Codex bridge (`cmd/c3-codex-adapter/main.go` + `cmd/codex/main.go` launcher + `c3-broker install-codex-shim`) is left at scaffold-stub for v0.1.0. The Python POC at `mvp/codex` + `mvp/codex_supervisor.py` + `mvp/codex_stub.py` continues to work (it talks to the OLD Python broker, not the new Go broker; switching to Go means losing Codex temporarily).

**Why:** Karthi explicitly deferred the Codex piece during the rearch ("Codex adapter, we'll come back to it"). Plan 7 in the spec describes the full Go reimplementation; landing it is one focused multi-day effort, not a rounding-error addition to v0.1.0.

**What this means in practice:** until Plan 7 ships, a user installing C3 Go-side gets Claude Code integration only. The Python POC is untouched and continues to function for whoever wants Codex routing — they just can't have both Go-Claude AND Python-Codex on the same machine because of Telegram's one-poller-per-token constraint.

## D011: Codex bridge follow-up landed in Go
**Date:** 2026-05-09

**Decision:** Plan 7 is implemented in Go. The active Codex path is now `cmd/codex/main.go` launcher + `cmd/c3-codex-adapter/main.go` MCP adapter + `c3-broker install-codex-shim`.

**What changed:** `codex` launches interactive sessions through a local Codex app-server with `c3-codex-adapter` injected into both the app-server and visible TUI config. The adapter speaks the Go broker IPC, exposes Codex tools, and forwards inbound Telegram messages to Codex as app-server turns over WebSocket. `install-codex-shim` replaces the old MVP shim in `~/.local/bin` and Node-manager bin directories.

**Why:** The temporary MVP Python Codex bridge proved the app-server forwarding loop, but it could not coexist cleanly with the Go broker. The Go implementation restores the intended single-broker architecture while preserving the proven Codex forwarding behavior.
