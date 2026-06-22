# C3 Recovery-Hardening — design / implementation plan (2026-06-22)

**Mandate (Karthi, verbatim intent):** "make sure this never happens again ... write
code so that it fails and recovers gracefully without needing a debug or anything."
Trigger: a fresh-laptop-wake flaky-proxy timeout storm caused a Telegram 409 that made
the poll loop exit permanently (broker alive, not polling) until a manual restart.

**Already shipped this session (the trigger):** `f817ee8` 409 self-heals (retry+backoff,
no exit); `aa945fe` adapters reap spawned brokers (no zombies). This plan covers the
**24 confirmed findings** from the 2026-06-22 multi-agent recovery-posture audit (run
`wf_b22ddff7-ffe`), plus the follow-on gap that audit found in the 409 fix itself.

**Scope (Karthi 2026-06-22):** full batch + the bigger IPC items + all four judgment
calls (pip-install sarvamai now [DONE], Codex reconnect parity, systemd unit, 2nd proxy),
and wire the sarvamai install instruction into all the right places.

## Global constraints (binding on every task)
- **Do NOT reintroduce the retired `lastPollReturn` atomic** (commit 39f2fc6 deliberately
  collapsed two competing false-positive watchdogs into the ONE fetch-health machine).
  Conflict/panic awareness must extend that single machine, not add a parallel signal.
- **Keep-out values never enter tracked files** (the proxy subdomains, the proxy GCP project
  name, its region, its static IP — see agent-memory `c3_telegram_proxy_deploy` /
  `c3_tg_proxy_gcp_project`). Placeholders only; real values live in `~/.config/c3/`,
  agent-memory, and on the VM. Run `~/arogara/pii-audit/scan.sh` before every push.
- **Never leak the bot token** in any new log line/error.
- TDD every fix (failing test first); per-batch reviewer subagent; PII + commit + push per batch.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Adapters deploy per-session (effective next fresh session); broker changes need a
  rebuild + restart to go live.

## Severity legend (recovery impact)
Critical = silent permanent death / data loss with no auto-recovery & no alert.
Important = needs manual restart but is visible/alerted. Minor = ungraceful but self-heals.

---

## Batch A — telegram resilience core (`internal/channel/telegram`)
- **A1 (Critical) panic recovery + supervise the poll goroutines** — poll-resilience-1 /
  health-observability-3. `telegram.go` spawns `pollLoop`/`silenceWatchdog`/`heartbeat`
  with no `recover()`; an unrecovered panic crashes the whole broker → (with no supervisor,
  Batch F) silent death. Fix: (a) per-iteration `recover()` around the dispatch body in
  pollLoop — log panic+stack, `reportHealth(RecordFailure("pollLoop panic"))`, `continue`
  (one poison update must not kill polling); (b) outer `recover()` on each goroutine that
  logs + drives health, and for pollLoop re-enters with a short backoff (supervisor),
  bounded so a deterministic panic can't tight-spin.
- **A2 (Important) conflict-aware health** — fix-review follow-on of my 409 fix. Because
  pollLoop now stays alive on a persistent 409, the 5-min `getMe` heartbeat (never sees a
  409) calls `RecordSuccess` and clears the DOWN alert while inbound is still conflict-dead.
  Fix: a `conflictActive` flag on the Channel (set when `consecConflict>0`, cleared on the
  first getUpdates success); heartbeat getMe success must not clear a DOWN whose cause is an
  active getUpdates conflict. Extend the single health machine; no new watchdog.
- **A3 (Minor) consecConflict comment/code consistency** — poll.go comment says "reset on
  any non-conflict outcome" but code only resets on a poll success. Make code match comment
  (reset conflictBackoff+consecConflict on the other non-conflict error branches too) — or
  fix the comment; prefer making behavior match the documented intent.
- **A4 (Minor) DOWN consec-count consistency** — health-observability-4. The silence-arm
  DOWN reports `consec=2` vs documented threshold 3 because `CheckSilence` doesn't bump
  consecFails. Fix: in `CheckSilence`, stamp a silence-specific reason unconditionally and
  surface the silence duration so the alert text is self-consistent.
