package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// TestHelloWireFields drives the hello → hello_ack handshake against an
// in-memory broker peer (a net.Pipe) and asserts the load-bearing fields the
// Claude Desktop adapter must send: CLI "desktop", CannotRenderChannels true
// (Desktop cannot render live channel frames — poll-only), and the fetch_queue
// capability (its only inbound path).
func TestHelloWireFields(t *testing.T) {
	t.Setenv("C3_DESKTOP_CWD", "/tmp/desktop-test-cwd")

	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close()
	defer serverEnd.Close()

	a := newAdapter()
	a.conn = ipc.NewConn(clientEnd)
	peer := ipc.NewConn(serverEnd)

	errCh := make(chan error, 1)
	go func() { errCh <- a.hello() }()

	// Read the hello frame the adapter wrote.
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatalf("peer.ReadFrame: %v", err)
	}
	var hello ipc.HelloMsg
	if err := json.Unmarshal(raw, &hello); err != nil {
		t.Fatalf("unmarshal hello: %v\nwire: %s", err, raw)
	}
	if hello.Op != ipc.OpHello {
		t.Errorf("hello.Op = %q; want %q", hello.Op, ipc.OpHello)
	}
	if hello.CLI != "desktop" {
		t.Errorf("hello.CLI = %q; want %q", hello.CLI, "desktop")
	}
	if !hello.CannotRenderChannels {
		t.Error("hello.CannotRenderChannels = false; want true (Desktop is poll-only)")
	}
	if hello.CWD != "/tmp/desktop-test-cwd" {
		t.Errorf("hello.CWD = %q; want C3_DESKTOP_CWD override", hello.CWD)
	}
	if !containsStr(hello.Capabilities, "fetch_queue") {
		t.Errorf("hello.Capabilities = %v; want to contain fetch_queue", hello.Capabilities)
	}

	// Reply with a hello_ack so a.hello() returns.
	if err := peer.WriteJSON(ipc.HelloAckMsg{Op: ipc.OpHelloAck}); err != nil {
		t.Fatalf("peer.WriteJSON ack: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("a.hello() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a.hello() did not return after hello_ack")
	}
	if a.helloAck.Op != ipc.OpHelloAck {
		t.Errorf("stored helloAck.Op = %q; want %q", a.helloAck.Op, ipc.OpHelloAck)
	}
}

