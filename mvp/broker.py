#!/usr/bin/env python3
"""C3 broker — spawn the official Telegram plugin's bun server.ts once, fan its
MCP notifications out to N Claude Code stubs by forum-topic id, and fan stubs'
tool calls back into bun.

Wire:
    bun server.ts  <- stdio ->  broker  <- unix sock ->  stub  <- stdio ->  Claude Code

Topics routing key = (chat_id, message_thread_id).  message_thread_id=0 means
"general" (no topic). Stubs claim a (chat_id, topic_id) pair via the `attach`
op; inbound MCP notifications from bun get forwarded only to the matching stub.

Patches to server.ts are applied idempotently at startup by patch_server.py.

IPC protocol (newline-delimited JSON on /tmp/c3.sock):

  stub -> broker:
    {"op":"server_info"}
    {"op":"attach","chat_id":..,"topic_id":..}
    {"op":"attach_auto","name":"..."}               (create-if-missing by name)
    {"op":"tools_list","id":..}
    {"op":"tool_call","id":..,"name":..,"args":{...}}

  broker -> stub:
    {"op":"server_info","serverInfo":{...},"capabilities":{...},"instructions":".."}
    {"op":"attached","ok":true,"chat_id":..,"topic_id":..,"name":".."}
    {"op":"tools_list","id":..,"tools":[...]}
    {"op":"tool_result","id":..,"result":{...}}  |  {"op":"tool_result","id":..,"error":{...}}
    {"op":"inbound","method":"notifications/claude/channel","params":{...}}
"""
import fcntl
import json
import os
import re
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
SOCK_PATH   = "/tmp/c3.sock"
TOPICS_FILE = HERE / "topics.json"
CONFIG_FILE = HERE / "config.json"

PLUGIN_BASE = Path.home() / ".claude" / "plugins" / "cache" / "claude-plugins-official" / "telegram"
STATE_DIR   = Path.home() / ".claude" / "channels" / "telegram"
ENV_FILE    = STATE_DIR / ".env"


def load_env(path: Path) -> dict:
    env = {}
    try:
        for line in path.read_text().splitlines():
            if "=" in line and not line.startswith("#"):
                k, v = line.split("=", 1)
                if k.strip().isidentifier():
                    env[k.strip()] = v.strip()
    except Exception:
        pass
    return env


def latest_plugin_dir() -> Path:
    versions = sorted(
        [p for p in PLUGIN_BASE.iterdir() if p.is_dir() and re.match(r"^\d+\.\d+\.\d+$", p.name)]
    )
    if not versions:
        sys.stderr.write("c3-broker: no telegram plugin versions found\n")
        sys.exit(1)
    return versions[-1]


ENV = load_env(ENV_FILE)
TOKEN = ENV.get("TELEGRAM_BOT_TOKEN") or os.environ.get("TELEGRAM_BOT_TOKEN")
if not TOKEN:
    sys.stderr.write(f"c3-broker: TELEGRAM_BOT_TOKEN missing from {ENV_FILE}\n")
    sys.exit(1)


def tg(method: str, **params) -> dict:
    url = f"https://api.telegram.org/bot{TOKEN}/{method}"
    data = json.dumps(params).encode()
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read())


# ─── Topics registry ───────────────────────────────────────────────────────────

def load_topics() -> list:
    if TOPICS_FILE.exists():
        try:
            return json.loads(TOPICS_FILE.read_text())
        except Exception:
            pass
    return []


TOPICS_LOCK = threading.Lock()

def save_topics(topics):
    TOPICS_FILE.write_text(json.dumps(topics, indent=2))


def upsert_topic(chat_id: int, topic_id: int, name: str | None):
    with TOPICS_LOCK:
        topics = load_topics()
        for t in topics:
            if t["chat_id"] == chat_id and t["topic_id"] == topic_id:
                if name and t.get("name") != name:
                    t["name"] = name
                    save_topics(topics)
                return
        topics.append({"chat_id": chat_id, "topic_id": topic_id, "name": name or f"topic-{topic_id}"})
        save_topics(topics)


