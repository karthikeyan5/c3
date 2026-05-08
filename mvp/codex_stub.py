#!/usr/bin/env python3
"""C3 Codex MCP stub.

This is the Codex-facing bridge for the existing C3 broker. It deliberately
does not reuse Claude Code's plugin wrapper or `notifications/claude/channel`
rendering path. Inbound Telegram messages are emitted as standard MCP log
notifications and also buffered for the `c3_inbox` fallback tool.
"""
from __future__ import annotations

import json
import os
import socket
import sys
import threading
import time
from collections import deque
from typing import Any, Callable


DEFAULT_SOCK_PATH = os.environ.get("C3_SOCK", "/tmp/c3.sock")
LOG_FILE = os.environ.get("C3_CODEX_LOG", "/tmp/c3-codex-stub.log")
REMOTE_BRIDGE_ENV = "C3_CODEX_REMOTE_BRIDGE"
ALLOW_MANUAL_FORWARD_ENV = "C3_CODEX_ALLOW_MANUAL_FORWARD"


def log(msg: str) -> None:
    try:
        with open(LOG_FILE, "a") as f:
            f.write(f"{time.strftime('%H:%M:%S')} [pid={os.getpid()}] {msg}\n")
    except Exception:
        pass


class BrokerError(RuntimeError):
    pass


class CodexAppServerError(RuntimeError):
    pass


