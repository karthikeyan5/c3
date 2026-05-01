# C3 MVP — Telegram multiplexer over the official plugin

**First-time setup on a new machine?** → [`../INSTALL.md`](../INSTALL.md).
This file documents the running system and day-to-day operations.

C3 is a thin broker that sits between the official Telegram Claude-plugin
(`bun server.ts`) and N Claude Code terminals, routing by forum topic.
The plugin is the only thing that talks to Telegram — we wrap it, we don't
fork it, so features and updates flow through automatically.

```
 bun server.ts  <-- stdio -->  broker.py  <-- unix sock -->  stub.py  <-- stdio -->  Claude Code
  (upstream)                    (ours)                        (ours)
```

## Files

- `broker.py`       — spawns bun once, fans its MCP notifications out to stubs by `(chat_id, message_thread_id)`, forwards stubs' tool calls back. Applies `patch_server.py` idempotently on startup.
- `stub.py`         — MCP stdio server, one per Claude Code process. Asks broker for bun's tools list / capabilities / instructions and relays them transparently.
- `c3-attach`       — launcher. Default behavior: takes `basename(pwd)` as the topic name, tells broker to attach-or-create, writes `.mcp.json`, execs `claude`.
- `patch_server.py` — idempotent patches to the plugin's `server.ts`. Each patch has a stable id; `PATCH_SPEC.md` documents what each one must achieve, so a future agent can re-derive the patch against an upstream refactor without spelunking git history.
    P1. inbound `notifications/claude/channel` meta gains `message_thread_id`
    P2. `reply` tool accepts `message_thread_id` and forwards it to Telegram (schema + body + sendMessage + sendFile)
    P3. disables bun's orphan-watchdog that spuriously kills the process under our broker
    P4. inbound meta gains Telegram quote-reply context (`reply_to_message_id`, `reply_to_user`, `reply_to_text`) so Claude sees which earlier message a user is replying to
    P5. voice handler passes `message_thread_id` to `stt-handler.py` so long-transcript chunks echo to the right topic instead of leaking to General
- `PATCH_SPEC.md`  — per-patch purpose, final behavior, and detection rules. Read this first if `patch_server.py` reports `PATCH BROKEN — <id>` on startup.
- `stt/`            — bundled voice-STT handler (`stt-handler.py` + `stt-pkg/` with pluggable providers). The official plugin spawns this on voice messages at the hardcoded path `~/.claude/channels/telegram/stt-handler.py`.
- `install_stt.py`  — symlinks `stt/stt-handler.py` and `stt/stt-pkg` into `~/.claude/channels/telegram/` on broker startup. Any pre-existing real files at the destination are backed up to `.pre-c3/<timestamp>/` once. Idempotent.
- `topics.json`     — **install-specific (gitignored).** Auto-written registry of every `(chat_id, topic_id, name)` the broker has seen or created. Created on first use; no template needed.
- `config.json`     — **install-specific (gitignored).** Required. `{"group_chat_id": -100…}` pinning which group new topics go in. Copy `config.json.example` and fill in the Bot API id of your group. Without it the broker falls back to "the first negative chat_id seen", which silently routes to the wrong group as soon as a second group ever appears (see "Pin the active group" below).
- `config.json.example` — committed template for `config.json`.
- `approve_group.py` — CLI. Adds a Telegram group to `~/.claude/channels/telegram/access.json`. Accepts a `t.me/c/...` URL, internal id, or Bot API id and writes a permissive policy by default. No broker restart needed.
- `rename_topic.py` — CLI. Renames an entry in `topics.json` (e.g. the placeholder `topic-0` → `general`). Does NOT rename the Telegram forum topic itself.

## Capabilities (inherited from the plugin)

Full set: `reply` (text+files+markdown), `react`, `edit_message`, `download_attachment`, inbound text / voice-STT / photos / documents, typing indicator, ack reaction, chunked outbound, allowlist access control, pairing flow, group mention-triggering.

Not yet relayed through C3: permission-request flow (CC asking approval for a tool via Telegram inline keyboard). Everything else passes through unchanged.

## Run

Just start `claude` — the MCP stub bootstraps the broker on demand:

- If `/tmp/c3.sock` is reachable, the stub connects to the existing broker.
- If the socket is absent or stale (e.g. after a reboot — `/tmp` is tmpfs), the stub spawns `broker.py` detached via `subprocess.Popen(..., start_new_session=True)` and waits ~10s for the socket to appear.
- Simultaneous stubs racing to spawn is safe: the broker acquires an `flock` on `/tmp/c3-broker.pid` at startup and any extra broker process exits immediately with `another broker already holds /tmp/c3-broker.pid`. The flock auto-releases on exit (clean or crash), so stale pid files never wedge startup.

Net effect: one long-lived broker per machine, started by whichever session happens to run first. No systemd unit, no manual restart.

```
# The stock Anthropic Telegram plugin MUST NOT be running in parallel
# (only one getUpdates consumer per bot token).

# Any Claude Code session — first one brings up the broker, rest reuse it.
cd ~/arogara/forge-on-forge
~/arogara/c3/mvp/c3-attach
# -> attaches to topic "forge-on-forge" (creates it in the group if missing)

cd ~/arogara/sthapati
~/arogara/c3/mvp/c3-attach
# -> attaches to topic "sthapati"
```

