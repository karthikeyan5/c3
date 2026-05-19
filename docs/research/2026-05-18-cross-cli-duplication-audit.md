# Cross-CLI Duplication Audit
**Date:** 2026-05-18  
**Scope:** C3 Claude Code + Codex adapters, broker setup, and launcher integration  
**Status:** Research-only; no code modifications

## Executive Summary

Found 12 duplications spanning tool descriptions, error messages, protocol constants, restart instructions, and attach proposal formatting. Most are high-impact string literals that diverged slightly between adapters (e.g., "Found topic..." vs "Found %q in...") or were never unified. Three are load-bearing: the OUTPUT MODE PROTOCOL needs extraction to `internal/mode/protocol.go` (parallel agent J), tool descriptions should consolidate, and the spawnBroker stderr-handling difference is subtle but critical.

---

## Duplications

### 1. OUTPUT MODE PROTOCOL (modeProtocol const)
**Severity:** HIGH — Load-bearing protocol rule
**Triage 2026-05-19:** [x] EXTRACTED — already landed before triage. `internal/mode/protocol.go` is the single source of truth; both adapters consume `mode.Combined()` in `buildInstructions()`, and the Codex AGENTS.md installer (`cmd/c3-broker/cli_host.go::ensureCodexAgentsMd`) sources the same body. See TODO #11.
**Status:** Partially in-flight (parallel agent J)

**What:** Identical agent-facing instruction appended to all adapter initialize responses explaining CLI vs Telegram mode switching semantics.

**Locations:**
- `cmd/c3-claude-adapter/main.go:695-699` (const modeProtocol, 4 lines)
- `cmd/c3-codex-adapter/main.go:502-506` (const modeProtocol, 4 lines + 1 mirror comment)

**Current divergence:**
- Claude: "arrive as `<channel>` blocks"
- Codex: "arrive as buffered inbound"

Both reference Karthi's standing instruction (2026-05-15 in Claude adapter) to make this part of the plugin contract.

**Single-source approach:**
Extract to `internal/mode/protocol.go` as an exported constant. Both adapters import and append it to their buildInstructions(). The Codex-specific wording ("buffered inbound" vs "`<channel>` blocks") can stay in `buildInstructions()` headings; the common protocol appends the same bytes.

**Risk if left as-is:** Drift — if the protocol rule evolves, both adapters must be edited. Agents trained on one adapter's rule won't match the other's.

**Effort:** Small (10 min extraction + imports)

---

