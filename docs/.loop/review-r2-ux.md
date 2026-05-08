# C3 Re-arch — UX & Operational Review (r2, fresh context)

Reviewer: independent fresh-context pass. No prior reviews consulted.
Scope: spec `2026-05-08-c3-rearch-design.md` + `USAGE.md` / `INSTALL.md` / `PLUGINS.md` / `CHANNELS.md` / `ADAPTERS.md`, against POC features documented in `mvp/README.md`.

## Verdict

**Yes, with these gaps.** The spec materially exceeds the POC on attach UX (proposal flow, multi-group, validated `--topic=N`), introduces real ergonomic wins (typing, `edit_progress`, debounce as a first-class config), and harmonizes tool names across CLIs. But several POC features are silently dropped or under-specified, the install path has friction the POC didn't (build-from-source + restart loop), and several recovery hooks are described in prose but not surfaced as user commands.

## Regressions vs POC

- **Allowlist / access-control plane is gone.** POC ships `approve_group.py` + `~/.claude/channels/telegram/access.json` (silently drops untrusted groups). Spec mentions `master_user_id` is "plumbed, enforcement deferred" and the entire access-control feature is under "deferred". Net effect: every member of any configured supergroup can drive the CLI in v1. POC was stricter. USAGE.md (lines 142–143) documents this drop but treats it as expected.
- **`rename_topic.py` has no replacement.** POC lets the user rename a topic locally without renaming on Telegram (useful for the `topic-0`→`general` case and after `attach --topic=N` storing the placeholder `topic-N`). Spec stores `topic-N` as a placeholder name on validation and tells the user to "rename later" — but there is no `c3-broker rename-topic` subcommand listed in §4.5.2. The user is left to hand-edit `mappings.json`.
- **Reactions / ack-emoji on inbound is dropped.** POC ships an "ack reaction" feature (the bot reacts to received messages so the phone shows it landed). Spec exposes a `react` outbound tool but the auto-ack-on-inbound behavior isn't reaffirmed. Telegram users will notice.
- **Chunked outbound** for >4096-char replies: POC handles it; spec's `SendReply` interface doesn't say. Likely an implementation detail but worth pinning down before users hit the silent truncation.
- **Group-mention triggering** is listed in POC's full feature set; spec is silent.
- **Stub-level auto-attach (zero tool calls)** — the project-root `CLAUDE.md` describes the POC stub walking up to find `CLAUDE.md` and pre-attaching before Claude is handed the prompt. Spec's flow has the adapter `hello` then `instructions` say "auto-attached" — same end state for the user, but the adapter has to do this work in Claude Code's hello-instructions cycle. As long as `instructions` is rendered to the user clearly, fine; if it isn't, the user gets a silent-claim with no confirmation message.
- **Message-history preservation across broker restarts** — POC's claim is in-memory too, but Karthi's mvp uses `topics.json` on disk as a registry survivor. Spec keeps topics in `mappings.json` (good), but losing live-claims on broker restart with no surfacing means a `pkill c3-broker` mid-conversation will silently swallow the next inbound until the user re-`attach`es. The cooldown-fallback reply (5 min) is the only signal; that's coarse.

## Install friction

- **Three-step build dance.** Install plugin → `/c3-build` (compile) → `/c3-setup` → restart. Each step has a failure mode: `go` not on PATH, `$GOBIN` not on PATH, `mappings.json` written after broker started (spec acknowledges this with `c3-broker reload-config`, but the user-facing instruction is "restart your Claude Code session" — INSTALL.md step 3). For a new user, "restart your CLI" mid-onboarding is a hard cliff.
- **`/c3-setup` failure path unclear.** If the user enters a wrong bot token, the slash command writes `mappings.json` with the bad token, broker fails `getMe`, and the user sees… what? INSTALL.md troubleshooting says "try `curl …/getMe`". That's a reasonable check but should be done by `/c3-setup` itself before writing the file.
- **Codex side requires Claude side first.** INSTALL step 4 + spec §5.1 both assume `~/.config/c3/mappings.json` already exists from `/c3-setup`. A Codex-only user has to either install Claude Code first or run `c3-broker install-codex-shim` which "interactively gathers" the same fields. Spec hand-waves this; INSTALL.md doesn't actually walk the Codex-only flow.
- **NVM-symlink installer is destructive in a quiet way.** `install-codex-shim` walks every `~/.nvm/versions/node/*/bin/` and writes a `codex` symlink. INSTALL.md uninstall section warns "CAUTION: only if they pointed at the C3 launcher" — but the installer doesn't capture or print which dirs it modified for later cleanup. A user upgrading `node` post-install will silently get a real-codex back in the new version's bin dir; the launcher won't be re-installed there until they re-run the shim.
- **Go ≥1.22 prereq** is fine but no version-check in `/c3-build` is specified. A user with `go1.20` will see a confusing `go: unknown directive` mid-compile.
- **`migrate-legacy`** does not import `mvp/topics.json`, by design ("start clean"). For Karthi this means re-attaching every project on his machine the day of the cutover. Worth surfacing in INSTALL "Migrating" section more loudly than the current single sentence.

