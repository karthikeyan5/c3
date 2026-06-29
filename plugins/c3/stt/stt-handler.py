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
import re
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

def run_stt(audio_path, extra_env, timeout=270):
    """Dynamically load stt.py and run it. Returns transcript or None.

    timeout: subprocess budget in seconds. main() passes a value reduced by the
    time the download already consumed so download + transcribe stays under the
    broker's 300s SIGKILL (stt-pipeline-5)."""
    spec = importlib.util.spec_from_file_location('stt', STT_PKG)
    # We call the providers directly rather than spawning a subprocess,
    # so we need to inject keys into the environment first.
    for k, v in extra_env.items():
        os.environ.setdefault(k, v)

    # Use stt.py's own chain logic via its main internals
    import subprocess
    env = {**os.environ, **extra_env}
    # Tell the provider chain how much wall-clock it has so the Sarvam batch wait
    # can size itself to return gracefully BEFORE this subprocess.run timeout
    # SIGKILLs it (the provider caps wait at min(240, budget-15)). main() shrinks
    # `timeout` by the elapsed download time so a slow download can't push the
    # total past the broker's Go-side 300s context (the true backstop).
    env["C3_STT_BUDGET_SECONDS"] = str(timeout)
    result = subprocess.run(
        [sys.executable, STT_PKG, audio_path],
        capture_output=True, text=True, env=env, timeout=timeout
    )
    transcript = result.stdout.strip()
    if result.returncode != 0 or not transcript:
        stderr_out = result.stderr.strip()
        logging.error(f'STT failed (rc={result.returncode}): {stderr_out}')
        print(stderr_out, file=sys.stderr)
        return None
    return transcript

# ── Telegram echo: W4 three-band renderer (testable) ───────────────────────────
#
# A transcript renders as EXACTLY ONE chat item in one of three size bands:
#   PLAIN    — short: header + whole transcript (HTML-escaped).
#   INLINE   — medium: a first-N … last-N preview with a counted elision marker,
#              above a <blockquote expandable> holding the WHOLE verbatim text.
#   DOCUMENT — long: a .txt attachment (whole verbatim text) with the same
#              preview in the caption.
# The full transcript is NEVER summarized and NEVER truncated — only the *preview*
# elides the middle. Band is chosen by the MEASURED UTF-16-displayed length of the
# exact string about to be sent, so multi-message overflow is structurally
# impossible (no chunk loop). A safety net wraps the text send: any Telegram error
# falls back to the .txt document, then to a short notice, then to a non-fatal log.

HARD_MSG_CAP   = 4096   # sendMessage text cap, post-entities (capabilities.go:15)
HARD_CAPTION   = 1024   # sendDocument caption cap, post-entities (capabilities.go:34)
MARGIN         = 128    # slack below the hard wire cap
CEIL           = HARD_MSG_CAP - MARGIN     # 3968 -> max DISPLAYED units for one sendMessage
CAPTION_CEIL   = HARD_CAPTION - 32         # 992  -> caption ceiling with small slack
T_PLAIN        = 1800   # <= this renders PLAIN. THE single UX dial; tune on a phone.
N_PREVIEW_MAX  = 5      # max sentences shown each side
PREVIEW_MAX    = 700    # UTF-16 cap on the preview block (keeps DOCUMENT caption < 1024)
CHAR_WINDOW    = 400    # run-on fallback: chars each side (word-snapped)
MAX_SEND_BYTES = 50 * 1024 * 1024  # sendDocument file ceiling (capabilities.go:36); unreachable

_SENT = re.compile(r'(?<=[.!?।॥…])\s+')   # Latin + Devanagari danda + ellipsis

_INLINE_SIGNPOST = '<i>\U0001f4c4 Full transcript</i>'   # displayed: "📄 Full transcript"


