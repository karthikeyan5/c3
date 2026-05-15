---
description: Tell the running C3 broker to re-read ~/.config/c3/mappings.json. Non-disruptive — no process restart, no adapter recycle.
---

!pkill -HUP -x c3-broker && echo "SIGHUP sent — broker will re-read mappings.json" || echo "no running broker to reload"

Display the output. The broker re-reads `~/.config/c3/mappings.json`, swaps the in-memory pointer, and logs the new counts to `~/.local/state/c3/broker.log`. New topics, mappings, and plugin config become visible immediately. Live route claims are preserved.

**What requires a real restart instead:**
- Telegram bot token change (channel was initialized at broker start; reload doesn't re-init the channel)
- Telegram group additions where you need the channel to start polling new groups
- Broker binary update after `/c3:build` (the running process is the old code)

For binary updates after `/c3:build`: quit Claude Code (`Ctrl-D` or `/exit`), then relaunch with `claude --resume`. The new adapter spawn auto-spawns a broker with the new binary. This avoids the `/c3:restart-broker`-from-inside-CC failure mode where killing the broker also recycles the MCP server (Claude Code closes the adapter's stdin).
