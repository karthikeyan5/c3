#!/usr/bin/env python3
"""Pluggable STT chain — runs providers in order until one succeeds.

Usage: python3 stt.py <audio_file>
       python3 stt.py <audio_file> --chain gemini,sarvam
       python3 stt.py <audio_file> --chain sarvam  (sarvam only, no fallback)

Config:
  --chain <providers>   Comma-separated provider names (default: gemini,sarvam)
  --retries <n>         Retries per provider before moving to next (default: 1)
  --retry-delay <s>     Seconds between retries (default: 2)

Providers are Python files in the providers/ directory.
Each must implement: transcribe(audio_path: str, audio_bytes: bytes) -> str | None

To add a new provider:
  1. Create providers/my_provider.py with a transcribe() function
  2. Use --chain gemini,my_provider,sarvam

Stdout: clean transcript only
Stderr: retry/fallback/error logging
"""
import sys, os, importlib.util, argparse, time, json, re, subprocess

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROVIDERS_DIR = os.path.join(SCRIPT_DIR, "providers")

# --- Energy / silence gate ---
# An LLM-based STT provider (Gemini 3 Flash via OpenRouter) will confabulate a
# fluent, ENTIRELY-INVENTED transcript when handed silent/near-silent audio —
# even with explicit anti-fabrication + <<NO_SPEECH>> rules in its system prompt
# (confirmed by live test: pure digital silence -> a full fabricated lecture).
# Prompt-level rules do NOT stop it. The only reliable defense is to NOT send
# effectively-silent audio to any provider. We measure peak loudness with
# ffmpeg's volumedetect filter and, if it's below the threshold, gate out.
#
# volumedetect reports peak amplitude on stderr as e.g. "max_volume: -91.0 dB".
# Pure digital silence measures ~ -91 dB; real speech peaks far higher (roughly
# -30 to -3 dB), so a simple energy threshold cleanly separates them.
SILENCE_MAX_DB_DEFAULT = -50.0

def _resolve_silence_threshold_db(default_db=SILENCE_MAX_DB_DEFAULT):
    """Resolve the max_volume threshold (dB) below which audio counts as silent.

    Reads $C3_STT_SILENCE_MAX_DB; falls back to `default_db` when unset, blank,
    or unparseable. Never raises — a bad env value must not break transcription."""
    raw = os.environ.get("C3_STT_SILENCE_MAX_DB")
    if raw is None or not raw.strip():
        return default_db
    try:
        return float(raw.strip())
    except ValueError:
        return default_db

def _parse_max_volume_db(ffmpeg_stderr: str):
    """Parse ffmpeg volumedetect's 'max_volume: <N> dB' out of its stderr.

    Returns the float dB value, or None when the field is absent/unparseable
    (so the caller fails OPEN and skips the gate). Pure — no I/O."""
    if not ffmpeg_stderr:
        return None
    m = re.search(r"max_volume:\s*(-?\d+(?:\.\d+)?)\s*dB", ffmpeg_stderr)
    if not m:
        return None
    try:
        return float(m.group(1))
    except ValueError:
        return None

def _is_effectively_silent(max_volume_db: float, threshold_db: float) -> bool:
    """True when peak loudness is below the threshold (i.e. no real speech).

    Boundary: exactly AT the threshold is NOT silent — we bias toward attempting
    transcription (fail toward doing the work) so only clearly-silent audio is
    gated. Pure — no I/O."""
    return max_volume_db < threshold_db

def _measure_max_volume_db(audio_path):
    """Measure peak loudness (dB) of an audio file via ffmpeg volumedetect.

    Thin subprocess wrapper: runs
      ffmpeg -hide_banner -i <path> -af volumedetect -f null -
    (volumedetect prints its summary to stderr) and parses out max_volume.

    Returns the float dB peak, or None if ffmpeg is missing, times out, or the
    output can't be parsed. Callers treat None as 'measurement unavailable' and
    FAIL OPEN (skip the gate) — a broken measurement must never block real
    speech. Mirrors sarvam-saaras-v3.py:_get_duration's shell-out-to-ffprobe
    style (stdlib subprocess only, no third-party deps)."""
    try:
        result = subprocess.run(
            ["ffmpeg", "-hide_banner", "-i", audio_path,
             "-af", "volumedetect", "-f", "null", "-"],
            capture_output=True, text=True, timeout=15,
        )
    except Exception:
        return None
    # volumedetect writes its summary to stderr; parse there (fall back to
    # stdout defensively in case a build routes it differently). Note: 0.0 dB is
    # a legitimate value, so test for None explicitly rather than truthiness.
    db = _parse_max_volume_db(result.stderr or "")
    if db is None:
        db = _parse_max_volume_db(result.stdout or "")
    return db

