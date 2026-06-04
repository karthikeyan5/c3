package proctree

import "testing"

// synthTree builds commOf/ppidOf closures over a static map describing a
// synthetic process tree, so the ancestor walks can be tested WITHOUT a
// real /proc. Each entry maps a pid to its comm and ppid.
type synthProc struct {
	comm string
	ppid int
}

func synthTree(t map[int]synthProc) (commOf func(int) string, ppidOf func(int) (int, bool)) {
	commOf = func(pid int) string {
		if p, ok := t[pid]; ok {
			return p.comm
		}
		return ""
	}
	ppidOf = func(pid int) (int, bool) {
		if p, ok := t[pid]; ok {
			return p.ppid, true
		}
		return 0, false
	}
	return commOf, ppidOf
}

// TestCLISessionPID_SkipsAdapterFindsClaude is THE core regression. Walking
// up from a stub's adapter pid (comm "c3-claude-adapt") must SKIP the
// adapter (it is NOT a real CLI under the strict predicate) and return the
// real claude ancestor — NOT the adapter pid itself.
//
//	9823 c3-claude-adapt -> 9801 claude -> 9700 zsh
//
// CLISessionPID(9823) must be 9801, not 9823.
func TestCLISessionPID_SkipsAdapterFindsClaude(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		9823: {comm: "c3-claude-adapt", ppid: 9801},
		9801: {comm: "claude", ppid: 9700},
		9700: {comm: "zsh", ppid: 1},
	})
	got := cliSessionPID(9823, commOf, ppidOf)
	if got != 9801 {
		t.Fatalf("CLISessionPID(9823)=%d, want 9801 (skip the adapter, find claude)", got)
	}
}

// TestCLISessionPID_StrictDoesNotMatchAdapter: an adapter comm with NO
// real-CLI ancestor must resolve to 0 — the strict predicate must never
// treat the adapter itself as the session CLI.
func TestCLISessionPID_StrictDoesNotMatchAdapter(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		9823: {comm: "c3-claude-adapt", ppid: 9700},
		9700: {comm: "zsh", ppid: 1},
	})
	got := cliSessionPID(9823, commOf, ppidOf)
	if got != 0 {
		t.Fatalf("CLISessionPID(9823)=%d, want 0 (adapter comm is not a CLI; no claude ancestor)", got)
	}
}

// TestCLISessionPID_DirectClaudeIsInclusive: when startPID is itself the
// claude pid, the inclusive walk returns it immediately.
func TestCLISessionPID_DirectClaudeIsInclusive(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		9801: {comm: "claude", ppid: 9700},
		9700: {comm: "zsh", ppid: 1},
	})
	got := cliSessionPID(9801, commOf, ppidOf)
	if got != 9801 {
		t.Fatalf("CLISessionPID(9801)=%d, want 9801 (inclusive: start pid is the CLI)", got)
	}
}

// TestCLISessionPID_CodexAlsoMatches: the strict predicate matches codex too.
func TestCLISessionPID_CodexAlsoMatches(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		7000: {comm: "c3-codex-adapte", ppid: 6900},
		6900: {comm: "codex", ppid: 6800},
		6800: {comm: "bash", ppid: 1},
	})
	got := cliSessionPID(7000, commOf, ppidOf)
	if got != 6900 {
		t.Fatalf("CLISessionPID(7000)=%d, want 6900 (codex ancestor)", got)
	}
}

// TestCLISessionPID_DepthCapped: a chain longer than the depth cap with no
// CLI in range returns 0 rather than looping unbounded.
func TestCLISessionPID_DepthCapped(t *testing.T) {
	tree := map[int]synthProc{}
	// Build a 30-deep chain of non-CLI procs; claude sits beyond the cap.
	for i := 100; i < 130; i++ {
		tree[i] = synthProc{comm: "zsh", ppid: i + 1}
	}
	tree[130] = synthProc{comm: "claude", ppid: 1}
	got := cliSessionPID(100, commOf(tree), ppidOf(tree))
	if got != 0 {
		t.Fatalf("CLISessionPID(100)=%d, want 0 (claude is beyond the depth cap)", got)
	}
}

