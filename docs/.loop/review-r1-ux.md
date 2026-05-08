# C3 v5 — UX & Operational Review (r1)

## Verdict

**Yes with these gaps.** v5 ships POC parity (auto-attach, proposal, voice, multi-CLI, NVM symlinks) plus the promised wins (debounce, typing, edit_progress, multi-group, attach-by-id). But several operational/recovery paths the POC handles implicitly aren't pinned down, and tool-name/wording choices will confuse users mid-flow. Fix before release; nothing is a showstopper.

## Regressions vs POC

- **Shared-root guard missing.** `mvp/stub.py:infer_topic_name` returns `None` when the nearest `CLAUDE.md` is `~/arogara` itself; v5 §4.4/§5.1 fall back to `basename(cwd)` — exactly the silent-topic-creation mode §3 swears off. Carry the guard into broker + Codex launcher.
- **Inferred name not echoed in IPC.** POC seeds `C3_ATTACH_NAME` from cwd. v5 has broker doing inference but the contract doesn't say the name lands in `hello_ack` / `attached.proposal`. Pin `proposal.name`.
- **No `release` / detach op.** POC ships `mvp/c3-attach` for shell-level release. v5 broker has none — taking over a claim means killing the holder (§5.6).
- **Cooldown-fallback reply unspecified.** §5.7 says "kept" but no duration, text, or dedup key. Easy to drop.

## Install friction

- **`/c3-build` first run pulls hundreds of MB of Go deps** with no progress — looks hung on slow networks. Print "may take 2-3 min on first run".
- **`$GOBIN` PATH advice buried.** Have `/c3-build` warn if `$GOBIN` isn't on `$PATH`, with the rc-line to add.
- **Broker-then-`/c3-setup` race.** Adapter spawns broker before `/c3-setup` runs, so a broker is alive holding empty config when the real one is written. Nothing says it re-reads. SIGHUP from `/c3-setup`, or document that "restart your session" means `pkill c3-broker` too.
- **NVM upgrade re-breaks the bridge.** "Required" symlinks correctly stressed, but `nvm install <new>` silently breaks them. Call out in Upgrading.
- **System-`npm` Codex install path missing.** v5's fallback glob is NVM-only; `npm -g` users won't be found.
- **`/c3-setup` question copy undefined.** Pasting a bot token is sensitive — prompt should say "stored mode 600 in ~/.config/c3/mappings.json".

## Daily-use rough edges

- **§5.2 step 9 proposal wording ambiguous.** "Reply yes to create" — agent has to translate to `attach(create=true)`. Spell the call inline.
- **§5.3 cross-group disambiguation** packs two distinct tool-call shapes into one sentence. Surface as numbered options with explicit calls.
- **`attach dm` no-mapping** is right per §5.5 but USAGE.md doesn't warn that tomorrow's session in the same dir won't auto-attach to DM.
- **Multi-group default implicit.** USAGE.md uses `main` as if hardcoded. Say "default is `channels.telegram.default_group`".
- **`topics` output format unspecified** — no example row, agents paraphrase inconsistently.

## Cross-CLI consistency issues

- **Tool name divergence.** Claude: `attach`, `topics`. Codex: `c3_attach`, `c3_topics`, `c3_inbox`, `c3_reply`, `c3_codex_forward`. Switching forces users to recall the prefix. Prefix both, or accept aliases.
- **`c3_inbox` poll vs Claude push asymmetry.** Codex falls back to polling when WS forwarding is down; Claude gets push. USAGE.md doesn't flag this — add a delivery-mode note for Codex.
- **`c3_reply` recovery from broker claim is Codex-only.** Claude adapter has no documented equivalent. Symmetric, or document the asymmetry.
- **§5.6** tells the user to attach elsewhere or wait, not how to release the holder. Without a release op, only path is `/exit` the holder. State so.

## Operational gaps

- **Log paths uncertain.** USAGE.md cites `/tmp/c3-broker.log` and `/tmp/c3-codex-supervisor.log`; spec promises neither. Pin down so users can `tail`.
- **No `c3-broker status` / `doctor`.** POC users learned `pkill c3-broker; rm /tmp/c3.sock` as folk remedy. Ship `status` (sock, pid, mappings parses, getMe reachable) before v0.1. `/c3-status` is in Phase 9 but contents undefined — at minimum: broker alive, config valid, channel reachable, claimed topics, plugin states.
- **Broker mid-session death.** ADAPTERS.md says "reconnect once"; if the broker process is gone, the adapter must respawn via flock. State in spec.
- **`mappings.json` corruption.** USAGE.md says broker "prints" the parse error — where? Provide `c3-broker validate <path>` for pre-save sanity.
- **Stale topic on Telegram** (deleted from phone): replies hit 4xx. Detect, mark mapping stale, surface on next attach.
- **Bot token rotation** — edit + restart broker; undocumented.
- **Plugin failures invisible.** Whisper failures silently degrade to `(voice message)`. Add a log line and `c3-broker plugin status`.
- **`/tmp/c3-codex-app-server.json` stale-pid check.** Metadata persists across reboot — verify the recorded pid is alive before reusing the port.

## Recommendation

**Iterate UX, then ship.** Design is sound; gaps above fix in spec/docs without architectural change. Highest priority before implementation:

1. Shared-root (`~/arogara`) guard in topic inference (broker + Codex launcher).
2. Specify cooldown-fallback reply (text, dedup key, duration).
3. Harmonize adapter tool names or document the divergence prominently.
4. Add `c3-broker status` / `validate` / `release` subcommands to Phase 9.
5. Tighten proposal/disambiguation wording with explicit tool-call shapes.
6. Document log paths, `/c3-setup` restart semantics, corruption recovery.

After those, v5 delivers POC parity + promised additions, and a fresh user has a fighting chance at INSTALL.md unaided.
