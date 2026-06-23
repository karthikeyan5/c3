# C3 Auto-Attach on Session Resume — Design Spec

**Date:** 2026-06-23
**Status:** DRAFT — awaiting Karthi's review
**Branch (proposed):** `feat/auto-attach-resume`

## Goal

When Karthi resumes an existing Claude Code session (`claude --resume`), C3
automatically re-attaches that session to the Telegram topic it was last
attached to — **without** him having to `cd` into the project dir and call
`attach` every time. Claude Code first; Codex deferred.

## Problem (code-grounded root cause)

C3's only auto-attach today is **cwd-based** and **agent-driven**:

- The Claude adapter sends `hello{CLI, PID, CWD: os.Getwd()}` at launch
  (`cmd/c3-claude-adapter/main.go:284-288`). `CWD` is the adapter process's
  own working dir, fixed at spawn — a later shell `cd` never reaches it.
- The broker's hello handler (`internal/broker/handler.go:89-106`) only sets
  ack flags: `NoConfig`, or `NoMapping` when `LookupByCwd(hello.CWD)` misses.
  It does **not** itself claim a route.
- The actual claim happens when the **agent** calls the `attach` tool with
  empty `expr` → `toolAttach` sends `AttachReq{CWD}` → broker `attachByName`
  saved-mapping fast path (`internal/broker/attach.go:319-377`) →
  `LookupByCwd(cwd)` → `tryClaim` (`attach.go:364`).

Karthi resumes from `~/arogara` (root). Two things break there:
1. The adapter's `CWD` is root, so a cwd lookup can't find his project's topic.
2. Worse, **root already has a cwd mapping** (`~/arogara` → `claw-migrate`,
   topic 2483, in `mappings.json`). So even the cwd path would claim the
   wrong topic, not the session's real one.

Claude Code **does** expose a stable per-session id to the adapter —
`CLAUDE_CODE_SESSION_ID` is present in every running adapter's
`/proc/<pid>/environ` (verified live) and is stable across `claude --resume
<id>`. **The adapter ignores it today.** That env var is the lever.

## Non-goals

