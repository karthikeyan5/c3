#!/usr/bin/env python3
"""Single-command C3 launcher for Codex.

Run this through the `codex` shim. It starts/reuses a local Codex app-server,
injects the C3 MCP stub for this launch, then launches the real Codex TUI
against that app-server.
"""
from __future__ import annotations

import os
import json
import socket
import subprocess
import sys
import time
from pathlib import Path


HERE = Path(__file__).resolve().parent
CODEX_STUB = HERE / "codex_stub.py"
DEFAULT_WS_URL = "ws://127.0.0.1:8766"
LOG_FILE = os.environ.get("C3_CODEX_SUPERVISOR_LOG", "/tmp/c3-codex-supervisor.log")
META_FILE = Path(os.environ.get("C3_CODEX_APP_SERVER_META", "/tmp/c3-codex-app-server.json"))
SHARED_ROOT = Path(os.path.expanduser("~/arogara")).resolve()
REQUIRED_TUI_FEATURES = ("goals",)

CODEX_SUBCOMMANDS = {
    "exec",
    "e",
    "review",
    "login",
    "logout",
    "mcp",
    "plugin",
    "mcp-server",
    "app-server",
    "completion",
    "update",
    "sandbox",
    "debug",
    "apply",
    "a",
    "cloud",
    "exec-server",
    "features",
    "help",
}


def log(msg: str) -> None:
    try:
        with open(LOG_FILE, "a") as f:
            f.write(f"{time.strftime('%H:%M:%S')} [pid={os.getpid()}] {msg}\n")
    except Exception:
        pass


def find_real_codex(wrapper_path: Path) -> Path:
    explicit = os.environ.get("C3_CODEX_REAL")
    if explicit:
        real = Path(explicit).expanduser().resolve()
        if real.exists():
            return real
        raise FileNotFoundError(f"C3_CODEX_REAL does not exist: {real}")

    wrapper = wrapper_path.resolve()
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        if not directory:
            continue
        candidate = Path(directory) / "codex"
        if not candidate.exists() or not os.access(candidate, os.X_OK):
            continue
        try:
            if candidate.resolve() == wrapper:
                continue
        except Exception:
            pass
        return candidate.resolve()
    for candidate in fallback_real_codex_candidates():
        if candidate.exists() and os.access(candidate, os.X_OK):
            return candidate.resolve()
    raise FileNotFoundError("could not find the real codex binary; set C3_CODEX_REAL")


def fallback_real_codex_candidates() -> list[Path]:
    candidates: list[Path] = []
    nvm_nodes = Path(os.path.expanduser("~/.nvm/versions/node"))
    if nvm_nodes.exists():
        candidates.extend(sorted(
            nvm_nodes.glob("*/lib/node_modules/@openai/codex/bin/codex.js"),
            reverse=True,
        ))
    return candidates


def infer_topic_name(cwd: Path, shared_root: Path = SHARED_ROOT) -> str | None:
    cwd = cwd.resolve()
    shared_root = shared_root.resolve()
    for directory in [cwd] + list(cwd.parents):
        if (directory / "CLAUDE.md").exists():
            return directory.name
    return cwd.name


def first_non_option(args: list[str]) -> str | None:
    skip_next = False
    options_with_values = {
        "-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env",
        "-i", "--image", "-m", "--model", "-p", "--profile", "-s", "--sandbox",
        "-C", "--cd", "--add-dir", "-a", "--ask-for-approval",
    }
    for arg in args:
        if skip_next:
            skip_next = False
            continue
        if arg == "--":
            return None
        if arg in options_with_values:
            skip_next = True
            continue
        if arg.startswith("-"):
            continue
        return arg
    return None


def should_bypass(args: list[str]) -> bool:
    if os.environ.get("C3_CODEX_DISABLE") == "1":
        return True
    if "-h" in args or "--help" in args or "-V" in args or "--version" in args:
        return True
    if "--remote" in args:
        return True
    first = first_non_option(args)
    return first in CODEX_SUBCOMMANDS


def has_cwd_arg(args: list[str]) -> bool:
    return "-C" in args or "--cd" in args


def toml_string(value: str) -> str:
    import json
    return json.dumps(value)


def mcp_config_args(ws_url: str, cwd: Path, topic: str | None) -> list[str]:
    args = [
        "-c", "mcp_servers.c3_codex.command=\"python3\"",
        "-c", f"mcp_servers.c3_codex.args=[{toml_string(str(CODEX_STUB))}]",
        "-c", f"mcp_servers.c3_codex.env.C3_CODEX_APP_SERVER_WS={toml_string(ws_url)}",
        "-c", f"mcp_servers.c3_codex.env.C3_CODEX_CWD={toml_string(str(cwd))}",
        "-c", "mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE=\"1\"",
        "-c", "mcp_servers.c3_codex.enabled=true",
    ]
    if topic:
        args.extend(["-c", f"mcp_servers.c3_codex.env.C3_ATTACH_NAME={toml_string(topic)}"])
    return args


def has_feature_arg(args: list[str], feature: str) -> bool:
    for index, arg in enumerate(args):
        if arg == "--enable" and index + 1 < len(args) and args[index + 1] == feature:
            return True
        if arg == f"--enable={feature}":
            return True
    return False


