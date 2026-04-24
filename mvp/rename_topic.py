#!/usr/bin/env python3
"""Rename an entry in c3/mvp/topics.json (C3's local topic registry).

When the broker first sees a new `(chat_id, topic_id)` — e.g. on the first
inbound message from a group's general topic — it upserts an entry with a
placeholder name like `topic-0`. Rename it here so `attach(target='<name>')`
can reach it by a sensible human name.

Two forms:
  1. python3 rename_topic.py <current_name> <new_name>
  2. python3 rename_topic.py --chat-id <id> --topic-id <id> <new_name>

Notes
-----
- This does NOT rename the Telegram forum topic itself. Telegram's Bot API
  has `editForumTopic` for that — a follow-up if you want Telegram's UI
  to track C3's names.
- There's a narrow race with the broker's `upsert_topic` (which only writes
  when adding a brand-new entry). Re-run if the rename gets clobbered by a
  concurrent insert.
"""
import argparse
import json
import sys
from pathlib import Path

TOPICS_FILE = Path(__file__).resolve().parent / "topics.json"


def main() -> None:
    ap = argparse.ArgumentParser(description="Rename a topic entry in topics.json.")
    ap.add_argument("names", nargs="+",
                    help="<current_name> <new_name>, or just <new_name> with --chat-id/--topic-id")
    ap.add_argument("--chat-id", type=int)
    ap.add_argument("--topic-id", type=int)
    args = ap.parse_args()

    if args.chat_id is not None and args.topic_id is not None:
        if len(args.names) != 1:
            ap.error("with --chat-id/--topic-id, pass only the new name")
        match = lambda t: t["chat_id"] == args.chat_id and t["topic_id"] == args.topic_id
        new_name = args.names[0]
        query = f"(chat_id={args.chat_id}, topic_id={args.topic_id})"
    else:
        if len(args.names) != 2:
            ap.error("positional form: <current_name> <new_name>")
        current_name, new_name = args.names
        match = lambda t: t["name"] == current_name
        query = f"name={current_name!r}"

    topics = json.loads(TOPICS_FILE.read_text())
    matches = [t for t in topics if match(t)]
    if not matches:
        sys.stderr.write(f"no topic matching {query}\n")
        sys.exit(1)
    if len(matches) > 1:
        sys.stderr.write(f"ambiguous: {len(matches)} topics match {query}\n")
        sys.exit(1)

    old = dict(matches[0])
    matches[0]["name"] = new_name

    tmp = TOPICS_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(topics, indent=2) + "\n")
    tmp.replace(TOPICS_FILE)

    print(f"renamed {old['name']!r} → {new_name!r}  (chat_id={old['chat_id']}, topic_id={old['topic_id']})")


if __name__ == "__main__":
    main()
