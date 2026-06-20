#!/usr/bin/env python3
"""Voice STT handler for the Telegram channel.

Called by the Go-side STT plugin (internal/plugin/builtins/stt/stt.go) for
every incoming voice message:

    stdin (line 1):  <bot_token>\\n
    argv:            python3 stt-handler.py <chat_id> <reply_msg_id> <file_id> [<message_thread_id>]

The bot token is supplied on stdin — never via argv — so it doesn't appear
in `ps` / `/proc/<pid>/cmdline` / audit logs (addresses code-review
2026-05-15 MAJOR #1, cli.md §1.10). The Go shim writes `<token>\\n` to our
stdin before invoking us.

The optional <message_thread_id> is the forum topic the voice was sent in;
when present, the echo-back sendMessage calls pass it so every chunk of a
long transcript lands in the right topic instead of leaking to General.

On success: prints transcript to stdout and sends it back to Telegram.
On failure: prints nothing (Go shim falls back to raw voice attachment).

All configuration lives here — the Go shim is a thin spawn-and-wait wrapper.
"""
import sys
import os
import json
import time
import urllib.request
import urllib.parse
import importlib.util
import logging

# ── Config ────────────────────────────────────────────────────────────────────

_HERE      = os.path.dirname(os.path.realpath(__file__))
STT_PKG    = os.path.join(_HERE, 'stt-pkg', 'stt.py')
ENV_FILE   = os.environ.get('STT_ENV_FILE',  os.path.expanduser('~/.claude/stt.env'))
INBOX_DIR  = os.environ.get('STT_INBOX_DIR', os.path.expanduser('~/.claude/channels/telegram/inbox'))
LOG_FILE   = os.environ.get('STT_LOG_FILE',  os.path.expanduser('~/.claude/channels/telegram/stt-handler.log'))

# TODO #12 (2026-05-16): on a fresh install ~/.claude/channels/telegram/
# doesn't exist, and logging.basicConfig(filename=...) does NOT create
# parent dirs — import would FileNotFoundError before any provider code
# ran, and the broker only surfaced "[STT FAILED: error]" (byte-identical
# with or without an API key, undiagnosable). Belt-and-suspenders: mkdir
# both parents up front, then try basicConfig with a stderr fallback so
# a read-only / no-perms FS still gets logs into broker.log (the broker
# captures stderr).
_LOG_DIR = os.path.dirname(LOG_FILE)
if _LOG_DIR:
    os.makedirs(_LOG_DIR, exist_ok=True)
os.makedirs(INBOX_DIR, exist_ok=True)

_LOG_FORMAT  = '%(asctime)s %(levelname)s %(message)s'
_LOG_DATEFMT = '%Y-%m-%d %H:%M:%S'
try:
    logging.basicConfig(
        filename=LOG_FILE,
        level=logging.DEBUG,
        format=_LOG_FORMAT,
        datefmt=_LOG_DATEFMT,
    )
except Exception:
    # File handler couldn't be opened (read-only FS, perms, race, etc.).
    # Fall back to stderr — the broker pipes our stderr into broker.log,
    # so logs still land somewhere the operator can find.
    logging.basicConfig(
        stream=sys.stderr,
        level=logging.DEBUG,
        format=_LOG_FORMAT,
        datefmt=_LOG_DATEFMT,
    )

# ── Load API keys ─────────────────────────────────────────────────────────────

def load_env(path):
    env = {}
    try:
        with open(os.path.realpath(path)) as f:
            for line in f:
                line = line.strip()
                m = line.split('=', 1)
                if len(m) == 2 and m[0].isidentifier():
                    env[m[0]] = m[1]
    except Exception:
        pass
    return env

# ── Telegram API helpers ───────────────────────────────────────────────────────

# Bot-API base URL. Defaults to Telegram direct, but honors C3_TELEGRAM_API_URL
# (injected by the Go STT shim from mappings.json:channels.telegram.api_base_url
# or the env of the same name). This routes BOTH the getFile call and the voice
# file download through the same reverse proxy the broker uses — direct
# api.telegram.org is IP-blocked in some networks (e.g. India), which made the
# download time out (`<urlopen error timed out>`) even though the proxy was live.
API_BASE = os.environ.get('C3_TELEGRAM_API_URL', 'https://api.telegram.org').rstrip('/')

def tg(token, method, **params):
    url = f'{API_BASE}/bot{token}/{method}'
    data = json.dumps(params).encode()
    req = urllib.request.Request(url, data=data, headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=15) as r:
        return json.loads(r.read())

def download_file(token, file_id, dest_path):
    result = tg(token, 'getFile', file_id=file_id)
    file_path = result['result']['file_path']
    url = f'{API_BASE}/file/bot{token}/{file_path}'
    req = urllib.request.Request(url)
    with urllib.request.urlopen(req, timeout=30) as r:
        os.makedirs(os.path.dirname(dest_path), exist_ok=True)
        with open(dest_path, 'wb') as f:
            f.write(r.read())

# ── STT ───────────────────────────────────────────────────────────────────────

