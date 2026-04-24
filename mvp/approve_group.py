#!/usr/bin/env python3
"""Approve a Telegram group for C3 by adding it to access.json.

The official plugin silently drops messages from groups not present in
`access.json` → `groups`. This script adds a permissive entry so the group's
traffic starts reaching the broker.

Chat id can come from:
  - a `t.me/c/<internal>/...` URL — the internal id is prefixed with -100
  - a raw Bot API id (starts with -100)
  - a raw internal id (digits only) — the script adds -100

`access.json` is re-read by bun on every inbound message (unless
`TELEGRAM_ACCESS_MODE=static`), so no broker restart is needed.

Usage
-----
  python3 approve_group.py <url-or-id>
  python3 approve_group.py <url-or-id> --require-mention
  python3 approve_group.py <url-or-id> --allow-from 12345 67890
"""
import argparse
import json
import os
import re
import sys
from pathlib import Path

ACCESS_FILE = Path.home() / ".claude" / "channels" / "telegram" / "access.json"


def parse_chat_id(raw: str) -> str:
    m = re.search(r"t\.me/c/(\d+)", raw)
    if m:
        return f"-100{m.group(1)}"
    raw = raw.strip()
    if raw.startswith("-100") and raw[1:].isdigit():
        return raw
    if raw.isdigit():
        return f"-100{raw}"
    raise ValueError(f"can't parse chat id from: {raw!r}")


def main() -> None:
    ap = argparse.ArgumentParser(description="Approve a Telegram group for C3.")
    ap.add_argument("url_or_id", help="t.me/c/... URL, internal id, or Bot API id")
    ap.add_argument("--require-mention", action="store_true",
                    help="Route only messages that @-mention the bot (default: off)")
    ap.add_argument("--allow-from", nargs="*", default=[],
                    help="User ids allowed to message (default: empty = any group member)")
    args = ap.parse_args()

    chat_id = parse_chat_id(args.url_or_id)
    access = json.loads(ACCESS_FILE.read_text())
    access.setdefault("groups", {})
    verb = "updated" if chat_id in access["groups"] else "added"
    access["groups"][chat_id] = {
        "requireMention": bool(args.require_mention),
        "allowFrom": list(args.allow_from),
    }

    tmp = ACCESS_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(access, indent=2) + "\n")
    os.chmod(tmp, 0o600)
    tmp.replace(ACCESS_FILE)

    print(f"{verb} group {chat_id} in {ACCESS_FILE}")
    print(f"  requireMention: {bool(args.require_mention)}")
    print(f"  allowFrom:      {args.allow_from or '(any member)'}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        sys.stderr.write(f"approve_group: {e}\n")
        sys.exit(1)