// commOf/ppidOf package-private helpers for the depth-cap test (kept here so
// the test file is self-contained).
func commOf(t map[int]synthProc) func(int) string {
	return func(pid int) string {
		if p, ok := t[pid]; ok {
			return p.comm
		}
		return ""
	}
}
func ppidOf(t map[int]synthProc) func(int) (int, bool) {
	return func(pid int) (int, bool) {
		if p, ok := t[pid]; ok {
			return p.ppid, true
		}
		return 0, false
	}
}

// TestBestEffortCallerPID_WalksShellToClaude: the ping/sessions caller walk
// starts at the caller's PARENT and must climb the shell to the real claude.
//
//	self -> ppid shell(zsh) -> ppid claude
//
// bestEffortCallerPID(selfPPID=shell, ...) resolves claude.
func TestBestEffortCallerPID_WalksShellToClaude(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		5000: {comm: "zsh", ppid: 4000},    // the shell (our parent)
		4000: {comm: "claude", ppid: 3000}, // the CLI session
		3000: {comm: "login", ppid: 1},
	})
	got := bestEffortCallerPID(5000, commOf, ppidOf)
	if got != 4000 {
		t.Fatalf("bestEffortCallerPID(ppid=5000)=%d, want 4000 (climb shell to claude)", got)
	}
}

// TestBestEffortCallerPID_StopsAtClaudeNotAdapter: even if the caller's chain
// somehow contained an adapter, the strict predicate must reach the real CLI.
// (In practice the ping shell-out chain is claude -> shell -> ping; the
// adapter is never in it — but the strict predicate guarantees correctness.)
func TestBestEffortCallerPID_StopsAtClaudeNotAdapter(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		8000: {comm: "zsh", ppid: 9801},
		9801: {comm: "claude", ppid: 9700},
		9700: {comm: "zsh", ppid: 1},
	})
	got := bestEffortCallerPID(8000, commOf, ppidOf)
	if got != 9801 {
		t.Fatalf("bestEffortCallerPID(ppid=8000)=%d, want 9801 (claude)", got)
	}
}

// TestBestEffortCallerPID_NoCLIReturnsZero: a caller whose ancestry contains
// no real CLI resolves to 0.
func TestBestEffortCallerPID_NoCLIReturnsZero(t *testing.T) {
	commOf, ppidOf := synthTree(map[int]synthProc{
		5000: {comm: "zsh", ppid: 4000},
		4000: {comm: "tmux", ppid: 1},
	})
	got := bestEffortCallerPID(5000, commOf, ppidOf)
	if got != 0 {
		t.Fatalf("bestEffortCallerPID=%d, want 0 (no CLI ancestor)", got)
	}
}

// TestIsCLIComm_StrictExcludesAdapters pins the strict predicate: it matches
// ONLY the real CLIs, NOT the adapter comms. This is the property the whole
// fix hinges on.
func TestIsCLIComm_StrictExcludesAdapters(t *testing.T) {
	cliMatches := []string{"claude", "codex"}
	for _, c := range cliMatches {
		if !isCLIComm(c) {
			t.Errorf("isCLIComm(%q)=false, want true (real CLI)", c)
		}
	}
	adapterComms := []string{
		"c3-claude-adapt", "c3-claude-adapter",
		"c3-codex-adapte", "c3-codex-adapter",
	}
	for _, c := range adapterComms {
		if isCLIComm(c) {
			t.Errorf("isCLIComm(%q)=true, want false (adapter must NOT match strict predicate)", c)
		}
	}
	for _, c := range []string{"bash", "zsh", "", "node"} {
		if isCLIComm(c) {
			t.Errorf("isCLIComm(%q)=true, want false", c)
		}
	}
}
