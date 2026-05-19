# Onboarding Preamble + Parallel Install — Implementation Plan

**Date:** 2026-05-19  
**Author:** Claude Opus (overnight 2026-05-18/19)  
**Status:** DRAFT — Karthi reviews the educational copy before ship.  
**TODO refs:** `TODO.md` items #4 (onboarding preamble) and #5 (front-load
bot token + parallel install). Karthi explicitly coupled them: the
preamble is delivered DURING the parallel install window.

---

## Goal

Make first-run `c3-broker setup` understandable to someone who's heard
"C3 sends Telegram messages to Claude Code, especially voice via STT"
and nothing else.

Two coupled changes:

1. Show an educational preamble BEFORE asking for anything, then a
   "can I install C3 for you?" gate.
2. Ask for the bot token immediately after consent (only true
   prerequisite), kick off `go install ./cmd/...` in a goroutine, then
   walk the user through bot+group setup while the build runs in the
   background. Join the background install before exiting.

---

## Non-goals

- No new copy in `docs/INSTALL.md` (that's already the authoritative
  manual checklist; preamble is a lighter inline summary).
- No prompt for shim install / Codex registration — those stay
  compulsory / host-detected as today.
- No new slash command. This is still `c3-broker setup`.
- No change to STT / Codex / Claude-shim sub-flows. They're invoked
  unchanged from the new orchestrator.
- No change to `attach.go`, `modeProtocol`, `ping` (parallel agent
  workstreams Q/R/S — out of scope per Karthi).

---

## The new flow (numbered)

```
0. Existing-config check                            (unchanged)
1. PREAMBLE — what C3 is, what happens next        (NEW)
2. Consent gate: "Install C3 for you?" [Y/n]       (NEW)
   - skippable via C3_NO_PROMPT=1 (defaults to yes)
   - N → exit 0 with pointer to docs/INSTALL.md
3. Bot token ask + Telegram getMe validation        (moved earlier)
4. Kick off `go install ./cmd/...` in goroutine     (NEW)
5. Walk user through 6-step bot+group checklist     (NEW, inline)
   - One step at a time with "press enter when done"
6. Ask DM chat id + group name + group chat id     (was: 3 prompts; moved later)
7. Write mappings.json                              (unchanged behaviour)
8. JOIN background `go install`                     (NEW)
   - On success: print "✓ binaries built"
   - On failure: print stderr, return error
9. promptSTTSetup                                   (unchanged)
10. ensureCodexMCPRegistration + ensureCodexAgentsMd (Codex host only,
    unchanged)
11. maybeInstallClaudeShim (Claude host only, unchanged)
12. sttHint if needed                               (unchanged)
13. Restart instruction                             (unchanged)
```

---

## Concurrency model

### Primitives

- One goroutine, started right after step 3 (bot token validated).
- Communication via a single `chan installResult` (buffered 1) so the
  goroutine never blocks even if the join is delayed.
- `installResult` struct: `{ err error; combinedOutput []byte;
  duration time.Duration }`.

### Goroutine lifecycle

```go
type installResult struct {
    err      error
    output   []byte         // stderr+stdout captured for failure reporting
    duration time.Duration
}

func startBackgroundInstall(ctx context.Context) <-chan installResult {
    ch := make(chan installResult, 1)
    go func() {
        start := time.Now()
        cmd := exec.CommandContext(ctx, "go", "install", "./cmd/...")
        cmd.Env = append(os.Environ(), ...) // inherit GOBIN etc.
        // cwd: the source dir. Resolved via runtime/debug.ReadBuildInfo
        // or os.Executable + walk to nearest go.mod (see "Source dir
        // discovery" below).
        out, err := cmd.CombinedOutput()
        ch <- installResult{err: err, output: out, duration: time.Since(start)}
    }()
    return ch
}
```

### Join semantics

- The orchestrator holds a `context.Context` with a cancellation hook
  on os.Interrupt / SIGTERM.
- On step 8: simple `result := <-ch`. The 6-step bot+group walk
  typically takes 2-5 min — `go install ./cmd/...` of the C3 tree
  is ~30s cold, ~5s warm. So the join is almost always instant.
- If the user is fast and the build is still running, the join blocks
  with a single progress line: `Waiting for build to finish...` and
  then prints duration on completion.
