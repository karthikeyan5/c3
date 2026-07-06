#!/usr/bin/env python3
"""Hermetic unit tests for stt.py's energy/silence gate.

Runnable as:  python3 test_silence_gate.py
Exits 0 when all checks pass, non-zero on the first failing group.

Dependency-light on purpose (matches the repo style): plain `assert`s, stdlib
only, NO pytest. Covers ONLY the pure, decision-making helpers — it never runs
ffmpeg and never touches audio or the network. The subprocess wrapper
(_measure_max_volume_db) is intentionally NOT exercised here for that reason.
"""
import os
import sys

# Import the module under test from the same directory. stt.py guards main()
# behind `if __name__ == "__main__"`, so importing it has no side effects.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import stt  # noqa: E402


# A realistic multi-line ffmpeg volumedetect stderr sample for PURE SILENCE.
# This is the shape stt._measure_max_volume_db feeds to _parse_max_volume_db.
SILENCE_STDERR = """\
ffmpeg version 6.1.1 Copyright (c) 2000-2023 the FFmpeg developers
Input #0, ogg, from '/home/user/.claude/channels/telegram/inbox/1720000000-abc.oga':
  Duration: 00:00:03.00, start: 0.000000, bitrate: 4 kb/s
  Stream #0:0: Audio: opus, 48000 Hz, mono, fltp
Stream mapping:
  Stream #0:0 (opus) -> volumedetect:default
  volumedetect:default -> Stream #0:0 (pcm_s16le)
Output #0, null, to 'pipe:':
  Stream #0:0: Audio: pcm_s16le, 48000 Hz, mono, s16, 768 kb/s
size=N/A time=00:00:03.00 bitrate=N/A speed= 250x
[Parsed_volumedetect_0 @ 0x55e8f3a2b400] n_samples: 144000
[Parsed_volumedetect_0 @ 0x55e8f3a2b400] mean_volume: -91.0 dB
[Parsed_volumedetect_0 @ 0x55e8f3a2b400] max_volume: -91.0 dB
[Parsed_volumedetect_0 @ 0x55e8f3a2b400] histogram_91db: 144000
"""

# Real speech: peaks far higher.
SPEECH_STDERR = """\
[Parsed_volumedetect_0 @ 0x561122334455] n_samples: 288000
[Parsed_volumedetect_0 @ 0x561122334455] mean_volume: -24.3 dB
[Parsed_volumedetect_0 @ 0x561122334455] max_volume: -3.1 dB
[Parsed_volumedetect_0 @ 0x561122334455] histogram_3db: 12
"""

# Some ffmpeg builds floor pure silence at -inf instead of a finite ~ -91 dB.
INF_SILENCE_STDERR = """\
[Parsed_volumedetect_0 @ 0x1] n_samples: 144000
[Parsed_volumedetect_0 @ 0x1] mean_volume: -inf dB
[Parsed_volumedetect_0 @ 0x1] max_volume: -inf dB
"""

# A COMPLETED but EMPTY decode: volumedetect reports zero samples and emits NO
# max_volume line at all. Unambiguously silent — must be gated, not failed open.
EMPTY_DECODE_STDERR = """\
ffmpeg version 8.1.2 Copyright (c) 2000-2024 the FFmpeg developers
Input #0, ogg, from '/home/user/.claude/channels/telegram/inbox/1720000000-zzz.oga':
  Duration: 00:00:00.00, start: 0.000000, bitrate: N/A
Output #0, null, to 'pipe:':
size=N/A time=00:00:00.00 bitrate=N/A speed=  0x
[Parsed_volumedetect_0 @ 0x55e8f3a2b400] n_samples: 0
"""


def test_parse_max_volume_db():
    # Parses a real multi-line silence sample.
    assert stt._parse_max_volume_db(SILENCE_STDERR) == -91.0
    # Parses real speech.
    assert stt._parse_max_volume_db(SPEECH_STDERR) == -3.1
    # A legitimate 0.0 dB peak (loud/clipping audio) parses to 0.0, NOT None.
    assert stt._parse_max_volume_db(
        "[Parsed_volumedetect_0 @ 0x1] max_volume: 0.0 dB") == 0.0
    # A positive dB value (rare, but volumedetect can report it) parses.
    assert stt._parse_max_volume_db(
        "[Parsed_volumedetect_0 @ 0x1] max_volume: 1.5 dB") == 1.5
    # The -inf silence floor some ffmpeg builds emit -> float('-inf').
    assert stt._parse_max_volume_db(
        "[Parsed_volumedetect_0 @ 0x1] max_volume: -inf dB") == float("-inf")
    assert stt._parse_max_volume_db(INF_SILENCE_STDERR) == float("-inf")
    # A (harmless, loud) +inf -> float('inf'), NOT None.
    assert stt._parse_max_volume_db(
        "[Parsed_volumedetect_0 @ 0x1] max_volume: inf dB") == float("inf")
    # Unparseable / absent field -> None (caller then fails open).
    assert stt._parse_max_volume_db("this has no volume line at all") is None
    assert stt._parse_max_volume_db("mean_volume: -20.0 dB\n") is None  # no max_volume
    assert stt._parse_max_volume_db("") is None
    assert stt._parse_max_volume_db(None) is None
    print("PASS test_parse_max_volume_db")


def test_parse_n_samples():
    assert stt._parse_n_samples(SILENCE_STDERR) == 144000
    assert stt._parse_n_samples(EMPTY_DECODE_STDERR) == 0
    # Absent / unparseable / empty -> None.
    assert stt._parse_n_samples("no samples reported here") is None
    assert stt._parse_n_samples("") is None
    assert stt._parse_n_samples(None) is None
    print("PASS test_parse_n_samples")


