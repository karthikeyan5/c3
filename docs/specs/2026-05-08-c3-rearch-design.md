# C3 Re-Architecture — Design Spec

**Date:** 2026-05-08 (revised after first review)
**Status:** Proposed v2 — pending Karthi sign-off on §11 open questions
**Supersedes:** D006 (Go rewrite), D007 (pluggable transport — promoted from "later" to v1 architecture), D008 (Go MCP SDK), and the deviation banners across `RESUME.md` / `TODO.md` / `DECISIONS.md`. A formal D009 will record this when implementation starts.

## 1. Goal

Take C3 from a hand-tuned local MVP into a **distributable, multi-channel, multi-CLI** plugin set.

- **Distributable** — one public github URL, anyone can install.
- **Multi-channel** — Telegram is the only channel today; the architecture admits more (web chat, voice mode, future) without rewrites.
- **Multi-CLI** — Claude Code first, Codex parity, future CLIs through a documented adapter contract.
- **Multi-group** — multiple Telegram supergroups can host C3 topics simultaneously, addressed by name.
- **Seamless attach UX** — sessions in known directories auto-attach; new directories prompt for confirmation; nothing gets created behind the user's back; `attach dm` works anywhere.

The current implementation works for Karthi's machine but: creates duplicate forum topics on every non-arogara session, pollutes `topics.json` with placeholder entries on every inbound, hard-codes `/home/karthi/...` paths in `.mcp.json`, and silently bottlenecks future channels behind the upstream bun plugin's release schedule.

## 2. Alignment with C3's stated direction

This rearch promotes pieces already in the long-term roadmap into the v1 architecture:

| C3 stated goal (README / TODO) | Where it lands in v2 |
|---|---|
| D007 "Pluggable interface — Telegram first, web chat and voice mode later" | §4 channels plane; default channel is `telegram` |
| README §"Key Features" message dedup + debouncing | §7 OpenClaw UX features; debouncing in v1 |
| README §"OpenClaw inspiration" sender id with cross-channel prefix | §4.3 inbound payloads carry `channel` field |
| README §"Routing Modes" group-based (no topics) | §4.2 routing key is `(channel, chat_id, topic_id)` — `topic_id=None` covers no-topics |
| TODO Phase 1 typing indicator | §7 — adopted in v1 |
| TODO Phase 4 "Stream thinking/tool calls" | §7 — adopted in v1 via edit_message progress |
| TODO Phase 3 user access / master TG user | §4.4 master_user_id stored in state but enforcement deferred |
| TODO Phase 2 CLI auto-spawn / multi-CLI | adapter plane is ready for it; auto-spawn deferred |

What stays deferred to later phases: inter-CLI messaging, monitoring dashboard, persistent message history, web/voice channels themselves, master-CLI admin commands, pairing flow.

What changes from the original direction (and why): we drop the **wrap-and-patch** strategy for the official bun Telegram plugin in favor of **forking** it into our repo (§6). Karthi explicitly authorized this in review. The patch system was the right call when we wanted upstream-tracking; with our own UX features (typing, streaming) it becomes a chase. Fork once, own forever, pull useful upstream changes manually.

## 3. UX contract — what "seamless" means (revised)

Normative paragraph:

A user with C3 enabled on Claude Code (and/or Codex) has a single broker daemon running on their machine. They `cd` into any directory and start their CLI. If that directory has been attached before, inbound messages from the mapped channel/topic appear in the CLI automatically. If the directory has not been attached before, the CLI starts unattached. The user types `attach`. The broker replies with a *proposal* — "I'd create a topic named `<basename(cwd)>` in the `<default-group>` group of the `telegram` channel. Confirm, or pass me a topic id" — and the CLI surfaces this to the user. Only on confirmation does the broker call `createForumTopic`. Once attached, the broker persists the mapping `<absolute-cwd> → <channel, chat_id, topic_id>` so the next session in this directory auto-attaches. `attach <name>` finds an existing topic by name (across all known groups in the default channel) or proposes to create one. `attach dm` always routes to the user's personal 1-on-1 chat. `attach --group=<g> <name>` targets a specific group. `attach --topic=<id>` claims an existing topic by id. If two sessions try to attach to the same mapping at once, the second is told who's holding it and stays unattached. The user never has to think about ids, JSON files, or which group a topic lives in.

