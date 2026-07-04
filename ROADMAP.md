# C3 — Roadmap

What's next for C3 after v1. Everything here is future or unbuilt; shipped work is in git history. One line per item — details land in the design when each is built.

## Interactive & trust

- Interactive Q&A: free-text / "Other" / comment answers (single/multi-select + Skip already ship).
- Codex parity for tap-to-approve, `ask`, and `detach`.
- Per-user access control — who is allowed to drive which CLI.
- Trusted-operator authorization for actions the CLI would otherwise hard-deny.
- Permission-relay niceties: "see more" expansion, a text `y/n` fallback.

## Reach

- Remote spawn + control of Claude Code / Codex sessions from chat.
- Inter-agent messaging — one agent can message another's channel (rate-limited).
- Stream the agent's reasoning to the channel.
- Be the phone surface for session managers (Claude Squad, CCManager, Conductor, …).

## More channels

- Web-chat and voice-mode channels via the `Channel` interface.
- Other transports the interface already admits (Slack, Matrix, …).

## Telegram completeness

- Media-group / album assembly, media echo by `file_id`, message forwarding.
- Richer formatting (underline, inline mentions), location sends, more poll options.

## Packaging & platform

- Auto-update from GitHub release tags.
- Drop the development-channels flag once C3 ships through a trusted plugin store.
- External (non-Go) loadable plugins.
- Broker-side `/list` and `/route` commands.
- Auto-attach on resume enabled by default (after live verification).
- Monitoring dashboard, persistent message history, STT latency instrumentation.

## Open design questions

- Whether a typed free-text answer is also queued as a normal message, or consumed only as the answer.
- Grant UX for operator authorization (per-action prompt vs standing grant).
