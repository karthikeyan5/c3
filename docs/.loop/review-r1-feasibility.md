# Review R1 — Implementation Feasibility (v5 spec + foundation plan)

## Verdict

**Yes, with these clarifications.** A solid Go developer can land Phases 1-2 from the foundation plan unaided — that plan is unusually well-specified (TDD, full code, expected outputs). Phases 3-9 in the spec, however, hand-wave several places where the engineer would need to re-derive design. The biggest blocker: the named `modelcontextprotocol/go-sdk` (v1.6.0, last verified) does **not** expose an API to send arbitrary custom JSON-RPC notifications, and the Claude adapter spec depends on emitting `notifications/claude/channel`. This must be resolved before §4.4 can be built.

## Underspecified interfaces

- **`Channel` interface lives in two docs with different signatures.** Spec §4.1 shows `EditMessage(args EditArgs) error`; `docs/CHANNELS.md` shows `EditMessage(args EditArgs) (*EditResult, error)` and adds `React` + `DownloadAttachment`. Pick one canonical Go interface block and reference it from both.
- **`Host` (channel-side) and `plugin.Host` are referenced but never defined.** `docs/CHANNELS.md` and `docs/PLUGINS.md` use `host.Config`, `host.Inbound`, `host.Logf`, `host.Channel(name)`, `host.State`, `host.CacheDir`, `host.RegisterTool`, `host.Done` — no struct definition exists. Recommend a single Go block in spec §4.5 or a new `internal/plugin/host.go` interface excerpt.
- **`InboundEvent` vs `Inbound`** — spec §4.1 uses `InboundEvent`; `CHANNELS.md` uses `Inbound`; `PLUGINS.md` uses `*Inbound`. Same shape, three names. Pick one.
- **IPC wire schema is a table, not a struct.** §4.4's IPC ops table lists fields in prose. Concrete Go structs (`HelloMsg`, `HelloAckMsg`, `AttachReq`, `AttachResp`, `InboundMsg`, `ToolCallMsg`, `ToolResultMsg`, `ErrorMsg`) with their JSON tags would prevent two implementers building drift.
- **`ReplyArgs`, `EditArgs`, `ReactArgs`, `Sender`, `Attachment`, `ReplyContext`, `VoicePayload`, `Mapping`, `Session`** — all referenced, none defined in the doc surface. Mappings types exist in the plan; the rest don't.
- **Tool registration return shape.** `RegisterTools` in `PLUGINS.md` shows `Handler func(ctx, args) (any, error)` returning a string; how does this become MCP `content[].text`? The marshalling rule needs one example.

## Library readiness

- **gotgbot/v2 — green.** Latest is `v2.0.0-rc.34` (Feb 2026). `CreateForumTopic`, `EditForumTopic`, `SendChatAction` (with `MessageThreadId` opts), `EditMessageText`, `SetMessageReaction`, `GetUpdates` with `Timeout` long-poll all confirmed. `GetUpdatesOpts.AllowedUpdates []string` accepts arbitrary update names including `message_reaction`. **Pin to a specific rc** in `go.mod`; rc numbers move.
- **modelcontextprotocol/go-sdk — yellow, with one red sub-issue.** v1.6.0 is stable. Stdio transport, tool registration with input schema, `Instructions` on `ServerOptions` and `InitializeResult`, `Ping`, structured `Log` notifications all present.
  - **Red sub-issue:** the SDK has **no public API to send arbitrary custom notifications**. `ServerSession` exposes `NotifyProgress`, `Log`, `ResourceUpdated`, list-changed — that's it. The connection (`getConn()`) is unexported; there is no `Send` / `Notify(method, params)` escape hatch. The Claude adapter's `notifications/claude/channel` cannot be sent through this SDK as-is. Three options the spec must pick: (a) upstream a PR adding `ServerSession.Notify(method, params)`, (b) bypass the SDK for stdio-write of inbound notifications and frame the JSON-RPC manually on the same fd, (c) repackage inbound as `notifications/message` (Codex already does this) and have the Claude adapter use that path too — losing the rich `<channel>` rendering. Decision needed pre-Phase 6.
- **Claude Code's `notifications/claude/channel` itself** is not a standard MCP method — assumption is Claude Code's MCP host accepts it because the existing bun plugin emits it. Verify the existing message format byte-for-byte before depending on rich rendering.

## Missing implementation detail

