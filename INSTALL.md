# Install C3

> **For human users:** open a Claude Code session in any directory and paste:
>
>     follow https://github.com/karthikeyan5/c3/blob/main/INSTALL.md to install c3
>
> (or `follow /path/to/local/c3/INSTALL.md` if you've cloned locally)
>
> The agent does everything below. You'll only be asked for your Telegram
> bot token and chat ids during step 5.

> **For Claude Code (or any) agent reading this:** you are installing C3 for
> the user. Execute the numbered steps in order. Surface errors verbatim and
> stop on first failure. Don't paraphrase commands — run them exactly. Every
> step is idempotent (safe to re-run).

---

## 1. Verify Go is installed

```bash
go version
```

If "command not found": tell the user to install Go ≥1.22 from
https://go.dev/dl/, then re-run this install. Stop.

If the printed version is older than 1.22: tell the user to upgrade Go
and re-run. Stop.

## 2. Detect existing Python C3 broker (cutover case only)

If the user previously ran the Python POC under `~/arogara/c3/mvp`, that
broker may still hold the Telegram bot's `getUpdates` poller. Two pollers
on the same bot = 409 Conflict; the new Go broker can't run alongside.

Detect:

```bash
pgrep -fa 'mvp/broker.py' || true
```

If pgrep prints a pid, ask the user:

> "I found the old Python C3 broker running (pid \<X\>). To switch to the
> Go version I need to stop it — Telegram only allows one bot poller at a
> time. Stop it now? [yes/no]"

Only proceed on explicit "yes". Then:

```bash
pkill -f 'mvp/broker.py'
sleep 1
pgrep -fa 'mvp/broker.py' && echo "WARNING: still running" || echo "Python broker stopped."
```

If still running, tell the user the pid and ask them to kill it manually.

## 3. Add the marketplace and install the plugin

These are slash commands the USER runs in their Claude Code session. Tell
the user:

> "Run these three slash commands in this session, then tell me when
> they're done:
>
>     /plugin marketplace add karthikeyan5/c3
>     /plugin install c3@c3
>     /reload-plugins
>
> (For local installs replace `karthikeyan5/c3` with the absolute path
> to the cloned repo.)"

Wait for the user to confirm completion.

## 4. Build the binaries

The plugin shipped Go source, not pre-built binaries.

```bash
PLUGIN_ROOT=$(ls -d ~/.claude/plugins/cache/*/c3 2>/dev/null | head -1)
if [ -z "$PLUGIN_ROOT" ]; then
  echo "ERROR: c3 plugin not found in ~/.claude/plugins/cache — did step 3 complete?"
  exit 1
fi
SRC_ROOT=$(cd "$PLUGIN_ROOT/../.." && pwd)
if [ ! -f "$SRC_ROOT/go.mod" ]; then
  echo "ERROR: no go.mod at $SRC_ROOT"
  exit 1
fi
echo "Building from $SRC_ROOT (1–3 minutes on first run)..."
cd "$SRC_ROOT" && go install ./cmd/...
```

Then verify:

```bash
GOBIN_DIR=$(go env GOBIN)
[ -z "$GOBIN_DIR" ] && GOBIN_DIR=$(go env GOPATH)/bin
echo "Binaries installed to: $GOBIN_DIR"
for bin in c3-broker c3-claude-adapter c3-codex-adapter migrate-legacy; do
  if [ -x "$GOBIN_DIR/$bin" ]; then
    echo "  ✓ $bin"
  else
    echo "  ✗ $bin (missing)"
  fi
done
command -v c3-broker >/dev/null || echo "WARNING: $GOBIN_DIR is not on \$PATH"
```

If `c3-broker` isn't on PATH, tell the user:

> "Add `<GOBIN_DIR>` to your `$PATH` by appending this to your shell rc
> (`~/.zshrc` or `~/.bashrc`):
>
>     export PATH=\"<GOBIN_DIR>:$PATH\"
>
> Open a new terminal and re-run this install to verify."

…and stop.

## 5. Configure C3

Branch on existing config:

### 5a. Migrate from Python POC (preferred when applicable)

If `~/.claude/channels/telegram/.env` exists AND `~/.config/c3/mappings.json`
does NOT exist, migrate:

```bash
if [ -f ~/.claude/channels/telegram/.env ] && [ ! -f ~/.config/c3/mappings.json ]; then
  migrate-legacy \
    --env=$HOME/.claude/channels/telegram/.env \
    --config=$HOME/arogara/c3/mvp/config.json \
    --out=$HOME/.config/c3/mappings.json
fi
```

Verify:

```bash
[ -f ~/.config/c3/mappings.json ] && echo "Config exists." || echo "Config missing — run 5b."
```

If migration ran successfully, skip to step 6.

### 5b. Fresh setup (no prior Python POC)

If no prior config exists, run interactive setup:

```bash
c3-broker setup
```

This prompts on stdin for:
- Telegram bot token (from `@BotFather`)
- DM chat id (their Telegram user id; positive int — `@userinfobot` provides it)
- Default group name (e.g. "main")
- That group's chat id (negative `-100…`)

It validates the token via Telegram `getMe` BEFORE writing. On 401 or
network failure it refuses to write and surfaces the actual error.

Tell the user to follow the interactive prompts.

### 5c. Existing Go config

If `~/.config/c3/mappings.json` already exists, do nothing here. Tell the
user:

> "Existing Go config at `~/.config/c3/mappings.json` — keeping it. Run
> `c3-broker setup` manually if you want to overwrite (it asks before
> overwriting)."

## 6. Verify

```bash
c3-broker validate
c3-broker status
```

`validate` exits 0 on a parseable + valid mappings.json. `status` reports
broker liveness, socket path, channels, plugin states. The broker won't
be running yet — that's fine; the next CLI session will spawn it.

## 6.5. Enable channel notifications (REQUIRED)

Claude Code requires explicit opt-in before it surfaces
`notifications/claude/channel` from any plugin. **Without this step the
broker delivers messages successfully but the CLI never sees them.**

Read `~/.claude/settings.json` and ensure these two top-level keys are
present:

```json
"channelsEnabled": true,
"allowedChannelPlugins": [
  { "marketplace": "c3", "plugin": "c3" }
]
```

If the user already has `allowedChannelPlugins` with other entries,
keep them and add `c3` alongside. If there's a stale `c3-telegram` entry
from a prior install, replace it with `c3` (the plugin was renamed).

**Permission gotcha:** Claude Code's auto-permission classifier treats
`~/.claude/settings.json` edits as self-modification and may deny
write access. If your edit is denied, **surface the JSON snippet to the
user verbatim and ask them to paste it themselves**, then proceed once
they confirm.

## 7. Tell the user the install is complete

> "Installation complete.
>
> **Restart this Claude Code session with the dev-channels flag**:
>
>     claude --dangerously-load-development-channels plugin:c3@c3
>
> A plain `claude` will work for sending outbound, but **inbound channel
> notifications get silently dropped** — the broker delivers correctly,
> but Claude Code rejects channel notifications from plugins not opted-in
> via this flag (or via the production marketplace flow).
>
> Then in any project directory, type `attach` and confirm the proposal —
> the broker will create a Telegram topic named after that directory.
>
> Useful slash commands going forward:
>   `/c3-status`  — health check
>   `/c3-setup`   — re-run setup (overwrites config)
>   `/c3-build`   — rebuild after `git pull` in the source dir
>
> Day-to-day guide: `docs/USAGE.md`."

End.

---

## Manual install (without an agent)

The same steps run by hand work fine — copy each shell block above into a
terminal. The only interactive step is `c3-broker setup` which prompts on
stdin. See [`docs/INSTALL.md`](docs/INSTALL.md) for a more verbose
human-targeted version.
