#!/usr/bin/env python3
"""C3 MCP stub — stdio MCP server that proxies one Telegram topic to Claude Code.

Spawned by Claude Code per .mcp.json. Reads either
  C3_TOPIC_ID + C3_CHAT_ID         (explicit attach)
or
  C3_ATTACH_NAME                   (attach-by-name, creates topic if missing)
from env, connects to the c3 broker over /tmp/c3.sock, and thereafter proxies:

  Claude Code  <- stdio ->  stub  <- unix sock ->  broker  <- stdio ->  bun server.ts

Capabilities, instructions, and tools list come from bun via the broker — we
don't hardcode them, so plugin upgrades flow through automatically.
"""
import json
import os
import socket
import sys
import threading
import time
from pathlib import Path

LOG_FILE = "/tmp/c3-stub.log"
def log(msg):
    try:
        with open(LOG_FILE, "a") as f:
            f.write(f"{time.strftime('%H:%M:%S')} [pid={os.getpid()}] {msg}\n")
    except Exception:
        pass
log("=== stub started ===")

TOPIC_ID    = os.environ.get("C3_TOPIC_ID")
CHAT_ID     = os.environ.get("C3_CHAT_ID")
ATTACH_NAME = os.environ.get("C3_ATTACH_NAME")
SOCK_PATH   = os.environ.get("C3_SOCK", "/tmp/c3.sock")

# DM chat_id from config.json — used to resolve target='dm' to the user's actual
# 1-on-1 chat (positive chat_id, thread 0), not a forum topic in the C3 group.
def _load_dm_chat_id():
    try:
        cfg_path = Path(__file__).resolve().parent / "config.json"
        with open(cfg_path) as f:
            cfg = json.load(f)
        v = cfg.get("dm_chat_id")
        return int(v) if v is not None else None
    except Exception:
        return None

DM_CHAT_ID = _load_dm_chat_id()

# The "shared root" — its CLAUDE.md is a persona/common-instructions file, NOT
# a project identity. Terminals opened here should start unattached and let
# Karthi pick a topic via the `attach` tool.
SHARED_ROOT = Path(os.path.expanduser("~/arogara")).resolve()


def infer_topic_name():
    """Walk up from cwd to the nearest CLAUDE.md and take that dir's basename.
    Returns None if the nearest CLAUDE.md is at the shared root — meaning
    'no project detected, stay unattached until user picks'."""
    cwd = Path.cwd().resolve()
    for d in [cwd] + list(cwd.parents):
        if (d / "CLAUDE.md").exists():
            if d == SHARED_ROOT:
                return None
            return d.name
    return cwd.name


if not (ATTACH_NAME or (TOPIC_ID and CHAT_ID)):
    ATTACH_NAME = infer_topic_name()
    sys.stderr.write(f"c3-stub: inferred topic name from cwd: {ATTACH_NAME!r}\n")


# ─── Broker connection ────────────────────────────────────────────────────────
# Probe the socket. If unreachable (no broker, or a stale socket file left
# from a crashed broker), spawn a fresh broker detached and wait. The broker
# holds an flock on /tmp/c3-broker.pid, so simultaneous stubs racing to spawn
# is safe — only one wins; extras exit.

def _try_connect(timeout=0.5):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(timeout)
    try:
        s.connect(SOCK_PATH)
        s.settimeout(None)
        return s
    except Exception:
        s.close()
        return None


def _establish_connection():
    """Connect to broker, spawning one if missing. Returns sock or None."""
    s = _try_connect()
    if s is not None:
        return s
    import subprocess as _sp
    broker_path = Path(__file__).resolve().parent / "broker.py"
    try:
        _sp.Popen(
            ["python3", "-u", str(broker_path)],
            stdin=_sp.DEVNULL,
            stdout=open("/tmp/c3b.log", "ab"),
            stderr=_sp.STDOUT,
            start_new_session=True,
        )
        sys.stderr.write(f"c3-stub: spawned broker {broker_path}; waiting for socket\n")
    except Exception as e:
        sys.stderr.write(f"c3-stub: failed to spawn broker: {e}\n")
    for _ in range(40):
        time.sleep(0.25)
        s = _try_connect()
        if s is not None:
            return s
    return None


dsock = _establish_connection()
if dsock is None:
    sys.stderr.write(f"c3-stub: cannot reach broker at {SOCK_PATH}; exiting\n")
    sys.exit(0)

dfile = dsock.makefile("rwb", buffering=0)
dlock = threading.Lock()
# Connection generation: bumped on each successful reconnect. Callers capture
# the gen they observed before a failed write/read, so concurrent reconnect
# attempts don't stack — only the first one wins, the rest see a newer gen
# and short-circuit.
_conn_lock = threading.Lock()
_conn_gen = [0]