Implications:

- **Nothing gets created without explicit confirmation.** Even if the LLM is acting autonomously, it has to make a second tool call (`attach(create=true)`) after seeing the proposal.
- **`attach` in a mapped directory is silent and immediate** — no proposal, no extra round-trip. That's the auto-attach case.
- **All operational state — channel configs, group ids, DM id, mappings, topic registry — lives in one user-visible JSON file** (`~/.config/c3/mappings.json`). Karthi's request: be able to see "everything" in one place.
- **Multi-group is first-class.** The user can run topics across multiple supergroups (e.g. one for personal, one for work). Default group resolves bare `attach`; `--group=<g>` overrides.
- **Multi-channel is first-class in the data model**, even though only Telegram is implemented in v1. The mapping schema, IPC protocol, and broker code all carry a `channel` field. Adding a new channel later is "implement the channel module"; nothing else changes.

## 4. Architecture

Four planes. Each has one job.

```
                ┌──────────────────────┐
                │  Channel modules     │
                │  ──────────────────  │
                │  Telegram (v1)       │
                │  Web      (future)   │
                │  Voice    (future)   │
                └──────────┬───────────┘
                           │ stdio (per channel)
                ┌──────────▼───────────┐    ┌─────────────────────┐
                │  C3 Broker           │◄──►│  ~/.config/c3/      │
                │  (singleton, py)     │    │  mappings.json      │
                │  ─ routing           │    │  (channels + groups │
                │  ─ mapping registry  │    │   + DM + topics +   │
                │  ─ confirmation flow │    │   cwd→topic map)    │
                │  ─ debounce/dedup    │    └─────────────────────┘
                └──┬─────┬─────┬───────┘
            /tmp/c3.sock (unix socket)
                   │     │     │
              ┌────▼─┐ ┌─▼──┐ ┌▼────────┐
              │Claude│ │Codex│ │Future   │
              │stub  │ │stub │ │CLI stub │
              └──┬───┘ └─┬──┘ └─┬───────┘
                 │ stdio │      │
              ┌──▼───┐  ┌▼───┐
              │ CC   │  │Codex│
              │ CLI  │  │ CLI │
              └──────┘  └─────┘
```

### 4.1 Channels plane

A channel is a unidirectional-aware message transport. v1 ships **telegram**.

Each channel module implements a small interface to the broker:
- Receives outbound messages from the broker (reply text, edit_message, react, send-typing).
- Emits inbound messages to the broker as a normalized payload: `{channel, chat_id, topic_id, message_id, sender, text, attachments, reply_to_*, ts}`.
- Owns its own configuration (bot token, polling, encryption, whatever).
- Owns its own subprocess/runtime (Telegram is bun + grammY today).

The Telegram channel in v1:
- A **fork** of the official bun Telegram plugin's `server.ts`, copied into `c3/channels/telegram/server.ts`. Patches removed; the equivalents are first-class code now (typing indicator, message_thread_id plumbing, reply_to fields, STT shell-out, no orphan watchdog).
- Spawned by the broker as a subprocess. Communicates with broker over stdio using the same `notifications/claude/channel` method (so we can keep the rich `<channel>` rendering in Claude Code).
- Owns Telegram's bot token, manages getUpdates polling, applies allowed_updates opt-ins, exposes per-method tools (reply, react, edit_message, download_attachment, plus new ones: send_typing, edit_progress).

This is a clean break from "we're a wrapper that patches an upstream library." We become a Telegram bot author with a forked starting point. **Open question §11.1**: do we preserve the upstream commit anchor as a baseline so we can pull useful upstream changes manually, or do we cleanroom-rewrite over time? My recommendation: keep the upstream baseline file (`server.ts.upstream-0.0.6`) for diffing; cherry-pick what's worth keeping; ignore the rest.

### 4.2 Routing plane

Routing key: `(channel, chat_id, message_thread_id)`. All three needed for multi-channel + multi-group + multi-topic. `chat_id` already disambiguates groups (Telegram supergroups have unique negative ids), so multi-group is purely additive — the routing key didn't need to change to support it; the *mapping registry* did.

