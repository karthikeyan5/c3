package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/karthikeyan5/c3/internal/sessionhandoff"
)

// withStdin replaces os.Stdin with a pipe carrying data for the duration of fn.
func withStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig; _ = r.Close() }()
	go func() {
		_, _ = w.WriteString(data)
		_ = w.Close()
	}()
	fn()
}

func TestRunSessionHook_WritesHandoff(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// CLAUDE_ENV_FILE's parent dir basename is the ephemeral instance id.
	envFile := filepath.Join(t.TempDir(), "b60e8044-instance", "sessionstart-hook-1.sh")
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	input := `{"session_id":"70341717-stable","cwd":"/home/k/proj","source":"resume","hook_event_name":"SessionStart"}`
	withStdin(t, input, func() {
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook returned error (must be nil): %v", err)
		}
	})

	e, ok := sessionhandoff.Read("b60e8044-instance")
	if !ok {
		t.Fatal("expected a handoff entry for the instance id")
	}
	if e.StableSessionID != "70341717-stable" {
		t.Fatalf("StableSessionID = %q, want 70341717-stable", e.StableSessionID)
	}
	if e.CWD != "/home/k/proj" || e.Source != "resume" {
		t.Fatalf("handoff entry = %+v", e)
	}
}

func TestRunSessionHook_EmptyEnvFileNoOp(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "") // no instance id derivable

	withStdin(t, `{"session_id":"70341717-stable","source":"resume"}`, func() {
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook must exit 0 with empty CLAUDE_ENV_FILE: %v", err)
		}
	})
	// Nothing should have been written anywhere under session-instances.
	dir := filepath.Join(state, "c3", "session-instances")
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		t.Fatalf("no handoff should be written without an instance id; found %d entries", len(ents))
	}
}

func TestRunSessionHook_EmptySessionIDNoOp(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	envFile := filepath.Join(t.TempDir(), "inst-xyz", "sessionstart-hook-1.sh")
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	withStdin(t, `{"cwd":"/x","source":"startup"}`, func() { // no session_id
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook must exit 0 with empty session_id: %v", err)
		}
	})
	if _, ok := sessionhandoff.Read("inst-xyz"); ok {
		t.Fatal("no handoff should be written without a session id")
	}
}

func TestRunSessionHook_GrokWritesHandoffByStableID(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")              // no Claude instance id derivable
	t.Setenv("GROK_SESSION_ID", "grok-uuid-123") // Grok env present → Grok branch

	withStdin(t, `{"session_id":"grok-uuid-123","cwd":"/w","source":"startup"}`, func() {
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook returned error (must be nil): %v", err)
		}
	})

	e, ok := sessionhandoff.Read("grok-uuid-123")
	if !ok {
		t.Fatal("expected a handoff entry keyed by the Grok stable session id")
	}
	if e.StableSessionID != "grok-uuid-123" || e.CWD != "/w" {
		t.Fatalf("handoff entry = %+v", e)
	}
}

func TestRunSessionHook_GrokRejectsUnsafeSessionID(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("GROK_SESSION_ID", "present") // trigger the Grok branch

	// A traversal-shaped session id must be refused as a handoff key (exit 0,
	// nothing written) — the guard mirrors sessionhandoff.Path's invariant.
	withStdin(t, `{"session_id":"../evil","cwd":"/w","source":"startup"}`, func() {
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook must exit 0 on an unsafe session id: %v", err)
		}
	})
	dir := filepath.Join(state, "c3", "session-instances")
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		t.Fatalf("no handoff should be written for an unsafe session id; found %d entries", len(ents))
	}
}

func TestRunSessionHook_GarbageStdinNoOp(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	envFile := filepath.Join(t.TempDir(), "inst-garbage", "sessionstart-hook-1.sh")
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	withStdin(t, `{not valid json`, func() {
		if err := runSessionHook(); err != nil {
			t.Fatalf("runSessionHook must exit 0 on garbage stdin: %v", err)
		}
	})
	if _, ok := sessionhandoff.Read("inst-garbage"); ok {
		t.Fatal("no handoff should be written on garbage stdin")
	}
}
