---
description: Configure C3 — gather Telegram bot token, DM chat id, group chat id; validate against Telegram; write `~/.config/c3/mappings.json` (mode 0600).
---

You are running C3's interactive setup. This is what the user just typed `/c3-setup` to do.

Steps to perform:

1. **Check whether config already exists.** Run:
   ```bash
   ls -la ~/.config/c3/mappings.json 2>/dev/null
   ```
   If the file exists with non-zero size AND contains a real `bot_token` (not just the skeleton placeholder), ask the user: "Config already exists at `~/.config/c3/mappings.json`. Do you want to (a) keep it as-is, (b) overwrite, or (c) just print what's there?". Only proceed to step 2 if they pick (b).

2. **Hand off to the broker.** The broker has a `setup` subcommand that does all the validation (calls Telegram `getMe` before writing, refuses on 401/timeout). Run it interactively in the user's shell:
   ```bash
   c3-broker setup
   ```
   This prompts for:
   - Telegram bot token (via `@BotFather`)
   - DM chat id (your Telegram user id, positive integer; ask `@userinfobot` if unknown)
   - At least one group: name (e.g. `main`) + chat id (negative `-100…`)
   - Optional: `master_user_id` (defaults to DM chat id)

   On success it writes `~/.config/c3/mappings.json` mode 0600 and prints a confirmation. On token-invalid or no-network it refuses to write.

3. **Verify the broker can connect.** After successful setup:
   ```bash
   c3-broker status
   ```
   Should show socket reachable, channel "telegram" connected as `@<your-bot>`. If status shows broker not running, the next CLI session will spawn it; tell the user to restart their Claude Code session.

4. **Confirm to user.** Tell them:
   - Setup is complete.
   - Restart their Claude Code session — current sessions are still bound to the old (or no) config.
   - In the new session, the adapter will auto-attach if the cwd has a saved mapping, or surface a `attach` proposal if not.

If the user already had a partially-populated config (skeleton from broker first-run), `c3-broker setup` overwrites it cleanly.