def find_topic_by_name(name: str):
    for t in load_topics():
        if t["name"] == name:
            return t
    return None


def default_group_chat_id() -> int | None:
    """Pick a group chat_id to create new topics in. Prefer config, else any known group."""
    if CONFIG_FILE.exists():
        try:
            cfg = json.loads(CONFIG_FILE.read_text())
            if cfg.get("group_chat_id"):
                return int(cfg["group_chat_id"])
        except Exception:
            pass
    for t in load_topics():
        if t["chat_id"] < 0:  # groups have negative ids
            return t["chat_id"]
    return None


# ─── Bun subprocess ───────────────────────────────────────────────────────────

class Bun:
    def __init__(self, plugin_dir: Path):
        # Bun's `bun run` re-resolves node_modules — just exec bun on server.ts directly.
        env = os.environ.copy()
        for k, v in ENV.items():
            env.setdefault(k, v)
        self.proc = subprocess.Popen(
            ["bun", "server.ts"],
            cwd=str(plugin_dir),
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            bufsize=0,
            env=env,
        )
        self.lock = threading.Lock()
        self.next_id = 100  # start high to avoid colliding with initialize/tools-list ids
        self.pending = {}   # id -> callback(result_or_error)
        self.pending_lock = threading.Lock()
        self.initialize_result = None
        self.tools = []
        self.notify_handler = None  # set by broker
        threading.Thread(target=self._stderr_pump, daemon=True).start()
        threading.Thread(target=self._stdout_pump, daemon=True).start()

    def _stderr_pump(self):
        for line in self.proc.stderr:
            sys.stderr.write(f"[bun] {line.decode(errors='replace')}")

    def _stdout_pump(self):
        for raw in self.proc.stdout:
            try:
                msg = json.loads(raw.decode())
            except Exception:
                sys.stderr.write(f"c3-broker: bun emitted non-JSON: {raw[:120]!r}\n")
                continue
            if "id" in msg and ("result" in msg or "error" in msg):
                cb = None
                with self.pending_lock:
                    cb = self.pending.pop(msg["id"], None)
                if cb:
                    cb(msg)
                else:
                    sys.stderr.write(f"c3-broker: orphan response id={msg['id']}\n")
            elif "method" in msg:
                if self.notify_handler:
                    try:
                        self.notify_handler(msg)
                    except Exception as e:
                        sys.stderr.write(f"c3-broker: notify_handler error: {e}\n")

    def _raw_send(self, obj):
        data = (json.dumps(obj) + "\n").encode()
        with self.lock:
            self.proc.stdin.write(data)
            self.proc.stdin.flush()

    def request(self, method: str, params=None, timeout=30):
        """Send a JSON-RPC request and block until response."""
        with self.pending_lock:
            self.next_id += 1
            rid = self.next_id
        done = threading.Event()
        holder = {}
        def cb(resp):
            holder["resp"] = resp
            done.set()
        with self.pending_lock:
            self.pending[rid] = cb
        msg = {"jsonrpc": "2.0", "id": rid, "method": method}
        if params is not None:
            msg["params"] = params
        self._raw_send(msg)
        if not done.wait(timeout):
            with self.pending_lock:
                self.pending.pop(rid, None)
            raise TimeoutError(f"bun timed out on {method}")
        return holder["resp"]

    def notify(self, method: str, params=None):
        msg = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            msg["params"] = params
        self._raw_send(msg)

    def initialize(self):
        resp = self.request("initialize", {
            "protocolVersion": "2024-11-05",
            "capabilities": {
                "experimental": {
                    "claude/channel": {},
                    "claude/channel/permission": {},
                }
            },
            "clientInfo": {"name": "c3-broker", "version": "0.0.1"},
        })
        if "error" in resp:
            raise RuntimeError(f"bun initialize failed: {resp['error']}")
        self.initialize_result = resp["result"]
        self.notify("notifications/initialized")
        tools_resp = self.request("tools/list")
        self.tools = tools_resp.get("result", {}).get("tools", [])
        sys.stderr.write(f"c3-broker: bun reports {len(self.tools)} tools: "
                         f"{[t['name'] for t in self.tools]}\n")


