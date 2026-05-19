# 2026-05-19 — Protocol announce-mode + /c3:ping + multi-session verifications

Plan covering three coordinated TODO items overnight (Karthi authorized
2026-05-18). See `TODO.md` items #15, #19(b), #19(d).

## Task 1 — #15 announce-mode-on-attach

**Goal.** Add one bullet to `internal/mode/protocol.go::ModeProtocol`
telling the agent to announce its current output mode immediately after
`attach` completes ("currently in CLI mode" / "currently in Telegram
mode"). The const propagates automatically to:

- Claude adapter MCP-initialize `instructions` field (via
  `internal/mode.Combined()`).
- Codex's `~/.codex/AGENTS.md` block (via `ensureCodexAgentsMd` next
  time setup runs under HostCodex; existing files are unchanged until
  setup re-runs, which is fine).

**Design call.** Broker does NOT persist mode (already established).
Agent owns the state. New rule: agent ALWAYS announces post-attach so
the human reading whichever surface gets immediate confirmation. No
new IPC, no persistence; just contract text.

**Files.**
- `internal/mode/protocol.go` — append bullet at end of ModeProtocol.
- `internal/mode/protocol_test.go` — extend
  `TestModeProtocol_HasCanonicalKeyPhrases` to assert the new phrase
  (e.g. `"currently in"` / `"after attach"`).
- `TODO.md` — flip #15 to `[x]` with the resolution note.

## Task 2 — #19(b) /c3:ping slash command

**Goal.** A slash command the user types in their CLI session that
causes the broker to send a one-shot "this is me" Telegram reply
identifying the calling session — `cwd / topic / PID / timestamp`. Lets
the human reading Telegram see which terminal owns the topic.

**Wire shape.** New IPC op:

```
OpPingThisSession Op = "ping_this_session"        // adapter → broker
OpPingThisSessionReply Op = "ping_this_session_reply"  // broker → adapter
```

```go
type PingThisSessionReq struct {
    Op Op `json:"op"`
}
type PingThisSessionReplyMsg struct {
    Op       Op     `json:"op"`
    OK       bool   `json:"ok"`
    Channel  string `json:"channel,omitempty"`
    Topic    string `json:"topic,omitempty"`   // human label
    SentText string `json:"sent_text,omitempty"`
    Err      string `json:"err,omitempty"`
}
```

**Broker handler.** `handlePingThisSession` in
`internal/broker/handler.go`:

1. Look up `stub.CurrentRoute()`. If nil → reply OK=false, Err="not
   attached — use /c3:attach first".
2. Compose text (similar tone to `welcomeText`):

   ```
   📍 c3-ping
   📁 <cwd>           (home-shortened like welcome)
   🤖 <cli>           (e.g. claude / codex)
   PID <pid>
   <timestamp ISO8601>
   ```

3. Send via channel's `SendReply` (synchronous; ping is rare and the
   reply needs to confirm delivery to the user). On error → return
   error in reply.
4. Wrap topic label via `Mappings().LookupTopicByID` for the topic
   name, else `dm` if `!HasTopic`, else `topic-<id>` fallback.

**New CLI subcommand.** `cmd/c3-broker/ping.go` — mirrors `pair.go`:

```go
func runPing(args []string) error
```

Opens IPC via `dialBroker()`, sends `PingThisSessionReq`, prints
acknowledgement. Wire up in `cmd/c3-broker/main.go` and `usage`.

**Important.** The `dialBroker()` helper sends `CLI: "c3-broker-cli"`
as its hello, which means its stub has NO current route. We need the
ping subcommand to identify the USER'S calling session, not the
transient CLI client. Approach: pass `pid` of the calling session
through CLI args? Simpler: the `/c3:ping` slash command runs INSIDE
the calling Claude/Codex session, so its parent PID chain reaches the
adapter. But `c3-broker ping` is a fresh `exec` — by the time it runs,
it knows only its own pid and getppid().

Decision: pass `--cwd` and `--pid` flags (or just `--cwd` and let the
broker look up the cwd-mapped stub). Simpler: the broker looks up the
**route-claimed by the cwd that matches this client's cwd**. But the
client's `os.Getwd()` is the slash command's expansion dir = the
caller's cwd, which matches the actual user session. Since the broker
already tracks `(CLI, PID, CWD)` per stub, we send `cwd` in the hello
(already does this — `dialBroker` writes `os.Getwd()`).

But we need the broker to look up the OTHER stub (the adapter) that
holds the route, not this transient client. So the wire op needs to
carry an identifier of "find my session's stub" — using the cwd. The
broker scans stubs for one whose `CWD` matches and whose route is
non-nil. If multiple match → first one (rare; one CC session per cwd
is normal). If none → "not attached".

