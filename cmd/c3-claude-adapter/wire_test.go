package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// note: io.NopCloserReader/Writer wrappers are defined in testhelpers_test.go

// TestServerInfoName guards the lesson from 2026-05-09: serverInfo.name MUST
// match the .mcp.json key ("c3") so Claude Code's channel dispatch can route
// notifications/claude/channel frames from this server. Both reference
// implementations (~/.claude/plugins/.../fakechat/server.ts:60 and
// .../telegram/0.0.6/server.ts:371) follow this convention.
//
// Post-SDK-migration the assertion is structural — we exercise the same
// code path that constructs the live MCP server (buildMCPServer) and
// confirm Implementation.Name + experimental capability declaration.
func TestServerInfoName(t *testing.T) {
	if adapterName != "c3" {
		t.Fatalf("adapterName must be %q to match .mcp.json key; got %q", "c3", adapterName)
	}

	// buildMCPServer wires Implementation + Capabilities; if either drifts
	// from what Claude Code expects, channel dispatch silently breaks.
	// Construct the server and inspect what the SDK will emit on initialize
	// by exercising in-memory transports end-to-end.
	a := newAdapter()
	srv := a.buildMCPServer()
	if srv == nil {
		t.Fatal("buildMCPServer returned nil")
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	a.notifyTx = newNotifyTransport(serverT)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx, a.notifyTx) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sess.Close()

	params := sess.InitializeResult()
	if params == nil {
		t.Fatal("InitializeResult is nil")
	}
	if params.ServerInfo == nil || params.ServerInfo.Name != "c3" {
		var got string
		if params.ServerInfo != nil {
			got = params.ServerInfo.Name
		}
		t.Fatalf("serverInfo.name = %q; want %q", got, "c3")
	}
	if params.Capabilities == nil || params.Capabilities.Experimental == nil {
		t.Fatal("capabilities.experimental missing")
	}
	if _, ok := params.Capabilities.Experimental["claude/channel"]; !ok {
		t.Fatal("capabilities.experimental missing claude/channel")
	}
	if params.Instructions == "" {
		t.Fatal("instructions empty in initialize response")
	}

	// Verify tools/list returns the adapter tools (attach, detach, topics,
	// reply, react, edit_message, send_typing, poll, download_attachment) —
	// Claude Code displays tool presence per-server and a regression here turns
	// a working session into "tool not found" for every adapter operation.
	listResult, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	wantTools := []string{
		"attach", "detach", "topics", "reply", "react",
		"edit_message", "send_typing", "poll", "download_attachment",
	}
	got := map[string]bool{}
	for _, tool := range listResult.Tools {
		got[tool.Name] = true
	}
	for _, name := range wantTools {
		if !got[name] {
			t.Errorf("tools/list missing %q (got %v)", name, got)
		}
	}
}

// TestHandleInboundEndToEnd drives the full path inside the adapter:
// receive a serialized OpInbound frame from the (mocked) broker, run
// handleInbound, and verify the bytes written to the SDK's stdout match
// the channel-notification shape Claude Code expects.
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

	// Capture the wire bytes by wrapping an IOTransport with a buffer
	// writer. Connection.Write goes straight to this buffer.
	var buf safeBuffer
	a := newAdapter()
	a.notifyTx = newNotifyTransport(&mcp.IOTransport{
		Reader: nopCloseReader{strings.NewReader("")},
		Writer: nopCloseWriter{&buf},
	})
	// Establish the connection (drives Connect on the wrapped transport
	// and captures the live Connection inside notifyTx).
	if _, err := a.notifyTx.Connect(context.Background()); err != nil {
		t.Fatalf("notifyTx.Connect: %v", err)
	}

	a.handleInbound(context.Background(), raw)

	out := buf.Bytes()
	t.Logf("WIRE: %s", string(out))

	if !bytes.HasSuffix(out, []byte{'\n'}) {
		t.Fatal("frame must be newline-terminated")
	}
	var msg map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &msg); err != nil {
		t.Fatalf("unmarshal: %v\nwire: %s", err, out)
	}
	if msg["method"] != "notifications/claude/channel" {
		t.Errorf("method = %v; want notifications/claude/channel", msg["method"])
	}
	params := msg["params"].(map[string]any)
	if _, isString := params["content"].(string); !isString {
		t.Errorf("params.content = %T; want string", params["content"])
	}
	meta := params["meta"].(map[string]any)
	if _, isString := meta["chat_id"].(string); !isString {
		t.Errorf("meta.chat_id = %T; want string per docs", meta["chat_id"])
	}
}

// TestChannelFrameWireBytes exercises buildClaudeChannelFrame +
// notifyTransport.Notify the same way the live adapter does, captures the
// bytes written to "stdout", and asserts the wire format against the
// official Telegram plugin's frame shape.
//
// Reference frame (golden) — derived from
// ~/.claude/plugins/.../telegram/0.0.6/server.ts at line 978-1000:
//
//	{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{
//	  "content":"hi inbound",
//	  "meta":{
//	    "chat_id": "-1001234567890",
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
// meta values are strings (per channels-reference.md), (c) no spurious fields,
// (d) one trailing newline (line-framed).
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

	var buf safeBuffer
	tx := newNotifyTransport(&mcp.IOTransport{
		Reader: nopCloseReader{strings.NewReader("")},
		Writer: nopCloseWriter{&buf},
	})
	if _, err := tx.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	frame := buildClaudeChannelFrame(in)
	if err := tx.Notify(context.Background(), "notifications/claude/channel", frame); err != nil {
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

	chatIDStr, ok := meta["chat_id"].(string)
	if !ok {
		t.Fatalf("meta.chat_id: want string, got %T (value: %v)",
			meta["chat_id"], meta["chat_id"])
	}
	if chatIDStr != "-1001234567890" {
		t.Errorf("meta.chat_id: want %q, got %q", "-1001234567890", chatIDStr)
	}

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

// ─── 3-state attach formatter tests moved 2026-05-19 ───────────────────────
//
// formatAttached was extracted to internal/ipc/format.go as part of the
// audit-triage extraction (docs/plans/2026-05-19-audit-triage.md).
// All formatter assertions (3-state Status, proposal-parity, OK + Err
// fallbacks) live in internal/ipc/format_test.go now — single source of
// truth for the formatter, single source of truth for its tests. The
// adapter call-sites just call ipc.FormatAttached / ipc.FormatTopics.

// TestNotifyTransport_DisconnectClearsConn exercises the preventative
// Disconnect() method documented in notify_transport.go: after a
// successful Connect+Notify, calling Disconnect must clear the stored
// Connection so the NEXT Notify returns the "connection not yet
// established" sentinel error rather than silently writing to a stale
// reference. Closes report MINOR m2 (2026-05-19).
func TestNotifyTransport_DisconnectClearsConn(t *testing.T) {
	var buf safeBuffer
	tx := newNotifyTransport(&mcp.IOTransport{
		Reader: nopCloseReader{strings.NewReader("")},
		Writer: nopCloseWriter{&buf},
	})
	if _, err := tx.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// First Notify works.
	if err := tx.Notify(context.Background(), "test/method", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("first Notify: %v", err)
	}

	// After Disconnect, the wrapper's stored conn is cleared.
	tx.Disconnect()

	// Subsequent Notify returns the sentinel error (caller must Connect
	// again before writing).
	err := tx.Notify(context.Background(), "test/method", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("Notify after Disconnect: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not yet established") {
		t.Errorf("Notify after Disconnect: want 'not yet established' sentinel, got %v", err)
	}
}
