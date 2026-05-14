package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mcp"
)

// TestServerInfoName guards the lesson from 2026-05-09: serverInfo.name MUST
// match the .mcp.json key ("c3") so Claude Code's channel dispatch can route
// notifications/claude/channel frames from this server. Both reference
// implementations (~/.claude/plugins/.../fakechat/server.ts:60 and
// .../telegram/0.0.6/server.ts:371) follow this convention.
func TestServerInfoName(t *testing.T) {
	if adapterName != "c3" {
		t.Fatalf("adapterName must be %q to match .mcp.json key; got %q", "c3", adapterName)
	}
	a := newAdapter()
	resp := a.initializeResponse(&mcp.Request{ID: json.RawMessage(`1`)})
	if resp == nil {
		t.Fatal("initializeResponse returned nil")
	}
	result := resp.Result.(map[string]any)
	si := result["serverInfo"].(map[string]any)
	if si["name"] != "c3" {
		t.Fatalf("serverInfo.name = %v; want %q", si["name"], "c3")
	}
	caps := result["capabilities"].(map[string]any)
	exp := caps["experimental"].(map[string]any)
	if _, ok := exp["claude/channel"]; !ok {
		t.Fatal("capabilities.experimental missing claude/channel")
	}
}

// TestHandleInboundEndToEnd drives the full path inside the adapter:
// receive a serialized OpInbound frame from the (mocked) broker, run
// handleInbound, and verify the bytes written to the MCP server's "stdout"
// match the channel-notification shape Claude Code expects.
func TestHandleInboundEndToEnd(t *testing.T) {
	tid := int64(914)
	in := c3types.Inbound{
		Channel:   "telegram",
		ChatID:    -1001234567890,
		MessageID: 933,
		TopicID:   &tid,
		Text:      "hi inbound",
		Sender: c3types.Sender{
			UserID:   12345678,
			Username: "alice",
		},
		Timestamp: time.Date(2026, 5, 9, 9, 17, 55, 0, time.UTC),
	}
	raw, err := json.Marshal(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: in})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var buf bytes.Buffer
	a := newAdapter()
	a.mcp = mcp.New(strings.NewReader(""), &buf, a)

	a.handleInbound(raw)

	out := buf.Bytes()
	t.Logf("WIRE: %s", string(out))

	if !bytes.HasSuffix(out, []byte{'\n'}) {
		t.Fatal("frame must be newline-terminated")
	}
	var msg map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["method"] != "notifications/claude/channel" {
		t.Errorf("method = %v; want notifications/claude/channel", msg["method"])
	}
	params := msg["params"].(map[string]any)
	if _, isString := params["content"].(string); !isString {
		t.Errorf("params.content = %T; want string", params["content"])
	}
	meta := params["meta"].(map[string]any)
	// Per channels-reference.md, meta is Record<string, string>: every value
	// must be a string. We previously sent chat_id as int (matching the
	// official Telegram plugin's accidental shape) but the doc spec is
	// string and Claude Code may silently drop non-conforming meta values.
	if _, isString := meta["chat_id"].(string); !isString {
		t.Errorf("meta.chat_id = %T; want string per docs", meta["chat_id"])
	}
}