Run the broker manually (`python3 broker.py`) only when debugging — e.g. to watch stderr live.

## Lifecycle: onboarding a new group or topic

The golden rule: prefer the tool / CLI over hand-editing `access.json` or
`topics.json`. Both files are live-reloaded (access by bun, topics by the
broker), so edits take effect without a restart, but the scripts here handle
validation, file permissions, and the -100 prefix math for you.

### Add a new Telegram group

Telegram silently drops messages from groups not in `access.json` → `groups`,
so nothing reaches the broker until you approve the group.

1. Get the group's chat id (easiest: copy any topic or message URL —
   `t.me/c/<internal>/...` — the script parses internal-id → Bot API form).
2. Run:
   ```
   python3 ~/arogara/c3/mvp/approve_group.py '<your-group-t.me-url>'
   ```
   Flags:
   - `--require-mention` — only route messages that @-mention the bot.
   - `--allow-from <userid> ...` — restrict senders (default: any group member).
3. **Pin the group in `config.json`** (one-time, strongly recommended — see "Pin the active group" below). Skip this and `attach_auto` will pick "any negative chat_id seen in `topics.json`" as the group for new topics, which silently routes to the wrong group the moment a second group ever appears.
4. Send a message in the group. The broker log (`/tmp/c3b.log`) should show
   `notifications/claude/channel` with the group's chat id and (for the
   general topic) `thread=0`.

### Pin the active group (`config.json`)

`config.json` lives next to `broker.py` and looks like:

```json
{ "group_chat_id": -100XXXXXXXXXX }
```

`default_group_chat_id()` reads it on every `attach_auto` call, so edits are
picked up live without a broker restart. **Treat this file as required, not
optional** — without it the broker falls back to "the first group with a
negative chat_id in `topics.json`", which is whatever group happened to send
a message first historically. That fallback is silent: `attach()` returns
`ok=true` and the topic gets created in the wrong group, with no warning.

The `attach()` tool response includes `chat_id` — when you create a new
topic, glance at the returned chat id and confirm it matches the group you
intended. If it doesn't, see "Switching to a new group" below.

### Create a new topic in an approved group

Just attach to the name you want. The broker calls Telegram's
`createForumTopic` if the topic doesn't exist yet and upserts the new
`(chat_id, topic_id, name)` into `topics.json`. Example from within any
Claude Code session attached to this broker:

```
attach(target='c3')
```

(The upstream plugin's `createForumTopic` API call rejects reserved names
like `general`, so use any other name for new topics.)

### Switching to a new group (or recovering from a wrong-group attach)

Symptom: you ask for a topic by name, `attach()` reports success, but the
topic doesn't appear in the group you're looking at. Almost always this
means the broker created it in a different group — `default_group_chat_id()`
returned a stale `chat_id` from `topics.json` because `config.json` was
absent or pointed elsewhere.

To migrate (or fix):

1. **Update `config.json`** to the new `group_chat_id` (Bot API form, with
   `-100` prefix). Live-read; no restart.
2. **Approve the new group** via `approve_group.py` if you haven't already.
3. **Prune `topics.json`** of the topic name you want to recreate — leave it
   in place and `attach_auto` finds the old entry first and skips creation.
   Removing a row here only affects the broker's name → topic lookup; the
   forum topic still exists in the old group (orphaned, harmless). Other
   names from the old group can stay or go; if the old group is fully
   abandoned, prune them all in one pass.
4. **Re-attach.** `attach()` releases the previous claim automatically, the
   broker calls `createForumTopic` against the new `group_chat_id`, and
   `topics.json` is upserted with the fresh `(chat_id, topic_id, name)`.
5. **Verify.** The `attach()` response now shows the new chat id; confirm
   the topic is visible in the intended group on Telegram.

### Register the group's built-in "General" topic under a useful name

When the first inbound message arrives from a group's general thread, the
broker inserts `{chat_id, topic_id: 0, name: 'topic-0'}` as a placeholder.
Rename it so `attach(target='…')` can find it:

```
python3 ~/arogara/c3/mvp/rename_topic.py topic-0 general
```

Or by id:

```
python3 ~/arogara/c3/mvp/rename_topic.py --chat-id <your-group-chat-id> --topic-id 0 general
```

### Auto-attach on project start

The root `~/arogara/CLAUDE.md` instructs every session: if `pwd` is a project
dir under `~/arogara/<project>/`, look up `<project>` in this `topics.json`
and call `attach(target='<project>')` automatically. Topics that don't exist
yet are NOT auto-created from `pwd` — creation still requires Karthi to say
"work on `<X>`", so typos and scratch dirs don't spawn forum topics.

## Notes

- If the plugin auto-updates (`~/.claude/plugins/cache/claude-plugins-official/telegram/X.Y.Z`), the broker re-applies all patches to the new version on next startup. If an upstream refactor breaks a patch's anchor, the broker prints `PATCH BROKEN — <id>` with pointers to `PATCH_SPEC.md` and a pristine `server.ts.c3-backup` for diffing; patch it back up from the spec rather than the old anchor.
- The broker takes over the token while running. The stock plugin is not needed in parallel; to keep the stock one available on sessions that don't go through C3, disable it in those sessions with `--disable-plugin` or similar and only start the broker when multiplexing.
- Stopping the broker (Ctrl-C or SIGTERM) cleanly shuts bun down so the next run doesn't hit 409 Conflict on Telegram.
