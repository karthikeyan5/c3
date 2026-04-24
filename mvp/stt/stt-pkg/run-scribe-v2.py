#!/usr/bin/env python3
"""Run ElevenLabs Scribe v2 against all STT quality test samples."""
import sys, os, time

# Load env from ~/.openclaw/.env
env_path = os.path.expanduser("~/.openclaw/.env")
if os.path.exists(env_path):
    with open(env_path) as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, v = line.split("=", 1)
                os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))

sys.path.insert(0, os.path.dirname(__file__))
from providers import elevenlabs_scribe_v2 as provider

SAMPLES_DIR = "/home/claw/.openclaw/workspace/archive/tests/stt-quality"

samples = [
    "sample-01-audit.ogg",
    "sample-02-stt-fix.ogg",
    "sample-02-chunk1.ogg",
    "sample-02-chunk2.ogg",
    "sample-02-chunk2b.ogg",
    "sample-02-chunk3.ogg",
    "sample-03-hindi-tamil-switch.ogg",
    "sample-04-slash-commands.ogg",
    "sample-05-multilingual-tamil-hindi-english.ogg",
    "sample-06-tamil-mixed-scolding.ogg",
    "sample-07-gemini-hallucination.ogg",
    "sample-08-collection-call-hindi.ogg",
    "sample-09-prompt-injection.ogg",
    "sample-10-analysis-list-hallucination.ogg",
    "sample-11-preamble-hallucination.ogg",
    "sample-12-tamil-english-codeswitching.ogg",
    "sample-13-short-v6-mishearing.ogg",
    "sample-14-gemini-empty-failure.ogg",
    "sample-15-sycophancy-benchmark-question.ogg",
]

results = {}

for sample in samples:
    path = os.path.join(SAMPLES_DIR, sample)
    if not os.path.exists(path):
        print(f"[SKIP] {sample} — file not found")
        results[sample] = "FILE NOT FOUND"
        continue

    print(f"[...] {sample}", flush=True)
    try:
        with open(path, "rb") as f:
            audio_bytes = f.read()
        transcript = provider.transcribe(path, audio_bytes)
        results[sample] = transcript or "(empty response)"
        print(f" → {results[sample][:120]}", flush=True)
    except Exception as e:
        results[sample] = f"ERROR: {e}"
        print(f" → ERROR: {e}", flush=True)

    time.sleep(1)  # Rate limit buffer

print("\n=== All done ===")
for k, v in results.items():
    print(f"\n--- {k} ---\n{v}")