## Daily-use rough edges

- **`attach` proposal is two round-trips.** Spec §5.2: the agent surfaces the proposal text, user says "yes", agent calls `attach(create=true)`. In Claude Code with auto-accept off, that's at least one extra prompt approval per fresh project. Compared to the POC's `c3-attach` launcher which created on first run with no confirmation, this is friction Karthi will feel. The trade-off (no silent topic creation) is right; the docs should set expectations.
- **The `attach` command in USAGE.md isn't actually a command** — it's a tool the agent calls. A user typing literally `attach` at the terminal will get whatever Claude does with the word. USAGE line 28 says "Type it (or have your CLI agent type it)" — should clarify that the natural phrasing is "attach to this project" and Claude's agent will translate.
- **Disambiguation when the same topic name exists in 2+ non-default groups.** Spec §5.3 only handles "found in one other group". If `feature-x` exists in both `work` and `personal` non-default groups, the proposal struct supports `Existing` + `Alternative` but not a list — the user can't pick from three.
- **`attach dm` doesn't persist.** Fine, but combined with "DM is universal", a Codex session in a project dir that ran `attach dm` and then crashes will, on next start, auto-attach to the project mapping (not DM). User has to manually `attach dm` every session. POC has the same issue; spec inherits it.
- **`release` is a broker subcommand AND an MCP op AND mentioned as "broker's release tool"** (USAGE.md line 98). USAGE doesn't tell the user how to invoke it from inside Claude Code — is there a `release` MCP tool exposed to the agent? Spec lists `OpRelease` in IPC but no corresponding MCP tool in §4.4.2's tool list. So a user who wants to hand off Claude→Codex on the same project has to `pkill claude` or `c3-broker release <cwd>` from another shell.
- **`edit_progress` placeholder lifecycle ambiguity.** Spec §4.5 says placeholder is per `(claim_route)`; on stub release the entry clears but the Telegram message stays. If the same stub re-attaches mid-turn, it gets a fresh placeholder — old one orphaned in the topic. Acceptable, but cumulative orphans across many releases will clutter the topic. No GC hook described.
- **Debounce window is a single per-channel knob** (`debounce_ms`). POC same. Spec calls out per-group override "later" — fine, but a user who wants quiet-debounce on a high-traffic topic and snappy on a low-traffic one has no v1 lever.

## Cross-CLI consistency issues

- **USAGE.md still uses `c3_attach`/`c3_topics`/`c3_reply` in places** (lines 98, 100, 123) while spec §4.4.2 says drop the `c3_` prefix on Codex-side tools. Either the doc is stale or the rename hasn't been applied consistently. This will confuse users typing literal tool names.
- **`codex_forward` keeps its prefix** (spec line 520) while every other Codex tool drops it. Defensible (Codex-specific debug tool) but inconsistent with the "namespace via MCP server name" rule.
- **`inbox` is Codex-only.** Claude users get rich `<channel>` rendering and have no inbox; Codex users have inbox + WS forwarding. If a future CLI adapter has neither, the `inbox` fallback should arguably become universal. Spec mentions "inbox" in `Capabilities` (§4.4.1 Hello.Capabilities) so the broker can pick the cheapest path — good architecture, but USAGE/ADAPTERS docs don't reflect that the broker may serve inbox to any adapter that asks.
- **`instructions` text is adapter-local** (per ADAPTERS.md "wording differs by CLI"). Two adapters' phrasing of the same proposal will drift over time. No spec-level test or canonical-string contract enforces parity.

## Operational gaps