// TestChannelFrameWireBytes exercises buildClaudeChannelFrame + mcp.Server.Notify
// the same way the live adapter does, captures the bytes written to "stdout",
// and asserts the wire format byte-by-byte against the shape the official
// Telegram plugin emits (~/.claude/plugins/.../telegram/0.0.6/server.ts:978).
//
// Reference frame (golden) — derived from server.ts at line 978-1000:
//
//	{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{
//	  "content":"hi inbound",
//	  "meta":{
//	    "chat_id": -1001234567890,
//	    "message_thread_id": "914",
//	    "message_id": "933",
//	    "user": "alice",
//	    "user_id": "12345678",
//	    "ts": "2026-05-09T09:17:55.000Z"
//	  }
//	}}
//
// Map iteration order in Go is randomised so we compare by parsed struct, not
// raw bytes. But we DO assert: (a) `content` is a string not an array, (b)
// `chat_id` is a raw int, (c) message_thread_id / message_id / user_id are
// strings, (d) no spurious fields, (e) one trailing newline (line-framed).
func TestChannelFrameWireBytes(t *testing.T) {
	tid := int64(914)
	in := &c3types.Inbound{
		Channel:   "telegram",
		ChatID:    -1001234567890,
		MessageID: 933,
		TopicID:   &tid,
		Text:      "hi inbound",
		Sender: c3types.Sender{
			UserID:   12345678,
			Username: "alice",
		},
		Timestamp: time.Date(2026, 5, 9, 9, 17, 55, 0, time.UTC),
	}

	var buf bytes.Buffer
	srv := mcp.New(strings.NewReader(""), &buf, nil) // handler nil — we won't Run
	frame := buildClaudeChannelFrame(in)
	if err := srv.Notify("notifications/claude/channel", frame); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	raw := buf.Bytes()
	t.Logf("WIRE BYTES (%d): %s", len(raw), string(raw))

	// Line framing: exactly one trailing \n, no embedded.
	if !bytes.HasSuffix(raw, []byte{'\n'}) {
		t.Fatalf("frame must end with \\n, got: %q", raw[max(0, len(raw)-10):])
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatalf("frame must contain exactly one \\n; found %d", bytes.Count(raw, []byte{'\n'}))
	}

	var msg struct {
		JSONRPC string         `json:"jsonrpc"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimRight(raw, "\n"), &msg); err != nil {
		t.Fatalf("unmarshal frame: %v\n%s", err, raw)
	}

	if msg.JSONRPC != "2.0" {
		t.Errorf("jsonrpc: want 2.0, got %q", msg.JSONRPC)
	}
	if msg.Method != "notifications/claude/channel" {
		t.Errorf("method: want notifications/claude/channel, got %q", msg.Method)
	}

	// content MUST be a plain string per official plugin server.ts:980.
	content, ok := msg.Params["content"].(string)
	if !ok {
		t.Fatalf("params.content: want string, got %T (value: %v)",
			msg.Params["content"], msg.Params["content"])
	}
	if content != "hi inbound" {
		t.Errorf("params.content: want %q, got %q", "hi inbound", content)
	}

	meta, ok := msg.Params["meta"].(map[string]any)
	if !ok {
		t.Fatalf("params.meta: want object, got %T", msg.Params["meta"])
	}

	// chat_id is a STRING per channels-reference.md (meta is
	// Record<string, string>). All values must be strings.
	chatIDStr, ok := meta["chat_id"].(string)
	if !ok {
		t.Fatalf("meta.chat_id: want string, got %T (value: %v)",
			meta["chat_id"], meta["chat_id"])
	}
	if chatIDStr != "-1001234567890" {
		t.Errorf("meta.chat_id: want %q, got %q", "-1001234567890", chatIDStr)
	}

	// String fields.
	for _, k := range []string{"chat_id", "message_thread_id", "message_id", "user", "user_id", "ts"} {
		v, ok := meta[k]
		if !ok {
			t.Errorf("meta.%s: missing", k)
			continue
		}
		if _, ok := v.(string); !ok {
			t.Errorf("meta.%s: want string, got %T (value: %v)", k, v, v)
		}
	}

	// Spot-check values.
	if meta["message_thread_id"] != "914" {
		t.Errorf("meta.message_thread_id: want %q, got %v", "914", meta["message_thread_id"])
	}
	if meta["message_id"] != "933" {
		t.Errorf("meta.message_id: want %q, got %v", "933", meta["message_id"])
	}
	if meta["user"] != "alice" {
		t.Errorf("meta.user: want %q, got %v", "alice", meta["user"])
	}
	if meta["user_id"] != "12345678" {
		t.Errorf("meta.user_id: want %q, got %v", "12345678", meta["user_id"])
	}

	// Reject any spurious fields the official plugin doesn't send. This is the
	// most likely cause of silent rejection by Claude Code's MCP receiver.
	allowed := map[string]bool{
		"chat_id":             true,
		"message_thread_id":   true,
		"message_id":          true,
		"user":                true,
		"user_id":             true,
		"ts":                  true,
		"reply_to_message_id": true,
		"reply_to_user":       true,
		"reply_to_text":       true,
		"image_path":          true,
		"attachment_kind":     true,
		"attachment_file_id":  true,
		"attachment_size":     true,
		"attachment_mime":     true,
		"attachment_name":     true,
	}
	for k := range meta {
		if !allowed[k] {
			t.Errorf("meta.%s: SPURIOUS field — official plugin doesn't send it", k)
		}
	}
}