- **A5 (Minor) offset-Save-failure advisory** — poll-resilience-4. A sustained `offsets.Save`
  failure silently re-floods on restart. Count consecutive Save failures; past a small
  threshold emit a one-time loud advisory (mirror the trip-on-N pattern). No health flip.

## Batch B — broker startup robustness (`cmd/c3-broker`, `internal/broker`, telegram)
- **B1 (Important) offline-safe boot** — broker-lifecycle-1. `gotgbot.NewBot` does a blocking
  `GetMe` at construction; on a flaky-wake network that aborts `RegisterChannel`→`runDaemon`→
  exit. Fix: `DisableTokenCheck:true`; hand unreachability to the proven poll/heartbeat health
  machinery. Handle `@<missing>` username (populate lazily from first success / soften the log).
- **B2 (Important) fatal startup errors → broker.log** — broker-lifecycle-4. `runDaemon` fatals
  go to `os.Stderr`, which adapter-spawned brokers send to `/dev/null` → silent death, no
  breadcrumb. Fix: log every fatal via the stdlib `log` MultiWriter (already wired by
  SetupLogging) right before exit.
- **B3 (Important) malformed mappings.json resilience** — broker-lifecycle-2. A parse/validate
  error makes every boot fatally exit, silently. Fix: log loud to broker.log; try `path+'.bak'`
  and validate; if good run with it + warn. Do NOT silently run a skeleton (could mis-route).
- **B4 (Minor) pidAlive hardening — DROPPED (2026-06-22).** broker-lifecycle-5. Investigated and
  rejected: `pidAlive` is consulted ONLY after `flock` already FAILED (a live process holds the
  lock on this pid file → can only be a real broker). A reboot that leaves a stale pid file
  releases the dead holder's flock, so the next broker's flock simply succeeds and pidAlive is
  never reached. A comm/start-time downgrade in the flock-held path would only WEAKEN the
  singleton guarantee (a false "stale" lets a second broker unlink + win). flock is the
  authority. Left an explanatory NOTE in singleton.go instead.

## Batch C — STT venv + graceful degrade + dependency (live STT break)
- **Live status:** venv created at `~/.config/c3/stt-venv` (pyenv 3.12.3) with `sarvamai 0.1.28`.
  System `python3` is Arch-managed 3.14 (PEP 668) and lacks sarvamai — the cause of the 08:35
  all-providers-failed on every >30s note.
- **C1 STT uses the venv** — wire the Go STT shim/handler to prefer a configured/auto-detected
  venv python (`~/.config/c3/stt-venv/bin/python`) over bare `python3`; new
  `plugins.stt.python` config (auto-detect when unset). Decouples STT from the system python.
- **C2 requirements.txt** — `plugins/c3/stt/requirements.txt` (sarvamai; note ffprobe is a
  system dep). 
- **C3 install instruction everywhere** — `c3-broker setup` + `c3:build` skill + INSTALL.md:
  create venv + `pip install -r requirements.txt`, and PRINT the exact command. (Karthi
  explicitly: "add the pip install sarvam instruction in the right places. install and error msgs etc.")
- **C4 graceful ImportError** — sarvam provider catches `ImportError` → clear
  `RuntimeError("sarvamai not installed — run: <venv>/bin/pip install sarvamai")`.
- **C5 ffprobe-missing → REST** — stt-pipeline-4. `_get_duration` failure currently returns 999
  → forces the sarvamai batch path. Default to the FEWER-dependency REST path instead.
- **C6 total-failure Telegram notice** — stt-pipeline-3. On full STT failure send a best-effort
  Telegram reply to the originating chat/thread ("couldn't transcribe — please retype/resend")
  before exit; keep raw-voice fallback. token/chat_id/msg_id/thread_id are in scope.
- **C7 download-time budget** — stt-pipeline-5. Measure download elapsed and pass
  `remaining = 270 - elapsed` (floored) into run_stt so a slow download can't eat the
  provider-failover margin before the broker's 300s SIGKILL.