- **No `/c3-status` slash command in spec body.** §12 phase 9 mentions it as a deliverable, but §4.5.2 only documents the `c3-broker status` CLI subcommand. The user has to drop to a shell — and from inside Claude Code that's a Bash tool call away, but a slash command would be nicer. Minor.
- **Logs scattered across `/tmp/`.** USAGE.md "Health checks" lists 5 paths: `/tmp/c3.sock`, `/tmp/c3-broker.pid`, `/tmp/c3-broker.log`, `/tmp/c3-codex-supervisor.log`, `/tmp/c3-codex-app-server.json`. Spec §4.2.2 standardizes the broker pid/socket on `$XDG_RUNTIME_DIR` with `/tmp/c3-$UID.sock` fallback — but USAGE.md still says bare `/tmp/c3.sock`. The codex-supervisor and app-server log paths are bare-`/tmp/` and don't include `$UID` — multi-user collisions on shared hosts.
- **No log-level control documented.** No env var (`C3_LOG_LEVEL`?), no slash command, no spec mention. When Karthi files a bug, "send me debug logs" has no defined answer.
- **`c3-broker status` shows liveness + claims + plugin enabled-states + getMe** — solid. But it doesn't show: last-inbound-time per route, fallback-cooldown state, debounce buffer depth, in-flight tool-call counters. "Why is my message not arriving?" diagnosis stops at "broker is up, claim is held" — the next layer of why isn't surfaced.
- **No `c3-broker tail` or equivalent live-event view.** Karthi will end up `tail -f /tmp/c3-broker.log` which works but leaves him guessing at log format.
- **Broker restart loses live placeholders, debounce buffers, and the typing-ticker state.** Spec acknowledges placeholder loss; the other two are silent. A user mid-burst-of-messages who triggers a broker restart will lose the buffered debounce window — possibly seeing only the last message land in the CLI.
- **`mappings.json` is hand-editable AND auto-rewritten by the broker.** Spec §4.3 says "atomic rewrite" — good. But if the user edits while the broker is mid-write, the user's save races and the broker's atomic-rename wins. No file-watcher for hand-edits is described; USAGE.md (line 108) says "the broker's next read will pick up the change" — but `reload-config` is the only mechanism, and the user has to know to call it. Otherwise the change applies on next broker boot.
- **No backup of mappings.json before atomic rewrite.** A bug in the broker that writes corrupt JSON would clobber the only copy. Spec says broker validates on read; doesn't say it validates before write.

## Recovery paths

- **Stale broker (crashed without releasing flock):** spec §4.2.2 step 3 handles via pid liveness check — good.
- **Stale socket file:** broker unlinks before binding — good.
- **Deleted topic on Telegram with stale `mappings.json` entry:** USAGE line 115 says "remove from mappings.json by hand or run cleanup tool if it exists by the time you read this". No cleanup tool is in spec. User edits JSON manually, hopes for the best.
- **Corrupted `mappings.json`:** spec says broker refuses to start, prints parse error. USAGE line 111 says "restore from your backup or fix the syntax." No backup is created automatically. If the user has no backup (most users), they're rebuilding by hand from `c3-broker setup`.
- **NVM upgrade clobbers codex symlink:** documented in INSTALL line 154 — re-run `install-codex-shim`. But there's no way for the user to know this happened until `which codex` shows the wrong path. No proactive check.
- **Topic claimed by dead pid (zombie claim):** USAGE line 120 says "restart the broker (`pkill c3-broker`)". That's heavy-handed — kills every other session's claim too. Spec's `c3-broker release <cwd>` is finer-grained but USAGE doesn't mention it for this case.
- **Bot token rotation:** no documented flow. User edits `mappings.json`, calls `c3-broker reload-config`. Should be one-line in USAGE.
- **Forum topic renamed on Telegram side** (`forum_topic_edited`): spec §10 explicitly says "plumbed-but-inert in update parsing" — meaning the user's local `name` field in mappings.json drifts silently. They renamed `c3` to `c3-old` on phone; routing still works (topic_id is source of truth), but `topics` listing shows the stale name.

## Recommendation

**Iterate UX, then ops.** The architecture is sound and the additions (proposal flow, multi-group, harmonized tools, typing/edit_progress/debounce) genuinely improve on the POC. But:

1. Reconcile USAGE.md's `c3_attach`/`c3_reply`/`c3_topics` references with spec §4.4.2's prefix-drop rule before users see either doc.
2. Add a `release` MCP tool to the adapter tool surface (or document explicitly that "release" is shell-only). The cross-CLI handoff story in USAGE depends on it.
3. Decide on `topic-N` rename: either a `c3-broker rename-topic` subcommand or a documented hand-edit ritual.
4. Strengthen `/c3-setup` to validate the bot token via `getMe` before writing `mappings.json`.
5. Tighten log paths to `$XDG_RUNTIME_DIR` (or at minimum `/tmp/c3-$UID-*`) for codex-supervisor and app-server scratch files.
6. Add a `c3-broker doctor` (or fold into `status`) that surfaces last-inbound-time per route, debounce depth, fallback cooldown timers — the second-level diagnostic info.
7. Auto-snapshot `mappings.json` to `mappings.json.bak` before each atomic rewrite. Cheap insurance.

After those, the spec's UX comfortably exceeds the POC. None of the gaps are blockers; all are polish before declaring v0.1.0 user-facing.
