# C3 Re-Architecture — Design Spec

**Date:** 2026-05-08 (revised after second review)
**Status:** Proposed v3 — pending Karthi sign-off on the small remaining questions in §11
**Reaffirms:** D006 (Go for daemon), D007 (pluggable transport — promoted to v1), D008 (official Go MCP SDK)
**Supersedes:** the deviation banners across `RESUME.md` / `TODO.md` / `DECISIONS.md` (Python wrapper MVP). A formal D009 will record the v1-MVP-superseded note when implementation starts.

## 1. Goal

Take C3 from a hand-tuned Python MVP into a **distributable, multi-channel, multi-CLI** plugin set, written in **Go**, with a **plugin extension system** so STT and future capabilities slot in cleanly.

- **Distributable** — one public github URL, anyone can install via Claude Code or Codex plugin marketplace.
- **Go everywhere** — broker, channels, adapters all in Go. Cross-compiled binaries shipped via GitHub releases. STT pipeline stays Python and is subprocess'd from Go (proven code, don't rewrite).
- **Multi-channel** — Telegram is the only channel today; web chat and voice mode were always D007's destination, now plumbed into v1's data model and code paths.
- **Multi-CLI** — Claude Code first, Codex parity, future CLIs through a documented adapter contract.
- **Multi-group** — multiple Telegram supergroups can host C3 topics simultaneously, addressed by name.
- **Seamless attach UX** — sessions in known directories auto-attach; new directories prompt for confirmation; nothing gets created behind the user's back; bare topic ids get validated against Telegram and accepted if real; `attach dm` works anywhere.
- **Plugin extension system** — STT is a plugin in v1, not a built-in. Defined hook points let other plugins drop in without core changes.

The current Python MVP works for Karthi but creates duplicate forum topics on every non-arogara session, hardcodes `/home/karthi/...` paths, bottlenecks future channels behind the upstream bun plugin's release schedule, and was written as a temporary wrapper that was never meant to scale across users.

## 2. Alignment with C3's stated direction

This rearch is mostly a **return** to the original direction (D006 Go, D007 pluggable transport, D008 Go MCP SDK), having spent April pivoting through a Python wrapper to ship something working. Promoting the right pieces from the long-term roadmap into v1's architecture:

| C3 stated goal (README / TODO / DECISIONS) | Where it lands in v3 |
|---|---|
| D006 "Go for daemon and MCP stubs" | reaffirmed; whole stack in Go |
| D007 "Pluggable transport — Telegram first, web/voice later" | §4.1 channels plane, multi-channel data model in v1 |
| D008 "Official Go MCP SDK" | reaffirmed; adapters use `github.com/modelcontextprotocol/go-sdk` v1+ |
| README §"Key Features" message dedup + debouncing | §7 OpenClaw UX features in v1 |
| README §"OpenClaw inspiration" sender id with cross-channel prefix | §4.3 inbound payloads carry `channel` field |
| README §"Routing Modes" group-based (no topics) | §4.2 routing key is `(channel, chat_id, topic_id)` — `topic_id=None` covers no-topics |
| TODO Phase 1 typing indicator | §7 — adopted in v1 |
| TODO Phase 1 STT in daemon, attachment forwarding | §8 STT plugin; attachments via channel passthrough |
| TODO Phase 4 "Stream thinking/tool calls" | §7 — adopted in v1 via `edit_progress` tool |
| TODO Phase 3 master Telegram user / access control | §4.3 `master_user_id` stored; enforcement deferred |
| TODO Phase 2 CLI auto-spawn / multi-CLI | adapter plane is ready; auto-spawn deferred |
| Plugin architecture for STT and future capabilities | §8 — pulled into v1 (was implicit roadmap) |

What stays deferred to later phases: inter-CLI messaging, monitoring dashboard, persistent message history, web/voice channels themselves, master-CLI admin commands, pairing flow, CLI auto-spawn.

What changes from the original direction: nothing. The April Python wrapper was a forced detour to wrap the working bun plugin without rewriting; it shipped, it ran, and now we're back on the originally specced path with everything we learned baked in.

## 3. UX contract — what "seamless" means

Normative paragraph:

A user with C3 enabled on Claude Code (and/or Codex) has a single broker daemon running on their machine. They `cd` into any directory and start their CLI. If the directory has been attached before, inbound messages from the mapped channel/topic appear automatically. Otherwise the CLI starts unattached. The user types `attach`. The broker checks the default channel's default group for a topic matching `basename(cwd)`. If found, claim it. If not, search the other groups in that channel; if found elsewhere, propose: "found 'foo' in group 'work'. Did you mean that, or create a new one in group 'main'?". If found nowhere, propose: "no topic 'foo' exists. Create it in group 'main'?". Only on confirmation does the broker call `createForumTopic`. Once attached, the broker persists the mapping `<absolute-cwd> → <channel, chat_id, topic_id>` so the next session in this directory auto-attaches. `attach <name>` runs the same default-then-search flow with the explicit name. `attach --topic=<id>` validates the id against Telegram (cheap test call); if valid, claim and add to registry; if invalid, refuse with the actual error. `attach --group=<g> <name>` targets a specific group as the default for that command. `attach dm` always routes to the user's personal 1-on-1 chat. If two sessions try to attach to the same mapping at once, the second is told who's holding it and stays unattached. The user never has to think about ids, JSON files, run-in-circles confirmations, or which group a topic lives in.

Implications:

- **Nothing gets created without explicit confirmation.** Even if the LLM is acting autonomously, it has to make a second tool call (`attach(create=true)`) after seeing the proposal.
- **`attach` in a mapped directory is silent and immediate** — no proposal, no extra round-trip. That's the auto-attach case.
- **Topic-id-by-number gets validated, not gated by `force=true`.** If the user knows the id, the broker validates via a lightweight Bot API call (sendChatAction with thread id, or no-op editForumTopic) and accepts if Telegram says yes. No make-them-confirm-twice flow.
- **All operational state — channel configs, group ids, DM id, mappings, topic registry — lives in one user-visible JSON file** (`~/.config/c3/mappings.json`). Karthi's request: "everything in mappings.json".
- **Multi-group is first-class.** Default group resolves bare `attach`; cross-group search happens when the default doesn't have a match.
- **Multi-channel is first-class in the data model**, even though only Telegram is implemented in v1. The mapping schema, IPC protocol, and broker code all carry a `channel` field.
- **One cwd ↔ one mapping.** A cwd cannot map to multiple (channel, chat_id, topic_id) tuples. If you want both Telegram and a future Slack notification for the same project, that's a future routing-rules feature, not a mappings extension.

## 4. Architecture

Five planes. Each has one job.

```
                ┌──────────────────────┐
                │  Channel modules     │
                │  ──────────────────  │
                │  Telegram (Go, v1)   │
                │  Web      (future)   │
                │  Voice    (future)   │
                └──────────┬───────────┘
                           │ Go pkg API (in-process)
                ┌──────────▼───────────┐
                │  C3 Broker (Go)      │
                │  (singleton daemon)  │
                │  ─ routing           │     ┌──────────────┐
                │  ─ mapping registry  │◄───►│ ~/.config/c3/│
                │  ─ confirmation flow │     │ mappings.json│
                │  ─ debounce/dedup    │     └──────────────┘
                │  ─ plugin host       │
                └──────────┬───────────┘
                           │ Go pkg API
                ┌──────────▼───────────┐
                │  Plugins (Go + ext)  │
                │  ──────────────────  │
                │  STT     (Go shim    │
                │            → Python) │
                │  <future plugins>    │
                └──────────┬───────────┘
                           │
                  ┌────────▼─────────┐
                  │ /tmp/c3.sock     │
                  └──┬─────┬─────┬───┘
              ┌─────▼┐  ┌──▼──┐ ┌▼─────────┐
              │Claude│  │Codex│ │Future CLI│
              │adapter│ │adapter│ │adapter │
              │(Go)   │ │(Go) │ │           │
              └──┬───┘  └─┬──┘ └─┬───────┘
                 │ stdio  │      │
              ┌──▼───┐  ┌▼───┐
              │ CC   │  │Codex│
              │ CLI  │  │ CLI │
              └──────┘  └─────┘
```

### 4.1 Channels plane (Go)

A channel is a unidirectional-aware message transport. v1 ships **telegram**.

Each channel implements a Go interface in the broker:

```go
type Channel interface {
    Name() string
    Start(ctx context.Context, broker *Broker) error
    Stop() error
    SendReply(args ReplyArgs) (*ReplyResult, error)
    SendTyping(chatID int64, threadID *int64) error
    EditMessage(args EditArgs) error
    ValidateTopic(chatID int64, threadID int64) error  // for attach --topic=N
    CreateTopic(chatID int64, name string) (int64, error)
    // …channel-specific extras
}
```

Inbound messages flow from the channel into the broker via a normalized event:

```go
type InboundEvent struct {
    Channel    string  // "telegram"
    ChatID     int64
    TopicID    *int64  // nil for non-forum, 1 for General, >1 for custom topic
    MessageID  int64
    Sender     Sender
    Text       string
    Attachments []Attachment
    ReplyTo    *ReplyContext
    Timestamp  time.Time
}
```

The Telegram channel in v1:
- **Cleanroom Go rewrite.** No retained file from upstream bun plugin — Karthi explicitly chose this in §11.1 review. We use upstream as inspiration at the spec level (what features it has, how it's structured) and write our own Go implementation.
- Library choice: `github.com/go-telegram-bot-api/telegram-bot-api/v5` or `github.com/PaulSonOfLars/gotgbot/v2`. Both maintained, both Bot API. **Open question §11.A** — pick during plan phase. My lean: gotgbot (strongly typed, more idiomatic Go, active development).
- Owns Telegram bot token, manages getUpdates with `allowed_updates = ["message", "edited_message", "callback_query", "message_reaction"]`, applies rate-limit handling (`parameters.retry_after`), runs in a goroutine inside the broker process (not a separate subprocess).
- All P1-P5 patch behaviors built natively: `message_thread_id` in inbound and outbound, `reply_to_message_id`/`reply_to_user`/`reply_to_text` in inbound meta, no orphan watchdog at all (broker manages its own lifecycle), STT plugin invoked on `message:voice` events.
- Exposes its tools to adapters: `reply`, `react`, `edit_message`, `download_attachment`, `send_typing` (new), `edit_progress` (new).

Future channels (web, voice) implement the same interface; adapters don't change.

### 4.2 Routing plane

Routing key: `(channel, chat_id, message_thread_id)`. `message_thread_id` is `*int64` — `nil` means "no topic / non-forum chat", `1` is the General forum topic, `>1` is custom topic. Telegram General topic id confusion (where some code treats 0 as General) is fixed end-to-end.

Live routes are in-memory only (`map[RouteKey]*Stub`). Persistence is for mappings, not for live claims.

### 4.3 Mapping registry plane (broker, persistent)

One file: `~/.config/c3/mappings.json`. Mode 600 (contains the bot token). The user can hand-edit. The broker treats it as authoritative on read, atomic-rewrites on update.

Schema:

```json
{
  "schema_version": 1,
  "channels": {
    "telegram": {
      "bot_token": "1234567:abc...",
      "default_group": "main",
      "groups": {
        "main": {"chat_id": -1003990699908, "title": "Karthi C3 (personal)"},
        "work": {"chat_id": -1009999999999, "title": "Work multiplexer"}
      },
      "dm_chat_id": 85720317,
      "master_user_id": 85720317,
      "topics": [
        {"chat_id": -1003990699908, "topic_id": 281, "name": "c3", "group": "main"},
        {"chat_id": -1003990699908, "topic_id": 207, "name": "sthapati", "group": "main"}
      ],
      "debounce_ms": 1500
    }
  },
  "mappings": {
    "/home/karthi/arogara/c3": {
      "channel": "telegram",
      "chat_id": -1003990699908,
      "topic_id": 281,
      "name": "c3",
      "group": "main",
      "created_at": "2026-04-21T22:00:00Z",
      "last_attached_at": "2026-05-08T06:05:00Z"
    }
  },
  "plugins": {
    "stt": {
      "enabled": true,
      "language": "en",
      "vocabulary_file": ""
    }
  }
}
```

`plugins` is added so plugins keep their config alongside everything else (per the "everything in mappings.json" rule). Each plugin owns its own subkey.

Why one file: explicit Karthi request. Operator's view in one place. Mode 600 protects the bot token. Atomic rewrites via temp-file-then-rename so a half-written file can't corrupt state.

### 4.4 Adapter plane (per-CLI stubs, Go)

Each adapter is a small Go binary that:
1. Speaks its host CLI's MCP protocol over stdio (using `github.com/modelcontextprotocol/go-sdk`).
2. Connects to the broker over `/tmp/c3.sock`.
3. Translates inbound notifications into whatever the host CLI can render.

Common IPC protocol (newline-delimited JSON over the unix socket):

| Direction | Op | Purpose |
|---|---|---|
| stub → broker | `hello` | `{cli, pid, cwd}`. Broker responds with auto-attach state. |
| stub → broker | `server_info` | Get serverInfo + capabilities + instructions. |
| stub → broker | `tools_list` | Get the broker's tool list (channel-specific + universal). |
| stub → broker | `attach` | `{cwd?, name?, target?, topic_id?, group?, channel?, create?}`. Semantics in §5. |
| stub → broker | `list_topics` | All known topics across channels + claim state. |
| stub → broker | `tool_call` | Forward tool invocation to the channel module. |
| broker → stub | `hello_ack` | `{auto_attached, mapping?, claim_holder?, no_config?}`. |
| broker → stub | `attached` | `{ok, channel, chat_id, topic_id, name, group?, needs_confirmation?, proposal?, err?}`. |
| broker → stub | `tool_result` | Result of forwarded tool call. |
| broker → stub | `inbound` | Normalized inbound message routed to this stub. |
| broker → stub | `topics_list` | Listing response. |
| broker → stub | `error` | Generic error. |

Host-specific translations:

- **Claude Code adapter** forwards inbound as `notifications/claude/channel` (preserves rich `<channel>` rendering). Tools list is broker tools + adapter-local `attach` and `topics`. Inbound `<channel>` blocks are rendered natively.
- **Codex adapter** forwards inbound as `notifications/message` log + buffers into `c3_inbox` poll tool + optional WS forwarder to Codex app-server. Tools: `c3_attach`, `c3_topics`, `c3_inbox`, `c3_reply`, `c3_codex_forward`. Designed so the inbox tool is a clean deletion when Codex ships native unsolicited-notification rendering (issues #18056/#17543/#15299 still open).
- **Future CLI adapters** implement the protocol; broker doesn't care.

### 4.5 Plugin host plane (broker, Go)

Plugin host is part of the broker. Plugins run as either:

- **Compiled-in (v1)** — Go packages under `core/plugins/<name>/`, statically linked into the broker binary. Discovered at build time. Configured via `mappings.json:plugins.<name>`.
- **External subprocess (v1.x roadmap)** — separate executables that the broker spawns and talks to over a defined plugin protocol (similar to MCP, simpler payloads). Discovered from `~/.config/c3/plugins/<name>/manifest.json`. Allows third-party plugins without recompiling the broker.

For v1, only compiled-in plugins are implemented. STT is the only one. The external-plugin protocol is documented in §8 but not built.

Hook points the plugin host exposes:

| Hook | When | Plugin can |
|---|---|---|
| `OnInbound(msg) → msg | drop` | After channel receives an event, before routing | mutate, replace, drop |
| `OnVoiceReceived(chan, payload) → text | None` | Telegram voice-event-specific | run STT, return transcript |
| `OnOutbound(msg) → msg | drop` | Before broker sends to channel | mutate, replace, drop |
| `OnAttach(stub, mapping)` | After a session attaches | observe |
| `RegisterTools(broker)` | At plugin load | add MCP tools the adapters expose |

Not all hooks are used in v1 (STT only uses `OnVoiceReceived`). They're plumbed so the next plugin doesn't need a core change.

Plugins are configured under `mappings.json:plugins.<name>`. Each plugin defines its own subkey schema. `enabled: false` skips the plugin without uninstalling.

## 5. Key flows

### 5.1 First install on a fresh machine

1. User runs `/plugin marketplace add karthikeyan5/c3`, then `/plugin install c3@c3`, then `/reload-plugins`.
2. The plugin manifest's `.mcp.json` references a wrapper script at `${CLAUDE_PLUGIN_ROOT}/adapter/claude-c3` (the Go binary, picked from `bin/<os>-<arch>/`).
3. On the next session, the Go adapter starts, tries `/tmp/c3.sock`, fails, spawns the broker binary (`${CLAUDE_PLUGIN_ROOT}/bin/<os>-<arch>/c3-broker`).
4. Broker starts, looks for `~/.config/c3/mappings.json`, doesn't find it. Writes a stub skeleton (mode 600), keeps running.
5. Adapter's `hello_ack` says `no_config: true`. Adapter's `instructions` text: *"C3 not yet configured. Run `/c3-setup` to provide your Telegram bot token, DM chat id, and at least one group chat id."*
6. User runs `/c3-setup`. The slash command (Claude Code) uses `AskUserQuestion` to gather: bot token, DM chat id, group chat id (named, e.g. "main"). Writes `mappings.json:channels.telegram.*`. Tells user to restart the session.
7. After restart, auto-attach (or proposal) works.

For Codex install: `codex plugin marketplace add github:karthikeyan5/c3` then `codex plugin install c3-codex`. If `~/.config/c3/mappings.json` exists, reused. If not, the Codex plugin ships **`SETUP.md`** that the agent reads and executes (per Karthi's call in §11.5):

```
# C3 Codex Setup

Hi agent — this is your setup checklist. Run these in order. The user
can also run them manually if they'd rather.

1. Verify ~/.config/c3/mappings.json exists. If not, ask the user for:
   - Telegram bot token (sensitive; from @BotFather)
   - DM chat id (their Telegram user id, positive integer)
   - At least one group chat id with a name they'll remember
   Then write the skeleton file at mode 600.
2. Run `bin/<os>-<arch>/c3-broker --check-config` — exits 0 if config is valid.
3. Tell the user setup is complete and they should restart the Codex session.
```

Plus a Go helper `bin/<os>-<arch>/c3-setup-codex --bot-token=… --dm-chat-id=…` that the agent can call to do the file write idempotently.

### 5.2 Fresh project — `attach` proposal flow

```
$ cd ~/projects/widget-foo
$ claude
```

1. Adapter `hello` with `cwd=/home/karthi/projects/widget-foo`.
2. Broker checks `mappings.mappings`. No entry. Replies `{auto_attached: false, no_mapping: true}`.
3. Adapter instructions: *"No mapping for `/home/karthi/projects/widget-foo`. Type `attach` to set one up."*
4. User types `attach`.
5. Claude calls `attach()` with no args.
6. Adapter forwards `{op: attach, cwd: "..."}`.
7. Broker:
   - Default channel: `telegram`.
   - Default group: `main`.
   - Proposed name: `widget-foo` (basename of cwd).
   - Search default group (`main`) for topic named `widget-foo` → not found.
   - Search other groups (`work`) for topic named `widget-foo` → not found.
   - Action: propose creation in `main`.
8. Broker replies `{ok: false, needs_confirmation: true, proposal: {channel: "telegram", group: "main", name: "widget-foo", action: "create"}}`.
9. Adapter returns to Claude: *"No mapping for this directory. I'd create a new topic 'widget-foo' in the 'main' Telegram group. Reply yes to create, or pass an existing topic id with `attach(topic_id=<n>)`."*
10. Claude relays. User says "yes".
11. Claude calls `attach(create=true)`.
12. Broker calls `createForumTopic(group=main, name="widget-foo")`. Gets `topic_id=917`. Inserts into `topics`, inserts into `mappings`, atomic-rewrites file, claims for this stub. Replies `attached`.
13. Adapter to Claude: *"Attached to 'widget-foo' (telegram, group main, thread 917). Future runs auto-attach."*

### 5.3 Cross-group search — found elsewhere

User in `~/projects/feature-x`, types `attach`. `feature-x` doesn't exist in default group `main` but exists in `work`.

1. Broker default-group search: not found.
2. Cross-group search: found `feature-x` in `work` group.
3. Broker replies `{ok: false, needs_confirmation: true, proposal: {action: "use_existing_other_group", existing: {group: "work", topic_id: 412, name: "feature-x"}, alternative: {action: "create", group: "main", name: "feature-x"}}}`.
4. Adapter to Claude: *"Found existing topic 'feature-x' in group 'work' (thread 412). Did you mean that one? Reply yes to claim it, or `attach(create=true, group="main")` to create a new one in your default group instead."*
5. User chooses.

### 5.4 `attach --topic=<id>` validation

User in any cwd types `attach --topic=412`.

1. Claude calls `attach(topic_id=412)`.
2. Broker doesn't know about topic 412. Validates via Telegram channel:
   - Try `sendChatAction(chat_id=default_group, message_thread_id=412, action="typing")`.
   - 200 → topic exists. Add to `topics` registry with placeholder name (or pull name from a follow-up `getChat`-like lookup if available; today, store as `topic-412` and let the user rename later).
   - 4xx → return error to the adapter with the actual Telegram error message.
3. If validated, broker claims and persists mapping for the cwd. Replies `attached`.
4. If not validated, adapter returns the Telegram error verbatim. No second-confirmation flow. Per §11.4 review.

The `--topic=<id>` flow needs a group context. If the user passes only `topic_id=412` without `--group`, broker tries the default group's chat_id. To validate against another group, the user passes `--group=work --topic=412` together.

### 5.5 `attach dm`

User types `attach dm`.

1. Broker reads `channels.telegram.dm_chat_id`. Claims `(telegram, dm_chat_id, nil)`.
2. **Does NOT update `mappings.mappings`** — DM is universal, not per-cwd.
3. Replies `attached`.

`attach dm` never proposes/confirms — fixed target.

### 5.6 Cross-CLI cwd collision

Session 1 (Claude in `~/arogara/c3`) auto-attached to topic 281. User opens Codex in same dir.

1. Codex stub `hello` with same cwd.
2. Broker finds mapping → topic 281. ROUTES already claims it for Claude pid 12345.
3. Replies `{auto_attached: false, mapping: {…}, claim_holder: {cli: "claude", pid: 12345}}`.
4. Codex instructions: *"Saved mapping points to 'c3' topic but it's currently held by Claude Code (pid 12345). Use `c3_attach(target='<other>')` to claim a different topic, or wait."*
5. No silent topic creation, no claim theft.

### 5.7 Inbound message routing

Telegram delivers a message in topic 281. The Telegram channel emits an `InboundEvent` to the broker. Broker:

1. Run plugin `OnInbound` chain (currently no-op for non-voice; voice arrives via `OnVoiceReceived`).
2. Apply debounce window per `(channel, chat_id, topic_id)` — 1.5s default (§7.3).
3. Look up `ROUTES[(telegram, -100..., 281)]`. Found Claude stub. Forward as `notifications/claude/channel`.
4. **Do NOT touch `topics` or `mappings`.** No opportunistic upserts.

If no stub claims the route: cooldown fallback reply (existing behavior, kept).

## 6. Telegram channel implementation (Go, cleanroom)

We do not retain a copy of upstream's bun source. We use it as a reference at the spec level (what features it had, what tools it exposed) and write our own Go implementation from scratch.

Capabilities to replicate from upstream + originals:

- `getUpdates` long-poll loop, `allowed_updates` opt-in (including `message_reaction`).
- Inbound MCP notification format `notifications/claude/channel` with meta carrying chat_id, message_id, message_thread_id, sender, attachments, reply_to_*.
- Outbound tools: `reply`, `react`, `edit_message`, `download_attachment`.
- Voice handler that hands off to STT plugin and uses the transcript as the message text (with the file_id retained in attachment meta).
- Rate-limit handling (`parameters.retry_after` honoured).
- General topic id is **1**, not 0 — confirmed by Telegram research, fixed end-to-end.

New first-class tools (not in upstream):

- `send_typing(chat_id, topic_id?)` — sends Telegram chat action.
- `edit_progress(chat_id, topic_id?, text, message_id?)` — creates a progress placeholder on first call, edits on subsequent. Broker tracks the placeholder per `(chat_id, topic_id, session)`.
- `validate_topic(chat_id, topic_id)` — test call for §5.4 attach-by-id.
- `create_topic(chat_id, name)` — wraps `createForumTopic` with rate-limit handling.

Telegram library: `github.com/PaulSonOfLars/gotgbot/v2` (recommended; pending §11.A confirmation).

## 7. OpenClaw-inspired UX features (top 3, in v1)

### 7.1 Typing indicator

When the agent is processing (any tool call in flight that takes longer than ~500ms), the channel sends `sendChatAction(action="typing")` to Telegram every 4s (Telegram caches the action for 5s). User sees continuous "typing…".

Implementation: broker tracks "is this stub busy" via in-flight tool-call counter. When it crosses 0→1, broker calls the channel's `send_typing` for the stub's claimed `(chat_id, topic_id)`. Re-fired on a 4s ticker until in-flight returns to 0.

### 7.2 Streaming progress (`edit_progress` tool)

New MCP tool exposed to the agent. First call creates a placeholder message in the topic; subsequent calls edit it. Broker tracks the placeholder per session.

Use case: long-running tool. Agent calls `edit_progress("scanning files…")`, then `edit_progress("found 47 hits, summarizing…")`, then a final `reply(...)`. User sees a single message with a live progress trail.

Telegram caveat: edits don't trigger push notifications. On agent turn completion, broker sends a fresh `reply` (not an edit) so the user's device pings.

### 7.3 Inbound debouncing

When a user sends multiple messages rapidly, broker buffers inbound by `(channel, chat_id, topic_id)` for `debounce_ms` after each new message (default 1500). After the window closes with no new arrivals, broker delivers a single `notifications/claude/channel` payload containing all buffered messages concatenated, with the latest `message_id` as canonical.

Configurable per channel via `mappings.json:channels.<chan>.debounce_ms`. Per-group override possible later (`groups.<g>.debounce_ms`) — not in v1.

### Why these three, not others

OpenClaw also has session-based routing, multi-turn ping-pong, fire-and-forget vs wait modes, sender-id prefixes. Those are inter-agent-messaging features (TODO Phase 4). Cross-channel sender-id prefix becomes relevant when we have >1 channel.

## 8. Plugin extension architecture

### Goal

Make adding a new capability (transcription, OCR, custom translation, slash-command shortcuts, scheduled outbound messages) **a self-contained drop-in** that doesn't require core changes.

### v1 plugin interface (compiled-in)

A compiled-in plugin is a Go package under `core/plugins/<name>/`:

```go
package stt

func Register(host *plugin.Host) error {
    host.OnVoiceReceived(handleVoice)
    return nil
}

func handleVoice(ctx context.Context, channel string, payload VoicePayload) (string, error) {
    // subprocess Python whisper, return transcript or empty string
}
```

Plugin host calls `Register` at broker startup. Plugin reads its config via `host.Config("stt")` — pulls `mappings.json:plugins.stt.*`. Plugin can also store derived state via `host.State("stt")` if it needs runtime state beyond config.

### v1 plugins

- `core/plugins/stt/` — Speech-to-text. Reads voice attachments via `OnVoiceReceived`, subprocesses the existing Python whisper pipeline (`stt-handler.py`), returns transcript. Bundled Python shim ships with the binary.

That's it for v1. The architecture leaves room for more.

### v1.x plugin protocol (external subprocess plugins)

For plugins users can install without recompiling:

- A plugin lives at `~/.config/c3/plugins/<name>/`.
- Has `manifest.json` declaring: name, executable, hook subscriptions, config-schema.
- Broker spawns the executable on startup, communicates via stdio newline-JSON.
- Same hook semantics as compiled-in plugins.

Documented but **not implemented in v1**. Users wanting a plugin in v1 either patch the broker source or wait for v1.x.

### Hook points (formal)

| Hook | Signature | Order |
|---|---|---|
| `OnInbound` | `(msg) → (msg, drop bool)` | chained; first non-drop result wins |
| `OnVoiceReceived` | `(channel, voice_payload) → (text, error)` | first plugin to return non-empty wins |
| `OnOutbound` | `(msg) → (msg, drop bool)` | chained |
| `OnAttach` | `(session, mapping)` | parallel observers |
| `RegisterTools` | `(broker)` | at plugin load only |

Plugin order is config-driven (`mappings.json:plugins.<name>.priority`), with stable default order.

## 9. Migration from current Python MVP

Karthi handles legacy topic cleanup. We migrate config:

`tools/migrate-legacy.go` (or equivalent script), idempotent:

1. Read `~/.claude/channels/telegram/.env` for `TELEGRAM_BOT_TOKEN`.
2. Read `c3/mvp/config.json` for `dm_chat_id`, `group_chat_id`.
3. If `~/.config/c3/mappings.json` exists, refuse to overwrite.
4. Otherwise write a fresh `mappings.json` skeleton:
   - `channels.telegram.bot_token` from .env
   - `channels.telegram.groups.main = {chat_id: <legacy>, title: "(migrated)"}`
   - `channels.telegram.default_group = "main"`
   - `channels.telegram.dm_chat_id = <legacy>`
   - empty `topics`, empty `mappings`, default plugin config
5. Print summary, set mode 600.
6. Tell user `c3/mvp/` can be deleted once they're satisfied (don't auto-delete).

Old Python broker stays runnable alongside the new Go broker — flock prevents both running at once, user picks which one starts. Once Go broker is verified, user kills Python MVP and removes its files manually.

## 10. What v3 explicitly does NOT include

- Cleaning up legacy topics (Karthi's call).
- Auto-creating topics opportunistically on inbound (removed entirely).
- Auto-deleting topics (manual via Bot API; future tool).
- `forum_topic_edited` rename tracking (defer; plumbed-but-inert in update parsing).
- External (subprocess) plugins (defer to v1.x; in-tree plugins only in v1).
- Per-CLI per-project config files (architecture pushes everything central).
- Webhook mode for Telegram (long-polling stays).
- Reactions / business connections / paid media surfacing (`message_reaction` is plumbed via `allowed_updates` but no tool exposes it yet).
- Auto-spawn of CLIs (TODO Phase 2 — adapter plane is ready, no code yet).
- Inter-CLI messaging, master-CLI admin commands, pairing flow, monitoring dashboard.
- Cleanroom STT rewrite (Go shim → Python pipeline; the working pipeline keeps working).

## 11. Remaining open questions

After Karthi's two reviews, three small calls left:

1. **§11.A — Telegram Go library: gotgbot vs telegram-bot-api.** Both maintained, both Bot API. gotgbot is more idiomatic Go and strongly typed; telegram-bot-api is more popular (more StackOverflow). My lean: gotgbot. Open to override.
2. **§11.B — Binary distribution mechanism.** Three options:
   - (a) Bundle pre-built binaries for all OS/arch in the GitHub release tarball that the plugin installer pulls down. ~50MB tarball.
   - (b) Build from source on first install via `go install` — requires Go on user's machine.
   - (c) Download the right binary on first run from a GitHub release URL. Plugin scaffold ships a small bootstrap script.
   My lean: **(a)** for the smallest install friction. Modern users handle ~50MB.
3. **§11.C — Adapter binary location inside the plugin.** Convention I'm proposing: `bin/<os>-<arch>/c3-broker` and `bin/<os>-<arch>/c3-claude-adapter` and `bin/<os>-<arch>/c3-codex-adapter`, with the `.mcp.json` `command` resolving to the right one via a tiny shell wrapper or via `${CLAUDE_PLUGIN_ROOT}/bin/${OS}-${ARCH}/...`. Confirm this is fine.

Resolved in v3 (Karthi's calls):

- §11.1 ⇒ cleanroom; no upstream baseline file.
- §11.2 ⇒ one cwd → one mapping.
- §11.3 ⇒ default-group-first then cross-group search with disambiguation; only when no mapping.
- §11.4 ⇒ validate via Bot API call; accept if valid; refuse with actual error if not. No `force=true` flag.
- §11.5 ⇒ Codex setup is agent-driven via `SETUP.md` + a Go helper binary.
- §11.6 ⇒ STT is a v1 plugin, not built-in. Plugin architecture pulled into v1.

## 12. Implementation phases

This spec produces an implementation plan via the `writing-plans` skill once §11 A/B/C are answered. Rough phases:

1. **Repo skeleton + Go modules.** `go mod init`, broker / adapters / channels / plugins package layout. `Makefile` for cross-compile.
2. **Mappings registry.** Read/write/validate `~/.config/c3/mappings.json`. Migration tool. Atomic rewrite.
3. **Broker core + IPC.** Unix socket server, IPC protocol v2, flock singleton, basic routing map.
4. **Telegram channel.** Cleanroom Go implementation against the Channel interface. Long-poll, inbound emit, outbound tools. STT plugin gets the voice hook.
5. **Plugin host + STT plugin.** Hook system, `OnVoiceReceived`, Python subprocess shim.
6. **Claude Code adapter.** MCP stdio server, claim-per-cwd, attach proposal flow, all attach modes (no-args, name, dm, topic-id, group-override).
7. **Debounce + dedup, typing indicator, edit_progress.**
8. **Codex adapter.** Same protocol, different host translation. SETUP.md + helper binary.
9. **`/c3-setup` (Claude) and Codex SETUP flow.**
10. **Documentation + release.** README rewrite, INSTALL rewrite, retire deviation banners with formal D009. Tag v0.1.0.

Phases 1-7 unblock Karthi's daily use. 8-10 are about shippability to others.

## 13. Sources

- D006 (Go for daemon and stubs), D007 (pluggable transport), D008 (official Go MCP SDK) — `c3/DECISIONS.md`.
- Claude Code plugin docs: docs.anthropic.com/en/docs/claude-code/plugins.
- Codex MCP docs: developers.openai.com/codex/mcp, /config-reference, /app-server.
- Codex inbound notification status: openai/codex#18056, #17543, #15299 — open.
- Telegram Bot API: core.telegram.org/bots/api, /api-changelog.
- Telegram forums constraint: tdlib/telegram-bot-api#356 (no `getForumTopics`).
- OpenClaw messaging features: c3/README.md §"Inspiration: OpenClaw's Message Tool".
- Existing Python MVP: `mvp/broker.py`, `mvp/stub.py`, `mvp/codex_stub.py`, `mvp/PATCH_SPEC.md` (reference; spec-level only).
- Existing plugin scaffold: `plugin/.claude-plugin/marketplace.json`, `plugin/plugins/c3-telegram/`.
- C3 stated direction: README.md §"Key Features (Full Vision)", TODO.md Phases 1-4, DECISIONS.md D001-D008.
- Go MCP SDK: github.com/modelcontextprotocol/go-sdk v1.0.0+ (per `research/go-mcp-sdk.md`).
- Go Telegram libraries surveyed: gotgbot (PaulSonOfLars), telegram-bot-api (go-telegram-bot-api).
