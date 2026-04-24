#!/usr/bin/env python3
"""Idempotently patch the official Telegram plugin's server.ts for C3.

Each entry in PATCHES has:
  id           — stable short name, shown in logs and in PATCH_SPEC.md
  purpose      — one-sentence why; reproduced in error messages
  marker       — substring that, if present, means the patch is already applied
                 (skip). Must be unique enough to not false-positive.
  anchor       — literal text in the *current* server.ts we splice into
  replacement  — what `anchor` becomes after the patch

If an anchor ever stops matching (upstream refactored that region), we print a
loud error pointing the reader at PATCH_SPEC.md so the patch can be re-derived
from its stated purpose — not from the old anchor text.
"""
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent
PATCH_SPEC_PATH = HERE / "PATCH_SPEC.md"
README_PATH = HERE / "README.md"


PATCHES = [
    {
        "id": "P1_inbound_meta_thread_id",
        "purpose": (
            "Inbound <channel> meta gains message_thread_id so the broker can "
            "route forum-topic messages to the right stub."
        ),
        "marker": "message_thread_id: String(",
        "anchor": (
            "      meta: {\n"
            "        chat_id,\n"
        ),
        "replacement": (
            "      meta: {\n"
            "        chat_id,\n"
            "        ...(ctx.message?.message_thread_id != null ? { message_thread_id: String(ctx.message.message_thread_id) } : {}),\n"
        ),
    },
    {
        "id": "P2a_reply_tool_schema_thread_id",
        "purpose": (
            "The `reply` tool's JSON schema accepts a message_thread_id arg "
            "so stubs can address a specific forum topic."
        ),
        "marker": "message_thread_id: { type: 'string' }",
        "anchor": (
            "          reply_to: {\n"
            "            type: 'string',\n"
            "            description: 'Message ID to thread under. Use message_id from the inbound <channel> block.',\n"
            "          },\n"
        ),
        "replacement": (
            "          reply_to: {\n"
            "            type: 'string',\n"
            "            description: 'Message ID to thread under. Use message_id from the inbound <channel> block.',\n"
            "          },\n"
            "          message_thread_id: { type: 'string' },\n"
        ),
    },
    {
        "id": "P2b_reply_tool_body_thread_id",
        "purpose": (
            "`reply` tool body reads message_thread_id from args as a Number, "
            "ready to be forwarded into bot.api.send*() option objects."
        ),
        "marker": "const message_thread_id =",
        "anchor": (
            "        const reply_to = args.reply_to != null ? Number(args.reply_to) : undefined\n"
        ),
        "replacement": (
            "        const reply_to = args.reply_to != null ? Number(args.reply_to) : undefined\n"
            "        const message_thread_id = args.message_thread_id != null ? Number(args.message_thread_id) : undefined\n"
        ),
    },
    {
        "id": "P2c_sendMessage_thread_id",
        "purpose": (
            "Text chunks sent via bot.api.sendMessage include message_thread_id "
            "so they land in the right forum topic."
        ),
        "marker": "...(message_thread_id != null ? { message_thread_id }",
        "anchor": (
            "            const sent = await bot.api.sendMessage(chat_id, chunks[i], {\n"
            "              ...(shouldReplyTo ? { reply_parameters: { message_id: reply_to } } : {}),\n"
            "              ...(parseMode ? { parse_mode: parseMode } : {}),\n"
            "            })\n"
        ),
        "replacement": (
            "            const sent = await bot.api.sendMessage(chat_id, chunks[i], {\n"
            "              ...(shouldReplyTo ? { reply_parameters: { message_id: reply_to } } : {}),\n"
            "              ...(parseMode ? { parse_mode: parseMode } : {}),\n"
            "              ...(message_thread_id != null ? { message_thread_id } : {}),\n"
            "            })\n"
        ),
    },
    {
        "id": "P3_disable_orphan_watchdog",
        "purpose": (
            "Short-circuit bun's orphan-watchdog setInterval: under our broker "
            "the ppid/pipe heuristic spuriously declares the parent dead and "
            "bun self-terminates. Broker handles its own shutdown."
        ),
        "marker": "C3_NO_ORPHAN_WATCHDOG",
        "anchor": (
            "setInterval(() => {\n"
            "  const orphaned =\n"
        ),
        "replacement": (
            "setInterval(() => { /* C3_NO_ORPHAN_WATCHDOG */ return;\n"
            "  const orphaned =\n"
        ),
    },
    {
        "id": "P2d_sendFile_thread_id",
        "purpose": (
            "File/photo/document sends also carry message_thread_id so "
            "attachments route to the correct forum topic."
        ),
        "marker": "message_thread_id != null ? { message_thread_id, ...(baseOpts",
        "anchor": (
            "          const opts = reply_to != null && replyMode !== 'off'\n"
            "            ? { reply_parameters: { message_id: reply_to } }\n"
            "            : undefined\n"
        ),
        "replacement": (
            "          const baseOpts = reply_to != null && replyMode !== 'off'\n"
            "            ? { reply_parameters: { message_id: reply_to } }\n"
            "            : undefined\n"
            "          const opts = message_thread_id != null ? { message_thread_id, ...(baseOpts ?? {}) } : baseOpts\n"
        ),
    },
    {
        "id": "P4_inbound_reply_to_message_meta",
        "purpose": (
            "Inbound <channel> meta surfaces Telegram quote-reply context "
            "(reply_to_message_id, reply_to_user, reply_to_text) so Claude "
            "sees which earlier message a user is replying to."
        ),
        "marker": "reply_to_message_id: String(ctx.message.reply_to_message",
        "anchor": (
            "        ts: new Date((ctx.message?.date ?? 0) * 1000).toISOString(),\n"
        ),
        "replacement": (
            "        ts: new Date((ctx.message?.date ?? 0) * 1000).toISOString(),\n"
            "        ...(ctx.message?.reply_to_message ? { reply_to_message_id: String(ctx.message.reply_to_message.message_id), reply_to_user: ctx.message.reply_to_message.from?.username ?? (ctx.message.reply_to_message.from?.id != null ? String(ctx.message.reply_to_message.from.id) : undefined), reply_to_text: ctx.message.reply_to_message.text ?? ctx.message.reply_to_message.caption } : {}),\n"
        ),
    },
    {
        "id": "P5_voice_handler_thread_id",
        "purpose": (
            "stt-handler.py echoes the transcript back to Telegram. When the "
            "transcript exceeds Telegram's 4096-char cap it's split into "
            "chunks; only chunk 0 gets reply_parameters, so chunks 1+ lose "
            "the forum topic and land in General. Pass message_thread_id to "
            "the handler so every chunk can address the right topic."
        ),
        "marker": "String(ctx.message?.message_thread_id ?? '')",
        "anchor": (
            "  const proc = Bun.spawnSync(['python3', handler, TOKEN!, chat_id, String(msgId), voice.file_id])\n"
        ),
        "replacement": (
            "  const proc = Bun.spawnSync(['python3', handler, TOKEN!, chat_id, String(msgId), voice.file_id, String(ctx.message?.message_thread_id ?? '')])\n"
        ),
    },
]


