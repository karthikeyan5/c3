"""ElevenLabs Scribe v2 STT provider.

Requires: ELEVENLABS_API_KEY env var (or in ~/.claude/stt.env)
API: POST https://api.elevenlabs.io/v1/speech-to-text
Model: scribe_v2
"""
import os, json, urllib.request, urllib.error

_EL_KEY = None

def _get_key():
    global _EL_KEY
    if _EL_KEY:
        return _EL_KEY
    _EL_KEY = os.environ.get("ELEVENLABS_API_KEY", "")
    if not _EL_KEY:
        env_path = os.path.expanduser("~/.claude/stt.env")
        try:
            with open(env_path) as f:
                for line in f:
                    line = line.strip()
                    if line.startswith("ELEVENLABS_API_KEY="):
                        _EL_KEY = line.split("=", 1)[1].strip().strip('"').strip("'")
                        break
        except:
            pass
    return _EL_KEY


def transcribe(audio_path: str, audio_bytes: bytes) -> str | None:
    """Transcribe audio using ElevenLabs Scribe v2.
    Returns transcript string or None on failure.
    """
    key = _get_key()
    if not key:
        raise RuntimeError("ELEVENLABS_API_KEY not available")

    # Build multipart/form-data manually
    boundary = "----ElevenLabsBoundary1234567890"
    filename = os.path.basename(audio_path) if audio_path else "audio.ogg"

    # Determine content type
    if filename.endswith(".ogg"):
        mime = "audio/ogg"
    elif filename.endswith(".mp3"):
        mime = "audio/mpeg"
    elif filename.endswith(".wav"):
        mime = "audio/wav"
    else:
        mime = "application/octet-stream"

    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="model_id"\r\n\r\n'
        f"scribe_v2\r\n"
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
        f"Content-Type: {mime}\r\n\r\n"
    ).encode() + audio_bytes + f"\r\n--{boundary}--\r\n".encode()

    req = urllib.request.Request(
        "https://api.elevenlabs.io/v1/speech-to-text",
        data=body,
        headers={
            "xi-api-key": key,
            "Content-Type": f"multipart/form-data; boundary={boundary}",
        }
    )

    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read())
            text = result.get("text", "") or ""
            return text.strip() if text.strip() else None
    except urllib.error.HTTPError as e:
        err_body = e.read().decode(errors="replace")
        raise RuntimeError(f"HTTP {e.code}: {err_body}")