// TestServerInfoAndTools exercises the same buildMCPServer path the live adapter
// uses and asserts, over real in-memory MCP transports: (a) serverInfo.name is
// "c3" (MUST equal the mcpServers.<key> in the Desktop config), (b) the server
// declares the Logging capability and NOT the Claude Code experimental
// claude/channel map (Desktop can't render channel frames), (c) instructions are
// non-empty, and (d) the exact adapter tool set is registered — with `ask`,
// `send_typing`, and any permission tool deliberately ABSENT.
func TestServerInfoAndTools(t *testing.T) {
	if adapterName != "c3" {
		t.Fatalf("adapterName must be %q to match the Desktop config mcpServers key; got %q", "c3", adapterName)
	}

	a := newAdapter()
	// Seed hello-ack caps so buildInstructions folds capability guidance in and
	// the reply media schema renders against a realistic manifest.
	a.helloAck.Capabilities = &c3types.Capabilities{
		Channel:         "telegram",
		RichText:        true,
		MaxMessageRunes: 4096,
		MediaKinds:      []c3types.MediaKind{c3types.MediaPhoto, c3types.MediaFile},
		CompressedPhoto: true,
		OriginalFile:    true,
		MaxSendBytes:    50 * 1024 * 1024,
		Polls:           true,
		Typing:          true,
	}
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
	if params.ServerInfo.Version != adapterVersion {
		t.Errorf("serverInfo.version = %q; want %q", params.ServerInfo.Version, adapterVersion)
	}
	if params.Capabilities == nil {
		t.Fatal("capabilities missing")
	}
	if params.Capabilities.Logging == nil {
		t.Error("capabilities.logging missing; Desktop adapter must declare Logging (agy parity)")
	}
	// Desktop must NOT advertise the Claude Code channel experimental capability.
	if params.Capabilities.Experimental != nil {
		if _, ok := params.Capabilities.Experimental["claude/channel"]; ok {
			t.Error("capabilities.experimental must NOT declare claude/channel (Desktop cannot render channels)")
		}
	}
	if params.Instructions == "" {
		t.Fatal("instructions empty in initialize response")
	}
	if !strings.Contains(params.Instructions, "POLL-ONLY") {
		t.Errorf("instructions must state Desktop is poll-only; got:\n%s", params.Instructions)
	}

	listResult, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range listResult.Tools {
		got[tool.Name] = true
	}
	wantTools := []string{
		"attach", "detach", "topics", "fetch_queue", "retranscribe",
		"reply", "react", "edit_message", "poll", "stop_poll", "download_attachment",
		"open_inbox",
	}
	for _, name := range wantTools {
		if !got[name] {
			t.Errorf("tools/list missing %q (got %v)", name, keys(got))
		}
	}
	// Exactly the expected set — no extras (guards against an accidental `ask`
	// or permission tool sneaking in).
	if len(got) != len(wantTools) {
		t.Errorf("tools/list has %d tools; want exactly %d (%v)", len(got), len(wantTools), keys(got))
	}
	for _, banned := range []string{"ask", "send_typing", "request_permission"} {
		if got[banned] {
			t.Errorf("tool %q must NOT be registered on the Desktop adapter", banned)
		}
	}

	// --- MCP Apps ("C3 Inbox") contract -------------------------------------
	// The adapter must advertise the io.modelcontextprotocol/ui extension, link
	// open_inbox to its ui:// resource via _meta.ui.resourceUri, and serve that
	// resource as text/html;profile=mcp-app.
	if params.Capabilities.Extensions == nil {
		t.Error("capabilities.extensions missing; MCP Apps hosts gate on it")
	} else if ext, ok := params.Capabilities.Extensions[uiExtensionID].(map[string]any); !ok {
		t.Errorf("capabilities.extensions[%q] missing or not an object; got %v", uiExtensionID, params.Capabilities.Extensions)
	} else if mimes, _ := ext["mimeTypes"].([]any); len(mimes) == 0 || mimes[0] != uiResourceMIME {
		t.Errorf("extension mimeTypes = %v; want [%q]", ext["mimeTypes"], uiResourceMIME)
	}

	var openInbox *mcp.Tool
	for _, tool := range listResult.Tools {
		if tool.Name == "open_inbox" {
			openInbox = tool
		}
	}
	if openInbox == nil {
		t.Fatal("open_inbox tool not found in tools/list")
	}
	if uiMeta, _ := openInbox.Meta["ui"].(map[string]any); uiMeta == nil || uiMeta["resourceUri"] != uiInboxURI {
		t.Errorf("open_inbox _meta.ui.resourceUri = %v; want %q", openInbox.Meta["ui"], uiInboxURI)
	}
	if openInbox.Meta["ui/resourceUri"] != uiInboxURI {
		t.Errorf("open_inbox deprecated _meta[ui/resourceUri] = %v; want %q (host back-compat)", openInbox.Meta["ui/resourceUri"], uiInboxURI)
	}

	// attach and topics are marked app-callable (visibility ["model","app"]) so
	// the C3 Inbox panel can call them through the host bridge (apps.mdx:399-401).
	for _, name := range []string{"attach", "topics"} {
		var tool *mcp.Tool
		for _, tl := range listResult.Tools {
			if tl.Name == name {
				tool = tl
			}
		}
		if tool == nil {
			t.Fatalf("%s tool not found in tools/list", name)
		}
		vis := uiVisibility(tool)
		if !containsStr(vis, "model") || !containsStr(vis, "app") {
			t.Errorf("%s _meta.ui.visibility = %v; want to contain \"model\" and \"app\"", name, vis)
		}
	}

	rr, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: uiInboxURI})
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", uiInboxURI, err)
	}
	if len(rr.Contents) == 0 {
		t.Fatalf("ReadResource(%s) returned no contents", uiInboxURI)
	}
	rc := rr.Contents[0]
	if rc.MIMEType != uiResourceMIME {
		t.Errorf("inbox resource mimeType = %q; want %q", rc.MIMEType, uiResourceMIME)
	}
	if !strings.Contains(rc.Text, "C3 Inbox connected") {
		t.Error("inbox HTML missing the '✅ C3 Inbox connected' banner")
	}
	if !strings.Contains(rc.Text, "ui/initialize") || !strings.Contains(rc.Text, "fetch_queue") {
		t.Error("inbox HTML missing the ui/initialize handshake or the fetch_queue call")
	}
	// Interactive inbox: in-panel attach form, Hand to Claude + Auto, and the
	// ui/message turn-start call must all be present in the served HTML.
	for _, want := range []string{"ui/message", "Hand to Claude", "Auto", "placeholder=\"topic name\"", "name: \"attach\""} {
		if !strings.Contains(rc.Text, want) {
			t.Errorf("inbox HTML missing %q (interactive-inbox extension)", want)
		}
	}
}

// TestSessionID covers the C3_DESKTOP_SESSION contract: an explicit (trimmed)
// value is used verbatim, an unset/blank value falls back to a per-process id.
func TestSessionID(t *testing.T) {
	t.Setenv("C3_DESKTOP_SESSION", "my-stable-id")
	if got := sessionID(); got != "my-stable-id" {
		t.Errorf("sessionID() = %q; want %q", got, "my-stable-id")
	}

	t.Setenv("C3_DESKTOP_SESSION", "  spaced  ")
	if got := sessionID(); got != "spaced" {
		t.Errorf("sessionID() = %q; want trimmed %q", got, "spaced")
	}

	t.Setenv("C3_DESKTOP_SESSION", "")
	if got := sessionID(); !strings.HasPrefix(got, "desktop-") {
		t.Errorf("sessionID() = %q; want per-process desktop- prefix when unset", got)
	}
}

// TestDesktopCWD covers the C3_DESKTOP_CWD override used for testing / power
// users; unset falls back to os.Getwd (non-empty in the test runner).
func TestDesktopCWD(t *testing.T) {
	t.Setenv("C3_DESKTOP_CWD", "/custom/desktop/dir")
	if got := desktopCWD(); got != "/custom/desktop/dir" {
		t.Errorf("desktopCWD() = %q; want the C3_DESKTOP_CWD override", got)
	}
	t.Setenv("C3_DESKTOP_CWD", "")
	if got := desktopCWD(); got == "" {
		t.Error("desktopCWD() empty with no override; want os.Getwd fallback")
	}
}

func uiVisibility(tool *mcp.Tool) []string {
	if tool == nil || tool.Meta == nil {
		return nil
	}
	ui, _ := tool.Meta["ui"].(map[string]any)
	if ui == nil {
		return nil
	}
	raw, _ := ui["visibility"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
