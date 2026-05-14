# C3 Re-Architecture — Design Spec

**Date:** 2026-05-08 (v5 — fully Go after Codex POC review)
**Status:** Approved. **Go end-to-end** for broker, channels, all CLI adapters, launchers, and operational tooling. The only Python in C3 is the bundled STT pipeline (Gemini 3 Flash → Sarvam Saaras v3 chain), invoked from a Go plugin shim via subprocess — and even that's a plugin, not a core dependency. The Codex bridge architecture in this spec is informed by a Python POC that proved the loop end-to-end (send/receive Telegram + voice transcription on Codex's TUI), but the spec describes a clean Go implementation; no part of the POC carries forward.
**Reaffirms:** D006 (Go for daemon and stubs), D007 (pluggable transport — promoted to v1), D008 (official Go MCP SDK).
**Supersedes:** the deviation banners across `RESUME.md` / `TODO.md` / `DECISIONS.md` (Python wrapper MVP). A formal D009 will record the v1-MVP-superseded note when implementation starts.

## 1. Goal

Take C3 from a hand-tuned Python MVP into a **distributable, multi-channel, multi-CLI** plugin set, written in **Go**, with a **plugin extension system** so STT and future capabilities slot in cleanly.

- **Distributable** — one public github URL, anyone can install via Claude Code or Codex plugin marketplace.
- **Go end-to-end** — broker, all channels, all CLI adapters (Claude, Codex), launchers (the `codex` shim that intercepts the user's command), and operational tooling (migrate-legacy, install-codex-shim) are all Go. The **only** Python that runs is the bundled STT pipeline (Gemini 3 Flash → Sarvam Saaras v3, with vocabulary biasing), called by a Go plugin shim via subprocess — and that's a swappable plugin, not part of the core. Users can override the handler with their own script (whisper, local model, anything matching the argv contract); the chain is editable too. New plugins can be written in any language since they speak a defined wire protocol; "Go everywhere" is the rule for first-party code.
- **Multi-channel** — Telegram is the only channel today; web chat and voice mode were always D007's destination, now plumbed into v1's data model and code paths.
- **Multi-CLI** — Claude Code first, Codex parity, future CLIs through a documented adapter contract.
- **Multi-group** — multiple Telegram supergroups can host C3 topics simultaneously, addressed by name.
- **Seamless attach UX** — sessions in known directories auto-attach; new directories prompt for confirmation; nothing gets created behind the user's back; bare topic ids get validated against Telegram and accepted if real; `attach dm` works anywhere.
- **Plugin extension system** — STT is a plugin in v1, not a built-in. Defined hook points let other plugins drop in without core changes.

The Python prototype proved the architecture but creates duplicate forum topics on out-of-tree sessions, hardcodes absolute paths, bottlenecks future channels behind the upstream bun plugin's release schedule, and was written as a temporary wrapper that was never meant to scale across users.

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
- **All operational state — channel configs, group ids, DM id, mappings, topic registry — lives in one user-visible JSON file** (`~/.config/c3/mappings.json`). Explicit design goal: "everything in mappings.json".
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
                  ┌────────────────────────┐
                  │ $XDG_RUNTIME_DIR/c3.sock│
                  │ (fallback: /tmp/c3-$UID.sock) │
                  └──┬─────┬─────┬─────────┘
              ┌──────┐  ┌──────────────┐  ┌──────────┐
              │Claude│  │Codex bridge  │  │Future CLI│
              │adapter  │(Go)          │  │adapter   │
              │(Go)   │ │ launcher →   │ │           │
              │       │ │ adapter      │ │           │
              └──┬───┘  └─┬────────────┘  └─┬────────┘
                 │ stdio  │ stdio + WS      │
              ┌──▼───┐  ┌─▼─────────────┐
              │ CC   │  │ Codex TUI     │
              │ CLI  │  │ (--remote)    │
              └──────┘  └───────────────┘
```

The Codex bridge is two Go binaries (`codex` launcher + `c3-codex-adapter` MCP server) speaking the same broker IPC the Claude adapter uses. The contract is CLI-agnostic.

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

Inbound messages flow from the channel into the broker via a normalized event. **Canonical name: `Inbound`** (drop any other names like `InboundEvent` or `*Inbound` from earlier drafts):

```go
type Inbound struct {
    Channel     string  // "telegram"
    ChatID      int64
    TopicID     *int64  // nil = non-forum chat (DM, plain group); int64(1) = General forum topic;
                        //  >1 = custom forum topic. NEVER 0 — the Telegram-0 sentinel from the
                        //  Python MVP is gone end-to-end. The routing key uses *int64 so nil and
                        //  &(1) are distinct.
    MessageID   int64
    Sender      Sender
    Text        string
    Attachments []Attachment
    ReplyTo     *ReplyContext
    Timestamp   time.Time
}

type Sender struct {
    UserID   int64  // positive Telegram user id
    Username string // optional; empty if user has no public username
}

type Attachment struct {
    Kind   string // "voice", "audio", "video", "video_note", "document", "photo", "sticker"
    FileID string // Telegram file_id, downloadable via channel.DownloadAttachment
    Size   int64  // bytes; 0 if unknown
    MIME   string // optional
    Name   string // filename (documents) or caption-derived label
}

type ReplyContext struct {
    MessageID int64
    User      Sender
    Text      string // text or caption of the replied-to message
}

type Outbound struct {
    Channel   string  // routes to that channel's SendReply
    ChatID    int64
    TopicID   *int64  // same encoding rules as Inbound
    Text      string
    Files     []string  // absolute paths the channel will upload
    ParseMode string    // optional, channel-specific (Telegram: "MarkdownV2" etc.)
    ReplyTo   *int64    // optional — original message_id to thread under
}

type ReplyArgs   = Outbound // alias; Outbound is the canonical name
type EditArgs struct {
    Channel   string
    ChatID    int64
    MessageID int64
    Text      string
    ParseMode string
}
type EditResult struct{ MessageID int64 }
type ReactArgs struct {
    Channel   string
    ChatID    int64
    MessageID int64
    Emoji     string  // single emoji per Telegram's setMessageReaction contract
}

type VoicePayload struct {
    Channel   string
    ChatID    int64
    TopicID   *int64
    MessageID int64
    FileID    string
    MIME      string
    Size      int64
    // The plugin gets file_id rather than the audio bytes; it calls
    // host.Channel(channel).DownloadAttachment(FileID) if it needs the file.
    // STT plugin currently shells out to a Python subprocess that does the
    // download itself via Telegram Bot API — see internal/plugin/builtins/stt.
}
```

The Telegram channel in v1:
- **Cleanroom Go rewrite.** No retained file from upstream bun plugin — this is the explicit direction per §11.1 review. We use upstream as inspiration at the spec level (what features it has, how it's structured) and write our own Go implementation.
- Library choice: `github.com/go-telegram-bot-api/telegram-bot-api/v5` or `github.com/PaulSonOfLars/gotgbot/v2`. Both maintained, both Bot API. **Open question §11.A** — pick during plan phase. My lean: gotgbot (strongly typed, more idiomatic Go, active development).
- Owns Telegram bot token, manages getUpdates with `allowed_updates = ["message", "edited_message", "callback_query", "message_reaction"]`, applies rate-limit handling (`parameters.retry_after`), runs in a goroutine inside the broker process (not a separate subprocess).
- All P1-P5 patch behaviors built natively: `message_thread_id` in inbound and outbound, `reply_to_message_id`/`reply_to_user`/`reply_to_text` in inbound meta, no orphan watchdog at all (broker manages its own lifecycle), STT plugin invoked on `message:voice` events.
- Exposes its tools to adapters: `reply`, `react`, `edit_message`, `download_attachment`, `send_typing` (new), `edit_progress` (new).

Future channels (web, voice) implement the same interface; adapters don't change.

### 4.2 Routing plane

User-visible types (`Inbound`, `Outbound`, MCP tool args) carry `*int64` for `TopicID` — `nil` distinguishes "no topic" from "General topic" (`&1`) at the type level. **Internally**, the broker normalizes to a value-typed `RouteKey` for map-key correctness — Go's `map[*int64]X` compares pointer identity, not pointed-to value, so two `*int64` values pointing to `1` would NOT collide as map keys. The normalization:

```go
type RouteKey struct {
    Channel  string
    ChatID   int64
    HasTopic bool   // false → DM / non-forum group; TopicID is meaningless
    TopicID  int64  // 1 = General forum topic; >1 = custom topic
}

func MakeRouteKey(channel string, chatID int64, topicID *int64) RouteKey {
    if topicID == nil {
        return RouteKey{Channel: channel, ChatID: chatID, HasTopic: false}
    }
    return RouteKey{Channel: channel, ChatID: chatID, HasTopic: true, TopicID: *topicID}
}
```

`RouteKey` is comparable, hashable, value-typed. Every place that builds a route table key (`ROUTES`, debounce buckets, `edit_progress` placeholder map, typing-ticker state) uses `MakeRouteKey`.

Telegram General topic id confusion (Python MVP code that treated 0 as General) is fixed end-to-end: 0 never appears in the data model, only `nil` (no topic) or `>=1`.

#### 4.2.0 Per-route serial executor (ordering invariant)

Every active route has **one goroutine** — the route worker — that owns all per-route mutable state and serializes the operations that touch it:

- **Inbound pipeline** for the route: STT (`OnVoiceReceived`) → `OnInbound` chain → debounce window → forward to claimed stub.
- **Outbound calls** to the channel: `reply`, `react`, `edit_message`, `edit_progress`, `send_typing`. Each tool-call from any adapter for this route lands on the worker queue.
- **Placeholder map mutations** (`edit_progress`).
- **Typing-indicator state** (in-flight counter, ticker start/stop).
- **Claim release** — when the holding stub disconnects, the worker drains its queue, clears placeholder + typing state, then exits.

The route worker is started lazily on first activity for a `RouteKey` and exits after `idle_timeout` (default 60s) of no work. Each worker reads from a channel of typed messages (`inboundJob`, `outboundJob`, `controlJob`) and dispatches in arrival order.

This eliminates the entire class of races R2 review caught: late `EditMessage` 200 repopulating a re-claimed route, typing-ticker firing into a released claim, debounce flush interleaving with STT subprocess return. There is exactly one mutator per route at any instant.

The route worker does NOT serialize across routes — different routes proceed in parallel.

#### 4.2.1 Claim lifecycle

Claims are in-memory only — `map[RouteKey]*Stub` in the broker. They are NOT persisted; on broker restart the map is empty and adapters re-claim on their next `hello`.

| Event | Effect on claim |
|---|---|
| Adapter sends `attach` and broker grants | Insert claim. Inbound for the route now flows to this stub. |
| Adapter cleanly disconnects (stdin EOF, `bye` op) | Release claim. Broker sweeps the route from `ROUTES`. |
| Adapter crashes / loses socket | Broker detects via `EPIPE` on next write and releases. The next inbound for that route triggers the cooldown-fallback reply (§4.4.x). |
| `release` op (explicit) | Release claim. Adapter stays connected, just gives up the route. New `attach` reclaims. |
| Broker restart | All claims gone. Adapters re-claim on reconnect. The `mappings.json` cwd→topic mapping is the durable state; live claims are not. |
| In-flight inbound when claim releases | **The route worker drains its queue before releasing.** Jobs already enqueued at release-time complete and the worker drives them through the channel; jobs that arrive AFTER the release op (still in transit at the network layer or sitting in OS pipe buffers) hit cooldown-fallback. The worker, not the network, is the mutator. ConnID-stamped late results from a previous claim are discarded by the new worker — they never mutate placeholder/typing state for the new claim. |

Single-claim-per-route invariant: only one stub at a time owns `(channel, chat_id, *topic_id)`. The broker rejects a second `attach` to the same route with `claim_holder` populated; adapter surfaces "already held by `<cli>` pid `<pid>`".

#### 4.2.2 Broker process model

Singleton-per-machine via `flock`. The lock file path:

```
$XDG_RUNTIME_DIR/c3-broker.pid    (preferred — created mode 0600 in user-private tmpfs)
$HOME/.cache/c3/c3-broker.pid     (fallback if XDG_RUNTIME_DIR unset)
```

Spawn protocol:

1. Adapter calls `connect(socket_path)` (§4.4 socket path resolution: `$XDG_RUNTIME_DIR/c3.sock` → `/tmp/c3-$UID.sock` fallback, never bare `/tmp/c3.sock` to avoid multi-user clobbering).
2. On `ECONNREFUSED` / `ENOENT`, adapter calls `exec.Command("c3-broker")` in a detached process group (`setsid`), then retries `connect` with exponential backoff up to 10 seconds.
3. Broker on startup:
   - Calls `flock(fd, LOCK_EX | LOCK_NB)` on the pid file.
   - On `EWOULDBLOCK`: read the pid, check if the process is alive (`kill(pid, 0)`). If alive → exit 0 silently (sibling won the race). If dead → unlink the stale pid file and retry the lock once.
   - On success: write own pid into the locked file (do NOT close the fd; closing releases the flock). Bind the socket.
4. Concurrent-spawn race: two adapters spawning simultaneously is safe — `flock` ensures exactly one broker survives. The losing broker exits silently. Both adapters' connect-with-backoff succeeds against the surviving broker.
5. Stale-lock recovery: a broker that crashed without releasing the lock leaves the pid file stale. Step 3's pid-liveness check handles this. The crashed broker's socket file is unlinked by the new broker before binding.

Daemonization: the broker does not double-fork or detach further than `setsid` puts it. It runs as a child of the spawning adapter's process group's session, but is reparented to init when the spawning adapter exits — standard Go `exec.Command` + `Setsid: true` behavior. No PID 1 needed.

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
        "main": {"chat_id": -1001234567890, "title": "C3 main group"},
        "work": {"chat_id": -1009999999999, "title": "Work multiplexer"}
      },
      "dm_chat_id": 12345678,
      "master_user_id": 12345678,
      "topics": [
        {"chat_id": -1001234567890, "topic_id": 281, "name": "c3", "group": "main"},
        {"chat_id": -1001234567890, "topic_id": 207, "name": "widget", "group": "main"}
      ],
      "debounce_ms": 1500,
      "debounce_max_messages": 50,
      "fallback_cooldown_s": 300,
      "stt_prefix": "[Transcribed voice]: "
    }
  },
  "codex": {
    "shared_root": "~/projects",
    "app_server_meta_path": "/tmp/c3-codex-app-server-${UID}.json"
  },
  "mappings": {
    "/home/user/projects/c3": {
      "channel": "telegram",
      "chat_id": -1001234567890,
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
      "priority": 10,
      "language": "en",
      "vocabulary_file": "",
      "model": "small"
    }
  }
}
```

Reserved top-level sections: `channels` (per-channel config + topics registry), `codex` (Codex-bridge-specific tunables that aren't a channel), `mappings` (cwd → claim entry), `plugins` (per-plugin config). Nothing else is read by the broker.

Channel-level field semantics:
- `debounce_ms` — debounce window after each new inbound (default 1500). Per-channel; future per-group via `groups.<g>.debounce_ms`.
- `debounce_max_messages` — hard cap that forces flush regardless of window (default 50).
- `fallback_cooldown_s` — cooldown for the no-claim fallback reply (default 300).
- `stt_prefix` — string prefix the channel writes onto STT-substituted inbound text (default `"[Transcribed voice]: "`). The Telegram channel applies this BEFORE emitting the `Inbound`; the broker doesn't re-add it. Plugins reading the inbound see the prefixed text.

Codex section:
- `shared_root` — directory whose `CLAUDE.md` does NOT trigger auto-attach. No default; set via `C3_CODEX_SHARED_ROOT` env var (or `mappings.json:codex.shared_root` when that key lands). Honored by the `codex` launcher's topic inference.
- `app_server_meta_path` — absolute path template; `${UID}` substituted with the running user's numeric uid for multi-user safety. Default `/tmp/c3-codex-app-server-${UID}.json`. The C3 launcher uses `flock` on this file before reading/writing the signature; concurrent invocations of `codex` from the same user are serialized.

Reserved plugin keys: `enabled` (bool, default true) skips subscriptions when false; `priority` (int, default 100) orders chained hooks (lower runs first). Other keys are plugin-defined.

`plugins` is added so plugins keep their config alongside everything else (per the "everything in mappings.json" rule). Each plugin owns its own subkey.

Why one file: explicit design constraint. Operator's view in one place. Mode 600 protects the bot token. Atomic rewrites via temp-file-then-rename so a half-written file can't corrupt state.

**Backup-on-write:** before each atomic rewrite the broker copies the current `mappings.json` to `mappings.json.bak` (mode 0600). One generation only — successive writes overwrite the same `.bak`. This protects against operator error (e.g. a hand-edit followed by a broker restart that fails validation): the user can `cp mappings.json.bak mappings.json` to roll back without going to git.

**Corruption recovery on boot:** if the broker fails to parse `mappings.json` at startup, it logs the parse error to stderr and exits non-zero. It does NOT fall back to a skeleton config or auto-recover from `.bak` — silent fallback would mask user error. The user is expected to either fix the JSON, restore from `.bak`, or invoke `c3-broker validate <path>` to lint.

### 4.4 Adapter plane (per-CLI stubs, Go)

Each adapter is a small Go binary that:
1. Speaks its host CLI's MCP protocol over stdio (using `github.com/modelcontextprotocol/go-sdk`).
2. Connects to the broker over the unix socket (path resolution: `$XDG_RUNTIME_DIR/c3.sock` → `/tmp/c3-$UID.sock` fallback; never bare `/tmp/c3.sock`).
3. Translates inbound notifications into whatever the host CLI can render.

#### 4.4.1 IPC types (Go, JSON-tagged)

The newline-delimited JSON IPC over the unix socket. Concrete Go structs — every adapter and the broker import these from `internal/ipc/messages.go`:

```go
package ipc

// Op is the op-code on every message. Adapters and broker dispatch on this.
type Op string

const (
    OpHello       Op = "hello"        // adapter → broker
    OpHelloAck    Op = "hello_ack"    // broker  → adapter
    OpServerInfo  Op = "server_info"
    OpToolsList   Op = "tools_list"
    OpAttach      Op = "attach"
    OpAttached    Op = "attached"
    OpRelease     Op = "release"      // adapter → broker, explicit detach
    OpListTopics  Op = "list_topics"
    OpTopicsList  Op = "topics_list"
    OpToolCall    Op = "tool_call"
    OpToolResult  Op = "tool_result"
    OpInbound     Op = "inbound"      // broker → adapter
    OpError       Op = "error"
    OpBye         Op = "bye"          // adapter → broker, clean disconnect
)

type HelloMsg struct {
    Op   Op     `json:"op"`            // = OpHello
    CLI  string `json:"cli"`           // "claude", "codex", "<future>"
    PID  int    `json:"pid"`
    CWD  string `json:"cwd"`           // absolute, resolved
    // Capabilities the adapter has for surfacing inbound. Used by the broker to
    // pick the cheapest delivery path (push vs poll vs WS forwarder) when more
    // than one is available. Optional; broker assumes the conservative default
    // ("inbox") if absent.
    Capabilities []string `json:"capabilities,omitempty"` // e.g. ["claude/channel", "log-notification", "inbox", "ws-forwarder"]
}

type HelloAckMsg struct {
    Op             Op       `json:"op"`             // = OpHelloAck
    AutoAttached   bool     `json:"auto_attached"`
    Mapping        *Mapping `json:"mapping,omitempty"`
    ClaimHolder    *Holder  `json:"claim_holder,omitempty"`
    NoConfig       bool     `json:"no_config,omitempty"`
    NoMapping      bool     `json:"no_mapping,omitempty"`
}

type Holder struct {
    CLI string `json:"cli"`
    PID int    `json:"pid"`
    CWD string `json:"cwd"`
}

type AttachReq struct {
    Op       Op      `json:"op"`               // = OpAttach
    CWD      string  `json:"cwd,omitempty"`    // absolute; required for cwd-keyed semantics
    Name     string  `json:"name,omitempty"`   // explicit topic name
    Target   string  `json:"target,omitempty"` // "dm" for DM, else empty
    TopicID  *int64  `json:"topic_id,omitempty"`
    Group    string  `json:"group,omitempty"`  // override default group
    Channel  string  `json:"channel,omitempty"`// defaults to default channel
    Create   bool    `json:"create,omitempty"` // confirmation flag

    // Confirm carries the proposal returned by the prior unsealed AttachReq.
    // Populated when responding to a `needs_confirmation` proposal so the
    // broker can detect sibling-stub state changes mid-confirm: if the broker
    // re-derives a different proposal at confirmation time, it returns
    // needs_confirmation again with the NEW proposal rather than silently
    // creating a topic with a name the user/agent never agreed to.
    Confirm *Proposal `json:"confirm,omitempty"`
}

type AttachedMsg struct {
    Op                Op           `json:"op"`              // = OpAttached
    OK                bool         `json:"ok"`
    Channel           string       `json:"channel,omitempty"`
    ChatID            int64        `json:"chat_id,omitempty"`
    TopicID           *int64       `json:"topic_id,omitempty"`
    Name              string       `json:"name,omitempty"`
    Group             string       `json:"group,omitempty"`
    NeedsConfirmation bool         `json:"needs_confirmation,omitempty"`
    Proposal          *Proposal    `json:"proposal,omitempty"`
    Err               string       `json:"err,omitempty"`
}

type Proposal struct {
    Action  string `json:"action"`        // "create" | "use_existing_other_group" | "claim_existing"
    Channel string `json:"channel"`
    Group   string `json:"group"`
    Name    string `json:"name"`         // proposed topic name (echoed for the agent to surface)
    Existing *Topic `json:"existing,omitempty"` // populated when action="use_existing_other_group" or "claim_existing"
    Alternative *Proposal `json:"alternative,omitempty"` // recursion: e.g. "you could create new in default group instead"
}

type InboundMsg struct {
    Op      Op      `json:"op"`            // = OpInbound
    Inbound Inbound `json:"inbound"`       // normalized payload defined in §4.1
}

type ToolCallMsg struct {
    Op    Op             `json:"op"`        // = OpToolCall
    ID    string         `json:"id"`        // adapter-generated; broker echoes in result
    Name  string         `json:"name"`      // tool name (e.g. "reply", "edit_progress")
    Args  map[string]any `json:"args"`
}

type ToolResultMsg struct {
    Op     Op             `json:"op"`       // = OpToolResult
    ID     string         `json:"id"`
    Result map[string]any `json:"result,omitempty"`
    Error  *ErrorPayload  `json:"error,omitempty"`
}

type ErrorPayload struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

type ErrorMsg struct {
    Op  Op     `json:"op"`                 // = OpError
    Err string `json:"err"`
}
```

**Writer mutex:** the IPC socket is duplex and a single connection carries both request/response (synchronous tool calls) and unsolicited push (broker → adapter inbound). Both sides use a single `bufio.Writer` guarded by a `sync.Mutex`; line-by-line frames are atomic, so a `Write` of a complete `\n`-terminated JSON encoding cannot interleave with another writer's frame. Reader uses `bufio.Scanner` with `ScanLines`. Mandatory in every adapter and the broker.

#### 4.4.2 Tool surface — harmonized

Drop the `c3_` prefix on Codex-side tools. The Codex adapter exposes the same base names as the Claude adapter (`attach`, `topics`, `reply`, `react`, `edit_message`, `edit_progress`, `download_attachment`, `send_typing`), plus Codex-only additions for the inbox-poll fallback and WS forwarder (`inbox`, `codex_forward`). The MCP server name (`c3` in Claude's `.mcp.json`, `c3` or `c3_codex` in Codex's `mcp_servers.<key>`) provides the namespace; per-tool prefixing is redundant.

This is a breaking change vs the Python POC's tool surface. The Codex adapter rewrite already requires updating callers, so the rename rides along.

#### 4.4.3 Cooldown-fallback reply

When an inbound for `(channel, chat_id, *topic_id)` arrives but no stub holds the claim, the broker sends a single fallback reply on the channel. Spec:

- **Text:** `"No CLI is currently attached to this topic. Run `c3-broker status` to see attached terminals, or open a CLI in the project directory and `attach`."`
- **Dedup key:** `(channel, chat_id, *topic_id)`. One fallback per key per cooldown window.
- **Cooldown:** 300 seconds (5 minutes). Configurable per channel via `mappings.json:channels.<chan>.fallback_cooldown_s`.
- **Implementation:** broker holds an in-memory `map[RouteKey]time.Time` of last-fallback times. On inbound with no claim, check map; if `now - last >= cooldown`, send via channel's `SendReply` and update map. The fallback skips plugin pipeline (no debounce, no `OnInbound`/`OnOutbound`) — it's an operator-level message, not a user message.
- The fallback survives broker restarts as "first-inbound-after-restart wins": map is reset, next inbound triggers a fresh fallback.

#### 4.4.4 Manual JSON-RPC framing for `notifications/claude/channel`

The named `modelcontextprotocol/go-sdk` (v1.6.0) does not expose a public API to send arbitrary custom JSON-RPC notifications. Its `ServerSession` only knows `NotifyProgress`, `Log`, `ResourceUpdated`, and list-changed. Claude Code's rich `<channel>` rendering depends on the broker emitting `notifications/claude/channel` over MCP stdio.

**Decision: manually frame the notification.** The MCP stdio protocol is JSON-RPC 2.0 newline-framed over the same stdout the SDK writes to. The Claude adapter:

1. Builds a JSON-RPC envelope `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{...}}` directly.
2. Acquires the same writer mutex the SDK uses internally (the adapter installs a custom writer wrapper around `os.Stdout` with a `sync.Mutex`; both the SDK and the manual framer go through it).
3. Writes one frame as `<json>\n`.

**Why this is safe:** newline-JSON frames are atomic at the OS level (`write()` of a single buffer is atomic for unix pipes up to `PIPE_BUF` = 4096 bytes, and the stdout-to-Claude pipe falls under that for typical channel meta). For longer frames, the writer mutex is the actual safety net.

**What to monitor:** if/when go-sdk adds a public `ServerSession.Notify(method, params)` method, drop the custom framer and use it. Track the upstream PR; this is a v1.x cleanup, not a v1 blocker.

#### 4.4.5 Codex bridge env-var contract

Consolidated table; previously scattered across §4.4 prose:

| Env var | Set by | Read by | Required | Default | Purpose |
|---|---|---|:---:|---|---|
| `C3_ATTACH_NAME` | `codex` launcher | adapter | ★ | — | Topic name inferred from cwd; adapter auto-attaches if mapped, surfaces proposal otherwise |
| `C3_CODEX_REMOTE_BRIDGE` | `codex` launcher | adapter | ★ | — | Must be `"1"` to enable WebSocket forwarding. Split-brain guard against stock `codex resume` running stub manually |
| `C3_CODEX_CWD` | `codex` launcher | adapter | ★ | — | Absolute cwd; used for `thread/list` filtering when multiple Codex threads are loaded |
| `C3_CODEX_APP_SERVER_WS` | `codex` launcher | adapter | ★ | — | The selected app-server WebSocket URL (port may be `8766+` per fallback) |
| `C3_CODEX_REAL` | user (manual override) | launcher | — | (PATH search + NVM glob) | Force a specific real-codex path; used for testing |
| `C3_CODEX_DISABLE` | user | launcher | — | unset | When `"1"`, launcher exec's real codex unmodified — full bypass |
| `C3_CODEX_ALLOW_MANUAL_FORWARD` | user (debug) | adapter | — | unset | Bypass the split-brain guard for the `codex_forward` tool. Use only for debugging the forwarder |

★ = set automatically by the C3 launcher. User never sets these by hand in normal operation.

Host-specific translations:

- **Claude Code adapter** forwards inbound as `notifications/claude/channel` (preserves rich `<channel>` rendering). Tools list is broker tools + adapter-local `attach` and `topics`. Inbound `<channel>` blocks are rendered natively.
- **Codex adapter** is two Go binaries that together provide the same end-to-end behavior the Python POC proved out (a `codex` invocation transparently puts the user inside a TUI whose conversation stream is bidirectionally bridged to a Telegram topic, voice-transcribed inbound included):

  - **`codex`** (built from `cmd/codex/main.go`, installed on the user's `PATH` ahead of the real `codex` binary). This is the launcher. It does five things in order on every invocation:
    1. **Find the real Codex executable.** Walk `PATH` skipping `os.Args[0]` (so we don't recurse into ourselves). If nothing matches, glob `~/.nvm/versions/node/*/lib/node_modules/@openai/codex/bin/codex.js`. Honor `C3_CODEX_REAL` env override for testing.
    2. **Decide whether to bypass.** A defined set of subcommands (`exec`, `e`, `review`, `login`, `logout`, `mcp`, `plugin`, `mcp-server`, `app-server`, `completion`, `update`, `sandbox`, `debug`, `apply`, `a`, `cloud`, `exec-server`, `features`, `help`) plus `-h`/`--help`/`-V`/`--version` plus any argv already containing `--remote` (anti-recursion guard) → exec real codex with the user's argv unchanged. `codex resume`, `codex fork`, and bare `codex` go through the bridge.
    3. **Infer the topic.** Walk from cwd up to the nearest `CLAUDE.md`; the basename of that directory is the topic name. **Shared-root guard:** if the nearest `CLAUDE.md` is at a configured shared-root (opt-in via `C3_CODEX_SHARED_ROOT` env, no default; future `mappings.json:codex.shared_root` will provide a persistent form), return empty — let the user attach explicitly rather than silently bind a "shared-root" topic. Fallback to `basename(cwd)` only when no `CLAUDE.md` is found AND cwd is not under a shared-root. `C3_ATTACH_NAME` env always overrides.
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
    1. Connects to the broker at the resolved socket path (`$XDG_RUNTIME_DIR/c3.sock` or `/tmp/c3-$UID.sock`); spawns the broker if missing, same flock pattern the Claude adapter uses.
    2. Sends `hello{cli="codex", pid, cwd: $C3_CODEX_CWD}`. If the broker has a mapping for that cwd, claims it. If not, reads `C3_ATTACH_NAME` and either auto-attaches (if the topic exists in the default group) or surfaces a proposal in `instructions` for the agent.
    3. Exposes five MCP tools to Codex:
       - `attach(target?)` — attach this Codex session to a topic. Same proposal-flow semantics as the Claude adapter's `attach`. Tool names are unprefixed; the MCP server name (`c3` or `c3_codex` in `mcp_servers.<key>`) provides the namespace.
       - `topics()` — list known topics + claim state.
       - `inbox(limit, ack)` — Codex-only fallback path. Drain buffered inbound messages when WS forwarding is unavailable. **Buffer policy:** ring buffer capped at 100 messages per claim; on overflow oldest are dropped (with a `… N messages dropped` log line). Buffer flushes on adapter disconnect — Codex sessions that go away never leak.
       - `reply(text, files?, parse_mode?)` — send a reply through the attached topic. If local `bound` state was lost (stub restarted while broker held the claim), recover it from the broker's claim before erroring out — same recovery the Claude adapter does (cross-CLI symmetry).
       - `codex_forward(app_server_ws, thread_id?)` — Codex-only debugging/manual override; gated by either `C3_CODEX_REMOTE_BRIDGE=1` (set by the launcher) or `C3_CODEX_ALLOW_MANUAL_FORWARD=1` (deliberate-debug). Refused otherwise — split-brain guard against attaching forwarding to a stock `codex resume` whose visible TUI isn't `--remote`-bound.

  - **Inbound forwarding** (the path that delivers Telegram messages into the Codex turn stream): every inbound from the broker is buffered for `inbox` AND, when forwarding is enabled, also pushed to the Codex app-server via WebSocket. The flow:
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

#### 4.5.1 Plugin host interface

Plugins receive a `*plugin.Host` from the broker. Concrete Go interface — every plugin imports this from `internal/plugin/host.go`:

```go
package plugin

type Host interface {
    // Hook subscriptions. Calling these registers the plugin for that hook.
    // All hook callbacks receive a context.Context the broker can cancel —
    // honor it and return promptly when ctx.Done() fires (broker shutdown
    // or per-route timeout).
    //
    // Hook chaining order: lower priority runs FIRST. (Plugin priority is
    // an int with default 100; STT defaults to 10 so it runs before
    // OnInbound transforms see the post-STT text.)
    OnInbound(fn func(ctx context.Context, msg *Inbound) (*Inbound, bool /*drop*/))
    OnVoiceReceived(fn func(ctx context.Context, payload VoicePayload) (string, error)) // returns transcript or "" + err
    OnOutbound(fn func(ctx context.Context, msg *Outbound) (*Outbound, bool /*drop*/))
    OnAttach(fn func(*Stub, *Mapping))

    // Tool registration. RegisterTools is called once during plugin Register().
    RegisterTools(fn func(*ToolRegistry))

    // Config / state.
    Config(name string, target any) error  // unmarshal mappings.json:plugins.<name> into target
    State(name string) StateDir             // ~/.config/c3/state/<name>/
    CacheDir(name string) string            // $XDG_CACHE_HOME/c3/<name>/

    // Channel access (for plugins that need to send/edit/react).
    Channel(name string) (Channel, error)   // returns the live Channel impl by name, or error if unknown

    // Logging.
    Logf(format string, args ...any)
    // Done is closed when the broker is shutting down. Plugins should observe.
    Done() <-chan struct{}
}

type Session = Stub  // alias; Stub is the canonical name

type Stub struct {
    CLI string  // "claude" | "codex" | future
    PID int
    CWD string
    // ConnID is the broker-assigned connection generation. Lifecycle:
    //   1. Assigned by broker when an adapter completes hello — broker holds
    //      a monotonic uint64 counter, increments, hands the value back in
    //      hello_ack. Adapter does not see the counter; it just knows its own.
    //   2. Bumped on every reconnect. The adapter's reconnect-once flow
    //      causes the broker to assign a new ConnID; the old ConnID is dead.
    //   3. Compared on every route-worker job: workers stamp ConnID into
    //      jobs they enqueue. When a job's ConnID no longer matches the
    //      currently-claimed Stub's ConnID, the worker discards the job —
    //      a late EditMessage 200 from a prior claim cannot mutate the
    //      current placeholder map. ConnID is the late-result-discard token.
    ConnID uint64
}

type StateDir interface {
    // Load reads name from the plugin's state dir into target. Returns
    // os.ErrNotExist if missing.
    Load(name string, target any) error
    // Save atomically writes target as JSON to name in the plugin's state dir.
    Save(name string, target any) error
}

type Mapping struct {
    Channel string
    ChatID  int64
    TopicID *int64
    Name    string
    Group   string
}

type Topic struct {
    ChatID  int64
    TopicID int64
    Name    string
    Group   string
}

type ToolRegistry interface {
    Add(t Tool)            // register a tool; broker rejects duplicate names
    Remove(name string)    // optional, mostly for testing
    List() []Tool          // current tools (for diagnostics / status command)
}

type Tool struct {
    Name        string
    Description string
    InputSchema map[string]any
    Handler     func(ctx context.Context, args map[string]any) (any, error)
}
```

**Hook ordering and the OnInbound vs OnVoiceReceived race:**

The broker fires hooks in a fixed sequence per inbound event:

1. Channel emits `*Inbound` (with `Attachments[0].Kind="voice"` for voice messages, `Text=""`).
2. **`OnVoiceReceived` first** for voice payloads — chained by `priority`; first plugin to return a non-empty transcript wins. The broker substitutes the transcript into `Inbound.Text` (preserving `Attachments`) before continuing.
3. **`OnInbound` second** — chained by `priority`; sees the (possibly STT-substituted) inbound. First plugin to set `drop=true` short-circuits.
4. Debounce window (per-route) collapses bursts.
5. Route lookup → `OpInbound` to the claimed stub. Or cooldown-fallback if no claim.

This order means `OnInbound` plugins see the post-STT text, never raw voice payloads. Plugins that want raw voice access should use `OnVoiceReceived` (one shot) or hook into the channel directly via `host.Channel("telegram")`.

**`edit_progress` placeholder lifecycle:**

A "session" for placeholder tracking is `(stub_pid, claim_route)` — the placeholder belongs to the currently-claimed stub for the route. The broker holds `map[(claim_route)]ProgressPlaceholder{MessageID, CreatedAt}`.

- First `edit_progress(text)` call from a stub: broker calls `channel.SendReply` to create a placeholder, stores `MessageID`. Subsequent calls in the same stub session: broker calls `channel.EditMessage(MessageID, text)`.
- On stub `release` / disconnect: broker clears the placeholder entry. The placeholder message stays in Telegram (no auto-delete).
- On broker restart: placeholder map is gone; the next `edit_progress` from a re-attached stub creates a fresh placeholder. Old placeholder messages remain in the topic. This is acceptable — they're scoped to a single agent turn and aren't expected to outlive a session anyway.
- Final `reply` always creates a new message (not edit) so the user's device pings.

**Confirmation-proposal expiry:**

The `attach` proposal flow is **stateless on the broker side**. Every `attach` call recomputes the proposal from current `mappings.json` + claim state. The broker does NOT remember "I proposed `widget-foo` to this stub". `attach(create=true)` is interpreted in light of the *current* search result, not a captured proposal. This means:

- If the user takes 5 minutes to confirm, the proposal is still valid (assuming no one else attached the topic in between).
- If a sibling stub creates `widget-foo` in the work group between the proposal and `attach(create=true)`, the second call sees the new topic and proposes "use_existing_other_group" instead of silently creating a duplicate. The agent surfaces the change to the user.

#### 4.5.2 Operational subcommands of `c3-broker`

The broker binary doubles as the operational tool. Subcommands beyond the default daemon mode:

| Subcommand | Purpose |
|---|---|
| `c3-broker` | (default) Run as daemon. Spawned automatically by adapters; rarely run by hand. |
| `c3-broker setup` | Interactive setup — gather bot token, DM chat id, group(s); validate the token by calling Telegram `getMe` BEFORE writing; write `~/.config/c3/mappings.json` only on validation success. Equivalent to `/c3-setup`. |
| `c3-broker validate [path]` | Parse and validate `mappings.json` (defaults to default path). Prints structural errors. Exits 0 on valid, 1 on invalid. Use to lint hand-edits before saving. |
| `c3-broker status` | Print broker liveness, socket path, pid, mappings.json path + parses, channel reachability (`getMe` for telegram), claimed topics with holder pid+cwd, plugin enabled-states. Read-only. |
| `c3-broker release <cwd>` | Release the claim on a route bound to `<cwd>` if any. Safe to run from any shell — broker hangs up the holding stub's claim, the next `attach` from any session reclaims. |
| `c3-broker install-codex-shim` | Symlink installer (PATH + every Node-manager bin dir). Idempotent. See §5.1 Codex side. |
| `c3-broker reload-config` | Re-read `mappings.json` without restarting (handles the `/c3-setup` race where broker started before config was written). Rebuilds in-memory channel configs. Live claims are preserved. |

The status / validate / release subcommands are diagnostic-only — they don't write to `mappings.json` and don't restart the broker.

## 5. Key flows

### 5.1 First install on a fresh machine

**Claude Code side:**

1. User runs `/plugin marketplace add karthikeyan5/c3`, then `/plugin install c3@c3`, then `/reload-plugins`.
2. User runs `/c3-build` (slash command shipped by the plugin) once. It runs `go install ./cmd/...` in the plugin source dir; binaries land in `$GOBIN`.
3. The plugin's `.mcp.json` references the adapter by name (e.g. `command: "c3-claude-adapter"`), assuming `$GOBIN` is on `$PATH`.
4. On the next session, the Go adapter starts, tries the resolved socket path, fails, spawns `c3-broker` via `$PATH` (process detached via `setsid` per §4.2.2).
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
   e. Verifies the broker is reachable at the resolved socket path; starts it if not.
   f. Prints a one-line status with each symlink's path so the user can audit.
3. From now on, running `codex` (from any shell — old or new) routes through the C3 launcher, which manages the app-server, spawns the adapter, and bridges inbound Telegram into Codex turns. The user types `codex` exactly as they always have; nothing else changes about their workflow.
4. The Codex side reuses `~/.config/c3/mappings.json` from the Claude-side setup. If neither side has run setup yet, `c3-broker install-codex-shim` prompts (interactively or via the agent) for the same fields the `/c3-setup` slash command would gather.

### 5.2 Fresh project — `attach` proposal flow

```
$ cd ~/projects/widget-foo
$ claude
```

1. Adapter `hello` with `cwd=/home/user/projects/widget-foo`.
2. Broker checks `mappings.mappings`. No entry. Replies `{auto_attached: false, no_mapping: true}`.
3. Adapter instructions: *"No mapping for `/home/user/projects/widget-foo`. Type `attach` to set one up."*
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
9. Adapter returns to Claude: *"No mapping for this directory. I'd create a new topic 'widget-foo' in the 'main' Telegram group. To proceed, call `attach(create=true)`. To use an existing topic instead, call `attach(topic_id=<n>)` (broker validates against Telegram). To use a different name, call `attach(name='<other>', create=true)`."*
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

Session 1 (Claude in `~/projects/c3`) auto-attached to topic 281. User opens Codex in same dir.

1. Codex stub `hello` with same cwd.
2. Broker finds mapping → topic 281. ROUTES already claims it for Claude pid 12345.
3. Replies `{auto_attached: false, mapping: {…}, claim_holder: {cli: "claude", pid: 12345}}`.
4. Codex instructions: *"Saved mapping points to 'c3' topic but it's currently held by Claude Code (pid 12345). Either run `release` from that Claude session, then `attach` here; or `attach(target='<other>')` to claim a different topic; or wait for it to detach."*
5. No silent topic creation, no claim theft.

### 5.7 Inbound message routing

Telegram delivers a message in topic 281. The Telegram channel emits an `Inbound` to the broker. Broker:

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
- `validate_topic(chat_id, topic_id)` — test call for §5.4 attach-by-id. Implementation: `sendChatAction(chat_id, "typing", message_thread_id=topic_id)`. Side effect: a transient typing indicator fires in the topic — visible but harmless. Alternative considered: `editForumTopic` (would be silent but requires `can_manage_topics` admin right). Picked typing-action because the bot already needs to send messages to the topic; visibility is a non-issue.
- `create_topic(chat_id, name)` — wraps `createForumTopic` with retry logic. Telegram throttles this aggressively (~20/min observed; spec assumes 10/min as the safe rate). On 429, honor `parameters.retry_after` (capped at 60s); on consecutive 429s within a single session, fail fast and surface to the user — bulk topic creation is not a supported flow.

**Topic-name collision:** when `attach --topic=N` validates an unknown id, the broker stores it as `topic-N` in `topics` (placeholder name). If the user later runs `attach topic-N --create=true` with that exact placeholder name, broker treats it as a name collision and returns an error: *"`topic-N` is a placeholder for an existing thread; use `attach --topic=N` to claim it, or pick a different name to create."* This prevents two entries with the same name in the same group.

**Debounce buffer cap:** the per-route inbound debounce window collapses up to **50 messages** before forcing a flush. Beyond that, a flush fires immediately and a new window opens. This prevents memory growth if a user accidentally pastes a long message that Telegram chunks into many ~4KB segments.

**Typing-indicator counter cleanup:** when an adapter disconnects (claim release), the broker decrements its in-flight-tool-call counter for the claimed route to 0 and stops the typing ticker. No leak.

Telegram library: `github.com/PaulSonOfLars/gotgbot/v2`. Pin to a specific rc version in `go.mod` (the stable v2.0.0 has not yet been tagged at time of writing; track the rc series). Bumping requires a pass to verify forum-topic + message-reaction support hasn't regressed.

#### 6.1 `notifications/claude/channel` payload schema

The Claude Code adapter emits this JSON-RPC notification (manually framed per §4.4.4) for every routed inbound. The payload is what Claude Code's MCP host renders as a `<channel>` block in the conversation.

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/claude/channel",
  "params": {
    "content": [
      { "type": "text", "text": "<message text or [Transcribed voice]: …>" }
    ],
    "meta": {
      "source":            "telegram",
      "chat_id":           "-1001234567890",
      "message_id":        "868",
      "user":              "alice",
      "user_id":           "12345678",
      "ts":                "2026-05-08T06:05:29.000Z",
      "message_thread_id": "281",                     // omitted if no topic
      "reply_to_message_id": "281",                   // omitted if not a reply
      "reply_to_user":     "examplebot",             // omitted if not a reply
      "reply_to_text":     "<replied-to text>",       // omitted if not a reply
      "attachment_kind":   "voice",                   // omitted if no attachment
      "attachment_file_id":"AwACAgUAAyEFAA…",         // omitted if no attachment
      "attachment_size":   "2997348",                 // omitted if no attachment
      "attachment_mime":   "audio/ogg"                // omitted if no attachment
    }
  }
}
```

All meta values are **strings** (Telegram's chat ids and message ids are int64 in the API but we serialize as decimal strings for JSON-roundtrip safety on Claude's side). Empty/absent fields are omitted, never `null`. Order of keys is not significant.

Future channels emit notifications with the same shape but `meta.source` set to their channel name. Claude Code's renderer keys off `meta.source` for any channel-specific affordances.

#### 6.2 STT plugin distribution

The STT plugin is a Go package under `internal/plugin/builtins/stt/` plus a Python pipeline shipped alongside the plugin manifest at `plugins/c3/stt/`. The Go package is statically compiled into the broker. The Python tree is **bundled in the plugin distribution** (not embedded in the binary) — the plugin manifest, the slash commands, and the Python files all live under the same `plugins/c3/` root and ship together when a user installs `c3@c3`.

Path resolution:
- Default: `${CLAUDE_PLUGIN_ROOT}/stt/stt-handler.py`. `$CLAUDE_PLUGIN_ROOT` is set by Claude Code when it launches the c3 adapter; the adapter inherits the env when spawning the broker.
- Fallback (for installs without `$CLAUDE_PLUGIN_ROOT`, e.g. broker started manually, or pre-c3 installs that still carry the handler at the legacy path): `~/.claude/channels/telegram/stt-handler.py`.
- User override: `mappings.json:plugins.stt.handler_path` wins over both.

Runtime: the Go shim invokes `python3 <resolved-handler-path> <bot_token> <chat_id> <reply_msg_id> <file_id> [<message_thread_id>]` as a subprocess. The handler downloads the audio (via Telegram's `getFile`), invokes the provider chain in `stt-pkg/stt.py`, prints the final transcript to stdout, and exits non-zero on hard failure.

Provider chain (default): **Gemini 3 Flash via OpenRouter** → **Sarvam Saaras v3** fallback. Both are remote APIs — no model weights downloaded, no large dependencies pinned. API keys are loaded from `~/.claude/stt.env` (`OPENROUTER_API_KEY`, `SARVAM_API_KEY`). Vocabulary biasing via `stt-pkg/vocabulary.txt` (one preferred term per line; optional `!=` for misheard alternatives, `--` for notes).

Why distribute the Python tree under the plugin instead of embedding via `//go:embed`:
- **`/c3-build` writes the Go binaries; the plugin install writes the Python tree.** Both happen during the same `claude plugin install c3@c3` flow, so there's no extra step for the user.
- **Provider chain stays editable.** Power users adding a new provider (e.g. a local whisper option) drop a file into `${CLAUDE_PLUGIN_ROOT}/stt/stt-pkg/providers/<name>.py` and switch the chain via the `--chain` arg the runner accepts; no rebuild needed.
- **User override stays clean.** Custom handler scripts override via `plugins.stt.handler_path` in `mappings.json` and don't conflict with plugin updates.
- **No binary bloat.** The 734-line Python tree never enters the broker binary.

Whisper is **not** shipped as a default provider. Users who want a local STT engine point `handler_path` at their own script — the argv contract (`<bot_token> <chat_id> <reply_msg_id> <file_id> [<message_thread_id>]`) is the only requirement.

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
    // subprocess the bundled Python pipeline (Gemini → Sarvam), return
    // transcript or "[STT FAILED: <reason>]" marker on failure
}
```

Plugin host calls `Register` at broker startup. Plugin reads its config via `host.Config("stt")` — pulls `mappings.json:plugins.stt.*`. Plugin can also store derived state via `host.State("stt")` if it needs runtime state beyond config.

### v1 plugins

- `internal/plugin/builtins/stt/` — Speech-to-text. Reads voice attachments via `OnVoiceReceived`, subprocesses the bundled Python pipeline at `plugins/c3/stt/stt-handler.py` (Gemini 3 Flash → Sarvam Saaras v3 chain, with vocabulary biasing), returns transcript or an `[STT FAILED: <reason>]` marker on failure.

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

## 9. Migration from a legacy Python prototype config

For installations carrying a legacy Python-prototype config layout
(`.env` with `TELEGRAM_BOT_TOKEN` + a JSON file with `dm_chat_id` /
`group_chat_id`), the `migrate-legacy` binary performs an idempotent
one-shot rewrite:

1. Read the `.env` for `TELEGRAM_BOT_TOKEN`.
2. Read the legacy config JSON for `dm_chat_id`, `group_chat_id`.
3. If `~/.config/c3/mappings.json` exists, refuse to overwrite.
4. Otherwise write a fresh `mappings.json` skeleton:
   - `channels.telegram.bot_token` from .env
   - `channels.telegram.groups.main = {chat_id: <legacy>, title: "(migrated)"}`
   - `channels.telegram.default_group = "main"`
   - `channels.telegram.dm_chat_id = <legacy>`
   - empty `topics`, empty `mappings`, default plugin config
5. Print summary, set mode 600.

Topic registrations are not migrated — by design, the user starts clean and
re-attaches per project. Legacy topic cleanup in the Telegram supergroup is
handled by the user out-of-band.

## 10. What v3 explicitly does NOT include

- Cleaning up legacy topics (operator's call).
- Auto-creating topics opportunistically on inbound (removed entirely).
- Auto-deleting topics (manual via Bot API; future tool).
- `forum_topic_edited` rename tracking (defer; plumbed-but-inert in update parsing).
- External (subprocess) plugins (defer to v1.x; in-tree plugins only in v1).
- Per-CLI per-project config files (architecture pushes everything central).
- Webhook mode for Telegram (long-polling stays).
- Reactions / business connections / paid media surfacing (`message_reaction` is plumbed via `allowed_updates` but no tool exposes it yet).
- Auto-spawn of CLIs (TODO Phase 2 — adapter plane is ready, no code yet).
- Inter-CLI messaging, master-CLI admin commands, pairing flow, monitoring dashboard.
- Cleanroom STT rewrite (Go shim → bundled Python pipeline subprocess; Gemini → Sarvam chain keeps working). STT is the only Python in C3.

## 11. Resolved questions (no opens left)

All resolved design choices:

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

Phases 1-7 unblock single-user daily use. 8-10 are about shippability to others.

## 13. Sources

- D006 (Go for daemon and stubs), D007 (pluggable transport), D008 (official Go MCP SDK) — `c3/DECISIONS.md`.
- Claude Code plugin docs: docs.anthropic.com/en/docs/claude-code/plugins.
- Codex MCP docs: developers.openai.com/codex/mcp, /config-reference, /app-server.
- Codex inbound notification status: openai/codex#18056, #17543, #15299 — open.
- Telegram Bot API: core.telegram.org/bots/api, /api-changelog.
- Telegram forums constraint: tdlib/telegram-bot-api#356 (no `getForumTopics`).
- OpenClaw messaging features: c3/README.md §"Inspiration: OpenClaw's Message Tool".
- The Codex bridge architecture was informed by an end-to-end Python prototype (Telegram ↔ Codex turns + voice transcription, all working) that preceded this rewrite. v5 takes that architecture and re-implements it in Go.
- Existing plugin scaffold: `plugin/.claude-plugin/marketplace.json`, `plugin/plugins/c3-telegram/`.
- C3 stated direction: README.md §"Key Features (Full Vision)", TODO.md Phases 1-4, DECISIONS.md D001-D008.
- Go MCP SDK: github.com/modelcontextprotocol/go-sdk v1.0.0+ (per `docs/research/go-mcp-sdk.md`).
- Go Telegram libraries surveyed: gotgbot (PaulSonOfLars), telegram-bot-api (go-telegram-bot-api).
