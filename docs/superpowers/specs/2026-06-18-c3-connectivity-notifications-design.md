# C3 Connectivity Notifications — Design Spec

- **Date:** 2026-06-18
- **Status:** Approved by Karthi (revised after 3 grounded persona reviews; pending final spec review → implementation plan)
- **Author:** Claude Code (Opus 4.8) via superpowers:brainstorming
- **Review:** 3 independent persona reviews (Go-systems, product-fidelity, adversarial) run against the live source; findings validated and folded in (see "Review outcomes" at the end).

## Problem

When the broker cannot reach Telegram, `BrokerHost.NotifyHealth` fans out to **four sinks at once, all unconditionally** (a deliberate "redundant so nothing is missed" design):

1. **Status cache** — in-memory, powers `c3-broker status` (silent).
2. **Broker log** — a loud edge line (silent to the user).
3. **Desktop popup** — best-effort via `notify-send`/`zenity`; silently skipped if neither binary exists.
4. **CLI turn-injection** — the `⚠️ SYSTEM: Cannot reach telegram since 14:38 …` advisory, broadcast to **every live CLI session** and injected into the agent's turn by the Claude Code harness.

The CLI turn-injection (#4) is **invasive**: it lands in the agent's turn on every outage, unconditionally, regardless of whether the desktop popup already told the user. Karthi wants the priority inverted and an ambient indicator added.

The alert trigger is **inbound fetch-health only** (`getUpdates` poll + `getMe` heartbeat). An outage where outbound `SendReply` still works but `getUpdates` fails is still correctly classified DOWN, because the user-facing symptom — phone messages not arriving — is an inbound failure regardless of outbound state. Consistent with the existing invariant, the alert **never attempts to deliver via Telegram itself** (re-entering the dead-ish channel is the anti-pattern `host.go` + `reportHealth` already forbid).

## Goals

- **Desktop popup is the primary** invasive alert.
- **CLI turn-injection becomes a fallback**: it fires **only when the desktop popup could not be delivered**, and its text explicitly says the desktop notification was unavailable.
- **Add a passive, ambient status-line indicator** (in the Claude Code status line under the prompt) that shows the outage and **auto-clears when Telegram reconnects, even while the user is idle**.
- **A single toggle** silences the invasive tier (desktop + CLI fallback) while leaving the non-invasive status line always on.

## Non-goals (YAGNI)

- Multi-channel health UI — only `telegram` health exists today; the design is channel-keyed but we render only telegram.
- macOS `terminal-notifier`/`osascript` support — out of scope (Linux `notify-send`/`zenity` only, as today).
- Proving the popup was *seen* (D-Bus `org.freedesktop.Notifications` owner probe, screen-lock/DND detection) — out of scope; "delivered" is a best-effort proxy, the status line is the always-on backstop (see Component 2 + Known limitations).
- Distinguishing "telegram down" from "broker not running" in the status line — noted as a future enhancement; would need the runtime dir (socket/pid), not the state dir.
- A separate toggle for the desktop popup vs the CLI fallback — one toggle covers both invasive surfaces.
- Rate-limit/coalesce of the invasive tier beyond the existing edge de-spam (see Known limitations: flapping).

## Current architecture (as-is)

Health detection (per-channel state machine):

- `internal/channel/telegram/fetchhealth.go` — UP/DOWN state machine. **Seeds UP at construction** (`newFetchHealth`, ~line 67). Tracks `since`, `consecFails` (DOWN after 3), `lastReason`, `lastSuccess` (90s max-silence watchdog).
- `internal/channel/telegram/telegram.go` — `silenceWatchdog()` (30s ticker, ~315), `heartbeat()` (5m `getMe` probe, ~351), `reportHealth()` (fires only on UP↔DOWN edges, ~279) → `host.NotifyHealth(ev)`. **Three goroutines** (pollLoop, silenceWatchdog, heartbeat) can drive edges.

Fan-out:

- `internal/broker/host.go`
  - `NotifyHealth(ev)` (110–144) — the four sinks above; today each sink runs in its own goroutine with its own `defer/recover` (≈124–129).
  - `systemEventForHealth(ev)` (149–169) — format string at ≈159–160:
    `"Cannot reach %s since %s (%d consecutive %s). Your phone messages won't arrive until this recovers."` (`ev.Since.Format("15:04")`).
- `internal/broker/broker.go`
  - `setLastHealth` / `lastHealthSnapshot` (225–240) — in-memory `lastHealth map[string]HealthEvent` under `healthMu`.
  - `broadcastSystemEvent` (186–217) — wraps the `SystemEvent` as `Inbound{Kind: InboundSystem}`, writes to every live CLI session.
  - `Mappings()` (~100) — atomic snapshot of config (`atomic.Pointer[MappingsFile]`). **This is the only correct place to read config from inside `BrokerHost`.**
- `internal/broker/desktopnotify.go` — `newDesktopNotifier()` resolves `notify-send`/`zenity` at startup (`dn.bin`); `Notify(ev)` runs the popup via `cmd.Run()` with a 2s timeout (~100); `formatHealthPopup(ev)` renders the body (DOWN body ≈116 currently ends `… Inbound offline — alerts here only.`). **Today `Notify` returns no usable result** and is called fire-and-forget.
- Path helpers: `internal/broker/log.go` `LogPath()` (16–26) → `$XDG_STATE_HOME/c3` (fallback `$HOME/.local/state/c3`), **no `/state` suffix**. NOTE: `internal/broker/plugin_host.go` `stateRoot()` (~286) returns `$XDG_STATE_HOME/c3/state` (extra segment) and `cacheRoot()` uses `XDG_CACHE_HOME` — **must not be used for health.json** or the bash reader won't find it. `internal/broker/paths.go` `runtimeDir()` is a third root (socket/pid).

Config:

- **All config lives in `internal/mappings/types.go:MappingsFile`** (fields: SchemaVersion, Channels, Codex, Mappings, Plugins, Allowlist). Read via `mappings.Read` (`internal/mappings/io.go:18-22`, plain `json.Unmarshal`). **There is no separate "broker config struct."**
- `internal/mappings/clone.go` `MappingsFile.Clone()` deep-copies **field-by-field and silently drops any field it doesn't list**. It is on the copy-on-write mutation path: every attach/`UpsertTopic`/`UpsertMapping` → `Broker.mutateMappings()` → `Clone()` → `Store()` (broker.go ≈107–114).
- `internal/mappings/validate.go` `Validate()` gates on `SchemaVersion == 1`; unknown JSON fields are ignored by `encoding/json`.
- SIGHUP reload: `cmd/c3-broker/main.go` ≈287–296 does `Read → Validate → SetMappings` (stores the freshly-read pointer).

Types:

- `internal/c3types/types.go:115-122` — `HealthEvent{Channel, State (up|down), Since time.Time, Consec int, Reason string, DownFor time.Duration}`.
- `internal/ipc/messages.go:159-166` — `HealthEntry` wire form.

Existing read paths: `OpListHealth` IPC op (JSON over the unix socket) and `c3-broker status` (text). **No JSON file and no config toggle exist today.**

## Target architecture (to-be)

Split the existing sinks into two tiers inside `NotifyHealth`:

**Ambient tier — always on, never gated** (eventually-consistent with each other; the edge de-spam makes divergence rare):
- Status cache (`setLastHealth`) — unchanged.
- Broker log — unchanged.
- **NEW:** `health.json` state file (the read source for the status line).

**Invasive tier — gated by `notifications.invasive` (default `true`):**
- **Desktop popup (primary).**
- **CLI turn-injection (fallback)** — fires only if the desktop popup did **not** deliver; text notes the desktop notification was unavailable. **Down-edge only** — no CLI injection on recovery.

### Components

**1. `health.json` writer** *(broker, new)*
- **Path:** resolved via the **same convention as `LogPath()`** — `$XDG_STATE_HOME/c3/health.json` (fallback `$HOME/.local/state/c3/health.json`), sitting next to `broker.log`. Add a single `HealthFilePath()` helper in `log.go` so writer and any reader share one definition; **do not** use `plugin_host.go:stateRoot()` (it appends `/state`). Create the dir with `os.MkdirAll(..., 0700)` (as `SetupLogging` does); file mode `0600` (matching `broker.log`).
- **Atomic write contract** (mirror `internal/mappings/io.go:Write`): create the temp file in the **target's own directory** via `os.CreateTemp(filepath.Dir(path), ...)` — a cross-filesystem rename is a copy, not atomic, and a unique temp name (CreateTemp) means concurrent writers can't clobber a shared `.tmp`. Then `rename(2)` into place. **No fsync** — this is a best-effort status file; a lost write just means a briefly-stale indicator, not corruption (the reader always sees one complete generation because rename is atomic). This is a deliberate choice.
- **Writes the current broker snapshot** (`lastHealthSnapshot()`), channel-keyed:
  ```json
  {"telegram": {"state": "down", "since_unix": 1718722680, "since_hhmm": "14:38", "reason": "dial failures", "consec": 3}}
  ```
  `since_hhmm` is `ev.Since.Format("15:04")` (same local-time format as the existing popup/system-event, so all surfaces agree) — precomputed so the bash reader needs no `date` call; `since_unix` is kept too so the script can recompute if ever needed.
- **Startup write:** call `writeHealthFile(lastHealthSnapshot())` **directly** (ambient-only — NOT through `NotifyHealth`, so it can never trigger a spurious popup) once during broker startup, after channels register. At boot `lastHealth` is empty, so this writes `{}` ⇒ the status line shows nothing. **Rationale (diverges from one review rec — see Review outcomes):** the file persists across runs, so a prior crash *while DOWN* would otherwise leave a stale `down` file forever (false-red) on a now-healthy restart. Writing the empty boot snapshot **clears any stale file** without falsely asserting up *or* down — `{}` is honest "no data yet." The seed-UP lives in `fetchHealth`'s internal state, not in the broker's `lastHealth`, so we never write a false `up`. The only residual window: if Telegram is *still* down at restart, the indicator is blank for up to ~90s until detection re-trips and writes `down` — acceptable and self-correcting.
- Best-effort: a write error is logged and otherwise ignored (must never break the health path).

**2. `Notify` returns a delivery result** *(broker, modified — `desktopnotify.go`)*
- Change the `healthNotifier` interface + `Notify(ev)` to return whether the popup was **delivered** (e.g. `(delivered bool)`).
- **Definition (explicit, honest):** "delivered" = **the notifier binary was resolved AND `cmd.Run()` returned a zero exit within the 2s timeout.** It is a *proxy for "spawned successfully," NOT proof the user saw anything.* `notify-send`/`zenity` exit 0 as soon as the call is handed to the session bus — they return success even with the screen locked, DND on, or **no notification daemon running at all**. So the reliable, common-case `!delivered` trigger is **missing binary** (`dn.bin == ""`, the real headless/SSH case); exit-0 false-positives are accepted (see Known limitations). `zenity --notification`'s exit code is even weaker; treat both the same.

**3. Gating logic** *(broker, modified — `NotifyHealth`)*
```
// read config ONCE, before any goroutine, via the atomic snapshot:
invasive := h.broker.Mappings().InvasiveNotifications()   // nil-safe; default true (Component 5)

// ambient — always, synchronous on the caller goroutine:
setLastHealth(ev)
brokerLog(ev)
writeHealthFile(lastHealthSnapshot())     // new

// invasive — gated:
if invasive {
    go func() {
        defer recover-and-log()                 // a panic here must never crash the broker
        delivered := h.desktopNotifier.Notify(ev)   // synchronous in this goroutine (≤2s)
        if !delivered && ev.State == down {          // down-edge only; recovery never injects
            broadcastSystemEvent(systemEventForHealth(ev, true /* desktopUnavailable */))
        }
    }()
}
```
- The whole invasive block runs in **one goroutine** (with its own `defer/recover`), so the health path never blocks. When the desktop popup is delivered, **zero CLI injection** — matching "only a desktop notification going."
- The config bool is read **once, before** the goroutine (a single `atomic.Pointer.Load` is safe; reading inside the goroutine would risk a torn read against a concurrent `SetMappings`).

**4. `systemEventForHealth` fallback variant** *(broker, modified)*
- Concrete signature: `systemEventForHealth(ev c3types.HealthEvent, desktopUnavailable bool) *c3types.SystemEvent` (Go has no named args). When `desktopUnavailable` is true, append a clause such as: `" (desktop notification unavailable — shown here instead)"`.
- Update the single existing caller (`host.go` ≈142). Blast radius is one file: `broadcastSystemEvent` + `systemEventForHealth` are only called from `host.go` (and `health_notify_test.go`, which calls `broadcastSystemEvent` directly).

**5. Config toggle** *(in `internal/mappings`, new)*
- Add to `MappingsFile` (`internal/mappings/types.go`):
  ```go
  Notifications *NotificationsConfig `json:"notifications,omitempty"`
  // ...
  type NotificationsConfig struct {
      Invasive *bool `json:"invasive,omitempty"` // nil ⇒ default true
  }
  ```
  **Pointer, not bare bool:** a Go `bool` zero-values to `false`, so a plain bool would default the toggle *OFF* for every existing user who never set it — the opposite of intent. `*bool` (nil ⇒ absent ⇒ true) preserves current behavior.
- Nil-safe resolver:
  ```go
  func (mf *MappingsFile) InvasiveNotifications() bool {
      if mf == nil || mf.Notifications == nil || mf.Notifications.Invasive == nil {
          return true
      }
      return *mf.Notifications.Invasive
  }
  ```
- **`Clone()` MUST deep-copy `Notifications`** (`internal/mappings/clone.go`). `Clone` drops any field it doesn't explicitly list, and it's on the attach/upsert COW path — without this, the first attach after setting `invasive:false` silently reverts the live snapshot to default. Allocate a new `NotificationsConfig` and a new `*bool`. Add a `clone_test.go` case asserting `invasive:false` round-trips through `Clone`.
- **`Validate()`:** no change required (the field is a pointer with a safe default; a misspelled/unknown key is ignored by `encoding/json` and degrades to the default — intentional). State this explicitly so it's a choice, not an oversight.
- **Read site:** `NotifyHealth` reads `h.broker.Mappings().InvasiveNotifications()` (nil-safe). SIGHUP reload works because `SetMappings` stores the freshly-read pointer; no `validate.go` change needed.
- `mappings.json`:
  ```json
  "notifications": { "invasive": true }
  ```
- Rationale for `invasive`: the status line is the non-invasive surface (always on); this one switch governs the invasive tier. `invasive: false` ⇒ only the status line remains.

**6. Status-line integration** *(personal dotfiles — OUTSIDE the c3 repo, edits approved by Karthi)*
- Augment `~/.claude/statusline-command.sh` by inserting a fail-safe segment **immediately before the final `printf "\033[0m"`** (the script's last statement). The existing script has no `set -e` (verified), and already hard-depends on `jq` for every field, so this introduces **no new failure mode**. Use a single `jq` call:
  ```bash
  # C3 telegram connectivity (ambient, fail-safe)
  health_file="${XDG_STATE_HOME:-$HOME/.local/state}/c3/health.json"
  if [ -f "$health_file" ]; then
    read -r tg_state tg_since < <(jq -r '[.telegram.state // "", .telegram.since_hhmm // ""] | @tsv' "$health_file" 2>/dev/null)
    if [ "$tg_state" = "down" ]; then
      printf " · \033[31m⚠ TG offline%s\033[0m" "${tg_since:+ $tg_since}"
    fi
  fi
  ```
  Missing file, `jq` error, or `state != "down"` ⇒ prints nothing.
- Add `refreshInterval` **INSIDE the existing `statusLine` object** in `~/.claude/settings.json` (NOT as a top-level key — Claude Code reads it only there; a top-level key is silently ignored and the segment would never auto-clear while idle). The existing block becomes:
  ```json
  "statusLine": {
    "type": "command",
    "command": "bash ~/.claude/statusline-command.sh",
    "refreshInterval": 5
  }
  ```
  Per the live Claude Code docs, `refreshInterval` (minimum 1) is **additive** to event-driven updates (after each assistant message / `/compact` / mode change), so it matters precisely during idle — which is the intended auto-clear case. **This nesting is load-bearing: it is the sole mechanism for idle auto-clear.**
- These two files are Karthi's personal dotfiles, **not** part of the c3 repo.

## Data flow

- **DOWN edge:** state machine → `reportHealth` → `NotifyHealth`:
  - read `invasive` once; ambient: cache + log + `health.json{telegram.state:"down"}`.
  - invasive (if on): desktop popup; if `!delivered` → CLI injection with the "desktop unavailable" note.
  - status line: next refresh (≤5s) reads `down` → red `⚠ TG offline 14:38`.
- **RECOVERY (UP) edge:** `NotifyHealth`:
  - ambient: cache + log + `health.json{telegram.state:"up"}` ← this flip is what clears the status line.
  - invasive (if on): desktop popup ("reachable again"); **no CLI injection**.
  - status line: next refresh reads non-`down` → segment disappears (closure).
- **Toggle off (`invasive:false`):** ambient tier still runs (status line keeps working); desktop + CLI never fire.
- **Startup:** direct `writeHealthFile(empty snapshot)` ⇒ `{}` ⇒ status line blank; clears any stale file from a prior crash.

## Error handling / edge cases

- `health.json` write failure → logged, ignored.
- **Desktop "delivered" is best-effort, not proof of view.** A running-but-headless/locked/DND session reports `delivered=true` and suppresses the CLI fallback. The reliable `!delivered` trigger is a missing binary (the common headless/SSH case). The **status line is the always-on backstop** for any case where the popup is reported-delivered-but-unseen. Documented limitation, not a bug.
- Toggle off → no invasive output, but the status line still updates (ambient).
- **Broker crash while DOWN, then restart:** the startup empty-snapshot write clears the stale `down` file → blank; if the outage persists, detection re-trips within ~90s and re-reds. **Broker crash while DOWN and never restarted:** indicator goes blank at next… (n/a — broker not running). A *running* broker that stays DOWN keeps the red correct. Future enhancement (non-goal now): have the script also check broker pid/heartbeat (runtime dir) to distinguish "telegram down" from "broker not running."
- **Flapping** (down→up→down) is the only residual invasive-spam vector: each genuine edge re-fires a popup (and a CLI fallback on down edges with no desktop). It is bounded by the 3-consecutive-fail DOWN threshold + first-success recovery, so sub-second flapping can't occur. **Decision: no extra coalescing (YAGNI).**
- Ambient surfaces (status cache vs `health.json`) are **eventually-consistent, last-writer-wins per surface**; the edge de-spam makes divergence rare and both converge.
- Status-line script is fully fail-safe: any read problem ⇒ silent (never breaks the existing status line).

## Testing

Unit (`internal/broker`; the existing `health_notify_test.go` provides the seam — `fakeNotifier`, `b.desktopNotifier` injection, `net.Pipe` agent conn):
- **Interface signature change**: extend `fakeNotifier` to return a controllable `delivered` bool (the current fake won't compile against the new `healthNotifier` signature — required edit, call it out).
- desktop **delivered=true** on a DOWN edge ⇒ **no** CLI broadcast.
- desktop **delivered=false** (DOWN edge) ⇒ CLI broadcast fires **and contains the "desktop unavailable" note**.
- **recovery (UP) edge** ⇒ **no** CLI broadcast **and** `health.json` flips to `state:up` (the clear).
- `invasive:false` ⇒ neither desktop nor CLI fire, **but** cache + `health.json` are still written.
- `health.json` writer: valid JSON, expected fields, atomic (temp-in-same-dir + rename); **concurrent-edges** test (two `NotifyHealth` calls) → file is always one complete generation.
- **Config default matrix** (three cases): `notifications` block entirely absent ⇒ invasive=true; block present but empty ⇒ true; `invasive:false` explicit ⇒ false.
- **Clone**: `invasive:false` round-trips through `MappingsFile.Clone()` (guards against the silent-drop).
- **Path resolution**: `HealthFilePath()` resolves the same absolute path with `XDG_STATE_HOME` set vs unset; matches the bash reader's `${XDG_STATE_HOME:-$HOME/.local/state}/c3`.
- **SIGHUP reload** (pattern: `reload_config_test.go`): `invasive:true → false` flips behavior.

Manual end-to-end:
- Force DOWN (point the Bot-API endpoint at a black hole) → desktop popup → status line red → restore → desktop "reachable" popup, status line clears within ~5s while idle.
- Make `notify-send`/`zenity` unresolvable → CLI fallback injection appears with the note.
- Run the augmented status-line script against crafted `health.json` (down / up / `{}` / missing) and confirm output.
- Confirm writer and reader resolve the same absolute path under both `XDG_STATE_HOME` set and unset.

## Files touched

In-repo:
- `internal/broker/host.go` — `NotifyHealth` gating (read invasive once; one invasive goroutine with recover); `systemEventForHealth(ev, desktopUnavailable bool)` + update its one caller.
- `internal/broker/desktopnotify.go` — `healthNotifier` interface + `Notify` return `delivered bool`; reword the DOWN popup body so it doesn't contradict the new CLI-fallback wording (drop/adjust "alerts here only").
- `internal/broker/broker.go` — `writeHealthFile(snapshot)`; startup direct call.
- `internal/broker/log.go` — `HealthFilePath()` helper (single path definition).
- `internal/mappings/types.go` — `Notifications *NotificationsConfig` + `NotificationsConfig{Invasive *bool}` + `InvasiveNotifications()` helper.
- `internal/mappings/clone.go` — deep-copy `Notifications`.
- `internal/mappings/clone_test.go` — round-trip test.
- `internal/broker/*_test.go` — tests above (incl. extending `fakeNotifier`).
- `mappings.json` sample/docs + `docs/` — document the new `notifications` block.
- (No `validate.go` change — confirmed.)

Outside repo (personal dotfiles, approved):
- `~/.claude/statusline-command.sh` — insert the fail-safe segment before the final reset.
- `~/.claude/settings.json` — add `refreshInterval: 5` **inside** the `statusLine` object.

## Resolved decisions

- **Toggle name:** `notifications.invasive`, represented as `*bool` (nil ⇒ default `true`). Karthi dislikes "interrupt"; prefers invasive/non-invasive framing and delegated the final pick — chosen `invasive`.
- **Recovery behavior:** desktop popup + status-line clear only; **no CLI injection on reconnect** (Karthi's explicit instruction). **Accepted trade-off:** a CLI-only user with no desktop notifier learns of recovery solely via the passive status-line clear (which depends on the `refreshInterval` nesting). See Open question below for an optional narrow exception.
- **`refreshInterval`:** 5s, nested inside the `statusLine` object. Dotfile edits to `~/.claude/*`: approved.
- **Status-line read mechanism:** state file (`health.json`), not a live socket query or `c3-broker status` spawn — chosen so the status line can never hang on a wedged broker.
- **`delivered`:** best-effort proxy ("spawned to zero exit ≤2s"), not proof of view; status line is the backstop.
- **Startup write:** writes the empty boot snapshot to clear stale files (not skipped, not a synthesized UP).

## Open question for Karthi (one)

**Recovery closure when there is no desktop notifier.** Today's decision (no CLI injection on reconnect) means a CLI-only user who saw the DOWN fallback only gets closure from the status-line clear. A *narrow* exception would honor the spirit of "no recovery spam": emit a one-line CLI recovery note **only when the matching DOWN edge itself fell back to CLI** (i.e. desktop was unavailable that cycle) — "close the surface you opened." It adds a tiny bit of state (remember whether the last DOWN used the fallback). Default in this spec is **no exception** (respecting the explicit instruction); flagging it in case you want the closure.

## Review outcomes (what changed from v1, and the two recs I did NOT take verbatim)

Three persona reviews ran against the live source. **Validated and applied:** default-true `*bool` trap; config lives in `MappingsFile` + `Clone()` must copy it (was wrongly called a "broker config struct"); `refreshInterval` must nest inside `statusLine`; `delivered` exit-0 false-positive made explicit + documented; XDG path must use the `LogPath()` convention (not `stateRoot()`); atomic write = temp-in-same-dir + `os.CreateTemp` for concurrency; concrete `systemEventForHealth(ev, bool)` signature; defer/recover in the invasive goroutine; expanded test matrix; flapping/eventual-consistency/partial-failure notes; popup body reword; tightened line refs.

**Diverged from a recommendation, with reason:** the adversarial reviewer recommended **skipping** the startup write ("missing ⇒ blank is honest"). That is incomplete — `health.json` *persists across runs*, so a crash while DOWN would leave a stale `down` file (permanent false-red) on a healthy restart. The validated fix is to **write the empty boot snapshot** at startup (ambient-only, direct call), which clears the stale file *and* stays honest (`{}`, never a false `up`, because the seed-UP isn't in the broker's `lastHealth` yet). The go-systems reviewer's "write an initial UP snapshot" was likewise softened to "write the actual (empty) snapshot" to avoid a false-green during an ongoing outage.

**Deferred to Karthi:** the recovery-closure narrow exception (see Open question) — not silently added, since it touches his explicit "no CLI injection on reconnect."
