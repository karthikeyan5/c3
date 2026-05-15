# Installing C3

Set-up steps for someone who has never run C3 before. Five minutes if everything goes well. If you've already used C3 and you're upgrading, skip to the bottom.

## Prerequisites

You need:

- **Go ≥1.22** on your `PATH`. C3 is built from source on first install. `go version` should print `go1.22` or newer.
- **A Telegram bot token.** Open Telegram, message `@BotFather`, send `/newbot`, follow prompts. You'll get a token shaped like `1234567:abcdefg...`. Keep it private.
- **A Telegram supergroup** where your bot will create forum topics. Make a new one (or repurpose one). Convert it to a supergroup if needed (Telegram does this automatically when you give it more than ~200 members or enable topics). Enable topics in group settings. Add your bot. Make the bot **an admin with `Manage Topics`** permission.
- **Your Telegram user id** for DMs. Message `@userinfobot` from your phone; it replies with your user id (a positive integer).
- **The supergroup's chat id.** Send any message in the group; the bot now sees it. Or use `@username_to_id_bot`-style helpers. Group chat ids are negative integers starting with `-100`.
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
mkdir -p ~/src && cd ~/src && git clone https://github.com/karthikeyan5/c3
```

Then inside Claude Code:

```
/plugin marketplace add ~/src/c3
/plugin install c3@c3
/reload-plugins
```

The clone location is permanent — `/c3:build` reads source from there to
compile, so don't move or delete it.

## Step 2: Build the binaries

Still inside Claude Code, run:

```
/c3:build
```

This is a slash command shipped by the plugin. It runs `go install ./cmd/...` from the plugin source dir. Five binaries land in `$GOBIN` (default `~/go/bin/`):

- `c3-broker` — the daemon
- `c3-claude-adapter` — Claude Code MCP server
- `codex` — the C3 launcher (will replace `which codex`)
- `c3-codex-adapter` — Codex MCP server
- `migrate-legacy` — one-shot migrator from a legacy Python-prototype config layout (only relevant if you have such a config)

Confirm `$GOBIN` is on your `PATH`. If `$GOBIN` is unset, Go installs to `$(go env GOPATH)/bin`. Add it:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

## Step 3: Configure C3

```
/c3:setup
```

The slash command interactively gathers:

- Your bot token (the `1234567:abc...` string).
- Your DM chat id (positive integer, your own user id).
- At least one group and its chat id (e.g. `main` → `-1001234567890`). You can add more groups later by editing `~/.config/c3/mappings.json`.
- (Optional) `master_user_id` for the future access-control feature; default is your DM id.

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

Optionally also remove the source clone (`rm -rf ~/src/c3` or wherever
you cloned in step 1).

The `~/.config/c3/mappings.json` lives outside both plugins; uninstalling the plugins doesn't touch it. Decide separately whether to keep it (you might reinstall later).

## Troubleshooting first-install issues

- **`/c3:build` fails with `command not found: go`** — install Go ≥1.22.
- **Build succeeds but `c3-broker` not on PATH** — `$GOBIN` isn't on `PATH`. See Step 2.
- **`/c3:setup` says it can't reach Telegram** — check the bot token. Try `curl https://api.telegram.org/bot<TOKEN>/getMe`; should return your bot's info.
- **Topic creation fails** — the bot isn't an admin with `Manage Topics` in the supergroup. Group settings → Administrators → your bot → toggle "Manage Topics" on.
- **`which codex` still resolves to NVM** — re-run `c3-broker install-codex-shim`, then `hash -r` (bash/zsh) or open a new terminal. Verify with `readlink $(which codex)`; it should point at `$GOBIN/codex`.
- **Voice transcription doesn't fire** — the broker log surfaces a `[plugin] stt: ...` line per voice message; check `~/.local/state/c3/broker.log`. Common causes: `~/.claude/stt.env` is missing or has no `OPENROUTER_API_KEY` / `SARVAM_API_KEY` (look for `stt: msg=N error ... stderr-tail=...`); `python3` not on the broker's PATH; `mappings.json:plugins.stt.enabled` is `false`. Failing voice messages now surface as `[STT FAILED: <reason>]` in the CLI rather than silent `(voice message)`. If you don't need voice, set `enabled: false`.
