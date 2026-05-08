# C3 Codex Bridge Spec

This document describes the Codex side of C3. The Claude Code integration is
separate and should not be changed to support Codex.

## Goal

Running `codex` should be enough to work from either the terminal or the C3
Telegram topic:

- outbound replies use the existing C3 broker and Telegram plugin
- inbound Telegram text and voice transcripts appear as normal Codex user turns
- no separate app-server terminal is required
- Claude Code's `stub.py`, `c3-attach`, and channel notification path remain
  untouched

## Components

```
Telegram
  -> official Telegram plugin
  -> broker.py over /tmp/c3.sock
  -> codex_stub.py over MCP stdio
  -> Codex app-server WebSocket
  -> visible Codex TUI launched with --remote
```

Codex-specific files:

- `codex`: executable shim.
- `codex_supervisor.py`: launcher and app-server supervisor.
- `codex_stub.py`: Codex MCP server connected to the C3 broker.
- `tests/test_codex_launcher.py`: launcher and stale-app-server regression tests.
- `tests/test_codex_stub.py`: MCP tool, forwarding, and recovery tests.

## Launch Flow

The installed command path should point at `c3/mvp/codex`. On this machine both
of these are symlinks to the shim:

```
~/.local/bin/codex
~/.nvm/versions/node/v20.19.0/bin/codex
```

The NVM symlink is intentional. Long-running shells can hash `codex` to the NVM
path, so only installing `~/.local/bin/codex` is not sufficient.

The wrapper:

1. Finds the real Codex executable, skipping itself. If the normal `PATH` scan
   cannot find it, it falls back to
   `~/.nvm/versions/node/*/lib/node_modules/@openai/codex/bin/codex.js`.
2. Bypasses non-interactive/admin commands such as `codex exec`, `codex mcp`,
   `codex app-server`, `codex --help`, and `codex --version`.
3. Infers a topic name from the nearest `CLAUDE.md`; from `~/arogara` this is
   `arogara`.
4. Starts or reuses a Codex app-server.
5. Launches the visible TUI with `--remote <app-server-ws>`.

`codex resume ...` and `codex fork ...` must not bypass the wrapper. They are
interactive TUI commands and need the same bridge.

## App-Server Ownership

Codex remote mode means the app-server owns MCP server startup. Passing MCP
config only to the remote TUI is too late: the visible TUI may have the right
environment, while the app-server-owned `codex_stub.py` has no `C3_*` variables.

Therefore `codex_supervisor.py` starts the app-server itself with the C3 MCP
config:

```
mcp_servers.c3_codex.command="python3"
mcp_servers.c3_codex.args=[".../codex_stub.py"]
mcp_servers.c3_codex.env.C3_CODEX_APP_SERVER_WS="<ws-url>"
mcp_servers.c3_codex.env.C3_CODEX_CWD="<cwd>"
mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE="1"
mcp_servers.c3_codex.env.C3_ATTACH_NAME="<topic>"
mcp_servers.c3_codex.enabled=true
```

The visible TUI also receives the same config, but the app-server copy is the
critical one.

## Port Selection

The default app-server URL is:

```
ws://127.0.0.1:8766
```

Older or manually-started app-servers may already occupy that port without the
right C3 MCP environment. Reusing one creates a broken state where Codex is
remote, but inbound Telegram still cannot attach or forward correctly.

The wrapper writes metadata to:

```
/tmp/c3-codex-app-server.json
```

If `8766` is reachable but its metadata does not match the current C3 signature
`(cwd, topic, stub path)`, the wrapper selects the next free localhost port
starting at `8767`.

## Inbound Forwarding

`codex_stub.py` receives inbound Telegram messages from `broker.py` as C3
broker `inbound` events. It always buffers them for `c3_inbox`.

When `C3_CODEX_REMOTE_BRIDGE=1`, it also forwards each inbound message into the
Codex app-server:

1. WebSocket connect to `C3_CODEX_APP_SERVER_WS`.
2. Use `suppress_origin=True`; otherwise Codex app-server rejects
   `websocket-client` with HTTP 403.
3. `initialize`
4. `thread/loaded/list`, or `thread/list` filtered by `C3_CODEX_CWD` when
   multiple threads are loaded.
5. `thread/resume` with the selected thread id.
6. `turn/start` with text:

```
Telegram message from <sender> (chat=<chat_id> thread=<thread_id>)
<message text>
```

Voice messages are already transcribed by the existing C3/Telegram path; Codex
sees them as normal text content such as `[Transcribed voice]: ...`.

## Split-Brain Guard

Do not enable forwarding manually from a stock `codex resume` process.

That creates split-brain:

- Telegram turns go to the background app-server.
- The app-server may persist and even answer those turns.
- The visible non-remote TUI does not render them.

`codex_stub.py` refuses `c3_codex_forward` unless either:

- `C3_CODEX_REMOTE_BRIDGE=1`, set by the wrapper, or
- `C3_CODEX_ALLOW_MANUAL_FORWARD=1`, reserved for deliberate debugging.

## MCP Tools

The Codex MCP server exposes:

- `c3_attach(target)`: attach this session to a C3 topic.
- `c3_topics()`: list known topics and current claims.
- `c3_inbox(limit, ack)`: read buffered inbound messages as a fallback.
- `c3_reply(text, files, parse_mode)`: reply through the attached topic.
- `c3_codex_forward(app_server_ws, thread_id)`: debugging/config tool for
  forwarding; normally the wrapper configures forwarding automatically.

`c3_reply` can recover its attached topic from broker claims if the MCP process
lost local in-memory `bound` state but still owns a topic in `topics`.

## Operational Checks

A healthy Codex bridge has all of these:

```
tail /tmp/c3-codex-supervisor.log
```

Shows a wrapper launch with `--remote ws://127.0.0.1:<port>`.

```
tail /tmp/c3-codex-stub.log
```

Shows:

```
=== codex stub started ===
broker->codex op=server_info
broker->codex op=attached
auto-attached to <topic>
```

The live app-server-owned stub environment includes:

```
C3_ATTACH_NAME=<topic>
C3_CODEX_REMOTE_BRIDGE=1
C3_CODEX_CWD=<cwd>
C3_CODEX_APP_SERVER_WS=ws://127.0.0.1:<port>
```

`c3_topics()` should show the intended topic claimed by the app-server-owned
`codex_stub.py` pid.

## Known Failure Modes

`codex` starts but Telegram messages do not appear:

- Check whether the visible process is stock `codex resume ...` instead of the
  wrapper. The process should include `--remote`.
- Check whether the app-server-owned `codex_stub.py` has the `C3_*` environment.
- Check whether `arogara` or the intended project topic is claimed by the new
  stub pid.

`c3_reply` says to attach first:

- The stub may have lost local state. Current code recovers from broker claims;
  if recovery fails, `c3_topics()` should reveal whether the topic is claimed by
  this stub or another process.

WebSocket forwarding fails with 403:

- Ensure `codex_stub.py` uses `websocket.create_connection(...,
  suppress_origin=True)`.

The wrapper keeps using a stale app-server:

- Check `/tmp/c3-codex-app-server.json`.
- If the signature does not match, the wrapper should choose a fresh port.

## Verification

Run:

```
python3 -m pytest c3/mvp/tests/test_codex_launcher.py c3/mvp/tests/test_codex_stub.py -v
python3 -m py_compile c3/mvp/codex_supervisor.py c3/mvp/codex_stub.py
```

