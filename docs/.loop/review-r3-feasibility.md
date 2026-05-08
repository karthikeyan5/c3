# Review R3 — Implementation Feasibility (independent fresh-context)

**Spec:** `docs/specs/2026-05-08-c3-rearch-design.md` (v5, 1109 lines)
**Reviewer scope:** Go interfaces, library readiness, load-bearing detail, build/dist, edge cases.

## Verdict

**Ready to lock**, with two non-blocking tighten-ups noted. A fresh Go developer can build from this spec without further architectural revision; the open ends that remain are in implementation choices the plan phase will close, not in architectural shape.

## Library readiness — verified live

- **gotgbot/v2** — current tag `v2.0.0-rc.34` (Feb 2026). All required surface present: `CreateForumTopic`, `EditForumTopic`, `SetMessageReaction`, `SendChatAction(opts.MessageThreadId)`, `GetUpdates`/`GetUpdatesWithContext`. Spec already calls out the rc-pin requirement (§6 "track the rc series"). Acceptable.
- **go-sdk** v1.6.0 (Apr 2026) — confirmed on `pkg.go.dev/.../mcp`: `ServerSession` exposes only `NotifyProgress`, `Log`, `Ping`, `CreateMessage*`, `Elicit`, `ListRoots`, `Close`, `Wait`. **No public `Notify(method, params)`**. Spec §4.4.4 already anticipates this and prescribes manual JSON-RPC framing with a shared writer mutex around `os.Stdout`. The mitigation matches what's actually needed.

## Lock-blocking items

**Zero.** Every load-bearing path has a concrete plan:

- Manual MCP framing (§4.4.4) — writer-mutex pattern is sound; SDK confirmed not to expose alternative.
- Per-route serial executor (§4.2.0) — concrete goroutine model with typed job queue, idle-timeout exit, `ConnID` generation guard for late results.
- Codex app-server lifecycle (§4.4.5) — port-fallback with `flock`'d signature file, `${UID}` substitution, app-server-side MCP config injection (load-bearing), 15s readiness wait. All five launcher steps spelled out.
- STT distribution (§6.2) — `//go:embed handler.py` + sha256-on-extract + user-override sentinel. Solves "`go install` doesn't ship `.py`" cleanly.
- Build/dist (§5.1, §11.B/C) — `go install ./cmd/...` from cloned plugin source, `$GOBIN` on `$PATH`, `/c3-build` slash command and `c3-broker install-codex-shim` for Codex. Go ≥1.22 documented.
- IPC types (§4.4.1) — concrete Go structs with JSON tags, op-code dispatch, `RouteKey` value-typed and comparable, `*int64` topic-id semantics fixed end-to-end. The `nil`-vs-`&1` distinction is explicit at every layer.

## Tighten-up items (should resolve, don't block)

1. **gotgbot pin pre-stable.** `v2.0.0-rc.34` has no semver-stable guarantee. Plan should pick one rc and pin in `go.mod` with `// pin: do not bump without re-running forum/reaction smoke test`. Spec already mandates this; plan must enforce.

2. **Manual framer + go-sdk's writer.** §4.4.4 says "the adapter installs a custom writer wrapper around `os.Stdout`" that both SDK and framer share. This requires either (a) the SDK accepting an injected `io.Writer` for its transport, or (b) wrapping `os.Stdout` *before* the SDK starts. Confirm via SDK source which works — the spec's `PIPE_BUF=4096` atomicity argument is correct for short frames but the mutex is the real safety net, so the wrapper must be in the SDK's write path. One-day spike; not a redesign.

3. **`thread/list` + `thread/loaded/list` Codex API surface.** §4.4.5 step 4 assumes specific Codex app-server JSON-RPC methods (`thread/loaded/list`, `thread/list?cwd=…&useStateDbOnly`, `thread/resume{excludeTurns:true}`, `thread/turn/start`). The Python POC validated these exist; spec should cite the exact Codex commit/version they're confirmed against so a Go re-implementer can verify. Cite the POC file path that exercises these calls.

4. **`createForumTopic` rate cap.** §6 says "spec assumes 10/min as the safe rate" but Telegram's actual cap is observed-only. Add an explicit per-channel `create_topic_min_interval_ms` knob (default 6000) so an operator can throttle without code change. Currently the code path will discover the limit by 429-ing.

5. **Stale `mappings.json.bak`.** §4.3 keeps one generation. If a user runs `c3-broker validate` and it passes, but on next start parsing fails (rare schema-drift), the `.bak` is whatever was on disk before the last *successful* write — possibly very old. Acceptable for v1, but note it in INSTALL.md. No spec change needed.

6. **`ConnID` not in `Stub` JSON contract.** §4.5.1 introduces `ConnID uint64` on `Stub` for late-result fencing, but the IPC schemas in §4.4.1 don't show it on `HelloAckMsg` / `ToolResultMsg`. Plan should plumb it through (broker assigns on `hello`, echoed in every `tool_result.id` parsed prefix) — minor implementation detail, but mentioning here so the planner doesn't miss it.

## Edge cases caught (all already addressed)

- `flock` race on `c3-broker.pid` (§4.2.2) — pid-liveness check + unlink-stale, retry-once. Correct.
- Concurrent `codex` invocations from same user (§4.3 `codex.app_server_meta_path`) — `flock` on signature file. Correct.
- Two `*int64` map-key collision — solved by `RouteKey` normalization. Correct.
- Late `EditMessage` after re-claim — solved by per-route serial executor + `ConnID`. Correct.
- Cooldown-fallback after broker restart — first-after-restart wins, documented. Acceptable.

## Recommendation

**Proceed to build.** No spike required. The two real implementation unknowns (manual framer's interaction with the SDK writer; Codex app-server method versioning) are both 1-day tighten-ups for the plan phase, not architectural risks. The spec is detailed enough that a fresh Go developer can produce `cmd/c3-broker/main.go` and `cmd/c3-claude-adapter/main.go` from §4 alone, and the Codex bridge from §4.4.5 alone. Implementation phase order in §12 is sane: phases 1-7 unblock daily use; 8 is the Codex bridge (highest novelty/risk); 9-10 are packaging.

Word count ~660.
