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

## Step 1: Install the Claude Code plugin

Inside Claude Code:

```
/plugin marketplace add karthikeyan5/c3
/plugin install c3@c3
/reload-plugins
```

This clones the repo into `~/.claude/plugins/cache/c3/c3/<version>/`. Nothing's compiled yet.

## Step 2: Build the binaries

Still inside Claude Code, run:

```
/c3-build
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
/c3-setup
```

The slash command interactively gathers:

- Your bot token (the `1234567:abc...` string).
- Your DM chat id (positive integer, your own user id).
- At least one group and its chat id (e.g. `main` → `-1001234567890`). You can add more groups later by editing `~/.config/c3/mappings.json`.
- (Optional) `master_user_id` for the future access-control feature; default is your DM id.

**Token validation.** Before writing the file, `/c3-setup` calls Telegram's `getMe` with the token. If it 401s (bad token) or times out (no network), the command refuses to write `mappings.json` and prints the actual Telegram error. Re-run `/c3-setup` after fixing the token. This avoids the failure mode where a typo in the token gets silently saved and surfaces only on the next inbound poll.

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

Restart your Claude Code session. The first new session spawns the broker daemon and you're ready.

## Step 4 (optional): Enable Codex integration

Inside Codex:

```
codex plugin marketplace add github:karthikeyan5/c3
codex plugin install c3-codex
```

Then read the `SETUP.md` the plugin ships, or just have the agent run:

```
c3-broker install-codex-shim
```

This is a Go subcommand that idempotently:

1. Symlinks `~/.local/bin/codex` to `$GOBIN/codex`.
2. Walks `~/.nvm/versions/node/*/bin/` and creates the same symlink in each version's bin dir. **This is required, not optional** — long-running shells hash `codex` to the NVM path; without these symlinks, your existing terminals bypass the C3 bridge entirely.
3. Verifies `~/.config/c3/mappings.json` exists (it does if you ran `/c3-setup`).
4. Verifies the broker is reachable.
5. Prints a one-line audit of every symlink it created or confirmed.

Open a fresh terminal (or `hash -r` your existing one) and run `which codex`. It should resolve to `~/.local/bin/codex`. From now on every `codex` invocation goes through the C3 launcher → app-server → adapter chain. Use Codex normally.

## Step 5: Verify

In a fresh Claude Code session in some project directory:

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
/c3-build
```

State (`~/.config/c3/mappings.json`) is in XDG, not the plugin cache, so upgrades don't touch it.

For Codex side, re-run `c3-broker install-codex-shim` after `/c3-build` to refresh the symlinks against the new binary.

## Uninstalling

```
/plugin uninstall c3@c3                # removes the plugin from Claude Code
codex plugin uninstall c3-codex         # removes from Codex
rm ~/.local/bin/codex                   # restore your real codex
find ~/.nvm/versions/node -name codex -type l -delete   # remove NVM-side symlinks (CAUTION: only if they pointed at the C3 launcher)
rm -f "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" /tmp/c3-$UID.sock "${XDG_RUNTIME_DIR:-$HOME/.cache/c3}/c3-broker.pid"   # broker scratch files
rm -f /tmp/c3-codex-app-server.json     # codex launcher scratch
rm -rf ~/.config/c3                     # CONFIG; remove only if you don't want to keep mappings/topics
$GOBIN/c3-broker --uninstall-binaries   # removes binaries from $GOBIN (or just `rm $GOBIN/c3-* $GOBIN/codex` manually)
```

The `~/.config/c3/mappings.json` lives outside both plugins; uninstalling the plugins doesn't touch it. Decide separately whether to keep it (you might reinstall later).

## Troubleshooting first-install issues

- **`/c3-build` fails with `command not found: go`** — install Go ≥1.22.
- **Build succeeds but `c3-broker` not on PATH** — `$GOBIN` isn't on `PATH`. See Step 2.
- **`/c3-setup` says it can't reach Telegram** — check the bot token. Try `curl https://api.telegram.org/bot<TOKEN>/getMe`; should return your bot's info.
- **Topic creation fails** — the bot isn't an admin with `Manage Topics` in the supergroup. Group settings → Administrators → your bot → toggle "Manage Topics" on.
- **`which codex` still resolves to NVM** — re-run `c3-broker install-codex-shim`, then `hash -r` (bash/zsh) or open a new terminal. Verify with `readlink $(which codex)`; it should point at `$GOBIN/codex`.
- **Voice transcription doesn't fire** — the broker log surfaces a `[plugin] stt: ...` line per voice message; check `~/.local/state/c3/broker.log`. Common causes: `~/.claude/stt.env` is missing or has no `OPENROUTER_API_KEY` / `SARVAM_API_KEY` (look for `stt: msg=N error ... stderr-tail=...`); `python3` not on the broker's PATH; `mappings.json:plugins.stt.enabled` is `false`. Failing voice messages now surface as `[STT FAILED: <reason>]` in the CLI rather than silent `(voice message)`. If you don't need voice, set `enabled: false`.
