import os
import subprocess
import sys
from pathlib import Path


MVP_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(MVP_DIR))

import codex_supervisor


def executable(path: Path) -> Path:
    path.write_text("#!/bin/sh\n")
    path.chmod(0o755)
    return path


def test_find_real_codex_skips_wrapper_path(tmp_path, monkeypatch):
    wrapper_dir = tmp_path / "wrapper"
    real_dir = tmp_path / "real"
    wrapper_dir.mkdir()
    real_dir.mkdir()
    wrapper = executable(wrapper_dir / "codex")
    real = executable(real_dir / "codex")

    monkeypatch.delenv("C3_CODEX_REAL", raising=False)
    monkeypatch.setenv("PATH", os.pathsep.join([str(wrapper_dir), str(real_dir)]))

    assert codex_supervisor.find_real_codex(wrapper) == real


def test_find_real_codex_honors_explicit_env(tmp_path, monkeypatch):
    real = executable(tmp_path / "codex-real")
    monkeypatch.setenv("C3_CODEX_REAL", str(real))

    assert codex_supervisor.find_real_codex(tmp_path / "codex") == real


def test_find_real_codex_falls_back_to_nvm_package_script(tmp_path, monkeypatch):
    wrapper_dir = tmp_path / "wrapper"
    real_dir = tmp_path / ".nvm" / "versions" / "node" / "v20.0.0" / "lib" / "node_modules" / "@openai" / "codex" / "bin"
    wrapper_dir.mkdir()
    real_dir.mkdir(parents=True)
    wrapper = executable(wrapper_dir / "codex")
    real = executable(real_dir / "codex.js")

    monkeypatch.delenv("C3_CODEX_REAL", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("PATH", str(wrapper_dir))

    assert codex_supervisor.find_real_codex(wrapper) == real


def test_topic_name_uses_nearest_project_claude_md(tmp_path):
    shared = tmp_path / "arogara"
    project = shared / "sthapati"
    child = project / "src"
    child.mkdir(parents=True)
    (shared / "CLAUDE.md").write_text("root")
    (project / "CLAUDE.md").write_text("project")

    assert codex_supervisor.infer_topic_name(child, shared_root=shared) == "sthapati"
    assert codex_supervisor.infer_topic_name(shared, shared_root=shared) == "arogara"


def test_build_codex_argv_wraps_remote_without_clobbering_user_cwd(tmp_path):
    real = tmp_path / "codex-real"
    cwd = tmp_path / "project"
    ws_url = "ws://127.0.0.1:8766"

    argv = codex_supervisor.build_codex_argv(real, ws_url, cwd, "sthapati", ["--model", "gpt-5.5"])
    assert argv[0] == str(real)
    assert argv.count("--enable") == 1
    assert argv[argv.index("--enable") + 1] == "goals"
    assert "--remote" in argv
    assert argv[argv.index("--remote") + 1] == ws_url
    assert argv[argv.index("-C") + 1] == str(cwd)
    assert argv[-2:] == ["--model", "gpt-5.5"]
    assert any("mcp_servers.c3_codex.command" in item for item in argv)
    assert any("mcp_servers.c3_codex.env.C3_ATTACH_NAME" in item for item in argv)
    assert any("mcp_servers.c3_codex.env.C3_CODEX_APP_SERVER_WS" in item for item in argv)
    assert any("mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE" in item for item in argv)

    argv = codex_supervisor.build_codex_argv(real, ws_url, cwd, None, ["-C", "/tmp/other"])
    assert argv[argv.index("--remote") + 1] == ws_url
    assert argv[-2:] == ["-C", "/tmp/other"]
    assert argv.count("-C") == 1

    argv = codex_supervisor.build_codex_argv(real, ws_url, cwd, None, ["--enable", "goals"])
    assert argv.count("--enable") == 1


def test_help_and_version_flags_bypass_wrapper():
    assert codex_supervisor.should_bypass(["--help"]) is True
    assert codex_supervisor.should_bypass(["-V"]) is True
    assert codex_supervisor.should_bypass(["mcp", "list"]) is True
    assert codex_supervisor.should_bypass(["plugin", "list"]) is True
    assert codex_supervisor.should_bypass(["--model", "gpt-5.5", "fix this"]) is False


def test_resume_and_fork_are_wrapped_for_c3_integration(tmp_path):
    real = tmp_path / "codex-real"
    cwd = tmp_path / "project"
    ws_url = "ws://127.0.0.1:8766"

    assert codex_supervisor.should_bypass(["resume", "--last"]) is False
    assert codex_supervisor.should_bypass(["fork", "--last"]) is False

    argv = codex_supervisor.build_codex_argv(real, ws_url, cwd, "arogara", ["resume", "thread-1"])
    assert argv[argv.index("--remote") + 1] == ws_url
    assert argv[-2:] == ["resume", "thread-1"]
    assert "-c" in argv


def test_ensure_app_server_starts_with_c3_mcp_config(tmp_path, monkeypatch):
    launched = {}
    real = tmp_path / "codex-real"
    real.write_text("#!/bin/sh\n")
    real.chmod(0o755)

    monkeypatch.setattr(codex_supervisor, "tcp_reachable", lambda _host, _port: False)
    monkeypatch.setattr(codex_supervisor, "wait_for_tcp", lambda _host, _port: True)

    class FakePopen:
        def __init__(self, argv, **kwargs):
            self.pid = 123
            launched["argv"] = argv
            launched["kwargs"] = kwargs

        def terminate(self):
            launched["terminated"] = True

    monkeypatch.setattr(subprocess, "Popen", FakePopen)

    codex_supervisor.ensure_app_server(
        real,
        "ws://127.0.0.1:8766",
        tmp_path / "project",
        "arogara",
    )

    argv = launched["argv"]
    assert argv[0] == str(real)
    assert argv.count("--enable") == 1
    assert argv[argv.index("--enable") + 1] == "goals"
    assert "app-server" in argv
    assert argv[-2:] == ["--listen", "ws://127.0.0.1:8766"]
    assert any("mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE" in item for item in argv)
    assert any("mcp_servers.c3_codex.env.C3_ATTACH_NAME" in item for item in argv)


def test_choose_app_server_url_avoids_stale_default_port(tmp_path, monkeypatch):
    monkeypatch.setattr(codex_supervisor, "META_FILE", tmp_path / "meta.json")

    def reachable(host, port):
        return port == 8766

    monkeypatch.setattr(codex_supervisor, "tcp_reachable", reachable)

    assert codex_supervisor.choose_app_server_url(
        "ws://127.0.0.1:8766",
        tmp_path / "project",
        "arogara",
    ) == "ws://127.0.0.1:8767"


def test_choose_app_server_url_reuses_matching_metadata(tmp_path, monkeypatch):
    meta = tmp_path / "meta.json"
    monkeypatch.setattr(codex_supervisor, "META_FILE", meta)
    cwd = tmp_path / "project"
    cwd.mkdir()
    codex_supervisor.write_app_server_meta("ws://127.0.0.1:8766", cwd, "arogara", 123)
    monkeypatch.setattr(codex_supervisor, "tcp_reachable", lambda _host, _port: True)

    assert codex_supervisor.choose_app_server_url(
        "ws://127.0.0.1:8766",
        cwd,
        "arogara",
    ) == "ws://127.0.0.1:8766"
