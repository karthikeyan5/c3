# C3 Re-Architecture — Design Spec

**Date:** 2026-05-08
**Status:** Proposed — pending Karthi review
**Supersedes:** D006 (Go rewrite), D008 (Go MCP SDK), and the deviation banners across `RESUME.md` / `TODO.md` / `DECISIONS.md`.

## 1. Goal

Take C3 from a hand-tuned local MVP to a clean, distributable, multi-CLI Telegram multiplexer. One public link, two installable plugins (Claude Code + Codex today; more CLIs later), a shared central broker, and an attach UX that works the same way everywhere.

The current implementation works for Karthi's machine. It does not work for "anyone else clones the repo and uses it" and the attach behavior creates duplicate forum topics whenever the user opens a CLI in a non-arogara directory. Both problems are solved by the rebuild.

## 2. Current state

What works today:
- `mvp/broker.py` — single-process daemon, owns the bun Telegram plugin via stdio, fans inbound `notifications/claude/channel` to per-session stubs over `/tmp/c3.sock`, routes `(chat_id, message_thread_id)` to the right stub, applies P1–P5 patches to bun's `server.ts` idempotently at startup, runs STT on inbound voice.
- `mvp/stub.py` — Claude Code-side MCP server that talks to the broker. Auto-attaches by inferred topic name on startup. Ships local tools `attach` and `topics`.
- `mvp/codex_stub.py` — Codex MCP stub on the same broker socket. Inbox-poll model plus optional Codex app-server forwarder. Untracked in git, written by a Codex session.
- `plugin/` — partial Claude Code plugin scaffold (marketplace.json + plugin.json + .mcp.json) with a hard-coded absolute path to `mvp/stub.py`.

What's broken (root causes, with line refs):

- **Eager auto-attach outside `~/arogara/`** — `stub.py:57` `infer_topic_name()` walks up to the nearest `CLAUDE.md` and uses that dir's basename. Only the literal shared root `~/arogara` is excluded. Opening `claude` in `~/Documents/HR-agent-dir` or `~` falls through to `cwd.name` and the broker calls `createForumTopic` with that name before the user types anything. This is how `agent-dir` (id 865) and `karthi` (id 858) ended up in the group.
- **Opportunistic upserts on every inbound** — `broker.py:407` calls `upsert_topic(chat_id, thread_id, None)` for every channel notification. Unknown threads get a placeholder name `topic-N`. This is how `topic-23`, `topic-0`, and `topic-816` ended up in `topics.json`.
- **Hard-coded paths everywhere** — `.mcp.json` files reference `/home/karthi/...`. The plugin can't be installed by anyone else.
- **General-topic id treated as 0** — Telegram assigns the General topic `message_thread_id = 1`, not 0. The broker's routing key `(chat_id, 0)` for groups is wrong; it works for Karthi only because his group has no traffic in General.
- **Two source-of-truth files, no migration story** — `mvp/topics.json`, `mvp/config.json`, and `~/.claude/channels/telegram/.env` together hold state. They live wherever the repo was cloned. A user upgrading the plugin via `/plugin update` would lose them.

## 3. UX contract — what "seamless" means

The attach UX, restated in one paragraph, normative:

A user with the C3 plugin enabled on Claude Code (and/or Codex) has a single bot running on their machine. They `cd` into any directory and start their CLI. If that directory has been attached before, inbound Telegram messages from the mapped topic appear in the CLI automatically and replies go back to that topic. If the directory has not been attached before, the CLI starts unattached. The user types `attach` and the broker creates a forum topic named after the directory's basename, persists the mapping `<absolute-cwd> → <topic>`, and claims it for this session. Future runs in this directory auto-attach. `attach <name>` finds an existing topic by name (or creates one with that name). `attach dm` always routes to the user's personal 1-on-1 chat. If two sessions try to attach to the same mapping at once, the second is told who's holding it and stays unattached. The user never has to think about topic IDs or write to JSON files.

Implications, normative:

- C3 never auto-creates a forum topic. Only `attach` (with or without args) creates topics.
- C3 never auto-deletes a forum topic. Cleanup is manual today; a script is future work.
- C3 stores all per-machine state outside any plugin install dir, so plugin upgrades don't touch state.
- Two plugins (Claude Code and Codex) installed on the same machine share one broker, one bot, one socket, one mapping file. Each session is a separate stub process — but the broker is one daemon.

