import sys
import time
import os
from pathlib import Path


MVP_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(MVP_DIR))

import codex_stub


class FakeBrokerClient:
    def __init__(self):
        self.requests = []
        self.inbox = []
        self.claimed_topic_pid = None

    def server_info(self):
        self.requests.append({"op": "server_info"})
        return {
            "op": "server_info",
            "serverInfo": {"name": "c3-codex-test", "version": "0.0.1"},
            "capabilities": {"tools": {}},
            "instructions": "broker instructions",
            "protocolVersion": "2024-11-05",
        }

    def push(self, msg: dict):
        self.inbox.append(msg)

    def attach_auto(self, name: str):
        self.requests.append({"op": "attach_auto", "name": name})
        return {"op": "attached", "ok": True, "chat_id": -1001, "topic_id": 42, "name": name}

    def list_topics(self):
        self.requests.append({"op": "list_topics"})
        return {
            "op": "topics_list",
            "topics": [
                {"chat_id": -1001, "topic_id": 42, "name": "c3", "claimed_by": self.claimed_topic_pid},
                {"chat_id": -1001, "topic_id": 43, "name": "busy", "claimed_by": 1234},
            ],
        }

    def tool_call(self, name: str, args: dict, timeout: float = 120.0):
        self.requests.append({"op": "tool_call", "name": name, "args": args})
        return {"op": "tool_result", "result": {"content": [{"type": "text", "text": "sent"}]}}

    def drain_inbox(self, limit: int, ack: bool = True):
        if ack:
            out = self.inbox[:limit]
            del self.inbox[:limit]
            return out
        return self.inbox[:limit]

    def inbox_size(self):
        return len(self.inbox)


def make_server(tmp_path):
    broker = FakeBrokerClient()
    server = codex_stub.CodexMCPServer(broker)
    server.start()
    return broker, server


def call(server, req_id, name, arguments=None):
    return server.handle_request({
        "jsonrpc": "2.0",
        "id": req_id,
        "method": "tools/call",
        "params": {"name": name, "arguments": arguments or {}},
    })


def text_content(resp):
    return resp["result"]["content"][0]["text"]


