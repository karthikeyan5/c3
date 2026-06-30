# RESUME

A short pointer to C3's canonical state docs. Detailed status, the prioritized
roadmap, and past decisions live in the dedicated files below — start there.

- **What C3 is + architecture:** [README.md](README.md)
- **Install / first run:** [INSTALL.md](INSTALL.md)
- **Canonical roadmap (what's next):** [ROADMAP.md](ROADMAP.md)
- **Working checklist:** [TODO.md](TODO.md)
- **Decisions:** [DECISIONS.md](DECISIONS.md)
- **Specs:** [docs/specs/](docs/specs/) · **Plans:** [docs/plans/](docs/plans/)
- **User guide + authoring:** [docs/USAGE.md](docs/USAGE.md),
  [docs/PLUGINS.md](docs/PLUGINS.md), [docs/CHANNELS.md](docs/CHANNELS.md),
  [docs/ADAPTERS.md](docs/ADAPTERS.md), [docs/COMMANDS.md](docs/COMMANDS.md)

## Launch command

To receive inbound channel notifications, start Claude Code with the
development-channels flag:

```
claude --dangerously-load-development-channels plugin:c3@c3
```

A plain `claude` leaves the c3 channel notifications enabled at the broker but
silently dropped by Claude Code (broker log shows `delivered`, but no
`<channel>` block appears). See [CLAUDE.md](CLAUDE.md) for the full reason.
