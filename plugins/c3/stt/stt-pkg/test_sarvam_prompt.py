#!/usr/bin/env python3
"""Hermetic test for providers/sarvam-saaras-v3.py:_get_prompt context-drop.

Pins that _get_prompt returns ONLY 'Key terms: <terms>' (a spelling hint) and
deliberately DROPS the topic-priming free-text `context` narrative, which as a
prompt bias seeds hallucinated transcripts on silent/unclear audio. A future
edit that re-introduced the context into the prompt would regress that fix.

Dependency-light on purpose (matches the repo style): plain `assert`s, stdlib
only, NO pytest. Loads the provider by file path via importlib because its
filename ('sarvam-saaras-v3.py') isn't a legal module name. No network, no
ffmpeg, no API key — the module is side-effect-free at import.

Runnable as:  python3 test_sarvam_prompt.py
Exits 0 when all checks pass, non-zero on the first failing group.
"""
import importlib.util
import os
import sys

PROVIDER_PATH = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "providers", "sarvam-saaras-v3.py")


def _load_provider():
    """Import the hyphen-named provider by file path (a fresh module each call
    so set_vocabulary state never leaks between tests)."""
    spec = importlib.util.spec_from_file_location("sarvam_saaras_v3", PROVIDER_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


CONTEXT_NARRATIVE = "Technical discussion about DevOps and Kubernetes deployments"


def test_get_prompt_drops_context():
    prov = _load_provider()
    prov.set_vocabulary({
        "terms": [{"preferred": "Kubernetes"}, {"preferred": "Grafana"}],
        "context": CONTEXT_NARRATIVE,
    })
    got = prov._get_prompt()
    # Exactly the terms spelling hint — nothing more.
    assert got == "Key terms: Kubernetes, Grafana", f"unexpected prompt: {got!r}"
    # The topic-priming narrative must NOT leak into the prompt.
    assert CONTEXT_NARRATIVE not in got, "context narrative must be dropped"
    assert "DevOps" not in got and "discussion" not in got, \
        "no topic-priming words from context"
    print("PASS test_get_prompt_drops_context")


def test_get_prompt_none_without_terms():
    prov = _load_provider()
    # Non-empty context but NO terms -> None (never prime the model on context).
    prov.set_vocabulary({"terms": [], "context": "some context that must not leak"})
    assert prov._get_prompt() is None, "no terms => prompt must be None"
    # A wholly-empty / None vocab is also None (no crash).
    prov.set_vocabulary(None)
    assert prov._get_prompt() is None, "empty vocab => prompt must be None"
    print("PASS test_get_prompt_none_without_terms")


def main():
    tests = [
        test_get_prompt_drops_context,
        test_get_prompt_none_without_terms,
    ]
    failed = 0
    for t in tests:
        try:
            t()
        except AssertionError as e:
            failed += 1
            print(f"FAIL {t.__name__}: {e}", file=sys.stderr)
        except Exception as e:
            failed += 1
            print(f"ERROR {t.__name__}: {type(e).__name__}: {e}", file=sys.stderr)
    if failed:
        print(f"\n{failed} test(s) failed", file=sys.stderr)
        sys.exit(1)
    print(f"\nAll {len(tests)} test groups passed")


if __name__ == "__main__":
    main()