def test_initialize_and_tools_list_expose_codex_c3_tools(tmp_path):
    broker, server = make_server(tmp_path)
    init = server.handle_request({"jsonrpc": "2.0", "id": 1, "method": "initialize"})
    assert init["result"]["serverInfo"]["name"] == "c3-codex"
    assert "C3 Codex bridge" in init["result"]["instructions"]

    tools = server.handle_request({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    names = {tool["name"] for tool in tools["result"]["tools"]}
    assert {"c3_attach", "c3_topics", "c3_inbox", "c3_reply", "c3_codex_forward"} <= names


def test_attach_topics_and_reply_proxy_to_broker(tmp_path):
    broker, server = make_server(tmp_path)
    attached = call(server, 1, "c3_attach", {"target": "c3"})
    assert "attached to c3" in text_content(attached)

    topics = call(server, 2, "c3_topics")
    assert "c3 (chat -1001, thread 42) - free" in text_content(topics)
    assert "busy (chat -1001, thread 43) - claimed by pid 1234" in text_content(topics)

    reply = call(server, 3, "c3_reply", {"text": "hello from codex"})
    assert text_content(reply) == "sent"
    tool_calls = [r for r in broker.requests if r.get("op") == "tool_call"]
    assert tool_calls[-1]["name"] == "reply"
    assert tool_calls[-1]["args"]["text"] == "hello from codex"


def test_start_auto_attaches_from_environment(tmp_path, monkeypatch):
    monkeypatch.setenv("C3_ATTACH_NAME", "sthapati")
    broker = FakeBrokerClient()
    server = codex_stub.CodexMCPServer(broker)

    server.start()

    assert server.bound["name"] == "sthapati"
    assert {"op": "attach_auto", "name": "sthapati"} in broker.requests


def test_inbound_notifications_are_buffered_until_inbox_reads_them(tmp_path):
    broker, server = make_server(tmp_path)
    broker.push({
        "op": "inbound",
        "method": "notifications/claude/channel",
        "params": {
            "content": [{"type": "text", "text": "voice transcript"}],
            "meta": {"chat_id": "-1001", "message_thread_id": "42", "sender": "karthi"},
        },
    })

    deadline = time.time() + 2
    while not server.inbox_size() and time.time() < deadline:
        time.sleep(0.01)

    inbox = call(server, 1, "c3_inbox", {"limit": 10})
    body = text_content(inbox)
    assert "voice transcript" in body
    assert "chat=-1001 thread=42" in body
    assert server.inbox_size() == 0


def test_inbound_notifications_can_be_forwarded_as_mcp_log_messages(tmp_path):
    broker, server = make_server(tmp_path)
    forwarded = []
    server.set_inbound_notifier(forwarded.append)

    server.handle_inbound({
        "op": "inbound",
        "method": "notifications/claude/channel",
        "params": {
            "content": {"type": "text", "text": "show me automatically"},
            "meta": {"chat_id": "-1001", "message_thread_id": "42", "user": "skarthi"},
        },
    })

    assert len(forwarded) == 1
    note = forwarded[0]
    assert note["jsonrpc"] == "2.0"
    assert note["method"] == "notifications/message"
    assert note["params"]["level"] == "info"
    assert note["params"]["logger"] == "c3"
    assert "show me automatically" in note["params"]["data"]
    assert "chat=-1001 thread=42 sender=skarthi" in note["params"]["data"]


def test_app_server_forwarder_turn_starts_inbound_text(tmp_path):
    sent = []

    class FakeWebSocket:
        def __init__(self):
            self.responses = [
                {"id": 1, "result": {"ok": True}},
                {"method": "thread/status/changed", "params": {"threadId": "thread-1"}},
                {"id": 2, "result": {"thread": {"id": "thread-1"}}},
                {"id": 3, "result": {"turn": {"id": "turn-1"}}},
            ]

        def send(self, payload):
            sent.append(payload)

        def recv(self):
            return sent_json(self.responses.pop(0))

        def close(self):
            pass

    def sent_json(obj):
        import json
        return json.dumps(obj)

    forwarder = codex_stub.CodexAppServerForwarder(
        ws_url="ws://127.0.0.1:8766",
        thread_id="thread-1",
        connector=lambda _url, _timeout: FakeWebSocket(),
    )

    forwarder.forward_text("Telegram message from skarthi\nhi")

    decoded = [codex_stub.json.loads(item) for item in sent]
    assert [item["method"] for item in decoded if "id" in item] == [
        "initialize",
        "thread/resume",
        "turn/start",
    ]
    assert decoded[1]["method"] == "initialized"
    turn_start = decoded[-1]
    assert turn_start["params"]["threadId"] == "thread-1"
    assert turn_start["params"]["input"] == [
        {"type": "text", "text": "Telegram message from skarthi\nhi", "text_elements": []}
    ]


def test_app_server_forwarder_discovers_loaded_thread_when_missing(tmp_path):
    sent = []

    class FakeWebSocket:
        def __init__(self):
            self.responses = [
                {"id": 1, "result": {"ok": True}},
                {"id": 2, "result": {"data": ["thread-auto"]}},
                {"id": 3, "result": {"thread": {"id": "thread-auto"}}},
                {"id": 4, "result": {"turn": {"id": "turn-1"}}},
            ]

        def send(self, payload):
            sent.append(payload)

        def recv(self):
            return sent_json(self.responses.pop(0))

        def close(self):
            pass

    def sent_json(obj):
        import json
        return json.dumps(obj)

    forwarder = codex_stub.CodexAppServerForwarder(
        ws_url="ws://127.0.0.1:8766",
        thread_id=None,
        connector=lambda _url, _timeout: FakeWebSocket(),
    )

    forwarder.forward_text("Telegram message")

    decoded = [codex_stub.json.loads(item) for item in sent]
    assert [item["method"] for item in decoded if "id" in item] == [
        "initialize",
        "thread/loaded/list",
        "thread/resume",
        "turn/start",
    ]
    assert forwarder.thread_id == "thread-auto"


def test_server_forwards_inbound_to_app_server_when_configured(tmp_path):
    broker, _ = make_server(tmp_path)

    class FakeForwarder:
        def __init__(self):
            self.forwarded = []

        def enabled(self):
            return True

        def forward_text(self, text):
            self.forwarded.append(text)
            return True

    forwarder = FakeForwarder()
    server = codex_stub.CodexMCPServer(broker, app_forwarder=forwarder)

    server.handle_inbound({
        "op": "inbound",
        "method": "notifications/claude/channel",
        "params": {
            "content": [{"type": "text", "text": "please fix it"}],
            "meta": {"chat_id": "-1001", "message_thread_id": "42", "sender": "skarthi"},
        },
    })

    deadline = time.time() + 1
    while not forwarder.forwarded and time.time() < deadline:
        time.sleep(0.01)

    assert len(forwarder.forwarded) == 1
    assert "Telegram message from skarthi" in forwarder.forwarded[0]
    assert "chat=-1001 thread=42" in forwarder.forwarded[0]
    assert "please fix it" in forwarder.forwarded[0]


def test_server_retries_app_server_forwarding_after_failure(tmp_path):
    broker, _ = make_server(tmp_path)

    class FlakyForwarder:
        def __init__(self):
            self.calls = 0

        def enabled(self):
            return True

        def forward_text(self, text):
            self.calls += 1
            return self.calls >= 2

    forwarder = FlakyForwarder()
    server = codex_stub.CodexMCPServer(broker, app_forwarder=forwarder)
    server.forward_retry_delay = 0.01

    server.handle_inbound({
        "op": "inbound",
        "method": "notifications/claude/channel",
        "params": {
            "content": [{"type": "text", "text": "retry me"}],
            "meta": {"chat_id": "-1001", "message_thread_id": "42", "sender": "skarthi"},
        },
    })

    deadline = time.time() + 1
    while forwarder.calls < 2 and time.time() < deadline:
        time.sleep(0.01)

    assert forwarder.calls >= 2


def test_codex_forward_tool_configures_app_server_forwarding(tmp_path, monkeypatch):
    broker, server = make_server(tmp_path)
    monkeypatch.setenv("C3_CODEX_ALLOW_MANUAL_FORWARD", "1")

    resp = call(server, 1, "c3_codex_forward", {
        "app_server_ws": "ws://127.0.0.1:8766",
        "thread_id": "thread-abc",
    })

    assert "codex app-server forwarding enabled" in text_content(resp)
    assert server.app_forwarder.ws_url == "ws://127.0.0.1:8766"
    assert server.app_forwarder.thread_id == "thread-abc"


def test_codex_forward_tool_rejects_non_remote_split_brain(tmp_path, monkeypatch):
    monkeypatch.delenv("C3_CODEX_ALLOW_MANUAL_FORWARD", raising=False)
    monkeypatch.delenv("C3_CODEX_REMOTE_BRIDGE", raising=False)
    broker, server = make_server(tmp_path)

    resp = call(server, 1, "c3_codex_forward", {
        "app_server_ws": "ws://127.0.0.1:8766",
        "thread_id": "thread-abc",
    })

    assert resp["error"]["code"] == -32602
    assert "hidden app-server copy" in resp["error"]["message"]


def test_reply_requires_an_attached_topic(tmp_path):
    broker, server = make_server(tmp_path)
    resp = call(server, 1, "c3_reply", {"text": "hello"})
    assert resp["error"]["code"] == -32000
    assert "attach first" in resp["error"]["message"]


def test_reply_recovers_bound_topic_from_existing_broker_claim(tmp_path):
    broker, server = make_server(tmp_path)
    broker.claimed_topic_pid = os.getpid()

    reply = call(server, 1, "c3_reply", {"text": "hello after reload"})

    assert text_content(reply) == "sent"
    tool_calls = [r for r in broker.requests if r.get("op") == "tool_call"]
    assert tool_calls[-1]["name"] == "reply"
    assert tool_calls[-1]["args"]["chat_id"] == "-1001"
    assert tool_calls[-1]["args"]["text"] == "hello after reload"
