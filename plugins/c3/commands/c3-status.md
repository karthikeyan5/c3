---
description: Print C3 status — socket reachable, broker pid, mappings.json validity, channel reachable, claimed topics, plugin states.
---

You are running C3's status check. This is what the user just typed `/c3-status` to do.

Steps to perform:

1. **Run `c3-broker status`** in the user's shell:
   ```bash
   c3-broker status
   ```
   This is read-only. It prints:
   - Whether the broker daemon is alive (pid file + flock check)
   - Socket path + permissions
   - `~/.config/c3/mappings.json` parse + validate result
   - Each registered channel's connectivity (e.g. `telegram: @YourBot`)
   - Currently-claimed routes with holder pid + cwd
   - Plugin enabled-states

2. **Surface the output** verbatim to the user. If `c3-broker` isn't found, tell them to run `/c3-build`.

3. **Common follow-ups** based on what status reports:
   - "broker not running" → next CLI session will spawn it; or run `c3-broker` from a shell.
   - "mappings.json: parse error" → tell the user to fix the JSON or restore from `~/.config/c3/mappings.json.bak`.
   - "channel telegram: 401" → token is invalid; re-run `/c3-setup`.
   - "claim held by pid <n>" → if they want to take over: `c3-broker release <cwd>`.

Don't go beyond reporting status and pointing at fix commands. The user drives the actual fix.
