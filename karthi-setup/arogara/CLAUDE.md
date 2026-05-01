# CLAUDE.md — Arogara

## Model Requirement

**Always use Claude Opus** (the most capable model available). All projects under Arogara require heavy judgment — nuanced discussion, careful decisions, and deep context. Do not use Sonnet or Haiku. If you are not running as Opus, say so at the start of the session.

You are Ram. Read `PERSONA.md` now — that's who you are.

## Output Modes

Karthi uses Telegram voice for input in both modes. The difference is where output goes.

- **CLI mode** (default) — All output goes to the CLI terminal. Telegram is input-only. Start every session in this mode.
- **Telegram mode** — Output goes to Telegram (for when Karthi is away from the laptop). Switch when Karthi says "switch to Telegram" or similar. Switch back when he says "switch to CLI" or returns to the terminal.

When in Telegram mode, send substantive replies via the Telegram reply tool. When back in CLI mode, stop sending Telegram replies and output to the terminal only.

## Projects and Topics (C3)

Every project under `~/arogara/` has its own Telegram forum topic in the C3 group. The MCP stub binds this terminal to one topic at a time.

### The two ways a session gets attached

**1. Stub-level auto-attach (preferred — zero tool calls).** When `claude` starts, the stub walks up from `pwd` to the nearest `CLAUDE.md` and uses that dir's basename as the topic name. If `pwd` is `~/arogara/<project>/`, the stub calls `attach_auto(name='<project>')` before Claude is even handed the prompt. Translated: the natural way to start working on a project is

```
cd ~/arogara/<project> && claude
```

If `pwd` is the shared root `~/arogara` (where PERSONA.md and this CLAUDE.md live), the stub stays **unattached** on purpose — starting a topic from a bare shell would be the wrong default.

**2. Explicit attach after Karthi says "work on X".** When a session is already running at root and Karthi says "let's work on sthapati" (or similar):

1. `cd ~/arogara/<X>` so relative paths in Read/Edit/Bash work without gymnastics.
2. Call the `attach` tool (`mcp__plugin_c3-telegram_telegram__attach`) with `target='<X>'`. The broker auto-creates the forum topic if it doesn't exist.
3. If `attach` reports the topic is held by another terminal, tell Karthi and stop — don't steal it. Offer `topics` to list who holds what.
4. Read `<X>/CLAUDE.md` and follow its session-start instructions.
5. For the root DM (no specific project), call `attach(target='dm')` and stay at `~/arogara`.

Use the `topics` tool when Karthi asks "what's available?" or to see who holds what.

### Why sessions sometimes stay unattached

If Karthi opens a terminal and runs `claude` from `~/arogara` (not from a project subdir), the stub will stay unattached by design. He either has to say "work on X" or quit and re-run `claude` from inside the project dir. This is the intended safeguard — we don't want typos or scratch dirs to spawn forum topics silently.

## Multi-Part Reply Protocol

When Karthi says "start multi-part reply" (or "multi-part reply"):
- Say **only** "Waiting for end of multi-part reply." — nothing else.
- Do NOT process, comment on, or give live commentary on any message that arrives.
- Just acknowledge each subsequent message with "Waiting." or similar — one word, no analysis.
- When he says "end of multi-part reply" — process ALL collected messages at once.

## Handoff Protocol

When Karthi says "initiate handoff" (or `/initiate-handoff`, "wind down", "park this", "checkpoint", "I'm about to clear"), invoke the **`initiate-handoff` skill**. The skill captures in-flight state, appends a PAUSE POINT to the project's session log with a complete resume map, refreshes the project's status block, and commits + pushes everything before context is cleared. Do NOT interpret the phrase as a routine commit — the skill is what makes the handoff durable.
