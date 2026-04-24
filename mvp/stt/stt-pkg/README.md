# STT Package — Pluggable Speech-to-Text Chain

Modular STT with provider chaining, retry, and automatic fallback.

**Replaces:** `gemini-stt.py` and `sarvam-stt.py` standalone scripts.

## Quick Start

```bash
# Default chain: Gemini 3 Flash → Sarvam v3 fallback
python3 stt.py audio.ogg

# Sarvam only
python3 stt.py audio.ogg --chain sarvam-saaras-v3

# Custom chain with extra retries
python3 stt.py audio.ogg --chain gemini-3-flash-openrouter,sarvam-saaras-v3 --retries 2 --retry-delay 3
```

## Structure

```
stt-pkg/
├── stt.py                                  # Main entry point
├── providers/
│   ├── gemini-3-flash-openrouter.py        # Gemini 3 Flash via OpenRouter
│   ├── sarvam-saaras-v3.py                 # Sarvam AI Saaras v3
│   └── __init__.py
└── README.md
```

## How It Works

1. `stt.py` loads providers from `providers/` directory by name
2. Runs them in chain order (left to right)
3. Each provider gets N attempts (1 + retries)
4. First non-empty result wins → printed to stdout
5. If a provider is exhausted, falls back to the next one
6. All retry/fallback activity logged to stderr

## Provider Naming Convention

Name provider files descriptively:

```
<model-name>-<api-source>.py
```

Examples:
- `gemini-3-flash-openrouter.py` — Gemini 3 Flash routed through OpenRouter
- `sarvam-saaras-v3.py` — Sarvam AI's Saaras model, version 3
- `whisper-large-v3-groq.py` — OpenAI Whisper Large v3 via Groq
- `gpt4o-transcribe-openai.py` — GPT-4o transcribe via OpenAI directly
- `deepgram-nova-2.py` — Deepgram Nova 2

The filename (minus `.py`) is the provider name used in `--chain`. This makes chains self-documenting:

```bash
python3 stt.py audio.ogg --chain gemini-3-flash-openrouter,whisper-large-v3-groq,sarvam-saaras-v3
```

## Adding a New Provider

Create `providers/<model-name>-<api-source>.py`:

```python
"""Short description of the provider."""

def transcribe(audio_path: str, audio_bytes: bytes) -> str:
    """Transcribe audio.
    
    Args:
        audio_path: Absolute path to the audio file (for tools that need file paths)
        audio_bytes: Raw file bytes (for APIs that accept binary uploads)
    
    Returns:
        Transcript string, or None/empty string on failure.
        Raise exceptions for hard errors (stt.py will log and retry).
    """
    # Your implementation here
    return "transcript text"
```

Then use it:
```bash
python3 stt.py audio.ogg --chain gemini-3-flash-openrouter,your-model-name,sarvam-saaras-v3
```

## OpenClaw Integration

Replace the old `gemini-stt.py` reference in your audio config:

```json
{
  "tools": {
    "media": {
      "audio": {
        "models": [{
          "type": "cli",
          "command": "python3",
          "args": ["/path/to/stt-pkg/stt.py", "{{MediaPath}}"],
          "timeoutSeconds": 120
        }]
      }
    }
  }
}
```

**Old (replace this):**
```json
"args": ["/path/to/gemini-stt.py", "{{MediaPath}}"]
```

**New:**
```json
"args": ["/path/to/stt-pkg/stt.py", "{{MediaPath}}"]
```

## Requirements

- Python 3.8+
- **Gemini provider:** OPENROUTER_API_KEY (env or openclaw.json)
- **Sarvam provider:** SARVAM_API_KEY (env, openclaw.json, or .env file)
- **Sarvam >30s audio:** `pip install sarvamai` + `ffprobe`

## Why This Exists

Gemini Flash occasionally returns HTTP 200 with `finish_reason: "stop"` but zero completion tokens and null content. Silent failure — no error code. Happens ~1 in 5 calls on some audio samples. The chain + retry pattern catches this reliably.
