# C3 Re-Architecture — Design Spec

**Date:** 2026-05-08 (v5 — fully Go after Codex POC review)
**Status:** Approved. **Go end-to-end** for broker, channels, all CLI adapters, launchers, and operational tooling. The only Python in C3 is the existing whisper STT pipeline, invoked from a Go plugin shim via subprocess — and even that's a plugin, not a core dependency. The Codex bridge architecture in this spec is informed by a Python POC that proved the loop end-to-end (send/receive Telegram + voice transcription on Codex's TUI), but the spec describes a clean Go implementation; no part of the POC carries forward.
**Reaffirms:** D006 (Go for daemon and stubs), D007 (pluggable transport — promoted to v1), D008 (official Go MCP SDK).
**Supersedes:** the deviation banners across `RESUME.md` / `TODO.md` / `DECISIONS.md` (Python wrapper MVP). A formal D009 will record the v1-MVP-superseded note when implementation starts.

## 1. Goal

Take C3 from a hand-tuned Python MVP into a **distributable, multi-channel, multi-CLI** plugin set, written in **Go**, with a **plugin extension system** so STT and future capabilities slot in cleanly.

- **Distributable** — one public github URL, anyone can install via Claude Code or Codex plugin marketplace.
- **Go end-to-end** — broker, all channels, all CLI adapters (Claude, Codex), launchers (the `codex` shim that intercepts the user's command), and operational tooling (migrate-legacy, install-codex-shim) are all Go. The **only** Python that runs is the whisper STT pipeline, called by a Go plugin shim via subprocess — and that's a swappable plugin, not part of the core. New plugins can be written in any language since they speak a defined wire protocol; "Go everywhere" is the rule for first-party code.
- **Multi-channel** — Telegram is the only channel today; web chat and voice mode were always D007's destination, now plumbed into v1's data model and code paths.
- **Multi-CLI** — Claude Code first, Codex parity, future CLIs through a documented adapter contract.
- **Multi-group** — multiple Telegram supergroups can host C3 topics simultaneously, addressed by name.
- **Seamless attach UX** — sessions in known directories auto-attach; new directories prompt for confirmation; nothing gets created behind the user's back; bare topic ids get validated against Telegram and accepted if real; `attach dm` works anywhere.
- **Plugin extension system** — STT is a plugin in v1, not a built-in. Defined hook points let other plugins drop in without core changes.

The current Python MVP works for Karthi but creates duplicate forum topics on every non-arogara session, hardcodes `/home/karthi/...` paths, bottlenecks future channels behind the upstream bun plugin's release schedule, and was written as a temporary wrapper that was never meant to scale across users.

## 2. Alignment with C3's stated direction

This rearch is mostly a **return** to the original direction (D006 Go, D007 pluggable transport, D008 Go MCP SDK), having spent April pivoting through a Python wrapper to ship something working. Promoting the right pieces from the long-term roadmap into v1's architecture:

| C3 stated goal (README / TODO / DECISIONS) | Where it lands in v5 |
|---|---|
| D006 "Go for daemon and MCP stubs" | reaffirmed without exception; broker, both adapters, codex launcher all Go |
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
              ┌──────┐  ┌─────────────┐  ┌──────────┐
              │Claude│  │Codex bridge │  │Future CLI│
              │adapter  │(Python POC) │  │adapter   │
              │(Go)   │ │ shim →       │ │           │
              │       │ │ supervisor → │ │           │
              │       │ │ stub.py      │ │           │
              └──┬───┘  └─┬──────────┘  └─┬────────┘
                 │ stdio  │ stdio + WS    │
              ┌──▼───┐  ┌─▼─────────────┐
              │ CC   │  │ Codex TUI     │
              │ CLI  │  │ (--remote)    │
              └──────┘  └───────────────┘
```

The Codex bridge is a three-piece chain (shim → supervisor → stub) speaking to the broker over the same `/tmp/c3.sock` IPC; the contract is identical to the Claude adapter's, the implementation language is just Python because the POC works.

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
- **Codex adapter** is two Go binaries that together provide the same end-to-end behavior the Python POC proved out (a `codex` invocation transparently puts the user inside a TUI whose conversation stream is bidirectionally bridged to a Telegram topic, voice-transcribed inbound included):

  - **`codex`** (built from `cmd/codex/main.go`, installed on the user's `PATH` ahead of the real `codex` binary). This is the launcher. It does five things in order on every invocation:
    1. **Find the real Codex executable.** Walk `PATH` skipping `os.Args[0]` (so we don't recurse into ourselves). If nothing matches, glob `~/.nvm/versions/node/*/lib/node_modules/@openai/codex/bin/codex.js`. Honor `C3_CODEX_REAL` env override for testing.
    2. **Decide whether to bypass.** A defined set of subcommands (`exec`, `e`, `review`, `login`, `logout`, `mcp`, `plugin`, `mcp-server`, `app-server`, `completion`, `update`, `sandbox`, `debug`, `apply`, `a`, `cloud`, `exec-server`, `features`, `help`) plus `-h`/`--help`/`-V`/`--version` plus any argv already containing `--remote` (anti-recursion guard) → exec real codex with the user's argv unchanged. `codex resume`, `codex fork`, and bare `codex` go through the bridge.
    3. **Infer the topic.** Walk from cwd up to the nearest `CLAUDE.md`; the basename of that directory is the topic name. Fallback to `basename(cwd)` if no `CLAUDE.md` is found. `C3_ATTACH_NAME` env overrides.
    4. **Start or reuse a Codex app-server.** Default URL `ws://127.0.0.1:8766`. Maintain a metadata file at `/tmp/c3-codex-app-server.json` with the **C3 signature** `(cwd, topic, adapter_path)` of the running app-server. If port 8766 is reachable AND the metadata signature matches, reuse it. If reachable but signature mismatches (stale app-server from a different cwd or topic), **fall forward** to the next free port in `[8767, 8767+50)`. If unreachable, start a new app-server: `<real-codex> <mcp-config-args> app-server --listen <ws-url>`, redirect stdout/stderr to `/tmp/c3-codex-app-server.log`, wait for the TCP port to open (15s timeout), write the metadata file with our signature.
    5. **Launch the visible TUI** with `<real-codex> <mcp-config-args> --remote <ws-url> -C <cwd> <user-argv>` and exit with the TUI's exit code.

       The `<mcp-config-args>` are passed to **both** the app-server and the TUI as repeated `-c` flags. They register a stdio MCP server named `c3_codex`:
       ```
       -c mcp_servers.c3_codex.command="<absolute path to c3-codex-adapter>"
       -c mcp_servers.c3_codex.args=[]
       -c mcp_servers.c3_codex.env.C3_CODEX_APP_SERVER_WS="<ws-url>"
       -c mcp_servers.c3_codex.env.C3_CODEX_CWD="<cwd>"
       -c mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE="1"
       -c mcp_servers.c3_codex.env.C3_ATTACH_NAME="<topic>"
       -c mcp_servers.c3_codex.enabled=true
       ```

       The app-server-side config is the load-bearing one — under `--remote`, the **app-server owns MCP server startup**, not the visible TUI. Passing the config only to the TUI is too late; the adapter spawns inside the app-server's environment, which would have no `C3_*` envs without this step.

  - **`c3-codex-adapter`** (built from `cmd/c3-codex-adapter/main.go`). MCP stdio server, spawned by the Codex app-server per the `mcp_servers.c3_codex` config above. On startup it:
    1. Connects to the broker at `/tmp/c3.sock` (spawning the broker if missing, same flock pattern the Claude adapter uses).
    2. Sends `hello{cli="codex", pid, cwd: $C3_CODEX_CWD}`. If the broker has a mapping for that cwd, claims it. If not, reads `C3_ATTACH_NAME` and either auto-attaches (if the topic exists in the default group) or surfaces a proposal in `instructions` for the agent.
    3. Exposes five MCP tools to Codex:
       - `c3_attach(target?)` — attach this Codex session to a topic. Same proposal-flow semantics as the Claude adapter's `attach`.
       - `c3_topics()` — list known topics + claim state.
       - `c3_inbox(limit, ack)` — drain buffered inbound messages. The fallback path for environments where the WS forwarder isn't viable.
       - `c3_reply(text, files?, parse_mode?)` — send a reply through the attached topic. If local `bound` state was lost (stub restarted while broker held the claim), recover it from the broker's claim before erroring out.
       - `c3_codex_forward(app_server_ws, thread_id?)` — debugging/manual override; gated by either `C3_CODEX_REMOTE_BRIDGE=1` (set by the launcher) or `C3_CODEX_ALLOW_MANUAL_FORWARD=1` (deliberate-debug). Refused otherwise — split-brain guard against attaching forwarding to a stock `codex resume` whose visible TUI isn't `--remote`-bound.

  - **Inbound forwarding** (the path that delivers Telegram messages into the Codex turn stream): every inbound from the broker is buffered for `c3_inbox` AND, when forwarding is enabled, also pushed to the Codex app-server via WebSocket. The flow:
    1. `websocket.Dial(ws_url, nil)` — Go's `gorilla/websocket` (or stdlib equivalent) does **not** set `Origin` by default, which is what the app-server needs (rejects with 403 if Origin is set to a non-localhost value).
    2. Send `initialize` JSON-RPC with `clientInfo` (`c3-codex-bridge`, version) and capabilities including `optOutNotificationMethods` for `item/agentMessage/delta`, `item/reasoning/textDelta`, `item/reasoning/summaryTextDelta` (we don't render these, the user's TUI does).
    3. Send `notifications/initialized`.
    4. **Discover thread.** Call `thread/loaded/list` (limit 20). If exactly one thread is loaded → use it. If multiple → call `thread/list` filtered by `cwd: $C3_CODEX_CWD` with `useStateDbOnly: true`, intersect with the loaded set, pick the most recent.
    5. `thread/resume` with the chosen thread id and `excludeTurns: true`.
    6. `thread/turn/start` with the inbound text formatted as: `"Telegram message from <sender> (chat=<chat_id> thread=<topic_id>)\n<text>"`. Voice messages arrive already transcribed by the Telegram channel's STT plugin; the adapter just sees them as text content like `[Transcribed voice]: ...`.
    7. Close the WebSocket. (Each inbound opens its own short-lived connection — the app-server doesn't expect a long-lived MCP-over-WS session from us; a new connection per turn is the contract.)

    The forward worker is a single goroutine drained from a queue, retrying with a 2s backoff if the WebSocket fails. This way the inbox tool always works as a fallback even if Codex's WS API is down.

- **Future CLI adapters** implement the same broker IPC; the broker is CLI-agnostic.

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

**Claude Code side:**

1. User runs `/plugin marketplace add karthikeyan5/c3`, then `/plugin install c3@c3`, then `/reload-plugins`.
2. User runs `/c3-build` (slash command shipped by the plugin) once. It runs `go install ./cmd/...` in the plugin source dir; binaries land in `$GOBIN`.
3. The plugin's `.mcp.json` references the adapter by name (e.g. `command: "c3-claude-adapter"`), assuming `$GOBIN` is on `$PATH`.
4. On the next session, the Go adapter starts, tries `/tmp/c3.sock`, fails, spawns `c3-broker` (also via `$PATH`).
5. Broker starts, looks for `~/.config/c3/mappings.json`, doesn't find it. Writes a stub skeleton (mode 600), keeps running.
6. Adapter's `hello_ack` says `no_config: true`. Adapter's `instructions` text: *"C3 not yet configured. Run `/c3-setup` to provide your Telegram bot token, DM chat id, and at least one group chat id."*
7. User runs `/c3-setup`. Slash command uses `AskUserQuestion` to gather: bot token, DM chat id, group chat id (named, e.g. "main"). Writes `mappings.json:channels.telegram.*`. Tells user to restart the session.
8. After restart, auto-attach (or proposal) works.

**Codex side:**

1. User runs `codex plugin marketplace add github:karthikeyan5/c3`, then `codex plugin install c3-codex`. The Codex marketplace plugin is a thin manifest with a `SETUP.md` that the agent reads.
2. `SETUP.md` instructs the agent (or user) to run **`c3-broker install-codex-shim`**, a subcommand of the Go broker binary that idempotently:
   a. Resolves `$GOBIN/codex` (the C3 launcher binary built by `/c3-build`).
   b. Creates `~/.local/bin/codex` as a symlink to it (replacing only if the existing target is already our binary).
   c. Walks `~/.nvm/versions/node/*/bin/` and creates the same symlink in each version's bin dir. Long-running shells hash `codex` to the NVM path — without this, existing terminals bypass the bridge entirely.
   d. Verifies `~/.config/c3/mappings.json` exists (created by Claude-side `/c3-setup`, or runs the equivalent gather-and-write flow if missing).
   e. Verifies the broker is reachable at `/tmp/c3.sock`, starts it if not.
   f. Prints a one-line status with each symlink's path so the user can audit.
3. From now on, running `codex` (from any shell — old or new) routes through the C3 launcher, which manages the app-server, spawns the adapter, and bridges inbound Telegram into Codex turns. The user types `codex` exactly as they always have; nothing else changes about their workflow.
4. The Codex side reuses `~/.config/c3/mappings.json` from the Claude-side setup. If neither side has run setup yet, `c3-broker install-codex-shim` prompts (interactively or via the agent) for the same fields the `/c3-setup` slash command would gather.

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
- Cleanroom STT rewrite (Go shim → Python whisper subprocess; the working pipeline keeps working). STT is the only Python in C3.

## 11. Resolved questions (no opens left)

All Karthi calls:

- §11.1 ⇒ cleanroom; no upstream baseline file.
- §11.2 ⇒ one cwd → one mapping.
- §11.3 ⇒ default-group-first then cross-group search with disambiguation; only when no mapping.
- §11.4 ⇒ validate via Bot API call; accept if valid; refuse with actual error if not. No `force=true` flag.
- §11.5 ⇒ Codex setup is agent-driven via `SETUP.md` + a Go helper binary.
- §11.6 ⇒ STT is a v1 plugin, not built-in. Plugin architecture pulled into v1.
- §11.A ⇒ `github.com/PaulSonOfLars/gotgbot/v2`.
- §11.B ⇒ Build from source on first install. Distribution is github-only — users `git clone` the plugin source into the Claude Code plugin cache and we compile locally. Go ≥1.22 is a documented prereq in INSTALL.md.
- §11.C ⇒ Resolved by §11.B's choice. With `go install` / `go build`, binaries land at `$GOBIN` (or wherever the build outputs). The `.mcp.json` `command` references binaries by name (e.g. `c3-claude-adapter`), assuming the user's PATH includes `$GOBIN`. A `/c3-build` slash command (Claude Code) runs `go install ./cmd/...` from the plugin source. Codex's `SETUP.md` instructs the agent to do the same. First-session friction: user installs plugin → runs `/c3-build` once → restart. Acceptable for v1.
- §11.D (revised v5) ⇒ Codex side is **Go** (`cmd/codex/main.go` for the launcher, `cmd/c3-codex-adapter/main.go` for the MCP server). v5 reverses v4's Python preservation. The Python POC's job was to prove the loop end-to-end (Telegram ↔ Codex turn stream + voice transcription); it succeeded; v5 takes the architecture (launcher with bypass list, app-server-owns-MCP, port fallback via `/tmp/c3-codex-app-server.json` signature, WebSocket forwarder with discovered thread, split-brain guard envs) and writes it natively in Go. No Python in the Codex bridge.

## 12. Implementation phases

This spec produces an implementation plan via the `writing-plans` skill. Rough phases:

1. **Repo skeleton + Go modules.** `go mod init`, broker / adapters / channels / plugins package layout. `Makefile` for cross-compile.
2. **Mappings registry.** Read/write/validate `~/.config/c3/mappings.json`. Migration tool. Atomic rewrite.
3. **Broker core + IPC.** Unix socket server, IPC protocol v2, flock singleton, basic routing map.
4. **Telegram channel.** Cleanroom Go implementation against the Channel interface. Long-poll, inbound emit, outbound tools. STT plugin gets the voice hook.
5. **Plugin host + STT plugin.** Hook system, `OnVoiceReceived`, Python subprocess shim.
6. **Claude Code adapter.** MCP stdio server, claim-per-cwd, attach proposal flow, all attach modes (no-args, name, dm, topic-id, group-override).
7. **Debounce + dedup, typing indicator, edit_progress.**
8. **Codex bridge in Go.** Implement `cmd/codex/main.go` (the launcher: bypass detection, real-codex finder, topic inference, app-server lifecycle with port fallback, MCP config injection, TUI launch with `--remote`) and `cmd/c3-codex-adapter/main.go` (the MCP server: broker IPC, five tools, inbound buffer + WebSocket forwarder with thread discovery, split-brain guard). Implement `c3-broker install-codex-shim` subcommand that idempotently installs the symlinks (`~/.local/bin/codex` + every NVM bin dir).
9. **`/c3-setup`, `/c3-build`, `/c3-status` slash commands (Claude) and Codex `SETUP.md` (which instructs the agent to run `c3-broker install-codex-shim`).**
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
- Internal Codex bridge architecture was informed by an end-to-end Python POC (Telegram ↔ Codex turns + voice transcription, all working). The POC validated the **architecture**; v5 takes the architecture, writes it in Go, and discards the POC code. The POC files in `mvp/` are kept on disk only for personal continuity until the Go binaries land — they are NOT a reference for new code.
- `mvp/PATCH_SPEC.md` (reference for what behaviors the cleanroom Go Telegram channel must replicate).
- Existing plugin scaffold: `plugin/.claude-plugin/marketplace.json`, `plugin/plugins/c3-telegram/`.
- C3 stated direction: README.md §"Key Features (Full Vision)", TODO.md Phases 1-4, DECISIONS.md D001-D008.
- Go MCP SDK: github.com/modelcontextprotocol/go-sdk v1.0.0+ (per `research/go-mcp-sdk.md`).
- Go Telegram libraries surveyed: gotgbot (PaulSonOfLars), telegram-bot-api (go-telegram-bot-api).
