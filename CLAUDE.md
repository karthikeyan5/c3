# CLAUDE.md — C3 (Claude Code Claw)

## Session Start (do this every time)

1. Read `README.md` — what C3 is and the architecture.
2. Read `RESUME.md` — where we left off and what's in progress.
3. Read `ROADMAP.md` — the canonical prioritized roadmap (what's next; nothing-lost).
4. Read `TODO.md` — the detailed working checklist.
5. Read `DECISIONS.md` — all decisions made so far.

## The Short Version

C3 is a Telegram multiplexer for multiple Claude Code CLI instances. One daemon, many MCP stubs, topic-based routing. Written in Go. See README.md for the full architecture.

Discuss before building anything non-trivial. The maintainer makes final calls.

## Launch command (for the maintainer to type)

For the Telegram channel to actually surface inbound messages in this CLI,
Claude Code must be started with the development-channels flag:

```
claude --dangerously-load-development-channels plugin:c3@c3
```

(or the same with `--resume` / `--resume <id>` appended)

A plain `claude` will leave the c3 channel notifications enabled at the
broker but **silently dropped by Claude Code** — broker log shows
`delivered`, but no `<channel>` block appears in the conversation. Both
gates must be passed: `~/.claude/settings.json`'s `channelsEnabled` +
`allowedChannelPlugins` (the user-side allowlist) AND the dev-channels
flag (additional gate for local-directory marketplace plugins like c3
that haven't shipped through Anthropic's marketplace yet).

Do NOT alias this — the maintainer prefers the full command in shell history.