### 2. MCP Protocol Version
**Severity:** LOW — Constant, no business logic
**Triage 2026-05-19:** [x] RESOLVED — `mcpProtocolVersion` const has been removed entirely from both adapters by the SDK migration (TODO #21). The `modelcontextprotocol/go-sdk` library handles protocol version negotiation internally. No code-action required.

**What:** The MCP spec version string "2024-11-05" is duplicated as a const in each adapter.

**Locations:**
- `cmd/c3-claude-adapter/main.go:59` (const mcpProtocolVersion)
- `cmd/c3-codex-adapter/main.go:53` (const mcpProtocolVersion)

**Current divergence:** None; exact string match.

**Single-source approach:**
Extract to `internal/mode/protocol.go` or a new `internal/ipc/protocol.go`. Both adapters already import shared packages (e.g., `internal/ipc`), so consolidation is natural.

**Risk if left as-is:** Low. Constant is stable. Any future upgrade needs two edits.

**Effort:** Small (consolidate to existing internal pkg)

---

### 3. Tool Descriptions (attach, reply, react, etc.)
**Severity:** MEDIUM — UX-visible but identical intent
**Triage 2026-05-19:** (b) 50/50 — needs Karthi review. See bottom of doc.

**What:** MCP tool schema descriptions are defined separately in each adapter's `toolsListResponse()`.

**Locations:**
- `cmd/c3-claude-adapter/main.go:718-799` (tools list)
- `cmd/c3-codex-adapter/main.go:525-627` (tools list)

**Current divergence:**

| Tool | Claude | Codex |
|------|--------|-------|
| attach | Long detailed description with expr/target/name/etc. | "Same proposal-flow semantics as Claude Code's attach." (references Claude) |
| topics | "List known Telegram topics across all groups, with claim state." | "List known Telegram topics + claim state." (shorter) |
| reply | "Send a Telegram reply to the currently-attached topic." | (identical) |
| react | "Set a single-emoji reaction on a Telegram message." | (identical) |
| edit_message | "Edit a previously-sent Telegram message." | (identical) |
| send_typing | "Send a typing indicator to the attached topic." | (identical) |
| download_attachment | "Download a Telegram file by file_id; returns the local path." | (identical) |

Codex has two extras (`inbox`, `codex_forward`) that Claude doesn't.

**Single-source approach:**
Create `internal/mcp/tools.go` with a func `BuildToolList(forCodex bool) []map[string]any`. Base tools are shared; Codex-only tools are conditionally appended. CLI-specific wording (e.g., attach description) can remain in adapters but delegate to a shared builder for the rest.

**Risk if left as-is:** MEDIUM — if tool names change or descriptions need improvement, both adapters must be edited and tested.

**Effort:** Medium (refactor toolsListResponse logic into shared builder)

---

### 4. Attach Proposal Formatting (formatAttached function)
**Severity:** MEDIUM — Business logic divergence
**Triage 2026-05-19:** [x] EXTRACTED 2026-05-19 — `internal/ipc/format.go::FormatAttached`. Both adapters now call `ipc.FormatAttached(&attached)`. The 4-proposal-action parity (disambiguate_dm + force_steal) is now structurally guaranteed by a single implementation; the Codex paraphrased wording is replaced with the Claude original. Tests centralised in `internal/ipc/format_test.go`. See `docs/plans/2026-05-19-audit-triage.md`.

**What:** Both adapters implement `formatAttached()` to render attach responses. The "create" and "use_existing_other_group" cases diverge subtly; the "disambiguate_dm" and "force_steal" cases are **missing from Codex entirely**.

**Locations:**
- `cmd/c3-claude-adapter/main.go:910-947` (formatAttached, full impl)
- `cmd/c3-codex-adapter/main.go:706-735` (formatAttached, truncated impl)

**Current divergence:**

| Proposal Action | Claude | Codex |
|---|---|---|
| "create" | "To proceed, call attach(create=true). To use an existing topic instead, call attach(topic_id=<n>)." | "Call attach(create=true) to proceed, or attach(topic_id=<n>) to use an existing topic." |
| "use_existing_other_group" | "Reply yes to claim it or attach(create=true, group=...) ..." | "Reply yes to claim it or attach(create=true) to create..." (missing group param) |
| "disambiguate_dm" | Full case with error message | **MISSING** |
| "force_steal" | Full case with holder info | **MISSING** |

**Single-source approach:**
Extract `formatAttached()` to `internal/ipc/attach_format.go` as a shared function. Pass the `ipc.AttachedMsg` once; format once. Both adapters call the same func, so wording and completeness are guaranteed identical.

**Risk if left as-is:** HIGH — Codex is silently incomplete. If the broker sends a "disambiguate_dm" or "force_steal" proposal to Codex, the agent sees "unspecified failure" instead of a real error message. Drift risk if either wording changes.

**Effort:** Small (extract function, add imports)

---

### 5. Topics Formatting (formatTopics function)
**Severity:** LOW — Wording differs very slightly
**Triage 2026-05-19:** [x] EXTRACTED 2026-05-19 — `internal/ipc/format.go::FormatTopics`. Both adapters now call `ipc.FormatTopics(&list)`. Bodies were byte-identical pre-extraction; collocating with `FormatAttached` in the same file keeps both renderers next to the `AttachedMsg`/`TopicsListMsg` types they consume.

**What:** Both adapters implement `formatTopics()` to render the list of known topics.

**Locations:**
- `cmd/c3-claude-adapter/main.go:976-991` (formatTopics)
- `cmd/c3-codex-adapter/main.go:764-779` (formatTopics)

**Current divergence:**
- Claude: "no topics configured."  
- Codex: "no topics configured." (identical!)

State line: both use `fmt.Sprintf("held by %s pid %d", ...)` identically.

The only micro-difference: Claude's return string is "no topics configured." (with period); Codex's is also "no topics configured." — they're the same.

**Single-source approach:**
Extract to `internal/ipc/topics_format.go` or consolidate with formatAttached in the same file.

**Risk if left as-is:** Low. Wording is already identical. Risk is purely future maintenance (if list format changes, both must be updated).

**Effort:** Small (extract, add imports)

---

### 6. Adapter "no topics configured" Message
**Severity:** TRIVIAL — Consistency only
**Triage 2026-05-19:** [x] DROPPED 2026-05-19 — micro-string consolidation. After #5 the two adapter sites both vanish into `ipc.FormatTopics`; only the broker CLI (`cmd/c3-broker/topics.go:20`, no-period form) remains separate, and that divergence is intentional (CLI-formatted output convention, see audit Edge Cases §2). A shared const for one CLI surface + one adapter surface would add an import for a 1-string buy.

**What:** When the broker CLI (`c3-broker topics`) finds no topics, it prints "no topics configured". Adapters print the same message.

**Locations:**
- `cmd/c3-broker/topics.go:20` ("no topics configured")
- `cmd/c3-claude-adapter/main.go:978` ("no topics configured.")
- `cmd/c3-codex-adapter/main.go:766` ("no topics configured.")

**Current divergence:**
- Broker: no period
- Adapters: period appended

**Single-source approach:**
Define once in a shared location (e.g., `internal/ipc/consts.go`) as a const or shared function output.

**Risk if left as-is:** Trivial. Messages are nearly identical. Polish issue only.

**Effort:** Trivial (one-line const extraction)

---

### 7. Broker Reconnection Error Messages
**Severity:** LOW — Similar but distinct per adapter
**Triage 2026-05-19:** (b) 50/50 — needs Karthi review. See bottom of doc.

**What:** When the broker is unreachable, both adapters emit similar error messages during tool calls.

**Locations:**
- `cmd/c3-claude-adapter/main.go:835, 889, 959, 1008` (4 messages)
- `cmd/c3-codex-adapter/main.go:689, 747, 848` (3 messages)

**Current divergence:**
- Claude: "broker not connected" (line 835 only)
- Both: "broker reconnecting — retry X in a moment" (near-identical)

The core pattern is identical; wording is the same.

**Single-source approach:**
Define error message strings in `internal/ipc/errors.go` or a constants file. Both adapters reference the same strings.

**Risk if left as-is:** Low. Strings are already nearly identical. Risk is message consistency if requirements change.

**Effort:** Trivial (extract const strings)

---

### 8. Idle Startup Watchdog Timeout
**Severity:** LOW — Identical constant
**Triage 2026-05-19:** [x] DROPPED 2026-05-19 — single 60s constant, 2 call sites, Codex already comments "mirror cmd/c3-claude-adapter behavior" inline. Extracting one const into a new `internal/adapter/` package violates the 3-caller floor; adding it to an unrelated existing package is architecturally noisier than the duplication it removes.

**What:** Both adapters implement an idle-startup watchdog with a timeout of 60 seconds.

**Locations:**
- `cmd/c3-claude-adapter/main.go:56` (const idleStartupTimeout = 60s)
- `cmd/c3-codex-adapter/main.go:63` (const idleStartupTimeout = 60s, with comment "mirror cmd/c3-claude-adapter behavior")

**Current divergence:** None; Codex even has an explicit comment saying it mirrors Claude.

**Single-source approach:**
Extract to `internal/adapter/consts.go` and import in both adapters.

**Risk if left as-is:** Low. Constant is stable and Codex's comment explicitly cross-references Claude. Risk is only if the timeout needs tuning.

**Effort:** Trivial (move to internal pkg)

---

### 9. Signal Handler Installation
**Severity:** LOW — Identical behavior, commented reference
**Triage 2026-05-19:** [x] DROPPED 2026-05-19 — 9-line byte-identical function, 2 callers, Codex cross-references Claude in a doc-comment. Same 3-caller-floor reasoning as #8 — a new `internal/adapter/` package would house one function with two callers. Drift surface is virtually zero (Go stdlib `signal.Notify` + `cancel()`).

**What:** Both adapters call `installSignalHandlers(cancel)` with identical implementations.

**Locations:**
- `cmd/c3-claude-adapter/main.go:129-137` (func installSignalHandlers)
- `cmd/c3-codex-adapter/main.go:117-125` (func installSignalHandlers, with comment referencing Claude adapter)

**Current divergence:** None; Codex's comment explicitly cites Claude's.

**Single-source approach:**
Extract to `internal/adapter/signal.go` and import in both.

**Risk if left as-is:** Low. Code is identical and Codex's comment cross-references Claude. Risk is purely if signal-handling logic needs to evolve.

**Effort:** Trivial (move to internal pkg)

---

### 10. spawnBroker() Stderr Handling (CRITICAL DIFFERENCE)
**Severity:** MEDIUM — Subtle behavioral divergence
**Triage 2026-05-19:** [x] DROPPED 2026-05-19 — the audit's "critical divergence" framing is stale: the trivial-fixes sweep (MINOR m3) aligned Codex's `cmd.Stderr = nil` to match Claude (load-bearing comment cross-referenced in both files). The bodies are now byte-identical including the helper `sysSetsid()` defined per-adapter in setsid.go. Same 3-caller-floor reasoning as #8/#9 applies — no shared package is justified by 2 functions × 2 callers each.

**What:** Both adapters implement `spawnBroker()` to spawn the broker daemon, but they differ in stderr handling.

**Locations:**
- `cmd/c3-claude-adapter/main.go:253-260` (spawnBroker)
- `cmd/c3-codex-adapter/main.go:192-199` (spawnBroker)

**Current divergence:**
```go
// Claude:
cmd.Stderr = nil

// Codex:
cmd.Stderr = os.Stderr
```

**Why:** Claude's comment explains: adapter stderr is piped to Claude Code's plugin host; noisy broker output makes the host "appear distressed" and may trigger CLI reconnect. Codex doesn't have this constraint (app-server owns MCP startup, not the TUI).

**Single-source approach:**
This divergence is **intentional and correct**. Do NOT consolidate. Document it clearly in the adapter-specific sections of a shared function or leave it as is.

If consolidation were needed (unlikely), pass a `forCodex bool` param to control stderr routing.

**Risk if left as-is:** None. The divergence is load-bearing and already commented. Consolidating naively would break Claude Code's plugin host.

**Effort:** None (leave as is, but document why they differ)

---

### 11. Restart Instructions (claudeRestartInstruction, codexRestartInstruction)
**Severity:** MEDIUM — High-impact user-facing strings
**Triage 2026-05-19:** n/a — correct divergence (CLIs have different startup contracts: Claude needs `--dangerously-load-development-channels`, Codex uses `resume --last`). No action; the audit doc itself called this out.

**What:** Setup prints different restart instructions depending on which CLI is detected.

**Locations:**
- `cmd/c3-broker/cli_host.go:78-86` (claudeRestartInstruction)
- `cmd/c3-broker/cli_host.go:88-99` (codexRestartInstruction)

**Current divergence:** Both are intentionally distinct (Claude needs `--dangerously-load-development-channels`; Codex uses `resume --last`). The divergence is correct and should not be consolidated.

**Single-source approach:**
Keep as-is. These are **correct divergences** reflecting the CLIs' different startup models. Do not consolidate.

**Risk if left as-is:** None. Divergence is necessary and correct.

**Effort:** None (leave as is)

---

### 12. Codex MCP Registration (ensureCodexMCPRegistration, codexC3MCPBlock)
**Severity:** MEDIUM — TOML generation; Claude has no equivalent
**Triage 2026-05-19:** n/a — correct divergence (Claude has `.mcp.json` shipped with the plugin; Codex needs `~/.codex/config.toml` written by setup). Different MCP registration models; no shared abstraction makes sense.

**What:** When setup detects Codex, it registers the c3_codex MCP server in `~/.codex/config.toml`. Claude Code has no equivalent because Claude's MCP config is `.mcp.json` in the plugin directory (not written by setup).

**Locations:**
- `cmd/c3-broker/cli_host.go:102-152` (ensureCodexMCPRegistration, codexConfigPath, containsCodexC3Section)
- `cmd/c3-broker/cli_host.go:183-193` (codexC3MCPBlock)

**Current divergence:**
- Codex writes TOML: `[mcp_servers.c3_codex]` with `command = "c3-codex-adapter"`
- Claude has `.mcp.json` already included in the plugin directory

This is a **correct and necessary divergence** — the two CLIs have different MCP registration models. Do NOT consolidate.

**Single-source approach:**
Keep as-is. Document why they differ (Claude's plugin mechanism vs Codex's server config).

**Risk if left as-is:** None. Divergence is necessary and correct.

**Effort:** None (leave as is)

---

### 13. Attach Request Parameter Handling
**Severity:** LOW — Identical logic

**What:** Both adapters parse the same attach request parameters from MCP arguments and build an `ipc.AttachReq`.

**Locations:**
- `cmd/c3-claude-adapter/main.go:848-877` (handleAttachLocal, param parsing)
- `cmd/c3-codex-adapter/main.go:651-677` (handleAttachLocal, param parsing)

**Current divergence:**
- Claude: Handles `expr` parameter (no Codex equiv)
- Codex: No `expr` handling (uses C3_ATTACH_NAME env var for auto-attach)

The difference is **correct** — Codex doesn't expose the `expr` parameter to agents; it's launcher-only.

**Single-source approach:**
Keep as-is. The implementations differ because the CLIs have different contracts with the launcher.

**Risk if left as-is:** None. Divergence is necessary.

**Effort:** None (leave as is)

---

## Recommended Consolidation Order

### Priority 1 (Load-bearing, high risk if missed)

1. **#4 Attach Proposal Formatting** — Codex is silently incomplete (missing "disambiguate_dm", "force_steal" cases). Extract `formatAttached()` to `internal/ipc/attach_format.go`. **Effort: Small.**

2. **#1 OUTPUT MODE PROTOCOL** — Already recognized in parallel agent J. Extract to `internal/mode/protocol.go`. **Effort: Small.** (Blocking on agent J completion)

### Priority 2 (Risk of drift)

3. **#3 Tool Descriptions** — If tool naming or semantics change, both adapters must be edited. Extract to `internal/mcp/tools.go`. **Effort: Medium.**

4. **#5 Topics Formatting** — Extract `formatTopics()` alongside attach formatting to `internal/ipc/topics_format.go`. **Effort: Small.**

### Priority 3 (Polish & maintenance)

5. **#2 MCP Protocol Version** — Extract to `internal/ipc/protocol.go` or `internal/mode/protocol.go`. **Effort: Small.**

6. **#6, #7, #8, #9** — Extract shared constants (error messages, timeout, etc.) to `internal/adapter/consts.go` or `internal/ipc/consts.go`. **Effort: Trivial (batch extraction).**

### Do Not Consolidate (Correct Divergences)

- **#10 spawnBroker() stderr** — Keep separate; behavioral difference is load-bearing.
- **#11 Restart Instructions** — Keep separate; divergence is correct.
- **#12 MCP Registration** — Keep separate; different MCP models.
- **#13 Attach Parameters** — Keep separate; different CLI contracts.

---

## Edge Cases & Open Questions

1. **Codex missing cases in formatAttached**: Are "disambiguate_dm" and "force_steal" proposals ever sent to Codex, or is the broker smart enough to avoid them? If they're possible, Codex needs the full impl. If they're never sent, the missing cases are dead code in Claude but silent failures in Codex. **Recommend: Ask broker owner for clarification.**

2. **Adapter vs Broker wording for "no topics"**: The broker's `topics` command prints "no topics configured" (no period) when called from the CLI, but adapters append a period. This is okay (adapters may format differently), but should it be consistent? **Recommend: Minor polish, low priority.**

3. **Tool description "Found topic..." divergence**: Claude says "Found topic %q in group..." but Codex says "Found %q in group...". The %q formats the topic name differently. **Recommend: Unify to Claude's wording (more explicit) during tool consolidation.**

---

## Summary

**Total duplications identified:** 13 (including 4 correct divergences that should NOT be consolidated)  
**Actionable duplications:** 9  
**Load-bearing (high priority):** 2 (OUTPUT MODE PROTOCOL, Attach Formatting)  
**Recommended consolidation effort:** ~2-3 hours for all 9 actionable items, starting with priority 1 & 2.

**Key insight:** Most duplications are low-risk (constants, error messages). The two load-bearing items (#1 and #4) are already in the TODO (parallel agent J for #1; #4 was not previously tracked).