# ─── Stub connection + routing ────────────────────────────────────────────────

class StubConn:
    def __init__(self, sock: socket.socket):
        self.sock = sock
        self.file = sock.makefile("rwb", buffering=0)
        self.chat_id = None
        self.topic_id = None
        self.name = None
        self.lock = threading.Lock()
        self.alive = True
        # Peer pid for diagnostics (e.g. "who holds this topic?").
        try:
            import struct
            creds = sock.getsockopt(socket.SOL_SOCKET, 17, struct.calcsize("iII"))  # 17 = SO_PEERCRED
            self.pid = struct.unpack("iII", creds)[0]
        except Exception:
            self.pid = None

    def send(self, obj: dict):
        if not self.alive:
            return
        try:
            with self.lock:
                self.file.write((json.dumps(obj) + "\n").encode())
        except Exception:
            self.alive = False


ROUTES_LOCK = threading.Lock()
ROUTES: dict[tuple[int, int], StubConn] = {}


def claim(stub: StubConn, chat_id: int, topic_id: int) -> bool:
    with ROUTES_LOCK:
        existing = ROUTES.get((chat_id, topic_id))
        if existing is not None and existing is not stub and existing.alive:
            return False
        # Releasing a prior claim by THIS stub (so it can move between topics).
        prev_key = (stub.chat_id, stub.topic_id)
        if prev_key != (None, None) and prev_key != (chat_id, topic_id):
            if ROUTES.get(prev_key) is stub:
                del ROUTES[prev_key]
        ROUTES[(chat_id, topic_id)] = stub
        stub.chat_id, stub.topic_id = chat_id, topic_id
        return True


def release_stub(stub: StubConn):
    with ROUTES_LOCK:
        key = (stub.chat_id, stub.topic_id)
        if key in ROUTES and ROUTES[key] is stub:
            del ROUTES[key]


def snapshot_topics_with_claims() -> list:
    """Return all known topics with who (if anyone) claims each."""
    out = []
    with ROUTES_LOCK:
        for t in load_topics():
            key = (t["chat_id"], t["topic_id"])
            stub = ROUTES.get(key)
            claimed_by = stub.pid if (stub is not None and stub.alive) else None
            out.append({**t, "claimed_by": claimed_by})
    return out


def route_inbound(chat_id: int, thread_id: int) -> StubConn | None:
    with ROUTES_LOCK:
        return ROUTES.get((chat_id, thread_id))


# ─── Main ─────────────────────────────────────────────────────────────────────

# Cooldown for "no terminal attached" fallback replies, keyed by (chat, thread).
# Telegram voice messages often arrive in bursts; one nudge per 5 min is enough.
FALLBACK_COOLDOWN_S = 300
_last_fallback: dict[tuple[int, int], float] = {}
_fallback_lock = threading.Lock()


def send_fallback_reply(bun: Bun, chat_id: int, thread_id: int):
    now = time.time()
    key = (chat_id, thread_id)
    with _fallback_lock:
        last = _last_fallback.get(key, 0)
        if now - last < FALLBACK_COOLDOWN_S:
            return
        _last_fallback[key] = now

    args = {
        "chat_id": str(chat_id),
        "text": ("No Claude Code terminal is attached to this chat yet. "
                 "In your CLI, open a Claude Code session and say 'attach to <this topic>' "
                 "(or 'work on <project>') so messages here can route to it."),
    }
    if thread_id:
        args["message_thread_id"] = str(thread_id)
    try:
        bun.request("tools/call", {"name": "reply", "arguments": args}, timeout=15)
    except Exception as e:
        sys.stderr.write(f"c3-broker: fallback reply failed: {e}\n")