# --- Domain vocabulary ---
# Shared across all providers. Each provider adapts this into its own format
# (system prompt, hotwords parameter, etc.)
#
# Resolution order (first found wins):
#   1. $C3_STT_VOCAB (explicit override path)
#   2. $XDG_CONFIG_HOME/c3/stt-vocabulary.txt
#   3. ~/.config/c3/stt-vocabulary.txt
#   4. <stt-pkg>/vocabulary.txt (bundled default, generic-tech-only)
#   5. <stt-pkg>/vocabulary.json (legacy advanced format)
#
# Users keep personal/project terms in the override path so they don't
# get clobbered on c3 upgrades and don't ship to other installers.
def _vocab_search_paths():
    paths = []
    if env := os.environ.get("C3_STT_VOCAB"):
        paths.append(env)
    xdg = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    paths.append(os.path.join(xdg, "c3", "stt-vocabulary.txt"))
    paths.append(os.path.join(SCRIPT_DIR, "vocabulary.txt"))
    return paths

VOCAB_JSON_PATH = os.path.join(SCRIPT_DIR, "vocabulary.json")

def load_vocabulary():
    """Load domain vocabulary.

    Tries vocabulary.txt at the user override path first, then the
    bundled default. Format:
      - First line starting with # context: is the context description
      - Other lines starting with # are ignored (comments)
      - Each non-empty line is a preferred term
      - Optional: "Vel != whale, well, veil" to specify common misheard alternatives
      - Optional: "Vel -- a software framework" to add a note

    Falls back to vocabulary.json (advanced format) if no txt exists.
    Returns dict with 'terms' (list) and 'context' (string).
    """
    for p in _vocab_search_paths():
        if os.path.exists(p):
            try:
                vocab = _parse_vocab_txt(p)
                vocab["source"] = p
                return vocab
            except Exception:
                continue
    if os.path.exists(VOCAB_JSON_PATH):
        try:
            with open(VOCAB_JSON_PATH) as f:
                v = json.load(f)
                v.setdefault("source", VOCAB_JSON_PATH)
                return v
        except Exception:
            pass
    return {"terms": [], "context": "", "source": ""}

def _parse_vocab_txt(path):
    """Parse simple vocabulary.txt format."""
    terms = []
    context = ""
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            if line.startswith("# context:"):
                context = line[len("# context:"):].strip()
                continue
            if line.startswith("#"):
                continue
            # Parse: "Term != alt1, alt2 -- note"
            note = ""
            nots = []
            if " -- " in line:
                line, note = line.rsplit(" -- ", 1)
                note = note.strip()
            if " != " in line:
                preferred, not_str = line.split(" != ", 1)
                preferred = preferred.strip()
                nots = [n.strip() for n in not_str.split(",")]
            else:
                preferred = line.strip()
            terms.append({"preferred": preferred, "not": nots, "note": note})
    return {"terms": terms, "context": context}