Telegram General-topic id is **1**, not 0. Routing represents "no topic / non-forum chat" as `topic_id=None` (or 0 in the on-wire JSON, with a clear comment). General topic is a real topic with id 1, treated like any other.

Live `ROUTES` map is in-memory only. Persistence is for mappings, not for live claims.

### 4.3 Mapping registry plane (broker, persistent)

One file: `~/.config/c3/mappings.json`. Mode 600 (contains the bot token). The user can hand-edit. The broker treats it as authoritative on read, atomic-rewrites on update.

Schema:

```json
{
  "$schema_version": 1,
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
      ]
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
  }
}
```

Why one file and not three:

- Karthi explicitly asked: "everything in mappings.json".
- The file is the **operator's view** of C3 — everything they'd want to see or hand-edit is in one place.
- Topics are nested under their channel because cross-channel topic ids are meaningless (a Telegram topic_id is not the same kind of thing as a future Slack channel id).
- Mappings are top-level (not nested under channels) because a cwd maps to exactly one (channel, chat_id, topic_id) at a time.

Why not `${CLAUDE_PLUGIN_DATA}` (which is what the Claude Code research recommended for plugin state):

- Codex plugin can't see Claude's PLUGIN_DATA dir, and we want both plugins reading the same file.
- Plugin uninstall/reinstall would wipe state. XDG survives that.
- The user can find their state without going `find ~/.claude/plugins/data -name '*c3*'`.

Why not `~/.local/share/c3/`:

- `~/.config/c3/` is more conventionally where TOML/JSON configs live; user can `cat` it.
- Either is defensible. Going with `~/.config/c3/` per XDG semantics for "config + small state".

### 4.4 Adapter plane (per-CLI stubs)

An adapter is a thin process that:
1. Speaks its host CLI's MCP protocol over stdio.
2. Connects to the broker over `/tmp/c3.sock`.
3. Translates inbound notifications into whatever the host CLI can render.

Common IPC protocol the broker speaks (newline-delimited JSON):

| Direction | Op | Purpose |
|---|---|---|
| stub → broker | `hello` | Stub introduces itself: `{cli, pid, cwd}`. Broker responds with auto-attach state. |
| stub → broker | `server_info` | Get serverInfo + capabilities + instructions for the host CLI's `initialize`. |
| stub → broker | `tools_list` | Get the broker's exposed tools (channel-specific tools + universal ones). |
| stub → broker | `attach` | `{cwd?, name?, target?, topic_id?, group?, channel?, create?}` — see §5 for semantics. |
| stub → broker | `list_topics` | Get all known topics across all channels + claim state. |
| stub → broker | `tool_call` | Forward a tool invocation to the channel module. |
| broker → stub | `hello_ack` | `{auto_attached, mapping?, claim_holder?}` — answers stub's hello. |
| broker → stub | `attached` | `{ok, channel, chat_id, topic_id, name, group?, needs_confirmation?, proposal?, err?}`. |
| broker → stub | `tool_result` | Result from a forwarded tool call. |
| broker → stub | `inbound` | A normalized inbound message, routed to this stub. |
| broker → stub | `topics_list` | Listing response. |
| broker → stub | `error` | Generic error. |

Host-specific translations:

