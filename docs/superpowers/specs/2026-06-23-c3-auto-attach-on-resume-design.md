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

### Why adapter-issued (not broker-at-hello) claim

The Claude adapter's `run()` is `connectBroker → hello → buildMCPServer →
brokerReader → server.Run` (`cmd/c3-claude-adapter/main.go:109-125`), with no
`autoAttach` (unlike Codex, which has one at `cmd/c3-codex-adapter/main.go:100`).
If the broker claimed during hello, backlog delivery could fire **before**
`brokerReader` is up and be missed. So the claim must be issued by the adapter
*after* the reader is running — mirroring Codex's proven `autoAttach`, but
gated on the recovery signal. The claim still executes server-side through
`tryClaim`.

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

4. **Hello recovery decision** — in the hello handler
   (`internal/broker/handler.go:89-106`), the precedence (see below). When
   recovery applies, set the (currently vestigial) ack fields so the adapter
   knows to claim and the boot text reads correctly:
   ```go
   ack.AutoAttached = true
   ack.Mapping = wireMapping(sa)   // ipc.HelloAckMsg.Mapping (messages.go:138-139)
   ```

5. **Adapter auto-claim** — in Claude `run()` after `brokerReader` starts
   (`cmd/c3-claude-adapter/main.go:109-125`), if
   `helloAck.AutoAttached && helloAck.Mapping != nil`, issue an `AttachReq`
   for that route (by name/key, carrying `SessionID`). This flows through
   `attachByName → tryClaim`. On a `force_steal` collision proposal: **do not
   steal** — surface it (render in boot instructions / let the agent decide).

6. **Boot instructions** — `buildInstructions()`
   (`cmd/c3-claude-adapter/main.go:1081-1095`) already has an
   `AutoAttached && Mapping != nil` branch that renders "Auto-attached to
   …" — it just was never reached before. Recovery now triggers it, so the
   agent sees it's attached and won't reflexively re-call `attach`.

7. **Record on attach** — in `persistMapping`
   (`internal/broker/attach.go:691-721`, where it already
   `UpsertMapping + SaveMappings`), also `UpsertSessionAttachment(stub.SessionID,
   …)` when `stub.SessionID != ""`. Every attach path funnels its persist
   through here, so one write point covers all of them.

8. **Tombstone on explicit detach** — in `OpRelease`
   (`internal/broker/handler.go:134-136`), mark
   `session_attachments[stub.SessionID].Detached = true` (or delete it). **Do
   NOT** clear on the dead-PID conn-drop path (`handler.go:76-86`) — that's a
   process exit, not a user detach; clearing there would wipe recovery on
   every quit-without-detach and defeat the feature.

9. **Claim safety** — the recovery claim routes through `tryClaim`
   (`internal/broker/attach.go:364,538-…`). A topic held by another **live**
   session yields the `force_steal` proposal, never a silent steal. A resumed
   session's *old* PID is dead, so its prior claim is auto-released on
   conn-drop (`handler.go:76-86`) and self-recovery never collides with itself.

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
| 1 | Topic held by another live session on resume | Surface the `force_steal` collision prompt; never auto-steal. |
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
  ack carries `AutoAttached + Mapping`; precedence (session-id beats cwd
  mapping incl. the root-has-a-mapping case); `OpRelease` tombstones;
  conn-drop does **not** clear; collision → `force_steal` proposal (reuse the
  existing `heldByDifferentLiveSession` / `tryClaim` tests as the pattern).
- **adapter unit:** hello includes `SessionID` from env; `run()` issues the
  auto-claim only when `AutoAttached && Mapping != nil`; collision proposal is
  surfaced, not auto-stolen.
- **persist:** every attach path records the session attachment via
  `persistMapping`.
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
