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


class TestDownloadFileOkFalse(unittest.TestCase):
    """I-9: a getFile {ok:false} envelope (expired/invalid file_id, or the Bot
    API 20MB getFile limit -> 'file is too big') must NOT KeyError. It must be
    raised as a PermanentDownloadError so the download loop treats it as
    terminal instead of burning the 3 retries on a guaranteed failure."""

    def setUp(self):
        self.handler = load_handler()

    def test_ok_false_raises_permanent_not_keyerror(self):
        calls = []

        def fake_tg(_token, method, **params):
            calls.append((method, params))
            return {"ok": False, "description": "file is too big", "error_code": 400}

        with self.assertRaises(self.handler.PermanentDownloadError) as ctx:
            self.handler.download_file(
                "tok", "BADFID",
                "/nonexistent/should-not-be-written.oga",
                tg_fn=fake_tg,
            )
        # surfaces the real reason ...
        self.assertIn("file is too big", str(ctx.exception))
        self.assertIn("400", str(ctx.exception))
        # ... and stops at getFile: no download attempt, no extra getFile calls.
        # (Non-retry on the permanent failure is enforced by main()'s loop, which
        # has a dedicated `except PermanentDownloadError` terminal branch.)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0], "getFile")

    def test_ok_false_is_not_keyerror(self):
        def fake_tg(_token, method, **params):
            return {"ok": False, "description": "invalid file_id"}

        try:
            self.handler.download_file("tok", "X", "/x.oga", tg_fn=fake_tg)
        except self.handler.PermanentDownloadError:
            pass
        except KeyError:
            self.fail("download_file raised the pre-fix cryptic KeyError('result')")


class TestRunSttFailureReturnsNone(unittest.TestCase):
    """I-2: run_stt must never let a subprocess failure escape as a bare
    traceback (which would bypass main()'s human 'could not transcribe' notice).
    TimeoutExpired and any other exception both become a clean None."""

    def setUp(self):
        self.handler = load_handler()

    def test_timeout_expired_returns_none(self):
        import subprocess
        from unittest import mock

        def boom(*_a, **_k):
            # text=True can still yield bytes/None stderr depending on version;
            # bytes here also exercises _stderr_snippet's guard.
            raise subprocess.TimeoutExpired(
                cmd="stt", timeout=1, stderr=b"provider stalled mid-poll")

        with mock.patch("subprocess.run", boom):
            result = self.handler.run_stt("/tmp/nope.oga", {}, timeout=1)
        self.assertIsNone(result)

    def test_generic_exception_returns_none(self):
        from unittest import mock
        with mock.patch("subprocess.run", side_effect=OSError("exec failed")):
            self.assertIsNone(self.handler.run_stt("/tmp/nope.oga", {}, timeout=5))


class TestNotifyTranscriptionFailed(unittest.TestCase):
    """I-3: the extracted sender-notice helper. Same wording/mechanism the
    no-transcript branch used, now reusable from every terminal-failure path."""

    def setUp(self):
        self.handler = load_handler()

    def test_sends_failure_notice(self):
        calls = []

        def fake_tg(_token, method, **params):
            calls.append((method, params))
            return {"ok": True}

        self.handler.notify_transcription_failed("tok", "-100", 4711, 914, tg_fn=fake_tg)
        self.assertEqual(len(calls), 1)
        method, params = calls[0]
        self.assertEqual(method, "sendMessage")
        self.assertIn("Could not transcribe", params["text"])
        self.assertEqual(params["reply_parameters"], {"message_id": 4711})
        self.assertEqual(params["message_thread_id"], 914)

    def test_dm_omits_thread(self):
        calls = []

        def fake_tg(_token, method, **params):
            calls.append((method, params))
            return {"ok": True}

        self.handler.notify_transcription_failed("tok", "-100", 1, None, tg_fn=fake_tg)
        self.assertNotIn("message_thread_id", calls[0][1])

    def test_notify_failure_is_nonfatal(self):
        def boom(*_a, **_k):
            raise RuntimeError("network down")

        # must not raise — a notify failure can never mask the original exit
        self.handler.notify_transcription_failed("tok", "-100", 1, 2, tg_fn=boom)


class TestMainDownloadTerminalFailureNotifies(unittest.TestCase):
    """I-3 (branch): main()'s download-terminal-failure branch must call the
    sender notice before exiting. Drives the PermanentDownloadError path because
    it exits immediately (no retry sleeps, no network)."""

    def setUp(self):
        self.handler = load_handler()

    def test_permanent_download_failure_notifies_and_exits(self):
        from unittest import mock
        import io

        notify_calls = []

        def fake_download(*_a, **_k):
            raise self.handler.PermanentDownloadError("file is too big")

        def fake_notify(*a, **k):
            notify_calls.append((a, k))

        with mock.patch.object(self.handler, "download_file", fake_download), \
             mock.patch.object(self.handler, "notify_transcription_failed", fake_notify), \
             mock.patch.object(sys, "argv",
                               ["stt-handler.py", "-100", "4711", "FID", "914"]), \
             mock.patch.object(sys, "stdin", io.StringIO("bottoken\n")):
            with self.assertRaises(SystemExit) as ctx:
                self.handler.main()

        self.assertEqual(ctx.exception.code, 1)
        self.assertEqual(len(notify_calls), 1, "sender notice must fire on download-terminal failure")


class TestCleanupAudio(unittest.TestCase):
    """I-10: the downloaded .oga is deleted after use; missing file is non-fatal."""

    def setUp(self):
        self.handler = load_handler()

    def test_removes_existing_file(self):
        fd, path = tempfile.mkstemp(suffix=".oga")
        os.close(fd)
        self.assertTrue(os.path.exists(path))
        self.handler.cleanup_audio(path)
        self.assertFalse(os.path.exists(path))

    def test_missing_file_is_nonfatal(self):
        # must not raise on a path that doesn't exist
        self.handler.cleanup_audio("/nonexistent/definitely/not/here.oga")

    def test_main_success_flow_cleans_up_oga(self):
        """End-to-end-ish: a full main() download+transcribe+echo flow leaves no
        .oga behind. download_file is faked to actually write a temp file at the
        path main() chose, so we can assert it's gone after main() returns."""
        from unittest import mock
        import io

        created = {}

        def fake_download(_token, _file_id, dest_path, **_k):
            os.makedirs(os.path.dirname(dest_path), exist_ok=True)
            with open(dest_path, "wb") as f:
                f.write(b"OggS-fake-audio")
            created["path"] = dest_path

        with mock.patch.object(self.handler, "download_file", fake_download), \
             mock.patch.object(self.handler, "run_stt", lambda *a, **k: "hello world"), \
             mock.patch.object(self.handler, "send_transcript_to_telegram",
                               lambda *a, **k: "plain"), \
             mock.patch.object(sys, "argv",
                               ["stt-handler.py", "-100", "4711", "FID", "914"]), \
             mock.patch.object(sys, "stdin", io.StringIO("bottoken\n")):
            self.handler.main()

        self.assertIn("path", created)
        self.assertFalse(
            os.path.exists(created["path"]),
            "cached .oga should be removed after a successful transcribe",
        )


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
