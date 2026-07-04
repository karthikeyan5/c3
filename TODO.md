# TODO — C3 v1 release

The v1 finish line. The multiplexer, topic routing, durable queue, and
cross-CLI core already ship — this is what's left before the public push.
Future/unbuilt work lives in [`ROADMAP.md`](ROADMAP.md); shipped history is
in git.

## Docs restructure (this pass)

- [x] Add `junk/` to `.gitignore`; archive done-and-dusted docs (RESUME, build-log, GCP brief, plans/research/specs/superpowers)
- [x] Rewrite README front door: positioning, honest flag box, "how is this different from X?"
- [x] Dissolve RESUME.md; ROADMAP → v2-only; TODO → this checklist
- [x] ADAPTERS.md: `inbox` → `fetch_queue`; drop `architecture.md` + `internal/adapter/broker` refs; describe the real adapter reality
- [x] CHANNELS.md: real file tree + interface signatures; no `registry.go`
- [x] PLUGINS.md: real registrar (`builtinPlugins` in `cmd/c3-broker/main.go`); drop the dead `OnOutbound` hook; link the STT provider how-to
- [x] COMMANDS.md: complete the verb/tool tables; resolve the Codex rows
- [x] DEBUGGING.md: `c3-ping.md` → `ping.md`; scrub personal names
- [x] USAGE.md / INSTALL.md: genericize examples; describe the STT chain honestly
- [x] Bump `plugins/c3/.claude-plugin/plugin.json` to `1.0.0`

## Release gates

- [ ] Fresh-machine install validation (public-push blocker)
- [ ] `ask` live-verify: button tap → choice returns to Claude, in a live Telegram session
- [ ] Permission-relay live-verify: a real Claude Code permission prompt → approve/deny over Telegram
- [ ] Forked-session queue-delivery blackhole fix — the adapter must not ack-as-delivered what the host can't render
- [ ] Auto-attach-on-resume: default OFF unless its two known bugs die quickly
- [ ] Fix the 2 flaky broker tests (fixture defect, not prod)
- [ ] `install-claude-shim` existing-symlink clobber fix
- [ ] Noisy re-poll / dedup-skip fix (if still live)
- [ ] STT gemini provider: revive with a key, or redocument on the Sarvam default
- [ ] Codex policy 3-state error messaging: confirm wired
- [ ] `release <cwd>`: print the `/exit` workaround (the full IPC op is v2)
- [ ] Smoke-test visual tails (expandable show-more; inline-button callback)

## Packaging

- [ ] Prebuilt binaries (Linux + macOS, amd64/arm64) — may slip to v1.1
- [ ] GitHub-source marketplace edit — paired with prebuilt binaries

## Ship

- [ ] Final PII audit before push (standing rule)
- [ ] Ship WITH the documented `--dangerously-load-development-channels` flag