def build_notify_handler(bun: Bun):
    def handle(msg: dict):
        method = msg.get("method")
        params = msg.get("params", {})
        sys.stderr.write(f"c3-broker: bun->broker notification: {method}\n")
        if method == "notifications/claude/channel":
            meta = params.get("meta", {})
            try:
                chat_id = int(meta.get("chat_id", 0))
            except Exception:
                chat_id = 0
            try:
                thread_id = int(meta.get("message_thread_id", 0))
            except Exception:
                thread_id = 0

            # Opportunistically remember this topic so c3-attach can find it.
            if chat_id:
                upsert_topic(chat_id, thread_id, None)

            stub = route_inbound(chat_id, thread_id)
            if stub is None:
                sys.stderr.write(f"c3-broker: no stub for chat={chat_id} thread={thread_id}; sending fallback\n")
                threading.Thread(target=send_fallback_reply, args=(bun, chat_id, thread_id), daemon=True).start()
                return
            stub.send({"op": "inbound", "method": method, "params": params})
        else:
            # Other notifications from bun (e.g. permission) — broadcast for now.
            # MVP: permission relay not yet implemented; log and drop.
            sys.stderr.write(f"c3-broker: unhandled bun notification: {method}\n")
    return handle


def handle_stub(bun: Bun, sock: socket.socket):
    stub = StubConn(sock)
    try:
        for raw in stub.file:
            try:
                req = json.loads(raw.decode())
            except Exception:
                continue
            op = req.get("op")

            if op == "server_info":
                info = bun.initialize_result or {}
                stub.send({
                    "op": "server_info",
                    "serverInfo": info.get("serverInfo", {}),
                    "capabilities": info.get("capabilities", {}),
                    "instructions": info.get("instructions", ""),
                    "protocolVersion": info.get("protocolVersion", "2024-11-05"),
                })

            elif op == "attach":
                chat_id = int(req["chat_id"]); topic_id = int(req["topic_id"])
                ok = claim(stub, chat_id, topic_id)
                name = None
                for t in load_topics():
                    if t["chat_id"] == chat_id and t["topic_id"] == topic_id:
                        name = t["name"]; break
                stub.send({"op": "attached", "ok": ok, "chat_id": chat_id, "topic_id": topic_id, "name": name})

            elif op == "attach_auto":
                # Attach by topic name. Create if missing.
                name = req["name"]
                t = find_topic_by_name(name)
                sys.stderr.write(f"c3-broker: attach_auto name={name!r} found={t!r}\n")
                if t is None:
                    group_id = default_group_chat_id()
                    if group_id is None:
                        stub.send({"op": "attached", "ok": False, "err":
                                   "no known group chat_id; send one message in the target group first, or set group_chat_id in config.json"})
                        continue
                    try:
                        r = tg("createForumTopic", chat_id=group_id, name=name)
                        if not r.get("ok"):
                            raise RuntimeError(r.get("description", "createForumTopic failed"))
                        new_topic_id = r["result"]["message_thread_id"]
                        upsert_topic(group_id, new_topic_id, name)
                        t = {"chat_id": group_id, "topic_id": new_topic_id, "name": name}
                    except Exception as e:
                        stub.send({"op": "attached", "ok": False, "err": f"createForumTopic: {e}"})
                        continue
                ok = claim(stub, t["chat_id"], t["topic_id"])
                sys.stderr.write(f"c3-broker: attach_auto claim ok={ok} chat={t['chat_id']} topic={t['topic_id']}\n")
                resp_msg = {"op": "attached", "ok": ok, "chat_id": t["chat_id"], "topic_id": t["topic_id"], "name": t["name"]}
                if not ok:
                    with ROUTES_LOCK:
                        holder = ROUTES.get((t["chat_id"], t["topic_id"]))
                    if holder is not None and holder.pid:
                        resp_msg["err"] = f"topic '{t['name']}' is held by terminal pid {holder.pid}"
                stub.send(resp_msg)

            elif op == "tools_list":
                stub.send({"op": "tools_list", "id": req.get("id"), "tools": bun.tools})

            elif op == "list_topics":
                stub.send({"op": "topics_list", "topics": snapshot_topics_with_claims()})

            elif op == "tool_call":
                stub_req_id = req.get("id")
                name = req["name"]
                args = dict(req.get("args", {}))
                # Inject message_thread_id into reply when stub has claimed a topic > 0.
                if name == "reply" and stub.topic_id:
                    args.setdefault("message_thread_id", str(stub.topic_id))
                # Forward to bun and route response back.
                def make_cb(stub=stub, stub_req_id=stub_req_id):
                    def cb(resp):
                        out = {"op": "tool_result", "id": stub_req_id}
                        if "error" in resp:
                            out["error"] = resp["error"]
                        else:
                            out["result"] = resp.get("result")
                        stub.send(out)
                    return cb
                with bun.pending_lock:
                    bun.next_id += 1
                    rid = bun.next_id
                    bun.pending[rid] = make_cb()
                bun._raw_send({
                    "jsonrpc": "2.0",
                    "id": rid,
                    "method": "tools/call",
                    "params": {"name": name, "arguments": args},
                })
            else:
                stub.send({"op": "error", "err": f"unknown op: {op}"})
    except Exception as e:
        sys.stderr.write(f"c3-broker: stub session error: {e}\n")
    finally:
        stub.alive = False
        release_stub(stub)
        try:
            sock.close()
        except Exception:
            pass