def required_feature_args(existing_args: list[str] | None = None) -> list[str]:
    existing_args = existing_args or []
    args: list[str] = []
    for feature in REQUIRED_TUI_FEATURES:
        if not has_feature_arg(existing_args, feature):
            args.extend(["--enable", feature])
    return args


def build_codex_argv(real_codex: Path, ws_url: str, cwd: Path, topic: str | None, args: list[str]) -> list[str]:
    argv = [str(real_codex)]
    argv.extend(required_feature_args(args))
    argv.extend(mcp_config_args(ws_url, cwd, topic))
    argv.extend(["--remote", ws_url])
    if not has_cwd_arg(args):
        argv.extend(["-C", str(cwd)])
    argv.extend(args)
    return argv


def parse_ws_url(ws_url: str) -> tuple[str, int]:
    if not ws_url.startswith("ws://"):
        raise ValueError(f"only ws:// app-server URLs are supported by the supervisor: {ws_url}")
    host_port = ws_url[len("ws://"):].split("/", 1)[0]
    host, port = host_port.rsplit(":", 1)
    return host, int(port)


def ws_url_for(host: str, port: int) -> str:
    return f"ws://{host}:{port}"


def tcp_reachable(host: str, port: int, timeout: float = 0.5) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except Exception:
        return False


def wait_for_tcp(host: str, port: int, timeout: float = 15.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if tcp_reachable(host, port):
            return True
        time.sleep(0.2)
    return False


def app_server_signature(cwd: Path, topic: str | None) -> dict[str, str | None]:
    return {
        "cwd": str(cwd.resolve()),
        "topic": topic,
        "stub": str(CODEX_STUB),
    }


def read_app_server_meta() -> dict[str, object]:
    try:
        return json.loads(META_FILE.read_text())
    except Exception:
        return {}


def write_app_server_meta(ws_url: str, cwd: Path, topic: str | None, pid: int | None) -> None:
    data = {
        "ws_url": ws_url,
        "pid": pid,
        "signature": app_server_signature(cwd, topic),
    }
    try:
        META_FILE.write_text(json.dumps(data, sort_keys=True) + "\n")
    except Exception as e:
        log(f"failed to write app-server metadata: {e}")


def app_server_meta_matches(ws_url: str, cwd: Path, topic: str | None) -> bool:
    meta = read_app_server_meta()
    return (
        meta.get("ws_url") == ws_url
        and meta.get("signature") == app_server_signature(cwd, topic)
    )


def choose_app_server_url(requested_ws_url: str, cwd: Path, topic: str | None) -> str:
    if requested_ws_url != DEFAULT_WS_URL:
        return requested_ws_url
    host, port = parse_ws_url(requested_ws_url)
    if not tcp_reachable(host, port):
        return requested_ws_url
    if app_server_meta_matches(requested_ws_url, cwd, topic):
        return requested_ws_url
    for candidate_port in range(port + 1, port + 50):
        if not tcp_reachable(host, candidate_port):
            candidate = ws_url_for(host, candidate_port)
            log(f"using {candidate}; {requested_ws_url} is occupied by a stale app-server")
            return candidate
    return requested_ws_url


def ensure_app_server(real_codex: Path, ws_url: str, cwd: Path, topic: str | None) -> subprocess.Popen | None:
    host, port = parse_ws_url(ws_url)
    if tcp_reachable(host, port):
        log(f"reusing codex app-server at {ws_url}")
        return None

    log(f"starting codex app-server at {ws_url}")
    out = open("/tmp/c3-codex-app-server.log", "ab")
    argv = [str(real_codex)]
    argv.extend(required_feature_args())
    argv.extend(mcp_config_args(ws_url, cwd, topic))
    argv.extend(["app-server", "--listen", ws_url])
    proc = subprocess.Popen(
        argv,
        stdin=subprocess.DEVNULL,
        stdout=out,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    if not wait_for_tcp(host, port):
        proc.terminate()
        raise RuntimeError(f"codex app-server did not become reachable at {ws_url}")
    write_app_server_meta(ws_url, cwd, topic, proc.pid)
    return proc


def run(argv: list[str], wrapper_path: Path) -> int:
    real_codex = find_real_codex(wrapper_path)
    if should_bypass(argv):
        os.execv(str(real_codex), [str(real_codex)] + argv)

    cwd = Path.cwd().resolve()
    topic = os.environ.get("C3_ATTACH_NAME") or os.environ.get("C3_CODEX_TOPIC") or infer_topic_name(cwd)
    requested_ws_url = os.environ.get("C3_CODEX_APP_SERVER_WS", DEFAULT_WS_URL)
    ws_url = choose_app_server_url(requested_ws_url, cwd, topic)

    ensure_app_server(real_codex, ws_url, cwd, topic)
    env = os.environ.copy()
    env["C3_CODEX_APP_SERVER_WS"] = ws_url
    env["C3_CODEX_REMOTE_BRIDGE"] = "1"
    if topic:
        env["C3_ATTACH_NAME"] = topic
    codex_argv = build_codex_argv(real_codex, ws_url, cwd, topic, argv)
    log(f"launching codex: {' '.join(codex_argv)}")
    return subprocess.call(codex_argv, env=env)


def main() -> int:
    return run(sys.argv[1:], Path(sys.argv[0]))


if __name__ == "__main__":
    raise SystemExit(main())
