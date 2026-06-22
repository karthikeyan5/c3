# C3 Durable Inbound Queue + Backlog Delivery — Design

> **Status:** Approved design (brainstormed 2026-06-22 with Karthi). Next step: implementation plan via the writing-plans skill.
>
> **For agentic workers:** this is the design spec, not the plan. Architecture, components, interfaces, and decisions are settled here; the implementation plan derives bite-sized TDD tasks from it.

**Goal:** Once C3 has received an inbound Telegram message, never lose it. Messages that arrive while no CLI session is attached (or while the broker was down and later catches up) are held durably and delivered — with an agent-visible backlog notification + pull — when a session attaches. Works for both the Claude and Codex adapters. The human never has to re-forward or babysit delivery.

**Architecture:** A new broker-side **durable, per-route, append-only on-disk queue** becomes the holding buffer for every inbound. The Telegram offset advances only *after* a message is durably persisted. Live attached sessions still receive messages by immediate push (lifecycle model **B**); messages with no live consumer accumulate in the queue and are delivered on attach via a **compact backlog summary + an agent-driven `fetch_queue` pull tool**. Stdlib-only — no new dependencies.

**Tech stack:** Go (broker + adapters), existing length-prefixed JSON IPC over the Unix socket, JSONL on disk under `$XDG_STATE_HOME/c3/queue/`. No third-party libraries.

---

## Global Constraints

- **No new third-party dependencies** — stdlib only (no SQLite, no embedded KV). Same no-extra-dependency rule Karthi set for the native-Sarvam work.
- **Both adapters reach parity** for: durable delivery, `fetch_queue`, `retranscribe`. Divergence is allowed only where it matches each agent's native consumption model (see Delivery).
- **No silent truncation.** Any cap/drop emits a broker.log line *and* a Telegram notice in the affected topic.
- **Never auto-switch output mode.** Out of scope here, but unchanged.
- **Keep-out values** (proxy subdomains, GCP project, region, static IP) never enter the public repo. This spec uses placeholders only; none are needed here.
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

## Background — the current loss bug (why this is needed)

Mapped from the current source (citations are the pre-change line numbers):

1. **No durable inbound queue exists today.** Inbound flows poll → `BrokerHost.Emit` (`internal/broker/host.go:56`) → per-route `RouteWorker` (`internal/broker/worker.go:119`) → immediate forward. The only buffering is a per-worker debounce slice on a goroutine stack and a `chan Job` of cap 64 — all memory-only, lost on restart.
2. **No session attached → messages are DROPPED.** `forwardOrFallback` (`worker.go:452-480`) sends one `fallbackText` ("No CLI is currently attached…", `internal/broker/fallback.go:47`) per 5-minute cooldown (`fallback.go:44`) and drops the rest. `fallbackTracker` stores only timestamps, never the messages.
3. **The offset advances on dispatch, not on delivery.** The Telegram offset is saved right after `host.Emit` returns (`internal/channel/telegram/poll.go:367-378`), i.e. before any agent has seen the message — and before it is persisted anywhere. So an accepted-but-undelivered message is unrecoverable: Telegram considers it delivered and won't resend, and C3 never stored it. A broker restart loses everything in flight.
4. **Claude is push-only with no buffer.** `notifications/claude/channel` (`cmd/c3-claude-adapter/main.go:537`); on notify failure the content is only logged (`main.go:546`) and lost. Claude has **no** inbound-pull tool.
5. **Codex has a fragile in-memory ring.** `inbox []c3types.Inbound`, cap 100, drop-oldest, lost on restart (`cmd/c3-codex-adapter/main.go:502-517`), drained by the `inbox` MCP tool (`main.go:813-826,1050-1090`).
6. **Attach delivers no backlog.** `tryClaim` (`internal/broker/attach.go:538`) claims the route and optionally sends a welcome; it delivers nothing that arrived earlier. `replayLastAttach` (`main.go:464`/`471`) replays only the route-claim metadata on reconnect, never messages.

The fix is exactly what Karthi intuited: a durable per-topic queue, and advancing the Telegram offset only after we have persisted the message ourselves.

---

## Telegram retention (bounds what is possible)

Telegram stores undelivered updates for **at most 24 hours** (Bot API `getUpdates`: *"Incoming updates are stored on the server until the bot receives them either way, but they will not be kept longer than 24 hours."*). C3 can only queue what it has actually received. A >24h gap with **no broker polling anywhere** loses messages at Telegram's level — outside C3's control, no matter what we build.