## 4. Architecture

Three planes, each with one job.

```
                    ┌──────────────────┐
                    │  Telegram Bot    │
                    │  (single token)  │
                    └────────┬─────────┘
                             │ long-poll
                    ┌────────▼─────────┐
                    │  bun server.ts   │   ← unchanged upstream + P1-P5
                    │  (transport)     │
                    └────────┬─────────┘
                             │ stdio
                    ┌────────▼─────────┐    ┌──────────────────┐
                    │  C3 Broker       │◄──►│  XDG state       │
                    │  (singleton, py) │    │  ~/.config/c3/   │
                    └────┬────┬────┬───┘    │  ~/.local/state/ │
                         │    │    │        └──────────────────┘
              /tmp/c3.sock (unix socket)
                         │    │    │
                  ┌──────┘    │    └─────┐
                  │           │          │
              ┌───▼───┐   ┌──▼───┐   ┌──▼────────┐
              │ Claude│   │ Codex│   │ Future CLI│
              │ stub  │   │ stub │   │ stub      │
              └───┬───┘   └──┬───┘   └──┬────────┘
                  │ stdio    │ stdio    │
              ┌───▼───┐   ┌──▼───┐
              │ CC    │   │ Codex│
              │ CLI   │   │ CLI  │
              └───────┘   └──────┘
```

### 4.1 Transport plane (broker → bun)

Unchanged. Broker owns one bun subprocess, applies P1–P5 patches at startup, drains stdout for `notifications/claude/channel`, dispatches to the routing layer.

One change: when the broker requests `getUpdates` (indirectly through bun), opt into `allowed_updates = ["message", "edited_message", "callback_query", "message_reaction"]` so reactions become available to plugins later. Not used in v1, just plumbed.

### 4.2 Routing plane (broker)

Routing key: `(chat_id, message_thread_id)` where `message_thread_id` is the integer Telegram delivers, **including 1 for the General topic and absent for non-forum chats**. The broker treats absent as `0` only as a sentinel inside its own data structures, never communicated to Telegram. Fix the General-topic confusion at the same time as the rebuild.

The `ROUTES` map is in-memory only. It's not persisted because it represents who's currently online — when the broker restarts, all stubs are gone.

### 4.3 Identity / mapping plane (broker, persistent)

This is the new piece. Three persistent JSON files in `~/.config/c3/`:

```
~/.config/c3/
├── config.toml         # bot_token, dm_chat_id, group_chat_id, master_user_id
├── topics.json         # known topics: [{chat_id, topic_id, name}]
└── mappings.json       # cwd → topic claim: {cwd: {chat_id, topic_id, name, last_attached_at}}
```

`mappings.json` is the answer to Karthi's "set default the full directory path against the default" requirement. Keys are absolute, resolved cwd paths. Values point into `topics.json` (well, they duplicate the relevant fields for read speed, but `topics.json` is the source of truth for "does this topic still exist as far as we know").

`mappings.json` is keyed by cwd, not by `(cli, cwd)`. The mapping is shared across CLIs — that's the cross-CLI consistency Karthi asked for. If Claude Code and Codex both open the same directory, they look up the same key. Whichever attaches first holds the topic; the other sees it as already-claimed.

`topics.json` is the local registry of known topics. It is never authoritative — Telegram is. A topic can disappear from Telegram (user deletes it manually) without C3 knowing until the next `editMessageText` 400's. But it's the closest thing we have to a list (the Bot API has no `getForumTopics`). Topics are added to it only when `attach` (with or without args) creates or claims one. **No more opportunistic upserts on inbound.**

Schema for `mappings.json`:

```json
{
  "/home/karthi/arogara/c3": {
    "chat_id": -1003990699908,
    "topic_id": 281,
    "name": "c3",
    "created_at": "2026-04-21T22:00:00Z",
    "last_attached_at": "2026-05-08T06:05:00Z"
  }
}
```

Schema for `config.toml`:

