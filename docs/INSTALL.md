# Installing C3

Set-up steps for someone who has never run C3 before. Five minutes if everything goes well. If you've already used C3 and you're upgrading, skip to the bottom.

## Prerequisites

You need:

- **Go ≥1.25** on your `PATH`. C3 is built from source on first install. `go version` should print `go1.25` or newer. (The `go` directive in `go.mod` pins `1.25.0`; older toolchains will fail the build or silently auto-download 1.25.)
- **A Telegram bot + group, set up per the checklist below.** Five minutes if you've done it before, ten if not. See [docs/research/2026-05-18-telegram-bot-setup.md](research/2026-05-18-telegram-bot-setup.md) for the "why" on each step.

  Use **Telegram Desktop, iOS, Android, or macOS** for the group-side steps — *not Telegram Web*. Web's Topics-enable and admin-rights UIs are incomplete.

  1. **Create the bot.** Message `@BotFather` → `/newbot` → pick a display name and a username ending in `bot`. Copy the HTTP token (`1234567:abcdefg...`). Keep it private.
  2. **Disable privacy mode.** Same chat: `/setprivacy` → pick your bot → `Disable`. Without this, the bot only sees messages that mention or reply to it. (Not exposed via the Bot API — must be done in BotFather.)
  3. **Create a Telegram group** (a regular group is fine; it auto-promotes to a supergroup when you turn Topics on).
  4. **Add your bot** to the group.
  5. **Enable Topics** in group settings. Do this *before* the next step — "Allow create topics" only appears in the admin checklist once Topics are on.
  6. **Promote the bot to admin** with these rights checked: **Manage Topics**, **Send Messages**, **Delete Messages**, **Pin Messages**. Everything else off.
- **Your phone (or any Telegram client)** for pairing. Setup discovers your user id and the group's chat id automatically: you send a short code to the bot in DM, and another in the group. No id hunting.
- **For Codex integration:** Codex CLI installed (typically via `npm install -g @openai/codex` or similar). The C3 launcher will detect it. NVM users: take note — long-running shells hash `codex` to your NVM path, so the install step below symlinks both `~/.local/bin/codex` and the NVM bin path.
- **For voice transcription (STT plugin):** the shipped first-class STT plugin runs a chained pipeline — **Gemini 3 Flash** (via OpenRouter) with **Sarvam Saaras v3** as the fallback. The plugin lives at `plugins/c3/stt/`; the broker subprocesses `python3` to run it. You need:
  - `python3` on PATH (3.11+ recommended).
  - API keys in `~/.claude/stt.env` — `OPENROUTER_API_KEY` for Gemini and `SARVAM_API_KEY` for Sarvam. The pipeline tries Gemini first, falls back to Sarvam if Gemini fails. Setting only one key works; the other provider gets skipped.
  - No model downloads — both providers are remote APIs.

  If you don't need voice, set `mappings.json:plugins.stt.enabled=false` and skip the API keys. You can swap in a custom handler (whisper, local, anything that matches the argv contract) by setting `plugins.stt.handler_path` to your own script — see `docs/PLUGINS.md`.

## Step 1: Clone the repo and install the Claude Code plugin

`/c3:build` (step 2) needs access to the Go source — `cmd/`, `go.mod`, the
whole tree. Claude Code's marketplace cache from a remote source only ships
the plugin subtree, so the install path is: clone the repo first, then point
Claude Code at the clone as a local marketplace.

```bash
git clone https://github.com/karthikeyan5/c3 ~/.local/share/c3
```

Clone into a **durable** location: `/c3:build` compiles from this directory on
every build, so a path that gets auto-cleared (`~/Downloads`, `/tmp`) will
break the marketplace later. `~/.local/share/c3` is the recommendation; any
stable dir works (`~/code/c3`, etc.) — just don't move or delete it afterward.

Then inside Claude Code:

```
/plugin marketplace add ~/.local/share/c3
/plugin install c3@c3
/reload-plugins
```

When `/plugin install` prompts for **user** vs **project** scope, choose
**user** — that makes C3 available in every Claude Code session, not just this
project.

## Step 2: Build the binaries

Still inside Claude Code, run:

```
/c3:build
```

This is a slash command shipped by the plugin. It runs `go install ./cmd/...` from the plugin source dir. Six binaries land in `$GOBIN` (default `~/go/bin/`):