class CodexAppServerForwarder:
    """Submit inbound Telegram text to a Codex app-server thread."""

    def __init__(
        self,
        ws_url: str | None,
        thread_id: str | None,
        connector: Callable[[str, float], Any] | None = None,
        request_timeout: float = 15.0,
    ):
        self.ws_url = (ws_url or "").strip()
        self.thread_id = (thread_id or "").strip()
        self.connector = connector
        self.request_timeout = request_timeout
        self.next_id = 0

    @classmethod
    def from_env(cls) -> "CodexAppServerForwarder":
        if not codex_app_forwarding_allowed():
            return cls(ws_url=None, thread_id=None)
        return cls(
            ws_url=os.environ.get("C3_CODEX_APP_SERVER_WS"),
            thread_id=os.environ.get("C3_CODEX_THREAD_ID") or os.environ.get("CODEX_THREAD_ID"),
        )

    def enabled(self) -> bool:
        return bool(self.ws_url)

    def forward_text(self, text: str) -> bool:
        if not self.enabled():
            return False
        ws = None
        try:
            ws = self._connect()
            self._request(ws, "initialize", {
                "clientInfo": {
                    "name": "c3-codex-bridge",
                    "title": "C3 Codex bridge",
                    "version": "0.1.0",
                },
                "capabilities": {
                    "experimentalApi": True,
                    "optOutNotificationMethods": [
                        "item/agentMessage/delta",
                        "item/reasoning/textDelta",
                        "item/reasoning/summaryTextDelta",
                    ],
                },
            })
            self._notify(ws, "initialized")
            if not self.thread_id:
                self.thread_id = self._discover_thread(ws)
            if not self.thread_id:
                raise CodexAppServerError("no loaded Codex thread found")
            self._request(ws, "thread/resume", {
                "threadId": self.thread_id,
                "excludeTurns": True,
            })
            self._request(ws, "turn/start", {
                "threadId": self.thread_id,
                "input": [{"type": "text", "text": text, "text_elements": []}],
            })
            log(f"forwarded inbound to codex app-server thread={self.thread_id}")
            return True
        except Exception as e:
            log(f"codex app-server forward failed: {e}")
            return False
        finally:
            if ws is not None:
                try:
                    ws.close()
                except Exception:
                    pass

    def _connect(self):
        if self.connector is not None:
            return self.connector(self.ws_url, self.request_timeout)
        try:
            import websocket
        except Exception as e:
            raise CodexAppServerError(f"python websocket-client is unavailable: {e}")
        return websocket.create_connection(self.ws_url, timeout=self.request_timeout, suppress_origin=True)

    def _discover_thread(self, ws) -> str | None:
        loaded = self._request(ws, "thread/loaded/list", {"limit": 20}).get("data", [])
        if not loaded:
            return None
        if len(loaded) == 1:
            return str(loaded[0])
        cwd = os.environ.get("C3_CODEX_CWD") or os.getcwd()
        listed = self._request(ws, "thread/list", {
            "limit": 50,
            "sortKey": "updated_at",
            "sortDirection": "desc",
            "cwd": cwd,
            "useStateDbOnly": True,
        }).get("data", [])
        loaded_ids = {str(item) for item in loaded}
        for thread in listed:
            if str(thread.get("id")) in loaded_ids:
                return str(thread["id"])
        return str(loaded[0])

    def _request(self, ws, method: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
        self.next_id += 1
        rid = self.next_id
        msg: dict[str, Any] = {"id": rid, "method": method}
        if params is not None:
            msg["params"] = params
        ws.send(json.dumps(msg))
        deadline = time.time() + self.request_timeout
        while time.time() < deadline:
            raw = ws.recv()
            resp = json.loads(raw)
            if resp.get("id") != rid:
                if "id" in resp and "method" in resp:
                    self._server_request_unsupported(ws, resp)
                continue
            if resp.get("error"):
                raise CodexAppServerError(f"{method}: {resp['error']}")
            return resp.get("result") or {}
        raise CodexAppServerError(f"{method}: timed out waiting for response")

    def _notify(self, ws, method: str, params: dict[str, Any] | None = None) -> None:
        msg: dict[str, Any] = {"method": method}
        if params is not None:
            msg["params"] = params
        ws.send(json.dumps(msg))

    def _server_request_unsupported(self, ws, req: dict[str, Any]) -> None:
        ws.send(json.dumps({
            "id": req["id"],
            "error": {
                "code": -32601,
                "message": f"c3 bridge does not handle app-server request: {req.get('method')}",
            },
        }))


def codex_app_forwarding_allowed() -> bool:
    return (
        os.environ.get(REMOTE_BRIDGE_ENV) == "1"
        or os.environ.get(ALLOW_MANUAL_FORWARD_ENV) == "1"
    )


class BrokerClient:
    """Small newline-delimited JSON client for `broker.py`'s unix socket."""

    def __init__(self, sock_path: str = DEFAULT_SOCK_PATH, connect_timeout: float = 0.5):
        self.sock_path = sock_path
        self.connect_timeout = connect_timeout
        self.sock: socket.socket | None = None
        self.file = None
        self.write_lock = threading.Lock()
        self.pending: dict[str, dict[str, Any]] = {}
        self.pending_lock = threading.Lock()
        self.inbox: deque[dict[str, Any]] = deque()
        self.inbox_lock = threading.Lock()
        self.reader_started = False
        self.next_id = 0
        self.last_error: str | None = None
        self.inbound_callback = None

    def connect(self) -> bool:
        if self.sock is not None:
            return True
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(self.connect_timeout)
        try:
            s.connect(self.sock_path)
            s.settimeout(None)
        except Exception as e:
            s.close()
            self.last_error = f"cannot reach C3 broker at {self.sock_path}: {e}"
            return False
        self.sock = s
        self.file = s.makefile("rwb", buffering=0)
        if not self.reader_started:
            self.reader_started = True
            threading.Thread(target=self._reader, daemon=True).start()
        self.last_error = None
        return True

    def is_connected(self) -> bool:
        return self.sock is not None and self.file is not None

    def _reader(self) -> None:
        while True:
            local_file = self.file
            if local_file is None:
                return
            try:
                for raw in local_file:
                    try:
                        msg = json.loads(raw.decode())
                    except Exception:
                        continue
                    self._dispatch(msg)
            except Exception as e:
                log(f"broker reader failed: {e}")
            self.last_error = "broker connection closed"
            self._wake_all_pending({"err": self.last_error})
            return

    def _dispatch(self, msg: dict[str, Any]) -> None:
        op = msg.get("op")
        log(f"broker->codex op={op}")
        if op == "inbound":
            with self.inbox_lock:
                self.inbox.append(msg)
            if self.inbound_callback is not None:
                try:
                    self.inbound_callback(msg)
                except Exception as e:
                    log(f"inbound callback failed: {e}")
            return
        key = str(msg.get("id") or op or "")
        if not key:
            return
        with self.pending_lock:
            pending = self.pending.pop(key, None)
        if pending is not None:
            pending["resp"] = msg
            pending["event"].set()

    def _wake_all_pending(self, resp: dict[str, Any]) -> None:
        with self.pending_lock:
            items = list(self.pending.values())
            self.pending.clear()
        for pending in items:
            pending["resp"] = resp
            pending["event"].set()

    def _send(self, obj: dict[str, Any]) -> None:
        if not self.connect():
            raise BrokerError(self.last_error or "broker unavailable")
        assert self.file is not None
        data = (json.dumps(obj) + "\n").encode()
        try:
            with self.write_lock:
                self.file.write(data)
        except Exception as e:
            self.last_error = f"broker write failed: {e}"
            raise BrokerError(self.last_error)

    def request(self, req: dict[str, Any], response_key: str, timeout: float = 15.0) -> dict[str, Any]:
        pending = {"event": threading.Event(), "resp": None}
        with self.pending_lock:
            self.pending[response_key] = pending
        try:
            self._send(req)
        except Exception:
            with self.pending_lock:
                self.pending.pop(response_key, None)
            raise
        if not pending["event"].wait(timeout):
            with self.pending_lock:
                self.pending.pop(response_key, None)
            raise BrokerError(f"broker timed out waiting for {response_key}")
        resp = pending["resp"] or {}
        if resp.get("err"):
            raise BrokerError(str(resp["err"]))
        return resp

    def server_info(self) -> dict[str, Any]:
        return self.request({"op": "server_info"}, "server_info")

    def attach_auto(self, name: str) -> dict[str, Any]:
        return self.request({"op": "attach_auto", "name": name}, "attached")

    def list_topics(self) -> dict[str, Any]:
        return self.request({"op": "list_topics"}, "topics_list")

    def tool_call(self, name: str, args: dict[str, Any], timeout: float = 120.0) -> dict[str, Any]:
        self.next_id += 1
        rid = f"codex-{self.next_id}"
        req = {"op": "tool_call", "id": rid, "name": name, "args": args}
        return self.request(req, rid, timeout=timeout)

    def drain_inbox(self, limit: int, ack: bool = True) -> list[dict[str, Any]]:
        with self.inbox_lock:
            if ack:
                out = []
                for _ in range(min(limit, len(self.inbox))):
                    out.append(self.inbox.popleft())
                return out
            return list(self.inbox)[:limit]

    def inbox_size(self) -> int:
        with self.inbox_lock:
            return len(self.inbox)


TOOLS = [
    {
        "name": "c3_attach",
        "description": "Attach this Codex session to a C3 Telegram topic by name.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "target": {"type": "string", "description": "Topic name, for example 'c3' or 'sthapati'."},
            },
            "required": ["target"],
        },
    },
    {
        "name": "c3_topics",
        "description": "List known C3 Telegram topics and which terminal currently claims each one.",
        "inputSchema": {"type": "object", "properties": {}},
    },
    {
        "name": "c3_inbox",
        "description": "Read buffered inbound Telegram messages for this Codex session.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "limit": {"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
                "ack": {"type": "boolean", "default": True, "description": "When true, remove returned messages from the buffer."},
            },
        },
    },
    {
        "name": "c3_reply",
        "description": "Send a Telegram reply through the attached C3 topic.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "text": {"type": "string", "description": "Reply text to send."},
                "parse_mode": {"type": "string", "description": "Optional Telegram parse mode, forwarded to the upstream reply tool."},
                "files": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": "Optional file paths supported by the upstream Telegram reply tool.",
                },
            },
            "required": ["text"],
        },
    },
    {
        "name": "c3_codex_forward",
        "description": "Configure Codex app-server forwarding so inbound Telegram messages start turns in this Codex thread.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "app_server_ws": {
                    "type": "string",
                    "description": "Codex app-server websocket URL, for example ws://127.0.0.1:8766.",
                },
                "thread_id": {
                    "type": "string",
                    "description": "Codex thread id. Defaults to C3_CODEX_THREAD_ID or CODEX_THREAD_ID when available.",
                },
            },
            "required": ["app_server_ws"],
        },
    },
]