def load_provider(name):
    """Dynamically load a provider module from providers/<name>.py"""
    path = os.path.join(PROVIDERS_DIR, f"{name}.py")
    if not os.path.exists(path):
        print(f"[stt] provider not found: {path}", file=sys.stderr)
        return None
    spec = importlib.util.spec_from_file_location(f"providers.{name}", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    if not hasattr(mod, "transcribe"):
        print(f"[stt] provider {name} missing transcribe() function", file=sys.stderr)
        return None
    return mod

def main():
    parser = argparse.ArgumentParser(description="Pluggable STT chain")
    parser.add_argument("audio_file", help="Path to audio file")
    parser.add_argument("--chain", default="gemini-3-flash-openrouter,sarvam-saaras-v3", help="Comma-separated provider chain (default: gemini-3-flash-openrouter,sarvam-saaras-v3)")
    parser.add_argument("--retries", type=int, default=1, help="Retries per provider (default: 1)")
    parser.add_argument("--retry-delay", type=float, default=2.0, help="Delay between retries in seconds (default: 2)")
    args = parser.parse_args()

    audio_path = args.audio_file
    if not os.path.exists(audio_path):
        print(f"ERROR: File not found: {audio_path}", file=sys.stderr)
        sys.exit(1)

    with open(audio_path, "rb") as f:
        audio_bytes = f.read()

    # ── Energy / silence gate (anti-fabrication) ─────────────────────────────
    # BEFORE any provider runs: if the audio is effectively silent, do NOT hand
    # it to the LLM (it would confabulate a fabricated transcript — see the
    # module header). Exit exactly like the all-providers-failed path below
    # (empty stdout, exit 1) so the Go shim's graceful [STT FAILED] path is
    # unchanged — no new stdout contract. FAIL OPEN on any measurement problem
    # (ffmpeg missing, unparseable output, timeout): skip the gate and fall
    # through to the normal chain so real speech is never blocked by a broken
    # measurement.
    threshold_db = _resolve_silence_threshold_db()
    max_volume_db = _measure_max_volume_db(audio_path)
    if max_volume_db is None:
        print("[stt] silence gate skipped: could not measure loudness "
              "(ffmpeg missing/failed or unparseable output); proceeding to providers",
              file=sys.stderr)
    elif _is_effectively_silent(max_volume_db, threshold_db):
        print(f"[stt] gated: audio effectively silent "
              f"(max_volume={max_volume_db}dB < {threshold_db}dB); no speech to transcribe",
              file=sys.stderr)
        # Same terminal contract as "all providers failed": nothing on stdout,
        # non-zero exit -> Go shim surfaces the graceful "[STT FAILED]" notice.
        sys.exit(1)
    else:
        print(f"[stt] loudness ok (max_volume={max_volume_db}dB >= {threshold_db}dB)",
              file=sys.stderr)

    chain = [name.strip() for name in args.chain.split(",") if name.strip()]
    if not chain:
        print("ERROR: empty provider chain", file=sys.stderr)
        sys.exit(1)

    # Load domain vocabulary
    vocab = load_vocabulary()
    if vocab.get("terms"):
        src = vocab.get("source") or "?"
        print(f"[stt] loaded {len(vocab['terms'])} vocabulary terms from {src}", file=sys.stderr)

    # Load all providers upfront
    providers = []
    for name in chain:
        mod = load_provider(name)
        if mod:
            providers.append((name, mod))
        else:
            print(f"[stt] skipping unavailable provider: {name}", file=sys.stderr)

    if not providers:
        print("ERROR: no valid providers in chain", file=sys.stderr)
        sys.exit(1)

    # Run chain: try each provider with retries
    for idx, (name, mod) in enumerate(providers):
        max_attempts = 1 + args.retries  # 1 initial + N retries
        for attempt in range(1, max_attempts + 1):
            try:
                # Pass vocabulary if provider supports it
                if hasattr(mod, 'set_vocabulary'):
                    mod.set_vocabulary(vocab)
                result = mod.transcribe(audio_path, audio_bytes)
                if result and result.strip():
                    if idx > 0 or attempt > 1:
                        print(f"[stt] success: {name} (attempt {attempt})", file=sys.stderr)
                    print(result.strip())
                    sys.exit(0)
                else:
                    print(f"[stt] {name} attempt {attempt}/{max_attempts}: empty result", file=sys.stderr)
            except Exception as e:
                print(f"[stt] {name} attempt {attempt}/{max_attempts}: {type(e).__name__}: {e}", file=sys.stderr)

            if attempt < max_attempts:
                print(f"[stt] retrying {name} in {args.retry_delay}s...", file=sys.stderr)
                time.sleep(args.retry_delay)

        # Provider exhausted, move to next
        if idx < len(providers) - 1:
            next_name = providers[idx + 1][0]
            print(f"[stt] {name} exhausted, falling back to {next_name}", file=sys.stderr)

    print("ERROR: all providers failed", file=sys.stderr)
    sys.exit(1)

if __name__ == "__main__":
    main()