- **Claude Code adapter** forwards inbound as `notifications/claude/channel` verbatim. Tools list is broker tools + local `attach`/`topics`. Inbound `<channel>` blocks render natively.
- **Codex adapter** forwards inbound as `notifications/message` log + buffers into `c3_inbox` poll tool + optional WS forwarder to Codex app-server. Tools list is `c3_attach` / `c3_topics` / `c3_inbox` / `c3_reply` / `c3_codex_forward`. Designed so the inbox tool is a clean deletion when Codex ships native unsolicited-notification rendering (issues #18056/#17543/#15299 still open as of May 2026).
- **Future CLI adapters** implement the protocol; the broker doesn't care.

### 4.5 Plugin packaging

One git repo, one Claude Code plugin (`c3`), one Codex plugin (`c3-codex` — Codex marketplace expects its own plugin), each bundling the broker code. Plugin name is `c3`, not `c3-telegram` — Telegram is one of multiple channels the plugin can wire up.

```
c3/                                          # public github repo
├── README.md
├── INSTALL.md
├── docs/
│   └── specs/2026-05-08-c3-rearch-design.md
├── .claude-plugin/
│   └── marketplace.json                     # marketplace catalog (Claude)
├── plugins/
│   ├── c3/                                  # Claude Code plugin
│   │   ├── .claude-plugin/plugin.json
│   │   ├── .mcp.json                        # uses ${CLAUDE_PLUGIN_ROOT}
│   │   ├── commands/
│   │   │   ├── c3-setup.md                  # /c3-setup interactive
│   │   │   └── c3-status.md                 # /c3-status diagnostic
│   │   ├── adapter/claude_stub.py
│   │   └── core/                            # bundled broker + channels
│   │       ├── broker.py
│   │       ├── channels/
│   │       │   └── telegram/
│   │       │       ├── server.ts            # forked from upstream
│   │       │       ├── stt-handler.py
│   │       │       └── README-fork.md       # what we changed vs. upstream
│   │       └── stt/                         # STT pipeline (built-in module)
│   └── c3-codex/                            # Codex plugin
│       ├── .codex-plugin/plugin.json
│       ├── .mcp.json
│       ├── adapter/codex_stub.py
│       └── core/                            # symlink-or-copy of plugins/c3/core/
└── tools/
    └── migrate_legacy.py                    # one-shot migration from mvp/
```

`.mcp.json` for Claude Code:

```json
{
  "mcpServers": {
    "c3": {
      "command": "python3",
      "args": ["${CLAUDE_PLUGIN_ROOT}/adapter/claude_stub.py"]
    }
  }
}
```

Stub spawns the broker on first connect via flock. Singleton-per-machine guaranteed by `/tmp/c3-broker.pid`. Both plugins ship their own broker copy; only one runs.

The `core/` directory is shared between both plugins. In the repo, it lives once under `plugins/c3/core/` and `plugins/c3-codex/core/` is a symlink. Build/release tooling can resolve the symlink to a copy at marketplace publish time if either marketplace's installer doesn't preserve symlinks.

### 4.6 Channel-extension architecture (future-proof, not v1)

To add a new channel (web, voice, IRC, whatever) later:

1. Drop a module under `core/channels/<name>/` that:
   - Spawns whatever process owns the channel's transport (server, polling loop, websocket, …).
   - Exposes inbound as the broker's normalized inbound payload.
   - Exposes outbound primitives as MCP-style tool calls (`reply`, `react`, `edit_message`, channel-specific extras).
2. Add a section to `mappings.json:channels.<name>` with channel-specific config.
3. Bump `$schema_version` if the schema needs new fields.
4. The broker, adapters, mappings registry, routing key, and IPC protocol all already accommodate it.

No code changes required in the broker core to add a channel. That's the test for whether the architecture is right.

For v1, only `channels/telegram/` exists. The plumbing for "channel" everywhere (routing key, IPC payloads, mappings schema) is in place.

## 5. Key flows

### 5.1 First install on a fresh machine

1. User runs `/plugin marketplace add karthikeyan5/c3` then `/plugin install c3@c3` then `/reload-plugins`.
2. Stub spawns on next session, connects to `/tmp/c3.sock`, finds it absent, spawns broker.
3. Broker starts, looks for `~/.config/c3/mappings.json`, finds nothing.
4. Broker writes a stub `mappings.json` skeleton (no token, no groups, no DM) with mode 600 and stays alive.
5. Stub gets `auto_attached: false, no_config: true` from the broker on `hello`. Builds `instructions` text: *"C3 not yet configured. Run `/c3-setup` to provide your Telegram bot token, DM chat id, and at least one group chat id, then restart this session."*
6. User runs `/c3-setup` (slash command shipped by the plugin). The command uses `AskUserQuestion` (or follow-up shell prompts) to gather: bot token, DM chat id, group chat id (named, e.g. "main"). Writes `mappings.json:channels.telegram.*`.
7. User restarts the session. Auto-attach (or proposal) works.

For Codex install: `codex plugin marketplace add github:karthikeyan5/c3` then `codex plugin install c3-codex`. If `~/.config/c3/mappings.json` already exists (because Claude was set up first), Codex reuses it. If not, Codex stub instructs the user to run setup via Claude or run a parallel `c3 setup` script.

### 5.2 `cd` into a fresh project — `attach` proposal flow

```
$ cd ~/projects/widget-foo
$ claude
```

1. Stub `hello` with `cwd=/home/karthi/projects/widget-foo`.
2. Broker checks `mappings.mappings`. No entry. Replies `{auto_attached: false, no_mapping: true}`.
3. Stub instructions: *"No mapping for `/home/karthi/projects/widget-foo`. Type `attach` to set one up."*
4. User types: `attach`.
5. Claude calls `attach` tool with no args.
6. Stub forwards `{op: attach, cwd: "/home/karthi/projects/widget-foo"}` to broker.
7. Broker computes proposal:
   - Default channel: `telegram`.
   - Default group within telegram: `main` (per `default_group`).
   - Proposed name: `widget-foo` (basename of cwd).
   - No existing topic with that name in that group.
8. Broker replies `{ok: false, needs_confirmation: true, proposal: {channel: "telegram", group: "main", name: "widget-foo", action: "create"}}`.
9. Stub returns to Claude: *"No mapping for this directory. I'd create a new topic `widget-foo` in the `main` Telegram group. Should I create it? Reply yes to create, or pass an existing topic id with `attach(topic_id=<n>)` to claim instead, or `attach(create=false)` to abort."*
10. Claude shows the user. User says "yes".
11. Claude calls `attach(create=true)`.
12. Stub forwards `{op: attach, cwd: ..., create: true}`.
13. Broker:
   a. Calls `createForumTopic(chat_id=group.main.chat_id, name="widget-foo")`. Gets back `topic_id=917`.
   b. Inserts into `channels.telegram.topics`: `{chat_id, topic_id: 917, name: "widget-foo", group: "main"}`.
   c. Inserts into `mappings.mappings`: full mapping entry.
   d. Atomic-rewrites `mappings.json`.
   e. Claims `(telegram, chat_id, 917)` for this stub.
14. Stub replies to Claude: *"Attached to 'widget-foo' (telegram, group main, thread 917). Future runs in this directory will auto-attach. Default-attached."*

If user instead says "use 281":
- Claude calls `attach(topic_id=281)`.
- Broker looks up topic 281 in `topics`. If present, claims it and persists the mapping with the existing topic. If absent — error: *"Topic 281 isn't in our registry. Confirm with `attach(topic_id=281, force=true)` to claim it anyway, or pass a topic name."*
- The `force=true` path appends to `topics` opportunistically based on the user's assertion. Documented as "trust the user".

### 5.3 `cd` into a known project — silent auto-attach

```
$ cd ~/arogara/c3
$ claude
```

1. Stub `hello` with cwd.
2. Broker finds mapping → `(telegram, -100..., 281, "c3", "main")`.
3. Broker checks `ROUTES[(telegram, -100..., 281)]`. Free. Claims.
4. Replies `{auto_attached: true, mapping: {...}}`.
5. Stub instructions say *"Auto-attached to 'c3' topic. Inbound messages render here as `<channel>` blocks."*
6. No prompts, no confirmation. The mapping was the user's prior consent.

### 5.4 `attach <name>` — explicit name

User in any cwd types `attach sthapati`.

1. Claude calls `attach(name="sthapati")`.
2. Stub forwards `{op: attach, name: "sthapati"}` (no cwd because not auto-attaching by cwd, but stub still sends cwd in the payload — the broker uses it to persist the mapping).
3. Broker looks up `sthapati` in `channels.telegram.topics` filtered by default group. Found: topic 207.
4. Free in ROUTES. Claims. Persists mapping for `cwd → topic 207`.
5. Replies `attached`.

If `sthapati` not found: standard proposal flow, but the proposed name is `"sthapati"` instead of basename(cwd).

### 5.5 `attach dm`

User in any cwd types `attach dm`.

1. Claude calls `attach(target="dm")`.
2. Broker looks up `channels.telegram.dm_chat_id`. Resolves to positive chat id.
3. Claims `(telegram, dm_chat_id, None)`. **Does NOT update `mappings.mappings`** — DM is universal, not per-cwd.
4. Replies `attached`.

`attach dm` never proposes/confirms; the DM target is fixed.

### 5.6 Multi-group: `attach <name> --group=work`

User types `attach feature-x --group=work`.

1. Claude calls `attach(name="feature-x", group="work")`.
2. Broker resolves `channels.telegram.groups.work.chat_id`. If `work` isn't defined: error *"Unknown group 'work'. Known: main. Add via /c3-setup."*
3. Look up `feature-x` in topics filtered by group `work`. Found or not — proceed via existing flow.

### 5.7 Cross-CLI cwd collision

Session 1 (Claude in `~/arogara/c3`) auto-attached to topic 281. User opens Codex in same dir.

1. Codex stub `hello` with same cwd.
2. Broker finds mapping → topic 281. ROUTES already claims it for Claude pid 12345.
3. Replies `{auto_attached: false, mapping: {...}, claim_holder: {cli: "claude", pid: 12345}}`.
4. Codex instructions: *"Saved mapping points to 'c3' topic but it's currently held by Claude Code (pid 12345). Use `c3_attach(target='<other>')` to claim a different topic, or wait for the Claude session to detach."*
5. No silent topic creation, no claim theft.

### 5.8 Inbound message routing

Telegram delivers a message in topic 281. bun (our forked server.ts) emits an inbound payload to the broker over stdio:

```json
{
  "channel": "telegram",
  "chat_id": -1003990699908,
  "topic_id": 281,
  "message_id": 868,
  "sender": {"username": "skarthi", "user_id": 85720317},
  "text": "...",
  "attachments": [],
  "reply_to": null,
  "ts": "2026-05-08T06:05:29Z"
}
```

Broker:
1. Apply debounce window (500ms-3s configurable, see §7).
2. Look up `ROUTES[(telegram, -100..., 281)]`. Found: Claude stub.
3. Forward as `notifications/claude/channel` to the stub. (Codex adapter would translate to `notifications/message` + inbox.)
4. **Do NOT touch `topics` or `mappings`** — no opportunistic upserts.

If no stub claims the route: cooldown fallback reply (existing behavior, kept).

## 6. Telegram channel implementation (forked)

Status of upstream patches under the new fork model:

| Patch | Old strategy | New under fork |
|---|---|---|
| P1 inbound `message_thread_id` | overlay anchor | first-class field in our server.ts |
| P2a-d reply tool `message_thread_id` | overlay anchors | first-class arg + per-send option |
| P3 disable orphan watchdog | overlay anchor (replace body with no-op) | watchdog deleted from our copy |
| P4 inbound reply_to fields | overlay anchor | first-class fields in our server.ts |
| P5 voice handler STT shell-out | overlay anchor (re-add upstream-removed STT) | first-class voice handler |
| ` patch_server.py` machinery | runs at broker start | **deleted** — no patches to apply |

We keep `server.ts.upstream-0.0.6` as a baseline file in the repo for diffing / cherry-picking future upstream changes. We **don't** auto-apply anything from upstream.

New first-class features added to our server.ts (see §7):

- `send_typing` tool — sends Telegram chat action so the user sees "typing…".
- `edit_progress` tool — creates a placeholder message and updates it on each progress beat (mapped to existing `editMessageText`).
- Inbound debouncing window (configurable per chat, default 1.5s) — short bursts collapse into a single inbound payload.
- Inbound dedup by `(chat_id, message_id)` — getUpdates retries / restarts don't replay messages.

Telegram-specific facts the rebuild encodes:

- General topic id is **1**, not 0. Routing handles 1 like any other id.
- Bot API has no `getForumTopics`. Local `topics` registry is the source of truth.
- `forum_topic_created`/`forum_topic_edited`/`*closed/reopened` arrive as service-message fields. **For v1 we don't act on them** (manual rename means the local label drifts; Karthi's call to defer). Plumbed but inert.
- `allowed_updates` opted in for `message`, `edited_message`, `callback_query`, `message_reaction`. Reactions plumbed but not exposed in v1 tools.
- `createForumTopic` rate-limited tightly. Error path respects `parameters.retry_after`.