class CodexMCPServer:
    def __init__(self, broker: BrokerClient, app_forwarder: CodexAppServerForwarder | None = None):
        self.broker = broker
        self.bound: dict[str, Any] = {"chat_id": None, "topic_id": None, "name": None}
        self.broker_info: dict[str, Any] = {}
        self.inbound_notifier = None
        self.app_forwarder = app_forwarder if app_forwarder is not None else CodexAppServerForwarder.from_env()
        self.forward_queue: deque[str] = deque()
        self.forward_lock = threading.Lock()
        self.forward_thread_started = False
        self.forward_retry_delay = 2.0
        if hasattr(self.broker, "inbound_callback"):
            self.broker.inbound_callback = self.handle_inbound

    def start(self) -> None:
        try:
            self.broker_info = self.broker.server_info()
        except BrokerError as e:
            self.broker_info = {"error": str(e)}
            return
        target = str(os.environ.get("C3_ATTACH_NAME") or "").strip()
        if target:
            try:
                resp = self.broker.attach_auto(target)
                if resp.get("ok"):
                    self.bound.update({
                        "chat_id": resp.get("chat_id"),
                        "topic_id": resp.get("topic_id"),
                        "name": resp.get("name") or target,
                    })
                    log(f"auto-attached to {self.bound['name']}")
                else:
                    log(f"auto-attach failed: {resp.get('err') or 'topic unavailable'}")
            except Exception as e:
                log(f"auto-attach failed: {e}")

    def inbox_size(self) -> int:
        return self.broker.inbox_size()

    def set_inbound_notifier(self, notifier) -> None:
        self.inbound_notifier = notifier

    def handle_inbound(self, msg: dict[str, Any]) -> None:
        if self.inbound_notifier is None:
            pass
        else:
            self.inbound_notifier(self._inbound_log_notification(msg))
        if self.app_forwarder is not None and self.app_forwarder.enabled():
            self._queue_forward(self._inbound_turn_text(msg))

    def _queue_forward(self, text: str) -> None:
        with self.forward_lock:
            self.forward_queue.append(text)
            if not self.forward_thread_started:
                self.forward_thread_started = True
                threading.Thread(target=self._forward_worker, daemon=True).start()

    def _forward_worker(self) -> None:
        while True:
            with self.forward_lock:
                text = self.forward_queue[0] if self.forward_queue else None
            if text is None:
                time.sleep(self.forward_retry_delay)
                continue
            if self.app_forwarder is None or not self.app_forwarder.enabled():
                time.sleep(self.forward_retry_delay)
                continue
            ok = self.app_forwarder.forward_text(text)
            if ok:
                with self.forward_lock:
                    if self.forward_queue and self.forward_queue[0] == text:
                        self.forward_queue.popleft()
                continue
            time.sleep(self.forward_retry_delay)

    def handle_request(self, req: dict[str, Any]) -> dict[str, Any] | None:
        method = req.get("method")
        req_id = req.get("id")
        if method == "initialize":
            return self._response(req_id, self._initialize_result())
        if method == "notifications/initialized":
            return None
        if method == "tools/list":
            return self._response(req_id, {"tools": TOOLS})
        if method == "tools/call":
            params = req.get("params", {})
            name = params.get("name")
            args = params.get("arguments", {}) or {}
            try:
                return self._response(req_id, self._call_tool(name, args))
            except BrokerError as e:
                return self._error(req_id, -32000, str(e))
            except ValueError as e:
                return self._error(req_id, -32602, str(e))
        if method == "ping":
            return self._response(req_id, {})
        if req_id is not None:
            return self._error(req_id, -32601, f"method not found: {method}")
        return None

    def _initialize_result(self) -> dict[str, Any]:
        broker_state = "connected"
        if self.broker_info.get("error"):
            broker_state = self.broker_info["error"]
        instructions = (
            "C3 Codex bridge. Use c3_attach(target='<topic>') before c3_reply. "
            "Telegram inbound messages are emitted as MCP log notifications and also buffered for c3_inbox. "
            "Use c3_codex_forward(app_server_ws='<ws-url>', thread_id='<thread-id>') in Codex app-server mode "
            "to submit inbound Telegram text as Codex turns. "
            f"Broker status: {broker_state}."
        )
        return {
            "protocolVersion": self.broker_info.get("protocolVersion", "2024-11-05"),
            "capabilities": {"tools": {}, "logging": {}},
            "serverInfo": {"name": "c3-codex", "version": "0.1.0"},
            "instructions": instructions,
        }

    def _call_tool(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        if name == "c3_attach":
            return self._tool_attach(args)
        if name == "c3_topics":
            return self._tool_topics()
        if name == "c3_inbox":
            return self._tool_inbox(args)
        if name == "c3_reply":
            return self._tool_reply(args)
        if name == "c3_codex_forward":
            return self._tool_codex_forward(args)
        raise ValueError(f"unknown tool: {name}")

    def _tool_attach(self, args: dict[str, Any]) -> dict[str, Any]:
        target = str(args.get("target") or "").strip()
        if not target:
            raise ValueError("c3_attach: target is required")
        resp = self.broker.attach_auto(target)
        if not resp.get("ok"):
            if self._recover_bound_from_claims(target):
                return self._text(
                    f"attached to {self.bound['name']} "
                    f"(chat {self.bound['chat_id']}, thread {self.bound['topic_id']})"
                )
            raise BrokerError(resp.get("err") or "topic already claimed by another terminal")
        self.bound.update({
            "chat_id": resp.get("chat_id"),
            "topic_id": resp.get("topic_id"),
            "name": resp.get("name") or target,
        })
        return self._text(
            f"attached to {self.bound['name']} "
            f"(chat {self.bound['chat_id']}, thread {self.bound['topic_id']})"
        )

    def _recover_bound_from_claims(self, target: str | None = None) -> bool:
        try:
            topics = self.broker.list_topics().get("topics", [])
        except Exception as e:
            log(f"bound recovery failed: {e}")
            return False
        pid = str(os.getpid())
        for topic in topics:
            if str(topic.get("claimed_by") or "") != pid:
                continue
            if target is not None and str(topic.get("name") or "") != target:
                continue
            self.bound.update({
                "chat_id": topic.get("chat_id"),
                "topic_id": topic.get("topic_id"),
                "name": topic.get("name") or target,
            })
            log(f"recovered attached topic {self.bound['name']} from broker claim")
            return self.bound["chat_id"] is not None
        return False

    def _tool_topics(self) -> dict[str, Any]:
        resp = self.broker.list_topics()
        topics = resp.get("topics", [])
        if not topics:
            return self._text("no C3 topics configured")
        lines = ["known C3 topics:"]
        for t in topics:
            claimer = t.get("claimed_by")
            state = f"claimed by pid {claimer}" if claimer else "free"
            stale = " [stale group]" if t.get("stale") else ""
            lines.append(f"{t['name']} (chat {t['chat_id']}, thread {t['topic_id']}) - {state}{stale}")
        return self._text("\n".join(lines))

    def _tool_inbox(self, args: dict[str, Any]) -> dict[str, Any]:
        limit = int(args.get("limit") or 10)
        limit = max(1, min(50, limit))
        ack = bool(args.get("ack", True))
        messages = self.broker.drain_inbox(limit, ack=ack)
        if not messages:
            return self._text("c3 inbox is empty")
        chunks = []
        for i, msg in enumerate(messages, start=1):
            params = msg.get("params", {})
            meta = params.get("meta", {})
            text = self._extract_text(params)
            chat = meta.get("chat_id", "?")
            thread = meta.get("message_thread_id", "0")
            sender = meta.get("sender") or meta.get("from") or meta.get("user") or "unknown"
            chunks.append(f"[{i}] chat={chat} thread={thread} sender={sender}\n{text}")
        return self._text("\n\n".join(chunks))

    def _tool_reply(self, args: dict[str, Any]) -> dict[str, Any]:
        if self.bound["chat_id"] is None:
            if not self._recover_bound_from_claims():
                raise BrokerError("attach first with c3_attach(target='<topic>')")
        text = str(args.get("text") or "")
        if not text.strip():
            raise ValueError("c3_reply: text is required")
        upstream_args = {"chat_id": str(self.bound["chat_id"]), "text": text}
        if args.get("parse_mode"):
            upstream_args["parse_mode"] = args["parse_mode"]
        if args.get("files"):
            upstream_args["files"] = args["files"]
        resp = self.broker.tool_call("reply", upstream_args)
        if resp.get("error"):
            raise BrokerError(str(resp["error"]))
        return resp.get("result") or self._text("sent")

    def _tool_codex_forward(self, args: dict[str, Any]) -> dict[str, Any]:
        if not codex_app_forwarding_allowed():
            raise ValueError(
                "c3_codex_forward is only safe in a Codex session launched by the C3 wrapper. "
                "Restart with `codex` or `codex resume ...` so the visible TUI is connected to "
                "the app-server; otherwise Telegram turns run in a hidden app-server copy."
            )
        ws_url = str(args.get("app_server_ws") or os.environ.get("C3_CODEX_APP_SERVER_WS") or "").strip()
        thread_id = str(
            args.get("thread_id")
            or os.environ.get("C3_CODEX_THREAD_ID")
            or os.environ.get("CODEX_THREAD_ID")
            or ""
        ).strip()
        if not ws_url:
            raise ValueError("c3_codex_forward: app_server_ws is required")
        if not thread_id:
            raise ValueError("c3_codex_forward: thread_id is required when CODEX_THREAD_ID is unavailable")
        self.app_forwarder = CodexAppServerForwarder(ws_url=ws_url, thread_id=thread_id)
        return self._text(f"codex app-server forwarding enabled for thread {thread_id} via {ws_url}")

    def _extract_text(self, params: dict[str, Any]) -> str:
        content = params.get("content")
        if isinstance(content, list):
            parts = []
            for item in content:
                if isinstance(item, dict) and item.get("type") == "text":
                    parts.append(str(item.get("text") or ""))
            if parts:
                return "\n".join(parts)
        if isinstance(content, dict) and content.get("type") == "text":
            return str(content.get("text") or "")
        if isinstance(content, str):
            return content
        if isinstance(params.get("text"), str):
            return params["text"]
        return json.dumps(params, ensure_ascii=False)

    def _inbound_log_notification(self, msg: dict[str, Any]) -> dict[str, Any]:
        params = msg.get("params", {})
        meta = params.get("meta", {})
        text = self._extract_text(params)
        chat = meta.get("chat_id", "?")
        thread = meta.get("message_thread_id", "0")
        sender = meta.get("sender") or meta.get("user") or meta.get("from") or "unknown"
        return {
            "jsonrpc": "2.0",
            "method": "notifications/message",
            "params": {
                "level": "info",
                "logger": "c3",
                "data": f"Telegram message chat={chat} thread={thread} sender={sender}\n{text}",
            },
        }

    def _inbound_turn_text(self, msg: dict[str, Any]) -> str:
        params = msg.get("params", {})
        meta = params.get("meta", {})
        text = self._extract_text(params)
        chat = meta.get("chat_id", "?")
        thread = meta.get("message_thread_id", "0")
        sender = meta.get("sender") or meta.get("user") or meta.get("from") or "unknown"
        return f"Telegram message from {sender} (chat={chat} thread={thread})\n{text}"

    def _response(self, req_id: Any, result: dict[str, Any]) -> dict[str, Any]:
        return {"jsonrpc": "2.0", "id": req_id, "result": result}

    def _error(self, req_id: Any, code: int, message: str) -> dict[str, Any]:
        return {"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}}

    def _text(self, text: str) -> dict[str, Any]:
        return {"content": [{"type": "text", "text": text}]}


def stdio_loop(server: CodexMCPServer) -> None:
    stdout_lock = threading.Lock()

    def send(obj: dict[str, Any]) -> None:
        with stdout_lock:
            sys.stdout.write(json.dumps(obj) + "\n")
            sys.stdout.flush()

    server.set_inbound_notifier(send)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception:
            continue
        try:
            resp = server.handle_request(req)
        except Exception as e:
            resp = {"jsonrpc": "2.0", "id": req.get("id"), "error": {"code": -32000, "message": str(e)}}
        if resp is not None:
            send(resp)


def main() -> int:
    log("=== codex stub started ===")
    broker = BrokerClient(DEFAULT_SOCK_PATH)
    server = CodexMCPServer(broker)
    server.start()
    stdio_loop(server)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