## Batch D — adapter IPC robustness (`cmd/c3-*-adapter`, `internal/broker`)
- **D1 (Critical) Codex reconnect-forever parity** — adapter-ipc-2. Codex reconnects once then
  dies on the next broker bounce. Port Claude's `recoverBroker()` ctx-aware backoff loop;
  thread run() ctx into brokerReader; reset the one-shot flag.
- **D2 (Important) reconnect-window inbound bounce** — adapter-ipc-1. On the alive-but-
  disconnected SKIP path in `worker.go`, bounce to the Telegram fallback instead of dropping
  (so the user is told to resend) — same path the STALE branch already uses.
- **D3 (Important) route-claim replay after broker restart** — adapter-ipc-3. Routes are
  in-memory; a broker restart strands non-attach sessions (Codex always). Give Codex
  rememberAttach/replayLastAttach; re-fire attach on reconnect; ensure Claude cwd-auto-claim
  records lastAttach.
- **D4 (Important) notify-failure not silently lost** — adapter-ipc-4. Broker counts
  "delivered" on IPC write; adapter→CLI render failure drops it. Minimum: log FULL content on
  notify FAIL so it's recoverable; assess a `nack` op to bounce to fallback.
- **D5 (Important) loud advisory when broker unrecoverable** — adapter-ipc-5. After ~M failed
  recovery cycles (~30s) surface a loud advisory via the existing status-line consumer.
- **D6 (Minor) Codex autoAttach correlation key** — adapter-ipc-7. autoAttach + tool-attach
  share the fixed `"attached"` pending key → startup race. Use unique per-call correlation IDs.
- **D7 (Minor) extract spawnDetached to a shared internal pkg** — design nit; both adapters
  duplicate it verbatim. One shared impl + one shared test.

## Batch E — health.json staleness (broker-dead detection)
- **E1 (Important) staleness-detectable health.json** — health-observability-2. `WriteHealthFile`
  only fires on edges + startup, so a dead broker leaves it frozen "green". Stamp broker pid +
  `written_unix`, refresh on a slow ticker (30–60s) regardless of edges; reader treats
  `pid not alive` OR `now-written_unix > 2x interval` as down/unknown. Update
  `~/.claude/statusline-command.sh` reader (.bak backup) + `docs/CHANNELS.md`.

## Batch F — systemd --user supervisor
- **F1 (Important) opt-in supervisor** — broker-lifecycle-3. Ship a `systemd --user` unit
  (Restart=on-failure, WantedBy=default.target) so a crashed/dead broker auto-restarts with no
  session open. Coexists with adapter spawnBroker (flock makes double-spawn safe). Opt-in;
  INSTALL docs + setup hint.

## Batch G — second Bot-API proxy (failover) [INFRA]
- **G1 (Important) provision a 2nd proxy** — proxy-network-1. Failover code is correct but needs
  >1 endpoint. Provision a second maintainer-owned proxy (different region — Singapore/Oracle
  options in memory) via the isolated proxy GCP project + deployer key (see memory
  `c3_tg_proxy_gcp_project`); configure `api_base_urls` in `~/.config/c3/mappings.json` (NOT the
  repo). Confirm region/provider/budget with Karthi before deploying. Likely its own focused effort.

## Deferred / not-fixing (with reason)
- **adapter-ipc-6** worker-exit race — verifier judged the proposed fix UNSOUND (the
  stopped-worker false-return is unreachable in production); leave as-is.
- **proxy-network-3** download per-socket timeout — low-pri hardening; the 300s SIGKILL + raw-voice
  fallback already bound the worst case. (May fold into C7.)

## Execution order & gates
A → B → C (also makes live STT work; rebuild+restart broker) → E → D → F → G.
Per batch: TDD, whole-module `go test`/`-race`, reviewer subagent, PII audit, commit, push.
Final: triple review (code-review-repo + independent lens + security), end-to-end verification,
ROADMAP + agent-memory update.
