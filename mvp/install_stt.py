#!/usr/bin/env python3
"""Install C3's bundled STT handler into the Telegram plugin's expected path.

The official plugin hardcodes `~/.claude/channels/telegram/stt-handler.py`
as the STT handler entrypoint (see the `message:voice` handler in its
server.ts). C3 ships the handler and its `stt-pkg/` sibling under
`c3/mvp/stt/` and symlinks them into the plugin's expected location so edits
to the C3 repo take effect immediately, without copy/sync.

Pre-existing real files at the destination are moved aside to
`.pre-c3/<timestamp>/` once — never silently overwritten.
"""
from pathlib import Path
import shutil
import sys
import time

SRC_DIR = Path(__file__).resolve().parent / "stt"
DST_DIR = Path.home() / ".claude" / "channels" / "telegram"
LINKS = ["stt-handler.py", "stt-pkg"]


def install() -> None:
    DST_DIR.mkdir(parents=True, exist_ok=True)
    for name in LINKS:
        src = (SRC_DIR / name).resolve()
        dst = DST_DIR / name
        if not src.exists():
            sys.stderr.write(f"c3-stt: source {src} missing; skipping\n")
            continue
        if dst.is_symlink():
            try:
                if dst.resolve() == src:
                    continue
            except OSError:
                pass
            dst.unlink()
        elif dst.exists():
            backup_root = DST_DIR / ".pre-c3" / time.strftime("%Y%m%dT%H%M%S")
            backup_root.mkdir(parents=True, exist_ok=True)
            shutil.move(str(dst), str(backup_root / name))
            sys.stderr.write(f"c3-stt: moved existing {dst} → {backup_root / name}\n")
        dst.symlink_to(src)
        sys.stderr.write(f"c3-stt: linked {dst} → {src}\n")


if __name__ == "__main__":
    install()