def test_resolve_measured_db():
    # Finite silence and speech pass straight through.
    assert stt._resolve_measured_db(SILENCE_STDERR, "") == -91.0
    assert stt._resolve_measured_db(SPEECH_STDERR, "") == -3.1
    # The -inf silence floor is preserved (gated, not failed open).
    assert stt._resolve_measured_db(INF_SILENCE_STDERR, "") == float("-inf")
    assert stt._resolve_measured_db(
        "[Parsed_volumedetect_0 @ 0x1] max_volume: -inf dB", "") == float("-inf")
    # Completed-but-empty decode (n_samples: 0, NO max_volume) -> measured silent.
    assert stt._resolve_measured_db(EMPTY_DECODE_STDERR, "") == float("-inf")
    # Defensive stdout fallback: max_volume routed to stdout still parses.
    assert stt._resolve_measured_db("", SPEECH_STDERR) == -3.1
    # Genuine measurement failure (no summary at all) -> None => FAIL OPEN.
    assert stt._resolve_measured_db("ffmpeg: some error, no summary", "") is None
    assert stt._resolve_measured_db("", "") is None
    print("PASS test_resolve_measured_db")


def test_is_effectively_silent():
    default = stt.SILENCE_MAX_DB_DEFAULT  # -50.0
    # Pure silence: -91 < -50 -> silent.
    assert stt._is_effectively_silent(-91.0, default) is True
    # Real speech peak: -20 >= -50 -> not silent.
    assert stt._is_effectively_silent(-20.0, default) is False
    # Boundary: EXACTLY at threshold is NOT silent (bias toward transcribing).
    assert stt._is_effectively_silent(-50.0, -50.0) is False
    # Just below threshold -> silent.
    assert stt._is_effectively_silent(-50.01, -50.0) is True
    # Loud 0.0 dB peak -> not silent.
    assert stt._is_effectively_silent(0.0, default) is False
    # -inf floor (some ffmpeg builds) -> silent; +inf (loud) -> not silent.
    assert stt._is_effectively_silent(float("-inf"), -50.0) is True
    assert stt._is_effectively_silent(float("inf"), -50.0) is False
    print("PASS test_is_effectively_silent")


def test_resolve_silence_threshold_db():
    saved = os.environ.get("C3_STT_SILENCE_MAX_DB")
    try:
        # Unset -> default.
        os.environ.pop("C3_STT_SILENCE_MAX_DB", None)
        assert stt._resolve_silence_threshold_db() == stt.SILENCE_MAX_DB_DEFAULT

        # Explicit override is respected.
        os.environ["C3_STT_SILENCE_MAX_DB"] = "-40.0"
        assert stt._resolve_silence_threshold_db() == -40.0

        # Integer-looking override.
        os.environ["C3_STT_SILENCE_MAX_DB"] = "-60"
        assert stt._resolve_silence_threshold_db() == -60.0

        # Whitespace is tolerated.
        os.environ["C3_STT_SILENCE_MAX_DB"] = "  -55.5  "
        assert stt._resolve_silence_threshold_db() == -55.5

        # Blank -> default (not 0.0).
        os.environ["C3_STT_SILENCE_MAX_DB"] = "   "
        assert stt._resolve_silence_threshold_db() == stt.SILENCE_MAX_DB_DEFAULT

        # Garbage -> default (never raises).
        os.environ["C3_STT_SILENCE_MAX_DB"] = "not-a-number"
        assert stt._resolve_silence_threshold_db() == stt.SILENCE_MAX_DB_DEFAULT

        # A caller-supplied default is honored when unset.
        os.environ.pop("C3_STT_SILENCE_MAX_DB", None)
        assert stt._resolve_silence_threshold_db(-33.0) == -33.0
    finally:
        if saved is None:
            os.environ.pop("C3_STT_SILENCE_MAX_DB", None)
        else:
            os.environ["C3_STT_SILENCE_MAX_DB"] = saved
    print("PASS test_resolve_silence_threshold_db")


def test_end_to_end_decision():
    """The parse + threshold + decide pipeline, end to end, WITHOUT ffmpeg:
    silence stderr -> parsed -> judged silent at the default threshold."""
    threshold = stt._resolve_silence_threshold_db()
    db = stt._parse_max_volume_db(SILENCE_STDERR)
    assert db is not None
    assert stt._is_effectively_silent(db, threshold) is True

    db2 = stt._parse_max_volume_db(SPEECH_STDERR)
    assert db2 is not None
    assert stt._is_effectively_silent(db2, threshold) is False
    print("PASS test_end_to_end_decision")


def main():
    tests = [
        test_parse_max_volume_db,
        test_parse_n_samples,
        test_resolve_measured_db,
        test_is_effectively_silent,
        test_resolve_silence_threshold_db,
        test_end_to_end_decision,
    ]
    failed = 0
    for t in tests:
        try:
            t()
        except AssertionError as e:
            failed += 1
            print(f"FAIL {t.__name__}: {e}", file=sys.stderr)
        except Exception as e:
            failed += 1
            print(f"ERROR {t.__name__}: {type(e).__name__}: {e}", file=sys.stderr)
    if failed:
        print(f"\n{failed} test(s) failed", file=sys.stderr)
        sys.exit(1)
    print(f"\nAll {len(tests)} test groups passed")


if __name__ == "__main__":
    main()