# Per-op response routing: {resp_key: threading.Event+holder}
PENDING = {}
PENDING_LOCK = threading.Lock()

def dsend(obj):
    """Write to broker; on connection failure, reconnect once and retry."""
    data = (json.dumps(obj) + "\n").encode()
    for attempt in range(2):
        observed_gen = _conn_gen[0]
        with dlock:
            try:
                dfile.write(data)
                return
            except (OSError, BrokenPipeError, ValueError) as e:
                last_err = e
        sys.stderr.write(f"c3-stub: dsend failed ({last_err}); reconnecting\n")
        if not _reconnect(observed_gen):
            return
    sys.stderr.write("c3-stub: dsend failed twice after reconnect; dropping\n")


def daemon_reader():
    """Read from broker; on EOF or error, trigger reconnect (which spawns a
    fresh reader). Each reader thread is bound to one connection generation —
    when its dfile dies, it requests a reconnect for that gen and exits."""
    my_gen = _conn_gen[0]
    local_dfile = dfile  # capture; reconnect may replace the global
    try:
        for raw in local_dfile:
            try:
                msg = json.loads(raw.decode())
            except Exception:
                continue
            op = msg.get("op")
            log(f"broker->stub: op={op}")
            if op == "inbound":
                mcp_send({"jsonrpc": "2.0", "method": msg["method"], "params": msg["params"]})
            elif op in ("tool_result",):
                rid = msg.get("id")
                with PENDING_LOCK:
                    ev = PENDING.pop(rid, None)
                if ev is not None:
                    ev["resp"] = msg
                    ev["event"].set()
            elif op in ("server_info", "tools_list", "attached", "topics_list"):
                # Synchronous responses during setup — use op as key.
                key = op
                with PENDING_LOCK:
                    ev = PENDING.pop(key, None)
                if ev is not None:
                    ev["resp"] = msg
                    ev["event"].set()
            elif op == "error":
                sys.stderr.write(f"c3-stub: broker error: {msg.get('err')}\n")
    except Exception as e:
        log(f"daemon_reader gen={my_gen} exception: {e}")
    log(f"daemon_reader gen={my_gen}: dfile EOF, attempting reconnect")
    _reconnect(my_gen)


def _reconnect(observed_gen: int) -> bool:
    """Tear down the dead connection, open a new one, re-handshake, re-attach.
    `observed_gen` is the gen the caller saw when it noticed the failure — if
    a newer gen exists, someone already reconnected, so we no-op."""
    global dsock, dfile
    with _conn_lock:
        if observed_gen != _conn_gen[0]:
            return True
        sys.stderr.write(f"c3-stub: broker connection lost (gen={observed_gen}); reconnecting\n")
        # Wake any callers blocked on wait_response so the reconnect doesn't deadlock
        # behind their pending entries (their requests went into the void anyway).
        with PENDING_LOCK:
            for k, ev in list(PENDING.items()):
                ev["resp"] = {"err": "broker reconnect"}
                ev["event"].set()
            PENDING.clear()
        try: dfile.close()
        except Exception: pass
        try: dsock.close()
        except Exception: pass
        new_sock = _establish_connection()
        if new_sock is None:
            sys.stderr.write("c3-stub: reconnect failed — broker unreachable\n")
            return False
        dsock = new_sock
        dfile = dsock.makefile("rwb", buffering=0)
        _conn_gen[0] += 1
        new_gen = _conn_gen[0]
        sys.stderr.write(f"c3-stub: reconnected (gen={new_gen}); re-handshaking\n")
        # Spawn a fresh reader for the new dfile. The old reader (which called
        # us) is about to return after this _reconnect, so won't double up.
        threading.Thread(target=daemon_reader, daemon=True).start()
        # Re-handshake server_info — broker won't honor anything else first.
        try:
            with dlock:
                dfile.write((json.dumps({"op": "server_info"}) + "\n").encode())
            wait_response("server_info", timeout=5)
        except Exception as e:
            sys.stderr.write(f"c3-stub: re-handshake server_info failed: {e}\n")
        # Re-claim the topic we held before the disconnect, if any.
        if BOUND.get("name") or BOUND.get("chat_id") is not None:
            try:
                if BOUND.get("name"):
                    payload = {"op": "attach_auto", "name": BOUND["name"]}
                else:
                    payload = {"op": "attach", "chat_id": int(BOUND["chat_id"]),
                               "topic_id": int(BOUND["topic_id"] or 0)}
                with dlock:
                    dfile.write((json.dumps(payload) + "\n").encode())
                wait_response("attached", timeout=5)
            except Exception as e:
                sys.stderr.write(f"c3-stub: re-attach failed: {e}\n")
        return True


