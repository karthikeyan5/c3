# C3 Rearch Spec — UX & Operational Review (R3, fresh-context)

**Verdict:** **Ready with these UX/doc fixes.** The user-facing contract is sound and matches-or-exceeds the POC. Two small command-name drifts and one stale plan reference need patching before lock; nothing is a redesign.

## Lock-blocking UX issues

1. **Foundation plan contradicts spec on Codex Go-rewrite.** `docs/plans/2026-05-08-c3-v3-foundation.md` line 1783 still says *"Codex bridge integration — no rewrite. Move the existing Python POC… Do NOT rewrite this in Go."* The spec (v5, §11.D, §4.4 Codex bridge env-var contract, Phase 8) reverses that and is explicit: *"No Python in the Codex bridge."* Whichever survives, the other must change before lock — otherwise the first implementer reads a contradictory directive on day one. Recommend keeping spec-v5 (Go) and revising the foundation plan's Phase 8 paragraph.

2. **`mappings.json` migration of legacy `topics.json` is silently dropped.** INSTALL.md says *"Existing topic registrations from the old `mvp/topics.json` are NOT migrated — by design, you'll re-attach as you go."* That's defensible, but there's no mention in USAGE.md or the spec §9 of what happens when the user, post-migration, runs `attach` from a project dir that previously had a working topic. They will see a "create new topic?" proposal even though a usable topic exists in Telegram. The **lock-blocker** is documentation, not behavior: USAGE.md "Migrating from the Python MVP" doesn't exist (it lives only in INSTALL.md), and the user expectation isn't set that legacy topics survive but need re-attach-by-id (`attach --topic=<n>`). Add a one-paragraph "first day after migrate-legacy" section to USAGE.md or INSTALL.md telling the user how to re-bind a legacy topic without recreating it.

## Tighten-up items

- **Tool name drift between USAGE.md and spec §4.4.2.** USAGE.md uses bare `attach`, `topics`, `reply`. Spec §4.4.2 ("Drop the `c3_` prefix") agrees. ADAPTERS.md still says *"`attach` (or `c3_attach`, `c3_codex_attach`, etc.)"* and *"`c3_inbox(limit, ack)`"* — the latter contradicts §4.4.2's harmonized `inbox`. Pick one and propagate. Recommend deleting all `c3_` references from ADAPTERS.md.

- **Ops paths not fully unified to `$XDG_RUNTIME_DIR`.** Spec §4.2.2 mandates `$XDG_RUNTIME_DIR/c3.sock` with `/tmp/c3-$UID.sock` fallback and "never bare `/tmp/c3.sock`". USAGE.md "Health checks" section still tells the user to `ls -la /tmp/c3.sock` and `pkill c3-broker; rm /tmp/c3.sock`. INSTALL.md "Uninstalling" likewise. This will confuse a user on a multi-user machine. Replace with the resolved path or document both.

- **No `c3-broker status` walkthrough in USAGE.md.** Spec §4.5.2 promises a useful read-only status subcommand (broker liveness, claimed topics, holders, plugin states). USAGE.md mentions it once in passing under "Health checks" but doesn't show example output. A 6-line example block would dramatically reduce the "is it actually working?" anxiety on first install.

- **`c3-broker reload-config` is undocumented user-side.** Spec §4.5.2 lists it as a way to handle the "/c3-setup race" without restarting. USAGE.md "Editing mappings.json by hand" tells the user to restart instead. Mention `c3-broker reload-config` as the soft-reload path.

- **`/c3-setup` failure path.** INSTALL.md Step 3 doesn't say what happens if the user enters a bad token. Spec §4.5.2 says `c3-broker setup` validates via `getMe` BEFORE writing — good — but the failure UX (error message, retry prompt, partial write avoidance) is not described in INSTALL.md. Add one line: "If the token is rejected by Telegram, `/c3-setup` reports the error and writes nothing; re-run after correcting."

- **Cross-CLI release ergonomics.** USAGE.md §"Cross-CLI on the same project" tells the user to `c3-broker release ~/arogara/sthapati`. That's good; but the example in §"When things go wrong" tells the user to `pkill c3-broker; rm /tmp/c3.sock /tmp/c3-broker.pid` which is the nuclear option. Reorder so `release <cwd>` is presented first; nuclear reset second.

- **DM auto-attach is invisible.** Spec §5.5: "`attach dm` does NOT update mappings.mappings — DM is universal." USAGE.md confirms this. But there's no documented way for the agent to know "I should default to DM here" — `attach` from an unmapped dir always proposes a topic, never DM. Clarify in USAGE.md: DM is opt-in per session via the explicit `attach dm` command.

- **STT prereq surprise.** INSTALL.md Step 5 says voice "should arrive after a couple of seconds" — but Step 1-4 never told the user to `pip install openai-whisper`. Spec §6.2 mentions the prereq; Troubleshooting line 178 mentions it. Move it up into a "Prerequisites" bullet for visibility.

## Recommendation

**Proceed to build** after the two lock-blockers are fixed (foundation plan Phase 8 paragraph + USAGE/INSTALL doc on legacy-topic re-attach). The tighten-up items are doc polish that can land in the same commit batch as the lock and don't require iterating the design. The UX contract itself — single-broker, claim-per-route, proposal-before-create, validate-by-id, harmonized tool names, one config file — is coherent, matches the POC's proven behavior, and adds genuinely useful affordances (`status`, `release`, `reload-config`, `validate`, `edit_progress`, typing). Lock it.
