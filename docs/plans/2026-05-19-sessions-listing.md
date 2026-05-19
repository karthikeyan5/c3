# Plan — `/c3:sessions` listing slash command (TODO #19e)

**Status:** drafted 2026-05-19. Closes the last sub-item of TODO #19.
Karthi authorized 2026-05-19 ("build it now").

## Goal

Add a `/c3:sessions` slash command that lists every live Claude Code /
Codex process the broker is currently tracking with their attached
state, so a human running `claude` (or `codex`) can disambiguate WHICH
terminal tab owns WHICH topic — without dropping to `ps` /
`/c3:topics` triangulation.

Mirrors the existing `/c3:ping` (TODO #19b) and `/c3:pair` (TODO #1)
stacks end-to-end (slash → CLI subcommand → IPC op → broker handler).

## Surface (verbatim from brief)

- `plugins/c3/commands/c3-sessions.md` — slash command (mirror
  `c3-ping.md` / `c3-pair.md`).
- `cmd/c3-broker/sessions.go` — CLI subcommand wired into
  `cmd/c3-broker/main.go`'s dispatch + usage block.
- `internal/ipc/ops.go` — new `OpListSessions` + reply op constant.
- `internal/ipc/messages.go` — new `ListSessionsReq` /
  `ListSessionsReplyMsg` + `SessionEntry` wire types.
- `internal/broker/handler.go` — new `handleListSessions` wired into
  the dispatch switch.

## Wire shape

```go
type ListSessionsReq struct {
    Op  Op  `json:"op"`  // = OpListSessions
    PID int `json:"pid"` // caller's parent PID for "this is me"
    CWD string `json:"cwd"`
}

type ListSessionsReplyMsg struct {
    Op       Op             `json:"op"` // = OpListSessionsReply
    Sessions []SessionEntry `json:"sessions"`
}

type SessionEntry struct {
    CLI           string `json:"cli"`           // "claude" | "codex" | "?"
    PID           int    `json:"pid"`
    CWD           string `json:"cwd"`
    ConnID        uint64 `json:"conn_id"`
    AttachedTo    string `json:"attached_to,omitempty"` // "<topic-name> (<group>)" or "dm" or ""
    IsThisSession bool   `json:"is_this_session,omitempty"`
}
```

Ordering: most-recently-registered first, i.e. **descending ConnID**
(`StubRegistry` mints ConnIDs via `atomic.Uint64.Add`, so this is the
correct freshness proxy — same approach used by
`handlePingThisSession`).

## Broker handler

```go
func (b *Broker) handleListSessions(conn *ipc.Conn, raw []byte) {
    var req ipc.ListSessionsReq
    _ = json.Unmarshal(raw, &req)  // tolerate empty body
    snap := b.Stubs.Snapshot()
    // Filter out the transient ping/topics/sessions CLI client itself.
    // The c3-broker-cli stub never attaches and would clutter the list;
    // since the caller IS that stub, we'd otherwise be listing
    // ourselves. Identify by CLI name (matches dialBroker in client.go).
    mappings := b.Mappings()
    entries := make([]ipc.SessionEntry, 0, len(snap))
    for _, s := range snap {
        if s.CLI == "c3-broker-cli" {
            continue
        }
        e := ipc.SessionEntry{
            CLI:    s.CLI,
            PID:    s.PID,
            CWD:    s.CWD,
            ConnID: s.ConnID,
        }
        if rk := s.CurrentRoute(); rk != nil {
            e.AttachedTo = sessionTopicLabel(b, *rk, mappings)
        }
        if req.PID != 0 && req.PID == s.PID {
            e.IsThisSession = true
        }
        entries = append(entries, e)
    }
    // Most-recent first.
    sort.SliceStable(entries, func(i, j int) bool {
        return entries[i].ConnID > entries[j].ConnID
    })
    _ = conn.WriteJSON(ipc.ListSessionsReplyMsg{
        Op:       ipc.OpListSessionsReply,
        Sessions: entries,
    })
}
```

`sessionTopicLabel` is the brief's "topic name + group" formatter.
The brief asks us to **reuse `pingTopicLabel`** for the label —
`pingTopicLabel` currently renders just `"<name>"` or `"dm"` /
`"topic-<id>"`. The brief's output example shows
`"c3 (main)"` form — so the table-level rendering of `(group)` lives
in the CLI subcommand, not the broker. The broker emits a
**structured-enough** label that the CLI can format. Concretely: the
broker emits the raw topic name (via `pingTopicLabel`) and **also**
includes the group as a separate-enough hint by appending `(group)`
when the topic has a non-empty group. Cleanest split: a tiny
`sessionTopicLabel` helper colocated with `pingTopicLabel` that
returns `"name (group)"` (or just `"name"` / `"dm"` / `"topic-<id>"`).
This keeps `pingTopicLabel` unchanged for `/c3:ping`'s use and gives
the listing its richer label.

## CLI subcommand

```go
func runSessions(_ []string) error {
    cwd, _ := os.Getwd()
    ppid := os.Getppid() // see "This is me" notes below
    conn, err := dialBroker()
    if err != nil { return err }
    defer conn.Close()
    if err := conn.WriteJSON(ipc.ListSessionsReq{
        Op: ipc.OpListSessions, PID: ppid, CWD: cwd,
    }); err != nil { return fmt.Errorf("write list_sessions: %w", err) }
    raw, err := conn.ReadFrame()
    if err != nil { return fmt.Errorf("read list_sessions_reply: %w", err) }
    var resp ipc.ListSessionsReplyMsg
    if err := json.Unmarshal(raw, &resp); err != nil { ... }

    // Format. Brief asks for a plain markdown-friendly table.
    if len(resp.Sessions) == 0 {
        fmt.Println("0 c3 sessions.")
        return nil
    }
    // Column widths: compute on the fly so wide cwd / long topic
    // names still align.
    fmt.Printf("%d c3 session(s):\n\n", len(resp.Sessions))
    // header + body using padded columns.
    // ...
    return nil
}
```

### "This is me" detection — design call

Brief: "the slash command invokes `c3-broker sessions` as a child of
the CC session, so the CLI's PID is the parent of `c3-broker
sessions`'s PID. (Actually verify this — Claude Code may spawn shells
differently. If parent-PID-walk is needed, do it with `os.Getppid()`
from the CLI side and pass that.)"

Decision: pass `os.Getppid()` over the wire. Reason: the slash command
runs `!c3-broker sessions` (the `!` prefix in `c3-pair.md` /
`c3-ping.md` indicates Bash-execution), which means Claude Code
spawns a shell that runs the binary. So:

- For Claude: `claude` (PID X) → `sh -c "c3-broker sessions"`
  (PID Y) → `c3-broker sessions` (PID Z). Z's PPID is Y, not X.
- Codex's `!` shell-out has the same shape.

Direct `os.Getppid()` lands at the shell PID, not the CC PID. So a
single-level PPID walk WON'T find the actual session.

**The brief explicitly anticipated this** and says: "If parent-PID-
walk is needed, do it with `os.Getppid()` from the CLI side and pass
that." Plan: walk the PPID chain from `c3-broker sessions` upward via
`/proc/<pid>/status` (Linux) until we hit a process named `claude`,
`codex`, `c3-claude-adapter`, or `c3-codex-adapter`. Pass that PID
over the wire. Limit walk depth to 10 to bound failure cases.

Fallback when walk fails (non-Linux, /proc missing, all parents
already exited, etc.): pass `0` and accept that NO entry will be
flagged `IsThisSession=true`. The user still gets the listing; just
without the "you are here" marker. Tolerable degradation.

For symmetry: also try `os.Getppid()` directly (single-level) as a
seed in case the slash command ever changes to not shell out. We
match the seed AND every ancestor up the walk, against each stub's
`PID`.

### CLI detection — best-effort

Brief: best-effort. Walk PPID up looking for an executable named
`claude`, `codex`, or `c3-claude-adapter` / `c3-codex-adapter`. If
walk fails, label as `?`.

Decision: **this work is done broker-side**, NOT in the
sessions CLI subcommand. Reason: each stub's `CLI` field is set at
`HelloMsg.CLI` time by the adapter itself — the adapter knows what it
is (`claude` for the Claude adapter, `codex` for Codex). So the
broker ALREADY HAS the right answer in `Stub.CLI` — no PPID walk
required. The brief's "?" fallback applies only when an adapter sent
a blank CLI string (theoretical — shouldn't happen in practice). The
broker handler maps empty CLI → "?".

This is a deliberate departure from the brief's "walk PPID
looking for an executable" approach — but better, because the
information is already authoritative on the broker side. No reason to
re-derive it via process introspection.

## Format — table rendering

Brief gave this example:

```
3 c3 sessions:

CLI     PID     CWD              Attached         This?
claude  159254  /projects/c3     c3 (main)        yes
claude  166545  /projects/c3     -                no
codex   170123  /projects/widget widget-foo (...)  no
```

Implementation: compute max column width per column across all rows,
then `fmt.Sprintf("%-*s", width, val)`. `-` for empty AttachedTo.
`yes` / blank (NOT `no`) for IsThisSession — keeps the "you are
here" marker visually quiet for the non-matching rows. Also
home-shorten CWD with `~` prefix the same way `pingText` does (matches
the welcome / ping aesthetic).

Header line + body lines. Indent / styling matches `c3-broker
topics` output (which is similar table-shape — verified pattern.)

## Tests

### IPC roundtrip — `internal/ipc/messages_test.go`

1. `TestListSessionsReq_Roundtrip` — marshal/unmarshal preserves
   `Op` + `PID` + `CWD`.
2. `TestListSessionsReplyMsg_Roundtrip_NonEmpty` — slice of 2
   entries; assert all fields preserved.
3. `TestSessionEntry_IsThisSessionFieldOmitEmpty` — when false, the
   key MUST be omitted from JSON (wire-additive).
4. `TestSessionEntry_AttachedToFieldOmitEmpty` — when empty,
   `attached_to` key MUST be omitted.

### Broker handler — `internal/broker/handler_test.go` (or a new
`sessions_test.go` if more readable)

5. `TestListSessions_ReturnsAllStubs` — register 3 stubs, ask for
   sessions, expect 3 entries (broker filters the c3-broker-cli
   ping-style transient itself, but the test directly registers
   adapter-style stubs so all 3 land).
6. `TestListSessions_MarksThisSession` — caller PID matches one
   stub's PID → exactly that entry has `IsThisSession=true`; all
   others false.
7. `TestListSessions_NoStubs_ReturnsEmptyList` — empty registry →
   `Sessions: []` (NOT nil), so `json.Marshal` emits `[]` and the
   CLI side's `len(resp.Sessions) == 0` branch fires.
8. `TestListSessions_AttachedTo_FormatsTopicLabel` — stub with an
   active topic claim → `AttachedTo == "<name> (<group>)"`.
9. `TestListSessions_AttachedTo_EmptyForUnattachedStub` — stub
   without `CurrentRoute()` → `AttachedTo == ""`.
10. `TestListSessions_OrderedByConnIDDesc` — register stubs in
    order A, B, C → reply is C, B, A.
11. `TestListSessions_FiltersTransientClientStub` — register a stub
    with `CLI=c3-broker-cli` plus one normal adapter; reply has 1
    entry.

### CLI subcommand — `cmd/c3-broker/sessions_test.go`

Brief mostly covers the broker side. The CLI layer is a thin client.
Plan to test ONLY the rendering helper as a pure function:

12. `TestRenderSessionsTable_EmptyList` — `0 c3 sessions.\n` output.
13. `TestRenderSessionsTable_SingleSession_NoAttach` — single row,
    AttachedTo rendered as `-`.
14. `TestRenderSessionsTable_HomeShortenedCWD` — CWD starting with
    `os.UserHomeDir()` is rendered with `~` prefix.
15. `TestRenderSessionsTable_MarksThisSession` — exactly one row has
    `yes` in the This? column; others blank.

I'm deliberately not testing the `runSessions` function end-to-end
against a live broker — those broker-handler tests above cover the
wire contract. The CLI's job is parse + format, both pure.

### PPID walk helper — `cmd/c3-broker/sessions.go`

The PPID-walk is Linux-only (uses `/proc/<pid>/status`). Plan: gate
the implementation on `runtime.GOOS == "linux"` (the broker's only
supported platform per `internal/broker/stubs.go::isPIDAlive` using
syscall.Kill — already Linux-pinned). On non-Linux: return ppid from
`os.Getppid()` directly without walking; the single-level fallback
is the documented degradation.

16. `TestWalkUpToCLIPID_FindsClaude` — set up a fake `/proc` tree?
    No — too invasive. Skip this test; the walk is pure-stdlib
    `/proc` parsing and the integration test (#6) already validates
    end-to-end "this is me" matching with a known PID.

## Implementation order (TDD)

1. Write the failing IPC roundtrip tests (#1–#4); add the wire
   types; tests pass.
2. Write the failing broker handler tests (#5–#11); add
   `handleListSessions` + the dispatch switch case + the
   `sessionTopicLabel` helper; tests pass.
3. Write the failing CLI rendering tests (#12–#15); add
   `cmd/c3-broker/sessions.go` + the renderer; tests pass.
4. Wire `runSessions` into `cmd/c3-broker/main.go` dispatch + usage
   block.
5. Author `plugins/c3/commands/c3-sessions.md` (cosmetic — no test).
6. Update TODO #19(e) + parent #19; append MORNING-REVIEW section.
7. Verification: `go test -count=1 -race ./...`, `go vet ./...`,
   `go build ./...`.

## Self-review rubric for the plan

- [ ] Every wire field has a clear owner: broker computes
      AttachedTo, broker tags IsThisSession, broker filters the
      transient client. CLI just renders.
- [ ] No new dependency. (Stdlib only — `sort` is already used in
      the package; `runtime` for the Linux gate.)
- [ ] Tests at the right level — IPC roundtrip, broker handler with
      stub registry, CLI renderer with pure helper.
- [ ] Failure modes documented: PPID walk fails → 0, no "this"
      marker; non-Linux → single-level PPID fallback; missing CLI
      string → "?".
- [ ] Mirrors existing patterns (`ping.go`, `pair.go`,
      `handlePingThisSession`) so future maintainers find no
      surprises.

## Open follow-ups (NOT blocking #19e closure)

- "Disconnected" stubs (claim survives PID-alive after conn drop)
  show up in the list with their last-known CWD/CLI — that's the
  correct behavior, but the table doesn't currently flag them.
  Could add a column. Out of scope.
- Detecting WHICH terminal tab owns each session (via TTY device)
  would close the loop completely but requires
  `/proc/<pid>/fd/0 → readlink → /dev/pts/<n>` plumbing. Out of
  scope.

## Self-review against the requesting-code-review rubric

- **Correctness:** Handler tests cover the 5 brief-mandated cases;
  IPC roundtrips for the new types; CLI renderer tests for the
  pure formatter.
- **No leaks:** Handler doesn't keep references to the snapshot
  slice; no goroutines spawned.
- **Concurrency:** Handler reads via `Stubs.Snapshot()` which
  copies under read lock — safe.
- **Backward compat:** New op-codes; old adapters that don't issue
  `OpListSessions` are unaffected.
- **No PII / secrets:** PID + CWD + CLI name are already exposed by
  `/c3:status` and `/c3:topics`. Same data, different view.
