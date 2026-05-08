# C3 Re-Architecture — Feasibility Review (R2, fresh context)

Independent assessment of `/home/karthi/arogara/c3/docs/specs/2026-05-08-c3-rearch-design.md` plus companion docs `PLUGINS.md`, `CHANNELS.md`, `ADAPTERS.md`, foundation plan `2026-05-08-c3-v3-foundation.md`. No other reviews consulted.

## Verdict

**Yes, with these clarifications.** A competent Go developer can build this from the spec, but a handful of concrete gaps will cost a few days of guess-and-check unless tightened. The architecture is buildable; the spec just leans on prose where it should pin a Go signature, version, or framing rule.

## Underspecified interfaces

- **`Channel` interface drift between docs.** Spec §4.1 defines `Channel` with `SendReply→(*ReplyResult,error)` and no `React`. `CHANNELS.md` defines a different `Channel` with `EditMessage→(*EditResult,error)` and adds `React`, `DownloadAttachment`. Pick one and replicate it; an implementer literally cannot write `internal/channel/channel.go` from these two together.
- **`plugin.Host` is a value vs pointer mismatch.** `PLUGINS.md` and §8 example show `Register(host *plugin.Host) error`, but §4.5.1 declares `Host` as an `interface`. Concrete decision missing. Affects every `internal/plugin/builtins/*` package.
- **`channel.Host` is referenced but never defined.** `CHANNELS.md` uses `host *Host` in `Start(ctx, host *Host)` but no struct/interface signature for the channel-side host appears anywhere. Methods named (`host.Inbound`, `host.Config`, `host.RegisterTool`, `host.Logf`, `host.Done()`) need an actual Go declaration.
- **`Tool` and `ToolRegistry` are partial.** §4.5.1 has `Tool{Handler func(ctx, args) (any, error)}` but the broker-side dispatcher (how `tool_call` op routes to a plugin-registered Tool vs a channel-registered tool vs an adapter-local tool) is not pinned. Who owns the registry in memory?
- **`MockHost`, `MockServer`** referenced as testing primitives in PLUGINS.md and ADAPTERS.md without signatures or contract. Implementers will invent divergent shapes.
- **`Inbound.Raw map[string]any`** appears in `CHANNELS.md` but not in spec §4.1 — two `Inbound` structs in flight.
- **Proposal recursion** (`Proposal.Alternative *Proposal`) — depth limit, expected leaf actions, and how the adapter renders nested proposals are unspecified.

## Library readiness

- **gotgbot/v2** — current release is `v2.0.0-rc.34` (Feb 2026); **no stable v2.0.0 tag exists**. All needed Bot API surface is present: `CreateForumTopic`, `EditForumTopic`, `SendChatAction` with `MessageThreadId` opts, `GetUpdatesOpts` with `AllowedUpdates`, `Message.MessageThreadId`. Spec §6 already calls out "pin to a specific rc". Workable, but document the rc-pin policy and mark it as a known risk in `INSTALL.md`.
- **`modelcontextprotocol/go-sdk` v1.6.0** — `&mcp.StdioTransport{}` and `mcp.AddTool` are present. **Confirmed gap:** `ServerSession` exposes only `NotifyProgress`, `Log`, `ResourceUpdated` — there is no public `Notify(method, params)` for arbitrary JSON-RPC notifications. Spec §4.4.4 already anticipates this and prescribes manual JSON-RPC framing through a shared `os.Stdout` mutex. The plan is sound, but the spec must mandate that **the SDK be initialized with a wrapped writer the adapter can co-use** — otherwise the SDK still owns raw `os.Stdout` and the manual frame races. That hookup is not shown.
- **WebSocket client for Codex bridge** — spec mentions "gorilla/websocket or stdlib equivalent" parenthetically. No stdlib WS client exists; pick `nhooyr.io/websocket` or `gorilla/websocket` explicitly and pin in §6 / `go.mod`.

## Missing implementation detail