- `c3-broker` — the daemon
- `c3-claude-adapter` — Claude Code MCP server
- `claude-shim` — the `claude` wrapper that auto-injects the dev-channels flag (symlinked into PATH by `install-claude-shim`; see Step 4.5)
- `codex` — the C3 launcher (will replace `which codex`)
- `c3-codex-adapter` — Codex MCP server
- `migrate-legacy` — one-shot migrator from a legacy Python-prototype config layout (only relevant if you have such a config)

Confirm `$GOBIN` is on your `PATH`. If `$GOBIN` is unset, Go installs to `$(go env GOPATH)/bin`. Add it:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

## Step 3: Configure C3

Run setup **inside Claude Code** so the agent can guide you through the whole
flow — bot token, Telegram pairing, and STT keys:

```
/c3:setup
```

Prefer this over the bare terminal. If you're not in a Claude Code session,
the standalone equivalent is `c3-broker setup` — it collects the same
`mappings.json` fields, just without the agent walking you through it.

The guided flow walks through:

- Your bot token (the `1234567:abc...` string).
- **DM pairing** — you send a short code to the bot in DM; setup discovers and records your user id (`dm_chat_id`, `master_user_id`, and the operator allowlist entry) automatically.
- **Group pairing** — you send another code in the (Topics-enabled) group; setup discovers and records the group's chat id. You can add more groups later with `/c3:pair` or by editing `~/.config/c3/mappings.json`.
- (Optional) voice-transcription API keys.

Completed steps are skipped on re-runs.

**Token validation.** Before writing the file, `/c3:setup` calls Telegram's `getMe` with the token. If it 401s (bad token) or times out (no network), the command refuses to write `mappings.json` and prints the actual Telegram error. Re-run `/c3:setup` after fixing the token. This avoids the failure mode where a typo in the token gets silently saved and surfaces only on the next inbound poll.

It writes `~/.config/c3/mappings.json` at mode 600 with this skeleton:

```json
{
  "schema_version": 1,
  "channels": {
    "telegram": {
      "bot_token": "1234567:abc...",
      "default_group": "main",
      "groups": {"main": {"chat_id": -1001234567890, "title": "My C3 Group"}},
      "dm_chat_id": 12345678,
      "master_user_id": 12345678,
      "topics": [],
      "debounce_ms": 1500
    }
  },
  "mappings": {},
  "plugins": {"stt": {"enabled": true}}
}
```

## Step 4: Enable channel notifications

Claude Code requires explicit opt-in before it surfaces
`notifications/claude/channel` from any plugin. **Without this step the
broker delivers messages successfully but the CLI never sees them.**

Open `~/.claude/settings.json` and add (merge if other top-level keys
exist):

```json
"channelsEnabled": true,
"allowedChannelPlugins": [
  { "marketplace": "c3", "plugin": "c3" }
]
```

If `allowedChannelPlugins` already has other entries, keep them and
append `c3` alongside.

Then **restart Claude Code with the development-channels flag**:

```
claude --dangerously-load-development-channels plugin:c3@c3
```