Refined `PingThisSessionReq`:

```go
type PingThisSessionReq struct {
    Op  Op     `json:"op"`
    CWD string `json:"cwd"`  // identify the calling user session
}
```

Broker scans `Stubs.Snapshot()` for the stub whose `CWD == req.CWD`
AND `stub.CurrentRoute() != nil`. Sends a ping via that route.

**Slash command.** `plugins/c3/commands/c3-ping.md` — mirrors
`c3-pair.md`:

```
---
description: Send a one-shot "this is me" message to Telegram identifying which CLI session currently owns the attached topic.
---

!c3-broker ping

Display the broker's output verbatim. The broker sent a short
identification message to the currently-attached Telegram topic so
the human reading Telegram can see which terminal owns it.

If the output says `not attached`, run `/c3:attach <topic>` first.
```

**Tests.**
- `internal/broker/handler_test.go`:
  `TestHandle_PingThisSession_NotAttached` (no route → OK=false).
- `internal/broker/attach_test.go`:
  `TestHandle_PingThisSession_SendsReply` (attach via fake channel,
  ping, assert one extra SendReply with expected ChatID/TopicID and
  text containing "c3-ping" + cwd + cli).

## Task 3 — #19(d) verifications + papercut doc

**Verify 1: conn-drop releases when PID dead.**

`internal/broker/handler.go:67-77` defer releases claims when
`isPIDAlive(stub.PID)` is false. Existing tests don't cover this
path. Add:

- `internal/broker/handler_test.go::TestConnDrop_ReleasesClaimWhenPIDDead`
  — drive a conn, attach to a fake route, close the conn with a stub
  whose `PID = -1` (sentinel → `isPIDAlive` returns false), assert
  `Routes.Snapshot()` is empty after the handler returns.

Trick: the real `HandleConn` reads `hello.PID` from the wire. So
send `PID: -1` in the hello, attach, then close. The defer runs with
`stub.PID = -1`, `isPIDAlive(-1)` returns false (`pid <= 0` short
circuit), claim is released.

**Verify 2: --resume re-attach via replayLastAttach.**

Existing test `TestDispatch_SetsDispatchedFlag` only proves the
dispatch middleware fires; doesn't exercise replayLastAttach. Add:

- `cmd/c3-claude-adapter/lifecycle_test.go::TestReplayLastAttach_ResendsLastAttachWithReplayFlag`
  — set `a.lastAttach = &ipc.AttachReq{...}`, swap in a stub `*ipc.Conn`
  (net.Pipe peer), invoke `a.replayLastAttach()`, read the frame from
  the peer side, assert `op == OpAttach` and `Replay == true` and the
  target fields match.

Needs `currentConn()` to return a real `*ipc.Conn`. Look at how
`reconnectBroker()` constructs `a.conn` to see what to stub. If the
adapter struct allows direct field set under test, set
`a.conn = ipc.NewConn(pipeEnd)` directly.

**Papercut doc.** New section in `DEBUGGING.md` (top-level, NOT in
`docs/`):

```
## Multi-session: alive-but-abandoned tabs

Case: a `claude --resume <id>` session left open in another terminal
still holds the route claim. New sessions can't attach to the same
topic without confirmation.

Why no auto-detect: the broker doesn't periodically ping each adapter
to ask "is a human still driving you?" — intentional rejection of
extra plumbing for an edge case. The PID is alive (CC kept it that
way for --resume), so the broker won't volunteer to release.

Workaround:
- In the new session run `/c3:attach <topic>` and accept the
  `force_steal` proposal — evicts the abandoned holder.
- Alternatively, kill the abandoned `claude` PID from another shell.
- To identify which terminal currently owns the topic before
  evicting, run `/c3:ping` in each candidate session — the one whose
  identification message lands in Telegram is the owner.
```

Cross-link from `/c3:ping`.

**TODO.md.** #19(b) → `[x]` with implementation note. #19(d) → `[x]`
with verification note + papercut doc reference. Parent #19 stays
open (sub-item (a) statusline is deferred; (e) is future).

## Process

For each task (combined plan, sequential execution):

1. Failing test first (TDD).
2. Implement.
3. `go test -count=1 -race ./...`, `go vet ./...`, `go build ./...`.
4. Self-review against `superpowers:requesting-code-review` rubric.

No commits.

## Out of scope

- shim install code (parallel agent Q).
- `cmd/c3-broker/setup.go` (parallel agent T).
- `internal/broker/attach.go` policy-state logic (parallel agent S).
- Periodic-ping infra (explicitly rejected by Karthi).
