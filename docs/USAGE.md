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
attach <topic-id>       # claim an existing topic by Telegram thread id
                        # (a bare integer).
attach dm               # route to your personal DM with the bot. Works
                        # anywhere, no mapping persistence (DM is universal).
attach create <name>    # create + claim a new topic named <name>.
attach -y <name>        # same as `create <name>`.
```

Structured args (`name=`, `topic_id=`, `target=`, `create=`) and the group override are passed by the CLI agent via the `attach` MCP tool, not typed as flags — see `docs/COMMANDS.md` for the full parser table.

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
- **Multi-message bursts** — multiple **text** messages in quick succession are collapsed by the debounce window (default 1500ms). Saves your CLI from getting confused by interleaved partial thoughts. Photo/file **albums** are not assembled as a unit in v0.1 — album siblings arrive as separate inbounds merged only by the debounce window; reliable album handling is a known gap (see RESUME.md FIX #1).

## Durable inbound queue & backlog

Once C3 has *received* a Telegram message, it never loses it — even if no CLI is attached or the broker was down when it later caught up. Inbound messages are held on disk and delivered when a session attaches. You never have to re-forward a voice note or babysit delivery.

How it works:

- **Held, never dropped.** A message that arrives while no session is attached to its topic is appended to a durable per-route queue under `$XDG_STATE_HOME/c3/queue/` (fallback `~/.local/state/c3/queue/`) and `fsync`'d to disk. The broker only advances the Telegram read offset *after* the message is durably persisted, so a broker crash mid-flight can't lose anything — Telegram redelivers it on the next poll.
- **Held-count auto-reply.** The first held message in a topic gets one reassurance reply: *"📨 Held — nothing lost. No CLI is attached to this topic right now. N message(s) queued — they'll be delivered when you attach a session here. Send /status to check."* It repeats at most once per 5-minute cooldown with the *running* count; messages in between are queued silently (no reply per voice note).
- **Backlog on attach.** When you `attach` to a topic with held messages, the session is told how many are queued (with a short per-message preview) and instructed to call `fetch_queue` to retrieve them. The agent decides whether to drain all at once or work through them in batches.
- **Live messages are unaffected.** When a session is attached, messages still push through immediately; they're removed from the queue once the agent has actually taken them. The queue earns its keep only when there's no live consumer.

### Pulling the backlog — the `fetch_queue` tool

Your CLI agent retrieves held messages with the `fetch_queue` MCP tool (both Claude Code and Codex have it):

- `limit` — how many oldest messages to pull. Default **3**, max **50**, or the string `"all"` to drain everything. Small batches let the agent process carefully one group at a time; `"all"` is for bulk catch-up.
- `ack` — default **true**, which *consumes* the messages (walks the cursor forward; the queue files are deleted once fully drained). Pass `ack=false` to *peek* without consuming.

Each returned message carries full content — sender, kind, timestamp, the text or voice transcript, any quote-reply context, and attachments (each with `file_id`) — plus `remaining`, the count still queued.

### Fixing a failed transcription — the `retranscribe` tool

STT failures are usually a transient or down provider, not lost audio. When transcription fails, the *agent* sees a self-documenting recovery message (not a dead end): it states the audio is saved, that you don't need to resend, and exactly how to recover it — `download_attachment` to fetch the audio, or `retranscribe` to re-run STT once the provider is healthy.

`retranscribe(file_id)` re-runs the STT provider chain on the saved audio (downloading it if not cached) and returns the fresh transcript. Pass the optional `message_id` and, if that message is still sitting in the queue, its stored transcript is refreshed **in place** — so when you later `fetch_queue` it, you get the corrected text. The audio is always preserved, so a failed transcription never means re-recording.

### Checking queue depth — `/status` in Telegram

Type `/status` directly into a Telegram chat (it autocompletes in the `/` menu) to see queue depth and attach state. This is a *Telegram bot command*, distinct from the `/c3:status` CLI slash command — the broker answers it directly and never routes it to an agent.

- **In a topic** → that topic's status, e.g. `📊 arogara · 3 queued (oldest 2h) · no CLI attached · broker up`.
- **In DM or General** → a global summary across all routes (empty queues omitted), e.g.:
  ```
  📊 Broker up (pid 12345). Active queues:
  • arogara — 3 (oldest 2h)
  • proctor — 1 (oldest 10m)
  1 attached · 1 idle
  ```

### Limits

- Per-route cap: **1000 messages OR 14 days**, whichever comes first. On overflow the oldest held messages are dropped, logged to `broker.log`, **and** announced in the topic (*"⚠️ queue full — dropped N oldest held message(s); attach a session soon."*) — never a silent truncation.
- **24-hour Telegram bound (outside C3's control).** Telegram itself keeps undelivered updates for at most 24 hours. C3 can only queue what it has actually received, so a gap longer than 24 hours with **no broker polling anywhere** loses messages at Telegram's level before C3 ever sees them. Keeping a broker polling (the opt-in `systemd --user` unit helps) is the only guard against that window.

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

The `codex` command goes through the C3 launcher, which spawns a Codex app-server, registers the C3 MCP adapter, and launches the visible TUI bound to that app-server. The adapter sees the cwd has a mapping but it's already claimed by Claude Code — so Codex stays unattached and tells you. To take over, `/exit` the Claude session to drop the claim. (`c3-broker release <cwd>` is on the roadmap but stubbed in v0.1.0 — for now, `/exit` the Claude session to drop the claim.) Then `attach` from Codex.

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

- **No CLI is attached, but messages keep arriving** — nothing is lost. They're held in the durable queue and you get a "Held — nothing lost" reply (cooldown 5 min) with the running count; send `/status` to check depth. Open a session in the project directory and `attach`, and the agent retrieves the backlog with `fetch_queue`. See "Durable inbound queue & backlog" above.
- **`attach` says the topic is held** — `topics` lists who. If it's a stale claim (the holder crashed), the broker now sweeps dead-pid holders on dispatch (2026-05-14 fix); just retry `attach`. If that doesn't free it, quit Claude Code and relaunch — the new session's broker auto-spawn starts clean. Don't bounce the broker from inside CC (killing the broker also kills this session's MCP server, requiring a manual `/mcp` reconnect). From an external terminal, `pkill c3-broker` works. For mappings.json edits, `/c3:reload-config` is non-disruptive.
- **Voice transcription is wrong or failed** — never re-record. The original audio is saved; the CLI can `download_attachment` to re-listen, or `retranscribe` to re-run STT (e.g. once a flaky provider recovers). On an outright STT failure the agent sees a self-documenting message telling it exactly how to recover. The STT plugin's confidence isn't surfaced in v1; treat the transcript as a hint when accuracy matters.
- **Typing indicator** — the broker now auto-pulses a typing indicator on a route while the agent is working, once that session has replied at least once in the topic (the signal you're in an active Telegram conversation). It stops when the agent sends its reply, or after a safety timeout. A brand-new topic shows no typing until the agent's first reply, and default-CLI-mode sessions (that never reply to Telegram) never pulse it.
- **`codex` doesn't seem to be using C3** — check `which codex` returns the C3 launcher (`$GOBIN/codex` after install). Long-running shells hash; open a new terminal or `hash -r`. The launcher logs to `/tmp/c3-codex-supervisor.log` — `tail` it during a `codex` invocation to see what it thinks it's doing.
- **`reply` says to attach first** — your adapter lost local state but the broker may still hold your claim. Try `attach` again with the same target; the adapter recovers from the broker's claim (both Claude and Codex adapters do this). If that fails, `topics` shows who's holding what; `c3-broker status` from a separate shell tells you the same with more detail.

## Health checks

Quick sanity sweep when something feels off:

```
c3-broker status              # one-shot summary of everything below
ls -la "${XDG_RUNTIME_DIR:-/run/user/$UID}/c3.sock"     # broker socket; should exist + 0o600
cat "${XDG_RUNTIME_DIR:-/run/user/$UID}/c3-broker.pid"  # the running broker's pid
tail ~/.local/state/c3/broker.log     # broker activity (XDG_STATE_HOME, or default)
tail ~/.local/state/c3/adapter.log    # adapter MCP frames
tail /tmp/c3-codex-supervisor.log     # last codex launcher invocation (if Codex installed)
cat /tmp/c3-codex-app-server-$UID.json  # current app-server signature (cwd, topic, adapter path)
```

If the socket is missing or the pid file points at a nonexistent process, the next CLI session will spawn a fresh broker. If something looks corrupted, `pkill c3-broker; rm -f "${XDG_RUNTIME_DIR:-/tmp}/c3.sock" /tmp/c3-$UID.sock "${XDG_RUNTIME_DIR:-$HOME/.cache/c3}/c3-broker.pid"` resets the world — do this from a separate terminal, not from inside Claude Code (broker death + adapter recycle = double pain). Operational subcommands: `c3-broker status` prints liveness, `c3-broker topics` lists topics and live claim state, `c3-broker validate` parses mappings.json. After editing `~/.config/c3/mappings.json` by hand, `/c3:reload-config` sends SIGHUP to the running broker so it re-reads the file in-place (live claims preserved, no process churn). (`c3-broker release <cwd>` is wired but stubbed in v0.1.0 — roadmap.)

## Privacy and safety

- The bot token is in `mappings.json` at mode 600. Treat it like a password.
- Anyone in your Telegram supergroup can send messages that hit your CLI. The `master_user_id` field in `mappings.json` is plumbed for future per-user access control; today, the bot trusts everyone in the group. Use private supergroups.
- C3 doesn't store message history beyond what's needed for routing. If you want a message log, set up Telegram's own export, or write a plugin that subscribes to `OnInbound` and writes to a file.
- The Codex bridge spawns app-servers. They're long-lived processes that hold MCP servers loaded; they listen on `127.0.0.1` only (not exposed to the network). If you `pkill codex-app-server` everything cleans up.
