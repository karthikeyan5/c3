package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServerInfoName guards the contract that the Codex adapter advertises
// `c3_codex` as serverInfo.name (matches mcp_servers.<key> in Codex's
// config). Same lesson as the Claude adapter (see
// cmd/c3-claude-adapter/wire_test.go TestServerInfoName) — channel
// dispatch keys on this exact string.
func TestServerInfoName(t *testing.T) {
	if adapterName != "c3_codex" {
		t.Fatalf("adapterName must be %q to match mcp_servers key; got %q", "c3_codex", adapterName)
	}

	a := newAdapter()
	srv := a.buildMCPServer()
	if srv == nil {
		t.Fatal("buildMCPServer returned nil")
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	a.transport = newLogNotifyTransport(serverT)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx, a.transport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sess.Close()

	result := sess.InitializeResult()
	if result == nil {
		t.Fatal("InitializeResult is nil")
	}
	if result.ServerInfo == nil || result.ServerInfo.Name != "c3_codex" {
		var got string
		if result.ServerInfo != nil {
			got = result.ServerInfo.Name
		}
		t.Fatalf("serverInfo.name = %q; want %q", got, "c3_codex")
	}
	if result.Instructions == "" {
		t.Fatal("instructions empty in initialize response")
	}

	// Verify tools/list returns the expected Codex tool set
	// (attach, topics, inbox, reply, react, edit_message,
	// send_typing, poll, download_attachment, codex_forward) — a regression
	// here breaks every adapter operation for Codex.
	listResult, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	wantTools := []string{
		"attach", "topics", "inbox", "reply", "react",
		"edit_message", "send_typing", "poll", "download_attachment", "codex_forward",
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

// ─── formatAttached tests moved 2026-05-19 ─────────────────────────────────
//
// Both adapters' formatAttached + formatTopics were extracted to
// internal/ipc/format.go as part of the audit triage
// (docs/plans/2026-05-19-audit-triage.md). The previously-Codex-specific
// proposal-parity guard (created when the Codex adapter silently lacked
// disambiguate_dm + force_steal branches) is now centrally enforced in
// internal/ipc/format_test.go::TestFormatAttached_ProposalParity, so a
// single source of truth covers both adapters.

func TestCodexAttachTool_AcceptsPolicyRejectedArg(t *testing.T) {
	// Wire-shape guard: the agent must be able to pass
	// `policy_rejected=true` to the `attach` tool. If the input schema
	// blocks it, the agent will never be able to surface the host
	// rejection cleanly. InputSchema is registered as map[string]any.
	a := newAdapter()
	srv := a.buildMCPServer()
	if srv == nil {
		t.Fatal("buildMCPServer returned nil")
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	a.transport = newLogNotifyTransport(serverT)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = srv.Run(ctx, a.transport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sess.Close()

	listResult, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var attachTool *mcp.Tool
	for _, tool := range listResult.Tools {
		if tool.Name == "attach" {
			attachTool = tool
			break
		}
	}
	if attachTool == nil {
		t.Fatal("attach tool not advertised")
	}
	// InputSchema is *jsonschema.Schema after deserialization through
	// the SDK; marshal back to JSON and substring-check so we don't
	// bind to internal SDK type names.
	raw, err := json.Marshal(attachTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal attach InputSchema: %v", err)
	}
	if !strings.Contains(string(raw), `"policy_rejected"`) {
		t.Errorf("attach input schema missing 'policy_rejected' property; schema=%s", raw)
	}
}

// TestLogNotifyTransport_DisconnectClearsConn — analogous to the Claude
// adapter test (TestNotifyTransport_DisconnectClearsConn). Calling
// Disconnect after Connect+Notify clears the captured Connection so
// the next Notify returns the "connection not yet established"
// sentinel. Closes report MINOR m2 (2026-05-19).
func TestLogNotifyTransport_DisconnectClearsConn(t *testing.T) {
	clientT, serverT := mcp.NewInMemoryTransports()
	tx := newLogNotifyTransport(serverT)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := tx.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Drain the in-memory transport on the client side so writes don't
	// block — we don't actually care about the bytes, just that Notify
	// returns nil on the first call.
	go func() {
		clientConn, err := clientT.Connect(ctx)
		if err != nil {
			return
		}
		for {
			if _, err := clientConn.Read(ctx); err != nil {
				return
			}
		}
	}()

	if err := tx.Notify(ctx, "notifications/message", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("first Notify: %v", err)
	}

	tx.Disconnect()

	err := tx.Notify(ctx, "notifications/message", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("Notify after Disconnect: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not yet established") {
		t.Errorf("Notify after Disconnect: want 'not yet established' sentinel, got %v", err)
	}
}
