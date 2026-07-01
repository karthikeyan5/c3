# C3 — Product Roadmap

C3 is a Go end-to-end Telegram multiplexer for multiple Claude Code / Codex CLI sessions — one broker daemon, per-CLI MCP adapters, topic-based routing.

**This is the forward-only roadmap: what's left to finish and what's still to build.** Shipped history — every dated "Shipped" log, done-lists, doc fixes, corrections, superseded designs — now lives in [`docs/build-log.md`](docs/build-log.md). Decisions waiting on Karthi are collected at the bottom.

---

## Partially built — needs completion

### Telegram Q&A round-trip (`ask`)
- **`ask` live-verify** — *high (headline).* Unit-tested + race-clean; needs the real end-to-end on the new binary in a live Telegram-mode session: tap a button on the phone → the choice returns to Claude. Gates trust in the whole feature. Spec: `docs/superpowers/specs/2026-06-26-c3-telegram-ask-roundtrip-design.md`.
- **`ask` free-text / "Other" / comment answers** — *medium.* Single/multi-select + Skip shipped. Remaining: the typed-answer paths (`free_text`, `allow_other`, `allow_comment`) that intercept the durable-queue text path (`flushInbounds`). Small build once the product call below is made (see Decisions: typed-answer queuing). Spec Phase 2.

### Trust & permissions
- **Permission relay live-verify** — *high.* Relay shipped (approve/deny tool prompts over Telegram); the novel piece — the transport-layer interceptor for `notifications/claude/channel/permission_request` — can only be confirmed with the real CC harness firing a permission prompt. Spec: `docs/superpowers/specs/2026-06-26-c3-permission-relay-design.md` ("Testing & the live-verify gate").

### Install & rollout
- **First-run install validation on a fresh machine** — *public-push blocker.* Paste the install one-liner into a fresh CC session, walk `INSTALL.md`, cd into a project, attach, confirm a real Telegram round-trip. Surfaces rough edges before the public push.
- **2nd Bot-API proxy for endpoint-failover** — *low-medium.* Code done + tested (failover engages when `channels.telegram.api_base_urls` has >1 endpoint). Remaining: provision a 2nd maintainer-owned proxy (GCP-Singapore or Oracle-Always-Free) and add it. The primary proxy is live + verified. Karthi deferred 2026-06-22.

### Reliability
- **Auto-attach-to-c3-by-default bug — re-verify** — *unverified.* Sessions default-attach to the c3 topic even when not on c3. Branch `feat/auto-attach-resume` exists (24 ahead, unmerged, unpushed). Remaining: verify the Telegram+CLI notice, the "queue not updating" bug, update the spec, PII audit, then a push/merge decision.

### Access control
- **Per-user access-control enforcement** — who can talk to which CLI. Pairing/allowlist primitives exist; full per-user→per-CLI enforcement is partial. Spec §4.3.

### Codex parity
- **Codex policy 3-state error messaging — confirm wired.** Plan `docs/plans/2026-05-19-codex-policy-3state.md` complete; `AttachStatus` enum + `PolicyRejected` hint landed. Confirm the Codex side is fully wired.

### STT
- **STT multi-provider modularity — close the "how to add a provider" README.** Chain + fallback exist (elevenlabs-scribe-v2 opt-in, gemini-3-flash-openrouter, sarvam-saaras-v3). MORNING-REVIEW notes the how-to README now exists at `plugins/c3/stt/stt-pkg/README.md` — verify and close.

### Broker commands & polish
- **Broker-side slash commands `/list` + `/route`.** `/status` is now answered directly by the broker (`internal/broker/status_command.go`); `/list` and `/route` are not yet built.
- **`c3-broker release <cwd>` runtime IPC op** — *stubbed.* The only genuinely unimplemented user-facing command (`cmd/c3-broker/status.go:153` returns "not yet implemented"); frees an attached topic without restarting the broker. Workaround today is `/exit` the holder.
- **Smoke-test visual tails** — minor live-verify leftovers: expandable show-more visual confirm; inline-buttons callback fresh-message tap.

---

## To build

