# TODO

**Canonical prioritized roadmap: [ROADMAP.md](ROADMAP.md)** (reconciled
2026-06-14). This file is the detailed working checklist; ROADMAP.md is the
prioritized product view — keep them in sync.

Status as of 2026-05-09. Locked spec is
[`docs/specs/2026-05-08-c3-rearch-design.md`](docs/specs/2026-05-08-c3-rearch-design.md).
Re-prioritize against the spec when picking next work.

## Install-flow feedback (surfaced 2026-05-15)

Surfaced during a third-party install pilot. Numbered so they can be
referenced individually ("start #12"); knock each one off and delete
the section when empty.

### Onboarding / setup flow

1. [x] **Auto-discover `chat_id` / `group_id` / `user_id` via
   4-digit pairing flow.** Done 2026-05-18. Allowlist schema in
   `internal/mappings/types.go` + `internal/mappings/allowlist.go`
   (migration-safe: pre-allowlist `mappings.json` round-trips fine).
   Pairing state machine + `Gate()` policy in
   `internal/broker/pairing.go` — 10-min TTL, crypto/rand 4-digit
   codes, separate DM and per-group windows. Channel-layer filter:
   `internal/channel/telegram/poll.go` `dispatchMessage` calls
   `host.GateInbound` before `host.Emit`; dropped inbounds never
   reach the worker pool. IPC op `pair_mode_start` +
   `c3-broker pair [dm|group <chat_id>]` subcommand
   (`cmd/c3-broker/pair.go`); slash command
   `plugins/c3/commands/c3-pair.md`. Auto-arms DM pairing on broker
   boot when `allowlist.users` is empty (no auto re-arm post-TTL —
   manual `/c3:pair` required after). Tests:
   `internal/broker/pairing_test.go` (gate + pairing + IPC),
   `internal/mappings/allowlist_test.go` (migration + persist +
   clone), `internal/channel/telegram/dispatch_gate_test.go`
   (channel layer drops without invoking Emit).
2. [x] **Bot-side auto-config of group, where possible.** Done 2026-05-18
   (`docs/INSTALL.md`). Manual steps documented; Bot API auto-config
   investigated and skipped because privacy mode is BotFather-only
   (no HTTP Bot API method exposes it), and the
   topics-enable / admin-promote toggles are group-owner-side actions
   that the bot itself cannot trigger. `setMyDefaultAdministratorRights`
   only pre-checks defaults *when a human promotes the bot*; it
   doesn't promote, and it doesn't touch privacy. Net: the install
   flow's six-step manual checklist is the floor — no programmatic
   shortcut available.
