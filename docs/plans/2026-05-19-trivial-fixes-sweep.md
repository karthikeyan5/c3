# Trivial-fixes sweep — 2026-05-19

Single sweep landing all MINOR + safe NIT items from the code-review report
at `/home/karthi/arogara/code-review/reports/c3-2026-05-19.md` that fit the
"<50 lines + single-file + no design choice" bar. Karthi explicitly directed:
"Fix all of them and come back with the final list that actually needs my
attention."

Process per item:
1. TDD (failing test first) for behavioural fixes; for docs/comments verify
   with existing suite + add a unit test where useful.
2. `superpowers:verification-before-completion` — `go test -count=1 -race
   ./...`, `go vet ./...`, `go build ./...` all green.
3. Self-review against `code-review/guidelines/*.md` rubric.

DO NOT commit.

## In-scope items

### m1 — Deterministic `/c3:ping` stub target

**Report anchor:** report MINOR m1, `internal/broker/handler.go:293-303`.

Today: `for _, s := range b.Stubs.Snapshot()` is map-order nondeterministic
when >1 stub shares a cwd. Fix: pick the most-recently-registered candidate
(max `ConnID` — `StubRegistry` mints monotonic ConnIDs via `atomic.Uint64`).
Log a warning to broker.log when there are multiple matching candidates.

Test: `TestPing_MultipleStubsAtCWD_TargetsMostRecent` — register two stubs,
both at the same cwd, both with claims; assert the higher-ConnID stub wins.

### m2 — `notifyTransport.Disconnect()` method

**Report anchor:** report MINOR m2, both adapter wrappers.

Add a `Disconnect()` (clears stored `mcp.Connection`) on each wrapper:
- `cmd/c3-claude-adapter/notify_transport.go::notifyTransport`
- `cmd/c3-codex-adapter/notify_transport.go::logNotifyTransport`

Document that the adapter doesn't call it today (no SDK reconnect surface)
but it exists for future SDK versions with transparent reconnect.

Test: assert calling Disconnect clears the conn; subsequent Notify returns
the same "connection not yet established" error path (the existing
sentinel — matches the SDK contract — adapter will re-Connect on next
session start).

### m3 — Codex `spawnBroker` stderr alignment

**Report anchor:** report MINOR m3, `cmd/c3-codex-adapter/main.go:199-206`.

Claude adapter sets `cmd.Stderr = nil` with a load-bearing comment; Codex
inherits stderr. Align Codex to match — `cmd.Stderr = nil` + copy the comment.

No test (pre-existing spawnBroker has no test scaffolding; behaviour is
"don't pipe noise into Codex's plugin host").

### m4 — `containsCodexC3Section` sub-table detection

**Report anchor:** report MINOR m4, `cmd/c3-broker/cli_host.go:175-183`.

Today: only `[mcp_servers.c3_codex]` (exact, trimmed) triggers detection;
descendants like `[mcp_servers.c3_codex.tools.attach]` alone don't.

Fix: check for prefix `[mcp_servers.c3_codex` (no trailing `]`), so any
descendant table also signals presence.

Test: update `TestContainsCodexC3Section` to pin the new behaviour — a
user-curated config with ONLY `[mcp_servers.c3_codex.tools.attach]` and no
parent header should now return `true` (so the installer doesn't append a
fresh parent block that would conflict).

### m5 — `docs/INSTALL.md` adds `install-claude-shim`

**Report anchor:** report MINOR m5, `docs/INSTALL.md` (no mention).

Add a "Step 4.5: Install the Claude wrapper" subsection. Note that
`/c3:setup` runs `install-claude-shim` automatically under HostClaude;
the standalone command is for manual / re-install / `--force`.

One paragraph + the bare command. No code test.

### m6 — `installClaudeShim` idempotency short-circuit

**Report anchor:** report MINOR m6, `cmd/c3-broker/install_claude_shim.go:118-123`.

When the existing symlink already points at our launcher (i.e.
`EvalSymlinks(installPath) == EvalSymlinks(launcher)`), short-circuit:
`return nil` without remove+recreate. Closes the brief window where
`installPath` doesn't exist on disk during a concurrent `claude` call.