def _report_broken_patch(patch: dict, server_ts: Path) -> None:
    """Loud, actionable stderr message when a patch's anchor no longer matches.

    The goal is that a future agent (or human) hitting this error has every
    pointer they need — spec, README, plugin source — to re-derive the patch
    without spelunking through commit history.
    """
    bar = "═" * 78
    sys.stderr.write(
        f"\n{bar}\n"
        f"c3-patcher: PATCH BROKEN — {patch['id']}\n"
        f"{bar}\n"
        f"Purpose:\n  {patch['purpose']}\n\n"
        f"What happened:\n"
        f"  The anchor text this patch splices against is no longer present\n"
        f"  in the upstream plugin's server.ts. This usually means the plugin\n"
        f"  was updated and that region was refactored or already patched\n"
        f"  upstream. The feature this patch provides is NOT active right now.\n\n"
        f"How to repair:\n"
        f"  1. Read {PATCH_SPEC_PATH} section '{patch['id']}' for the final\n"
        f"     behavior this patch is supposed to achieve.\n"
        f"  2. Check if upstream already added it: grep server.ts for\n"
        f"     the concept (e.g. 'reply_to_message' for P4). If present,\n"
        f"     update this patch's `marker` so it skips cleanly.\n"
        f"  3. If still needed, open server.ts and find the region described\n"
        f"     in the spec. Derive a new `anchor`/`replacement` pair here in\n"
        f"     patch_server.py that produces the same final behavior.\n"
        f"  4. Re-run the broker; it re-applies patches idempotently on start.\n\n"
        f"Files:\n"
        f"  spec    : {PATCH_SPEC_PATH}\n"
        f"  readme  : {README_PATH}\n"
        f"  patcher : {Path(__file__).resolve()}\n"
        f"  target  : {server_ts}\n"
        f"  backup  : {server_ts}.c3-backup (pristine copy, pre-first-patch)\n"
        f"{bar}\n\n"
    )


def apply_patches(plugin_dir: Path) -> list[str]:
    server_ts = plugin_dir / "server.ts"
    src = server_ts.read_text()
    applied: list[str] = []
    broken: list[str] = []
    for patch in PATCHES:
        if patch["marker"] in src:
            continue
        if patch["anchor"] not in src:
            _report_broken_patch(patch, server_ts)
            broken.append(patch["id"])
            continue
        src = src.replace(patch["anchor"], patch["replacement"], 1)
        applied.append(patch["id"])
    if applied:
        backup = plugin_dir / "server.ts.c3-backup"
        if not backup.exists():
            backup.write_text(server_ts.read_text())
        server_ts.write_text(src)
    if broken:
        sys.stderr.write(
            f"c3-patcher: {len(broken)} patch(es) failed to apply — see above. "
            f"The broker will run, but affected features are off.\n"
        )
    return applied


if __name__ == "__main__":
    base = Path.home() / ".claude" / "plugins" / "cache" / "claude-plugins-official" / "telegram"
    versions = sorted([p for p in base.iterdir() if p.is_dir() and re.match(r"^\d+\.\d+\.\d+$", p.name)])
    if not versions:
        sys.stderr.write("c3-patcher: no telegram plugin versions found\n")
        sys.exit(1)
    latest = versions[-1]
    applied = apply_patches(latest)
    if applied:
        print(f"applied {len(applied)} patch(es) to {latest.name}: {applied}")
    else:
        print(f"{latest.name}: all patches already present (or broken — see stderr)")