**Future (explicitly NOT in this spec):** self-hosting the Telegram Bot API server extends retention beyond 24h. That is an endpoint swap only — `channels.telegram.api_base_urls` already supports a custom base URL — and requires **zero C3 code change**. Recorded here as forward context, not built now.

---

## Scope

**In scope**
- Durable per-route append-only on-disk queue + cursor + delete-on-drain + capped compaction.
- Persisted-offset reordering (advance offset only after durable persist).
- No-session **hold** (replace drop) + "held, nothing lost" auto-reply carrying the running count.
- **Backlog-on-attach**: compact summary + agent-driven `fetch_queue` pull (all-or-one-by-one).
- Per-adapter delivered semantics (Claude push-ack; Codex pull-ack).
- `/status` Telegram command (per-topic + global).
- Self-documenting STT-failure message.
- `retranscribe` MCP tool (both adapters).

**Out of scope (named so the plan doesn't drift into them)**
- Guaranteeing a broker is always polling. The opt-in `systemd --user` unit already exists (`docs/systemd/`); a hosted always-on broker is future work.
- Self-hosting the Telegram Bot API server (future; no C3 change needed).
- Cross-session / multi-topic awareness (each session handles its own topic's queue).
- Changing the output-mode protocol.

---

## Lifecycle model (decided: **B**)

> Chosen over "ACK-is-truth" (A) and "auto-replay-all" (C) because it keeps the live path lean — no per-message agent-context overhead when a session is healthy and watching — while the durable queue earns its keep only when there is no live consumer.

- **Live** (session attached when the message lands): push immediately, as today. Remove from the queue once the agent has *actually taken* it (definition is per-adapter, below).
- **Backlog** (arrived with no live consumer, or a live push failed): held in the durable queue; delivered on attach as a **compact summary**, then **pulled** by the agent all-or-one-by-one via `fetch_queue`.
- **Internal delivery acks are not agent overhead.** The Claude push-ack (below) is broker↔adapter plumbing the agent never sees. Karthi's "no overhead where not required" concern is about agent *context*, which this respects.

---

## Component 1 — Durable queue store (`internal/queue`, new package)

**On-disk layout** under `$XDG_STATE_HOME/c3/queue/` (fallback `~/.local/state/c3/queue/`), one pair of files per route key:

- `<routekey>.jsonl` — append-only; each line is one JSON-encoded `c3types.Inbound` (already carries text/transcript, `Attachments` with FileIDs, `Sender`, `Timestamp`, `ReplyTo`, `Kind`, and the source `update_id`).
- `<routekey>.cur` — a single integer: **line number** consumed so far (the "delivered up to here" cursor). Human-readable (`7` = first 7 lines delivered).

`<routekey>` is a filesystem-safe encoding of `(Channel, ChatID, TopicID)`; `TopicID` nil (DM) encodes as `none`. Example: `telegram__-1003990699908__914.jsonl`.

**Lifecycle (Karthi's model, verbatim):**
- **Append**: write one line, `fsync`. *Then* (Component 2) the source `update_id` becomes eligible to advance the offset.
- **Consume(n)**: read up to `n` lines starting at the cursor; advance the cursor; persist `.cur`.
- **Delete-on-empty**: when the cursor reaches EOF (all lines consumed), **delete both files**. The next append lazily recreates them.
- **No mid-life rewriting** on the normal path. One-by-one draining just walks the cursor; the file is deleted whole when it empties.
- **Compaction = cap valve only** (rare): when a route exceeds the cap, rewrite the file dropping the oldest lines, and adjust the cursor by the number dropped.

**Concurrency — single owner, no locks on files:**
- *Cross-process:* impossible — the broker is a flock singleton; adapters never open these files (they go through the broker over IPC).
- *Within the broker:* **all file operations for a route are funneled through that route's `RouteWorker` goroutine** (extend its existing `chan Job` with `JobFetch` / `JobConsume` job kinds). IPC handlers for `fetch_queue` drop a request on the worker's channel and await the reply; they never touch the file. One goroutine owns one file ⇒ zero races (validated with `go test -race`).

**Status index (for `/status`, which spans routes):** the store keeps a small mutex-guarded in-memory index `map[routeKey]{pending int, oldestUnix int64}`, updated by each worker on append/consume/evict. `/status` reads a snapshot under the lock. File I/O stays per-worker; only the cheap counters are shared. This is the one cross-route read and it touches no files.

**Caps — never silent:** per-route bound, **1000 messages OR 14 days, whichever first**. On overflow: drop oldest (compaction), log to broker.log, and send one Telegram notice in that topic (e.g. *"⚠️ queue full — dropped N oldest held messages; attach a session soon"*).

**Crash recovery:** the file + cursor are the only truth; pending-count is derived (lines after cursor). On startup, `RecoverOnStartup()` scans the queue dir: if `.cur` ≥ line count, delete the pair; else rebuild the index. A crash mid-consume leaves the cursor slightly behind ⇒ at-least-once re-delivery (dedupe by `message_id`). That is the safe failure direction: never lose, occasionally repeat.

---

## Component 2 — Persisted-offset tracker (`internal/channel/telegram`)

Today the offset is saved after `Emit` (before persist). It must advance only to the **highest contiguous `update_id` that has been durably persisted** (or is a no-op: gated, dropped, or a non-message update).

- The poll loop registers each accepted `update_id` as *in-flight*; gated/dropped/non-inbound updates are marked *done* immediately (nothing to persist).
- When a worker's `Append` + `fsync` succeeds, it reports the `update_id` to the tracker → marked *done*.
- The tracker advances `committedOffset` to the highest id whose entire prefix (`≤ id`) is *done*, and persists `committedOffset + 1` to the existing offset store (`offset_store.go`) periodically and on shutdown.

This keeps async, out-of-order, per-route STT/persist correct: an `update_id` whose message is still mid-STT is not *done*, so the offset can't pass it; if the broker crashes there, the offset never advanced and Telegram redelivers it (within 24h). Loss-free by construction.

**STT timing:** transcription stays at flush time (post-receipt, pre-persist) so the stored line already contains the transcript in `Text`. Storage is **per-message** (one line each); the existing debounce/`mergeBatch` remains a *delivery-presentation* concern only and does not merge stored lines.

---

## Component 3 — Delivery (per-adapter)

### Claude — push for live, summary+pull for backlog
- **Live:** worker appends → pushes full content via `notifications/claude/channel` (unchanged surface). The adapter reports delivery back to the broker (`OpInboundDelivered{update_id, ok}`); on `ok` the worker `Consume`s it (cursor advances, delete-on-empty). On failure, the broker retries the push a few times; if still failing, the message stays queued and surfaces as backlog.
- **Backlog (on attach):** the attach response carries `QueuedCount` + a compact `QueuedSummary`; the adapter renders it as a channel notification instructing the agent to call `fetch_queue`.
- **Recovery nudge:** whenever undelivered messages exist for an attached Claude session, the next successful push and the attach summary append "(N pending — call `fetch_queue`)", so Claude can always recover even after a failed push.
- Claude **gains** the `fetch_queue` tool (it has no inbound-pull tool today).

### Codex — everything is pull
Codex cannot render unsolicited notifications, so it already polls. Unify: **Codex's `inbox` tool becomes `fetch_queue`, broker-backed** (reads the durable queue, not a local ring). The in-memory cap-100 ring is **retired** (removes a fragility: ring loss on restart). Codex delivery = a lightweight "N pending" nudge (`notifications/message`) + the agent calling `fetch_queue`. Cursor advances on `fetch_queue(ack=true)`. Live and backlog are the same path for Codex.

This per-adapter split matches each agent's nature and adds no agent-context overhead in either.

---

## Component 4 — `fetch_queue` MCP tool (both adapters)

Broker-backed (new IPC `OpFetchQueue`). The adapter forwards to the broker, which routes the request through the claimed route's worker.

- **Params:** `limit` (integer, default **3**, max 50; or the string `"all"`) and `ack` (boolean, default **true** = consume / `false` = peek).
- **Returns:** the oldest up-to-`limit` messages with full content — `message_id`, `sender`, `kind`, `timestamp`, `text` (transcript for voice), `reply_to`, and `attachments` (each with `file_id`, `mime`, `size`, `name`) — plus `remaining` (count still queued).
- `ack=true` walks the cursor forward (and deletes the files when drained). The agent drains all (`limit:"all"`) or in small batches (default 3) for careful one-at-a-time processing.
- **Tool description** documents drain-all vs one-by-one and the ack semantics, so draining is native agent knowledge.

---

## Component 5 — `retranscribe` MCP tool (both adapters)

Broker-backed (new IPC `OpRetranscribe`). Re-runs the STT provider chain (`Plugins.FireOnVoiceReceived`) on a voice attachment's audio by `file_id` (downloading it if not cached), and returns the fresh transcript.

- **Params:** `file_id` (string, required); optional `message_id` (integer) — if the corresponding message is still queued, its stored `Text` is refreshed in place during a cap-safe rewrite (otherwise the transcript is just returned).
- **Returns:** `text` (new transcript) or `error` (provider still failing).
- **Why:** STT failures are usually a transient/down provider (e.g. the current Sarvam gap). This lets the agent fix a failed transcription once the provider is healthy — pairs with the pending native-Sarvam reimplementation. The agent learns of it from the self-documenting failure message (Component 6).

---

## Component 6 — Surfaces

### 6a. "Held, nothing lost" auto-reply (replaces the drop)
When a message is queued because no session is attached, the auto-reply reassures and counts:

> 📨 **Held — nothing lost.** No CLI is attached to this topic right now. **{N} message(s) queued** — they'll be delivered when you attach a session here. Send `/status` to check.

Cadence: send on the first queued message, then **at most once per cooldown window** (reuse the existing 5-min `fallbackTracker`) with the *running* count. Messages in between are queued silently — reassure without a reply per voice note.

### 6b. `/status` Telegram command
A **bot command sent in chat** (distinct from the `/c3:status` CLI slash command — different surface, no conflict). Intercepted in the poll path *before* gating/routing: an inbound whose text is `/status` (or `/status@<botname>`) is handled by the broker directly — it answers and is **never queued or routed to an agent** (its `update_id` is marked *done* in the tracker).

- **In a topic** → that topic's status:
  > 📊 **arogara** · 3 queued (oldest 2h) · no CLI attached · broker up
- **In DM / General** → global summary (empty queues omitted):
  > 📊 Broker up (pid 12345). Active queues:
  > • arogara — 3 (oldest 2h)
  > • proctor — 1 (oldest 10m)
  > 1 attached · 1 idle

Registered via `setMyCommands` so it autocompletes in Telegram's `/` menu. Implemented as a tiny command dispatcher — only `/status` wired now; trivially extensible (`/drain`, `/clear`) later (YAGNI for now).

### 6c. Self-documenting STT-failure message
On STT failure, the text the **agent** sees becomes a recovery instruction, not a dead end:

> ⚠️ **[voice transcription failed: {reason}]** The audio is saved and recoverable — **the user does not need to resend.** Call `download_attachment` with `file_id="{FileID}"` ({mime}, {duration}) to retrieve it, or `retranscribe` with the same `file_id` to re-run transcription.

So the agent natively knows the audio exists, exactly how to fetch it, that it can retry transcription, and that re-forwarding is never required.

---

## Data flow (end to end)

1. **Live (attached, Claude):** update → convert → gate → STT → **append+fsync** → offset eligible → push → adapter Notify → `OpInboundDelivered(ok)` → `Consume` → delete-on-empty.
2. **No session:** update → … → **append+fsync** → offset eligible → no claim → held-reply (cooldown'd, running count). Message stays in queue.
3. **Attach with backlog:** `attach` → claim → attach resp carries `{QueuedCount, QueuedSummary}` → agent sees summary → `fetch_queue(limit:3|all, ack:true)` → worker `Consume`s → cursor advances → delete-on-empty when drained.
4. **Codex (any):** update → … → append → "N pending" nudge → `fetch_queue` → `Consume`.
5. **STT failure:** STT chain fails → stored `Text` = self-documenting message → delivered/queued normally → agent may `retranscribe(file_id)` or `download_attachment(file_id)`.

---

## Interfaces (for the implementation plan)

**New package `internal/queue`** — `Store` with worker-invoked methods: `Append(routeKey, *Inbound) error`, `Peek(routeKey, n) ([]Inbound, error)`, `Consume(routeKey, n) ([]Inbound, error)`, `Pending(routeKey) (int, time.Time)`, `StatusAll() map[routeKey]Status`, `EvictOverCap(routeKey) (dropped int, err error)`, `RecoverOnStartup() error`. (EvictOverCap does file I/O — atomic rewrite — so it returns an error; the implementation plan uses this `(int, error)` form.)

**New IPC ops (`internal/ipc`)**
- `OpFetchQueue` — `FetchQueueReq{Limit int, All bool, Ack bool}` → `FetchQueueResp{Messages []c3types.Inbound, Remaining int}`.
- `OpInboundDelivered` — `InboundDeliveredMsg{UpdateID int64, OK bool}` (Claude live-ack; adapter→broker).
- `OpRetranscribe` — `RetranscribeReq{FileID string, MessageID int64}` → `RetranscribeResp{Text string, Err string}`.
- Attach response extension — `QueuedCount int`, `QueuedSummary []QueuedItem{MessageID int64, Sender, Kind string, Unix int64, Preview string}`.

**New MCP tools (both adapters):** `fetch_queue{limit?:int=3|"all", ack?:bool=true}`, `retranscribe{file_id:string, message_id?:int}`.

**Files touched (indicative)**
- New: `internal/queue/*.go`, queue tests.
- `internal/broker/worker.go` — append-before-deliver; `JobFetch`/`JobConsume`; per-adapter delivered semantics; held-reply via the queue.
- `internal/broker/host.go` / `attach.go` — backlog summary in attach resp; worker job plumbing.
- `internal/channel/telegram/poll.go` — persisted-offset tracker; `/status` intercept.
- `internal/channel/telegram/*` — `setMyCommands` registration; command dispatcher.
- `internal/broker/dispatch.go` — `OpFetchQueue` / `OpRetranscribe` handlers.
- `internal/broker/worker.go` (STT block) — self-documenting failure text.
- `internal/ipc/messages.go` — new ops/structs.
- `cmd/c3-claude-adapter/main.go` — `fetch_queue` + `retranscribe` tools; `OpInboundDelivered`; backlog-summary rendering; recovery nudge.
- `cmd/c3-codex-adapter/main.go` — `inbox`→`fetch_queue` (broker-backed); retire in-memory ring; `retranscribe`; nudge.
- Docs: `docs/USAGE.md`, `docs/CHANNELS.md` (queue + `/status`), `INSTALL.md` (if needed), `ROADMAP.md`.

---

## Error handling & edge cases

- **Push fails (Claude):** no `ok` ack → retry a few times → else leave queued (backlog path + recovery nudge). Never removed unacked.
- **Broker crash mid-STT:** offset not advanced → Telegram redelivers within 24h. Loss-free.
- **Broker crash mid-consume:** cursor behind → at-least-once re-delivery; dedupe by `message_id`.
- **Cap overflow:** drop-oldest + log + Telegram notice. Never silent.
- **`/status` must not be queued or routed** — handled and acked as *done*.
- **DM vs topic routing** for both the held-reply and `/status`.
- **Corrupt `.jsonl` line on recovery:** skip the bad line, log it, continue (don't fail the whole queue).
- **Disk full on append:** treat as persist failure → do not advance offset → Telegram retains → log + (best-effort) Telegram notice.

---

## Testing plan

- **Queue unit:** append→cursor→delete-on-empty; `Peek` vs `Consume`; crash-recovery (cursor behind → at-least-once); cap-overflow compaction + cursor adjust; corrupt-line skip.
- **Offset tracker:** contiguous-prefix advance with out-of-order persist; gated/dropped updates don't block; crash before persist → no advance.
- **Concurrency:** `go test -race` hammering one route worker with interleaved appends + fetches → no races.
- **Delivery:** no-session → queued + held-count reply (cooldown'd running count); attach → backlog summary + `fetch_queue` (all *and* one-by-one, ack true/false); push-fail → stays queued + recovery nudge.
- **Per-adapter:** Claude push-ack removes; Codex `fetch_queue`-ack removes; both expose `fetch_queue` + `retranscribe`.
- **Surfaces:** `/status` in-topic vs global; held-reply wording + count; STT-failure text includes `file_id` and recovers via `download_attachment`; `retranscribe` re-runs the chain.
- **Both adapters** build and pass.

---

## Decisions log

- **Scope = "from receipt onward"** (Option 1). Always-on polling and the self-hosted Bot API server are future, no C3 change for the latter.
- **Lifecycle = B** (push live, queue fills gaps). Chosen for no agent-context overhead on the live path.
- **Cursor = line number** (not byte offset) — human-readable; worker holds an open reader so no mid-life rescans; `.cur` is the crash checkpoint only.
- **Compaction = delete-on-drain + cap-only rewrite.** No per-line tombstones, no infinite list.
- **`fetch_queue` default `limit` = 3** (careful one-at-a-time-ish processing; bump to 5 if too granular).
- **Caps = 1000 msgs / 14 days**, drop-oldest, never silent.
- **STT recovery = self-documenting message + `retranscribe` tool.**
- **Codex in-memory ring retired** in favor of the broker-backed durable queue.
- **No new dependencies.**
