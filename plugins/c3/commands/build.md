---
description: Rebuild C3 binaries from source via `go install ./cmd/...`.
---

!cd "${CLAUDE_PLUGIN_ROOT}/../.." && go install ./cmd/...
!command -v c3-broker >/dev/null && c3-broker --help 2>&1 | head -1

If the build succeeded, tell the user: "binaries installed. To load the new code, quit Claude Code (Ctrl-D or `/exit`) and relaunch with `claude --resume` — the next adapter spawn auto-spawns a fresh broker. Don't try to bounce the broker from inside Claude Code; it kills the MCP server. `/c3:reload-config` is for mappings.json edits only; it won't reload binaries."

Also remind the user (once): "voice-note transcription needs its Python deps in a dedicated venv — if you haven't already, run `bash plugins/c3/stt/setup-venv.sh` (installs `sarvamai`; C3 auto-detects `~/.config/c3/stt-venv`). Long (>30s) notes fail without it."

If `go install` failed, surface the error verbatim and suggest checking Go version (`go version` ≥ 1.22) and that the plugin source dir contains a `go.mod`.
