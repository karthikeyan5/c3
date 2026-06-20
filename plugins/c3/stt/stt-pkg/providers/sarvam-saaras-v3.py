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
    """Get audio duration in seconds using ffprobe."""
    try:
        result = subprocess.run(
            ["ffprobe", "-v", "quiet", "-show_entries", "format=duration", "-of", "csv=p=0", audio_path],
            capture_output=True, text=True, timeout=5
        )
        return float(result.stdout.strip())
    except:
        return 999  # Assume long, use batch

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
    from sarvamai import SarvamAI
    client = SarvamAI(api_subscription_key=key)
    job = client.speech_to_text_job.create_job(model="saaras:v3", mode="translate", language_code="unknown")
    job.upload_files(file_paths=[audio_path])
    job.start()
    # 240s: long notes need more than the old 120s; kept under stt-handler.py's
    # 270s subprocess cap so the job returns/raises here before the handler
    # hard-kills the process.
    job.wait_until_complete(timeout=240)

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

    duration = _get_duration(audio_path)
    if duration <= 30:
        return _transcribe_rest(audio_path, audio_bytes, key)
    else:
        return _transcribe_batch(audio_path, key)
