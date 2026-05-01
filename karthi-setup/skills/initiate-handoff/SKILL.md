---
name: initiate-handoff
description: Park an in-flight session before context clears. Captures unwritten state, appends a PAUSE POINT entry to the project session log with a complete resume map, refreshes the project status block, and commits + pushes everything before the user clears context. Triggered by phrases like "initiate handoff", "wind down", "park this", "checkpoint", "I'm about to clear" — or the explicit slash command.
allowed-tools: Bash, Read, Edit, Write
---

# initiate-handoff: Park a session before context clears

## When to invoke this skill

Trigger this skill when the user signals they're about to clear context, switch machines, or end a working session that has substantive in-flight state. Common signals:

- "/initiate-handoff", "/handoff", "/park", "/wind-down" (slash invocation)
- "Initiate handoff." / "Wind this down." / "Park this." / "Checkpoint." / "I'm about to clear context."
- "Make this resumable later." / "Set this up so I can come back to it."
- The user explicitly mentions clearing, closing the laptop, switching context, or ending the session.

**Do NOT invoke this skill for routine commits.** This is for **paused-mid-flow** moments where the in-flight thinking would be lost without an explicit capture step. If the work is naturally complete, a normal commit is enough.

## Why this skill exists

Context-clear is the single most common way good thinking gets dropped. Tasks the model is tracking, decisions made-but-not-yet-written, half-formed plans, "I was just about to..." — all of it lives only in conversation context, not on disk. After a /clear, it's gone.

This skill forces an **on-disk capture** of the current state's three layers:

1. **What's on disk already** (committed work) — git captures this.
2. **What's on disk but uncommitted** (modified files) — `git status` reveals this; this skill commits it.
3. **What's only in context** (in-progress thoughts, queued decisions, pending steps, unwritten conclusions) — only the user knows this; this skill asks for it explicitly.

Layer 3 is where the value is. The whole point of this skill is to surface and persist layer 3.

## Protocol — five steps, in order

### Step 1 — Discover the project's session log + status file

The skill works across projects with different conventions. Look for the session log first; the status file is secondary.

**Session log discovery (search in this order, accept the first match):**
- `forge/session-logs.md` (sthapati / forge-on-forge convention)
- `session-logs.md` at repo root
- `SESSION_LOGS.md` at repo root
- `docs/session-logs.md`
- `.session-logs.md` (hidden file convention)

**Status file discovery (search in this order):**
- `forge/state/priorities.md` (sthapati convention)
- `priorities.md` at repo root
- `STATUS.md` at repo root
- `TODO.md` (fallback — if it has a status section)

If neither file exists, surface this fact in step 2 and ask the user how they want to capture state. Never auto-create a session log file in an unfamiliar project — that's a structural decision the user should make.

If a project-level CLAUDE.md exists, also read it briefly — it may name a different session-log convention that overrides the search order above.

### Step 2 — Ask the user what's in flight

This is the load-bearing step. Surface, in plain language, the things that are only-in-context. Ask explicitly. Examples of useful prompts:

- "What's in flight that isn't on disk yet? Decisions made but not filed, half-written ideas, pending steps you'd want to resume from."
- "What's the *next* thing you'd do if we kept going right now? Capture that as the resume entry-point."
- "Any blockers / open questions you want flagged so a fresh session won't trip on them?"

If the conversation already has obvious in-flight state (e.g. an open multi-part discussion, pending decisions), name them in the question rather than asking blank-canvas. Make it easy for the user to confirm or extend.

**Output of this step:** a short list of in-flight items + a one-line "next thing if we kept going" + any open-question flags. This is the raw material for step 3.

### Step 3 — Append a PAUSE POINT entry to the session log

Write a new entry at the bottom of the session log (the file is append-only by convention; never edit prior entries). Use this structure:

```markdown
### 🔴 PAUSE POINT — YYYY-MM-DD <session-tag-if-needed>

<One-paragraph narrative of what was happening at the pause. What was being worked on, why this stopping point.>

**State on disk at pause:**
- <key file>: <committed | modified | uncommitted; one line on what's there>
- <repeat for each relevant file>
- HEAD: <commit SHA — fill in after the commit in step 5>

**In-flight items captured from user (step 2):**
- <each item, one line>

**Open questions / flags for resume:**
- <each, one line>

**Resume map for fresh-context session:**
1. `cd <repo path> && claude` (or whatever the project's entry pattern is).
2. Read `<status file>` Status block — points here.
3. Read this pause entry (session log bottom).
4. Read <any other files the resumer needs>.
5. Start by <concrete next action — taken from step 2's "next thing if we kept going">.

**Output-mode state at pause:** <CLI / Telegram / other — for projects that have output-mode conventions like sthapati's CLAUDE.md>.
```

Keep entries concise but complete. The test: a fresh model, with no prior context, should be able to pick up exactly where the user left off by reading this entry + the named files. No load-bearing implicit knowledge.

### Step 4 — Update the project status file

Update the Status block of the project's `priorities.md` (or equivalent) to point at the pause entry. Typically this means:
- Add a 🔴 PAUSE POINT line at the top of Status with a one-line summary + a link to the session-log entry.
- Update any "current plan" or "next action" lines to reflect the paused state.
- Don't rewrite the whole file — just the Status block + any directly-affected live flags.

If the project has no equivalent status file (the search in step 1 failed), this step is skipped; the session-log entry alone has to carry the resume map.

### Step 5 — Commit and push

Stage the modified files (session log, status file, any in-flight uncommitted work the user wants captured). Commit with a clean message that names this as a wind-down checkpoint, e.g.:

```
forge: 🔴 PAUSE POINT YYYY-MM-DD <session-tag> — <one-line summary>

<brief description of what was paused, what's next>

Co-Authored-By: <as appropriate>
```

After the commit, edit the session-log entry's HEAD line to fill in the actual commit SHA (the one piece that wasn't known until commit-time). Optionally commit that as a follow-up "set HEAD pointer" or include it via amend if the user prefers.

`git push` if the repo has a remote. If push fails (auth, conflict, no remote), surface the failure to the user — don't fail silently. The point of the skill is *durable* persistence; an unpushed commit on a single machine fails the durability test.

## Confirmation before completion

Before declaring the handoff complete, confirm with the user:
- "Session log entry written + status file updated + commit pushed. Anything else you want captured before clearing?"

This is the user's last chance to add something before context goes away. Don't skip it.

## What this skill is NOT

- **Not a substitute for normal commits.** Use this only for paused-mid-flow handoffs. Routine commits should stay routine.
- **Not a CI/CD trigger.** This skill writes prose for human resume; it doesn't kick off automated runs.
- **Not a mode switch.** If the user is also switching output modes (e.g. CLI ↔ Telegram), capture that as a separate "Output-mode state at pause" line in the session-log entry but don't try to do the actual mode switch as part of this skill.

## Project conventions this skill respects

- **sthapati / forge-on-forge:** uses `forge/session-logs.md` as the append-only narrative record; `forge/state/priorities.md` for status. Pause entries are 🔴 PAUSE POINT under a `### YYYY-MM-DD` heading. Pre-existing pause entries (2026-04-24, 2026-04-30 + 2026-05-01) are the reference shape.
- **Other projects:** discover via the search order in step 1. If conventions are unfamiliar, ask the user before writing — never invent a new structure on first use.