def sock_server(bun: Bun):
    if os.path.exists(SOCK_PATH):
        os.unlink(SOCK_PATH)
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.bind(SOCK_PATH)
    os.chmod(SOCK_PATH, 0o600)
    s.listen(16)
    sys.stderr.write(f"c3-broker: listening on {SOCK_PATH}\n")
    while True:
        conn, _ = s.accept()
        threading.Thread(target=handle_stub, args=(bun, conn), daemon=True).start()


def acquire_singleton_lock():
    """Ensure only one broker runs at a time.

    Telegram's Bot API permits exactly one getUpdates consumer per token, so
    two brokers would cascade into 409 Conflict. flock on a pid file auto-
    releases when the process exits (clean or crash), so stale locks are
    harmless. The fd is held open for the life of the process — do not close
    it.
    """
    lock_path = "/tmp/c3-broker.pid"
    fd = os.open(lock_path, os.O_CREAT | os.O_WRONLY, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        sys.stderr.write(f"c3-broker: another broker already holds {lock_path}; exiting\n")
        sys.exit(0)
    os.ftruncate(fd, 0)
    os.write(fd, f"{os.getpid()}\n".encode())
    # Intentionally leak fd — closing would release the flock.


def main():
    acquire_singleton_lock()
    from patch_server import apply_patches
    from install_stt import install as install_stt
    plugin_dir = latest_plugin_dir()
    applied = apply_patches(plugin_dir)
    if applied:
        sys.stderr.write(f"c3-broker: applied {len(applied)} patch(es) to {plugin_dir.name}\n")
    install_stt()

    bun = Bun(plugin_dir)
    try:
        bun.initialize()
    except Exception as e:
        sys.stderr.write(f"c3-broker: bun initialize failed: {e}\n")
        bun.proc.terminate()
        sys.exit(1)

    bun.notify_handler = build_notify_handler(bun)

    def shutdown(*_):
        sys.stderr.write("c3-broker: shutting down\n")
        try:
            # Closing stdin lets server.ts run its own graceful shutdown
            # (it kills itself on stdin EOF to release the token slot).
            bun.proc.stdin.close()
        except Exception:
            pass
        try:
            bun.proc.wait(timeout=3)
        except Exception:
            bun.proc.kill()
        sys.exit(0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)

    try:
        sock_server(bun)
    except KeyboardInterrupt:
        shutdown()


if __name__ == "__main__":
    main()