def run_stt(audio_path, extra_env):
    """Dynamically load stt.py and run it. Returns transcript or None."""
    spec = importlib.util.spec_from_file_location('stt', STT_PKG)
    # We call the providers directly rather than spawning a subprocess,
    # so we need to inject keys into the environment first.
    for k, v in extra_env.items():
        os.environ.setdefault(k, v)

    # Use stt.py's own chain logic via its main internals
    import subprocess
    env = {**os.environ, **extra_env}
    # 270s: long voice notes (esp. via the Sarvam batch API) routinely take
    # >120s; the old 120s cap silently failed them. Ordered under the broker's
    # Go-side 5m (300s) context with ~30s margin; Sarvam's own wait is set lower
    # (240s) so it returns gracefully before this kill fires.
    result = subprocess.run(
        [sys.executable, STT_PKG, audio_path],
        capture_output=True, text=True, env=env, timeout=270
    )
    transcript = result.stdout.strip()
    if result.returncode != 0 or not transcript:
        stderr_out = result.stderr.strip()
        logging.error(f'STT failed (rc={result.returncode}): {stderr_out}')
        print(stderr_out, file=sys.stderr)
        return None
    return transcript

# ── Telegram echo (testable) ──────────────────────────────────────────────────

def send_transcript_to_telegram(token, chat_id, msg_id, thread_id, transcript, tg_fn=None):
    """Echo a transcript back to Telegram, splitting into 4096-char chunks
    when needed. Returns the number of chunks sent.

    Critical invariant (regression-tested in test_stt_handler.py): every
    chunk carries `message_thread_id` when `thread_id` is set. Without it,
    Telegram routes chunks 2+ to the General topic — the regression Karthi
    hit on 2026-05-14 because an older handler version only carried the
    thread id implicitly via `reply_parameters` on the first chunk.

    `tg_fn` is the Telegram-API caller (defaults to the module-level `tg`);
    tests inject a fake to capture calls without doing network I/O.
    """
    if tg_fn is None:
        tg_fn = tg
    prefix = '\U0001f3a4 [Voice transcript]: '
    full_text = prefix + transcript
    MAX_LEN = 4096
    thread_kwargs = {'message_thread_id': thread_id} if thread_id else {}
    if len(full_text) <= MAX_LEN:
        tg_fn(token, 'sendMessage',
              chat_id=chat_id,
              text=full_text,
              reply_parameters={'message_id': msg_id},
              **thread_kwargs)
        return 1
    chunks = []
    remaining = full_text
    while remaining:
        chunks.append(remaining[:MAX_LEN])
        remaining = remaining[MAX_LEN:]
    for i, chunk in enumerate(chunks):
        params = {'chat_id': chat_id, 'text': chunk, **thread_kwargs}
        if i == 0:
            params['reply_parameters'] = {'message_id': msg_id}
        tg_fn(token, 'sendMessage', **params)
    return len(chunks)

# ── Main ──────────────────────────────────────────────────────────────────────

def main():
    if len(sys.argv) < 4:
        sys.exit(1)

    # Bot token is supplied on stdin (line 1) — never via argv — so it
    # doesn't appear in /proc/<pid>/cmdline, ps, or audit logs. The Go
    # shim writes "<token>\n" to our stdin before calling Run() (see
    # internal/plugin/builtins/stt/stt.go runHandler).
    token = sys.stdin.readline().rstrip('\n')
    if not token:
        logging.error('stt-handler: empty token on stdin (expected <token>\\n as line 1)')
        sys.exit(1)

    chat_id    = sys.argv[1]
    msg_id     = int(sys.argv[2])
    file_id    = sys.argv[3]
    thread_raw = sys.argv[4] if len(sys.argv) > 4 else ''
    thread_id  = int(thread_raw) if thread_raw else None

    # Download audio
    audio_path = os.path.join(INBOX_DIR, f'{int(time.time()*1000)}-{file_id}.oga')
    logging.info(f'Processing voice msg_id={msg_id} file_id={file_id}')
    for attempt in range(1, 4):
        try:
            download_file(token, file_id, audio_path)
            fsize = os.path.getsize(audio_path)
            logging.info(f'Downloaded audio to {audio_path} ({fsize} bytes) [attempt {attempt}]')
            if fsize > 0:
                break
            logging.warning(f'Downloaded file is 0 bytes [attempt {attempt}], retrying after 2s...')
            time.sleep(2)
        except Exception as e:
            logging.warning(f'Download failed [attempt {attempt}]: {e}')
            if attempt == 3:
                logging.error(f'Download failed after 3 attempts: {e}')
                print(f'[stt-handler] download failed: {e}', file=sys.stderr)
                sys.exit(1)
            time.sleep(2)
    else:
        logging.error('Download produced 0 bytes after 3 attempts')
        sys.exit(1)

    # Load API keys
    keys = load_env(ENV_FILE)

    # Transcribe
    transcript = run_stt(audio_path, keys)
    if not transcript:
        logging.error(f'STT returned no transcript for {audio_path}')
        sys.exit(1)
    logging.info(f'Transcript ({len(transcript)} chars): {transcript[:80]}...')

    # Echo transcript back to Telegram (chunked if it exceeds the 4096-char
    # per-message cap). Extracted into a helper so the chunking + topic
    # propagation is regression-tested in test_stt_handler.py.
    try:
        n = send_transcript_to_telegram(token, chat_id, msg_id, thread_id, transcript)
        if n > 1:
            logging.info(f'Echo split into {n} messages')
    except Exception as e:
        logging.warning(f'Telegram echo failed (non-fatal): {e}')
        print(f'[stt-handler] telegram reply failed: {e}', file=sys.stderr)
        # Non-fatal — the CLI side still gets the transcript via stdout

    # Print transcript to stdout for the Go-side STT shim to read
    print(transcript)

if __name__ == '__main__':
    main()