- On error: surface `result.output` (truncated to last 4 KiB) on
  stderr, return the error so main.go exits with exitConfig.

### Source-dir discovery

`go install ./cmd/...` only works from the source tree. Resolution
order:

1. `$C3_SRC_DIR` env var override (testing / unusual installs).
2. Walk up from `os.Executable()` until a `go.mod` whose `module`
   line matches `github.com/karthikeyan5/c3`. This catches the common
   case where the user ran `go install` once and `c3-broker` lives at
   `~/go/bin/c3-broker` — we can't walk up from there to source, so
   step 3 takes over.
3. `~/src/c3` (the canonical clone path from `docs/INSTALL.md` step 1).
4. Walk `runtime/debug.ReadBuildInfo()`'s `vcs.modified` / module path.
5. If none resolve: log a warning, SKIP background install, tell the
   user to run `/c3:build` or `go install ./cmd/...` manually after
   setup. (Setup itself doesn't need a rebuild to function — the
   already-running `c3-broker` binary is what's prompting.)

This last fallback is critical: skipping the parallel install gracefully
when source isn't reachable is better than aborting setup.

### Cancellation

- SIGINT / SIGTERM during step 5 (interactive walk) → cancel the
  install context, exit with usage error. Don't leave a dangling
  `go install` after the user Ctrl-Cs setup.

---

## C3_NO_PROMPT non-interactive path

Codex sometimes drives `c3-broker setup` programmatically (no real
TTY). The new consent gate would deadlock that flow if we read stdin
unconditionally.

Behaviour:

- `C3_NO_PROMPT=1` (or any truthy value: `1`, `true`, `yes`) → skip
  the consent prompt, proceed as if the user said yes. Still print
  the preamble so the agent driving setup sees what's happening in its
  output stream.
- `C3_NO_PROMPT` unset and stdin is not a TTY → degrade gracefully:
  print the preamble, log a one-liner ("non-interactive stdin, skipping
  consent prompt"), proceed. This matches the existing behaviour of
  `promptSTTSetup` (which silently degrades on piped stdin).

The bot-token prompt and chat-id prompts still require stdin; if
stdin is piped, they read from the pipe as today. Non-interactive
callers (Codex agent) feed those values through the pipe.

---

## Files changed

- `cmd/c3-broker/setup.go` — major refactor of `runSetup`. Split
  body into phase helpers; new `runSetup` becomes an orchestrator.
- `cmd/c3-broker/preamble.go` (NEW) — copy constants + small helpers
  (`printPreamble`, `confirmInstall`). Isolating the copy in its own
  file makes it easy for Karthi to grep / diff in the morning.
- `cmd/c3-broker/preamble_test.go` (NEW) — table tests for copy
  invariants (must contain certain substrings — keeps the DRAFT
  honest), C3_NO_PROMPT behaviour, "n" answer behaviour.
- `cmd/c3-broker/setup_flow_test.go` (NEW) — orchestration test:
  the install goroutine fires after the token ask, gets joined before
  STT setup, errors propagate. Uses package-level `installRunFn` var
  for fake injection (same pattern as `installClaudeShimFn`).
- `cmd/c3-broker/setup.go` — add `installRunFn` package var and
  `installResult` struct. Replace inline `go install` call with the
  indirection.

No new external deps. Standard library only.

---

## Test plan (TDD — RED first)

### preamble_test.go

1. `TestPreambleCopy_ContainsKeyConcepts` — preamble mentions: bot,
   group, topics, voice/STT, Telegram. Catches regression if Karthi's
   edit accidentally drops a concept.
2. `TestPreambleCopy_HasDraftMarker` — copy contains `DRAFT` substring
   (forcing function: removed only when Karthi approves).
3. `TestConfirmInstall_YesProceeds` — fake stdin "y\n" → returns true.
4. `TestConfirmInstall_NoAborts` — fake stdin "n\n" → returns false.
5. `TestConfirmInstall_EmptyDefaultsToYes` — fake stdin "\n" → returns
   true. (Karthi: most fresh installers will just hit enter.)
6. `TestConfirmInstall_NoPromptEnvBypasses` — `C3_NO_PROMPT=1` →
   returns true without reading stdin.

### setup_flow_test.go

7. `TestRunSetup_StartsInstallAfterToken` — using fake `installRunFn`
   that records when it was called relative to a sentinel "saw token
   prompt" signal. Asserts install starts AFTER token validation,
   BEFORE chat-id prompts. (Critical: this is the whole point of the
   refactor.)
8. `TestRunSetup_JoinsInstallBeforeSTT` — install goroutine
   sets a flag; STT prompt runs only after flag set.
9. `TestRunSetup_InstallErrorSurfacedNonFatal` — fake `installRunFn`
   returns error; runSetup returns the error AFTER writing
   mappings.json (so a build failure doesn't erase config the user
   already gave us).
10. `TestRunSetup_SourceDirMissingSkipsInstall` — set
    `C3_SRC_DIR=/nonexistent`, install is skipped with a warning,
    setup completes successfully.

### Concurrency / race

11. Run all of the above with `-race`. The fake `installRunFn`
    captures concurrent writes to a shared "events" slice via a
    mutex; the test asserts the event sequence is correct. This
    exercises the goroutine + channel join.

---

## Backwards compatibility

- `C3_NO_PROMPT` is new but defaults to off, so existing interactive
  invocations are unchanged.
- The token / DM / group prompts are in the same order on stdin
  (token → DM → group-name → group-chat-id), so a piped-stdin caller
  (Codex) doesn't need to change its input stream.
- Existing tests in `setup_claude_shim_test.go` continue to pass —
  `maybeInstallClaudeShim` signature is unchanged.

---

## Educational copy — DRAFT 2026-05-19

> **DRAFT** — Karthi reviews this in the morning before ship. Marked
> with `// DRAFT 2026-05-19 — pending Karthi approval` in the source
> so the const is greppable.

```
C3 — what it is

  C3 lets you talk to your Claude Code (or Codex) CLI sessions from
  Telegram. Text and voice work: voice messages are transcribed by a
  custom STT pipeline (Gemini 3 Flash → Sarvam fallback) and surfaced
  to the CLI as text. Replies from the agent come back into Telegram.

What we're about to set up

  1. A Telegram bot — your phone-side endpoint. Made via @BotFather.
     (If you don't have one yet, I'll walk you through it.)
  2. A Telegram group with Topics enabled — one topic per CLI
     session, so multiple projects don't collide in one chat.
  3. ~/.config/c3/mappings.json — a 600-mode config file with your
     bot token and chat ids. Stays on this machine.
  4. C3 binaries — built from source via `go install ./cmd/...`.
     I'll kick this off in the background while you wire up Telegram.

What you'll need handy

  - A Telegram bot token (the 1234567:abc... string from @BotFather).
    If you don't have one yet, that's fine — I'll point you at
    @BotFather first.
  - Your own Telegram user id (for DMs).
  - A supergroup with Topics on, and its chat id.

  All three can be set up in the next 5 minutes if you're new to this.
```

Then the consent prompt:

```
Install C3 for you now? [Y/n]:
```

If `n`: print a short pointer:

```
No problem. Manual install instructions: docs/INSTALL.md (in this repo).
```

---

## Self-review rubric

After implementing, score against:

- [ ] Copy is tight — no paragraphs longer than 3 lines.
- [ ] Copy covers: what C3 does, what happens next, what user needs.
- [ ] DRAFT marker present in source.
- [ ] Background install starts after token validation, joins before
      STT. Test asserts ordering.
- [ ] C3_NO_PROMPT=1 path works (no stdin read for consent).
- [ ] Source-dir discovery has a graceful skip path; setup completes
      even if `go install` can't find source.
- [ ] Existing `setup_claude_shim_test.go` still passes unchanged.
- [ ] `go test -count=1 -race ./...` clean.
- [ ] No new external deps.

---

## Risks

1. **Source-dir discovery is fragile.** Fallback to skip-with-warning
   makes this safe — we don't break setup if we can't find source.
2. **Goroutine leak on Ctrl-C.** Mitigated via `exec.CommandContext`
   and signal handler that cancels the context.
3. **Karthi rejects the copy.** Designed for that — the const lives in
   its own file with a DRAFT marker and dedicated test for substring
   invariants, so an edit is a small targeted diff.
4. **C3_NO_PROMPT collision with existing env.** Verified no current
   usage of this env var in the codebase (grep clean).