## 7. OpenClaw-inspired UX features (top 3, in v1)

Karthi asked for three features from OpenClaw's user-update repertoire. Picked:

### 7.1 Typing indicator

When the agent is processing (any tool call in flight that takes longer than ~500ms), the channel sends `sendChatAction(action="typing")` to Telegram every 4s (Telegram caches the action for 5s). The user sees a continuous "typing…" indicator.

Implementation: broker tracks "is this stub currently busy" via an in-flight tool-call counter. When it crosses 0→1, broker calls the channel's `send_typing` tool with the relevant `(chat_id, topic_id)`. Re-fired on a 4s interval until in-flight returns to 0.

### 7.2 Streaming progress (edit_message-based)

A new MCP tool `edit_progress(text)` exposed to the agent. First call creates a placeholder message in the topic; subsequent calls edit that message. The broker tracks the placeholder message_id per (chat_id, topic_id, agent-session). On agent turn completion, the placeholder either stays (if user saw it as the final result) or gets edit-replaced with the final reply.

Use case: long-running tool (codebase scan, large diff, build). Agent calls `edit_progress("scanning files...")`, then `edit_progress("found 47 hits, summarizing...")`, then a final `reply(...)`. User sees one message with a live progress trail, not a wall of pings.

Limit: edits don't trigger push notifications; on turn completion, broker explicitly sends a fresh `reply` (not `edit_progress`) so the user's device pings.