- Codex auto-attach-on-resume (Codex exposes no comparable stable session id
  to its MCP env; deferred — matches Karthi's "worry about Codex later").
- Changing the existing cwd-based / agent-driven attach for fresh sessions.
- Auto-*creating* topics on resume. Recovery only re-claims a topic the
  session was *already* attached to.
- Output-mode behavior (CLI vs Telegram) is untouched.

## Approach

Add an **additive** session-id recovery path. The broker keeps a
`session_attachments` store (`sessionID → last-attached route`). On hello,
if a valid entry exists, the broker signals recovery in the ack; the adapter,
once its MCP server + broker-reader are up, auto-issues a claim for that route
through the **existing** attach machinery (collision-safe, backlog-replay,
welcome). The existing cwd path is left untouched as a fallback.

### Claim realized broker-side at hello (pull-model backlog)

The broker performs the recovery claim during the hello handshake
(`internal/broker/handler.go` HandleConn), using the low-level
`b.Routes.Claim` — NOT `tryClaim`, which writes an AttachedMsg to the conn the
adapter isn't yet expecting. This is safe because C3's backlog is **pull, not
push**: claiming a route does NOT flush its queued backlog (the normal attach
path only *reports* a count via `withBacklog`; the agent pulls with
`fetch_queue`). So there is no flood-before-`brokerReader`-is-up race. Only NEW
messages push after the claim, by which point `run()` has started `brokerReader`
(and the OS socket buffer covers the brief gap regardless). The held-backlog
count travels in the hello ack (`QueuedCount`, new field) so the boot
instructions can tell the agent to call `fetch_queue`. A topic held by another
live session is detected first (`heldByDifferentLiveSession`) and recovery is
skipped (falls through to the normal cwd / NoMapping flow) — never a silent
steal. A resumed session's own prior claim, if still lingering, is held by a
dead PID, so `Routes.Claim` releases it and grants the new one.

## Data model

New top-level key in `MappingsFile` (`internal/mappings/types.go:8-16`),
alongside the existing `mappings` map (same config-file, same atomic
Read/Write/mutate machinery — consistent with how cwd mappings are stored):

```go
// SessionAttachment records the last topic a CLI session was attached to,
// keyed by the host CLI's stable session id, so a resumed session can be
// re-attached automatically regardless of its launch directory.
type SessionAttachment struct {
    Channel        string    `json:"channel"`
    ChatID         int64     `json:"chat_id"`
    TopicID        *int64    `json:"topic_id,omitempty"`
    Name           string    `json:"name"`
    Group          string    `json:"group,omitempty"`
    CWD            string    `json:"cwd,omitempty"`            // informational
    LastAttachedAt time.Time `json:"last_attached_at"`
    Detached       bool      `json:"detached,omitempty"`      // tombstone
}

// MappingsFile gets:
SessionAttachments map[string]SessionAttachment `json:"session_attachments,omitempty"`
```

`omitempty` keeps existing config files byte-identical until the first
session attaches. New accessors next to `UpsertMapping`
(`internal/mappings/mutate.go`): `UpsertSessionAttachment`,
`LookupSessionAttachment`, `DeleteSessionAttachment`, and a TTL sweep.

## Mechanism (end-to-end) + integration points

All file:line refs are current-tree (per research 2026-06-23); the plan
re-verifies each.

1. **IPC** — add `SessionID string \`json:"session_id,omitempty"\`` to
   `ipc.HelloMsg` (`internal/ipc/messages.go:126-132`). Additive-omitempty
   (IPC versioning convention); older brokers ignore it.

2. **Adapter hello** — read the env and set the field
   (`cmd/c3-claude-adapter/main.go:284-288`):
   ```go
   SessionID: os.Getenv("CLAUDE_CODE_SESSION_ID"),
   ```

3. **Stub carries it** — add `SessionID string` to `Stub`
   (`internal/broker/stubs.go`); extend `Stubs.Register(...)` to take it; pass
   `hello.SessionID` at both call sites (`internal/broker/handler.go:51,59`).

4. **Hello recovery (broker, the core)** — in the hello handler
   (`internal/broker/handler.go:89-106`), BEFORE the cwd/NoMapping logic
   (session-id precedence): if `hello.SessionID` resolves to a valid (non-
   tombstoned, within-TTL) session attachment whose route is NOT
   `heldByDifferentLiveSession`, claim it via `b.Routes.Claim(key, stub)` +
   `stub.SetRoute(&key)`, refresh the attachment's `LastAttachedAt`, and set:
   ```go
   ack.AutoAttached = true
   ack.Mapping = wireMapping(sa)               // ipc.HelloAckMsg.Mapping (messages.go:139)
   ack.QueuedCount, _ = b.backlogSummary(key)  // ipc.HelloAckMsg.QueuedCount (NEW)
   ```
   On collision, or no/expired/tombstoned attachment, skip recovery and fall
   through to the existing cwd-mapping / NoMapping logic.

5. **New ack field** — add `QueuedCount int \`json:"queued_count,omitempty"\``
   to `ipc.HelloAckMsg` (additive-omitempty).

6. **Boot instructions** — `buildInstructions()`
   (`cmd/c3-claude-adapter/main.go:1089-1091`) already has the
   `AutoAttached && Mapping != nil` branch rendering "Auto-attached to …";
   recovery now triggers it. Extend that branch so that when `QueuedCount > 0`
   it appends "N message(s) held — call `fetch_queue` to retrieve." The agent
   thus knows both that it is attached (won't reflexively re-`attach`) and that
   it should drain.

7. **Record on attach** — in `persistMapping`
   (`internal/broker/attach.go`, where it already `UpsertMapping +
   SaveMappings`), also `UpsertSessionAttachment(stub.SessionID, …)` when
   `stub.SessionID != ""`. The **topic** attach paths (attachByTopicID,
   attachByName, createAndClaim) funnel their persist through `persistMapping`.
   The **DM** path (`attachDM`) must NOT write a cwd default, so it records the
   session attachment via a sibling helper `recordSessionAttachment`
   (session entry only, nil TopicID → DM route key). Both paths are covered.

8. **Tombstone on explicit detach** — in `OpRelease`
   (`internal/broker/handler.go:134-136`), mark
   `session_attachments[stub.SessionID].Detached = true` (or delete it). **Do
   NOT** clear on the dead-PID conn-drop path (`handler.go:76-86`) — that's a
   process exit, not a user detach; clearing there would wipe recovery on
   every quit-without-detach and defeat the feature.

9. **Claim safety** — recovery uses `b.heldByDifferentLiveSession(key, stub)`
   (`internal/broker/attach.go:510`) FIRST: if a different **live** session
   holds the route, recovery is **skipped** (no claim, no steal) and the flow
   falls through to the normal cwd / NoMapping path so the agent can attach
   explicitly (and get the usual `force_steal` prompt if it wants the topic).
   When not held by a live other session, `b.Routes.Claim(key, stub)` grants
   it; a resumed session's *old* PID is dead, so any lingering self-claim is
   released by `Claim` and re-granted. Recovery never calls `tryClaim` (which
   would write an unexpected AttachedMsg to the just-handshaked conn).

## Precedence order (on hello, Claude)

1. No channels configured → `NoConfig` *(unchanged)*.
2. **Session-id recovery** — `hello.SessionID` present **and**
   `session_attachments[id]` exists, not `Detached`, within TTL → recover to
   that route. **Takes precedence over the cwd mapping** (this is what makes
   resume-from-root work; root's stale `claw-migrate` mapping must not shadow
   the session's real topic) *(new)*.
3. cwd mapping hit → existing agent-driven empty-attach claims it *(unchanged)*.
4. else → `NoMapping` *(unchanged)*.

Explicit user `attach` always overrides whatever recovery did (it's a normal
claim with the user's argument).

## Edge cases & decisions (defaults — please confirm)

| # | Case | Decision (default) |
|---|------|--------------------|
| 1 | Topic held by another live session on resume | Skip recovery (no claim, no steal); fall through to normal flow so the agent can attach explicitly. Never auto-steal. |
| 2 | User did an explicit `detach` earlier | Tombstone (`Detached=true`) → resumed session stays unattached until it attaches again. |
| 3 | Session quit without detach (conn-drop) | Entry kept → next resume recovers. (This is the core feature.) |
| 4 | Project dir moved/renamed | Still recovers (session-id is dir-independent); stored `cwd` is informational only. |
| 5 | Bare `--continue` / `--resume` (no id) mints a fresh `CLAUDE_CODE_SESSION_ID` | No entry → falls through to cwd → today's behavior. No regression. (Medium-confidence on the fresh-id claim; plan verifies.) |
| 6 | Stale entries | TTL prune (proposed **30 days** since `LastAttachedAt`) on load/sweep, so ancient ids don't resurrect topics. |
| 7 | `session_attachments` grows the config file | Bounded by tombstone-clear + TTL. Alternative considered: a sibling state file under `$XDG_STATE_HOME/c3`. Default: keep in `mappings.json` (consistent with the existing `mappings` state). |
| 8 | Recovery fires, then agent reflexively calls `attach` (empty, cwd) | Boot text says "Auto-attached to X" so the agent shouldn't; if it does, that's an explicit cwd claim and overrides — acceptable. |

## Codex (future)

Codex already auto-attaches by inferred name at launch
(`cmd/codex/main.go:62-98` sets `C3_ATTACH_NAME`;
`cmd/c3-codex-adapter/main.go:100` `autoAttach`), but has the same
resume-from-root gap and **no documented stable session id** in its MCP env.
So session-id recovery does not port cleanly. Deferred; revisit once Codex's
session-id story is confirmed.

## Testing strategy

- **mappings unit:** Upsert/Lookup/Delete/TTL-sweep of `SessionAttachments`;
  round-trip JSON; `omitempty` keeps old files byte-identical.
- **broker unit:** hello with a known `SessionID` + a stored attachment →
  ack carries `AutoAttached + Mapping + QueuedCount` and the route is claimed
  (`stub.CurrentRoute()` set); precedence (session-id beats cwd mapping incl.
  the root-has-a-mapping case); `OpRelease` tombstones; conn-drop does **not**
  clear; held-by-another-live-session → recovery skipped, no claim, falls
  through (reuse the existing `heldByDifferentLiveSession` tests as the pattern).
- **adapter unit:** hello includes `SessionID` from env; `buildInstructions`
  renders the "Auto-attached … N held — call fetch_queue" line when the ack
  has `AutoAttached + Mapping (+ QueuedCount)`.
- **persist:** topic attaches record the session attachment via
  `persistMapping`; the DM attach records via `recordSessionAttachment` (its
  own path, tested through `attachDM`).
- **churn:** a reconnect within `sessionRefreshInterval` (1h) of the last
  attach does NOT rewrite `mappings.json`; a stale one does (TTL stays
  reliable).
- **race:** `go test -race ./...` green.
- **live:** attach this session to a topic; quit; `claude --resume` from
  `~/arogara` root → it re-attaches to the same topic with no manual `cd` +
  `attach`; held backlog (if any) is delivered.

## Open questions for Karthi

1. **Precedence** — confirm session-id recovery should beat the cwd mapping
   for resumed sessions (required for resume-from-root to work). Default: yes.
2. **TTL** — 30 days OK, or different?
3. **Storage** — `session_attachments` inside `mappings.json` (default) vs a
   separate state file?
4. **Detach semantics** — a deliberately-detached session staying unattached
   on next resume (default) — agreed?