```toml
# C3 broker config. Lives at ~/.config/c3/config.toml.

[telegram]
bot_token   = "1234567:abc..."
dm_chat_id  = 85720317        # user's personal Telegram user-id (positive)
group_chat_id = -1003990699908  # the supergroup C3 creates topics in
master_user_id = 85720317     # only this Telegram user can run admin commands (future)

[broker]
socket_path = "/tmp/c3.sock"  # default, overridable
log_path    = "/tmp/c3-broker.log"
```

State files are NOT in any plugin's install dir — that means plugin upgrades don't touch them. They're not in `${CLAUDE_PLUGIN_DATA}` either, because the Codex plugin can't see Claude's data dir. XDG (`~/.config/c3/`) is the one place both plugins (and any future plugin) agree to read.

### 4.4 Adapter plane (per-CLI stubs)

An adapter is a thin process that:
1. Speaks its host CLI's MCP protocol over stdio.
2. Connects to the broker over `/tmp/c3.sock`.
3. Translates inbound notifications into whatever the host CLI can render.

Common IPC protocol the broker speaks (newline-delimited JSON):

| Direction | Op | Purpose |
|---|---|---|
| stub → broker | `hello` | Stub introduces itself: `{cli, pid, cwd}`. Broker responds with auto-attach state. |
| stub → broker | `server_info` | Get bun's serverInfo + capabilities + instructions for the host CLI's `initialize`. |
| stub → broker | `tools_list` | Get bun's tools list (passthrough). |
| stub → broker | `attach` | `{name?, target?}` — name attaches by name; target=`dm` attaches DM; no args attaches by cwd. |
| stub → broker | `list_topics` | Get all known topics + claim state. |
| stub → broker | `tool_call` | Forward a tool invocation to bun. |
| broker → stub | `hello` | `{auto_attached: bool, mapping?, claim_holder?}` — answers stub's hello. |
| broker → stub | `attached` | `{ok, chat_id, topic_id, name, err?}` — response to attach. |
| broker → stub | `tool_result` | Result from a forwarded tool call. |
| broker → stub | `inbound` | A `notifications/claude/channel` from Telegram, routed to this stub. |
| broker → stub | `topics_list` | Listing response. |
| broker → stub | `error` | Generic error. |

The protocol is the contract. Anyone writing a new adapter implements it.

#### Claude Code adapter

`plugins/c3-telegram/adapter/claude_stub.py`. Behavior:

1. On stdio connect from Claude Code, send `hello` to broker with current `cwd`.
2. Broker responds with auto-attach result.
3. Stub builds `instructions` string for `initialize`: includes one of:
   - "Auto-attached to `<name>` topic. Inbound messages render here as `<channel>` blocks."
   - "Found a saved mapping for this directory but it's already in use by `<cli>` pid `<pid>`. Stay unattached or use `attach <name>` for another topic."
   - "No saved mapping for this directory. Type `attach` to create a topic named `<basename>` and persist the mapping. Or `attach dm` to chat with the user's DM. Or `attach <name>` to claim/create a specific topic."
4. Tools list is broker's bun tools (reply, react, edit_message, download_attachment) plus stub-local `attach` and `topics`.
5. Inbound `notifications/claude/channel` from broker forwards verbatim to Claude Code's stdio. No translation needed — Claude Code natively renders these.

The `attach` tool's `target` parameter:
- omitted → use `basename(cwd)` as the topic name; create if missing; persist mapping.
- `target="dm"` → resolve via `config.toml:dm_chat_id`, claim, do NOT persist mapping (DM is universal).
- `target="<name>"` → find existing topic named `<name>` in pinned group; create if missing; claim; persist mapping for the current cwd.

#### Codex adapter

`plugins/c3-codex/adapter/codex_stub.py`. Same broker IPC, different host translation:

