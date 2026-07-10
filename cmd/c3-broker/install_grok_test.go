package main

import "testing"

func TestPatchGrokConfig_Fresh(t *testing.T) {
	out, changed := patchGrokConfig("")
	if !changed {
		t.Fatal("expected change")
	}
	if !containsStr(out, "use_leader = true") || !containsStr(out, "c3-grok-adapter") {
		t.Fatalf("got %q", out)
	}
}

func TestPatchGrokConfig_Idempotent(t *testing.T) {
	first, _ := patchGrokConfig("")
	second, changed := patchGrokConfig(first)
	if changed {
		t.Fatalf("second patch should be no-op; got %q vs %q", second, first)
	}
}

func TestPatchGrokConfig_FlipClaudeAdapter(t *testing.T) {
	in := "[mcp_servers.c3]\ncommand = \"c3-claude-adapter\"\nenabled = true\n"
	out, changed := patchGrokConfig(in)
	if !changed || !containsStr(out, "c3-grok-adapter") {
		t.Fatalf("got %q", out)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
