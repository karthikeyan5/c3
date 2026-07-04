# STT Package — Pluggable Speech-to-Text Chain

Modular STT with provider chaining, retry, and automatic fallback. Used
by c3 as its bundled speech-to-text engine; also runnable standalone.

## Quick Start

```bash
# Default chain: Gemini 3 Flash → Sarvam v3 fallback
python3 stt.py audio.ogg

# Sarvam only
python3 stt.py audio.ogg --chain sarvam-saaras-v3

# ElevenLabs Scribe v2 first, gemini then sarvam as fallbacks
python3 stt.py audio.ogg --chain elevenlabs-scribe-v2,gemini-3-flash-openrouter,sarvam-saaras-v3

# Custom chain with extra retries
python3 stt.py audio.ogg --chain gemini-3-flash-openrouter,sarvam-saaras-v3 --retries 2 --retry-delay 3
```

## Structure

```
stt-pkg/
├── stt.py                                  # Main entry point
├── vocabulary.txt                          # Optional domain-vocabulary biasing
├── providers/
│   ├── gemini-3-flash-openrouter.py        # Gemini 3 Flash via OpenRouter (default)
│   ├── sarvam-saaras-v3.py                 # Sarvam AI Saaras v3 (default fallback)
│   ├── elevenlabs-scribe-v2.py             # ElevenLabs Scribe v2 (opt-in via --chain)
│   └── __init__.py
└── README.md
```

All three providers ship bundled. The default chain
(`gemini-3-flash-openrouter,sarvam-saaras-v3`) keeps the API-key surface
minimal for the typical install; `elevenlabs-scribe-v2` is wired and
ready — just add it to your `--chain` and set `ELEVENLABS_API_KEY`.

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

`transcribe()` is the only required function. A provider may **optionally**
add `set_vocabulary()` to receive the shared domain vocabulary — `stt.py`
calls it (when present) right before each `transcribe()`, so you can bias the
model toward preferred spellings:

```python
_VOCAB = {"terms": [], "context": ""}

def set_vocabulary(vocab):
    """Optional. Receives the domain vocabulary loaded by stt.py.

    vocab is a dict:
        terms:   list of {"preferred": str, "not": [str], "note": str}
        context: str — a short description of the domain
    Adapt it into whatever your API accepts (system prompt, hotwords, a
    `prompt` parameter, …). See the bundled gemini/sarvam providers for
    two different adaptations.
    """
    global _VOCAB
    _VOCAB = vocab or {"terms": [], "context": ""}
```

Then use it:
```bash
python3 stt.py audio.ogg --chain gemini-3-flash-openrouter,your-model-name,sarvam-saaras-v3
```

## How c3 Wires This Up

c3's broker subprocesses `plugins/c3/stt/stt-handler.py` for each voice
attachment. That handler in turn subprocesses `stt.py` from this
directory. The chain is whatever the handler passes via `--chain`
(defaults to gemini → sarvam). To change the chain c3 uses globally,
set `plugins.stt.handler_path` in `~/.config/c3/mappings.json` to a
custom handler that calls `stt.py` with your preferred `--chain`.

To use this package outside of c3 (e.g. from another voice-input tool),
just invoke `python3 stt.py <audio>` directly — stdout is the transcript,
stderr is the trace.

## Requirements

- Python 3.8+
- **Gemini provider:** OPENROUTER_API_KEY (env or `~/.claude/stt.env`)
- **Sarvam provider:** SARVAM_API_KEY (env or `~/.claude/stt.env`)
- **ElevenLabs provider:** ELEVENLABS_API_KEY (env or `~/.claude/stt.env`)
- **Sarvam >30s audio:** `ffprobe` (batch path is native urllib — no extra PyPI deps)

## Why This Exists

Gemini Flash occasionally returns HTTP 200 with `finish_reason: "stop"` but zero completion tokens and null content. Silent failure — no error code. Happens ~1 in 5 calls on some audio samples. The chain + retry pattern catches this reliably.
