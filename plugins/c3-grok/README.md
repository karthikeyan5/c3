# C3 plugin for Grok Build

## One-shot host setup

```bash
cd /path/to/c3 && go install ./cmd/c3-grok-adapter ./cmd/c3-broker
c3-broker install-grok
grok plugin install /path/to/c3/plugins/c3-grok --trust
```

`install-grok` enables **leader mode** and pins `mcp_servers.c3` → `c3-grok-adapter`.

## Day-to-day

```bash
grok --leader          # or plain `grok` if use_leader = true
# first time / new project:
#   attach name=<topic>
# resume same session:
#   silent auto-attach to your last topic (if you attached before)
```

### Lifecycle (what “just works” means)

| You | C3 |
|-----|-----|
| Open Grok (leader on) | Adapter registers session id; resumes last topic if known |
| Attach once | Claim + session attachment recorded |
| Quit TUI | Adapter may outlive TUI under leader; claim released on **adapter/leader exit** |
| Resume session | Auto-attach + optional backlog notice |
| Rebuild adapter binary | **Reload MCP** (`/mcps`) or restart leader — TUI-only restart keeps old MCP PID |

## How live inject works

See `docs/GROK-INJECT.md`. Inbound: leader ACP `session/prompt`. Outbound: MCP tools (`reply`, …).
