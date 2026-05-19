"""Regression tests for stt-handler.py.

Run with: python3 -m unittest plugins/c3/stt/test_stt_handler.py
(from the repo root).
"""
import importlib.util
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
HANDLER_PATH = os.path.join(HERE, "stt-handler.py")


def load_handler():
    spec = importlib.util.spec_from_file_location("stt_handler", HANDLER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TestSendTranscriptToTelegram(unittest.TestCase):
    """Karthi 2026-05-14: long transcripts used to drop topic on chunks 2+ —
    the first chunk replied to the source message (which carries
    message_thread_id implicitly via reply_parameters), but subsequent
    chunks had no message_thread_id and landed in General. These tests
    guard the fix in send_transcript_to_telegram."""

    def setUp(self):
        self.handler = load_handler()
        self.calls = []

    def fake_tg(self, _token, method, **params):
        self.calls.append((method, params))
        return {"ok": True}

    def test_short_transcript_single_call_with_thread(self):
        n = self.handler.send_transcript_to_telegram(
            "tok", "-100", 42, 914, "hi there", tg_fn=self.fake_tg
        )
        self.assertEqual(n, 1)
        self.assertEqual(len(self.calls), 1)
        method, params = self.calls[0]
        self.assertEqual(method, "sendMessage")
        self.assertEqual(params["message_thread_id"], 914)
        self.assertEqual(params["reply_parameters"], {"message_id": 42})
        self.assertIn("[Voice transcript]:", params["text"])

    def test_long_transcript_every_chunk_carries_thread(self):
        # ~10000 chars → at least 3 chunks of 4096.
        n = self.handler.send_transcript_to_telegram(
            "tok", "-100", 42, 914, "x" * 10000, tg_fn=self.fake_tg
        )
        self.assertGreaterEqual(n, 3, "expected >=3 chunks for 10000-char transcript")
        self.assertEqual(len(self.calls), n)
        for i, (_method, params) in enumerate(self.calls):
            self.assertEqual(
                params.get("message_thread_id"),
                914,
                f"chunk {i} missing message_thread_id: {params!r}",
            )

    def test_long_transcript_only_first_chunk_has_reply_parameters(self):
        # Subsequent chunks must NOT carry reply_parameters — that would
        # make every chunk a "reply to the voice message", spamming
        # notifications and confusing the reply chain.
        self.handler.send_transcript_to_telegram(
            "tok", "-100", 42, 914, "y" * 9000, tg_fn=self.fake_tg
        )
        first_params = self.calls[0][1]
        self.assertEqual(first_params.get("reply_parameters"), {"message_id": 42})
        for i, (_m, params) in enumerate(self.calls[1:], start=1):
            self.assertNotIn(
                "reply_parameters",
                params,
                f"chunk {i} should not have reply_parameters: {params!r}",
            )

    def test_no_thread_id_omits_kwarg(self):
        # DM case — no topic. message_thread_id must NOT appear in params
        # (Telegram rejects null thread ids; absence is the correct shape).
        self.handler.send_transcript_to_telegram(
            "tok", "99", 42, None, "hi", tg_fn=self.fake_tg
        )
        params = self.calls[0][1]
        self.assertNotIn(
            "message_thread_id",
            params,
            "message_thread_id should be omitted when thread_id is None",
        )

    def test_chunk_boundary_4096(self):
        # Exactly 4096 chars after prefix → single chunk (no off-by-one).
        prefix_len = len("\U0001f3a4 [Voice transcript]: ")
        transcript = "a" * (4096 - prefix_len)
        n = self.handler.send_transcript_to_telegram(
            "tok", "-100", 42, 914, transcript, tg_fn=self.fake_tg
        )
        self.assertEqual(n, 1, "transcript exactly at the limit should NOT split")

    def test_chunk_boundary_4097(self):
        # One char past the limit → exactly two chunks.
        prefix_len = len("\U0001f3a4 [Voice transcript]: ")
        transcript = "a" * (4097 - prefix_len)
        n = self.handler.send_transcript_to_telegram(
            "tok", "-100", 42, 914, transcript, tg_fn=self.fake_tg
        )
        self.assertEqual(n, 2)


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
