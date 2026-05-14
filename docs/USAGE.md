# Using C3

This is the day-to-day guide for someone who has C3 installed and wants to actually work with it. For first-time install, see `INSTALL.md`. For developing extensions, see `PLUGINS.md` / `CHANNELS.md` / `ADAPTERS.md`.

## What C3 does for you

C3 is a Telegram (today; more channels later) multiplexer for Claude Code, Codex, and any other CLI you have a C3 adapter for. One Telegram bot, many CLI sessions, each one bound to its own forum topic. From any CLI session you can:

- Send replies to your phone (Telegram).
- Receive messages from your phone, including voice notes (transcribed automatically) and quote-replies (with the quoted text in context).
- Have multiple terminals running simultaneously, each on a different topic, all going through the same bot.
- Switch between Claude Code and Codex on the same project — they coordinate via the broker so you don't accidentally end up with two CLIs answering the same Telegram message.

## The mental model

Three things to internalize:

- **One broker per machine.** It's a long-lived background daemon that owns the bot token, polls Telegram, and routes messages. You never start it manually — your first CLI session of the day does that for you.
- **One mapping per directory.** When you `cd ~/projects/foo && claude`, C3 looks up `/home/you/projects/foo` in `~/.config/c3/mappings.json`. If it's mapped, the session auto-attaches to that topic. If not, you type `attach` and confirm.
- **One claim per topic.** Two sessions can't hold the same topic at once. The second session sees who's holding it and stays unattached. Useful: you can park Claude Code on the topic and open Codex elsewhere; one project, one Telegram chat, no double-replies.

## The one command you'll use most

```
attach
```

Type it (or have your CLI agent type it) when you want to bind the current directory to a Telegram topic. Three things happen:

1. **Mapped directory** — silent claim. The session attaches. You see "Auto-attached to 'foo' topic" and inbound Telegram messages start showing up.
2. **Unmapped directory** — proposal. The broker says "I'd create a topic 'foo' in the 'main' Telegram group. Confirm with `attach(create=true)`, or pass an existing topic id with `attach(topic_id=<n>)`." You confirm; the topic is created; the mapping is persisted; future sessions in this dir auto-attach.
3. **Topic claimed by another session** — broker tells you who's holding it. You either wait for that session to detach or attach to a different topic.

Variations:

```
attach <name>           # attach by topic name. Searches default group first,
                        # then all groups, with disambiguation if found
                        # in multiple. Otherwise proposes creation.
attach --topic=<id>     # claim an existing topic by Telegram thread id.
                        # The broker validates with Telegram before claiming.
attach --group=<g> <name>   # name + explicit group override
attach dm               # route to your personal DM with the bot. Works
                        # anywhere, no mapping persistence (DM is universal).
```

## The other commands

```
topics                  # list known topics + who's currently holding each
```

That's basically it for daily use. Most flow runs implicitly — the topic auto-attaches, your messages flow.

## Telegram-side workflow