def wait_response(key, timeout=30):
    ev = {"event": threading.Event(), "resp": None}
    with PENDING_LOCK:
        PENDING[key] = ev
    if not ev["event"].wait(timeout):
        with PENDING_LOCK:
            PENDING.pop(key, None)
        raise TimeoutError(f"c3-stub: broker timed out on {key}")
    return ev["resp"]


threading.Thread(target=daemon_reader, daemon=True).start()

# ─── Pre-initialize handshake with broker ─────────────────────────────────────

# 1. Fetch server_info (capabilities/instructions to mirror to CC).
dsend({"op": "server_info"})
server_info = wait_response("server_info", timeout=15)
SERVER_INFO  = server_info.get("serverInfo", {"name": "c3-telegram", "version": "0.0.1"})
CAPABILITIES = server_info.get("capabilities", {})
BASE_INSTRUCTIONS = server_info.get("instructions", "")
PROTOCOL_VERSION = server_info.get("protocolVersion", "2024-11-05")

# 2. Claim our topic — or start unattached if no project detected.
BOUND = {"chat_id": None, "topic_id": None, "name": None}


def do_attach(name=None, chat_id=None, topic_id=None):
    """Send attach/attach_auto to broker, update BOUND. Returns (ok, info)."""
    if name is not None:
        dsend({"op": "attach_auto", "name": name})
    else:
        dsend({"op": "attach", "chat_id": int(chat_id), "topic_id": int(topic_id)})
    resp = wait_response("attached", timeout=15)
    if resp.get("ok"):
        BOUND["chat_id"]  = resp["chat_id"]
        BOUND["topic_id"] = resp["topic_id"]
        BOUND["name"]     = resp.get("name")
    return resp.get("ok"), resp


if TOPIC_ID and CHAT_ID:
    ok, resp = do_attach(chat_id=CHAT_ID, topic_id=TOPIC_ID)
    if not ok:
        sys.stderr.write(f"c3-stub: attach failed: {resp.get('err') or 'topic already claimed'}\n")
        sys.exit(1)
elif ATTACH_NAME:
    ok, resp = do_attach(name=ATTACH_NAME)
    if not ok:
        sys.stderr.write(f"c3-stub: attach-by-name '{ATTACH_NAME}' failed ({resp.get('err') or 'taken'}); "
                         f"staying unattached — use the attach tool to pick another topic\n")
else:
    sys.stderr.write("c3-stub: shared root — starting unattached; use attach tool to pick a topic\n")

sys.stderr.write(f"c3-stub: BOUND={BOUND}\n")


def describe_bound() -> str:
    """Human phrase for where messages will route from."""
    if BOUND["chat_id"] is None:
        return "unattached — no Telegram messages will route here yet"
    cid, tid, nm = BOUND["chat_id"], BOUND["topic_id"], BOUND["name"]
    # DM: positive chat_id matching a user.
    if cid > 0 and tid == 0:
        return f"the DM with {nm or f'user {cid}'}"
    # Group + topic.
    if cid < 0 and tid > 0:
        return f"the '{nm}' topic in group {cid}"
    # Group, no topic.
    if cid < 0 and tid == 0:
        return f"group {cid} (no topics)"
    return f"chat {cid} thread {tid} ({nm or 'unnamed'})"


def build_instructions() -> str:
    state = describe_bound()
    attach_help = ""
    if BOUND["chat_id"] is None:
        attach_help = (
            "This terminal is UNATTACHED — Karthi opened the shared root ~/arogara without picking a project. "
            "Wait for him to name one (e.g. 'work on sthapati'):\n"
            "  1. Call the `attach` tool with target='<project-name>'. For the root DM, call attach(target='dm').\n"
            "  2. After attach, read ~/arogara/<project>/CLAUDE.md if it exists — use absolute paths, don't cd.\n"
            "  3. If `attach` returns 'topic held by another terminal', tell Karthi; don't steal it. Offer `topics` to see what's free.\n"
        )
    else:
        attach_help = (
            f"This terminal is ATTACHED to {state}. Inbound messages from that chat/topic route here as "
            f"<channel source='...'> blocks. To switch projects later, call `attach` again — the broker releases "
            "the old claim automatically.\n"
        )
    return BASE_INSTRUCTIONS + "\n\nC3 multiplexer status:\n" + attach_help + \
        "\nUse the `topics` tool to list all known topics and who holds each.\n"


INSTRUCTIONS = build_instructions()

# 3. Fetch tools list.
dsend({"op": "tools_list"})
tools_resp = wait_response("tools_list", timeout=15)
BUN_TOOLS = tools_resp.get("tools", [])

