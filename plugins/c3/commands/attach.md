---
description: Attach this session to a Telegram topic. Empty = cwd-saved silent claim. "dm" = actual DM. "<int>" = topic_id. "<name>" = topic by name. "create <name>" or "-y <name>" = create that topic immediately.
argument-hint: "[empty | dm | <topic-id> | <name> | \"create <name>\" | \"-y <name>\"]"
allowed-tools: ["mcp__plugin_c3_c3__attach", "AskUserQuestion"]
---

User typed: $ARGUMENTS

Call `mcp__plugin_c3_c3__attach` with `expr` set to the user's argument string ("$ARGUMENTS" verbatim). The broker parses it (rules in `docs/COMMANDS.md`) and either:
- Silent-claims and returns `attached to "<name>"`
- Returns a proposal (`create` / `use_existing_other_group` / `disambiguate_dm` / `force_steal`) requiring confirmation

If the response is **needs_confirmation** with proposal action:

- **`create`**: Ask the user via `AskUserQuestion` whether to create the topic. On yes → re-invoke `attach(expr="create $ARGUMENTS")` (or `attach(name=..., create=true)` if you have the name parsed). On no → tell them how to use an existing topic instead.

- **`use_existing_other_group`**: Ask the user via `AskUserQuestion` whether to claim the existing topic (in a different group) or create a new one in the default group. Re-invoke accordingly.

- **`disambiguate_dm`**: Ask the user via `AskUserQuestion` whether they meant the topic named "dm" or the actual Telegram DM. On topic → `attach(topic_id=<the topic_id from the proposal>)`. On DM → `attach(target="dm", steal=true)` (steal=true bypasses the disambiguation check).

- **`force_steal`**: Ask the user via `AskUserQuestion` — show holder cli/pid/cwd from the proposal — whether to evict and take the claim. On yes → re-invoke with `steal=true`. On no → leave it.

For successful attach: just display the broker's response. No extra commentary.

For errors that aren't proposals: display the error verbatim. The most common is "no telegram bot_token configured" which points at `/c3:setup`.
