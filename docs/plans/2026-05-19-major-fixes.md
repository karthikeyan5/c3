# 2026-05-19 — fix 3 MAJOR findings from overnight code review

Source: `/home/karthi/arogara/code-review/reports/c3-2026-05-19.md`

Karthi's goal: "fix all BLOCKER/MAJOR findings before morning." 0 BLOCKER,
3 MAJOR (M1, M2, M3). All three are code-review follow-ups — they close
cleanly without opening new TODO items.

Process discipline:
- Failing test FIRST, then code (`superpowers:test-driven-development`).
- `go test -count=1 -race ./...`, `go vet ./...`, `go build ./...` all green
  before claiming done (`superpowers:verification-before-completion`).
- Self-review against `code-review/guidelines/{general,go,daemon,cli}.md`.
- DO NOT commit. Karthi reviews diff in the morning.
- DO NOT touch unrelated TODO items or other parallel agents' tests.
- Append a single line to MORNING-REVIEW-2026-05-19.md under the existing
  "Code-review pass — 2026-05-19" section once all three land.

## Fix M1 — `internal/shimconfig` schema_version

**File**: `internal/shimconfig/shimconfig.go` (struct around line 39).

**Why**: sibling `~/.config/c3/mappings.json` has `schema_version: 1`
(see `internal/mappings/types.go` line 9). The shim config has no
versioning, so the day Anthropic CC's launcher contract changes there
is no in-band signal that the broker reading the file disagrees with
the schema. Add it now, before the install base is large enough to be
hard to migrate.

**Design**:
- `File` struct gains `SchemaVersion int \`json:"schema_version"\``.
- `currentSchemaVersion = 1` package-level const (so a future bump is
  one place to edit).
- `Save(path, realClaude string)` always writes `SchemaVersion: 1`
  (the caller signature doesn't change — schema management is internal
  to the package).
