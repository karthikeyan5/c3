# C3 commands — cross-CLI source of truth

The verb spec for C3's user-facing commands. Each verb is implemented
once in C3 (in the broker as a CLI subcommand, or in the adapter as an
MCP tool) and exposed by each CLI through a thin wrapper. **When a
verb's behavior changes, edit this file first**, then sync each CLI's
wrapper to match.

The principle: the actual logic lives in the
shared layer (broker / MCP); per-CLI surface area is intentionally
thin so the same set of verbs is trivial to add for every new CLI we
support.

## Verb table

| Verb            | Shared interface                  | Mode          | What it does                                                                                                          |
|-----------------|-----------------------------------|---------------|-----------------------------------------------------------------------------------------------------------------------|
| `status`        | `c3-broker status` (CLI)          | pure shell    | Daemon liveness, socket reachability, mappings.json validation, channel state, **live route claims** (via OpListClaims). |
| `topics`        | `c3-broker topics` (CLI)          | pure shell    | List every topic in mappings.json + which session (if any) currently claims it.                                       |
| `build`         | `go install ./cmd/...` (shell)    | pure shell    | Rebuild C3 binaries from the plugin source dir.                                                                       |
| `setup`         | `c3-broker setup` (CLI)           | interactive   | Prompt the user for bot token + chat IDs; validate token via Telegram getMe; write mappings.json (mode 0600).         |
| `reload-config` | `pkill -HUP c3-broker`            | pure shell    | Signal the broker to re-read mappings.json. Non-disruptive — no process restart, in-memory pointer swap, live claims preserved. Replaces the old `restart-broker` (which killed the CLI's MCP server as a side effect — see 2026-05-14 RESUME). For binary updates, restart Claude Code instead. |
| `attach`        | `mcp_attach(expr=…)` (MCP tool)   | LLM dispatch  | Attach this session's adapter to a Telegram topic. Broker parses `expr` and either silent-claims or proposes.          |
| `detach`        | `mcp_detach()` (MCP tool)         | LLM dispatch  | Release the session's current claim (sends `OpRelease`).                                                              |

## attach — the parser (lives in the broker)

`AttachReq.Expr` is a single user-supplied string. The broker's
`applyExprToAttachReq` (in `internal/broker/attach.go`) parses it. Rules:

| Input                              | Resolves to                                  |
|------------------------------------|----------------------------------------------|
| `""` (empty)                       | use cwd-saved mapping (silent claim)         |
| `"dm"` / `"DM"` (case-insensitive) | `target = "dm"` (DM disambiguation may fire) |
| `"<int>"`                          | `topic_id = <int>`                           |
| `"create <name>"`                  | `name = <name>, create = true`               |
| `"-y <name>"` / `"yes <name>"`     | `name = <name>, create = true`               |
| `"<other string>"`                 | `name = <string>`                            |

Whitespace is trimmed. Unparsable input falls through to `name`.

## attach — proposal flow

The broker may return `needs_confirmation: true` with a proposal action.
The slash command wrapper must handle each one — typically by asking the
user via the CLI's confirmation primitive (`AskUserQuestion` in Claude
Code, equivalent in Codex) and re-invoking attach with the confirmation
flag set.

| Proposal action            | What the wrapper should do                                                                                                       |
|----------------------------|----------------------------------------------------------------------------------------------------------------------------------|
| `create`                   | Ask "create new topic <name> in group <group>?" → on yes, re-invoke with `create=true`.                                           |
| `use_existing_other_group` | Ask "claim existing <name> in group <other_group>, or create new in <default_group>?" → re-invoke per choice.                     |
| `disambiguate_dm`          | Ask "topic 'dm' exists; did you mean the topic or actual DM?" → topic: `topic_id=<from proposal>`. DM: `target="dm", steal=true`. |
| `force_steal`              | Ask "topic held by <holder cli pid cwd>; evict and take?" → on yes, re-invoke with `steal=true`. Only with explicit user OK.      |

## Per-CLI implementations

| Verb            | Claude Code (slash)                                | Codex                                  |
|-----------------|----------------------------------------------------|----------------------------------------|
| `status`        | `plugins/c3/commands/status.md`                    | _todo: codex command_                  |
| `topics`        | `plugins/c3/commands/topics.md`                    | _todo_                                 |
| `build`         | `plugins/c3/commands/build.md`                     | _todo_                                 |
| `setup`         | `plugins/c3/commands/setup.md`                     | _todo_                                 |
| `reload-config` | `plugins/c3/commands/reload-config.md`             | _todo_                                 |
| `attach`        | `plugins/c3/commands/attach.md`                    | _todo (codex MCP attach tool exists)_  |
| `detach`        | `plugins/c3/commands/detach.md`                    | _todo (codex MCP detach tool not yet)_ |

When adding a new verb:
1. Implement the shared interface (broker subcommand or MCP tool).
2. Update the verb table above.
3. Add the per-CLI wrapper file(s) and update the per-CLI table.
4. Cover with a test in `cmd/c3-broker/...` (CLI subcommands) or
   `cmd/c3-claude-adapter/wire_test.go` (MCP tools) where practical.

## Change-management notes

- `attach.md` is the longest wrapper because the LLM has to handle four
  distinct proposal flows. Every other wrapper is one Bash line + a
  one-sentence display instruction. **Resist adding logic to the
  wrappers** — push it into the broker or MCP tool so all CLIs benefit.
- The `Expr` parser is the single chokepoint for `attach`'s "user typed
  a string" interface. New input forms should be added to
  `applyExprToAttachReq` and documented in the parser table above —
  not encoded into each CLI's wrapper.
- Force-steal flow exists because the broker now refuses to displace a
  live PID's claim by default (per the "broker is authority" principle).
  If you change that policy, update both the proposal-flow table here and
  the `tryClaim` doc-comment in `internal/broker/attach.go`.
