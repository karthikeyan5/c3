---
description: Rebuild C3 binaries from source via `go install ./cmd/...`.
---

!cd "${CLAUDE_PLUGIN_ROOT}/../.." && go install ./cmd/...
!command -v c3-broker >/dev/null && c3-broker --help 2>&1 | head -1

If the build succeeded, tell the user: "binaries installed. Run `/c3:restart-broker` to load the new broker, then restart this Claude Code session to load the new adapter binary."

If `go install` failed, surface the error verbatim and suggest checking Go version (`go version` ≥ 1.22) and that the plugin source dir contains a `go.mod`.