- `Load(path string) (string, bool)` behaviour:
  - File missing / corrupt JSON / empty `real_claude` → `("", false)`
    (no change from today).
  - `schema_version` missing or `== 0` → treat as legacy v0; upgrade
    in-memory to v1; log via `fmt.Fprintf(os.Stderr, ...)` ONCE per
    process (a `sync.Once`). Since the only field today is
    `real_claude`, the in-memory shape after upgrade is identical;
    the log is the audit trail.
  - `schema_version == 1` → use directly.
  - `schema_version > 1` → return `("", false)` AND surface an error
    so the broker installer can report the mismatch. **Need to extend
    the contract**: add `LoadStrict(path) (string, error)` that
    returns the error for installer-side callers; keep `Load(path)
    (string, bool)` for the shim runtime (whose contract is "silent
    fallback to PATH walk; never hard-fail at runtime").
  - The error message: "shimconfig schema version %d unsupported;
    downgrade c3 or remove ~/.config/c3/claude-shim.json".

**Tests (add to `internal/shimconfig/shimconfig_test.go`):**
1. `TestLoad_LegacyV0_UpgradesAndLogsOnce` — write a JSON file without
   `schema_version`; first `Load` returns ok with the path; capture
   stderr; verify upgrade log appears. Second `Load` from the same
   process does NOT re-log (sync.Once).
2. `TestSaveLoad_V1RoundTrip_ByteEqual` — `Save` writes a known
   path; raw file on disk has `"schema_version": 1` AND
   `"real_claude": "/x"`. Re-read and assert.
3. `TestLoad_V999_OkFalse` — write `{"schema_version": 999, "real_claude": "/x"}`;
   `Load` returns `("", false)`. `LoadStrict` returns an error matching
   "unsupported".
4. `TestSave_WritesSchemaVersion1` — call `Save`; read the raw file
   bytes; assert it contains `"schema_version": 1`. Defends against
   accidental omission on future struct changes.
5. `TestLoad_LegacyV0_LogOnlyFiresOnce_AcrossLoads` — multiple Load
   calls in one process; stderr only has one "upgraded legacy" line.

**Non-goals**:
- Migration that mutates the file on disk. The contract is "silent
  in-memory upgrade"; rewriting the file would be a surprise to a
  shim that's never had write permission on the config.
- Versioning beyond v1. We're not adding `v2`; we're protecting the
  ability to ship one if the launcher contract changes.

**Risk**:
- The `sync.Once` lives at package level. Tests that need to re-observe
  the upgrade log have to coordinate; the test plan above only checks
  the log fires AT LEAST once across multiple legacy-load calls in one
  test process (sufficient). Multiple tests both wanting "log fires"
  would observe it on whichever runs first; document this in the test.
- Changing `Load` semantics for v999: today it returns `("", false)`
  (since `RealClaude` would be parsed, but we want to refuse). Need
  to ensure the existing test `TestSaveLoad_RoundTrip` still passes
  (it doesn't write a version field — falls into "legacy v0" path,
  which DOES return ok). Defensive: that test wrote with the old
  `Save`, but now `Save` writes v1. The round-trip still passes
  because the file `Save` writes has `schema_version: 1`. Good.

## Fix M2 — Compulsory shim install failure UX

**File**: `cmd/c3-broker/setup.go` lines 215-218.

**Why**: under Claude Code, the agent transcript shows stdout but
intermittent stderr. The current `fmt.Fprintf(os.Stderr, "warning: ...")`
emits a warning the user never sees. Most common failure: an NVM-managed
`~/.local/bin/claude` already at the target → install refuses non-fatally
→ the user's dev-channels never wire up → C3 silently broken.

**Design**:
- Replace the two `fmt.Fprintf(os.Stderr, ...)` calls with a helper
  `printShimInstallFailure(err error)` that writes the same structured
  block to BOTH stdout and stderr.
- Block format (literal, no Unicode glyphs in the header):
  ```
  [claude shim NOT installed]
    error: <err>

    The shim is required for c3 channels to surface in Claude Code.
    Most common cause: an existing non-shim `~/.local/bin/claude`
    (e.g. one installed by NVM / npm).

    To overwrite the existing file:
      c3-broker install-claude-shim --force

    --force overwrites a non-shim file at ~/.local/bin/claude. If you
    have never run `c3-broker install-claude-shim` before, the original
    `claude` symlink target will be lost. Verify a successful one-time
    install without --force first if you want the original real-claude
    path persisted to ~/.config/c3/claude-shim.json.
  ```
- Why both surfaces: stdout is visible in the Claude-Code agent
  transcript; stderr is visible in the raw shell where setup may have
  been driven from. Cost is trivial (a few extra lines of duplicate
  text); benefit is the user actually sees it.

**Tests (add to `cmd/c3-broker/setup_claude_shim_test.go`):**
1. `TestMaybeInstallClaudeShim_FailureSurfacesBlockOnBothStreams` —
   set `installClaudeShimFn` to return `errors.New("simulated: existing
   non-shim file")`; capture stdout AND stderr; call a new helper
   `runShimInstallStep(host)` that wraps the maybeInstall + failure
   reporting; assert both streams contain `[claude shim NOT installed]`
   AND `c3-broker install-claude-shim --force`.

**Refactor needed for testability**:
- Today, `runSetup()` inlines the warning print. To test the surface
  without invoking the full setup flow, extract the print to
  `printShimInstallFailure(stdout, stderr io.Writer, err error)`.
  `runSetup` calls it with `os.Stdout, os.Stderr`. Test calls it with
  `bytes.Buffer{}` for each stream.

**Edge cases**:
- HostCodex / HostUnknown: `maybeInstallClaudeShim` returns nil; no
  failure block is printed (existing behaviour). The existing
  TestMaybeInstallClaudeShim_CodexHostSkipsInstaller / UnknownHost...
  tests still pass.
- Failure path with `force` already attempted manually: out of scope.
  The block tells the user how to retry; if --force also fails, that's
  a separate problem and the user has the actionable hint.

## Fix M3 — DRAFT marker enforcement

**File**: `cmd/c3-broker/preamble.go`.

**Why**: today `TestPreambleCopy_HasDraftMarker` asserts the marker
exists. After Karthi approves the copy, removing the marker requires:
(a) edit the copy string to drop the footer, (b) edit the test to
invert the assertion, (c) edit the package comment. Forgetting (a)
ships the marker. Forgetting (b) breaks the test (visible). The
dangerous case is editing the copy AND test but missing some other
context — the test catches the wrong thing once `draftApproved=true`
is intended.

**Design**:
- Add `const draftApproved = false` at the top of `preamble.go` with
  a multi-line explanatory comment above it. The comment is the
  actual safety mechanism for a maintainer-led project — it tells
  whoever flips it what else they need to do in the SAME commit.
- Extract `renderPreamble(draftApproved bool) string` from
  `preambleCopy`/`printPreamble`. The DRAFT footer line is
  conditionally appended iff `!draftApproved`. The body (sections 1-4)
  is the same in both branches.
- `printPreamble()` calls `renderPreamble(draftApproved)` and writes
  to stdout.
- Refactor `TestPreambleCopy_HasDraftMarker` into two subtests
  (`t.Run`) that flip the bool argument directly — no mutation of the
  package-level const needed:
  - `subtest "draftMode renders marker"`: `renderPreamble(false)` must
    contain `"[DRAFT 2026-05-19"`.
  - `subtest "approvedMode omits marker"`: `renderPreamble(true)` must
    NOT contain `"DRAFT"` AND must NOT contain `"pending Karthi review"`.
- Keep `TestPreambleCopy_ContainsKeyConcepts` and
  `TestPreambleCopy_NotTooLong` — they assert on the body, not the
  footer.

**Subtle point about `preambleCopy` const**:
- Today `preambleCopy` is a raw string literal that ends with the
  `[DRAFT 2026-05-19 — copy pending Karthi review]` footer line. The
  cleanest refactor:
  - Rename `preambleCopy` → `preambleBody` (the part without the
    footer).
  - Remove the trailing `[DRAFT ...]` line from the const body.
  - In `renderPreamble`, append the footer string when
    `!draftApproved`.
  - Update `TestPreambleCopy_ContainsKeyConcepts` to assert on
    `renderPreamble(true)` (the approved-mode output, which has all
    the load-bearing concepts; footer was never load-bearing).

**No "constant cannot be flipped" test**: per the task brief, this
would be hostile in a maintainer-led project. The explanatory comment
above the const is the actual mechanism.

**Tests (modify/add to `cmd/c3-broker/preamble_test.go`):**
1. `TestRenderPreamble_DraftMode_RendersMarker` (replaces / supplements
   `TestPreambleCopy_HasDraftMarker`).
2. `TestRenderPreamble_ApprovedMode_OmitsMarker` — new.
3. `TestPreambleCopy_ContainsKeyConcepts` — update to test
   `renderPreamble(true)` so the load-bearing-concepts assertion holds
   in both modes.
4. `TestPreambleCopy_NotTooLong` — update similarly (probably check
   `renderPreamble(draftApproved)` so it tracks whatever mode ships).

## Verification (run after all three land)

- `go test -count=1 -race ./...` — clean.
- `go vet ./...` — clean.
- `go build ./...` — clean.
- 6+ new/modified tests across the three fixes:
  - M1: 5 new tests in `shimconfig_test.go`.
  - M2: 1 new test in `setup_claude_shim_test.go`.
  - M3: 2 new tests + 2 updated in `preamble_test.go`.

## Self-review rubric

Run before claiming done:
- general.md §3 ("don't add hooks without a current caller") — the
  `LoadStrict` function in M1 must be called from somewhere; if it's
  not, drop it OR document where it's expected to be wired in later.
  Decision: wire `LoadStrict` into install_claude_shim.go where the
  symlink path now reads the config; surface the error as a warning
  on the install path. Or, simpler: keep the strict path internal to
  the package and have `Load` return ok=false + stderr-once on
  v999. Picking the simpler path: do NOT add a public LoadStrict;
  the v999 case logs via the same sync.Once mechanism and returns
  ok=false. Keeps the public surface minimal.

**Revised M1 design** (after self-review): no `LoadStrict`. `Load`
handles all cases via one `sync.Once`-gated warning. Cleaner; matches
existing "silent fallback to PATH walk; never hard-fail at runtime"
contract; one fewer exported symbol; one fewer caller to wire.

The schema-version-write side stays as-is. Save always writes 1.

The "v999 returns error" task constraint becomes: v999 returns ok=false
+ a one-time stderr log "shimconfig: schema version %d unsupported on
disk; falling back to PATH walk. Remove ~/.config/c3/claude-shim.json
or downgrade c3 to fix." (Same actionable hint, no need for a new
public API.)

Test `TestLoad_V999_OkFalse` updated: still asserts ok=false, but
asserts stderr contains the unsupported-version message (sync.Once
shared with the v0 upgrade log — both go through the same `once.Do`
warning?? NO — different messages. Use TWO separate `sync.Once`
guards, one per warning kind. Documented in the test plan.).

- go.md §5 ("godoc on every exported identifier") — `currentSchemaVersion`
  const, any new exported function get doc comments.
- daemon.md §5 ("schema version on disk") — M1 directly addresses
  this rule.
- cli.md §3 ("stdout is data; stderr is conversation") — M2 puts the
  failure block on BOTH streams. Justified because under Claude Code
  the agent never sees stderr; under a raw shell the user might miss
  stdout intermixed with the long setup transcript. The block is
  named (`[claude shim NOT installed]`) so it's greppable in either
  stream. Documented in the helper's doc comment.

## Out of scope (explicit)

- 7 MINOR + 5 NIT findings → daylight triage.
- Guideline file clarifications (5 items) → Karthi handles in morning.
- TODO updates beyond the one MORNING-REVIEW line.
- Commit creation → Karthi reviews diff first.

## Time budget

2 hours wall clock. If exceeded, stop and report partial.
