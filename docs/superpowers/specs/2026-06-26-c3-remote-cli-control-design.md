# C3 — Thread C/D: remote spawn + control of Claude Code / Codex CLIs (design)

**Date:** 2026-06-26 · **Branch:** to be cut off `master` (design lives on `docs/roadmap-2026-06-26`) · **Status:** hardened design spec, **no implementation** — decision-ready for Karthi.

This spec covers **Thread C** (control a CLI C3 owns — run slash commands, inject input, see status — from Telegram or another agent) and **Thread D** (make spawn-and-attach programmatic — callable via MCP, a `c3-broker` subcommand, or an API). They share one engine, so they are designed as one subsystem.

> **Reading order for the reviewer:** §0 (what changed and why the scope shrank) → §1 (architecture) → §6 (phasing / MVP) → §7 (open questions). Everything between is the detail.

---

## 0. This supersedes the old "PTY is the whole feature" plan

The previous framing lives in two places and is now **superseded**:

- `RESUME.md` §THE MAIN FEATURE (the 2026-06-01 handoff, ~`RESUME.md:167-230`): *"reliably control the terminal, TUI or not"*, reference to `helvesec/rmux`, and a **DECIDED** direction — C3 stays the brain; an **all-Go PTY stack** (`creack/pty` + `Netflix/go-expect` + a VT emulator) assembled as a new broker worker type; **arbitrary TUIs via raw PTY**; the three open questions **Q1/Q2/Q3** (`RESUME.md:208-210`); recommendation to *prototype the Go PTY stack first*.
- `ROADMAP.md` **P1 — Remote terminal-control** and the **2026-06-26 Thread C/D** section, which still carries the PTY-first decision as the existing home.

**What changed (2026-06-26 live-docs feasibility probe, CC v2.1.193 — captured in `MORNING-REVIEW-2026-06-26.md` "Probe C-lite"):** for sessions **C3 spawns and owns** there is a **GA, supported, no-PTY control plane**:

```
claude -p --input-format stream-json --output-format stream-json
```

(or the equivalent Agent SDK streaming mode). It gives, over the child's own stdio — **not** over the C3 channel surface:

- **input injection** (write a `user` message to the child's stdin),
- **interrupts**,
- the **dispatchable slash-command subset** sent as the prompt string: `/compact`, `/context`, `/usage`, `/clear`, and custom commands,
- structured **`system/init`** and **`result`** events (JSON) that expose model / tools / MCP servers / cost / token usage — these **replace `/status`** as a machine-readable status read.

**Why the scope shrank.** The genuinely hard part of the old plan was *rendering a constantly-redrawing TUI pane into a chat message* (the snapshot-on-idle / VT-emulator problem). With stream-json that problem **disappears** for the case Karthi actually asked for ("control a Claude on my server from my phone"): the child emits **structured turn events**, not a screen to scrape. So the MVP collapses from "a large PTY/VT subsystem" to **"a child-process manager + a programmatic spawn-and-attach surface."**

**What still needs PTY (the long tail, demoted to a later phase):**

- **Codex** — no Anthropic control surface; stream-json is Claude-only.
- **Arbitrary, pre-existing, human-started TUIs** — C3 didn't spawn them with stream-json wired, so it can only drive them as a terminal.
- **Exact `/status`** (the rendered panel) and **arrow-key option-menu navigation** — these are TUI-render / keystroke concerns.

The old **Q1/Q2/Q3** are partly resolved by this:

- **Q1 (C3 the brain vs rmux replaces part of C3)** → unchanged: **C3 stays the brain.**
- **Q2 (Rust rmux bridge vs all-Go PTY stack)** → for the **primary** path, *neither* — stream-json needs **no PTY at all**. The **all-Go PTY stack remains the chosen approach for the Phase-3 long tail** (Go-coherent; no Rust bridge).
- **Q3 (which agents — MCP-aware only vs arbitrary TUIs)** → both, but on **different drivers**: MCP-aware Claude on the stream-json driver (primary); Codex + arbitrary TUIs on the PTY driver (long tail).

**The old "can't ride the channel" constraint still holds and is honored.** A C3 channel has exactly three surfaces — push (`<channel>` blocks), the `reply` tool, and the permission relay — with **no slash-command injection and no permission-mode change** (`RESUME.md:177-184`, re-confirmed by probe A/C-lite). This spec does **not** try to control the *attached* session through its channel. It controls a **separate child process C3 owns**, out-of-band via that child's own stdio. That is the whole reason a managed session is a distinct concept from an attached session.

**Not a build path (mention only):** claude.ai / mobile **"Remote Control"** is a human UI (Anthropic-brokered, subscription-gated). It is the zero-build answer if Karthi just wants to steer his laptop session from his phone, but it is **not an API C3 can drive** — out of scope for the build.

---

## 1. Architecture

### 1.1 The new concept: a *managed CLI session*

Today C3 knows exactly one kind of session: an **adapter-attached session**. A human (or agent) launches `claude` / `codex` themselves; the C3 MCP adapter (`cmd/c3-claude-adapter`, `cmd/c3-codex-adapter`) dials **into** the broker over the unix socket, says `hello` (`internal/broker/handler.go:38-62`), and claims a route via `attach` (`internal/broker/attach.go:35`). The broker is **passive** — it receives a connection and tracks it as a `Stub` (`internal/broker/stubs.go:24`). The broker never started that process and cannot drive it.

A **managed CLI session** inverts this: **C3 is the parent.** The broker spawns the CLI child, **owns its stdio (or PTY)**, drives it, and supervises it. There is no MCP adapter in the loop for the managed child's *control* — the control plane is the broker reading/writing the child's pipes. (The child may still load C3's own MCP for its *own* outbound, but that is orthogonal.)

| | Adapter-attached session | **Managed CLI session (new)** |
|---|---|---|
| Who starts the process | human / external agent | **the broker** |
| How control flows | broker → adapter conn → CC harness | **broker → child stdio (stream-json) or PTY** |
| Broker's role | passive receiver | **active owner + supervisor** |
| Survives broker restart | yes (adapter reconnects, `handler.go:50-57`) | **no in MVP** (see §4.4) |
| Route holder | real `Stub` with `*ipc.Conn` | synthetic `Stub` whose sink is the **Driver** |
| Lifetime | the user's CLI process | bound to the broker process (MVP) |

### 1.2 Two drivers behind one interface

```go
// internal/broker/managed (new package, or internal/managed)

// Driver controls one managed CLI child. Implementations:
//   - streamJSONDriver (primary, Claude Code): owns child stdin/stdout pipes,
//     speaks `--input-format stream-json --output-format stream-json`.
//   - ptyDriver (long tail, Phase 3): owns a PTY master, drives via send-keys,
//     renders via a VT emulator snapshot.
type Driver interface {
    Kind() string                       // "stream-json" | "pty"
    Start(ctx context.Context) error    // spawn child; begin reading its output
    Inject(text string) error           // feed a user turn into the child
    Command(slash string) error         // dispatch a slash command (/compact, …)
    Interrupt() error                   // stream-json control interrupt; pty Ctrl-C
    Status() (ManagedStatus, error)     // stream-json: last system/init+result; pty: VT snapshot
    Stop() error                        // terminate child, release resources
}
```

The **Driver interface is the single seam** that makes the old PTY work a *plug-in for the long tail* rather than the spine of the feature. Phase 1 ships only `streamJSONDriver`; `ptyDriver` lands in Phase 3 behind the same interface with zero changes to the surrounding subsystem.

- **`streamJSONDriver` (primary):** `exec.Command("claude", "-p", "--input-format","stream-json", "--output-format","stream-json", …)` with `StdinPipe`/`StdoutPipe`. A reader goroutine decodes the child's JSONL event stream (`system/init`, assistant message deltas, `result`); `Inject`/`Command` write a `user` message frame to stdin. `--replay-user-messages` is used so the child echoes accepted user messages back as an ack (see §7 probe). No PTY, no VT emulator, no screen scraping.
- **`ptyDriver` (Phase 3):** the old plan, scoped down — `creack/pty` for the PTY, `Netflix/go-expect` for `wait_for_text` / quiescence, a VT emulator (`hinshun/vt10x` or `charmbracelet/x/vt`) for snapshot-on-idle. Drives Codex, arbitrary TUIs, exact `/status`, arrow-key menus.

### 1.3 How a managed session relates to routes/topics

A managed session is **bound to a `RouteKey`** (`internal/broker/route.go:9`) — exactly the `(channel, chat, topic)` triple an attached session claims. That binding is what lets us reuse the route machinery:

- **Inbound from the topic** (the human types in Telegram) flows through the normal poll → gate → `Host.Emit` → per-route `RouteWorker` path (`internal/broker/worker.go:124`). The worker **diverts** it to the Driver instead of an adapter conn (§1.4).
- **Output back to the topic** uses the same channel-neutral `ch.SendReply(...)` (`internal/channel/channel.go:24`) the worker already uses in `dispatchOutbound` (`worker.go:1006`). Long output auto-splits at 4096 in the channel — free.
- **Route ownership / collision / listing:** the managed session registers a **synthetic claim** in the `Routes` table (`internal/broker/routes.go:35`) via a synthetic `Stub` (CLI label `"claude-managed"`, `PID` = child pid, `Conn = nil`, alive via `isPIDAlive`, `stubs.go:110`). This is reuse, not a hack: it makes the route show as *taken* so a real `attach` to the same topic gets the existing **force_steal** proposal (`attach.go:559-576`), and it makes the managed session appear in `c3-broker sessions` / `status` / `claims` (`handler.go:225`, `handler.go:531`) **for free**.

Because the synthetic `Stub` has `Conn == nil`, the normal delivery path (`forwardOrFallback`, `worker.go:542`) would treat it as "alive but disconnected" and bounce. So the divert in §1.4 must run **before** that path is reached. That ordering is the one load-bearing hot-path change.

### 1.4 The divert — reusing the ask/perm pattern

The just-built `ask` and `perm` features (on `feat/telegram-permission-relay`) establish the exact pattern this subsystem reuses: **IPC-op + broker-side registry + worker callback-divert + register-before-send**. Concretely:

- `ask` diverts a callback in `RouteWorker.flushEvent` *before* `forwardOrFallback`: `if … "ask:" prefix → if w.broker.resolveAsk(w.key, cb) { return }` (`ask.go:resolveAsk`, called from `worker.go:470`).
- `perm` adds a transport-layer inbound interceptor and pushes its verdict via `Routes.Holder(route).ConnValue().(*ipc.Conn).WriteJSON(...)` — the **same delivery path as `OpInbound`** (`worker.go:610`), so it survives a same-process broker reconnect (`TransferAllByConnID`, `routes.go:113`).

Managed sessions divert at the **same two seams**, with the same "is there a live registry entry for this route?" check the ask/perm registries use:

```
RouteWorker.flushInbounds  (worker.go:333, text path) ┐
RouteWorker.flushEvent     (worker.go:470, event path)├─ if mgd := b.Managed.ForRoute(w.key); mgd != nil {
                                                       │       mgd.Handle(in, covered)   // → Driver.Inject / Command / Interrupt
                                                       │       return                    // suppress the normal holder/fallback path
                                                       │   }
```

- **Text inbound** → `mgd.Handle` classifies (§2.1): plain text → `Driver.Inject` (a turn); leading-`/` → `Driver.Command` (a slash command). It then consumes the covered durable-queue line(s) (the managed session is the live consumer, mirroring the `OpInboundDelivered` ack at `worker.go:617`), so backlog accounting stays correct.
- **Callback events** (control buttons, §2.2) → resolved by `b.Managed.resolve(route, cb)` with a `managed:` callback prefix, exactly mirroring `ask:` / `perm:` parsing and the resolve-once registry.

`b.Managed` is a new field on `Broker` (`internal/broker/broker.go:36`), constructed in `New` (`broker.go:91`) like `Asks` / `Perms`. The registry is mutex-guarded (one goroutine registers on spawn; route workers read/resolve), identical in shape to `askRegistry` / `permRegistry`.

### 1.5 Why not "managed = a loopback adapter" (rejected alternative)

The maximum-reuse alternative is to have the driver **dial the broker's own unix socket** and behave like a normal adapter (hello + attach), so a managed session is literally indistinguishable from an attached one and *zero* hot-path changes are needed (the transient `c3-broker-cli` clients already self-connect this way, `cmd/c3-broker/client.go:30`). **Rejected because:**

1. Karthi explicitly wants the managed session **distinct** from an attached session; the loopback approach erases the distinction.
2. A broker process opening a socket connection to itself to move in-process data is awkward and adds a bootstrap-ordering dependency (socket must be listening before the child dials back).
3. The divert pattern is already proven (ask/perm) and is a *single* guarded branch at two seams; it is the smaller, more honest change.

The first-class registry + divert is the recommendation. (This is also listed as an explicit fork in §7 in case Karthi prefers the loopback model's reuse.)

---

## 2. Thread C — control surface (human in a Telegram topic, or another agent)

### 2.1 Command routing on a managed route (the dividing line)

Once a topic has a managed session, every inbound on that route is interpreted as control:

| Input in the topic | Action | Driver call |
|---|---|---|
| plain text | inject as a **turn** | `Driver.Inject(text)` |
| `/compact`, `/context`, `/usage`, `/clear`, `/<custom>` | forward to the **child** as a slash command | `Driver.Command("/…")` |
| `/status` | **C3-rendered status** (route-aware, see below) | `Driver.Status()` |
| control buttons (Interrupt / Stop / Snapshot) | drive the wrapper | `managed:` callback (§2.2) |

**The `/status` collision.** `/status` is already a broker-owned bot command (queue depth) intercepted in `BrokerHost.HandleCommand` (`internal/broker/status_command.go:18`), which the telegram poll loop calls **gate-first** (`internal/channel/telegram/poll.go:826`). Make `HandleCommand` **route-aware**: if the inbound's topic has a managed session, `/status` renders the **managed** status (model / tools / MCP / cost / usage from the cached `system/init` + last `result`, or the VT snapshot for a PTY driver); otherwise it renders the existing queue status. This is a ~5-line change in one function and keeps the gate-first security ordering intact.

All other slash commands must **not** be intercepted by `HandleCommand` (it only owns `/status`); they flow normally to the worker, where the managed divert (§1.4) sends them to `Driver.Command`. This is why command-routing lives in the **worker divert**, not in `HandleCommand` — only the worker knows the route is managed and holds the Driver.

**Turn results + structured events render back** via `ch.SendReply` on the bound route:

- assistant turn text → one (auto-split) Telegram message,
- a compact `result`-event footer (turns, tokens, cost) appended or sent as a thin status line,
- a `system/init` summary surfaced on spawn ("🤖 claude managed here — model X, N tools, MCP: …") so the human sees what they're driving.

### 2.2 Control buttons — reuse the shipped P7 inline keyboards

Rather than make the human memorize control words, attach an inline keyboard to managed status/footer messages — **[⏸ Interrupt] [📸 Snapshot] [⛔ Stop]** — using the channel-neutral `Buttons [][]c3types.Button` already on `Outbound`/`ReplyArgs` (`internal/c3types/types.go:219,233`) and the `InlineKeyboards` capability (`internal/c3types/caps.go:80`). Taps come back as `InboundCallback` events and are resolved by a `managed:<sessionID>:<verb>` divert that mirrors `ask:` / `perm:` callback parsing and the resolve-once registry (`perm.go:parsePermCallback`, `ask.go:tapIndex`). Verbs: `interrupt`, `snapshot`, `stop`. callback_data carries only an opaque session id + verb — no secrets (matches the ask/perm keep-out).

This is pure reuse of P7 + the perm callback machinery; no new transport.

### 2.3 Agent-driven control (another Claude via the C3 MCP)

A second agent controls a managed session through **new MCP tools** (registered in `registerTools`, `cmd/c3-claude-adapter/main.go:1115`), which forward to the broker as new IPC ops (§3). These address a managed session **by id**, independent of the caller's own claimed route:

- `cli_send(session_id, text)` — inject a turn,
- `cli_command(session_id, command)` — dispatch a slash command,
- `cli_interrupt(session_id)`,
- `cli_status(session_id)` — returns the structured status,
- `cli_list()` — enumerate managed sessions,
- `cli_kill(session_id)`.

These are **broker-level ops**, not channel tools — they do **not** go through `dispatchTool` (`internal/broker/dispatch.go`, which maps channel methods like `reply`/`react`). They use the blocking request/response shape proven by `toolFetchQueue` (`main.go:1515`): pending map keyed by request id, `WriteJSON`, `select` on `ctx` / timeout / result channel.

---

## 3. Thread D — programmatic spawn-and-attach surface

The one operation: **"spawn a CLI of type X and attach it to channel Y / topic Z."** Three callable forms, all hitting the same broker handler.

### 3.1 The broker handler

New IPC ops on `internal/ipc/ops.go:11-44` (additive, omitempty — the wire-compat discipline the codebase already follows, e.g. `messages.go:143-152`):

```
OpSpawnCLI       Op = "spawn_cli"        // client → broker
OpSpawnCLIResult Op = "spawn_cli_result" // broker → client
OpListManaged    Op = "list_managed"
OpManagedInput   Op = "managed_input"    // send / command / interrupt, by session id
OpKillManaged    Op = "kill_managed"
OpManagedEvent   Op = "managed_event"    // (optional) broker → controlling adapter: async child output
```

`handleSpawnCLI(conn, raw)` (new, in a `handler_managed.go`, dispatched from the `HandleConn` switch at `handler.go:125`):

1. Parse `SpawnCLIReq{ CLI, Channel, TopicName, TopicID, Create, Driver, CWD, Model, SystemPrompt, InitialPrompt, Steal }`.
2. **Resolve the route by reusing the attach resolver** — the same logic `handleAttach` uses (`attach.go:84-102`): `target/topic_id/name`, default-group search, and `Create=true` → `channel.CreateTopic` (`attach.go:464`). This is where Thread D inherits "attach it to a named channel+topic, create if needed" for free, and why channels being pluggable (`internal/channel/channel.go:14`) means another chat API plugs in with no spawn-side changes.
3. **Authorize** (§5).
4. Build the `Driver` for `CLI`+`Driver` (default: `claude`+`stream-json`).
5. Spawn the child via a new `spawn.Managed` helper (§4.1), register the synthetic claim on the resolved `RouteKey` (`Routes.Claim`, `routes.go:35`; honor `Steal` via `ForceReleaseKey`, `routes.go:96`), register the managed session in `b.Managed`, start the supervisor (§4).
6. Reply `SpawnCLIResult{ OK, SessionID, ResolvedTopic, Err }`.

### 3.2 Form 1 — MCP tool `spawn_cli`

Registered in both adapters' `registerTools` (`main.go:1115`). Args: `cli` (`"claude"`|`"codex"`), `channel` (default the single configured channel, `attach.go:780`), `topic` (name or id) / `create`, `driver` (`"stream-json"`|`"pty"`), `cwd`, `model`, `system_prompt`, `initial_prompt`. Forwards `OpSpawnCLI`; blocks for `OpSpawnCLIResult` (the `toolFetchQueue` shape, `main.go:1515`). This is how "another Claude instance brings up and attaches a CLI" works.

### 3.3 Form 2 — `c3-broker spawn` subcommand

A new transient subcommand in the `cmd/c3-broker/main.go` switch (`main.go:52-131`), using `dialBroker` (`cmd/c3-broker/client.go:30`) to send `OpSpawnCLI` and print the result — same pattern as `c3-broker sessions` / `status`. Companions: `c3-broker cli-list`, `c3-broker cli-send`, `c3-broker cli-kill`.

```
c3-broker spawn --cli claude --channel telegram --topic myproj --create \
                --driver stream-json --cwd ~/arogara/myproj
c3-broker cli-list
c3-broker cli-send  <session-id> "run the tests and summarize"
c3-broker cli-kill  <session-id>
```

### 3.4 Form 3 — local HTTP/JSON API (deferred; YAGNI for MVP)

The unix-socket IPC + the CLI subcommand already give programmatic access from anything on the box. A local HTTP API is a thin wrapper over the same `OpSpawnCLI` for *remote* agents that can't reach the socket. **Defer** — note as a Phase-4 fork; do not build until there's a concrete remote caller. (Cross-link: ROADMAP "Programmatic (non-chat) channel extension".)

### 3.5 Lifecycle verbs (all forms)

`spawn` · `list` · `send` / `command` / `interrupt` (by id) · `kill` · `restart`. Each maps to one of the ops in §3.1.

---

## 4. Lifecycle & supervision

### 4.1 Spawn

`internal/spawn/spawn.go:32` (`Detached`) nils stdin/stdout/stderr, `setsid`s, and **fire-and-forget reaps** — perfect for the broker auto-spawn race but **wrong for a managed child**, which needs live stdin/stdout pipes and a supervised `Wait`. Add a sibling:

```go
// spawn.Managed prepares cmd for broker-owned, supervised execution: a NEW
// session (setsid) so the broker can signal the whole child process group on
// kill, but stdin/stdout KEPT as pipes (the control plane) and stderr captured
// to broker.log. The CALLER owns Wait() (the supervisor goroutine), unlike
// Detached's async reap.
func Managed(cmd *exec.Cmd) (stdin io.WriteCloser, stdout io.ReadCloser, err error)
```

(Mirrors `Detached`'s `Setsid: true` rationale at `spawn.go:48-53`; the PTY driver uses `creack/pty.Start` instead, which sets the controlling TTY.)

### 4.2 Crash handling + restart

One **supervisor goroutine** per managed session `Wait()`s the child. On unexpected exit:

1. log (broker.log) with the child pid + exit status,
2. post a notice to the bound topic ("⚠️ managed claude exited (code N)"),
3. release the synthetic claim (`Routes.Release`, `routes.go:130`) and remove the registry entry,
4. apply restart policy: **MVP = no auto-restart** (report and stop); Phase 2 adds opt-in restart-with-backoff modeled on the adapter's `recoverBroker` exponential backoff (`cmd/c3-claude-adapter/main.go:386`).

### 4.3 Panic supervision

Every driver/supervisor goroutine guards itself with `recoverGoroutine` / `recoverGoroutineThen` (`internal/broker/recover.go:26,36`) — the same discipline that keeps a poisoned route worker from taking down the broker (`worker.go:209`). A reaper for orphaned/zombie managed entries (child dead but registry stale) follows `StartAskReaper` / `StartHealthRefresh` (`ask.go:StartAskReaper`, `internal/broker/healthfile.go:125`): panic-supervised ticker, stops on `b.ctx` cancel.

### 4.4 Persistence across broker restart — **MVP: none, by design**

This is the sharpest difference from adapter-attached sessions and must be explicit:

- An adapter-attached session **survives** a broker bounce: the adapter holds the CLI process and **reconnects**, transferring its claims (`handler.go:50-57`, `routes.go:113`).
- A **stream-json managed child** is driven through **broker-held pipes**. If the broker exits, those pipes die. Even if the child is `setsid`'d into its own session and survives as an orphan, the **new** broker has no handle to its stdin/stdout — it **cannot re-adopt** it.

Therefore, **MVP policy:** on `Broker.Shutdown` (`broker.go:239`), **kill all managed children** (clean teardown) so no orphans accumulate. Managed sessions are **broker-process-lifetime-bound**. The broker may best-effort write a managed-session manifest (like the health file, `healthfile.go:61`) so a restarted broker can *report* "these were running and were terminated," but it does not resurrect them.

True persistence across broker restart requires a **re-attachable transport** (a PTY under a detachable multiplexer, or a side socket the child re-dials) — that is the Phase-3/4 PTY direction, where it's natural. Flagged as a fork in §7.

### 4.5 Shutdown ordering

Managed-session teardown slots into `Broker.Shutdown` (`broker.go:239`) **before** `Workers.Stop()` (so a driver mid-`SendReply` isn't racing channel teardown — the same drain-order reasoning that fixed the 2026-05-15 hang documented at `broker.go:220-238`): kill managed children → stop workers → stop channels → cancel ctx.

---

## 5. Security

Spawning a CLI **runs arbitrary code on the box** as the broker's Unix user, with the broker's environment and secrets. This is materially more powerful than sending a chat message, and the gate must reflect that.

- **Remote (Telegram) triggers require an operator.** Any spawn/control that originates from a Telegram command or callback is gated to the **allowlisted, DM-cleared operator** set — `b.Mappings().IsUserAllowed(actor.UserID)` — the **same higher-trust gate the permission relay uses** (`perm.go:resolvePerm` SENDER-GATE: a perm tap is honored only from an operator, "higher trust than a chat reply"). A non-operator command/tap is ignored. Inbound already passes `Host.GateInbound` (allowlist, `channel.go:67`) before reaching the broker; this adds the operator check on top for the spawn/control verbs specifically.
- **MCP `spawn_cli` from another agent** is as-trusted-as-the-user: that agent is a CLI the user already launched on the box. No *additional* network gate, but the broker logs every spawn (who/what/where) for audit. Document that exposing `spawn_cli` to a session means that session can start arbitrary children.
- **`c3-broker spawn` subcommand** = local shell access, already user-equivalent; no extra gate.
- **Trust boundary made explicit:** the managed child inherits the broker user's filesystem + network. Optionally constrain `cwd` and pass a reduced env; do **not** run the child with `--dangerously-skip-permissions` by default (§7). callback_data for control buttons carries only an opaque session id + verb (no secrets), matching the ask/perm keep-out.
- **The child's own permission prompts:** a headless `claude -p` still makes tool calls. MVP runs it in **default permission mode** and **relays its permission prompts to the bound topic via the Thread-B permission-relay machinery** — a clean convergence (the operator approves the managed child's actions the same way they approve an attached session's). This makes Thread B a soft dependency for a *safe* managed session; note it as a phase dependency, not an MVP blocker (MVP can ship with the child in a read-only / plan posture if Thread B isn't merged yet).
- Cross-reference: this is the same "higher-trust operator" concept as SSHGate's signer/gate and the trusted-operator PreToolUse hook spec (memory `c3_trusted_operator_authz_spec`). Reuse the conceptual model; do not re-invent an auth scheme.

---

## 6. Phasing (each phase independently shippable)

**Phase 0 — pre-build probe (gate; ~half a day).** Confirm in a small Go harness that a `claude -p --input-format stream-json --output-format stream-json` child **stays alive accepting multiple `user` messages across turns** on its stdin (not one-shot-and-exit), and that `--replay-user-messages` gives a usable per-message ack. This is the one assumption the whole MVP rests on. (See §7.) Do **not** run it against the live bridge — isolated harness only.

**Phase 1 — MVP: spawn-and-own one Claude session via stream-json on one topic.** The smallest end-to-end useful slice:
- `spawn.Managed` helper (§4.1); `streamJSONDriver` with `Inject` + `Command` + `Status` + `Stop`.
- `b.Managed` registry + synthetic claim + the worker divert at `flushInbounds`/`flushEvent` (§1.4).
- Command routing (§2.1): plain text → turn; `/<cmd>` → child slash command (gets `/compact` for free); route-aware `/status` → structured status.
- Turn-result + `system/init`/`result` rendering back to the topic via `ch.SendReply`.
- Thread D surface for spawn/list/kill via **both** `c3-broker spawn` (§3.3) and the `spawn_cli` MCP tool (§3.2).
- Supervision: one child, supervisor `Wait`, clean kill on broker shutdown (§4.5), panic guards. **No restart, no persistence, Claude-only, stream-json-only.**
- Tests (TDD, mirror the ask/perm test style): registry register/resolve; divert classification (text vs slash vs status); a fake driver asserting `Inject`/`Command` are called; spawn handler route-resolution reuse.

**Phase 2 — control polish + multi-session + agent control.**
- Control buttons (Interrupt / Snapshot / Stop) via the `managed:` callback divert (§2.2).
- `cli_send` / `cli_command` / `cli_interrupt` / `cli_status` / `cli_list` / `cli_kill` MCP tools + ops (§2.3, §3.1) — agent-driven control by session id.
- Multiple concurrent managed sessions; restart-with-backoff policy (§4.2); spawn-onto-named-topic-with-create (reuse `attach.go:464`).
- Child permission-relay convergence (depends on Thread B).

**Phase 3 — PTY long tail.** `ptyDriver` behind the same `Driver` interface (the old all-Go stack, now scoped): Codex, arbitrary pre-existing TUIs, exact `/status`, arrow-key menu nav; snapshot-on-idle rendering (`wait_for_text`/quiescence). This is the demoted "whole feature."

**Phase 4 — persistence + reach.** Re-attachable transport for restart-survival (§4.4); local HTTP API (§3.4); other channels for Thread D (the `Channel` interface is already pluggable, `channel.go:14`).

---

## 7. Open questions / pre-build probes — explicit forks for Karthi

1. **[GATE — probe] stream-json multi-turn stdin.** Does the `claude -p --input-format stream-json --output-format stream-json` child accept **multiple** `user` messages across turns and stay alive, with `--replay-user-messages` as the ack? If it's effectively one-shot, the MVP needs a different shape (re-spawn per turn, or the Agent SDK in-process). **Must run before Phase 1.**
2. **[FORK — architecture] managed-as-first-class-divert (recommended) vs managed-as-loopback-adapter.** §1.5. Recommendation: first-class divert (keeps managed distinct, smaller hot-path change, proven ask/perm pattern). The alternative buys maximum reuse at the cost of erasing the distinction Karthi asked for.
3. **[FORK — persistence] accept "managed children die on broker restart" for MVP (recommended)** vs invest now in a re-attachable transport so a managed session survives a broker bounce. §4.4. Recommendation: accept it for MVP; revisit in Phase 3/4 where PTY makes detach natural.
4. **[FORK — child safety] child permission posture.** Default-mode + relay the child's permission prompts via Thread B (recommended, safe) vs run the child read-only/plan-only until Thread B lands vs (rejected) `--dangerously-skip-permissions`. §5.
5. **[CONFIRM — UX] command dividing line.** Plain text → turn; `/<cmd>` → child; route-aware `/status` → C3 status; control via buttons. Confirm the `/status` route-aware override is acceptable (it changes what `/status` returns inside a managed topic).
6. **[CONFIRM — scope] Codex = PTY-phase only.** Probe says Codex has no Anthropic control surface, so MVP is **Claude-only** and Codex waits for Phase 3's `ptyDriver`. Confirm that's acceptable for "build now."
7. **[CONFIRM — tool timeout]** the blocking control/spawn MCP tools rely on Claude Code tolerating a long-ish tool call (the `toolFetchQueue` uses 120s, `main.go:1542`). Spawn is fast; a *turn* could be long — agent-driven `cli_send` should be **fire-and-then-poll** (return on accept, deliver the turn result as a later `OpManagedEvent`/topic message), not block for the whole turn. Confirm the async-turn model for agent control.

---

## 8. Non-goals (YAGNI)

- A full tmux/rmux replacement or a detachable multi-pane multiplexer. (We build the minimum child-process manager, not a terminal product.)
- A web UI / live terminal streaming. (Phase 4 *maybe*; not designed here.)
- Driving claude.ai/mobile **Remote Control** — not an API (§0).
- Controlling the **attached** session via the channel (slash-command injection / permission-mode change) — proven infeasible; the 3-surface limit stands (§0). We only control sessions C3 **owns**.
- Persisting stream-json managed sessions across broker restart (MVP) (§4.4).
- Codex in the MVP (§7.6).
- A bespoke auth scheme — reuse the allowlist/operator gate (§5).

---

## Appendix — code-grounding index (every integration claim, with file:line)

- Per-route worker model + job kinds + run-loop panic backstop: `internal/broker/worker.go:16`, `:124`, `:201`, `:209`.
- Worker divert seams (text / event): `internal/broker/worker.go:333` (`flushInbounds`), `:470` (`flushEvent`); normal delivery to a holder conn: `:542` (`forwardOrFallback`), `:610` (`WriteJSON(OpInbound)`), live-ack consume `:617`; outbound `ch.SendReply` path `:1006`.
- Channel + Host contracts: `internal/channel/channel.go:14` (`Channel`), `:24` (`SendReply`), `:43` (`Host`), `:67` (`GateInbound`), `:83` (`HandleCommand`).
- Capabilities / buttons: `internal/c3types/caps.go:80` (`InlineKeyboards`); `internal/c3types/types.go:156` (`CallbackEvent`), `:219` (`Outbound.Buttons`), `:233` (`Button`), `:243` (`ReplyArgs`).
- IPC ops + wire types + additive-omitempty discipline: `internal/ipc/ops.go:11`, `internal/ipc/messages.go:13`, `:126` (`HelloMsg`), `:143-152` (additive `Capabilities`).
- Connection dispatch + tool-call route resolution: `internal/broker/handler.go:21` (`HandleConn`), `:50-57` (reconnect/claim transfer), `:125` (op switch), `:164` (`handleToolCall`), `:225` (`handleListClaims`), `:531` (`handleListSessions`).
- Attach resolution reused by spawn: `internal/broker/attach.go:35` (`handleAttach`), `:84-102` (target branch), `:464` (`createAndClaim`), `:559-576` (force_steal), `:780` (`defaultChannel`).
- Route table + key + liveness: `internal/broker/route.go:9` (`RouteKey`), `internal/broker/routes.go:35` (`Claim`), `:96` (`ForceReleaseKey`), `:113` (`TransferAllByConnID`), `:130` (`Release`), `:166` (`Holder`); `internal/broker/stubs.go:24` (`Stub`), `:101` (`IsAlive`), `:110` (`isPIDAlive`).
- Spawn / detach semantics: `internal/spawn/spawn.go:32` (`Detached`), `:48-53` (`setsid` rationale); adapter spawn+dial+reconnect: `cmd/c3-claude-adapter/main.go:256` (`connectBroker`), `:279` (`spawnBroker`), `:386` (`recoverBroker` backoff).
- Blocking-tool / pending-map pattern for new control tools: `cmd/c3-claude-adapter/main.go:1115` (`registerTools`), `:1515` (`toolFetchQueue`), `:1542` (120s timeout).
- Supervision / lifecycle primitives: `internal/broker/recover.go:26` (`recoverGoroutine`); `internal/broker/healthfile.go:125` (`StartHealthRefresh`), `:61` (`WriteHealthFile`); `internal/broker/singleton.go:23` (`AcquireSingleton` flock); `internal/broker/broker.go:91` (`New`), `:36` (registries on `Broker`), `:239` (`Shutdown` drain order); `internal/broker/server.go:43` (`Listen`).
- CLI subcommands + transient client for Thread D: `cmd/c3-broker/main.go:52-131` (subcommand switch), `cmd/c3-broker/client.go:30` (`dialBroker`); `/status` intercept: `internal/broker/status_command.go:18`, gate-first ordering `internal/channel/telegram/poll.go:826`.
- Reused ask/perm substrate (on `feat/telegram-permission-relay`): `internal/broker/ask.go` (register-before-send, `resolveAsk`, `StartAskReaper`), `internal/broker/perm.go` (`resolvePerm` operator sender-gate, `deliverPermVerdict` via holder conn), spec `docs/superpowers/specs/2026-06-26-c3-telegram-ask-roundtrip-design.md`.
- Superseded prior framing: `RESUME.md:167-230` (PTY-first "main feature", Q1/Q2/Q3), `ROADMAP.md` P1 + the 2026-06-26 Thread C/D section; probe results `MORNING-REVIEW-2026-06-26.md` ("Probe C-lite").
