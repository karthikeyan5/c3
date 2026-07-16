# C3 on Claude Desktop (Windows)

The `c3-desktop-adapter` bridges C3 into **Claude Desktop** as an MCP server. Read the honest limitation first — it changes how you use it.

## What it is (and what it is not)

On Claude Desktop, C3 is a **pull bridge, not a push one.**

There is **no** "a Telegram message arrived → Claude speaks up." That live-render behaviour exists only in Claude Code (via its channel-notification support). Claude Desktop has no way for an MCP server to interrupt a chat, so:

- **Inbound is poll-only.** Telegram messages sent to your topic wait in C3's **durable on-disk queue**. Claude sees them only when *you ask it to check* — it calls `fetch_queue` and reads back whatever is waiting. Nothing is lost while you're away; it just doesn't surface on its own.
- **Outbound is on request.** Ask Claude to `reply` / `react` and it sends to Telegram.
- *(Optional)* an hourly **Claude Cowork Scheduled Task** can poll on a timer ("every hour, check my C3 messages and summarize"), which is the closest you get to push — a cron, not an interrupt.

If you want live "Claude speaks when Telegram pings," that's Claude Code with `--dangerously-load-development-channels`, not Desktop. See the top-level `README.md`.

## Prerequisites

Both the broker and the adapter run **locally on the Windows box** — a self-contained C3 node, no separate server:

- **`c3-broker`** built/installed and on `PATH`.
- **`c3-desktop-adapter.exe`** built/installed and on `PATH`.

Get the binaries either way:

- **Cross-compile** from a dev machine: `make build` builds every binary; `PLATFORMS` now includes `windows/amd64` and `windows/arm64`, so `make dist` produces a Windows release tarball containing `c3-desktop-adapter.exe`.
- **Build on the box:** with Go installed on Windows, `go install ./cmd/c3-broker ./cmd/c3-desktop-adapter`.

You also need a configured `~/.config/c3/mappings.json` (bot token, allowlist). Run `c3-broker setup` on the box, or copy over an existing config — but read the **Telegram single-consumer** caveat below before pointing it at a token another machine already polls.

## Install

### Option A — the installer (recommended)

```
c3-broker install-desktop
```

It writes/merges Claude Desktop's config at `%APPDATA%\Claude\claude_desktop_config.json` (macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`). It **merges** — every other MCP server and every other key in the file is preserved; only the `mcpServers.c3` entry is added/updated. If the file is present but not valid JSON, it refuses to touch it and tells you to fix or remove it.

It resolves `c3-desktop-adapter.exe` on `PATH` and writes its **absolute** path (Claude Desktop requires an absolute command path). If the binary isn't on `PATH` yet, it writes the bare name and warns you to edit it once you've built it. Re-running the installer **replaces the `c3` entry** (any `args`/`env` you hand-added to it are overwritten); every other server is untouched.

`--config <path>` (or `--path <path>`) overrides the target file on any OS — useful to stage a config on Linux, or to target an MSIX install path (below).

### Option B — hand-edit

Open `%APPDATA%\Claude\claude_desktop_config.json` and add the `c3` server. Use an **absolute** path with escaped backslashes:

```json
{
  "mcpServers": {
    "c3": {
      "command": "C:\\Users\\you\\.local\\bin\\c3-desktop-adapter.exe"
    }
  }
}
```

If the file already has other servers, add `c3` alongside them — don't replace the object.

### Then, either way

**Fully quit and restart Claude Desktop** — tray icon → **Quit**, not just closing the window. It reads the config and spawns MCP servers only at startup.

## First use

In a Desktop chat:

1. `attach name=<topic>` — claim (or create) your Telegram topic.
2. "check my messages" — Claude calls `fetch_queue` and reads back anything waiting in the queue.
3. Ask it to `reply` / `react` to respond.

Tool calls surface as a local **Allow / Always allow** approval in the Desktop GUI (see caveats).

## Caveats

> **Telegram is single-consumer — use a separate bot token on Windows.**
> Telegram's `getUpdates` allows **one** poller per bot token. If the Windows broker polls the **same** token as a broker on another machine, Telegram returns **409 Conflict** and inbound breaks on both. Give the Windows box its **own bot token** (a second bot from `@BotFather`), or route it through the C3 Telegram proxy. Do **not** share one token across two live brokers.

- **Microsoft Store (MSIX) install trap.** If Claude Desktop was installed from the Microsoft Store, edits to `%APPDATA%\Claude\` are **ignored** — the config that actually loads lives under:
  ```
  ...\Packages\Claude_*\LocalCache\Roaming\Claude\claude_desktop_config.json
  ```
  Re-run `c3-broker install-desktop --config "<that path>"`, or hand-edit the file there.
- **protocolVersion `2025-11-25`.** The adapter negotiates this MCP protocol version. After restart, confirm the `c3` tools appear in the chat. If they're refused, it's almost always a version-negotiation mismatch — check the adapter and Claude Desktop versions.
- **Approval is a local GUI tap.** Approving a tool call is a click in the Desktop app; "Always allow" is per-chat. There is **no** Telegram permission relay here (that Allow/Deny-over-Telegram flow is Claude Code-only).
- **Auto-update is not yet wired for Windows.** `c3-broker update` / the auto-updater cannot replace a running `.exe` on Windows (the OS locks it). Update the Windows box by **rebuilding from source** — `git pull`, then `go install ./cmd/c3-broker ./cmd/c3-desktop-adapter`, then fully quit + restart Claude Desktop. (From-source builds report version `dev` and never auto-update, so the checker stays quiet.)

## Verify on the box tomorrow

Everything here is **compile-verified on Linux** but **runtime-unverified on Windows**. Confirm on the actual box:

- [ ] The native `.exe` loads over stdio directly — no `cmd /c` wrapper needed in `command`.
- [ ] Which config path actually loads — the plain installer path vs. the MSIX `...\Packages\Claude_*\...` path.
- [ ] `c3-desktop-adapter` shows its tools in a Desktop chat after a full quit + restart.
- [ ] `attach` → `fetch_queue` → `reply` round-trips against a real topic.
- [ ] The broker singleton + unix-socket equivalent work under `%LOCALAPPDATA%\c3` on Windows.
- [ ] STT (if you use voice notes) still transcribes on the Windows box.