Test:
`TestInstallClaudeShim_SymlinkAlreadyPointsAtLauncher_DoesNotRemove`
— plant a symlink already pointing at launcher, record its inode, run
install, confirm inode unchanged (no remove+recreate).

### m7 — `plugins/c3/stt/stt-handler.py` docstring

**Report anchor:** report MINOR m7, `plugins/c3/stt/stt-handler.py:4-5`.

Update top-of-file docstring: token via stdin line 1 (not argv); argv
positions are `<chat_id> <reply_msg_id> <file_id> [<message_thread_id>]`.

Match the Go-side phrasing in `internal/plugin/builtins/stt/stt.go:13-17`.

No test (docstring-only).

### n1 — Octal literal style consistency in setup.go

**Report anchor:** report NIT n1, `cmd/c3-broker/setup.go:141,526,565`.

Convert `0700` / `0600` → `0o700` / `0o600`. Audit the whole file for any
other old-style octal literals.

No test (style-only).

### n2 — Version marker on Codex MCP config block

**Report anchor:** report NIT n2, `cmd/c3-broker/cli_host.go::codexC3MCPBlock`.

Header comment becomes
`# c3 v0.1 — written by c3-broker setup on YYYY-MM-DD`. Use
`time.Now().UTC().Format("2006-01-02")` to produce a stable per-day marker
(matches the rest of the file — `ensureCodexMCPRegistration` doesn't pin
content via tests on the exact comment; only on the section header and
command line, both of which we preserve).

Test: `TestCodexC3MCPBlock_HasVersionMarker` — assert the rendered string
contains `c3 v0.1` and a YYYY-MM-DD substring matching today's UTC date.

### n4 — `walkBotGroupChecklist` honors `C3_NO_PROMPT`

**Report anchor:** report NIT n4, `cmd/c3-broker/setup.go:450-462`.

Change `interactive := term.IsTerminal(int(syscall.Stdin))` →
`interactive := term.IsTerminal(int(syscall.Stdin)) && !isNoPromptSet()`.
This way Codex's non-interactive path (which already bypasses the consent
gate via `C3_NO_PROMPT`) also skips the step pauses.

No test (the function does no behavioural work beyond pause-or-not; existing
tests don't pin the pause behaviour; an `isNoPromptSet()` check is already
verified by the consent-gate tests).

### n5 — `internal/mode` package doc lists 3 consumers

**Report anchor:** report NIT n5, `internal/mode/protocol.go:13-15`.

Extend the package comment to enumerate:
1. Claude adapter via `instructions` field at
   `cmd/c3-claude-adapter/main.go`
2. Codex adapter via `instructions` at `cmd/c3-codex-adapter/main.go`
3. `c3-broker setup`'s AGENTS.md installer at
   `cmd/c3-broker/cli_host.go::ensureCodexAgentsMd`

No test (package-doc-only).

## Out of scope (skip per directive)

- **n3** (Unicode bullets in user output) — subjective UX, Karthi decides.
- 5 guideline-file updates — Karthi's rubric files, his voice on each.
- `/c3:sessions` (`#19(e)`) — deferred future feature.
- `#20` broader cross-CLI duplication audit — design calls baked in.

## Sweep beyond named items

After the above land, walk the project diff for similar-flavour items:
stale comments, version markers missing, env-var inconsistency,
gofmt-untouched style, package-doc gaps. Fix anything that hits the
"<50 lines + single-file + no design choice" bar. Report what stayed
unfixed and why so the boundary line is clear.

## Verification gate

1. `go test -count=1 -race ./...` — green
2. `go vet ./...` — clean
3. `go build ./...` — clean

## MORNING-REVIEW update

Append one new section "## Trivial-fixes sweep — 2026-05-19" listing each
fix landed + one-line resolution. No TODO additions (these are code-review
follow-ups, not TODO items).

## Time budget

2-3 hours wall-clock. Stop and report at 3h.