def _u16(s):
    """UTF-16 code-unit length. Python len() counts CODEPOINTS and UNDER-counts
    astral chars (🎤 = 2 UTF-16 units, 1 codepoint); all budget math MUST use this.

    Telegram's 4096 cap is on the DISPLAYED text after entities parsing, so under
    parse_mode='HTML' the tags (<b>, <blockquote expandable>, …) and escapes
    (&amp; -> displayed '&') cost 0 — the wire cost of escaped content equals its
    unescaped displayed length. We therefore measure the visible string and send
    the same structure with tags + _esc()-ed content.
    # TODO(W4 Phase 0): live-verify displayed-length budget; until then
    # MARGIN+fallback-net cover it.
    """
    return len(s.encode('utf-16-le')) // 2


def _esc(s):
    """Escape '< > &' for Telegram HTML — '&' FIRST so we don't double-escape
    (matches the displayed output of Go's escapeRune/escapeText, format.go:531)."""
    return s.replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;')


def _H(hint):
    """Bold header. `hint` is '~N sentences' / '~N words' / None (no hint, PLAIN)."""
    h = '\U0001f3a4 <b>Voice transcript</b>'
    if hint:
        h += f' · {hint}'
    return h


def _split_sentences(t):
    """Pragmatic, multilingual-ish sentence split. Preview-only — imperfect is
    fine (the openable/attached full text is always verbatim)."""
    t = (t or '').strip()
    if not t:
        return []
    return [p for p in _SENT.split(t) if p.strip()]


def _cap_u16(s, limit):
    """Trim s (on codepoints) until its UTF-16 length <= limit; append '…' if cut.
    Final safety belt — callers budget so this is normally a no-op."""
    if _u16(s) <= limit:
        return s
    out = s[:limit]                       # codepoints <= u16, so this is a safe upper bound
    while out and _u16(out) > limit - 1:
        out = out[:-1]
    return out + '…'


def _snap_head(t, limit_u16):
    """Longest prefix of t with _u16 <= limit_u16, snapped back to a word boundary."""
    out = t[:limit_u16]
    while out and _u16(out) > limit_u16:
        out = out[:-1]
    if len(out) < len(t):
        idx = out.rfind(' ')
        if idx > 0:
            out = out[:idx]
    return out


def _snap_tail(t, limit_u16):
    """Longest suffix of t with _u16 <= limit_u16, snapped forward to a word boundary."""
    start = max(0, len(t) - limit_u16)
    out = t[start:]
    while out and _u16(out) > limit_u16:
        out = out[1:]
    if start > 0:
        idx = out.find(' ')
        if 0 <= idx < len(out) - 1:
            out = out[idx + 1:]
    return out