- **flock singleton — path and stale-lock behavior unstated.** Recipe needed: which file (`/tmp/c3.sock.lock` vs `~/.config/c3/broker.lock`?), what happens if a previous broker crashed leaving the lock file (use `LOCK_NB`, fall through if EWOULDBLOCK after stat-checking pid liveness), how the adapter spawns broker with `setsid` / detached process group.
- **Atomic rewrite recipe is in the plan, not the spec.** The plan (Task 2.3) specifies tempfile-then-rename + chmod 600 correctly; the spec just says "atomic rewrites." If a different team builds Phase 3 before reading the plan, they'll re-derive. Hoist into spec §4.3.
- **Codex `--remote` WebSocket forwarder — Origin header.** §4.4 says "gorilla/websocket does not set Origin by default." `gorilla/websocket` v1.5+ requires the caller to construct headers; the default `Dial` does send a `Host`-derived Origin in some versions. Spec should give the explicit `http.Header{}` construction (empty header → no Origin) and call out that `nhooyr/websocket` or `coder/websocket` may behave differently if the engineer substitutes.
- **Telegram `validate_topic` via `sendChatAction`.** The visible side effect is a typing indicator firing in the topic — harmless but visible. Spec should acknowledge this and consider the alternative (`getChat` doesn't accept thread_id; `editForumTopic` with no-op fields requires `can_manage_topics`). Pick one and document the trade.
- **Debounce buffer overflow.** §7.3 says "buffers inbound for `debounce_ms` after each new message" — what if 1000 messages arrive in 1.5s? Cap, drop policy, or unbounded? Spec is silent.
- **Typing-indicator lifecycle on stub crash.** §7.1 ticks every 4s while in-flight > 0. If the adapter dies mid-call, what decrements the counter? Probably broker-side cleanup on stub disconnect — call it out.
- **`mappings.json` corruption recovery.** Atomic write protects against half-write; what about a manually-edited file with bad JSON? Spec's `Validate()` (in plan task 2.9) returns errors but doesn't say what the broker does on boot — abort, or fall back to skeleton?

## Distribution gotchas

- **`go install ./cmd/...` requires `$GOBIN` (or `$GOPATH/bin`) on PATH.** Not stated in `INSTALL.md` (spec assumes it). On macOS Homebrew Go users, `$GOBIN` defaults to `$HOME/go/bin`, not on PATH. The `/c3-build` slash command should at minimum echo "ensure `$(go env GOBIN || echo $HOME/go/bin)` is on PATH" and ideally fail fast if the binary isn't reachable post-install.
- **`go.mod` Go version pin.** Plan says `go 1.22`; rc.34 of gotgbot may require newer. Spec should commit to a floor (`go >=1.23` is current safe minimum for new projects) and document it in `INSTALL.md`.
- **Dependency pinning.** No `go.sum` strategy stated. Installing from an arbitrary commit risks rc-version drift. Tag releases and pin in `go.mod`.
- **NVM symlink walk in `install-codex-shim`.** Spec walks `~/.nvm/versions/node/*/bin/`. Doesn't handle: nvm not installed (skip silently? error?), Volta, fnm, asdf, Homebrew node, system node at `/usr/local/bin/node`. Document the supported subset and fail-loudly path for the rest.
- **`/tmp/c3.sock` on multi-user systems.** Mode/owner not specified. If two users on the same box run C3, second user's broker fails to bind. Should be `$XDG_RUNTIME_DIR/c3.sock` (per-uid) or `/tmp/c3-$UID.sock`.

## Edge cases that need handling

- **Concurrent broker spawn race.** Two adapters start simultaneously, both find `/tmp/c3.sock` missing, both spawn `c3-broker`. flock saves the second from running, but the second adapter's connect-with-backoff needs to handle "broker started by sibling" — clarify retry budget.
- **Disconnect/reconnect ordering.** `ADAPTERS.md` says "reconnect once" + re-claim. What if the broker restarted with different mappings (user edited the file)? Re-claim might point to a different topic. Need a versioned handshake or claim-token.
- **Telegram rate-limit specifics.** Spec mentions `parameters.retry_after`. Doesn't mention 429 vs 420, global vs per-method limits, or `createForumTopic`'s undocumented but tight limit (~20/min observed). The proposal-then-create flow could trigger this if a user mass-attaches.
- **Forum topic id collision.** §5.4 stores unknown topic-id-by-validate as `topic-412`. If user later creates a real "topic-412", names collide in `LookupTopicAcrossGroups`. Document the rename path.
- **Codex `--remote` thread discovery race.** §4.4 step 4 calls `thread/loaded/list`; if the user has no threads loaded yet (fresh `codex` invocation, first turn), what happens? Loop with backoff? Refuse? The Python POC's behavior here should be transcribed.
- **NVM symlink overwrite safety.** `install-codex-shim` "replaces only if the existing target is already our binary." What's the detection — name match, hash, magic byte? If a user has their own `~/.local/bin/codex` shim, we'd skip and confuse them. Spec should specify (e.g., readlink + check it points into `$GOBIN`).
- **Broker → adapter `inbound` while adapter is mid-`tool_call`.** Same socket, interleaved frames. Newline-JSON is fine if both sides use a single writer with mutex; spec should mandate that.
- **`os.Getenv("HOME")` in `DefaultPath`.** Plan task 2.10 falls back to `$HOME/.config/c3/mappings.json` even when HOME is unset (would produce `/.config/c3/mappings.json`). Use `os.UserHomeDir()` — also handles Windows if that's ever in scope.

## Recommendation

**Spike key uncertainties first, then proceed to build.** Concretely:

1. **One-day spike: prove `notifications/claude/channel` over go-sdk.** Either find an undocumented `ServerSession` escape hatch, write the upstream PR, or fall back to manual JSON-RPC framing on the stdio fd. Cannot start Phase 6 without an answer.
2. **One-day spike: confirm `gotgbot` v2 rc -> stable timeline** or accept building on an rc; pin exact version in `go.mod`.
3. **Half-day doc pass: hoist all the type definitions** (`Inbound`, `Host`, IPC structs, `ReplyArgs` etc.) into spec §4 as concrete Go blocks. This unlocks parallel Phase 4-7 work.
4. **Then proceed with the foundation plan as written** — Phases 1-2 are buildable today and the plan is excellent. Phases 3-9 each need a follow-up plan at the same fidelity, which the spec already commits to ("This spec produces an implementation plan via the writing-plans skill").

The architecture is sound; the spec just needs ~2 days of concretization work before five engineers could fan out on it without colliding.