### 7.3 Inbound debouncing

When a user sends multiple messages rapidly (voice burst, multi-line typing), the broker buffers inbound messages keyed by `(channel, chat_id, topic_id)` for 1.5s after each new message. After the window closes with no new arrivals, the broker delivers a single `notifications/claude/channel` payload containing all buffered messages concatenated, with the latest `message_id` as the canonical id.

Configurable per-chat via `mappings.json:channels.telegram.groups.<g>.debounce_ms` (default 1500). DM debounce: same default.

What this is NOT: **dedup**. Dedup is a separate concern (drop duplicate `message_id`). Both are useful; dedup is trivially cheap. Implement both — debounce has the user-visible value; dedup is hygiene.

### Why these three, not others

OpenClaw also has session-based routing, multi-turn ping-pong, fire-and-forget vs wait modes, sender-id prefixes. These are inter-agent-messaging features; they only matter once we add CLI-to-CLI messaging, which is TODO Phase 4. Dropping them from v1.

## 8. Migration from current state

Karthi handles legacy topic cleanup. We migrate config:

`tools/migrate_legacy.py`, idempotent:

1. Read `~/.claude/channels/telegram/.env` for `TELEGRAM_BOT_TOKEN`.
2. Read `c3/mvp/config.json` for `dm_chat_id`, `group_chat_id`.
3. If `~/.config/c3/mappings.json` exists, error out — refuse to overwrite.
4. Otherwise, write a fresh `mappings.json` skeleton with:
   - `channels.telegram.bot_token` from .env
   - `channels.telegram.groups.main = {chat_id: <legacy>, title: "(migrated)"}`
   - `channels.telegram.default_group = "main"`
   - `channels.telegram.dm_chat_id = <legacy>`
   - empty `topics`, empty `mappings`
