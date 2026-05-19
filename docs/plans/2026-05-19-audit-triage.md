# Audit triage — 2026-05-19

Triage of the 13-entry cross-CLI duplication audit in
`docs/research/2026-05-18-cross-cli-duplication-audit.md`, per Karthi's
2026-05-19 voice rules:

- **(a)** Clearly decreases complexity → fix without asking.
- **(b)** 50/50 → don't fix; surface to Karthi for review.
- **(c)** Clearly increases complexity or unnecessary → drop with one-line reason.

Standing principles:
- Every flow must work the same in Codex — parity bugs masquerading as
  duplication go in (a).
- Don't pile on — if extracting introduces a new package or abstraction
  heavier than the current 2 inline copies, that's (c).
- Load-bearing divergences stay separate; document why.

## Classification summary

| # | Title                                  | Class | Action                          |
|---|----------------------------------------|-------|---------------------------------|
| 1 | modeProtocol                           | done  | already extracted to `internal/mode/` |
| 2 | mcpProtocolVersion                     | done  | const removed entirely by SDK migration |
| 3 | Tool descriptions                      | (b)   | Karthi review — Codex has 2 extra tools + different attach desc; helper signature non-trivial |
| 4 | formatAttached                         | (a)   | extract to `internal/ipc/format.go` |
| 5 | formatTopics                           | (a)   | extract to `internal/ipc/format.go` |
| 6 | "no topics configured" const           | (c)   | drop — 3 occurrences, identical-already, const-per-string is heavier than the dedup it buys |
| 7 | Broker reconnection error strings      | (b)   | Karthi review — strings near-identical, 1 outlier ("broker not connected"); const file or shared error helpers are a design call |
| 8 | idleStartupTimeout                     | (c)   | drop — single 60s constant, 2 callers, Codex already comments "mirror Claude" |
| 9 | installSignalHandlers                  | (c)   | drop — 9-line byte-identical func, 2 callers; new `internal/adapter/` package would violate the 3-caller floor |
| 10| spawnBroker (post-m3 stderr alignment) | (c)   | drop — same 3-caller floor reasoning as #9; identical now, no daylight between them to abstract over |
| 11| Restart instructions                   | n/a   | intentional divergence (already noted as "do not consolidate") |
| 12| Codex MCP registration                 | n/a   | intentional divergence (already noted) |
| 13| Attach request param handling          | n/a   | intentional divergence (already noted) |

Net: 2 already-done, 2 extract, 4 drop, 2 Karthi-review, 3 correct-divergences.

## (a) Extractions

### Plan: formatAttached + formatTopics → `internal/ipc/format.go`

**Why a:** Both adapters now have all 4 proposal branches (the
disambiguate_dm / force_steal parity bug surfaced in the audit was
fixed under TODO #14's overnight work). What remains is identical
shape with trivial wording divergence ("To proceed, call X. To use Y
instead, call Z." vs "Call X to proceed, or Z to use Y") — pure
divergence-without-purpose. `internal/ipc/` already owns the
`AttachedMsg` / `TopicsListMsg` types these functions format, so
collocating the renderer with the type is a clean fit. Adds to an
existing package — no new-package fence to clear.

**Single source picked:** the Claude adapter's wording for each branch
is the older/more-explicit form; we adopt it verbatim. (Codex tests
match on substrings like `"Call attach(create=true)"` which are
present in the Claude wording too, OR the test will be re-anchored
to the unified wording before the extraction lands.)

**Surface:**

```go
package ipc

func FormatAttached(a *AttachedMsg) string { ... }
func FormatTopics(list *TopicsListMsg) string { ... }
```

Both adapter call-sites become:

```go
return toolTextResult(ipc.FormatAttached(&attached)), nil
return toolTextResult(ipc.FormatTopics(&list)), nil
```

The two `formatAttached`/`formatTopics` functions in the adapters are
deleted. Existing tests:
- `cmd/c3-claude-adapter/wire_test.go`'s `TestFormatAttached_*` tests
  call the package-local `formatAttached` — those become thin
  wrappers calling `ipc.FormatAttached`, OR get moved to
  `internal/ipc/format_test.go`. We move them (single source for the
  formatter, single source for the formatter's tests).
- `cmd/c3-codex-adapter/wire_test.go`'s `TestFormatAttached_*` tests
  do the same. They're identical to Claude's parity test in shape;
  we keep ONE copy in `internal/ipc/format_test.go` and delete the
  duplicated tests.

**Behavioural risk:** none — the helpers stay pure functions of
`*AttachedMsg`/`*TopicsListMsg`. Both adapters produce identical
formatter output after extraction. The Claude-adapter wording was
already the substring-tested baseline; the codex wording was a
trivial paraphrase that lost no information.

**TDD discipline:** because this is a code-move with intentional
wording unification (Codex moves to Claude wording, not the other
way around), tests must be written/moved FIRST asserting the
unified wording, then implementation aligned. Specifically:

1. Move/rename Claude's `TestFormatAttached_ProposalParity` (Claude
   doesn't have one — codex does) and `TestFormatAttached_{NoTopicsConfigured,PolicyRejected}` to
   `internal/ipc/format_test.go`. Reference `ipc.FormatAttached`.
2. Run tests → FAIL (function doesn't exist yet).
3. Add `FormatAttached` + `FormatTopics` to `internal/ipc/format.go`
   with the Claude-wording bodies.
4. Run tests → PASS.
5. Switch claude/codex adapters to call `ipc.FormatAttached` /
   `ipc.FormatTopics`; delete the inline copies.
6. Run full suite → PASS (both adapters' wire_tests still green).

## (c) Drops — recorded with reasons

These don't get touched. Recorded in the audit doc itself for
traceability.

- **#6 "no topics configured" const** — only 3 occurrences, adapter
  strings already identical, broker uses no-period for CLI output
  (different surface, different convention). A shared const buys a
  1-byte edit guarantee at the cost of a `const` + 2 imports.
- **#8 idleStartupTimeout** — single 60s value, 2 call sites, Codex's
  definition already comments "mirror cmd/c3-claude-adapter behavior".
  No new package for one constant.
- **#9 installSignalHandlers** — 9-line byte-identical func, 2
  callers. New `internal/adapter/` package would house ONE function
  with 2 callers — violates the 3-caller floor. Existing
  cross-reference comment keeps drift visible.
- **#10 spawnBroker (post-m3 alignment)** — now byte-identical
  between adapters. Same 3-caller-floor reasoning as #9. `sysSetsid`
  is a 3-line OS-specific helper duplicated in both setsid.go files;
  same reasoning.

## (b) Karthi-review entries

See "## 50/50 — needs Karthi review" at the bottom of the audit doc.