# Stub-local tools — handled here, not forwarded to bun.
LOCAL_TOOLS = [
    {
        "name": "attach",
        "description": "Attach this terminal to a Telegram topic so inbound messages route here. "
                       "Pass target='<topic-name>' (creates the forum topic if missing), or "
                       "target='dm' for the user's personal 1-on-1 chat (resolves via "
                       "dm_chat_id in config.json — only one terminal can own it). "
                       "Re-attaching releases the previous claim automatically.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "target": {"type": "string", "description": "Topic name (e.g. 'sthapati'), or 'dm' for the user's personal 1-on-1 Telegram chat."},
            },
            "required": ["target"],
        },
    },
    {
        "name": "topics",
        "description": "List all known Telegram topics and who's currently claiming each one. "
                       "Use this when the user asks 'what's available?' or to see which terminal owns what.",
        "inputSchema": {"type": "object", "properties": {}},
    },
]

TOOLS = LOCAL_TOOLS + BUN_TOOLS

# ─── MCP stdio loop ───────────────────────────────────────────────────────────

stdout_lock = threading.Lock()
tool_call_counter = [0]
tool_call_lock = threading.Lock()

def mcp_send(obj):
    out = json.dumps(obj)
    log(f"stub->cc: {out[:200]}")
    with stdout_lock:
        sys.stdout.write(out + "\n")
        sys.stdout.flush()


def respond(req_id, result=None, error=None):
    msg = {"jsonrpc": "2.0", "id": req_id}
    if error is not None:
        msg["error"] = error
    else:
        msg["result"] = result
    mcp_send(msg)


def handle_mcp(req):
    method = req.get("method")
    req_id = req.get("id")
    log(f"cc->stub: method={method} id={req_id}")

    if method == "initialize":
        respond(req_id, {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": CAPABILITIES,
            "serverInfo": SERVER_INFO,
            "instructions": INSTRUCTIONS,
        })
        return

    if method == "notifications/initialized":
        return

    if method == "tools/list":
        respond(req_id, {"tools": TOOLS})
        return

    if method == "tools/call":
        params = req.get("params", {})
        name = params.get("name")
        args = params.get("arguments", {})

        # Local tools — handled by the stub, not forwarded to bun.
        if name == "attach":
            target = (args.get("target") or "").strip()
            if not target:
                respond(req_id, error={"code": -32602, "message": "attach: 'target' is required"})
                return
            if target.lower() == "dm":
                if DM_CHAT_ID is None:
                    respond(req_id, {"content": [{"type": "text", "text":
                        "attach failed: 'dm' requires dm_chat_id in c3/mvp/config.json. "
                        "Set it to your personal Telegram user id (positive integer) and restart."}]})
                    return
                ok, resp = do_attach(chat_id=DM_CHAT_ID, topic_id=0)
            else:
                ok, resp = do_attach(name=target)
            if ok:
                text = f"attached to {describe_bound()}. messages from there will now render here as channel blocks."
            else:
                text = f"attach failed: {resp.get('err') or 'topic already claimed by another terminal'}"
            respond(req_id, {"content": [{"type": "text", "text": text}]})
            return

        if name == "topics":
            dsend({"op": "list_topics"})
            resp = wait_response("topics_list", timeout=10)
            topics = resp.get("topics", [])
            if not topics:
                text = "no topics configured yet. use attach with a new name to create one."
            else:
                lines = ["known topics:"]
                for t in topics:
                    claimer = t.get("claimed_by")
                    state = f"claimed by pid {claimer}" if claimer else "free"
                    stale_tag = " [stale group — not in config.json]" if t.get("stale") else ""
                    lines.append(f"  • {t['name']}  (chat {t['chat_id']}, thread {t['topic_id']}) — {state}{stale_tag}")
                text = "\n".join(lines)
            respond(req_id, {"content": [{"type": "text", "text": text}]})
            return

        # Async forward: broker will push tool_result back.
        with tool_call_lock:
            tool_call_counter[0] += 1
            broker_key = f"tc-{tool_call_counter[0]}"
        ev = {"event": threading.Event(), "resp": None}
        with PENDING_LOCK:
            PENDING[broker_key] = ev
        dsend({"op": "tool_call", "id": broker_key, "name": name, "args": args})

        def waiter():
            if not ev["event"].wait(120):
                respond(req_id, error={"code": -32000, "message": f"{name}: timed out"})
                return
            resp = ev["resp"]
            if resp.get("error"):
                respond(req_id, error=resp["error"])
            else:
                respond(req_id, resp.get("result") or {"content": []})
        threading.Thread(target=waiter, daemon=True).start()
        return

    if method == "ping":
        respond(req_id, {})
        return

    if req_id is not None:
        respond(req_id, error={"code": -32601, "message": f"method not found: {method}"})


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
    except Exception:
        continue
    try:
        handle_mcp(req)
    except Exception as e:
        sys.stderr.write(f"c3-stub: handler error: {e}\n")
