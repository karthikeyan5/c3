---
description: Build C3 binaries from source via `go install ./cmd/...`. Run once after `/plugin install c3@c3`, and again after each `/plugin update`.
---

You are running C3's first-time build step. This is what the user just typed `/c3-build` to do.

The C3 plugin is shipped as Go source. The plugin install only cloned the source; the binaries (`c3-broker`, `c3-claude-adapter`, `c3-codex-adapter`, `migrate-legacy`) need to be compiled and installed to `$GOBIN` (or `$GOPATH/bin`).

Steps to perform:

1. **Verify Go is installed.** Run `go version`. If it errors, tell the user: "Go ≥1.22 must be installed. Install Go from https://go.dev/dl/, then re-run `/c3-build`." and stop.

2. **Locate the plugin source.** The `${CLAUDE_PLUGIN_ROOT}` variable points at `<...>/plugins/c3`. The Go source is two levels up at the marketplace root. Run:
   ```bash
   cd "${CLAUDE_PLUGIN_ROOT}/../.." && pwd
   ```
   Verify the printed path contains `go.mod`.

3. **Install the binaries.** Run:
   ```bash
   cd "${CLAUDE_PLUGIN_ROOT}/../.." && go install ./cmd/...
   ```
   This downloads dependencies (gotgbot/v2 and a few others) and produces four binaries in `$GOBIN`. First run takes 1–3 minutes on a fresh machine due to dep download; subsequent runs are fast.

4. **Verify `$GOBIN` is on `$PATH`.** Run:
   ```bash
   GOBIN=$(go env GOBIN); [ -z "$GOBIN" ] && GOBIN=$(go env GOPATH)/bin; echo "$GOBIN"
   command -v c3-claude-adapter || echo "NOT-ON-PATH:$GOBIN"
   ```
   If the second line says `NOT-ON-PATH:<dir>`, tell the user: "Add `<dir>` to your `$PATH` (typically by appending `export PATH=\"<dir>:$PATH\"` to `~/.zshrc` or `~/.bashrc`), open a new terminal, and re-run `/c3-build` to verify."

5. **Confirm to user.** If the binary is on PATH, report:
   - Each of the four binaries' versions (`<binary> --version` may not be implemented yet — fall back to "installed at $GOBIN/<name>").
   - Next step: "Run `/c3-setup` to provide your Telegram bot token, then restart this session."

Don't run anything beyond what's listed. If any step fails, surface the actual error to the user — don't silently retry or paper over.
