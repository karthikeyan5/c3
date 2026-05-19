# Plan — Terminal title-bar surface for attached topic (TODO #19a)

**Status:** drafted 2026-05-19. Closes TODO #19(a). Surface decision locked
upstream (see MORNING-REVIEW-2026-05-19.md): ANSI title-bar escape emitted
by the adapter on attach, not a Claude-Code statusline plugin.

## Goal

On a successful attach, each adapter (Claude + Codex) emits an OSC-0
title-bar ANSI escape to stderr so the terminal-emulator title reflects
the currently-attached Telegram destination:

- Topic attach: `c3: <name> · <group>`
- DM attach: `c3: dm`
- Detach: empty title (`\033]0;\007`)

Cross-CLI parity. Same code path for both adapters. Same escape sequence.
Stderr-only so MCP stdout framing stays clean. Honors
`C3_NO_TERMINAL_TITLE` and `isatty(2)` so non-tty / piped /
log-captured stderr does not see escape garbage.

## Why ANSI escape (not Claude Code statusline plugin)

Recorded in MORNING-REVIEW-2026-05-19.md under "NOTICE — #19(a)
statusline". Short version:

1. Works in Claude Code AND Codex (Karthi's "every flow must work the
   same in Codex" principle). Statusline plugin is Claude-only.
2. No `settings.json` edits required — universal OSC-0 escape that
   xterm / gnome-terminal / alacritty / kitty / tmux / screen / iTerm2
   / terminator all honor.
3. Terminal-emulator-level escape is the canonical idiom for this UX
   (vim, tmux, ssh agents all do it).

## Constraints (verbatim from the task brief)

1. `C3_NO_TERMINAL_TITLE=1` (any truthy value) suppresses emit.
   Default = enabled.
2. Only emit if `isatty(2)` returns true for `os.Stderr`.
3. No emit on attach-failure paths (status != ok / NeedsConfirmation
   / Err). Only on the OK/proposal-accepted branches.
4. No emit during MCP initialize / handshake. Only when an actual
   attach succeeds (i.e. just before `formatAttached` is rendered for
   an `AttachedMsg` with `OK=true` / equivalent).
5. Codex parity. Both adapters must do this. Identical escape; trigger
   point differs slightly because the response handlers differ.

## Where the emit goes (per-adapter trigger-point analysis)

### Claude adapter — `cmd/c3-claude-adapter/main.go`

`toolAttach` (line ~834). The current OK branch is:

```go
case res := <-ch:
    attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
    if attached.OK {
        a.rememberAttach(attachReq)
    }
    return toolTextResult(formatAttached(&attached)), nil
```

Title-emit goes after `rememberAttach` and BEFORE `formatAttached`.
NOT inside `formatAttached` — the formatter's return value is the
agent-facing text, the title-bar is a side-effect-only sister surface.

```go
case res := <-ch:
    attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
    if attached.OK {
        a.rememberAttach(attachReq)
        termtitle.EmitAttach(&attached)
    }
    return toolTextResult(formatAttached(&attached)), nil
```

`toolDetach` (line ~902). Clear the title on the success branch
(after the WriteJSON returns nil):

```go
a.amu.Lock()
a.lastAttach = nil
a.amu.Unlock()
termtitle.Clear()
return toolTextResult("detached"), nil
```

### Codex adapter — `cmd/c3-codex-adapter/main.go`

`toolAttach` (line ~657). Mirror trigger-point in the OK branch
(line ~717):

```go
case res := <-ch:
    attached, _ := res.Result["_attached"].(ipc.AttachedMsg)
    if attached.OK {
        termtitle.EmitAttach(&attached)
    }
    return toolTextResult(formatAttached(&attached)), nil
```

Codex doesn't have an explicit detach tool; the title clears
implicitly on process exit (terminal emulator restores). The
auto-attach goroutine `autoAttach` also feeds back through the same
`dispatchAttached` → `pending["attached"]` channel that `toolAttach`
reads, so a successful auto-attach would emit a title via the
goroutine's discarded channel — that case is fine to leave alone for
phase 1 (auto-attach happens only if `C3_ATTACH_NAME` was set by the
launcher, in which case the agent will follow up with an explicit
attach soon enough). NOTE: explicit emit inside `autoAttach`'s
result-receive branch is a 2-line extension if Karthi wants it.

## New package — `internal/termtitle`

Single source of truth for the escape. Tested in isolation; adapter
trigger points become 1-line calls.

### API

```go
// Package termtitle emits OSC-0 terminal title-bar escapes for the
// currently-attached C3 topic.

// EmitAttach writes the title-bar escape derived from msg to os.Stderr,
// honoring the global gates (C3_NO_TERMINAL_TITLE off, stderr isatty).
// No-op on any failure path (msg.OK==false or msg==nil).
func EmitAttach(msg *ipc.AttachedMsg)

// Clear writes the empty-title escape to os.Stderr. Used on detach.
func Clear()

// EmitTo is the testable variant: explicit writer + tty hint + noEnv hint,
// so unit tests can capture the bytes without depending on os.Stderr or
// the real environment.
func EmitTo(w io.Writer, isTTY bool, suppressed bool, msg *ipc.AttachedMsg)

// ClearTo is the testable Clear variant.
func ClearTo(w io.Writer, isTTY bool, suppressed bool)

// FormatTitle is the pure title-string builder for an AttachedMsg.
// Exposed so tests can assert the exact "c3: foo · bar" / "c3: dm" form
// without escape framing.
func FormatTitle(msg *ipc.AttachedMsg) string
```

### Title formation rules

| Case                                    | Title         |
|-----------------------------------------|---------------|
| DM (TopicID nil OR Name=="dm")          | `c3: dm`      |
| Topic with Group                        | `c3: name · group` |
| Topic without Group (broker quirk)      | `c3: name`    |
| Anything else (defensive — Name empty)  | `c3`          |

Bullet separator: U+00B7 MIDDLE DOT (`·`). Matches the brief's
example verbatim.

### Suppression gate

```go
func suppressed() bool {
    v := strings.ToLower(strings.TrimSpace(os.Getenv("C3_NO_TERMINAL_TITLE")))
    switch v {
    case "", "0", "false", "no", "off":
        return false
    }
    return true
}
```

Any truthy value (1, true, yes, on) suppresses. Empty / 0 / false /
no / off allows. Matches the `C3_NO_PROMPT` precedent in
`cmd/c3-broker/preamble.go`.

### TTY check

`golang.org/x/term` is already a direct dep (`go.mod` line 9). Use
`term.IsTerminal(int(os.Stderr.Fd()))`. No new dependency required.

## Tests — `internal/termtitle/termtitle_test.go`

Pure-Go unit tests using `EmitTo` / `ClearTo` / `FormatTitle` so we can
capture output via a `bytes.Buffer` without touching os.Stderr.

1. `TestFormatTitle_Topic` — `Name=foo, Group=bar, TopicID=&123` →
   `c3: foo · bar`.
2. `TestFormatTitle_TopicNoGroup` — `Name=foo, TopicID=&123` →
   `c3: foo` (defensive — broker normally fills Group).
3. `TestFormatTitle_DMByNilTopicID` — `Name=dm, TopicID=nil` →
   `c3: dm`.
4. `TestFormatTitle_DMByName` — `Name=dm, TopicID=&5` → `c3: dm`
   (Name="dm" treated as DM even if topic id present — defensive
   against broker's disambiguate_dm flow returning a topic with that
   name).
5. `TestFormatTitle_EmptyName` — `Name="", TopicID=nil` → `c3`.
6. `TestEmitTo_EmitsEscape_WhenTTY` — `isTTY=true, suppressed=false,
   msg.OK=true, Name=foo, Group=bar` → buffer contains
   `\x1b]0;c3: foo · bar\x07`.
7. `TestEmitTo_DM_EmitsExpected` — same with DM → `\x1b]0;c3: dm\x07`.
8. `TestEmitTo_Suppressed_NoEmit` — `isTTY=true, suppressed=true` →
   buffer empty.
9. `TestEmitTo_NonTTY_NoEmit` — `isTTY=false, suppressed=false` →
   buffer empty.
10. `TestEmitTo_FailedAttach_NoEmit` — `OK=false` (e.g.
    `Status=no_topics_configured`) → buffer empty.
11. `TestEmitTo_NeedsConfirmation_NoEmit` —
    `OK=false, NeedsConfirmation=true` → buffer empty.
12. `TestEmitTo_PolicyRejected_NoEmit` —
    `OK=false, Status=policy_rejected` → buffer empty.
13. `TestEmitTo_NilMsg_NoEmit` — nil pointer → buffer empty (no panic).
14. `TestClearTo_EmitsEmptyTitle_WhenTTY` — buffer contains
    `\x1b]0;\x07`.
15. `TestClearTo_Suppressed_NoEmit` / `TestClearTo_NonTTY_NoEmit` —
    same gating as Emit.
16. `TestSuppressedEnv_TruthyValues` — table-driven: `1, true, yes, on,
    True, YES` all suppress; empty, `0, false, no, off` all allow.

## Adapter-level tests

Trigger-point parity tests in each adapter, focused on the call-site
correctness (does the OK branch invoke EmitAttach? does the failure
branch NOT?). Same skeleton both sides.

`cmd/c3-claude-adapter/title_test.go` (new):

1. `TestToolAttach_EmitsTitleOnOK` — wire `toolAttach` through a
   stub broker that returns `AttachedMsg{OK=true, Name=foo,
   Group=bar, TopicID=&n}`; capture the emit via a swapped
   `termtitle.WriterFn` package var; assert one title write of
   `c3: foo · bar`.
2. `TestToolAttach_NoEmitOnNoTopicsConfigured` — broker returns
   `AttachedMsg{OK=false, Status=no_topics_configured}`; no title
   write.
3. `TestToolAttach_NoEmitOnPolicyRejected` — same with
   `Status=policy_rejected`.
4. `TestToolAttach_NoEmitOnProposal` — broker returns
   `AttachedMsg{OK=false, NeedsConfirmation=true, Proposal=...}`;
   no title write.
5. `TestToolDetach_EmitsClearTitle` — toolDetach success →
   `\x1b]0;\x07` written.

`cmd/c3-codex-adapter/title_test.go` (new): same as #1–#4 above
(no detach tool in Codex adapter).

To make the adapter tests work without involving the real
`os.Stderr`, expose a package-level `WriterFn func() io.Writer` and
`TTYFn func() bool` in `internal/termtitle`. Default values point to
`os.Stderr` and the real `term.IsTerminal`. Tests can swap them.

## Process discipline (per brief)

1. `superpowers:writing-plans` — this doc.
2. Self-review the plan (rubric below) before coding.
3. `superpowers:test-driven-development` — failing tests first,
   then code.
4. `superpowers:verification-before-completion` — `go test -race
   -count=1 ./...`, `go vet ./...`, `go build ./...` all clean.
5. Self-review diff before declaring done.

## Self-review rubric for the plan

- [ ] Does the plan address every constraint in the brief? (env,
      isatty, no emit on failure, no emit on MCP init, codex parity)
- [ ] Are the trigger points correct for each adapter? (verified
      against the current code shape)
- [ ] Are the tests at the right level? (unit on the package, focused
      integration on the adapter call-site)
- [ ] Does the package API stay small / single-purpose?
- [ ] No new external dependency? (using existing `golang.org/x/term`)

## Open follow-ups (not blocking #19a closure)

- Codex `autoAttach` could also emit a title — phase 2 if the
  launcher path becomes the dominant flow.
- TODO #19 parent stays open; only (e) is the remaining sub-item per
  brief.
