"""Sarvam AI Saaras v3 STT provider.

Requires: SARVAM_API_KEY env var (or in ~/.claude/stt.env)
For audio >30s: requires sarvamai Python package (pip install sarvamai)
"""
import os, json, time, subprocess, tempfile, urllib.request, urllib.error

# Dynamic vocabulary (set by main stt.py via set_vocabulary)
_VOCAB = {"terms": [], "context": ""}

def set_vocabulary(vocab):
    global _VOCAB
    _VOCAB = vocab or {"terms": [], "context": ""}

def _get_prompt():
    """Build a prompt string from vocabulary for Sarvam's prompt parameter."""
    if not _VOCAB.get("terms"):
        return None
    context = _VOCAB.get("context", "")
    terms = ", ".join(t["preferred"] for t in _VOCAB["terms"])
    return f"{context} Key terms: {terms}" if context else f"Key terms: {terms}"

# --- Load API key ---
_SARVAM_KEY = None

def _get_key():
    global _SARVAM_KEY
    if _SARVAM_KEY:
        return _SARVAM_KEY
    _SARVAM_KEY = os.environ.get("SARVAM_API_KEY", "")
    if not _SARVAM_KEY:
        env_path = os.path.expanduser("~/.claude/stt.env")
        try:
            with open(env_path) as f:
                for line in f:
                    line = line.strip()
                    if line.startswith("SARVAM_API_KEY="):
                        _SARVAM_KEY = line.split("=", 1)[1].strip().strip('"').strip("'")
                        break
        except:
            pass
    return _SARVAM_KEY

def _get_duration(audio_path):
    """Get audio duration in seconds using ffprobe.

    Returns (duration, known). known=False when ffprobe is unavailable or fails,
    so the caller can choose a dependency-light path instead of blindly assuming
    a long note — the old behavior returned 999 and forced EVERY note (even short
    ones) onto the sarvamai-dependent batch path (stt-pipeline-4)."""
    try:
        result = subprocess.run(
            ["ffprobe", "-v", "quiet", "-show_entries", "format=duration", "-of", "csv=p=0", audio_path],
            capture_output=True, text=True, timeout=5
        )
        return float(result.stdout.strip()), True
    except Exception:
        return 0.0, False

def _transcribe_rest(audio_path, audio_bytes, key):
    """REST API for audio ≤30s.
    Uses /speech-to-text-translate when prompt is available (supports prompt param),
    falls back to /speech-to-text with saaras:v3 otherwise.
    """
    boundary = "----SarvamBoundary" + str(int(time.time()))
    prompt = _get_prompt()

    # Use translate endpoint if we have vocabulary (it supports prompt param)
    if prompt:
        endpoint = "https://api.sarvam.ai/speech-to-text-translate"
        MODEL = "saaras:v3"
    else:
        endpoint = "https://api.sarvam.ai/speech-to-text"
        MODEL = "saaras:v3"

    body = b""
    body += f"--{boundary}\r\n".encode()
    body += f'Content-Disposition: form-data; name="file"; filename="{os.path.basename(audio_path)}"\r\n'.encode()
    body += b"Content-Type: audio/ogg\r\n\r\n"
    body += audio_bytes
    body += b"\r\n"
    body += f"--{boundary}\r\n".encode()
    body += b'Content-Disposition: form-data; name="model"\r\n\r\n'
    body += MODEL.encode()
    body += b"\r\n"

    if not prompt:
        # Only /speech-to-text needs language_code and mode
        body += f"--{boundary}\r\n".encode()
        body += b'Content-Disposition: form-data; name="language_code"\r\n\r\n'
        body += b"unknown"
        body += b"\r\n"
        body += f"--{boundary}\r\n".encode()
        body += b'Content-Disposition: form-data; name="mode"\r\n\r\n'
        body += b"translate"
        body += b"\r\n"

    if prompt:
        body += f"--{boundary}\r\n".encode()
        body += b'Content-Disposition: form-data; name="prompt"\r\n\r\n'
        body += prompt.encode()
        body += b"\r\n"

    body += f"--{boundary}--\r\n".encode()

    req = urllib.request.Request(
        endpoint,
        data=body,
        headers={
            "api-subscription-key": key,
            "Content-Type": f"multipart/form-data; boundary={boundary}"
        }
    )

    with urllib.request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read())
        return data.get("transcript", "")

def _transcribe_batch(audio_path, key):
    """Batch API for audio >30s."""
    try:
        from sarvamai import SarvamAI
    except ImportError as e:
        # Surface an ACTIONABLE message instead of a bare ModuleNotFoundError —
        # this is the 2026-06-22 failure (every >30s note failed silently). The
        # fix is the dedicated STT venv, which C3 auto-detects.
        raise RuntimeError(
            "sarvamai is not installed for this interpreter — long (>30s) voice "
            "notes need it. Create the C3 STT venv: `bash plugins/c3/stt/setup-venv.sh` "
            "(or `pip install sarvamai`), then point plugins.stt.python at "
            "~/.config/c3/stt-venv/bin/python (auto-detected if you use the venv)."
        ) from e
    client = SarvamAI(api_subscription_key=key)
    job = client.speech_to_text_job.create_job(model="saaras:v3", mode="translate", language_code="unknown")
    job.upload_files(file_paths=[audio_path])
    job.start()
    # Wait budget derived from the handler's remaining subprocess budget
    # (C3_STT_BUDGET_SECONDS, set by stt-handler.py = 270s minus elapsed download
    # time). We keep the wait ~15s UNDER that budget so the job returns/raises
    # here gracefully BEFORE the handler's subprocess.run hard-kills us. Capped at
    # 240s (long notes need it), floored at 30s. The Go-side 300s context is the
    # true backstop. Default 270 when the env isn't set (direct invocation).
    try:
        _budget = int(os.environ.get("C3_STT_BUDGET_SECONDS", "270"))
    except ValueError:
        _budget = 270
    _wait = max(30, min(240, _budget - 15))
    job.wait_until_complete(timeout=_wait)

    if job.is_successful():
        with tempfile.TemporaryDirectory() as td:
            job.download_outputs(output_dir=td)
            for f in os.listdir(td):
                with open(os.path.join(td, f)) as fh:
                    data = json.load(fh)
                    return data.get("transcript", "")
    return None

def transcribe(audio_path: str, audio_bytes: bytes) -> str:
    """Transcribe audio using Sarvam v3.
    Returns transcript string or None on failure.
    """
    key = _get_key()
    if not key:
        raise RuntimeError("SARVAM_API_KEY not available")

    duration, known = _get_duration(audio_path)
    if known:
        if duration <= 30:
            return _transcribe_rest(audio_path, audio_bytes, key)
        return _transcribe_batch(audio_path, key)
    # Unknown duration (ffprobe missing/failed): prefer the dependency-light REST
    # path (no sarvamai); fall back to the batch path if REST fails — either by
    # raising OR by returning an empty transcript (a 200-with-empty response,
    # e.g. the clip was actually too long for REST). This avoids forcing short
    # notes onto the sarvamai-dependent path while still handling long ones.
    try:
        t = _transcribe_rest(audio_path, audio_bytes, key)
        if t and t.strip():
            return t
        return _transcribe_batch(audio_path, key)
    except Exception:
        return _transcribe_batch(audio_path, key)
