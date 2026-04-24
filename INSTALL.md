# Installing C3 on a new machine

**What you end up with:** a running C3 broker daemon and Claude Code sessions whose Telegram MCP plugin routes messages to it. Starting `claude` inside `~/arogara/<project>/` auto-attaches that session to the matching forum topic.

## The working version

The working implementation is [`mvp/`](mvp/) — Python wrapper over the official `claude-plugins-official/telegram` bun plugin. Every path below refers to files under `mvp/` unless noted otherwise.

The rest of this repo (`README.md`, `TODO.md`, `DECISIONS.md`, and the Go rewrite plan in `RESUME.md`) is design history. For setup, ignore it — the Python MVP is what ships and is what the author's own machine runs through.

## Prerequisites

- **Python 3.11+** — stdlib only for the broker/stub; no `pip install` for C3 itself. STT providers may want extras (`requests`, etc.) — see `mvp/stt/stt-pkg/README.md`.
- **bun** — the JS runtime for the upstream Anthropic plugin's `server.ts`. Install: https://bun.sh
- **Claude Code CLI** — installed and logged in. Verify with `claude --version`.
- **A Telegram bot** — chat with `@BotFather` → `/newbot` → grab the `HTTP API` token.
- **Your Telegram user id** — send any message to `@userinfobot` and note the `Id:` number.

## One-time setup

### 1. Clone the repo

Pick a stable path — C3 stores absolute paths in `topics.json` and the plugin's `.mcp.json`, so moving the tree later means re-patching them.

```
mkdir -p ~/arogara
git clone <repo-url> ~/arogara/c3
```

### 2. Install the upstream Anthropic Telegram plugin

Inside Claude Code:

```
/plugin marketplace add claude-plugins-official
/plugin install telegram@claude-plugins-official
```

This pulls `server.ts` into `~/.claude/plugins/cache/claude-plugins-official/telegram/<version>/`. `mvp/patch_server.py` applies the 7 C3 patches to that file idempotently every time the broker starts — you never run the patcher by hand.

### 3. Configure the bot token

```
mkdir -p ~/.claude/channels/telegram
cat > ~/.claude/channels/telegram/.env <<'EOF'
TELEGRAM_BOT_TOKEN=<paste token here>
EOF
chmod 600 ~/.claude/channels/telegram/.env
```

### 4. Bootstrap `access.json`

Allowlist yourself as the only DM-eligible user (adjust `<your-user-id>`):

```
cat > ~/.claude/channels/telegram/access.json <<'EOF'
{
  "dmPolicy": "allowlist",
  "allowFrom": ["<your-user-id>"],
  "groups": {},
  "pending": {}
}
EOF
chmod 600 ~/.claude/channels/telegram/access.json
```

Group approvals come later via `mvp/approve_group.py`.

### 5. Fix the stub path in the C3 plugin's `.mcp.json`

The plugin's MCP config ships with an author-specific absolute path. Edit it to match your clone:

```
# file: ~/arogara/c3/plugin/plugins/c3-telegram/.mcp.json
{
  "mcpServers": {
    "telegram": {
      "command": "python3",
      "args": ["<ABSOLUTE-PATH>/c3/mvp/stub.py"]
    }
  }
}
```

Replace `<ABSOLUTE-PATH>` with the full path to your clone — e.g. `/home/you/arogara`. `~` does not expand in `.mcp.json`.

### 6. Install the C3 plugin from this repo's marketplace

Inside Claude Code:

```
/plugin marketplace add ~/arogara/c3/plugin
/plugin install c3-telegram@c3
```

### 7. (Optional) Add STT keys for voice transcription

```
# file: ~/.claude/stt.env
GEMINI_API_KEY=...
SARVAM_API_KEY=...
# or ELEVENLABS_API_KEY=...
```

Providers chain in order — default is `gemini,sarvam`. See `mvp/stt/stt-pkg/README.md`.

### 8. First run

```
cd ~/arogara/c3 && claude
```

What should happen:

1. The stub starts, sees `/tmp/c3.sock` is absent, spawns `broker.py` detached (`c3-stub: spawned broker ...; waiting for socket` on stderr).
2. Broker acquires the flock on `/tmp/c3-broker.pid`, applies patches to `server.ts`, starts bun, installs STT symlinks.
3. bun logs `telegram channel: polling as @YourBot`.
4. Stub's `infer_topic_name()` sees `pwd` is `~/arogara/c3`, calls `attach_auto(name='c3')` — broker creates the forum topic in your Telegram group if missing.
5. Claude Code takes over.

Verify:

```
pgrep -af 'broker.py|bun server'
# one of each, broker's child is bun

ls -la /tmp/c3.sock /tmp/c3-broker.pid
# socket + pid file

readlink ~/.claude/channels/telegram/stt-handler.py
# -> ~/arogara/c3/mvp/stt/stt-handler.py
```

Logs: `/tmp/c3b.log` (broker stderr + bun output), `/tmp/c3-stub.log` (stub ops). Both are tmpfs; they reset on reboot.

## After setup

Ongoing operations (adding groups, creating topics, renaming, attaching) are covered in `mvp/README.md` under **Lifecycle: onboarding a new group or topic**.

## Troubleshooting

- **`bun server.ts` exits with 409 Conflict** — another `getUpdates` consumer holds the token (typically the stock Anthropic Telegram plugin running outside C3). Stop it, then restart the broker.
- **MCP plugin disconnects after reboot** — expected; `/tmp` is tmpfs. Start any fresh `claude` session and the stub will auto-spawn a new broker.
- **`c3-broker: another broker already holds /tmp/c3-broker.pid; exiting`** — another broker is live. `pgrep -af broker.py` to find it. `flock` auto-releases on process exit.
- **`PATCH BROKEN — <id>`** — upstream refactored `server.ts`. Read `mvp/PATCH_SPEC.md` for the patch's **Purpose / Final behavior / Detection**, then update the anchor/replacement in `mvp/patch_server.py`. A pristine upstream copy lives at `server.ts.c3-backup` next to the patched file.
- **Group messages silently don't arrive** — the group isn't in `access.json.groups`. Run `python3 ~/arogara/c3/mvp/approve_group.py '<t.me/c/...-url>'`.
- **Stub exits immediately with `cannot reach broker`** — broker failed to bind or crashed. Read `/tmp/c3b.log` for the actual error (token invalid, port in use, etc.).
