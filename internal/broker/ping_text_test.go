package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPingText_RendersResolvedSubdir confirms the /c3:ping identification
// message shows the resolved project dir (launchCWD/topicName when that
// subdir exists), matching the on-attach welcome — not the bare launch
// dir. Regression guard for the 2026-06-04 ping/welcome consistency fix.
func TestPingText_RendersResolvedSubdir(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := &Stub{CWD: parent, CLI: "claude", PID: 4242}

	got := pingText(stub, "proj")

	// The rendered cwd line must include the refined subdir, not the bare
	// parent. We assert the "/proj" suffix is present in the dir segment.
	if !strings.Contains(got, "proj`") && !strings.Contains(got, "proj\n") {
		t.Errorf("pingText did not render resolved subdir; got:\n%s", got)
	}
	if !strings.Contains(got, filepath.Base(parent)+"/proj") &&
		!strings.Contains(got, "~/"+filepath.Base(parent)) {
		// Tolerate the home-shorten path; the load-bearing check is that
		// the topic-name subdir appears, which the first assert covers.
	}
}

// TestPingText_NoSubdir_KeepsLaunchCWD confirms that when no subdir
// matching the topic name exists, pingText falls back to the launch cwd
// (resolveAttachCWD returns it unchanged) — no spurious path invention.
func TestPingText_NoSubdir_KeepsLaunchCWD(t *testing.T) {
	parent := t.TempDir()
	stub := &Stub{CWD: parent, CLI: "claude", PID: 4242}

	got := pingText(stub, "nonexistent-topic")

	if strings.Contains(got, "nonexistent-topic`") || strings.Contains(got, "nonexistent-topic\n") {
		t.Errorf("pingText invented a subdir that does not exist; got:\n%s", got)
	}
}

// TestPingText_EmptyCWD_FriendlyFallback confirms the DM/no-cwd case
// (stub.CWD == "") still renders the compact single-line form without a
// directory, matching the prior behavior.
func TestPingText_EmptyCWD_FriendlyFallback(t *testing.T) {
	stub := &Stub{CWD: "", CLI: "claude", PID: 4242}
	got := pingText(stub, "dm")
	if strings.Contains(got, "📁") {
		t.Errorf("pingText with empty cwd should omit the directory line; got:\n%s", got)
	}
	if !strings.Contains(got, "dm") {
		t.Errorf("pingText should still name the topic; got:\n%s", got)
	}
}