### Remote CLI control — managed CLI sessions (Thread C/D) — *P1, "build now"*
Spawn-and-own a Claude/Codex CLI and drive it from Telegram or another agent; the broker controls every CLI it started. Hardened design, 0 code. Reshaped by probe C-lite: stream-json driver is primary; PTY is the long tail. Spec: `docs/superpowers/specs/2026-06-26-c3-remote-cli-control-design.md`.
- **Phase 0 (GATE probe, run first):** confirm `claude -p --input-format stream-json --output-format stream-json` stays alive accepting multiple `user` turns across stdin (`--replay-user-messages` as ack). Spec §6, §7.1.
- **Phase 1 (MVP):** `spawn.Managed` helper; `streamJSONDriver` (Inject/Command/Status/Stop); `b.Managed` registry + synthetic claim + worker divert at `flushInbounds`/`flushEvent`; command routing (text→turn, `/cmd`→child incl. `/compact`, route-aware `/status`); render turn-results/`system_init`/`result` back to topic; Thread-D spawn/list/kill via `c3-broker spawn` subcommand + `spawn_cli` MCP tool. Claude-only, stream-json-only, one session, no restart, no persistence.
- **Phase 2:** control buttons (Interrupt/Snapshot/Stop) via `managed:` callback divert; agent-control MCP tools `cli_send`/`cli_command`/`cli_interrupt`/`cli_status`/`cli_list`/`cli_kill`; multiple concurrent sessions; restart-with-backoff; spawn-onto-named-topic-with-create; child permission-relay convergence (soft-dep on the permission relay).
- **Phase 3 (PTY long tail):** `ptyDriver` behind the same interface (`creack/pty` + `Netflix/go-expect` + a VT emulator) for Codex, arbitrary pre-existing TUIs, exact `/status` panel, arrow-key menu nav, snapshot-on-idle.
- **Phase 4 (persistence + reach):** re-attachable transport for restart-survival; local HTTP/JSON API (Thread D Form 3, deferred YAGNI); other channels for Thread D.

### Live broker fix
- **Noisy re-polling / dedup-skip of recent Telegram updates** — *high (live regression, found 2026-06-27).* After the Thread A/B merge the live broker re-polls/dedup-skips recent updates ~3/sec. Not message loss, but needs root-cause + fix.

### Trust & permissions
- **Trusted-operator DM authorization (PreToolUse hook)** — *P1 ("write a spec to solve this").* Spec written; Phase-0 gate RESOLVED (a hook `allow` bypasses the auto-mode classifier, CC v2.1.193). Covers classifier *hard-denies* (no prompt opens) — complementary to the permission relay. Remaining after Karthi's §10 product calls (see Decisions): Phases 1–3 — grant store, `AuthorizeCheck` IPC op, the hook + plugin.json registration, DM grammar (`/authorize`·`/grants`·`/revoke`), audit, Style-B per-action card, hardening. Spec: `docs/specs/2026-06-14-trusted-operator-dm-authorization.md` §9–§10.
- **Permission relay Phase 2 niceties** — *low.* `perm:more:<id>` "See more" expansion; `y/n <request_id>` text fallback (regex `/^\s*(y|yes|n|no)\s+([a-km-z]{5})\s*$/i`); Codex adapter parity.
- **First-class Claude Code "channel trust" signal** — *idea (upstream FR).* Long-term: harness honoring a `meta` attribute marking an allowlisted-owner DM as operator-trusted, removing the hook shim. File as an Anthropic feature request. Trusted-op spec §11.

### Telegram Q&A round-trip (`ask`)
- **`ask` Phase 3 parity/polish** — *low.* Codex adapter `ask` parity; align the generic event frame to include `user`/`user_id`; `/status`-style introspection of open asks.

