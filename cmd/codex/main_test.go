package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestShouldBypassNonInteractiveCommands(t *testing.T) {
	if !shouldBypass([]string{"exec", "echo", "hi"}) {
		t.Fatal("codex exec should bypass C3 launcher")
	}
	if !shouldBypass([]string{"--remote", "ws://127.0.0.1:8766"}) {
		t.Fatal("--remote should bypass C3 launcher")
	}
	if shouldBypass([]string{"resume"}) {
		t.Fatal("codex resume should stay bridged")
	}
}

func TestInferTopicNameUsesNearestClaudeMDWithSharedRootGuard(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "projects")
	project := filepath.Join(shared, "widget")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "CLAUDE.md"), []byte("# root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := inferTopicName(shared, shared); got != "" {
		t.Fatalf("shared root topic = %q, want empty", got)
	}
	if got := inferTopicName(project, shared); got != "" {
		t.Fatalf("project without its own CLAUDE.md under shared root = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("# project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := inferTopicName(project, shared); got != "widget" {
		t.Fatalf("project topic = %q, want widget", got)
	}
}

func TestMCPConfigArgsPointAtGoCodexAdapter(t *testing.T) {
	args := mcpConfigArgs("/usr/local/bin/c3-codex-adapter", "ws://127.0.0.1:8766", "/tmp/work", "dm")
	joined := "\n" + joinArgs(args) + "\n"
	for _, want := range []string{
		`mcp_servers.c3_codex.command="/usr/local/bin/c3-codex-adapter"`,
		`mcp_servers.c3_codex.env.C3_CODEX_APP_SERVER_WS="ws://127.0.0.1:8766"`,
		`mcp_servers.c3_codex.env.C3_CODEX_CWD="/tmp/work"`,
		`mcp_servers.c3_codex.env.C3_CODEX_REMOTE_BRIDGE="1"`,
		`mcp_servers.c3_codex.env.C3_ATTACH_NAME="dm"`,
	} {
		if !contains(joined, want) {
			t.Fatalf("mcp args missing %s in:\n%s", want, joined)
		}
	}
	if contains(joined, "codex_stub.py") || contains(joined, "python3") {
		t.Fatalf("mcp args should not reference Python MVP stub:\n%s", joined)
	}
}

func TestChooseAppServerURLFallsForwardWhenDefaultOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defaultURL := "ws://" + ln.Addr().String()
	got := chooseAppServerURL(defaultURL, "/tmp/work", "dm", func(string) bool { return false })
	if got == defaultURL {
		t.Fatalf("chooseAppServerURL reused occupied default %s", defaultURL)
	}
}

func joinArgs(args []string) string {
	out := ""
	for _, arg := range args {
		out += arg + "\n"
	}
	return out
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
