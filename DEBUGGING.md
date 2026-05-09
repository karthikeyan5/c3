# Debugging C3

When something looks wrong (message didn't arrive, attach is rejected, the
broker won't start), this is where to look first. **Read this before adding
print statements or `pkill`-ing the broker.**

## TL;DR — first thing to do

```bash
c3-broker status              # broker pid, socket, mappings, log path
tail -F ~/.local/state/c3/broker.log
```

Then reproduce the bug. The broker emits one log line per inbound delivery
(success or failure). If nothing appears in the log when you send a
Telegram message, the bug is upstream of the broker (polling, network, or
Telegram API).

## Logs

| Source | Path | Notes |
|---|---|---|
| Broker | `$XDG_STATE_HOME/c3/broker.log` (default `~/.local/state/c3/broker.log`) | Tee'd to stderr while a tty exists. Persistent. Override with `C3_LOG_FILE=...`. |
| Adapter (`c3-claude-adapter`) | The CLI's stderr — captured by Claude Code's plugin mechanism | Not persistent; restart the session to clear. The broker log is the durable signal. |

The broker log is **always read first.** Adapter stderr is opportunistic.

## Log line shape

All lines use stdlib `log` format: `2026/05/09 06:45:01.123456 ` prefix.
Field order is stable so you can grep / awk.

```
telegram: inbound update=12345 msg=914 chat=-1003990699908 thread=914 kind=text edited=false
emit DROP chan=telegram chat=-1003... topic=914 msg=914: worker queue full or stopped
delivered chan=telegram chat=-1003... topic=914 msg=914 to cli=claude pid=1234 conn=2
deliver FAIL chan=telegram chat=-1003... topic=914 msg=914 to cli=claude pid=1234: write: broken pipe
drop chan=telegram chat=-1003... topic=- msg=915: no claim, fallback in cooldown
fallback chan=telegram chat=-1003... topic=- msg=916: no claim, sent fallback reply
fallback FAIL ... : send: telegram: 400 Bad Request: chat not found
```

Field meanings:

- `chan` — channel name (always `telegram` in v1).
- `chat` — `chat_id` (negative for groups, positive for DMs).
- `topic` / `thread` — `message_thread_id`. `-` means no topic (DM or
  non-forum group).
- `msg` — `message_id`.
- `cli` / `pid` / `conn` — which adapter received the inbound. `conn` is
  the broker's monotonic adapter conn id (bumped on each (re)connect).

## Content policy

Logs **never include**:

- the message text,
- the sender username,
- attachment file content (file_id is OK — it's an opaque Telegram token,
  not user content).

Sender `user_id` is also kept out of the success path. If you're tempted to
add it for a bug hunt, prefer to derive it from `chat_id` first (DMs share
`chat_id == user_id`); only log `user_id` in failure-path lines and only
when needed to diagnose the failure.

## Common diagnostic flows

### "I sent a message in Telegram and the CLI never saw it"

```bash
tail -F ~/.local/state/c3/broker.log &
# now send the message in Telegram
```

What to look for, in order:

1. **No `telegram: inbound …` line.** Polling didn't see the message, or
   `convertInbound` returned nil. Possible causes: bot token revoked,
   network timeout, message is a service event (forum_topic_created etc.),
   or the bot isn't a member of the chat. Check
   `pgrep -af c3-broker` is alive; check `ss -tnp | grep c3-broker` for
   the TLS connection to `149.154.166.x:443`.
2. **`telegram: inbound …` but no `delivered` / `drop` / `fallback` line
   within ~2 seconds.** Worker pool is wedged, or the route worker's
   debounce isn't flushing. Worker's `defaultDebounceWindow` is 1.5s.
3. **`drop … no claim, fallback in cooldown`.** No adapter has claimed
   the route, AND a fallback was sent in the last 5 minutes. The
   adapter that should own this route either crashed, or never called
   `attach`, or attached to a *different* `chat_id`/`topic_id`. Run
   `c3-broker status` and compare `chat_id`/`topic_id` from the log line
   against the mappings file's `topics` and `dm_chat_id`.
4. **`fallback … sent fallback reply`.** Same as above but the cooldown
   was clear, so Telegram now shows a "no agent attached" message in the
   chat. Same fix path.
5. **`delivered chan=… to cli=claude pid=…`.** Broker did its job. Bug is
   adapter-side or Claude Code side. Move to the next section.

### "Broker says delivered but Claude Code doesn't show the message"

The adapter logs to its own stderr (transient). To capture it for one
session, run the adapter outside Claude Code:

```bash
c3-claude-adapter 2>/tmp/c3-adapter-debug.log
```

…and point Claude Code's MCP config at this wrapper, OR temporarily edit
`~/.claude/plugins/cache/*/c3/.mcp.json` to redirect stderr.

Look for `notified chan=… msg=…` lines. If they appear, the MCP frame was
written to stdout — the receiver (Claude Code) is silently dropping it.
This usually means the notification method (`notifications/claude/channel`)
isn't recognized; check that against the official Telegram plugin's
notification name and the adapter's `Notify` call.

If `notify FAIL …` appears, stdin/stdout is broken — usually means Claude
Code closed its end.

### "Broker won't start"

```bash
c3-broker 2>&1 | head -20      # run in foreground
```

Common causes: stale pid file with a live foreign process at that pid
(unlikely but possible after reboots reusing pid numbers); mappings.json
parse error (`c3-broker validate`); socket dir not writable.

### "Two brokers fighting"

Telegram gives `409 Conflict` when two pollers hit the same bot token.
Check:

```bash
pgrep -af 'c3-broker|broker.py'
```

If the Python POC broker is still running, kill it before the Go broker.
See `INSTALL.md` step 2.

## Conventions for future log lines

- One line per **delivery outcome**, not per stage. Stages are debug noise.
- Lead with the verb: `delivered`, `drop`, `deliver FAIL`, `fallback`,
  `fallback FAIL`, `emit DROP`. Greppable.
- Always include `chan=… chat=… topic=… msg=…` in that order.
- Failures get a trailing `: <reason>`. Successes don't.
- Never log message content. Never log usernames. See content policy above.

## Things that should be logged but aren't yet

- Outbound tool calls (`reply` / `react` / `edit_message` / `send_typing` /
  `download_attachment`) — currently silent. Add when an outbound bug
  surfaces and you actually need them. The log line should include the
  same `chan/chat/topic` fields plus `tool=reply` and the resulting
  Telegram `message_id` on success / error string on failure.
- Plugin-pipeline drops — when an `OnInbound` hook returns nil and the
  message is intentionally dropped, log `pipeline DROP chan=… msg=…
  by=<plugin>`. Currently silent (intentional drops look identical to
  bugs).
- Adapter (re)connect events with conn id and prior conn id — useful when
  a session loses its claim and you want to know why.

## OpenClaw resilience parity

OpenClaw's `extensions/telegram/` (grammy-based, TypeScript) has more
production hardening than we currently do — explicit 429 `retry_after`
honoring, 401 circuit-breaker, error classification (transient vs
permanent), persisted update-id watermark for crash safety, per-method
timeout policy, etc.

The actionable punch list (researched 2026-05-09) lives in
[`TODO.md`](TODO.md) under **"Telegram resilience — OpenClaw parity"**.
Not done yet; pick from there in priority order when adding more
hardening to `internal/channel/telegram/`.

## Things we've found and fixed (history)

- **2026-05-09 — getUpdates always timing out.** gotgbot's
  `DefaultTimeout` is 5s; our long-poll asked Telegram to hold 25s.
  Client cancelled every cycle with `context deadline exceeded`. Zero
  inbounds reached the broker. Fixed in `internal/channel/telegram/poll.go`
  by passing `RequestOpts.Timeout: longPollTimeout + 10s` on each
  GetUpdates call. The "per-method timeout policy" item in TODO.md
  generalizes this — today *all* gotgbot calls share the 35s timeout, but
  control calls (`getMe`, `sendMessage`) shouldn't wait that long.