### Telegram channel completeness
- **Inbound + outbound albums** — *medium (Karthi wants; explicitly parked, not dropped).* Media-group assembly + `sendMediaGroup` (albums descoped to sequential single sends in v1).
- **Echo media by `file_id`** — zero-cost re-send of inbound media; sidesteps the 20MB-download / 50MB-upload caps.
- **Forwarding messages** — outbound forwarding (the empty-forwarded-text *decode* half already shipped via rich-inbound).
- **Underline + inline user-mention** (`tg://user?id=`) formatting.
- **Location sends** (and likely venue/contact).
- **Link-preview control, partial-quote highlighting, `entities[]` path** — same deferred coverage-matrix bucket.
- **Deferred Bot-API 10.x items** — `deleteMessageReaction`, rich HTML tables with borders/spans, `sendRichMessageDraft` streaming, guest mode.
- **getUpdates response-body size cap** — *hardening.* No `io.LimitReader`/cap on the getUpdates body (the read lives inside gotgbot's `RequestWithContext`; `MaxDownloadBytes` only caps media). Minor today (transport-trust); becomes Important if the proxy is ever semi-trusted. Real trade-off — bypassing `RequestWithContext` loses verified byte-parity.
- **Typing-cap redesign** — *deferred.* Current ~60s-then-stops behavior stays until revisited (Karthi 2026-06-16).

### Streaming reasoning/thinking to Telegram
- **Deterministic streaming of reasoning/thinking** — *P1-sequenced (build right after Thread C/D).* Path TBD: (a) reverse the Codex forwarder opt-out → Codex-only streaming (asymmetric), or (b) pivot C3 to host the agent via the SDK/Messages-API → both CLIs (large). Verified: CC exposes no in-flight reasoning to an MCP adapter; manifest reports `StreamViaEdit=false`.

### Channels & platform
- **Programmatic (non-chat) channel extension** — *idea.* Make C3 a pluggable platform beyond Telegram: deterministic code injects context into an LLM via C3 and gets a fixed-format response back. Cross-links Thread D's spawn-and-attach control plane.
- **Web chat channel** — second `Channel` impl; magic-link URL flow; the abstraction is already multi-channel-ready.
- **Voice mode channel** — continuous voice (record→send→read aloud), hands-free/driving.

### Packaging / install
- **Eliminate `--dangerously-load-development-channels` / register a private trusted plugin store** — *medium.* Karthi is ready to sign a cert to drop the dangerous flag and officially register his own trusted plugin store (he'll maintain many private plugins). Status unverified against current CC.
- **Auto-update from GitHub release tags** — *medium (maintainer wants; update cadence is rising).* When a new GitHub **release** is published, C3 pulls it and self-deploys, so fixes reach users without a manual redeploy. Two modes to support/decide: (a) fully autonomous self-deploy, or (b) ask-in-a-turn then update **agentically** on approval (the Allow/Deny surface already exists). **Research first:** whether official Claude Code plugins have a built-in auto-update mechanism to use — else wire it ourselves off release tags. Depends on the broker being restart-safe (it is) and a versioned release process.
- **`install-claude-shim` existing-symlink clobber bug** — *fix.* `cmd/c3-broker/install_claude_shim.go:79-82` assumes any existing `~/.local/bin/claude` symlink is a prior shim; on Karthi's machine it points at the real binary → replacing it orphans `claude` in PATH. Not yet triggered. Fix path is Karthi's call (see Decisions).

### Access control
- **Master Telegram user / admin-from-Telegram** — admin who configures the system from Telegram. Pairing + per-user allowlist landed; master-user enforcement remains (the dead `MasterUserID` field could be repurposed — trusted-op §6).

### Reliability & tests
- **MCP-resume lifecycle hardening** — heartbeat + singleton-PID guard. Deeper CC MCP lifecycle on resume is poorly understood; surface these if symptoms recur. Karthi: "want this UX really smooth, no breakages."
- **Fix the 2 flaky broker tests** — *test-isolation defect, not prod breakage.* `TestAttach_CwdDefault_HeldByDifferentLiveSession_WarnsCollision` + `TestAttach_ExplicitName_HeldTopic_StillForceSteal` need `syscall.Kill(9823,0)` to report a live PID; fix the fixture.

### STT
- **STT gemini-3-flash-openrouter provider dead** — *ops, low (STT works on Sarvam fallback).* No `OPENROUTER_API_KEY` where the handler reads. Copying the key from the predecessor bot was blocked by the auto-mode classifier (cross-user read); needs CLI-level approval.

### Codex parity
- **Codex ↔ Claude install/setup parity gaps** — Codex MCP install hiccups; Codex didn't prompt for STT keys; Codex unaware of the CLI/Telegram output-mode protocol; Codex adapter lacks a `detach` tool. Confirm which asymmetries are intentional vs gaps.
- **`ask` / permission-relay / managed-session Codex parity** — all three new features are Claude-first; Codex parity is explicitly the later phase of each (cross-refs the items above).

### Phase 4 (advanced — not started)
- **Inter-CLI messaging** (CLI-1 → CLI-2 via broker).
- **Topic creation via API** (beyond the interactive attach proposal; overlaps Thread D's spawn-and-attach route resolver).
- **Monitoring dashboard** (adapters, message counts, STT health, broker resilience) — several "c3 down/broken" incidents argue real value.
- **Persistent message history** (context recovery across restarts).
- **Live CLI view** (web live-view; overlaps terminal-control's snapshot capability).

### Cross-project (ideas)
- **Shared SSHGate + C3 Telegram adapter** — both talk to Telegram (send/receive + approvals); explore one shared adapter instead of two parallel ones. Design + decision later.
- **Sibling project's alert-delivery seam → C3 transport** — *low-confidence (agent comment, not a Karthi quote).* A sibling project's alert seam is intentionally swappable for a future C3 transport; surfaced only so the seam isn't lost.

### Smaller backlog / quality
- **Cross-CLI duplication audit follow-ups b1/b2** — *awaiting Karthi review.* b1: tool-description divergence (Codex 2 extra tools + paraphrased attach desc); b2: broker reconnection error strings (shared error-helper design call). `docs/research/2026-05-18-cross-cli-duplication-audit.md`.
- **Tighter concurrent-inbound interleaving test** — sequentialization already works (per-route worker pool); a tighter interleaving test is still worth writing. No new prod code.
- **TODO #19(e) — CWD-fallback session matching** — verify whether `ListSessionsReq`'s "match-by-cwd when PID walk fails" is wired or a placeholder (`internal/proctree/proctree.go`).
- **Stale-doc spots needing judgment rewrites** — (a) `docs/ADAPTERS.md` still describes Codex's retired in-memory `inbox` ring/tool (now `fetch_queue` + durable queue); (b) `README.md` Status block refresh v0.1.0→current; (c) `docs/COMMANDS.md` verb/tool tables omit newer MCP tools + `pair/ping/sessions` slash commands; (d) `RESUME.md` top checkpoint reads as if its NEXT-ACTIONS are pending though executed. Left for Karthi's judgment.

---

## Needs a decision from Karthi

- **Typed-answer queuing (`ask` free-text)** — does a typed answer *also* get queued/delivered as a normal message, or is it consumed *only* as the answer? Unblocks the `ask` free-text/Other/comment build.
- **Trusted-operator §10 product calls** — grant UX (Style A vs B), operator identity, the default guarded set, default/max TTL, and whether v1 scope is CLI-agnostic. Unblocks the trusted-operator hook build (Phases 1–3).
- **Thread C/D open forks** — (a) managed-as-first-class-divert [recommended] vs managed-as-loopback-adapter; (b) accept "managed children die on broker restart" for MVP [recommended] vs invest in a re-attachable transport now; (c) child permission posture (default+relay [recommended] vs read-only/plan vs `--dangerously-skip-permissions` [rejected]); (d) confirm route-aware `/status` override; (e) confirm Codex = PTY-phase only; (f) confirm the async-turn model for agent `cli_send`. Spec §7.
- **`install-claude-shim` fix path** — (a) EvalSymlinks-and-remember the original target, (b) refuse-unless-shim, or (c) roll back the compulsory wiring.
- **5 code-review guideline-file edits** — *subjective.* Karthi's rubric files await his voice on each. `MORNING-REVIEW-2026-05-19.md`.
- **n3 — Unicode bullets in user output** — *subjective.* Keep Unicode bullets in user-facing output?
- **Push / merge** — nothing pushed to the public remote tonight (merges are local-master only). Branches are push-ready (PII audit clean, report `pii-audit/reports/c3-2026-06-27.md`); `feat/auto-attach-resume` is unpushed/unmerged. Re-run the PII audit right before any push (standing rule).
- **Deploy** — the live broker is still the pre-tonight binary; `ask`/permission-relay/etc. are not deployed. `go install ./cmd/...` + a broker restart bounces every live session.