def _char_window_preview(t, budget):
    """Run-on fallback: first ~K … last ~K chars (word-snapped), bare '[ … ]'
    marker, header hint '~N words' (sentence count unreliable). Returns
    (escaped_preview, hint, visible_u16)."""
    t = t.strip()
    words = len(t.split())
    hint = f'~{words} words'
    marker = '[ … ]'
    overhead = _u16(marker) + 4                      # two blank-line separators
    per_side = max(1, min(CHAR_WINDOW, (budget - overhead) // 2))
    head = _snap_head(t, per_side)
    tail = _snap_tail(t, per_side)
    visible = f'{head}\n\n{marker}\n\n{tail}'
    preview = f'{_esc(head)}\n\n{marker}\n\n{_esc(tail)}'
    return preview, hint, _u16(visible)


def _build_preview(sents, transcript, budget):
    """Adaptive-N sentence elision (N=5→1) with a counted marker, falling to the
    char-window when too few sentences to elide or no N fits `budget`. Returns
    (escaped_preview, hint, visible_u16). Measurement is on the UNESCAPED visible
    text (escaping is budget-neutral)."""
    budget = min(budget, PREVIEW_MAX)
    n = len(sents)
    for N in range(N_PREVIEW_MAX, 0, -1):
        if n >= 2 * N + 1:
            middle = n - 2 * N
            head = ' '.join(sents[:N])
            tail = ' '.join(sents[-N:])
            marker = f'[ … {middle} more sentences … ]'
            visible = f'{head}\n\n{marker}\n\n{tail}'
            if _u16(visible) <= budget:
                preview = f'{_esc(head)}\n\n{marker}\n\n{_esc(tail)}'
                return preview, f'~{n} sentences', _u16(visible)
    return _char_window_preview((transcript or '').strip(), budget)


def _render_inline(t):
    """Build the largest-N inline message whose MEASURED displayed length <= CEIL
    (preview + the whole verbatim transcript in <blockquote expandable>). Returns
    (html_text, 'inline') or None (-> caller falls to DOCUMENT)."""
    sents = _split_sentences(t)
    words = len(t.split())
    body_visible = _u16(t)
    # Conservative header/signpost budget (raw u16 incl. tag chars over-counts
    # displayed length — safe, absorbed by MARGIN).
    hint_guess = f'~{max(len(sents), words, 1)} sentences'
    hdr_u16 = _u16(_H(hint_guess))
    signpost_u16 = _u16(_INLINE_SIGNPOST)
    avail = CEIL - hdr_u16 - signpost_u16 - 4 - body_visible   # 4 = structural newlines
    if avail < 24:                       # no room for even a minimal preview
        return None
    preview, hint, pv = _build_preview(sents, t, min(avail, PREVIEW_MAX))
    header = _H(hint)
    measured = _u16(header) + 1 + pv + 2 + signpost_u16 + 1 + body_visible
    if measured > CEIL:
        return None
    body = _esc(t)
    text = (f'{header}\n{preview}\n\n{_INLINE_SIGNPOST}\n'
            f'<blockquote expandable>{body}</blockquote>')
    return text, 'inline'


def tg_document(token, chat_id, filename, content_bytes, **fields):
    """stdlib multipart sendDocument. Object params (reply_parameters) MUST be
    JSON-encoded strings in multipart; routes through API_BASE so the upload also
    uses the India-IP reverse proxy."""
    url = f'{API_BASE}/bot{token}/sendDocument'
    boundary = '----c3' + os.urandom(8).hex()
    buf = bytearray()

    def add(name, value):
        if isinstance(value, (dict, list)):
            value = json.dumps(value)            # object params MUST be JSON strings in multipart
        buf.extend((f'--{boundary}\r\nContent-Disposition: form-data; '
                    f'name="{name}"\r\n\r\n{value}\r\n').encode())

    add('chat_id', str(chat_id))
    for k, v in fields.items():
        if v is None:
            continue
        add(k, v)
    buf.extend((f'--{boundary}\r\nContent-Disposition: form-data; '
                f'name="document"; filename="{filename}"\r\n'
                f'Content-Type: text/plain; charset=utf-8\r\n\r\n').encode())
    buf.extend(content_bytes)
    buf.extend(b'\r\n')
    buf.extend(f'--{boundary}--\r\n'.encode())
    req = urllib.request.Request(
        url, data=bytes(buf),
        headers={'Content-Type': f'multipart/form-data; boundary={boundary}'})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read())


def send_transcript_to_telegram(token, chat_id, msg_id, thread_id, transcript,
                                tg_fn=None, tg_doc_fn=None):
    """Echo a transcript back to Telegram as ONE chat item. Returns the band
    actually sent: 'plain' | 'inline' | 'document' | 'failed'.

    Invariants (regression-tested in test_stt_handler.py):
    - `message_thread_id` is carried ONLY when `thread_id` is truthy; DM (None)
      omits it (Telegram rejects a null thread id). Since there is no chunk loop,
      the 2026-05-14 "chunks 2+ leak to General" regression is gone by construction.
    - `reply_parameters={'message_id': msg_id}` rides the SINGLE send of every band
      (the source voice note), which also drives the reply-quote UX.
    - Safety net: any Telegram error on the text send re-sends the WHOLE verbatim
      transcript as a .txt document; if that fails, a short plain notice; if that
      fails, a non-fatal log (stdout still carries the transcript to the agent).

    `tg_fn`/`tg_doc_fn` are injectable (default to the module-level `tg`/`tg_document`)
    so tests capture calls without network I/O.
    """
    tg_fn = tg_fn or tg
    tg_doc_fn = tg_doc_fn or tg_document
    t = (transcript or '').strip()
    thread = {'message_thread_id': thread_id} if thread_id else {}
    reply = {'message_id': msg_id}
    B = _u16(t)

    def _send_document():
        sents = _split_sentences(t)
        words = len(t.split())
        signpost = f'<i>\U0001f4c4 Full transcript attached ({words} words)</i>'
        hint_guess = f'~{max(len(sents), words, 1)} sentences'
        budget = CAPTION_CEIL - _u16(_H(hint_guess)) - _u16(signpost) - 4
        preview, hint, _pv = _build_preview(sents, t, max(0, budget))
        caption = _cap_u16(f'{_H(hint)}\n{preview}\n\n{signpost}', CAPTION_CEIL)
        fname = time.strftime(f'voice-transcript-{msg_id}-%Y%m%d-%H%M.txt')
        tg_doc_fn(token, chat_id, fname, t.encode('utf-8'),
                  caption=caption, parse_mode='HTML',
                  reply_parameters=reply, **thread)
        return 'document'

    # 1) choose the text band (measured on the exact string we will send)
    if B <= T_PLAIN:
        text, band = f'{_H(None)}\n{_esc(t)}', 'plain'
    else:
        rendered = _render_inline(t)
        if rendered is None:
            return _send_document()
        text, band = rendered

    # 2) send the text band, with the safety net
    try:
        tg_fn(token, 'sendMessage', chat_id=chat_id, text=text,
              parse_mode='HTML', reply_parameters=reply, **thread)
        return band
    except Exception as e:
        logging.warning(f'inline/plain echo failed ({e}); falling back to .txt document')
        try:
            return _send_document()
        except Exception as e2:
            logging.warning(f'document fallback failed ({e2}); sending short notice')
            try:
                tg_fn(token, 'sendMessage', chat_id=chat_id,
                      text='\U0001f3a4 <b>Voice transcript</b> (too long to display; '
                           'delivery failed — see logs)',
                      parse_mode='HTML', reply_parameters=reply, **thread)
            except Exception:
                pass
            return 'failed'

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
    dl_start = time.time()
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

    # Budget the transcription so download + transcribe stays under the broker's
    # 300s SIGKILL: 270s total minus the time the download already consumed,
    # floored at 60s so a slow download doesn't leave an unusably tiny window.
    dl_elapsed = time.time() - dl_start
    stt_timeout = max(60, 270 - int(dl_elapsed))
    logging.info(f'Download took {dl_elapsed:.1f}s; STT subprocess budget={stt_timeout}s')

    # Transcribe
    transcript = run_stt(audio_path, keys, timeout=stt_timeout)
    if not transcript:
        logging.error(f'STT returned no transcript for {audio_path}')
        # Best-effort user-facing notice so the human knows immediately (the Go
        # shim separately surfaces an [STT FAILED] marker to the agent). Without
        # this the sender just sees their voice note silently ignored.
        try:
            tg(token, 'sendMessage',
               chat_id=chat_id,
               text='\U0001f3a4 ⚠️ Could not transcribe that voice note (speech-to-text failed). Please retype or resend.',
               reply_parameters={'message_id': msg_id},
               **({'message_thread_id': thread_id} if thread_id else {}))
        except Exception as e:
            logging.warning(f'failed to send STT-failure notice to Telegram: {e}')
        sys.exit(1)
    logging.info(f'Transcript ({len(transcript)} chars): {transcript[:80]}...')

    # Echo transcript back to Telegram as ONE chat item (PLAIN / INLINE /
    # DOCUMENT band by measured displayed length — never truncated). The helper
    # has its own safety net (text-send error -> .txt document); this outer
    # try/except is the final non-fatal backstop. Band selection + invariants
    # are regression-tested in test_stt_handler.py.
    try:
        band = send_transcript_to_telegram(token, chat_id, msg_id, thread_id, transcript)
        logging.info(f'Echo sent as {band}')
    except Exception as e:
        logging.warning(f'Telegram echo failed (non-fatal): {e}')
        print(f'[stt-handler] telegram reply failed: {e}', file=sys.stderr)
        # Non-fatal — the CLI side still gets the transcript via stdout

    # Print transcript to stdout for the Go-side STT shim to read
    print(transcript)

if __name__ == '__main__':
    main()
