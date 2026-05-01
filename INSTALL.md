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

Pick a stable path — `topics.json` and the plugin's `.mcp.json` (both install-specific, gitignored, see steps 5 and 5b) bake in absolute paths derived from this clone location, so moving the tree later means re-editing them.

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

### 5. Create the C3 plugin's `.mcp.json` from the template

The plugin's MCP config is install-specific (it bakes in an absolute path
to your clone) and is gitignored. Copy the template and edit:

```
cp ~/arogara/c3/plugin/plugins/c3-telegram/.mcp.json.example \
   ~/arogara/c3/plugin/plugins/c3-telegram/.mcp.json
```

Then open `.mcp.json` and replace `<ABSOLUTE-PATH-TO-c3-CLONE>` with the
full path to your clone — e.g. `/home/you/arogara/c3`. `~` does not expand
in `.mcp.json`.

### 5b. Create `mvp/config.json` from the template

`config.json` pins the Telegram group new topics get created in. Required
on every install — without it the broker silently creates topics in the
wrong group as soon as it has ever seen more than one. Gitignored.

```
cp ~/arogara/c3/mvp/config.json.example ~/arogara/c3/mvp/config.json
```

You can leave the placeholder `-100XXXXXXXXXX` in place for now; you'll
fill in the real `group_chat_id` after you approve your first group (see
`mvp/README.md` → **Pin the active group**). The broker re-reads
`config.json` on every `attach_auto` call, so no restart is needed when
you edit it.

### 6. Install the C3 plugin from this repo's marketplace

Inside Claude Code:

```
/plugin marketplace add ~/arogara/c3/plugin
/plugin install c3-telegram@c3
```

### 7. Disable the upstream Telegram plugin

C3 needs the upstream plugin's `server.ts` *file* (the broker spawns it
directly and applies patches to it on every startup) — but it must not
also have the upstream plugin's MCP server registered in Claude Code.
Both polling `getUpdates` against the same bot token at once gives you
the `409 Conflict` error from the troubleshooting section; it's the
single most common way a fresh install silently half-works.

Inside Claude Code, open `/plugin`, pick `telegram@claude-plugins-official`,
and disable it. Leave `c3-telegram@c3` enabled — that's the one that
routes through the broker.

**Make sure the disable persists.** `/plugin` sometimes only flips state
in-memory for the current session. Check `~/.claude/settings.json` —
`enabledPlugins` should read:

```json
"enabledPlugins": {
  "telegram@claude-plugins-official": false,
  "c3-telegram@c3": true
}
```

If the upstream line is still `true` or missing, edit it by hand. A
session that re-enables it on next launch will immediately start racing
the broker again.

Verify: `/mcp` should list exactly one Telegram-related MCP server
(from `c3-telegram`) once plugins reload. The upstream plugin's cached
files under `~/.claude/plugins/cache/claude-plugins-official/telegram/`
stay on disk — that's intentional; the broker still reads `server.ts`
from there.

### 7b. Opt in to channel notifications

Inbound Telegram messages arrive in your session as `<channel>` blocks
via the MCP `notifications/claude/channel` mechanism. Claude Code gates
this behind an off-by-default setting. Add to `~/.claude/settings.json`:

```json
"channelsEnabled": true
```

Without this, the broker still receives messages and the stub still
forwards the JSON-RPC notifications — but Claude Code drops them on the
floor instead of rendering them. Symptom: `/tmp/c3-stub.log` shows
`op=inbound` entries, yet your session stays silent.

### 8. (Optional) Add STT keys for voice transcription

```
# file: ~/.claude/stt.env
GEMINI_API_KEY=...
SARVAM_API_KEY=...
# or ELEVENLABS_API_KEY=...
```

Providers chain in order — default is `gemini,sarvam`. See `mvp/stt/stt-pkg/README.md`.

### 9. First run

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
