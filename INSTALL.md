# Install C3

> **For human users:** open a Claude Code session in any directory and paste:
>
>     follow https://github.com/karthikeyan5/c3/blob/main/INSTALL.md to install c3
>
> (or `follow /path/to/local/c3/INSTALL.md` if you've cloned locally)
>
> The agent does everything below. You'll only be asked for your Telegram
> bot token and chat ids during step 4.

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

## 2. Clone the repo and add as a local marketplace

`/c3:build` needs access to the full Go source tree (`cmd/`, `go.mod`).
Claude Code's marketplace cache from a remote GitHub source only ships
the plugin subtree, not the build inputs. So the canonical install is:
clone the repo first, then point Claude Code at the local clone as a
marketplace.

Tell the user:

> "Pick a directory to keep the c3 source (e.g. `~/src`), then run:
>
>     mkdir -p ~/src && cd ~/src && git clone https://github.com/karthikeyan5/c3
>
> Then in this Claude Code session, run these three slash commands and
> tell me when they're done:
>
>     /plugin marketplace add ~/src/c3
>     /plugin install c3@c3
>     /reload-plugins
>
> Replace `~/src/c3` with wherever you cloned. The `git clone` location
> is permanent — the plugin's `/c3:build` reads source from there to
> compile binaries."

Wait for the user to confirm completion and capture the clone path
(we'll need it in step 3).

## 3. Build the binaries

The plugin shipped Go source, not pre-built binaries. After step 2's
marketplace add, `${CLAUDE_PLUGIN_ROOT}/../..` resolves to the cloned
repo:

```bash
PLUGIN_ROOT=$(ls -d ~/.claude/plugins/cache/*/c3 2>/dev/null | head -1)
if [ -z "$PLUGIN_ROOT" ]; then
  echo "ERROR: c3 plugin not found in ~/.claude/plugins/cache — did step 2 complete?"
  exit 1
fi
SRC_ROOT=$(cd "$PLUGIN_ROOT/../.." && pwd)
if [ ! -f "$SRC_ROOT/go.mod" ]; then
  echo "ERROR: no go.mod at $SRC_ROOT — looks like the marketplace points at a remote GitHub source, not a local clone. Go back to step 2 and 'git clone' first, then 'marketplace add' the clone path."
  exit 1
fi
echo "Building from $SRC_ROOT (1–3 minutes on first run)..."
cd "$SRC_ROOT" && go install ./cmd/...
```

Then verify all six binaries are present:

```bash
GOBIN_DIR=$(go env GOBIN)
[ -z "$GOBIN_DIR" ] && GOBIN_DIR=$(go env GOPATH)/bin
echo "Binaries installed to: $GOBIN_DIR"
for bin in c3-broker c3-claude-adapter c3-codex-adapter claude-shim codex migrate-legacy; do
  if [ -x "$GOBIN_DIR/$bin" ]; then
    echo "  ✓ $bin"
  else
    echo "  ✗ $bin (missing)"
  fi
done
command -v c3-broker >/dev/null || echo "WARNING: $GOBIN_DIR is not on \$PATH"
```

Note: a user who already has a separate Codex CLI on PATH will have
`codex` resolve to that one; the check above passes for either. The
codex check is "the C3 launcher binary exists in $GOBIN_DIR" — whether
PATH shadows it is sorted out in the optional codex-shim step below.

The `codex` binary is the Codex CLI launcher (only used if the user wants
Codex integration; symlinked into PATH by `install-codex-shim` in the
optional step below). `migrate-legacy` is a one-shot config migrator from
the Python-prototype layout — most users never run it.

If `c3-broker` isn't on PATH, tell the user:

> "Add `<GOBIN_DIR>` to your `$PATH` by appending this to your shell rc
> (`~/.zshrc` or `~/.bashrc`):
>
>     export PATH=\"<GOBIN_DIR>:$PATH\"
>
> Open a new terminal and re-run this install to verify."

…and stop.

## 4. Configure C3

If `~/.config/c3/mappings.json` already exists, validate it first:

```bash
c3-broker validate
```

If validation passes, tell the user:

> "Existing config at `~/.config/c3/mappings.json` — keeping it. Run
> `c3-broker setup` manually if you want to overwrite (it asks before
> overwriting)."

…and skip to step 5.

If validation FAILS (e.g. a half-corrupted carryover from a previous
install), surface the error verbatim and ask the user whether to
overwrite. On yes, back up the existing file (`cp
~/.config/c3/mappings.json ~/.config/c3/mappings.json.broken-$(date
+%s)`) and continue to interactive setup.

Otherwise, run interactive setup:

```bash
c3-broker setup
```

This prompts on stdin for:
- Telegram bot token (from `@BotFather`)
- DM chat id (the user's Telegram user id; positive int — `@userinfobot` provides it)
- Default group name (e.g. "main")
- That group's chat id (negative `-100…`)

It validates the token via Telegram `getMe` BEFORE writing. On 401 or
network failure it refuses to write and surfaces the actual error.

Tell the user to follow the interactive prompts.

### Speech-to-text (voice notes) — Python deps

Voice-note transcription runs a Python handler. **STT needs only system
`python3` + ffmpeg (`ffprobe`); no Python packages, no venv.** The provider
chain uses only the standard library (the Sarvam long-audio path is native
`urllib`), so a plain system `python3` works — even an externally-managed
(PEP 668) one. Override the interpreter via `mappings.json`
`plugins.stt.python` if you need a specific one.

Install **ffmpeg** (provides `ffprobe`, for audio-duration detection) via your
OS package manager. STT still works without it (REST-first), just less
precisely routed for long notes.

## 5. Verify

```bash
c3-broker validate
c3-broker status
```

`validate` exits 0 on a parseable + valid mappings.json. `status` reports
broker liveness, socket path, channels, plugin states. The broker won't
be running yet — that's fine; the next CLI session will spawn it.

## 5.5. Enable channel notifications (REQUIRED)

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
keep them and add `c3` alongside.

**Permission gotcha:** Claude Code's auto-permission classifier treats
`~/.claude/settings.json` edits as self-modification and almost always
denies the Write tool here. **When that denial fires, STOP. Do not retry.
Do not paraphrase. Print the literal block below to the user verbatim**
(both keys, including any other `allowedChannelPlugins` entries already
present), then ask "paste this into `~/.claude/settings.json` and tell
me when done":

```json
"channelsEnabled": true,
"allowedChannelPlugins": [
  { "marketplace": "c3", "plugin": "c3" }
]
```

Wait for the user's confirmation before proceeding.

## 6. (Optional) Enable Codex integration

Skip this step if the user only uses Claude Code. If they also use Codex,
run:

```bash
c3-broker install-codex-shim
```

This symlinks the C3 `codex` launcher into `~/.local/bin/codex` and into
every `~/.nvm/versions/node/*/bin/` so existing shells (which hash `codex`
to the NVM path) bypass NVM in favor of the launcher. It's idempotent;
re-running is safe. Tell the user to open a fresh terminal and verify with
`readlink $(which codex)` — it should point at `$GOBIN/codex`.

If they don't have Codex installed yet, skip — they can run this later
after `npm install -g @openai/codex` (or however they get Codex).

## 6.5. (Optional) Supervise the broker with systemd

By default the broker is spawned on demand by the first adapter and stays up as
a singleton. If you want it auto-restarted even when **no CLI session is open**
(so a crash can't leave inbound silently dead until your next launch), enable the
opt-in `systemd --user` unit:

```bash
mkdir -p ~/.config/systemd/user
cp docs/systemd/c3-broker.service ~/.config/systemd/user/
# If your GOBIN isn't ~/go/bin, edit ExecStart= first (go env GOBIN GOPATH).
systemctl --user daemon-reload
systemctl --user enable --now c3-broker.service
loginctl enable-linger "$USER"   # keep it running across logout
```

It coexists with adapter auto-spawn (the broker is a flock singleton). See
`docs/systemd/README.md` for details and uninstall.

**STT caveat:** a systemd-supervised broker has no `$CLAUDE_PLUGIN_ROOT`, so set
`plugins.stt.handler_path` in `~/.config/c3/mappings.json` to your cloned repo's
`plugins/c3/stt/stt-handler.py` or voice transcription silently turns off. (STT
needs only system `python3` + ffmpeg (`ffprobe`); no Python packages, no venv.)
Details in `docs/systemd/README.md`.

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
> Then in any project directory, run `/c3:attach` and confirm the proposal —
> the broker will create a Telegram topic named after that directory.
>
> Useful slash commands going forward:
>   `/c3:attach`         — claim a Telegram topic for this session
>   `/c3:detach`         — release the current claim
>   `/c3:topics`         — list known topics + claim state
>   `/c3:status`         — broker health check
>   `/c3:setup`          — re-run interactive setup (overwrites config)
>   `/c3:build`          — rebuild binaries after `git pull` in the source dir
>   `/c3:reload-config`  — broker re-reads mappings.json (SIGHUP, no restart)
>
> Day-to-day guide: `docs/USAGE.md`."

End.

---

## Manual install (without an agent)

The same steps run by hand work fine — copy each shell block above into a
terminal. The only interactive step is `c3-broker setup` which prompts on
stdin. See [`docs/INSTALL.md`](docs/INSTALL.md) for a more verbose
human-targeted version.