You created a bot with `@BotFather` and got a token. You added the bot as an **admin** with `Manage Topics` permission to a supergroup. You set your `dm_chat_id` to your own user id (positive integer; ask `@userinfobot` if you don't know it).

From there:

- **Sending a message into a topic** — your CLI's reply tool sends a message into the right thread. From your phone you see them in the topic.
- **Replying from your phone** — type into the topic's chat. The CLI sees your message as an inbound block.
- **Voice notes** — record on your phone, send to the topic. The STT plugin transcribes; the CLI sees `[Transcribed voice]: <text>` plus the original voice attachment available for re-download if the transcript is wrong.
- **Quote-replying** — long-press a CLI message, hit Reply, type. The CLI sees the inbound with `reply_to_message_id`, `reply_to_user`, and `reply_to_text` so it knows which prior message you're answering.
- **Multi-message bursts** — send three messages in quick succession; the broker's debounce window collapses them into one inbound (default 1500ms). Saves your CLI from getting confused by interleaved partial thoughts.

## Multi-group setups

Configure multiple Telegram supergroups in `mappings.json`:

```json
{
  "channels": {
    "telegram": {
      "default_group": "main",
      "groups": {
        "main": {"chat_id": -1001111, "title": "Personal"},
        "work": {"chat_id": -1002222, "title": "Work multiplexer"}
      }
    }
  }
}
```

`attach` and `attach <name>` default to `main`. `attach --group=work <name>` targets the work group. The broker tracks the chosen group in the mapping so future sessions auto-attach to the right place.

Each group needs the bot as an admin with `Manage Topics`.

## Cross-CLI on the same project

You're in `~/projects/widget`. You started Claude Code there earlier; it auto-attached to topic `widget`. Now you want to ask Codex something about the same project. From a different terminal:

```
$ cd ~/projects/widget
$ codex
```

The `codex` command goes through the C3 launcher, which spawns a Codex app-server, registers the C3 MCP adapter, and launches the visible TUI bound to that app-server. The adapter sees the cwd has a mapping but it's already claimed by Claude Code — so Codex stays unattached and tells you. To take over, either `/exit` the Claude session, or run `c3-broker release ~/projects/widget` from any shell to drop the claim without quitting Claude. Then `attach` from Codex.

If you want Claude and Codex on different topics in the same group, attach Codex to a different topic explicitly: `attach(target="widget-codex")`. The broker creates that as a sibling topic in the group; future Codex sessions in this dir will need the same explicit override (or you switch the dir's default mapping).

## Editing mappings.json by hand

Nothing stops you. Mode 600, JSON, atomically rewritten by the broker — but if you `vim` it while the broker is running and save, the broker's next read will pick up the change. Common edits:

- Rename a topic locally (without renaming on Telegram). Changes the `name` field; routing keeps working because `topic_id` is the source of truth.
- Migrate a mapping to a new cwd (e.g. you renamed a project directory). Move the entry's key.
- Add a group. Insert under `channels.telegram.groups`; reload the broker (or restart your next session).
- Remove a stale mapping. Just delete the cwd's entry.

If you bork the file structurally (invalid JSON), the broker refuses to start and prints the parse error. Restore from your backup or fix the syntax.

## Deleting a topic

C3 doesn't auto-delete. From your phone (Telegram), long-press the topic in the group's topic list and pick "Delete topic". Then in `mappings.json`, remove the topic from `channels.telegram.topics` and any cwd mappings that pointed at it. Or run the broker's cleanup tool if it exists by the time you read this.

## When things go wrong

- **No CLI is attached, but messages keep arriving** — the broker sends a one-shot fallback reply (cooldown 5 min) telling you to attach a CLI. Open a session in the project directory and `attach`.
- **`attach` says the topic is held** — `topics` lists who. If it's a stale claim (the holder crashed), restart the broker (`pkill c3-broker`; the next session spawns a fresh one).
- **Voice transcription is wrong** — re-record (Telegram preserves the original audio; the CLI can `download_attachment` to re-listen). The STT plugin's confidence isn't surfaced in v1; treat the transcript as a hint when accuracy matters.
- **`codex` doesn't seem to be using C3** — check `which codex` returns the C3 launcher (`$GOBIN/codex` after install). Long-running shells hash; open a new terminal or `hash -r`. The launcher logs to `/tmp/c3-codex-supervisor.log` — `tail` it during a `codex` invocation to see what it thinks it's doing.
- **`reply` says to attach first** — your adapter lost local state but the broker may still hold your claim. Try `attach` again with the same target; the adapter recovers from the broker's claim (both Claude and Codex adapters do this). If that fails, `topics` shows who's holding what; `c3-broker status` from a separate shell tells you the same with more detail.

## Health checks

Quick sanity sweep when something feels off:

```
ls -la "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" 2>/dev/null || ls -la /tmp/c3-$UID.sock   # broker socket; should exist + 0o600
cat /tmp/c3-broker.pid        # the running broker's pid
tail /tmp/c3-broker.log       # recent activity
tail /tmp/c3-codex-supervisor.log  # last codex launcher invocation (if Codex installed)
cat /tmp/c3-codex-app-server.json  # current app-server signature (cwd, topic, adapter path)
```

If the socket is missing or the pid file points at a nonexistent process, the next CLI session will spawn a fresh broker. If something looks corrupted, `pkill c3-broker; rm -f "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" /tmp/c3-$UID.sock "${XDG_RUNTIME_DIR:-$HOME/.cache/c3}/c3-broker.pid"` resets the world. Or use the operational subcommands instead — `c3-broker status` prints liveness, `c3-broker release <cwd>` drops a stuck claim, `c3-broker reload-config` re-reads `mappings.json` after hand-edits without dropping live claims.

## Privacy and safety

- The bot token is in `mappings.json` at mode 600. Treat it like a password.
- Anyone in your Telegram supergroup can send messages that hit your CLI. The `master_user_id` field in `mappings.json` is plumbed for future per-user access control; today, the bot trusts everyone in the group. Use private supergroups.
- C3 doesn't store message history beyond what's needed for routing. If you want a message log, set up Telegram's own export, or write a plugin that subscribes to `OnInbound` and writes to a file.
- The Codex bridge spawns app-servers. They're long-lived processes that hold MCP servers loaded; they listen on `127.0.0.1` only (not exposed to the network). If you `pkill codex-app-server` everything cleans up.
