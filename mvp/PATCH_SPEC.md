# server.ts Patch Spec

Every patch C3 applies to the official Telegram plugin's `server.ts` is listed
here by its stable id. This doc is the source of truth for *what the patch
should do*. `patch_server.py` is the source of truth for *how it's currently
applied*.

If a patch's anchor breaks because upstream was refactored, you regenerate the
anchor/replacement from this spec — the purpose, final behavior, and detection
rules shouldn't change even when the surrounding code does.

## The repair flow

When the broker reports `PATCH BROKEN — <id>`:

1. Open this file at the matching section.
2. Read **Purpose** and **Final behavior** to understand what the patch must
   achieve in the new file.
3. Apply **Detection** to the current `server.ts`. Three outcomes:
   - **Already present upstream** — the feature exists natively. Bump the
     patch's `marker` in `patch_server.py` so it skips cleanly. Done.
   - **Present but with a different shape** — upstream added something that
     covers the same concept but with different field names or structure.
     Update consumers (broker/stub/reply tool) to match upstream's shape, then
     delete the patch from `PATCHES`.
   - **Still absent** — derive a new `anchor`/`replacement` pair in
     `patch_server.py` that achieves **Final behavior**. Use **Region** to
     find roughly where in `server.ts` to inject. Pick the most stable
     nearby text as the anchor — prefer field names and strings over
     whitespace-sensitive structural text.
4. Restart the broker; patches re-apply on startup.

A pristine copy of `server.ts` is saved as `server.ts.c3-backup` next to the
patched file the first time a patch runs — that's the upstream baseline you
can diff against.

## Invariants that hold across all patches

- Patches are idempotent. Each has a `marker` string; if present in the source,
  the patch is skipped silently.
- Patches never delete upstream behavior — they only add fields/args or
  short-circuit code paths we've explicitly decided we don't want
  (see P3).
- Extra MCP `meta` keys are passthrough — Claude Code renders unknown meta
  keys as `<channel ... key="value">` attributes, no coordinated schema
  change needed.
- **Minimize upstream footprint.** The point of a patch is the behavior it
  produces, not the shape of the diff. When rewriting, aim for the smallest
  anchor/replacement pair that achieves the stated final behavior. Useful
  tricks: pack conditional fields into a single spread on one line; emit
  `undefined` for "field not applicable" and let `JSON.stringify` drop it
  (MCP notifications go through JSON.stringify); drop JSON-schema `description`
  text when the concept is already documented in tool instructions.

---

## P1_inbound_meta_thread_id

**Purpose** — Forum-topic routing in C3 keys on `(chat_id, message_thread_id)`.
The upstream plugin sends chat_id in inbound meta but not thread_id, so C3
can't tell which topic a message came from.

**Final behavior** — When a user sends a message inside a forum topic, the
inbound `notifications/claude/channel` notification's `meta` object includes
`message_thread_id` as a string. DMs and non-topic group chats omit the field.

**Detection** — Grep the `mcp.notification({ method: 'notifications/claude/channel', ... })` call in `server.ts`.
The `meta` object literal should include a conditional spread of
`message_thread_id` sourced from `ctx.message.message_thread_id`.

**Region** — Inside the handler that emits `notifications/claude/channel` for
inbound text/voice/media messages.

---

## P2a_reply_tool_schema_thread_id

**Purpose** — The `reply` tool needs to accept a thread id so stubs can
address a specific forum topic when calling the tool.

**Final behavior** — The tool's JSON input schema declares an optional
`message_thread_id` string parameter alongside `reply_to`.

**Detection** — Grep the tool registration for `name: 'reply'`. Its input
schema `properties` should list `message_thread_id`.

**Region** — Tool registration block where `reply` is defined via
`server.tool(...)` or an equivalent MCP tool descriptor.

---

## P2b_reply_tool_body_thread_id

**Purpose** — Extract `message_thread_id` from incoming tool args so the
sendMessage/sendPhoto/sendDocument calls below can use it.

**Final behavior** — Inside the `reply` tool handler, a local
`message_thread_id` is assigned from `args.message_thread_id`, coerced to
Number (or undefined).

**Detection** — Inside the `reply` handler, `const message_thread_id = ...`
is defined before the send calls.

**Region** — `reply` tool handler body, near the line that reads `reply_to`.

---

## P2c_sendMessage_thread_id

**Purpose** — Text chunks sent out must include `message_thread_id` or
Telegram posts them to the group root instead of the requested topic.

**Final behavior** — Every `bot.api.sendMessage` call inside the `reply`
handler's chunk loop includes `message_thread_id` when it's defined, alongside
existing `reply_parameters` and `parse_mode` options.

**Detection** — In the chunk loop, the options object passed to
`sendMessage` spreads a conditional `message_thread_id`.

**Region** — `reply` tool handler, loop that iterates over chunked text.

---

## P2d_sendFile_thread_id

**Purpose** — File sends (photo / document / voice reply attachments)
must also route to the correct topic.

