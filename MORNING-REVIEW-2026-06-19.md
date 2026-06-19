# Morning review — 2026-06-19 (overnight autonomous run)

Two features built overnight per Karthi's "complete everything and keep". **Nothing pushed, nothing merged** — both branches held for your ratify + merge. A terminal crash mid-run was recovered cleanly (only a re-runnable subagent was lost; all committed work + durable state survived).

Legend: **DECIDE** = needs your call before merge/next step · **FYI** = done, just so you know.

---

## Feature 1 — Connectivity notifications  ✅ COMPLETE (held)
Branch `feat/connectivity-notifications` (9 commits off `origin/master` 51b7076, HEAD `2b4dc0b`).

What it does: desktop popup is now the PRIMARY outage alert; the CLI turn-injection is a FALLBACK that fires only when the popup didn't deliver (and says so); a new ambient **status-line indicator** shows "⚠ TG offline HH:MM" and auto-clears on reconnect; a `notifications.invasive` toggle (default true) silences the invasive surfaces while keeping the status line.

Process: brainstorm → spec (3 persona reviews + revisions) → plan → 7 TDD tasks each via subagent + independent per-task review → final triple review (code/concurrency/security) → fixed 2 important concurrency bugs the per-task reviews couldn't see (status line could stick red after recovery; stale advisory after recovery) → fix re-reviewed clean. Security review: clean (no token leak, correct perms, no injection).

Your `~/.claude` dotfiles were edited (approved): status-line segment + `refreshInterval:5`, backups at `*.bak`. Verified live + injection-safe.

- **DECIDE** — review the spec (`docs/superpowers/specs/2026-06-18-c3-connectivity-notifications-design.md`) + the branch, then merge. To see it live the broker must be rebuilt/restarted (currently held until the proxy deploy — your call whether to fold this in).

## Feature 2 — Rich text  ◑ converter bug FIXED (held); guidance reframing is YOUR call
Branch `feat/richtext-formatting` (3 commits off 51b7076, HEAD `6c80728`).

Why agents don't use C3's rich text = two causes:
1. **A real converter bug (FIXED).** Mixed `**`/`__` (or `*`/`_`) spellings made the converter emit Telegram-illegal same-type nested tags → 400 → ALL formatting silently stripped to plaintext. That alone trains agents to stop formatting. Fixed: same-type spans now collapse, different-type nesting preserved, `***x***`→bold-italic; added a property-test guard. Plus stale-doc-comment cleanup. Reviewed clean, no regressions.
2. **Guidance is permissive + buried + out-prioritized by your plain-prose preference (DECIDE).** The capability is stated ("Rich text: YES") but never *encouraged*; the `reply` tool description (the compose-time surface) never mentions formatting; and your `feedback_telegram_mode.md` memory explicitly says PLAIN PROSE — the dominant signal. I did NOT touch this — it directly contradicts "agents should format", so it's your policy call.

- **DECIDE** — the formatting policy. Proposed: *conversational replies stay plain prose; use rich formatting only for genuinely structural content (code blocks, multi-item lists/tables, quoted blocks, links).* If you approve a policy, the wording edits are small & known (reframe guidance.go line, add a nudge to the `reply` tool description in both adapters, reorder Combined(), add one worked example, carve-out the plain-prose memory).

## Deferred (NOT pulled forward — already your-call / non-trivial)
- **rich_message inbound decode shim** (forwarded rich messages arrive as empty text). Classified buildable/pre-approved with a design in `docs/specs/2026-06-16...`, but I deferred it (unattended + wanted your confirm it's truly mechanical). **DECIDE** — want me to build it next?
- Bot-API features still parked per ROADMAP (albums, echo-by-file_id, underline, mention, forwarding, location, link-preview, partial-quote): non-trivial / your-call.
- Streaming via `sendRichMessageDraft`: large + architecture decision.
- Wide-table→image guidance reachability now that native tables are on.

## Security FYI
- `~/.claude/settings.json` permissions allowlist contains a **live OpenRouter API key in plaintext** (a `Bash(printf 'OPENROUTER_API_KEY=…')` allow-rule). Outside the repo, so not a push risk — but consider rotating/removing it. (Possibly related to the ROADMAP "OPENROUTER_API_KEY missing where the STT handler reads" item.)
- PII audit on the c3 repo: gitleaks clean (working tree + full history); proxy keep-out values (subdomains/IP/project/region) have zero hits in repo or history.

## Recovery note
Terminal crashed mid-run while the rich-text converter subagent was working; it had committed nothing. Recovered: stashed its 27-line partial test (`git stash@{0}`, untrusted, recoverable), re-ran fresh. Connectivity work was already committed and intact. All `.git/sdd/` durable state (ledger, briefs, reports, review packages) survived.
