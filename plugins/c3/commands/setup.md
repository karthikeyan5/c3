---
description: Interactively configure C3 — bot token, DM chat id, group + chat id.
---

Run `c3-broker setup` and let the user type their answers at the terminal. The subcommand prompts for bot token (validated via Telegram getMe), DM chat id, default group name + group chat id, and writes `~/.config/c3/mappings.json` (mode 0600).

!c3-broker setup

After it completes, if it succeeded, tell the user: "setup complete. Run `/c3:restart-broker` and then `/c3:attach` in any project directory."
