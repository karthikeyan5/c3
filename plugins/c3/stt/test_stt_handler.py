"""Regression tests for stt-handler.py.

Run with: python3 -m unittest plugins.c3.stt.test_stt_handler -v
(from the repo root).

W4 (2026-06-30): the old 4096-char chunk loop was replaced by the
three-band renderer (PLAIN / INLINE / DOCUMENT) — one chat item per
transcript, never truncated. The obsolete chunk tests were deleted and
the suite rebuilt around the new send_transcript_to_telegram signature
`(token, chat_id, msg_id, thread_id, transcript, tg_fn=None, tg_doc_fn=None)`
which returns the band actually sent ('plain'|'inline'|'document'|'failed').
"""
import importlib.util
import os
import re
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
HANDLER_PATH = os.path.join(HERE, "stt-handler.py")

MARKER_SENT = re.compile(r"\[ … \d+ more sentences … \]")
MIC = "\U0001f3a4"        # 🎤
PAGE = "\U0001f4c4"       # 📄
ELLIPSIS = "…"       # …


def load_handler():
    spec = importlib.util.spec_from_file_location("stt_handler", HANDLER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def sentence_doc(target_u16, sent_len=40):
    """Build an ASCII (u16 == len) transcript of ~target_u16 chars made of
    uniquely-tagged sentences. Returns (text, tokens) where tokens[i] == "S{i}"
    and sentence i begins with that token and ends with a period."""
    parts, tokens = [], []
    i, total = 0, 0
    while total < target_u16:
        tag = f"S{i}"
        filler = "w" * max(1, sent_len - len(tag) - 2)
        s = f"{tag} {filler}."
        parts.append(s)
        tokens.append(tag)
        total += len(s) + 1
        i += 1
    return " ".join(parts), tokens


def runon_doc(target_u16):
    """A long run-on transcript with NO sentence punctuation (no . ! ?)."""
    s = ""
    i = 0
    while len(s) < target_u16:
        s += f"word{i} "
        i += 1
    return s[:target_u16].strip()


def exact_ascii(target_u16):
    """ASCII transcript of EXACTLY target_u16 chars, containing periods so it
    has multiple sentences."""
    s = ""
    while len(s) < target_u16:
        s += "Note about the status here. "
    return s[:target_u16]


class TestSendTranscriptToTelegram(unittest.TestCase):
    def setUp(self):
        self.handler = load_handler()
        self.calls = []       # (method, params) for sendMessage
        self.doc_calls = []   # (filename, content_bytes, fields) for sendDocument

    def fake_tg(self, _token, method, **params):
        self.calls.append((method, params))
        return {"ok": True}

    def fake_tg_doc(self, _token, chat_id, filename, content_bytes, **fields):
        self.doc_calls.append((filename, content_bytes, fields))
        return {"ok": True}

    def raising_tg(self, *_a, **_k):
        raise RuntimeError("boom-text")

    def raising_doc(self, *_a, **_k):
        raise RuntimeError("boom-doc")

    def send(self, transcript, msg_id=4711, thread_id=914):
        return self.handler.send_transcript_to_telegram(
            "tok", "-100", msg_id, thread_id, transcript,
            tg_fn=self.fake_tg, tg_doc_fn=self.fake_tg_doc,
        )

    def head_before_blockquote(self, text):
        return text.split("<blockquote", 1)[0]

    # 1
    def test_short_renders_plain(self):
        band = self.send("hi there, this is a short one.")
        self.assertEqual(band, "plain")
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(len(self.doc_calls), 0)
        method, params = self.calls[0]
        self.assertEqual(method, "sendMessage")
        self.assertEqual(params["parse_mode"], "HTML")
        self.assertTrue(params["text"].startswith(f"{MIC} <b>Voice transcript</b>"))
        self.assertNotIn("<blockquote", params["text"])
        self.assertEqual(params["message_thread_id"], 914)
        self.assertEqual(params["reply_parameters"], {"message_id": 4711})

    # 2
    def test_plain_uses_html_parse_mode_and_escapes(self):
        band = self.send("Tom & Jerry < Spike > nobody.")
        self.assertEqual(band, "plain")
        text = self.calls[0][1]["text"]
        self.assertEqual(self.calls[0][1]["parse_mode"], "HTML")
        self.assertIn("&amp;", text)
        self.assertIn("&lt;", text)
        self.assertIn("&gt;", text)

    # 3
    def test_dm_omits_thread_all_bands(self):
        # PLAIN
        self.calls, self.doc_calls = [], []
        self.send("short dm message.", thread_id=None)
        self.assertEqual(len(self.calls), 1)
        self.assertNotIn("message_thread_id", self.calls[0][1])
        # INLINE
        self.calls, self.doc_calls = [], []
        mid, _ = sentence_doc(2100)
        self.send(mid, thread_id=None)
        self.assertEqual(len(self.calls), 1)
        self.assertIn("<blockquote", self.calls[0][1]["text"])
        self.assertNotIn("message_thread_id", self.calls[0][1])
        # DOCUMENT
        self.calls, self.doc_calls = [], []
        long, _ = sentence_doc(6000)
        self.send(long, thread_id=None)
        self.assertEqual(len(self.doc_calls), 1)
        self.assertNotIn("message_thread_id", self.doc_calls[0][2])

    # 4
    def test_mid_renders_inline(self):
        mid, tokens = sentence_doc(2100)
        band = self.send(mid)
        self.assertEqual(band, "inline")
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(len(self.doc_calls), 0)
        text = self.calls[0][1]["text"]
        self.assertIn("<blockquote expandable>", text)
        # whole verbatim transcript, escaped, inside the blockquote
        self.assertIn(self.handler._esc(mid.strip()), text)
        self.assertRegex(text, MARKER_SENT.pattern)
        self.assertEqual(self.calls[0][1]["message_thread_id"], 914)
        self.assertEqual(self.calls[0][1]["reply_parameters"], {"message_id": 4711})

    # 5
    def test_inline_preview_shows_start_and_end(self):
        mid, tokens = sentence_doc(2100)
        self.send(mid)
        head = self.head_before_blockquote(self.calls[0][1]["text"])
        self.assertIn(tokens[0] + " ", head)       # first sentence token
        self.assertIn(tokens[-1] + " ", head)      # last sentence token

    # 6
    def test_inline_adaptive_N_shrinks(self):
        # Large body + long sentences -> preview budget too small for N=5,
        # so N must drop. Sentence S4 (5th) is elided; start+end stay.
        big, tokens = sentence_doc(3400, sent_len=85)
        band = self.send(big)
        self.assertEqual(band, "inline")
        self.assertEqual(len(self.doc_calls), 0)
        text = self.calls[0][1]["text"]
        self.assertRegex(text, MARKER_SENT.pattern)
        head = self.head_before_blockquote(text)
        self.assertIn(tokens[0] + " ", head)        # start kept
        self.assertIn(tokens[-1] + " ", head)       # end kept
        self.assertNotIn("S4 ", head)               # N shrank below 5

    # 7
    def test_long_renders_document(self):
        long, _ = sentence_doc(6000)
        band = self.send(long)
        self.assertEqual(band, "document")
        self.assertEqual(len(self.doc_calls), 1)
        self.assertEqual(len(self.calls), 0)
        fname, content, fields = self.doc_calls[0]
        self.assertEqual(fields["message_thread_id"], 914)
        self.assertEqual(fields["reply_parameters"], {"message_id": 4711})
        caption = fields["caption"]
        self.assertLessEqual(self.handler._u16(caption), 1024)
        self.assertRegex(caption, MARKER_SENT.pattern)
        self.assertIn(PAGE, caption)

    # 8
    def test_document_body_verbatim_exact(self):
        base, _ = sentence_doc(5000)
        transcript = base + " a < b > c & d </tag>"  # no surrounding whitespace
        band = self.send(transcript)
        self.assertEqual(band, "document")
        _fname, content, _fields = self.doc_calls[0]
        self.assertEqual(content.decode("utf-8"), transcript)

    # 9
    def test_blockquote_injection_safe(self):
        band = self.send("please render </blockquote> literally here.")
        self.assertEqual(band, "plain")  # short -> plain, no structural blockquote
        text = self.calls[0][1]["text"]
        self.assertIn("&lt;/blockquote&gt;", text)
        self.assertNotIn("</blockquote>", text)

    # 10
    def test_escaping_amp_first_no_double_escape(self):
        self.send("a & b < c")
        text = self.calls[0][1]["text"]
        self.assertIn("a &amp; b &lt; c", text)
        self.assertNotIn("&amp;lt;", text)

    # 11
    def test_utf16_measurement_emoji(self):
        ascii_part = exact_ascii(1797)
        transcript = ascii_part + MIC + MIC   # 2 codepoints, 4 UTF-16 units
        self.assertEqual(len(transcript), 1799)            # codepoints -> would be PLAIN
        self.assertEqual(self.handler._u16(transcript), 1801)  # UTF-16 -> INLINE
        band = self.send(transcript)
        self.assertEqual(band, "inline")
        self.assertIn("<blockquote", self.calls[0][1]["text"])

    # 12
    def test_band_boundary_T_PLAIN(self):
        at = exact_ascii(1800)
        self.assertEqual(self.handler._u16(at), 1800)
        self.assertEqual(self.send(at), "plain")
        self.calls, self.doc_calls = [], []
        over = exact_ascii(1801)
        self.assertEqual(self.handler._u16(over), 1801)
        self.assertEqual(self.send(over), "inline")

    # 13
    def test_inline_to_document_boundary(self):
        under, _ = sentence_doc(3700)
        self.assertEqual(self.send(under), "inline")
        self.calls, self.doc_calls = [], []
        over, _ = sentence_doc(4100)
        self.assertEqual(self.send(over), "document")

    # 14
    def test_runon_no_punctuation_fallback(self):
        ro = runon_doc(2500)
        self.assertNotIn(".", ro)
        band = self.send(ro)
        self.assertEqual(band, "inline")
        text = self.calls[0][1]["text"]
        self.assertIn(f"[ {ELLIPSIS} ]", text)          # bare run-on marker
        self.assertNotIn("more sentences", text)         # not the sentence marker
        self.assertIn("words", text)                     # header hint ~N words
        self.assertIn("<blockquote expandable>", text)
        self.assertIn(self.handler._esc(ro), text)       # full text verbatim

    # 15
    def test_on_send_failure_falls_back_to_document(self):
        band = self.handler.send_transcript_to_telegram(
            "tok", "-100", 4711, 914, "a short transcript that fails to send.",
            tg_fn=self.raising_tg, tg_doc_fn=self.fake_tg_doc,
        )
        self.assertEqual(band, "document")
        self.assertEqual(len(self.doc_calls), 1)
        _fname, content, _fields = self.doc_calls[0]
        self.assertEqual(content.decode("utf-8"),
                         "a short transcript that fails to send.")

    # 16
    def test_document_failure_is_nonfatal(self):
        band = self.handler.send_transcript_to_telegram(
            "tok", "-100", 4711, 914, "everything fails here.",
            tg_fn=self.raising_tg, tg_doc_fn=self.raising_doc,
        )
        self.assertEqual(band, "failed")  # no exception propagates

    # 17
    def test_caption_within_1024(self):
        long, _ = sentence_doc(8000, sent_len=300)
        band = self.send(long)
        self.assertEqual(band, "document")
        _fname, content, fields = self.doc_calls[0]
        self.assertLessEqual(self.handler._u16(fields["caption"]), 1024)
        self.assertEqual(content.decode("utf-8"), long.strip())


class TestImportCreatesParentDirs(unittest.TestCase):
    """TODO #12 (2026-05-16): on a fresh install
    ~/.claude/channels/telegram/ doesn't exist; logging.basicConfig(
    filename=...) does not create parent dirs, so import crashed with
    FileNotFoundError and the broker only surfaced [STT FAILED: error].

    These tests guard the fix: import must mkdir LOG_FILE's parent and
    INBOX_DIR up front, regardless of where STT_LOG_FILE/STT_INBOX_DIR
    point. We point them at fresh, deeply-nested temp dirs and confirm
    the handler imports cleanly and both dirs exist afterward.
    """

    def setUp(self):
        # sys.modules cache must be cleared so the next import re-runs
        # the module-level mkdir + basicConfig with our patched env.
        sys.modules.pop("stt_handler", None)
        self._saved_env = {
            k: os.environ.get(k)
            for k in ("STT_LOG_FILE", "STT_INBOX_DIR")
        }
        self.tmp = tempfile.mkdtemp(prefix="c3-stt-test-")
        # Use deeply-nested paths to make sure mkdir is recursive
        # (the fresh-install bug was specifically about the parent dir
        # not existing — not just the leaf).
        self.log_path = os.path.join(self.tmp, "deeply", "nested", "logs", "stt.log")
        self.inbox_path = os.path.join(self.tmp, "deeply", "nested", "inbox")
        os.environ["STT_LOG_FILE"] = self.log_path
        os.environ["STT_INBOX_DIR"] = self.inbox_path

    def tearDown(self):
        sys.modules.pop("stt_handler", None)
        for k, v in self._saved_env.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_import_creates_log_and_inbox_dirs(self):
        # Sanity: neither dir exists yet.
        self.assertFalse(os.path.isdir(os.path.dirname(self.log_path)))
        self.assertFalse(os.path.isdir(self.inbox_path))
        # Import must succeed (no FileNotFoundError) AND the dirs must
        # have been created during module load — both halves of the fix.
        load_handler()
        self.assertTrue(
            os.path.isdir(os.path.dirname(self.log_path)),
            "LOG_FILE parent dir should be mkdir'd at import time",
        )
        self.assertTrue(
            os.path.isdir(self.inbox_path),
            "INBOX_DIR should be mkdir'd at import time",
        )


if __name__ == "__main__":
    unittest.main()
