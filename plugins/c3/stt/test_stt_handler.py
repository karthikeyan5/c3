"""Regression tests for stt-handler.py.

Run with: python3 -m unittest plugins.c3.stt.test_stt_handler -v
(from the repo root).

2026-06-30: the Telegram transcript "readback" echo moved OUT of this handler
and INTO the Go broker/channel (internal/channel/telegram/readback.go +
internal/broker/worker.go). The handler now does ONLY download + whisper +
print-to-stdout, so the old echo tests (send_transcript_to_telegram band
selection, notify_transcription_failed) are gone; what remains exercises the
download / run_stt / cleanup / main-flow contract the Go shim depends on.
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
    traceback (which would bypass main()'s clean non-zero exit). TimeoutExpired
    and any other exception both become a clean None."""

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


class TestMainDownloadTerminalFailureExits(unittest.TestCase):
    """A non-retryable download failure (PermanentDownloadError) must exit
    non-zero WITHOUT burning the remaining retries. The human "couldn't
    transcribe" notice is now the Go broker's job (worker.go echoReadback): the
    handler just logs + exits, the Go shim sees empty stdout → [STT FAILED]
    marker, and the broker sends the notice."""

    def setUp(self):
        self.handler = load_handler()

    def test_permanent_download_failure_exits_nonzero_first_attempt(self):
        from unittest import mock
        import io

        download_calls = []

        def fake_download(*a, **k):
            download_calls.append((a, k))
            raise self.handler.PermanentDownloadError("file is too big")

        with mock.patch.object(self.handler, "download_file", fake_download), \
             mock.patch.object(sys, "argv",
                               ["stt-handler.py", "-100", "4711", "FID", "914"]), \
             mock.patch.object(sys, "stdin", io.StringIO("bottoken\n")):
            with self.assertRaises(SystemExit) as ctx:
                self.handler.main()

        self.assertEqual(ctx.exception.code, 1)
        # Exited on the FIRST attempt — the permanent failure didn't retry.
        self.assertEqual(len(download_calls), 1)


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
        """End-to-end-ish: a full main() download+transcribe+print flow leaves no
        .oga behind. download_file is faked to actually write a temp file at the
        path main() chose, so we can assert it's gone after main() returns. The
        Telegram echo is no longer in Python, so nothing is mocked for it."""
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