1. On stdio connect from Codex, same `hello` flow.
2. `instructions` string adapted to Codex's behavior — note that Codex doesn't render unsolicited MCP notifications in the TUI today (issues #18056, #17543, #15299 still open).
3. Tools: `c3_attach`, `c3_topics`, `c3_inbox`, `c3_reply`, `c3_codex_forward`.
4. Inbound `notifications/claude/channel` from broker is split into:
   - `notifications/message` log notification (cheap, future-proofs for when Codex starts surfacing them).
   - Buffered in `c3_inbox` for poll-mode.
   - Optional: forwarded to Codex app-server `turn/start` when configured (experimental, but the only push path).

The Codex adapter is forward-compatible. When Codex ships native unsolicited-notification rendering, we delete the inbox tool and the WS forwarder.

#### Future adapters

Any new adapter (Cursor, Aider, plain CLI bot, web UI) implements the IPC protocol against `/tmp/c3.sock`. Nothing in the broker is CLI-specific.

### 4.5 Plugin packaging

Single git repo. Two plugin manifests, one per host CLI. The broker code is bundled in both — small (~30KB), and means each plugin can be installed independently.

```
c3/                                          # public github repo
├── README.md
├── INSTALL.md
├── docs/
│   └── specs/2026-05-08-c3-rearch-design.md  (this doc)
├── .claude-plugin/
│   └── marketplace.json                     # Claude Code marketplace catalog
├── plugins/
│   ├── c3-telegram/                         # Claude Code plugin
│   │   ├── .claude-plugin/plugin.json
│   │   ├── .mcp.json                        # uses ${CLAUDE_PLUGIN_ROOT}
│   │   ├── commands/
│   │   │   └── c3-setup.md                  # /c3-setup interactive setup
│   │   ├── hooks/hooks.json                 # SessionStart: ensure broker reachable
│   │   ├── adapter/claude_stub.py
│   │   └── broker/                          # bundled broker code
│   │       ├── broker.py
│   │       ├── patch_server.py
│   │       ├── patches/
│   │       └── stt/
│   └── c3-codex/                            # Codex plugin
│       ├── .codex-plugin/plugin.json
│       ├── .mcp.json
│       ├── adapter/codex_stub.py
│       └── broker/                          # same bundled broker code (symlink in repo)
└── tools/
    └── migrate_legacy.py                    # one-shot migration from mvp/ → XDG
```

`.mcp.json` for Claude Code:

```json
{
  "mcpServers": {
    "c3-telegram": {
      "command": "python3",
      "args": ["${CLAUDE_PLUGIN_ROOT}/adapter/claude_stub.py"]
    }
  }
}
```

The stub spawns the broker on first connect via `flock`. Singleton-per-machine guaranteed by the existing flock mechanism — keep it.

#### Why both plugins bundle the broker

Considered alternatives:

- **A. Both bundle their own broker.** Wastes ~30KB on a Codex-only or Claude-only install of the other. Wins on simplicity and zero install ordering. Chosen.
- **B. Only one plugin bundles the broker; other depends on it.** Breaks Codex-only users; couples plugin install order. Rejected.
- **C. Broker is a separate pip package.** Adds a Python install step before plugin install. Adds friction. Rejected for v1; could revisit if the broker grows.

The flock guarantees exactly one broker process. Two bundled copies don't conflict — they race for the lock and the loser exits.

The Codex plugin's broker dir can be a symlink to the Claude Code plugin's dir within the repo, so we maintain one source. At install time the marketplace expands the symlink (Claude Code plugin marketplace handles this; Codex's may not — if not, we copy at build time).

### 4.6 Plugin ecosystem (broker-side plugins)

Today: STT lives in `mvp/stt/` and the broker installs a symlink at `~/.claude/channels/telegram/stt-handler.py` so the patched bun plugin can shell out to it.

Future: a broker-side plugin loader scans `~/.config/c3/plugins/` at startup and loads each (Python module with a `register(broker)` function). Plugins can:
- Add middleware for inbound messages (transcription, OCR, translation).
- Add middleware for outbound replies (auto-emoji on completion, etc.).
- Register custom MCP tools that adapters expose to their host CLIs.

For v1, STT is a built-in module, not a plugin. The plugin loader is **deferred**. The architecture leaves room for it (broker is the stable hub, adapters are thin). Document the API as future work in the README.

## 5. Key flows

### 5.1 First install on a fresh machine

1. User clones nothing. They go to Claude Code and run `/plugin marketplace add karthikeyan5/c3`.
2. `/plugin install c3-telegram@c3`.
3. `/reload-plugins`.
4. Stub spawns on first session, tries to connect to `/tmp/c3.sock`, fails, spawns broker.
5. Broker starts, looks for `~/.config/c3/config.toml`, doesn't find it.
6. Broker writes a stub config and exits with a clear stderr message: `c3: not configured. Run /c3-setup in Claude Code to provide bot token and DM chat id.`
7. Stub catches the broker exit, returns an `initialize` response whose `instructions` text says: *"C3 not configured yet. Run `/c3-setup` to set your Telegram bot token and DM chat id, then restart this session."*
8. User runs `/c3-setup`. This is a slash command that prompts for bot token + DM chat id (and optionally group chat id) using `AskUserQuestion`, writes `~/.config/c3/config.toml`, and tells the user to restart their CLI.
9. Subsequent sessions auto-spawn the broker and everything works.

For Codex install: `codex plugin marketplace add github:karthikeyan5/c3`, `codex plugin install c3-codex`. Setup reuses `~/.config/c3/config.toml` if present (which it will be after Claude Code setup). If not present, Codex tells the user to run setup via Claude Code first (or we add a parallel `c3 setup` script for Codex).

### 5.2 `cd` into a fresh project

```
$ cd ~/projects/widget-foo
$ claude
```

1. Stub starts, sends `hello` with `cwd=/home/karthi/projects/widget-foo`.
2. Broker checks `mappings.json`. No entry.
3. Broker replies `{auto_attached: false, no_mapping: true}`.
4. Stub's `instructions` say: *"No C3 mapping for `/home/karthi/projects/widget-foo`. Type `attach` to create topic 'widget-foo' and persist this mapping. Or `attach dm` to chat in the user's DM. Or `attach <name>` for a specific topic."*
5. User types: `attach`.
6. Claude calls the `attach` tool with no args.
7. Stub forwards: `{op: attach, cwd: "/home/karthi/projects/widget-foo"}`.
8. Broker:
   a. `name = basename(cwd) = "widget-foo"`.
   b. Look up `widget-foo` in `topics.json` (filtered to `group_chat_id`). Not found.
   c. Call `createForumTopic(chat_id=group_chat_id, name="widget-foo")`. Get back `topic_id=917`.
   d. Insert into `topics.json`: `{chat_id, topic_id: 917, name: "widget-foo"}`.
   e. Insert into `mappings.json`: `{"/home/karthi/projects/widget-foo": {chat_id, topic_id: 917, name: "widget-foo", ...}}`.
   f. Claim `(chat_id, 917)` for this stub.
9. Stub replies to Claude: *"Attached to 'widget-foo' (chat -100..., thread 917). Future runs in this directory will auto-attach. Default-attached."*

### 5.3 `cd` into a known project

```
$ cd ~/arogara/c3
$ claude
```

1. Stub starts, sends `hello` with `cwd=/home/karthi/arogara/c3`.
2. Broker checks `mappings.json`. Found: `topic_id=281, name="c3"`.
3. Broker checks `ROUTES` for `(chat_id, 281)`. Free. Claims for this stub.
4. Broker replies `{auto_attached: true, name: "c3", chat_id, topic_id: 281}`.
5. Stub's `instructions` say: *"Auto-attached to 'c3' topic. Inbound messages render here as `<channel>` blocks."*
6. User types nothing, just works.

### 5.4 `attach dm` from anywhere

1. User types `attach dm`.
2. Claude calls `attach(target="dm")`.
3. Stub forwards: `{op: attach, target: "dm"}`.
4. Broker:
   a. Read `dm_chat_id` from config.
   b. Claim `(dm_chat_id, 0)` for this stub. (Positive chat_id, no thread.)
   c. **Do NOT update `mappings.json`** — DM is global, not per-cwd.
5. Stub replies: *"Attached to DM with `<user>`. Inbound messages from your direct chat will render here."*

`attach dm` is symmetric: any session, anywhere, can do it. Only one session at a time can hold the DM (singleton enforced by `ROUTES`).

### 5.5 Cross-CLI cwd collision

Session 1 (Claude in `~/arogara/c3`) is auto-attached to topic 281. User opens Codex in the same directory.

1. Codex stub sends `hello` with same cwd.
2. Broker looks up mapping → topic 281. Looks up `ROUTES[(chat_id, 281)]` → already claimed by Claude session 1.
3. Broker replies `{auto_attached: false, mapping: {...}, claim_holder: {cli: "claude", pid: 12345}}`.
4. Codex stub `instructions`: *"Saved mapping points to 'c3' topic but it's currently held by Claude Code (pid 12345). Use `c3_attach(target='<other>')` to claim a different topic, or wait for the Claude session to detach."*
5. User decides. No silent topic creation.

### 5.6 Inbound message routing

Telegram delivers a message in topic 281. bun emits `notifications/claude/channel` with `meta.chat_id=-100..., meta.message_thread_id="281"`. Broker's `notify_handler`:

1. Parse `(chat_id, 281)`.
2. Look up `ROUTES`. Found Claude stub. Forward.
3. Do **not** call `upsert_topic` — gone. The message is routed by live state, not by writing a placeholder.

If no stub is claiming `(chat_id, 281)`:
- Broker sends the cooldown fallback reply on Telegram (existing behavior, works fine).

If the message is from a thread we have a name for in `topics.json` but no live claim, fallback path triggers. We don't auto-claim.

### 5.7 Editing topic names (`forum_topic_edited`)

When a user renames a topic in Telegram, bun's update arrives as a service message with `forum_topic_edited`. The current bun plugin doesn't surface this to the broker (it only emits `notifications/claude/channel` for user text).

For v1, we don't try to track renames. If a user renames `c3` → `claudec` in Telegram, our local `topics.json` and `mappings.json` still say `c3`. The topic_id is stable, so routing still works. The local label drifts.

For a future iteration, add a P6 patch: surface `forum_topic_*` service messages to the broker as a separate notification, broker updates `topics.json` to match. Document as **deferred**.

## 6. Telegram-platform handling

Compiled from the research:

- **No `getForumTopics` exists in Bot API.** Confirmed by tdlib/telegram-bot-api#356. Local `topics.json` is the closest thing to a list. This is fine — we only need to know about topics we care about, and we care about topics we (or the user) created via `attach`.
- **General topic id is 1, not 0.** Fix the broker's General-topic handling. Current `topic_id=0` semantics inside C3 changes meaning: 0 = no topic concept (DM, non-forum group), 1 = General forum topic, >1 = custom forum topic.
- **`forum_topic_created`/`forum_topic_edited`/`forum_topic_closed`/`forum_topic_reopened`/`forum_topic_deleted`** all arrive as service-message fields on `Message`. Not used in v1. Future P6 patch can surface them.
- **`allowed_updates` opt-in.** Current bun plugin probably uses defaults. If we want reactions later, we'd need to add `message_reaction` to `allowed_updates`. Plumb this in v1 as a config knob, set defaults conservatively.
- **Rate limits.** `createForumTopic` is throttled tightly — community reports cluster around "a few per minute, then 429". Not a concern for normal use (Karthi creates ~1 topic per project per machine), but our error path should respect `parameters.retry_after`. Current code doesn't; this is a small fix during the rebuild.
- **Long polling vs webhooks.** Stay on long polling (current). Webhooks need a public HTTPS endpoint and aren't worth it for personal use.

## 7. Migration from current state

Karthi explicitly said he handles legacy topic cleanup. So: we don't migrate `topics.json` entries automatically. We migrate **config**:

`tools/migrate_legacy.py` — one-shot, idempotent:

1. Read `~/.claude/channels/telegram/.env` for `TELEGRAM_BOT_TOKEN`.
2. Read `<repo>/mvp/config.json` for `dm_chat_id`, `group_chat_id`.
3. Write `~/.config/c3/config.toml` with these values, **only if the config doesn't exist**.
4. Print a summary: tokens redacted, ids shown, paths shown.
5. Tell the user to verify and that `mvp/` can now be deleted (but don't delete it).

After migration:
- `~/.config/c3/config.toml` exists.
- `~/.config/c3/topics.json` is empty (Karthi's call — he'll re-attach as he goes).
- `~/.config/c3/mappings.json` is empty.
- The legacy `mvp/topics.json`, `mvp/config.json`, `~/.claude/channels/telegram/.env` are untouched, pending Karthi's manual cleanup.

The new broker reads only from `~/.config/c3/`. The old broker can keep running alongside for a moment while Karthi tests; the flock prevents both from running at once, so he picks which one starts.

## 8. Open questions for Karthi

These are decisions where I made a call but want explicit confirmation:

1. **Plugin name.** Existing scaffold uses `c3-telegram@c3` (plugin `c3-telegram` in marketplace `c3`). I'd keep that for the Claude Code plugin. The Codex plugin would be `c3-codex` in the same marketplace (Codex marketplaces seem to allow this via `github:owner/repo` source). Confirm this naming or pick something else.
2. **`attach` at the shared root (`~/arogara`).** Today the stub returns `None` from `infer_topic_name()` so the session stays unattached. With the new design, `attach` (no args) at `~/arogara` would try to create a topic named "arogara" — which already exists (id 329). So it'd just claim. That seems fine. Confirm.
3. **`attach` at `~`.** Similarly, this would try to claim/create a topic named "karthi". Topic 858 already exists with that name. Probably also fine. Karthi never works from `~` directly, so the question is mostly academic.
4. **State location: `~/.config/c3/` vs `${CLAUDE_PLUGIN_DATA}`.** I chose XDG so Codex and Claude can both find it. Alternative: use `${CLAUDE_PLUGIN_DATA}` and have Codex hard-code that path. Less clean but means everything plugin-related is under `~/.claude/plugins/`. I prefer XDG.
5. **Bundle the broker in both plugins (Option A) vs one plugin (Option B).** I chose A; B breaks Codex-only users.
6. **Setup UX.** I'm proposing `/c3-setup` as a slash command in the Claude Code plugin. Codex doesn't have slash commands the same way; for Codex, we'd document a one-liner to run. Acceptable?
7. **STT pipeline location.** I'm bundling STT inside the broker dir of each plugin. Alternative: STT becomes a separate `c3-stt` plugin that the user installs alongside C3. Cleaner long-term, more setup steps short-term. I lean built-in for v1, plugin for v2.
8. **`forum_topic_edited` (rename) handling.** Deferred. Confirm we're OK with the local label drifting if you rename topics in Telegram, until we add a P6 patch.

## 9. What this design explicitly does NOT include

- Cleaning up legacy topics (Karthi's call).
- Auto-creating topics opportunistically on inbound (removed entirely).
- Auto-deleting topics (manual via Bot API; future tool).
- Broker-side plugin loader (deferred to v2; STT is built-in).
- Per-CLI per-project config files (architecture pushes everything central).
- Webhook mode (stay on long polling).
- Reactions, business connections, paid media (Bot API features we don't need).

## 10. Implementation phases (high-level)

This spec produces an implementation plan via the `writing-plans` skill once approved. Rough phasing for context:

1. **Move state to XDG + write migration tool.** No behavior change for Karthi; old broker still runs. Verifies the migration path.
2. **Refactor broker IPC for `hello` and the new `attach` semantics.** Break old stubs intentionally — bumping the protocol.
3. **Write the new `claude_stub.py` against the new IPC.** Drop eager auto-attach. Drop opportunistic upserts.
4. **Wire the plugin packaging.** Update `.mcp.json` to use `${CLAUDE_PLUGIN_ROOT}`. Marketplace catalog ready for public install.
5. **Port `codex_stub.py`** to the new IPC. Same `attach` semantics.
6. **Add `/c3-setup` command.** First-run UX.
7. **Documentation pass.** README rewrite, INSTALL rewrite, deprecate old TODO/RESUME/DECISIONS deviation banners with a fresh D009.
8. **Public release.** Tag v0.1.0 on the repo.

Phases 1–4 unblock Karthi's daily use. 5–8 are about making it shippable to others.

## 11. Sources

- Claude Code plugin docs: docs.anthropic.com/en/docs/claude-code/plugins.
- Codex MCP docs: developers.openai.com/codex/mcp, /config-reference, /app-server.
- Codex inbound notification status: openai/codex#18056, #17543, #15299 — open.
- Telegram Bot API: core.telegram.org/bots/api, /api-changelog.
- Telegram forums constraint: tdlib/telegram-bot-api#356 (no `getForumTopics`).
- Existing C3 code: `mvp/broker.py`, `mvp/stub.py`, `mvp/codex_stub.py`, `mvp/PATCH_SPEC.md`.
- Existing plugin scaffold: `plugin/.claude-plugin/marketplace.json`, `plugin/plugins/c3-telegram/`.