The plain `claude` command keeps `allowedChannelPlugins` enabled but
also gates local-marketplace plugins behind the dev flag (so the c3
adapter's `notifications/claude/channel` frames get silently dropped).
The official-marketplace plugin distribution flow doesn't need this
flag; we do, until c3 is published through Anthropic's marketplace.

## Step 4.5: Install the Claude wrapper

`/c3:setup` runs this automatically when invoked from a Claude Code
session — it is COMPULSORY under HostClaude per the locked 2026-05-18
design. The standalone command is for **manual** install, re-install
(after a `claude` binary upgrade clobbered the symlink), or
`--force` scenarios:

```
c3-broker install-claude-shim
```

The shim is a tiny launcher symlinked at `~/.local/bin/claude` that
transparently adds `--dangerously-load-development-channels plugin:c3@c3`
to every `claude` invocation. Without it you have to type the long flag
form by hand on every session start (which the maintainer prefers but
new users typically forget — leading to the silent-channel-drops failure
mode #18 was meant to close).

The most common manual-install hiccup is an existing non-shim
`~/.local/bin/claude` (often from NVM, npm, or a hand-edited symlink to
the real claude binary). The installer refuses to overwrite it without
`--force`. Verify a successful one-time install **without** `--force`
first — that path persists the resolved real-claude target to
`~/.config/c3/claude-shim.json` so the shim's fallback lookup chain
still finds your binary. Use `--force` only if the standard install
already wrote that config or you know the real-claude target by hand.

## Step 5 (optional): Enable Codex integration

If you also use the Codex CLI, run:

```
c3-broker install-codex-shim
```

This is a Go subcommand that idempotently:

1. Symlinks `~/.local/bin/codex` to `$GOBIN/codex`.
2. Walks `~/.nvm/versions/node/*/bin/` and creates the same symlink in each version's bin dir. **This is required, not optional** — long-running shells hash `codex` to the NVM path; without these symlinks, your existing terminals bypass the C3 bridge entirely.
3. Verifies `~/.config/c3/mappings.json` exists (it does if you ran `/c3:setup`).
4. Verifies the broker is reachable.
5. Prints a one-line audit of every symlink it created or confirmed.

Open a fresh terminal (or `hash -r` your existing one) and run `which codex`. It should resolve to `~/.local/bin/codex`. From now on every `codex` invocation goes through the C3 launcher → app-server → adapter chain. Use Codex normally.

## Step 6: Verify

In a fresh Claude Code session (started with the dev-channels flag from
step 4) in some project directory:

```
attach
```

The agent should respond with a proposal: "I'd create a topic '<dirname>' in group 'main'. Confirm?"

Confirm. The topic appears in your Telegram supergroup. Send a message into it from your phone. It should appear in the CLI as a `<channel>` block. Reply via the agent's `reply` tool. The reply lands in the topic.

If voice messages are working: send a voice note from your phone. After a couple of seconds it should arrive in the CLI as `[Transcribed voice]: <text>`. STT can take 1-3 seconds depending on length.

## Upgrading

Inside Claude Code:

```
/plugin marketplace update
/plugin upgrade c3@c3
/c3:build
```

State (`~/.config/c3/mappings.json`) is in XDG, not the plugin cache, so upgrades don't touch it.

For Codex side, re-run `c3-broker install-codex-shim` after `/c3:build` to refresh the symlinks against the new binary.

## Uninstalling

```
/plugin uninstall c3@c3                          # removes the plugin from Claude Code
pkill c3-broker                                  # stop the daemon
rm ~/.local/bin/codex 2>/dev/null                # restore your real codex (if you'd installed the shim)
find ~/.nvm/versions/node -name codex -type l -delete 2>/dev/null   # remove NVM-side symlinks (CAUTION: only if they pointed at the C3 launcher)
rm -f "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" /tmp/c3-$UID.sock "${XDG_RUNTIME_DIR:-/run/user/$UID}/c3-broker.pid" "${XDG_RUNTIME_DIR:-/run/user/$UID}/c3-broker.caps"   # broker scratch files
rm -f "/tmp/c3-codex-app-server-$UID.json"       # codex launcher scratch (per-uid path)
rm -rf ~/.config/c3                              # CONFIG; remove only if you don't want to keep mappings/topics
rm -f $(go env GOBIN)/c3-* $(go env GOBIN)/codex $(go env GOBIN)/migrate-legacy 2>/dev/null   # binaries
```

Optionally also remove the source clone (`rm -rf ~/.local/share/c3` or
wherever you cloned in step 1).

The `~/.config/c3/mappings.json` lives outside both plugins; uninstalling the plugins doesn't touch it. Decide separately whether to keep it (you might reinstall later).

## Troubleshooting first-install issues

- **`/c3:build` fails with `command not found: go`** — install Go ≥1.25.
- **Build succeeds but `c3-broker` not on PATH** — `$GOBIN` isn't on `PATH`. See Step 2.
- **`/c3:setup` says it can't reach Telegram** — check the bot token. Try `curl https://api.telegram.org/bot<TOKEN>/getMe`; should return your bot's info.
- **Topic creation fails** — the bot isn't an admin with `Manage Topics` in the supergroup. Group settings → Administrators → your bot → toggle "Manage Topics" on.
- **`which codex` still resolves to NVM** — re-run `c3-broker install-codex-shim`, then `hash -r` (bash/zsh) or open a new terminal. Verify with `readlink $(which codex)`; it should point at `$GOBIN/codex`.
- **Voice transcription doesn't fire** — the broker log surfaces a `[plugin] stt: ...` line per voice message; check `~/.local/state/c3/broker.log`. Common causes: `~/.claude/stt.env` is missing or has no `OPENROUTER_API_KEY` / `SARVAM_API_KEY` (look for `stt: msg=N error ... stderr-tail=...`); `python3` not on the broker's PATH; `mappings.json:plugins.stt.enabled` is `false`. Failing voice messages now surface as `[STT FAILED: <reason>]` in the CLI rather than silent `(voice message)`. If you don't need voice, set `enabled: false`.
