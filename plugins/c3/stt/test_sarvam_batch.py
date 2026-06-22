"""Unit tests for the native-urllib Sarvam batch (>30s) transcription flow.

These guard the 2026-06-22 reimplementation that dropped the `sarvamai`
PyPI dependency: the batch path is now plain stdlib urllib, so the request
URLs / methods / headers / JSON bodies must match Sarvam's bulk-job API
contract exactly (a wrong URL or field name silently breaks long-voice STT).

We monkeypatch urllib.request.urlopen to a fake that records every request
and replays canned Sarvam responses, then assert the full call sequence.

Run with: python3 -m unittest plugins/c3/stt/test_sarvam_batch.py
(from the repo root).
"""
import importlib.util
import json
import os
import tempfile
import unittest
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
PROVIDER_PATH = os.path.join(
    HERE, "stt-pkg", "providers", "sarvam-saaras-v3.py"
)


def load_provider():
    spec = importlib.util.spec_from_file_location("sarvam_saaras_v3", PROVIDER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class _FakeResp:
    """Minimal context-manager response standing in for http.client.HTTPResponse."""

    def __init__(self, body=b"", status=200):
        self._body = body
        self.status = status

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


class TestSarvamBatchRequests(unittest.TestCase):
    KEY = "test-sub-key"
    BASE = "https://api.sarvam.ai"

    def setUp(self):
        self.mod = load_provider()
        # A real temp file so the upload step can read bytes + guess a mime type.
        fd, self.audio_path = tempfile.mkstemp(suffix=".ogg")
        os.write(fd, b"OggS-fake-audio-bytes")
        os.close(fd)
        self.file_name = os.path.basename(self.audio_path)
        self.requests = []  # (method, url, headers, body_bytes)
        self._orig_urlopen = urllib.request.urlopen
        urllib.request.urlopen = self._fake_urlopen
        # Keep the completion wait short + deterministic (single poll, no sleep).
        os.environ["C3_STT_BUDGET_SECONDS"] = "270"

    def tearDown(self):
        urllib.request.urlopen = self._orig_urlopen
        os.unlink(self.audio_path)
        os.environ.pop("C3_STT_BUDGET_SECONDS", None)

    def _fake_urlopen(self, req, timeout=None):
        url = req.full_url
        method = req.get_method()
        body = req.data
        # http.client lowercases header keys via add_header; normalize for asserts.
        headers = {k.lower(): v for k, v in req.header_items()}
        self.requests.append((method, url, headers, body))

        # --- Sarvam api.sarvam.ai bulk-job endpoints ---
        if url == f"{self.BASE}/speech-to-text/job/v1" and method == "POST":
            return _FakeResp(json.dumps({"job_id": "JID-123"}).encode())
        if url == f"{self.BASE}/speech-to-text/job/v1/upload-files" and method == "POST":
            return _FakeResp(json.dumps({
                "upload_urls": {
                    self.file_name: {"file_url": "https://blob.example/upload?sig=u"}
                }
            }).encode())
        if url == f"{self.BASE}/speech-to-text/job/v1/JID-123/start" and method == "POST":
            return _FakeResp(json.dumps({"job_state": "Running"}).encode())
        if url == f"{self.BASE}/speech-to-text/job/v1/JID-123/status" and method == "GET":
            return _FakeResp(json.dumps({
                "job_state": "Completed",
                "job_details": [{
                    "state": "Success",
                    "inputs": [{"file_name": self.file_name}],
                    "outputs": [{"file_name": "out-001.json"}],
                }],
            }).encode())
        if url == f"{self.BASE}/speech-to-text/job/v1/download-files" and method == "POST":
            return _FakeResp(json.dumps({
                "download_urls": {
                    "out-001.json": {"file_url": "https://blob.example/download?sig=d"}
                }
            }).encode())

        # --- Presigned Azure blob upload / download ---
        if url == "https://blob.example/upload?sig=u" and method == "PUT":
            return _FakeResp(b"", status=201)
        if url == "https://blob.example/download?sig=d" and method == "GET":
            return _FakeResp(json.dumps({"transcript": "long voice note text"}).encode())

        raise AssertionError(f"unexpected request: {method} {url}")

    def _find(self, method, url):
        for m, u, h, b in self.requests:
            if m == method and u == url:
                return (m, u, h, b)
        self.fail(f"no {method} {url} in recorded requests: "
                  f"{[(m, u) for m, u, _, _ in self.requests]}")

    def test_returns_transcript_end_to_end(self):
        out = self.mod._transcribe_batch(self.audio_path, self.KEY)
        self.assertEqual(out, "long voice note text")

    def test_init_job_request(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, body = self._find("POST", f"{self.BASE}/speech-to-text/job/v1")
        self.assertEqual(headers.get("api-subscription-key"), self.KEY)
        self.assertEqual(headers.get("content-type"), "application/json")
        payload = json.loads(body)
        self.assertEqual(payload, {
            "job_parameters": {
                "language_code": "unknown",
                "model": "saaras:v3",
                "mode": "translate",
                "num_speakers": None,
                "with_diarization": False,
                "with_timestamps": False,
            }
        })

    def test_upload_links_request(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, body = self._find(
            "POST", f"{self.BASE}/speech-to-text/job/v1/upload-files")
        self.assertEqual(headers.get("api-subscription-key"), self.KEY)
        payload = json.loads(body)
        self.assertEqual(payload, {"job_id": "JID-123", "files": [self.file_name]})

    def test_blob_upload_put_headers(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, body = self._find("PUT", "https://blob.example/upload?sig=u")
        self.assertEqual(headers.get("x-ms-blob-type"), "BlockBlob")
        # .ogg guesses audio/ogg; the contract just requires a real Content-Type.
        self.assertIn("content-type", headers)
        self.assertEqual(body, b"OggS-fake-audio-bytes")
        # The presigned blob PUT must NOT carry the Sarvam api key.
        self.assertNotIn("api-subscription-key", headers)

    def test_start_request(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, body = self._find(
            "POST", f"{self.BASE}/speech-to-text/job/v1/JID-123/start")
        self.assertEqual(headers.get("api-subscription-key"), self.KEY)
        # No JSON body / no ptu_id query param.
        self.assertIsNone(body)

    def test_status_poll_request(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, _b = self._find(
            "GET", f"{self.BASE}/speech-to-text/job/v1/JID-123/status")
        self.assertEqual(headers.get("api-subscription-key"), self.KEY)

    def test_download_links_request(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        _m, _u, headers, body = self._find(
            "POST", f"{self.BASE}/speech-to-text/job/v1/download-files")
        self.assertEqual(headers.get("api-subscription-key"), self.KEY)
        payload = json.loads(body)
        self.assertEqual(payload, {"job_id": "JID-123", "files": ["out-001.json"]})

    def test_call_sequence_order(self):
        self.mod._transcribe_batch(self.audio_path, self.KEY)
        seq = [(m, u) for m, u, _, _ in self.requests]
        self.assertEqual(seq, [
            ("POST", f"{self.BASE}/speech-to-text/job/v1"),
            ("POST", f"{self.BASE}/speech-to-text/job/v1/upload-files"),
            ("PUT", "https://blob.example/upload?sig=u"),
            ("POST", f"{self.BASE}/speech-to-text/job/v1/JID-123/start"),
            ("GET", f"{self.BASE}/speech-to-text/job/v1/JID-123/status"),
            ("POST", f"{self.BASE}/speech-to-text/job/v1/download-files"),
            ("GET", "https://blob.example/download?sig=d"),
        ])


class TestSarvamBatchFailureModes(unittest.TestCase):
    KEY = "k"
    BASE = "https://api.sarvam.ai"

    def setUp(self):
        self.mod = load_provider()
        fd, self.audio_path = tempfile.mkstemp(suffix=".ogg")
        os.write(fd, b"x")
        os.close(fd)
        self.file_name = os.path.basename(self.audio_path)
        self._orig = urllib.request.urlopen
        os.environ["C3_STT_BUDGET_SECONDS"] = "270"

    def tearDown(self):
        urllib.request.urlopen = self._orig
        os.unlink(self.audio_path)
        os.environ.pop("C3_STT_BUDGET_SECONDS", None)

    def test_failed_job_returns_none(self):
        def fake(req, timeout=None):
            url, method = req.full_url, req.get_method()
            if url == f"{self.BASE}/speech-to-text/job/v1":
                return _FakeResp(json.dumps({"job_id": "J"}).encode())
            if url == f"{self.BASE}/speech-to-text/job/v1/upload-files":
                return _FakeResp(json.dumps({
                    "upload_urls": {self.file_name: {"file_url": "https://b/u"}}}).encode())
            if url == "https://b/u":
                return _FakeResp(b"", status=201)
            if url.endswith("/start"):
                return _FakeResp(json.dumps({"job_state": "Running"}).encode())
            if url.endswith("/status"):
                return _FakeResp(json.dumps({"job_state": "Failed"}).encode())
            raise AssertionError(f"unexpected {method} {url}")
        urllib.request.urlopen = fake
        self.assertIsNone(self.mod._transcribe_batch(self.audio_path, self.KEY))


if __name__ == "__main__":
    unittest.main()