3. [x] **BotFather `/setprivacy → Disable` in setup instructions.**
   Done 2026-05-18 (`docs/INSTALL.md`). `/setprivacy → Disable` is
   documented as step 2 of the bot-setup checklist, before adding
   the bot to the group. Kept manual (BotFather-only — see item #2).
4. [x] **Onboarding preamble explaining what C3 is and why each
   step exists.** Users hit setup completely blank on what the system
   does, why they're creating a group, why topics. Lead with a short
   narrative before asking for any input. (verified done 2026-06-14)
5. [x] **Front-load bot-token ask; run install in background.** Bot
   token is the only true prerequisite — ask for it first and kick
   off install in parallel while walking the user through the rest of
   the flow. (verified done 2026-06-14)
6. [x] **Per-client Telegram-settings matrix.** Done 2026-05-18
   (`docs/INSTALL.md`). Per Karthi: no per-client matrix, just
   "don't use Telegram Web — use Desktop/iOS/Android/macOS." The
   research doc retains the full per-client breakdown for reference.
7. [x] ~~Outdated-client detection / warning.~~ **DROPPED** 2026-05-18.
   Per Karthi: 2022 is old enough that no current installer is on a
   pre-Topics build.

### Cross-CLI (Claude Code + Codex)

8. [x] **CLI detection for restart instructions.** Done 2026-05-18
   (`cmd/c3-broker/cli_host.go`, `cmd/c3-broker/setup.go`). Detects
   host CLI via `$C3_HOST_CLI` override → `$CLAUDECODE` / `$CLAUDE_PLUGIN_ROOT`
   → `$CODEX_HOME`, defaults to Claude. Setup prints the matching
   restart command (Claude: `--dangerously-load-development-channels`
   + `--resume`; Codex: `codex resume --last`). Tests:
   `TestDetectHostCLI_*`, `TestClaudeRestartInstructionMentionsDevChannelsFlag`,
   `TestCodexRestartInstructionMentionsCodex`.
9. [x] **Codex MCP self-registration during install.** Done 2026-05-18
   (`cmd/c3-broker/cli_host.go`, `cmd/c3-broker/setup.go`). When
   `DetectHostCLI()==HostCodex`, setup appends a
   `[mcp_servers.c3_codex]` block to `$CODEX_HOME/config.toml`
   (default `~/.codex/config.toml`). Idempotent: skips if the
   section header is already present. Tests:
   `TestEnsureCodexMCPRegistration_{CreatesFileAndAppendsBlock,Idempotent,AppendsBlankLineSeparator}`,
   `TestContainsCodexC3Section`.
10. [x] **STT API keys via Codex install path.** Done 2026-05-18
    (`cmd/c3-broker/setup.go`). Root cause: under Codex the agent
    drives stdin programmatically so the Y/N STT prompt auto-skips
    silently. `promptSTTSetup` now returns `(written, err)`; when
    nothing was written, setup prints a host-specific `sttHint` —
    under Codex, an explicit walkthrough of `~/.claude/stt.env`
    format and a recommendation to rerun in a real terminal; under
    Claude, a one-liner pointer to rerun.
11. [x] **Codex mode-awareness (CLI vs Telegram).** Done 2026-05-18.
    Codex's MCP host doesn't read MCP-initialize `instructions`
    (parallel investigation: openai/codex#6148 closed-not-planned,
    empirical session-rollout grep, binary symbol audit), so the
    Claude path of "ship protocol in instructions" doesn't reach
    Codex. Codex DOES read `~/.codex/AGENTS.md` and concatenates it
    into developer_instructions, so the install path is an
    idempotent delimited-block writer there
    (`<!-- c3:protocol START --> … <!-- c3:protocol END -->`).
    Single source of truth extracted to
    `internal/mode/protocol.go` (`ModeProtocol` + `MultipartProtocol` +
    `Combined()`); both adapters import it for their `instructions`
    field, and `cmd/c3-broker/cli_host.go`'s `ensureCodexAgentsMd`
    installs the same body into `$CODEX_HOME/AGENTS.md` from
    `runSetup` when `DetectHostCLI()==HostCodex`. Tests:
    `TestEnsureCodexAgentsMd_{CreatesFileWhenAbsent,ReplacesExistingBlock,IdempotentOnRerun,CreatesParentDir,AppendsWhenMissingMarkers}`,
    `TestReplaceCodexAgentsMdBlock_CorruptBlockNotReplaced`,
    `TestCodexAgentsMdPath_{RespectsCODEX_HOME,FallsBackToHome}`,
    plus `internal/mode/protocol_test.go` for the shared text.

### Broker / STT bugs

12. [x] **Fresh-install STT crash: missing log-dir parents.** Done
    2026-05-16 (`plugins/c3/stt/stt-handler.py`,
    `internal/plugin/builtins/stt/stt.go`). `os.makedirs` for both
    `LOG_FILE` parent and `INBOX_DIR` before `basicConfig`; try/except
    around `basicConfig` falls back to `stream=sys.stderr` (broker
    captures stderr into broker.log) if file-handler init still fails.
    Belt-and-suspenders: broker's `stt.Register` now pre-creates the
    *default* `~/.claude/channels/telegram/{,inbox}` dirs at startup
    via `ensureSTTDefaultDirs`; env-overridden paths remain the
    Python side's responsibility (it knows the actual values).
    Regression test:
    `TestImportCreatesParentDirs` in `plugins/c3/stt/test_stt_handler.py`.
13. [x] **STT error surface swallows stderr.** Done 2026-05-18
    (`internal/plugin/builtins/stt/stt.go`,
    `internal/broker/worker.go`). Failure markers now embed the
    broker log path: `[STT FAILED: <reason> — see <path>]` where
    `<path>` is `$C3_LOG_FILE` / `$XDG_STATE_HOME/c3/broker.log` /
    `~/.local/state/c3/broker.log`. Both code paths
    (`sttFailureMarker` in the stt plugin and the
    `no_transcript_plugin` fallback in `worker.go`) emit the hint.
    Existing tests still pass (they assert `Contains("[STT FAILED:")`
    + reason substring).

### Codex policy layer

14. [x] **`approvals_reviewer=auto_review` silently rejects `attach`
    as data-export risk.** Done 2026-05-19 (`internal/ipc/messages.go`,
    `internal/broker/attach.go`, `cmd/c3-claude-adapter/main.go`,
    `cmd/c3-codex-adapter/main.go`, `DEBUGGING.md`,
    `docs/plans/2026-05-19-codex-policy-3state.md`). New `AttachStatus`
    enum on `AttachedMsg` with three constants
    (`AttachStatusOK` / `AttachStatusNoTopicsConfigured` /
    `AttachStatusPolicyRejected`). Broker emits
    `no_topics_configured` directly when mappings has no channels or
    no DM destination. `policy_rejected` is short-circuited at the top
    of `handleAttach` when `AttachReq.PolicyRejected=true` — the agent
    sets the hint on a re-invoke after observing the host's policy
    rejection in its turn output (Codex's policy layer lives upstream
    of the spawned MCP server, so the adapter never sees the original
    call — only the agent can surface it). Both adapter formatters
    branch on `Status` before falling through to `Err`. New tool
    argument `policy_rejected` on the Codex adapter's `attach` tool.
    `DEBUGGING.md` gains "Codex policy layer rejected attach" section
    documenting the failure mode + resolution path. Tests cover the
    enum exposure, JSON omitempty wire shape, all four success paths
    setting `Status=ok`, the two `no_topics_configured` branches, the
    short-circuit, both adapter formatters' new status branches, and
    the Codex adapter's tool-schema advertising `policy_rejected`.
    Agent S's task notification stream stalled at the final
    verification step, but `go test -race`, `go vet`, `go build` all
    re-verified clean post-stall.

### Post-attach mode UX (surfaced 2026-05-18)

15. [x] **Announce current output mode after attach.** Done
    2026-05-19. Resolution: protocol addition, no persistence. New
    bullet appended to `internal/mode/protocol.go::ModeProtocol`
    mandating that the agent announce its current output mode
    immediately after attach completes ("currently in CLI mode" /
    "currently in Telegram mode"). The broker still does NOT persist
    mode (agent owns it, as before); the protocol now requires the
    explicit confirmation on the agent's side so the human always
    knows where replies will land. The const propagates automatically
    to (a) the Claude adapter's MCP-initialize `instructions` via
    `internal/mode.Combined()` and (b) Codex's `~/.codex/AGENTS.md`
    delimited block via `ensureCodexAgentsMd` next time setup runs
    under HostCodex. Tests:
    `internal/mode/protocol_test.go::TestModeProtocol_HasAnnounceModeAfterAttach`.

### Second install pilot — Sakthi (surfaced 2026-05-16)

Sakthi's first-install report
(`c3-voice-channel-bug-report.md`, attached 2026-05-18).
End-to-end pipeline worked except the very last hop into CC, lost
hours to a flag we don't document and to multi-session claim
confusion. Items are scoped distinct from #1–14.

16. ~~Document `--channels` launch flag.~~ **DROPPED** 2026-05-18.
    Per Karthi: no credible alternative form to document. Sakthi's
    `strings` finding most likely came from a different / older code
    path in his 2.1.142 build; the existing
    `--dangerously-load-development-channels plugin:c3@c3` form
    works and stays as documented.

17. [x] **`c3-broker install-claude-shim`** that wraps `claude` and
    auto-injects the required channel flag(s), parallel to the
    existing `install-codex-shim`. Removes the requirement that the
    user remember the flag, and scales when more channel-emitting
    plugins land. Karthi's standing instruction is no shell aliases
    — this is a real installed wrapper, not an alias.
    Done 2026-05-18 (`cmd/claude-shim/main.go`,
    `cmd/c3-broker/install_claude_shim.go`). Idempotent — preserves
    existing `--dangerously-load-development-channels` flag if
    present, appends `plugin:c3@c3` only when absent. Refuses to
    overwrite a non-shim `~/.local/bin/claude` without `--force`
    (uses an embedded sentinel string to recognize prior shims).
    Companion `uninstall-claude-shim` is idempotent on the missing-
    path case. Auto-installed during `c3-broker setup` when host CLI
    is Claude Code (2026-05-18, compulsory per Karthi — no prompt,
    no opt-out; failures surface as a non-fatal warning telling the
    user to run `install-claude-shim --force` manually).

    **🚨 BUG FLAGGED 2026-05-19 (smoke test).** Existing-symlink
    clobber: `cmd/c3-broker/install_claude_shim.go:79-82` says "any
    existing symlink — assume prior shim install; safe to replace,"
    but on Karthi's machine `~/.local/bin/claude` is already a
    user-curated symlink pointing at the REAL claude binary
    (`~/.local/share/claude/versions/2.1.143`). Replacing it with a
    symlink to `~/go/bin/claude-shim` orphans the only `claude` in
    PATH; shim's `findRealClaude` PATH walk then finds nothing and
    errors. Result: `claude` becomes unusable until manual restore.
    Has NOT yet triggered on this machine (compulsory install only
    fires on `c3-broker setup`; not run since the wiring landed) —
    Karthi to choose fix path: (a) EvalSymlinks-and-remember
    original target into `~/.config/c3/claude-shim.json` so shim
    PATH walk has a fallback, (b) refuse-unless-shim and require
    user migration, or (c) roll back compulsory wiring to opt-in.
    Karthi pick pending.
18. [x] ~~Adapter-side preflight on initialize: detect "no channels
    accepted for this session".~~ **CLOSED as subsumed by #17** —
    2026-05-18. Two reasons: (a) MCP spec has no wire-level signal
    for "host refused this server's channel capability" — the drop
    decision happens AFTER initialize when CC receives the
    notification, so no positive negative signal exists to fire on
    (Agent J's investigation: openai/codex#6148 closed-not-planned;
    empirical session-rollout audit). (b) Karthi made the
    install-claude-shim COMPULSORY during `c3-broker setup` (item
    #17), so the misconfiguration that #18 was meant to catch can't
    be reached via supported install paths.
19. [x] **Multi-CC-session UX.** Closed 2026-05-19 with all five
    sub-items (a–e) shipped. Sakthi's phantom-session-bug rabbithole
    is now structurally impossible: terminal title bars show the
    attached topic (a), `/c3:ping` fingerprints individual tabs over
    Telegram (b), PID-death + `--resume` reattach are
    test-protected with the alive-but-abandoned-tab papercut
    documented (d), and `/c3:sessions` enumerates every live
    adapter with cwd + attached topic + "this session" marker (e).
    Steal-semantics doc (c) intentionally dropped — Sakthi's
    confusion was unawareness of his own holder status, fully
    addressed by (a) + (b). Revised plan after Karthi review
    2026-05-18:
    (a) [x] Set terminal title / status line to attached topic info.
        Done 2026-05-19. Surface picked: OSC-0 ANSI title-bar escape
        emitted by the adapter on attach (NOT a Claude-Code statusline
        plugin) — same code path works for both Claude Code AND Codex
        (Karthi's "every flow must work the same in Codex" principle),
        no settings.json edits required. New `internal/termtitle/`
        package owns the escape framing + format
        ("c3: <name> · <group>", "c3: dm" for DMs, empty on detach);
        both adapters call `termtitle.EmitAttach(&attached)` on the
        OK branch and `termtitle.Clear()` on detach (Claude only —
        Codex has no detach tool). Gated on `isatty(stderr)` and
        `C3_NO_TERMINAL_TITLE` env var (truthy values suppress).
        Tests: `internal/termtitle/termtitle_test.go` (16 tests:
        FormatTitle variants, EmitTo/ClearTo gating, env truthy
        matrix); per-adapter call-site tests
        `cmd/c3-claude-adapter/title_test.go` (8) and
        `cmd/c3-codex-adapter/title_test.go` (7) covering OK / DM /
        no_topics_configured / policy_rejected / proposal / env
        suppression / non-tty paths. Plan:
        `docs/plans/2026-05-19-terminal-title.md`.
    (b) [x] `/c3:ping` slash command. Done 2026-05-19. New IPC op
        `ping_this_session` (`internal/ipc/ops.go`,
        `internal/ipc/messages.go`); broker handler
        `handlePingThisSession` in `internal/broker/handler.go`
        matches the calling session by CWD against live stubs (the
        `c3-broker ping` transient client doesn't hold a route, so
        we scan for the user's actual adapter stub whose
        `CurrentRoute()` is non-nil). New CLI subcommand
        `c3-broker ping` (`cmd/c3-broker/ping.go`), wired into
        `cmd/c3-broker/main.go`'s dispatch + usage block. Slash
        command `plugins/c3/commands/c3-ping.md` mirrors the
        `/c3:pair` structure. Tests:
        `TestPing_SendsReplyToAttachedRoute` (happy path),
        `TestPing_NoAttachedStubReturnsError` (no-attach error
        path) in `internal/broker/attach_test.go`.
    (c) ~~document steal semantics~~ DROPPED. Steal semantics work
        as Karthi expected; Sakthi's confusion was unawareness of
        his own holder status (he was the holder, called steal,
        got a self-evict-self-reclaim no-op). Fixed by (a) + (b).
    (d) [x] Verified + documented. Done 2026-05-19.
        - PID-death MUST trigger mapping release — verified by
          `internal/broker/handler_test.go::TestConnDrop_ReleasesClaimWhenPIDDead`
          (sentinel PID=-1 triggers `isPIDAlive(pid<=0)` short-
          circuit → dead-PID branch in HandleConn defer →
          `Routes.ReleaseAllByConnID`).
        - `--resume` MUST re-attach via `replayLastAttach` —
          verified by
          `cmd/c3-claude-adapter/lifecycle_test.go::TestReplayLastAttach_ResendsLastAttachWithReplayFlag`
          (rememberAttach + replayLastAttach against net.Pipe;
          asserts Replay=true and user fields preserved) and
          `TestReplayLastAttach_NoopWithoutPriorAttach` (no spurious
          frame when no prior attach).
        - "Alive-but-abandoned-tab" papercut documented in
          `DEBUGGING.md` § "Multi-session: alive-but-abandoned
          tabs": workaround via `force_steal` proposal, kill the
          PID, or use `/c3:ping` to identify the owner. No new
          periodic-ping (explicitly rejected by Karthi).
    (e) [x] `/c3:sessions` listing live CC processes, cwd, claim
        state, "this session" marker. Done 2026-05-19. New IPC op
        `OpListSessions` + `ListSessionsReq` / `ListSessionsReplyMsg`
        / `SessionEntry` wire types in `internal/ipc/`. Broker
        handler `handleListSessions` in `internal/broker/handler.go`
        with `sessionTopicLabel` formatter ("name (group)" / "dm" /
        "topic-<id>"). CLI subcommand `c3-broker sessions` in
        `cmd/c3-broker/sessions.go` with a Linux `/proc`-based
        parent-PID walk that seeds the broker's "this is me" match
        (degrades gracefully to `os.Getppid()` on non-Linux or when
        walk fails — no false-positive marker in that case). Slash
        command `plugins/c3/commands/c3-sessions.md`. The transient
        client itself (CLI=="c3-broker-cli") is filtered from the
        reply so callers never see themselves listed. Tests: 8 in
        `internal/broker/sessions_test.go` (returns all stubs,
        marks-this-session, empty-list, attached-to-formats,
        empty-when-unattached, dm-label, ordered-desc-by-conn-id,
        filters-transient-stub, empty-CLI-mapped-to-questionmark);
        4 IPC roundtrip tests in `internal/ipc/messages_test.go`;
        5 renderer tests in `cmd/c3-broker/sessions_test.go`. Plan:
        `docs/plans/2026-05-19-sessions-listing.md`.

    With (e) closed, all sub-items (a–e) of #19 are done. Parent
    #19 closed below.

21. [x] **Migrate MCP wire layer to modelcontextprotocol/go-sdk.**
    Done 2026-05-18 (`cmd/c3-claude-adapter/`, `cmd/c3-codex-adapter/`).
    SDK v1.6.0 pinned in `go.mod`; all existing tests pass with
    rewritten bodies; `notifications/claude/channel` wire shape
    preserved byte-for-byte via a `notifyTransport` wrapper that
    exposes the SDK Connection for custom-method notification frames
    (the SDK's typed Notify* API is locked to spec-defined methods).
    `instructions` field continues to come from
    `internal/mode.Combined()`; `claude/channel` experimental
    capability still declared via `ServerCapabilities.Experimental`.
    Codex parity also landed: `disambiguate_dm` + `force_steal`
    proposal-formatter branches added so Codex agents see the same
    actionable text Claude has been seeing, instead of "attach:
    unspecified failure" (broker emits these regardless of CLI per
    `internal/broker/attach.go:160 + 464`). Regression tests:
    `TestServerInfoName` (both adapters — confirms the
    `serverInfo.name` / `experimental.claude/channel` invariants
    survive migration end-to-end through in-memory transports),
    `TestChannelFrameWireBytes` (asserts byte-exact wire shape for
    the custom notification), `TestFormatAttached_ProposalParity`
    (Codex now formats all 4 proposal actions).

### Single-source-of-truth audit (surfaced 2026-05-18)

20. [ ] **Audit cross-CLI duplication; consolidate to one source.**
    Karthi standing principle 2026-05-18: anything duplicated
    between Claude and Codex adapters / install paths (protocol
    text, install instructions, restart commands, tool descriptions,
    setup-time effects) must have ONE source of truth.
    Implementation surface can differ (e.g. modeProtocol → MCP
    `instructions` for Claude, `~/.codex/AGENTS.md` block for
    Codex) but the underlying string / behaviour must be defined
    once. First concrete extraction:
    `internal/mode/protocol.go` for modeProtocol +
    multipartProtocol. Then sweep the rest and propose
    consolidations before landing.
    **First extraction landed via this work — modeProtocol +
    MultipartProtocol now in internal/mode/.** Both adapters call
    `mode.Combined()` for their MCP-initialize `instructions`; the
    Codex AGENTS.md installer (#11) sources the same body. Next
    candidates to audit: restart-instruction text (already
    centralised in `cli_host.go`'s `claudeRestartInstruction` /
    `codexRestartInstruction`), tool descriptions (currently
    duplicated across the two adapters' `toolsListResponse`), and
    the install-flow STT hint text.

    **Triage 2026-05-19** (`docs/plans/2026-05-19-audit-triage.md`).
    Counts across the 13 audit entries: 2 already-done (modeProtocol,
    mcpProtocolVersion), 2 EXTRACTED (`FormatAttached` + `FormatTopics`
    → `internal/ipc/format.go`; both adapters call `ipc.Format*`),
    4 DROPPED with one-line reasons (no_topics_configured string,
    idleStartupTimeout const, installSignalHandlers helper,
    spawnBroker — all hit the 3-caller floor for extraction or are
    already aligned), 3 correct-divergences (restart instructions,
    Codex MCP registration, attach request param handling), 2 surfaced
    for Karthi review — see audit doc bottom section:
    (b1) tool descriptions (Codex has 2 extra tools + paraphrased
    `attach` description; helper signature non-trivial),
    (b2) broker reconnection error strings (near-identical with 1
    outlier; design call on whether to introduce a shared error helper
    for 2 callers). Item stays `[ ]` until those two are resolved.

## In flight (user-driven)

- [ ] **First-run validation of the Go broker.** Paste the install
  one-liner into a fresh Claude Code session, walk through `INSTALL.md`,
  then `cd` into a project, `attach`, and confirm a real Telegram
  round-trip. Surfaces any rough edges before public GitHub push.

## Pre-release UX bugs (surfaced 2026-05-13)

Surfaced during install/attach pilot. Must fix before the public push.

### Second-round bugs surfaced 2026-05-14 (post-computer-restart resume)

- [x] **Welcome message never fired after broker bounce + fresh user attach.**
  Done 2026-05-14. Root cause: a 30s post-startup `welcomeRecoveryWindow`
  was added as belt-and-suspenders for adapters that didn't yet thread
  the `Replay` flag — but it false-positived against legitimate
  user-typed attaches landing within 30s of broker startup. Replay
  flag is the authoritative signal; the window was removed
  (`internal/broker/attach.go:sendWelcome`,
  `internal/broker/broker.go`). Regression test:
  `TestSendWelcome_FreshUserAttachJustAfterBrokerStartup_Fires`.
- [x] **`c3` MCP plugin shows "disconnected" on Claude Code `--resume`,
  requires manual `/mcp` reconnect.** Done 2026-05-14. Hardened the
  adapter against the observed failure mode where Claude Code spawns
  the adapter but never sends an MCP frame (orphaned spawn during
  session resume teardown). Three changes in
  `cmd/c3-claude-adapter/main.go`: (1) signal handlers
  (SIGTERM/SIGINT/SIGHUP) that log + cancel ctx so future incidents
  leave a breadcrumb in adapter.log; (2) idle-startup watchdog —
  if no MCP frame within 60s of startup, exit cleanly so the adapter
  doesn't zombie holding a broker conn; (3) explicit exit-reason
  logging at every return path. Regression tests:
  `TestIdleStartupWatchdog_CancelsWhenNoDispatch`,
  `TestIdleStartupWatchdog_StaysQuietAfterDispatch`,
  `TestDispatch_SetsDispatchedFlag`. Open follow-up: deeper Claude
  Code MCP lifecycle interactions on resume are still
  poorly-understood — surface a heartbeat and a singleton-PID guard
  if symptoms recur.
- [x] **Stale claim after `/mcp` reconnect blocks inbound delivery AND
  fallback.** Done 2026-05-14. Sequence Karthi hit: `/c3:attach` (got
  welcome ✅) → `/mcp` reconnect (CC kills old adapter, spawns new) →
  old adapter dies; broker's conn-drop defer marks stub disconnected
  but doesn't release the claim. New inbounds hit `deliver FAIL:
  holder.Conn is not *ipc.Conn` (type-assertion on nil conn) and no
  fallback fires either (a stale-but-present claim ≠ no claim). Fixes:
  (1) `internal/broker/worker.go:forwardOrFallback` now calls
  `holder.IsAlive()` at dispatch time; dead-holder claims are released
  in-place and the message falls through to the fallback path; alive-
  but-disconnected holders cause a SKIP log (adapter is between
  reconnects). (2) `internal/broker/handler.go` conn-drop defer now
  checks `isPIDAlive(stub.PID)` and releases claims when the PID is
  already dead at conn-drop time. Regression tests:
  `TestForwardOrFallback_StaleClaim_ReleasesAndFallsThrough`,
  `TestForwardOrFallback_AliveButDisconnectedHolder_SkipsDelivery`.
- [x] **`/c3:restart-broker` kills this session's MCP adapter as a side
  effect.** Done 2026-05-14. Empirically confirmed via two consecutive
  test bounces: pid 25193 and pid 34696 (both running the Phase-1-
  hardened binary with quieted stderr + nil-conn guards) still exited
  via `stdin-eof` immediately on every `/c3:restart-broker`. Claude
  Code's MCP host closes the adapter's stdin on broker-process death,
  for reasons we couldn't pin down without inspecting CC internals.
  Rather than fight CC's recycle behavior (Phase 2 SIGTERM coordination
  or Phase 3 fd-passing handoff), removed the broken primitive. The
  slash command is now `/c3:reload-config` — sends SIGHUP, broker
  re-reads mappings.json in-place, no process churn, MCP adapter
  unaffected. For binary updates, the right action is `quit` +
  `claude --resume` (the new adapter spawn auto-spawns a fresh broker
  with new binaries; no manual bounce needed).

- [x] **Welcome message on attach.** Done 2026-05-14
  (`internal/broker/attach.go:sendWelcome`). Friendly tone, no PID, async,
  suppressed for adapter-replay re-attaches (broker bounce or conn-drop
  recovery doesn't spam the topic).
- [x] **CLI doesn't actually `cd` into the named project.** Done
  2026-05-14 via `~/arogara/AGENTS.md` rule "Hard rule — `cd` before
  anything else when switching projects". Promoted to its own section
  near the top of the C3 attach docs so it's impossible to miss.
  Multi-project caveat documented for the rare case where staying at
  the parent and using absolute paths is correct. Not a broker change —
  shell discipline lives in agent instructions.
- [x] **Default cwd for a fresh topic = launch root, not project root.**
  Done 2026-05-14 (`internal/broker/attach.go:resolveAttachCWD`). The
  broker now refines launch_cwd → launch_cwd/topic_name when that
  subdirectory exists, so attaching to multiple topics from the same
  parent directory persists distinct mappings.
- [x] **Mappings registry allows duplicate default cwd across topics.**
  Done 2026-05-14, hardened later same day per voice feedback
  (`internal/broker/attach.go:persistMapping`). Broker now REFUSES to
  silently rebind a saved cwd → topic mapping when a different topic
  is being attached from the same cwd. Live claim still proceeds;
  only an explicit `~/.config/c3/mappings.json` edit can change the
  saved default. Loud log line on refusal.

## Completed follow-up (D011 — Plan 7: Codex bridge in Go)

Landed 2026-05-09. The Go broker now supports Codex through the Go launcher
and Go adapter.

- [x] **`cmd/c3-codex-adapter`** — WS forwarder implemented. Inbound C3
  messages are submitted to the Codex app-server as turns.
- [x] **`cmd/codex/main.go`** — launcher binary that intercepts the `codex`
  command and shims to the adapter (parallels how the Claude adapter is
  loaded as an MCP server).
- [x] **`c3-broker install-codex-shim`** — subcommand that symlinks the
  launcher into the user's PATH.

## Broker — small follow-ups

- [ ] **`c3-broker release <cwd>` runtime IPC op.** Currently stubbed. Lets
  a project free its attached topic without restarting the broker.
- [x] **Adapter auto-recover beyond reconnect-once.** Done 2026-05-09.
  `recoverBroker` (exponential backoff 0.5s → 30s, no give-up) +
  `replayLastAttach` (re-issues the last successful attach on reconnect)
  in `cmd/c3-claude-adapter/main.go`. A long-running session now survives
  a broker bounce without restarting Claude Code.

## Telegram resilience — OpenClaw parity

Surfaced 2026-05-09 after the polling-timeout bug fix. Source: OpenClaw's
`extensions/telegram/` (grammy-based). Most items landed 2026-05-09 in the
same session.

- [x] **Honor `parameters.retry_after` on Telegram 429.** Done in
  `internal/channel/telegram/poll.go` pollLoop (cap 60s).
- [x] **401 circuit-breaker.** Done — `authBreaker` in
  `internal/channel/telegram/resilience.go`; trips after 10 consecutive
  401s, sleeps 5min between probes, clears on any success.
- [x] **409 Conflict detection.** Done — pollLoop logs loud and `return`s
  when classifyError returns `errClassConflict`.
- [x] **Per-method timeout policy.** Done — `timeoutFor(method, longPoll)`
  in resilience.go, used via `requestOptsFor()` from every gotgbot call
  site. Long-poll budget is now `25s + 30s = 55s`.
- [x] **Error classification: transient-network vs permanent-API.** Done —
  `classifyError` + `isTransientNetworkError` in resilience.go. Permanent
  errors (other 4xx) feed the auth breaker; transient errors get the
  exponential backoff path; conflict and rate-limited get their own paths.
- [x] **Persisted update-id watermark.** Done — `offsetStore` in
  `internal/channel/telegram/offset_store.go` writes
  `$XDG_STATE_HOME/c3/telegram-offset.json` after each successful
  GetUpdates. pollLoop seeds `offset` from this on startup.
- [x] **Outbound rate-limiting.** Done — `rateLimiter` in
  `internal/channel/telegram/rate.go` using `golang.org/x/time/rate`.
  Global 30/sec, group 20/min, private 1/sec (burst 5). Wired into every
  outbound call in `outbound.go`.
- [x] **Per-update semantic dedup.** Done — `updateDedup` LRU in
  `internal/channel/telegram/dedup.go` (capacity 2000, TTL 5min).
- [ ] **Sequentialize per-chat handler dispatch.** **Already provided** by
  the per-route worker pool — `internal/broker/worker.go` runs one
  goroutine per `RouteKey = (channel, chat_id, *topic_id)`, serializing
  inbound + outbound for that route. Worth a tighter test (concurrent
  inbound interleaving) but no new code needed. Marked done.

Skipped (intentional):
- Transport-level fallback chain (IPv4-sticky / pinned IP) — overkill for
  our deployment shape.

Reopened (was mis-marked "Skipped (intentional)"):
- **Album / media-group handling — KNOWN GAP, reopened as RESUME FIX #1.**
  ~~Media-group debounce (500ms hold-and-merge) — only matters for
  multi-photo album sends, not in any current flow.~~ (2026-06-14
  reconciliation) This contradicts `RESUME.md` §FIX #1 (parked), which
  documents a real bug: a same-poll-batch inbound was silently dropped
  (msgs 186/187 logged ~33µs apart, only 187 delivered) AND C3 has **no
  media-group assembly** — it relies on the 1.5s debounce, so a two-file
  album loses a sibling. Not intentional; tracked as RESUME FIX #1
  (P1, parked). See `RESUME.md` §FIX #1 and ROADMAP.md P1.

## Phase 3 — User & Access Management (not started)

- [ ] **Per-user access control** — who can talk to which CLI.
- [ ] **Pairing flow** — new users get a pairing code, approved by master
  CLI or admin.
- [ ] **Master Telegram user** — admin who can configure the system from
  Telegram itself.

## Phase 4 — Advanced (not started)

- [ ] **Inter-CLI messaging** — CLI-1 sends a message to CLI-2 through the
  broker.
- [ ] **Topic creation via API** beyond the attach proposal flow —
  programmatic topic management for admins.
- [ ] **Monitoring dashboard** — connected adapters, message counts, STT
  stats.
- [ ] **Persistent message history** — context recovery across CLI
  restarts.
- [ ] **Slash commands handled in the broker** — `/status`, `/list`,
  `/route`, etc. without round-tripping to the LLM. OpenClaw-style fast
  ops.
- [ ] **Stream thinking / tool calls to Telegram** — research best UX
  first.
- [ ] **Web chat channel** — second `Channel` impl alongside Telegram.
  Magic-link URL flow. The pluggable channel layer is already in place
  (D007).
- [ ] **Voice mode channel** — continuous voice (record → send → read aloud).
  Driving / hands-free.
- [ ] **Live CLI view** — see what's happening in the CLI from the remote
  interface.

## Done — v0.1.0

Kept short for reference; full detail in the git history.

- Plan 1: repo skeleton (go.mod, cmd/, internal/, Makefile)
- Plan 2: mappings registry + `migrate-legacy` (27 tests)
- Plan 3: broker core + IPC; live daemon (27 broker/ipc tests)
- Plan 4A: Channel/Host interfaces + RouteWorker + WorkerPool
- Plan 4B: Telegram channel cleanroom Go (`gotgbot/v2` rc.34) — outbound
  tools, getUpdates, inbound conversion, OpToolCall + cooldown-fallback,
  attach proposal flow (8 tests), debounce + mergeBatch (7 tests),
  reconnect-once on adapter
- Plan 5: plugin host + STT plugin
- Plan 6: Claude Code MCP adapter — end-to-end live, 7 tools, manual
  framing for `notifications/claude/channel`
- Plan 9: install plumbing — marketplace.json, plugin.json, .mcp.json,
  `/c3:build`/`/c3:setup`/`/c3:status` slash commands, `c3-broker setup` /
  `status` / `validate` subcommands, root `INSTALL.md` single-line install
- Plan 10: doc pass — D009 added, README + RESUME + TODO rewritten to
  current state

## Recovered from past sessions (2026-06-14 reconciliation)

Ideas raised in voice notes / sessions that were never tracked here. Mined
during the 2026-06-14 roadmap reconciliation; see ROADMAP.md for priority.

- **Drop `--dangerously-load-development-channels` / register a private
  trusted plugin store** — sign a certificate or whatever's needed to
  eliminate the dangerous flag, and officially register Karthi's own
  trusted plugin store for his many private plugins. (session 2026-05-18)
  — see ROADMAP.md.
- **ContestEval extension / programmatic non-Telegram channel** —
  pluggable platform beyond Telegram where deterministic code injects
  context into the LLM via C3 and gets a fixed-format response back (a
  programmatic channel, not chat). (session 2026-06-04) — see ROADMAP.md.
- **STT multi-provider modularity + retry/fallback + "how to add a
  provider" README** — chain + fallback exist; the explicit how-to-add-a-
  provider README Karthi asked for is unverified / likely missing.
  (session 2026-05-15) — see ROADMAP.md.
- **Codex ↔ Claude install/setup parity gaps** — Codex MCP install
  hiccups; Codex didn't prompt for STT keys; Codex unaware of the
  CLI/Telegram output-mode protocol. Confirm which asymmetries are
  intentional vs gaps. (session 2026-05-18) — see ROADMAP.md.
- **Auto-attach-to-c3-by-default bug — re-verify** — sessions default-
  attach to the c3 topic even when not working on c3; reported fixed but
  unverified in-repo (no commit since 2026-06-04). Re-verify.
  (session 2026-06-13) — see ROADMAP.md.
- **5 code-review guideline-file edits** — Karthi's rubric files awaiting
  his voice on each (subjective rubric changes, not code).
  (MORNING-REVIEW-2026-05-19) — see ROADMAP.md.
- **n3 — Unicode bullets in user output** — keep Unicode bullets in
  user-facing output? Subjective UX call; Karthi decides.
  (MORNING-REVIEW-2026-05-19) — see ROADMAP.md.
