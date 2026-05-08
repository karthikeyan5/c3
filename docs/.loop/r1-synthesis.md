# Round 1 Review Synthesis

Three reviewers ran in parallel: architecture coherence, implementation feasibility, UX/operational. All three say "yes with fixes" — none flag a structural problem requiring a rewrite. Foundation plan (Phases 1-2) is unaffected and immediately actionable per all three. Phase 3+ has identified work to do before code lands.

## Cross-cutting consensus (caught by 2+ reviewers)

1. **Type drift** (arch + feasibility) — `Inbound` vs `InboundEvent` vs `*Inbound`; `Host`, `plugin.Host`, `ReplyArgs`, `EditArgs`, `Sender`, `Attachment`, `ReplyContext`, `VoicePayload`, `Mapping`, `Session` all referenced but never given Go type definitions. Doc surface needs concrete struct definitions.
2. **IPC ops as Go structs, not prose** (arch + feasibility) — §4.4's IPC table is prose; replace with concrete structs + JSON tags.
3. **Codex diagram still labels Python** (arch) — header says Go end-to-end, diagram still shows "(Python POC) shim → supervisor → stub.py". Fix.
4. **Tool prefix divergence** (arch + UX) — Claude has `attach`/`topics`; Codex has `c3_attach`/`c3_topics`/etc. Pick one.
5. **Claim lifecycle undefined** (arch + UX) — release on disconnect? broker restart? in-flight inbound during release? No release/detach op exists; only path is killing the holder.
6. **Broker singleton/spawn unspecified** (arch + feasibility) — flock path, spawn race, stale-lock recovery not in the spec.
7. **Shared-root guard regression** (UX) — `mvp/stub.py:infer_topic_name` returns `None` for `~/arogara`; v5 falls back to `basename(cwd)`, recreating the silent-topic-creation bug v5 was supposed to fix.
8. **Multi-user socket safety** (feasibility) — `/tmp/c3.sock` not per-uid; should be `$XDG_RUNTIME_DIR/c3.sock` or `/tmp/c3-$UID.sock`.

## Phase 6 blocker

**`modelcontextprotocol/go-sdk` v1.6.0 has no public API to send arbitrary custom JSON-RPC notifications.** The Claude adapter's `notifications/claude/channel` cannot be sent through the SDK as-is. Three escape paths:
- (a) Upstream PR adding `ServerSession.Notify(method, params)`.
- (b) Bypass SDK for inbound; manually frame JSON-RPC on the same stdio fd. Writer mutex required.
- (c) Use `notifications/message` instead — loses rich `<channel>` rendering on Claude Code.

**Decision needed before Phase 6 starts.** Doesn't block Phases 1-5.

**My pick: (b) — manual framing.** Pragmatic, preserves UX, no upstream dependency. The MCP stdio protocol IS just newline-JSON over stdin/stdout; we can write our own bytes alongside the SDK's writes if we own the writer mutex. Document this as a spec-level decision.

## Single-reviewer findings (also valid)

**Architecture only:**
- General topic id encoding ambiguous (`*int64(1)` vs `nil`).
- `no_mapping` field in §5.2 missing from §4.4 hello_ack contract.
- `priority` field referenced in §8 but missing from §4.3 plugin schema.
- Confirmation expiry — stateless or stateful? Spec mixed.
- Codex thread discovery tiebreaker undefined.
- Plugin debounce-vs-OnInbound ordering ambiguous.
- `edit_progress` placeholder "session" undefined; no broker-restart recovery.
- Env-var contract for Codex bridge scattered, no consolidated table.

**Feasibility only:**
- `gotgbot/v2` pinned at rc.34 — pin exact version in `go.mod`.
- Atomic rewrite recipe in plan but not spec (hoist).
- Origin header construction explicit for `gorilla/websocket` (default may send Origin in some versions).
- `validate_topic` via `sendChatAction` causes visible typing indicator — call out trade.
- Debounce buffer overflow policy.
- Typing indicator counter on stub crash.
- mappings.json corruption recovery on boot.
- `go.mod` Go floor pin (≥1.23 safe minimum).
- `go.sum` pinning strategy.
- NVM walk doesn't address Volta, fnm, asdf, Homebrew, system npm.
- `/c3-build` not stating GOBIN PATH requirement.
- Concurrent broker spawn race retry budget.
- Disconnect/reconnect ordering vs mutated mappings.json (claim-token versioning).
- Telegram rate-limit specifics (`createForumTopic` ~20/min observed).
- Forum topic name collision (`topic-412` from validate-by-id can later collide with a real `topic-412`).
- NVM symlink overwrite detection (readlink check).
- Broker → adapter writer mutex (interleaved frames).
- `os.UserHomeDir()` instead of `$HOME` getenv in plan task 2.10.

**UX only:**
- Inferred name not echoed in IPC `proposal.name`.
- No `release` / detach op.
- Cooldown-fallback reply (§5.7 says "kept" but no duration/text/dedup).
- `/c3-build` first-run timing hint (Go deps download).
- Broker-then-`/c3-setup` race: broker started before config, no config-reload.
- NVM upgrade re-breaks symlinks.
- System-npm Codex install path not handled.
- `/c3-setup` token-storage transparency in prompt copy.
- §5.2 step 9 proposal wording — agent has to translate "yes" to `attach(create=true)`. Spell the call inline.
- §5.3 cross-group disambiguation — surface as numbered options with explicit tool-call shapes.
- `attach dm` no-mapping caveat in USAGE.md.
- Multi-group default — USAGE.md hardcodes `main`.
- `topics` output format example.
- `c3_reply` recovery from broker claim is Codex-only — Claude needs equivalent or doc the asymmetry.
- §5.6 doesn't tell user how to release the holder.
- Log paths uncertain (USAGE.md cites paths spec doesn't promise).
- No `c3-broker status` / `doctor` subcommand.
- `mappings.json` corruption — `c3-broker validate <path>` for pre-save sanity.
- Stale-topic-on-Telegram detection (deleted from phone).
- Bot token rotation procedure undocumented.
- Plugin failures invisible (whisper failure silent-degrades to `(voice message)`).
- Stale-pid check on `/tmp/c3-codex-app-server.json`.

## Action plan (this iteration)

Apply the cross-cutting consensus + critical single-reviewer items in one focused revision pass. Lower-priority polish (bot token rotation, NVM walk extension, plugin status visibility, log path standardization) folds in alongside without architectural change.

Then spawn round 2 with three fresh reviewers on the revised spec. If round 2 lands at "yes" with no new structural issues, do round 3. If round 3 also clean, lock spec and start build phase 1.

Target: spec revision in this turn, round 2 spawned at end of turn. State file tracks progress.