- **`notifications/claude/channel` payload schema is not in the spec.** Adapter has to emit it with the right `params` shape (meta attributes for `chat_id`, `message_id`, `message_thread_id`, `user`, `reply_to_*`, attachment paths). The Python MVP knows this; the Go rewrite does not have it written down. Mandatory.
- **STT plugin Python subprocess contract** — `core/plugins/stt` shells to `stt-handler.py` / `stt/transcribe.py` (two names appear). Argv, stdin/stdout protocol, exit codes, where the Python lives in the installed binary tree, and how `go install` carries the `.py` file (it doesn't — `go install` only ships binaries) are all unspecified. This is a real distribution problem.
- **`flock` portability** — Linux-only via `unix.Flock`. macOS works similarly via `golang.org/x/sys/unix`. Windows is silent. State the supported OS matrix.
- **Atomic mappings.json rewrite** — temp-file-then-rename is named, but `fsync` of the dir, mode-600 preservation, and concurrent-writer race against a hand-edit are not.
- **Codex app-server port-fallback metadata file race** — `/tmp/c3-codex-app-server.json` is read/written by every `codex` invocation. No locking mentioned. Two simultaneous launches in different cwds will both read "stale", both fall forward, both pick port 8767, both lose.
- **Debounce buffer message-id concatenation** — "concatenated, with the latest `message_id` as canonical" — but the inbound payload carries one `message_id`; adapters need either an array or a documented join character. Spec says one canonical, code must agree.
- **Plugin priority tie-break** — when two plugins share `priority: 100`, order is undefined. Pick something (registration order, name lex).
- **`MockHost` test-helper API** — referenced as load-bearing for plugin TDD; signature absent.

## Distribution gotchas

- **`go install ./cmd/...` requires `$GOBIN` on `$PATH`.** Spec §11.C says "assuming the user's PATH includes `$GOBIN`". `INSTALL.md` must explicitly check and fail loudly with a fixup message when it isn't — both `/c3-build` and `c3-broker install-codex-shim` should refuse to proceed without it.
- **Go version pin** — spec says "Go ≥1.22". Confirmed in foundation plan. Make `go.mod`'s `go 1.22` line authoritative; CI and `/c3-build` should reject older toolchains with a clear message.
- **No dependency pinning policy.** With gotgbot on rc and go-sdk on a fast-moving v1, `go.sum` is the only protection. Adopt `GOFLAGS=-mod=readonly` for build steps; document `go mod tidy` cadence.
- **STT Python files are not Go binaries.** `go install ./cmd/...` will not place `stt-handler.py` anywhere. Need either a `make install-stt` step that copies the .py into `~/.local/share/c3/stt/`, or embed via `//go:embed` and write-on-first-run. This is currently invisible in the spec.
- **`~/.local/bin/codex` symlink in `install-codex-shim`** — must verify `~/.local/bin` is on `$PATH` ahead of any `nvm` shim path. The docs note long-running shells hash `codex` to NVM bin dirs (good catch) but don't cover fish/zsh `command -v` cache rehash semantics post-install.
- **Plugin marketplace install of an uncompiled binary** — spec's "build from source on first install" is novel for Claude Code marketplace; `/c3-build` running `go install` from inside the plugin source dir assumes the user has Go *and* the plugin dir is writable. Document failure modes.

## Edge cases that need handling

- **Broker spawn race vs flock** — adapter's exponential-backoff connect retry can race with the *losing* spawned broker's silent exit. Spec covers the broker side but doesn't say the adapter must accept `ECONNREFUSED` for the full 10s window even after spawn returned.
- **Stub crash mid-`tool_call`** — broker has dispatched a tool call to the channel; channel returns; broker writes `tool_result` to a closed adapter socket. The §4.4.1 writer mutex protects framing but not orphaned results. Either drop quietly or queue for the next reconnect — pick.
- **Reconnect-once-with-reclaim** (`ADAPTERS.md`) — what if reclaim fails because another stub took the route during the disconnect? Adapter behavior unspecified.
- **`message_reaction` is in `allowed_updates` but no inbound surface** — spec §10 says "plumbed but no tool yet", but the channel still needs to *not* crash on the unknown update type. Make the no-op explicit.
- **NVM symlink iteration** — `~/.nvm/versions/node/*/bin/` may contain dozens of versions; install must be idempotent across re-runs and handle the case where one bin dir is read-only (corporate-managed Node).
- **Topic id `*int64` ergonomics** — `TopicID: &(int64(1))` is fiddly; provide a helper `pInt64(v) *int64` in `internal/ipc` or a typed `TopicID` enum-ish wrapper. Otherwise every call site re-invents it.
- **`sendChatAction` validation side effect** — §6 acknowledges typing indicator fires in the topic during `attach --topic=N` validation. Fine, but if the topic is *closed* (Telegram allows closed forum topics), the bot returns 400; map that error to the user as "topic is closed" rather than the raw API string.
- **Mode-600 mappings.json on multi-user macOS** — fine. On a system where `~/.config` is on a network share with no POSIX perms (NFSv3, SMB), 600 is silently lost; a startup check should warn.
- **Concurrent `attach(create=true)` from two cwds with the same basename** — both stubs propose `widget-foo`, both confirm, broker creates twice. The "stateless proposal" choice (§4.5.1) makes this race possible. Need a per-name in-flight lock at the channel layer.

## Recommendation

**Spike key uncertainties first, then proceed to build.** Concretely, two short spikes before Phase 3 of the foundation plan:

1. **MCP custom-notification spike** — wire go-sdk v1.6.0 stdio server alongside a shared-mutex manual JSON-RPC framer; prove `notifications/claude/channel` actually round-trips into Claude Code's `<channel>` rendering. If it does, lock the writer-injection pattern into `internal/adapter/broker/` and don't revisit. If it doesn't, the whole Claude adapter design needs a Plan B.
2. **Codex app-server `--remote` MCP spike** — confirm that injecting `mcp_servers.c3_codex.*` via repeated `-c` flags into the app-server invocation actually causes our adapter to spawn with the right env. The Python POC proved this is doable; the Go path needs its own one-day proof before §4.4.5's contract gets coded into a launcher.

Beyond those, the rest of the spec is buildable as written once the interface drift between spec and `CHANNELS.md`/`PLUGINS.md` is reconciled (one focused editing pass) and the STT-Python distribution gap is resolved (one design decision: embed-and-extract or `make install-stt`). No structural blockers.

Sources:
- [github.com/PaulSonOfLars/gotgbot/v2 docs](https://pkg.go.dev/github.com/PaulSonOfLars/gotgbot/v2)
- [github.com/modelcontextprotocol/go-sdk/mcp docs](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