5. Print summary, set mode 600.
6. Tell user to verify and that they can delete `c3/mvp/config.json` once they're happy.

Old broker can keep running alongside new for testing — flock prevents both at once, so user picks which one starts.

## 9. Plugin ecosystem (deferred from v1)

A future broker plugin loader can scan `~/.config/c3/plugins/` and load Python modules that hook the broker's middleware (inbound transforms, outbound transforms, channel extensions, custom tools). For v1: STT is a built-in module, not a plugin. Architecture leaves room — broker is the stable hub, channel modules are the natural extension axis, broker tools are the registry.

Document the plugin API as future work in README. Don't implement.

## 10. What v2 explicitly does NOT include

- Cleaning up legacy topics (Karthi's call).
- Auto-creating topics opportunistically on inbound (removed entirely).
- Auto-deleting topics (manual via Bot API; future tool).
- `forum_topic_edited` rename tracking (defer, plumbed-but-inert).
- Broker-side plugin loader (defer to v2.x; STT stays built-in).
- Per-CLI per-project config files (architecture pushes everything central).
- Webhook mode for Telegram (long-polling stays).
- Reactions, business connections, paid media surfacing (Bot API features we don't need yet, though `message_reaction` is plumbed via `allowed_updates`).
- Auto-spawn of CLIs (TODO Phase 2 — adapter plane is ready, no code yet).
- Inter-CLI messaging, master-CLI admin commands, pairing flow, monitoring dashboard (later phases).

## 11. Open questions for Karthi

These are the calls left after your first review. Listed in order of impact:

1. **Upstream baseline retention.** When we fork the bun server.ts, do we keep `server.ts.upstream-0.0.6` checked in alongside as a baseline file (for future cherry-picks), or do we go cleanroom and never reference upstream again? My recommendation: keep the baseline. Cheap insurance.
2. **Channel-per-cwd uniqueness.** A cwd can map to exactly one (channel, chat_id, topic_id). If you wanted both Telegram AND a future Slack channel notifying for the same project, you'd need to extend mappings to a list per cwd. I'm not building that; one-to-one only. Confirm.
3. **`attach <name>` cross-group resolution.** When you type `attach feature-x` without `--group`, do we (a) only search the default group, (b) search all groups and disambiguate if found in multiple, or (c) match the first? My choice: **(a) default group only**. To target other groups, use `--group=<g>`. Confirm.
4. **`force=true` for unknown topic ids.** When user passes `attach(topic_id=N)` and N isn't in our registry, do we (a) refuse, (b) accept and add to registry trusting the user, or (c) try `getChat`/`getForumTopicIcon` to validate first? My choice: **(a) refuse without `force=true`, (b) trust with force**. Telegram has no validation API, so (c) doesn't really exist. Confirm.
5. **Setup UX on Codex side.** Claude Code gets `/c3-setup` slash command. Codex doesn't have slash commands the same way. My choice: ship a `c3-setup` script in the Codex plugin that the user runs once: `python3 ~/.codex/plugins/cache/.../c3-setup.py`. Acceptable, or do you want a Codex-native interactive flow?
6. **STT location.** STT stays bundled inside `core/channels/telegram/stt-handler.py` (Telegram-specific). When a future channel needs STT (voice-mode channel), refactor at that point. Confirm.

## 12. Implementation phases (high-level)

This spec produces an implementation plan via the `writing-plans` skill once approved. Rough phases:

1. **Mappings refactor + state migration.** Create `~/.config/c3/mappings.json` schema and writer, write `tools/migrate_legacy.py`. Old broker still runs.
2. **Broker IPC v2.** New `hello` op, new `attach` proposal flow, debounce + dedup, multi-group support. Old stubs intentionally break — bumping the protocol.
3. **Telegram channel fork.** Take upstream `server.ts` snapshot, delete patch_server.py, port P1-P5 as first-class code, add `send_typing` and `edit_progress` tools.
4. **Claude Code adapter rewrite.** New `claude_stub.py` against the new IPC. Drop `infer_topic_name`. Drop opportunistic upserts. Plugin packaged as `c3@c3` with `${CLAUDE_PLUGIN_ROOT}` paths.
5. **Codex adapter port.** New `codex_stub.py` against the same IPC.
6. **`/c3-setup` and `/c3-status` slash commands.**
7. **Documentation pass.** README rewrite, INSTALL rewrite, retire deviation banners with formal D009.
8. **Public release.** Tag v0.1.0 on the public repo.

Phases 1-4 unblock Karthi's daily use. 5-8 are about shippability to others.

## 13. Sources

- Claude Code plugin docs: docs.anthropic.com/en/docs/claude-code/plugins.
- Codex MCP docs: developers.openai.com/codex/mcp, /config-reference, /app-server.
- Codex inbound notification status: openai/codex#18056, #17543, #15299 — open.
- Telegram Bot API: core.telegram.org/bots/api, /api-changelog.
- Telegram forums constraint: tdlib/telegram-bot-api#356 (no `getForumTopics`).
- OpenClaw messaging features: c3/README.md §"Inspiration: OpenClaw's Message Tool".
- Existing C3 code: `mvp/broker.py`, `mvp/stub.py`, `mvp/codex_stub.py`, `mvp/PATCH_SPEC.md`.
- Existing plugin scaffold: `plugin/.claude-plugin/marketplace.json`, `plugin/plugins/c3-telegram/`.
- C3 stated direction: README.md §"Key Features (Full Vision)", TODO.md Phases 1-4, DECISIONS.md D001-D008.
