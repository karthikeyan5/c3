#!/usr/bin/env bash
# Create / refresh the dedicated C3 STT virtualenv and install its Python deps.
#
# WHY: C3's STT plugin auto-detects this venv (~/.config/c3/stt-venv/bin/python)
# and runs the handler under it. As of 2026-06-22 the STT chain has NO required
# PyPI packages (the Sarvam >30s batch path is native stdlib urllib), so the venv
# is OPTIONAL — system python3 works fine. Kept as a stable, isolated interpreter
# target and to install any optional deps listed in requirements.txt.
#
# Idempotent — safe to re-run.
#
# Interpreter selection: tries python3.12 / 3.11 / 3.13 / python3, using the
# first that actually RUNS (a pyenv shim can exist but fail if that version
# isn't active). Override with C3_STT_PYTHON=/abs/path/to/python.
# Venv location override: C3_STT_VENV=/abs/path.
#
# ffprobe (from ffmpeg) is a SYSTEM dependency — install it via your OS package
# manager. STT degrades gracefully without it.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REQ="$HERE/requirements.txt"
VENV="${C3_STT_VENV:-${XDG_CONFIG_HOME:-$HOME/.config}/c3/stt-venv}"

PY="${C3_STT_PYTHON:-}"
if [ -z "$PY" ]; then
  for cand in python3.12 python3.11 python3.13 python3; do
    if command -v "$cand" >/dev/null 2>&1 && "$cand" --version >/dev/null 2>&1; then
      PY="$cand"; break
    fi
  done
fi
if [ -z "$PY" ]; then
  echo "setup-venv: no working python3 found; set C3_STT_PYTHON=/abs/path/to/python" >&2
  exit 1
fi

echo "setup-venv: base interpreter: $("$PY" --version 2>&1) ($PY)"
echo "setup-venv: venv: $VENV"
"$PY" -m venv "$VENV"
"$VENV/bin/python" -m pip install --quiet --upgrade pip
"$VENV/bin/python" -m pip install --quiet -r "$REQ"
echo "setup-venv: requirements installed from $REQ (STT currently needs no PyPI deps)."
echo "setup-venv: done — C3 auto-detects $VENV/bin/python (or set mappings.json plugins.stt.python)."

if ! command -v ffprobe >/dev/null 2>&1; then
  echo "setup-venv: NOTE ffprobe not found — install ffmpeg for audio-duration detection (STT still works, REST-first)." >&2
fi