**Final behavior** — The options object for `sendPhoto` / `sendDocument` etc.
includes `message_thread_id` when defined. Existing `reply_parameters` is
preserved.

**Detection** — File-send call sites in the `reply` handler have an options
object that merges `message_thread_id` with the pre-existing options.

**Region** — `reply` tool handler, right before the `bot.api.sendPhoto` /
`sendDocument` calls.

---

## P3_disable_orphan_watchdog

**Purpose** — The upstream plugin runs a `setInterval` that checks whether its
parent process is still alive and self-terminates if it looks orphaned. Under
the C3 broker, the heuristic (ppid change or pipe state flip) fires
spuriously and kills bun while the broker is still happily driving it.

**Final behavior** — The watchdog body returns immediately on every tick,
effectively disabled. The marker comment `C3_NO_ORPHAN_WATCHDOG` must be
present in the file so idempotency works.

**Detection** — The `setInterval(() => { ...orphan check... })` block either
contains the `C3_NO_ORPHAN_WATCHDOG` comment (patched) or runs the full
orphan-check logic (not patched).

**Region** — Top-level setup code in `server.ts`, typically near process
lifecycle / signal handling.

**Notes** — If upstream ever removes or rewrites the orphan watchdog, delete
this patch entirely rather than adapt it. The broker already handles its own
shutdown via SIGTERM; we don't need bun to second-guess.

---

## P4_inbound_reply_to_message_meta

**Purpose** — When a Telegram user quote-replies to an earlier message, the
bot sees it as just the new text with no signal that it was a reply. We want
Claude to know *which* message was being replied to, because that's the whole
point of quote-replies.

**Final behavior** — When `ctx.message.reply_to_message` is present, the
inbound `notifications/claude/channel` meta object includes:

- `reply_to_message_id` — string, the replied-to message's id.
- `reply_to_user` — string, the replied-to message's author (username
  preferred, id as fallback). Omitted if no `from` field.
- `reply_to_text` — string, the text or caption of the replied-to message.
  Omitted if the replied-to message has neither (e.g. pure-media with no
  caption).

When `reply_to_message` is absent, none of these fields appear — existing
meta shape is preserved byte-for-byte.

Claude Code renders these as attributes on the `<channel>` tag, so Claude
reading an inbound message can immediately see `reply_to_message_id="123"`
`reply_to_text="..."` and respond with context.

**Detection** — Grep the `mcp.notification({ method: 'notifications/claude/channel', ... })`
call. The `meta` object should contain a conditional spread referencing
`ctx.message.reply_to_message` with at least `reply_to_message_id` inside.

**Region** — Same `meta` object as P1 — the handler that emits
`notifications/claude/channel` for inbound messages.

**Notes** — We deliberately don't surface `external_reply` (cross-chat
replies) or `quote` (partial-text quotes) yet; add follow-up patches if those
start mattering. The `reply_to_text` payload is untruncated on purpose — full
fidelity matters more than context size, and Telegram caps single messages at
4096 chars anyway.

---

## P5_voice_handler_thread_id

**Purpose** — Upstream 0.0.6 refactored the voice handler into a thin
"forward voice as an attachment" path and removed the Python STT
shell-out entirely. C3 still wants every inbound voice to be transcribed
server-side so (a) the user sees the transcript echoed back in Telegram
and (b) Claude receives real text instead of `(voice message)` plus a
file_id it has to transcribe itself. This patch reintroduces the
shell-out. While we're at it, we include `message_thread_id` in the argv
so `stt-handler.py`'s own `sendMessage` chunks stay in the right forum
topic — the original reason for P5's existence.

**Final behavior** — The `bot.on('message:voice', ...)` handler:

1. Spawns `python3 ~/.claude/channels/telegram/stt-handler.py` with argv
   `[bot_token, chat_id, msg_id, file_id, message_thread_id_or_empty]`.
2. If the process exits 0 and stdout is non-empty, treats stdout as the
   transcript and uses it as the inbound message text.
3. Otherwise falls back to `ctx.message.caption ?? '(voice message)'`
   (the pristine upstream behavior).

Either way, `handleInbound` is called once with a `kind: 'voice'`
attachment meta — the MCP client still receives the voice file_id so it
can download the audio if the transcript is ambiguous.

**Detection** — Grep the `message:voice` handler body. A patched file
contains the marker `/* C3_STT_HANDLER */` and a `Bun.spawnSync` call
whose first arg is `'python3'` and whose second arg points at
`stt-handler.py`. An unpatched file has neither.

**Region** — `bot.on('message:voice', async ctx => { ... })` body,
between `const voice = ctx.message.voice` and the `handleInbound(...)`
call.

**Notes** — Paired with `mvp/stt/stt-handler.py`'s argv contract
(`<bot_token> <chat_id> <reply_msg_id> <file_id> [<message_thread_id>]`)
and the `c3-stt` symlink the broker installs at
`~/.claude/channels/telegram/stt-handler.py`. If upstream ever reintroduces
a structurally different voice hook (e.g. JSON on stdin, or a plugin-level
extension point), delete this patch and adapt `stt-handler.py` to match.
