// Package proctree is the single source of truth for resolving a CLI
// session's PID from a point in the Linux /proc process tree.
//
// Two distinct walks live here, both built on the same primitives:
//
//   - CLISessionPID(startPID): walk UP from startPID (inclusive) and return
//     the first ancestor whose comm is a REAL user CLI (claude / codex). The
//     broker calls this on a registered stub's PID. A Claude stub registers
//     with its ADAPTER's own pid (os.Getpid() inside c3-claude-adapter), whose
//     /proc comm is "c3-claude-adapt" (15-char truncation). The STRICT
//     predicate isCLIComm deliberately does NOT match the adapter comm, so the
//     walk skips the adapter and returns the real claude/codex ancestor — the
//     pid the slash-command caller actually resolves.
//
//   - BestEffortCallerPID(): the walk a transient CLI subprocess (c3-broker
//     ping / sessions) does to guess the calling CLI session's pid. Starts at
//     os.Getppid() (our shell) and climbs to the first real-CLI ancestor.
//
// Why the STRICT predicate matters (the bug this package fixes): the old
// cmd/c3-broker isUserCLIComm matched BOTH the CLIs AND the adapter comms. A
// walk up from a stub's adapter pid would then STOP AT THE ADAPTER ITSELF and
// return the adapter pid — which never equals the caller's resolved claude
// pid, so `c3-broker ping` reported "not attached" even when attached.
package proctree

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// maxWalkDepth caps every ancestor walk so a pathological /proc tree (or a
// pid-reuse cycle) can never loop unbounded.
const maxWalkDepth = 10

// isCLIComm is the STRICT predicate: it matches ONLY the real user CLIs the
// session pid can belong to — "claude" and "codex". It deliberately does NOT
// match the c3 adapter comms ("c3-claude-adapt"/"c3-codex-adapte" and their
// untruncated forms). That exclusion is the whole point: an ancestor walk
// from a stub's adapter pid must skip the adapter and reach the real CLI.
func isCLIComm(comm string) bool {
	switch comm {
	case "claude", "codex":
		return true
	}
	return false
}

// cliSessionPID walks UP from startPID (INCLUSIVE) using ppidOf and returns
// the first pid whose comm satisfies isCLIComm. Depth-capped; returns 0 if no
// CLI ancestor is found within the cap, the chain reaches init/0, or a lookup
// fails. Pure: takes the /proc accessors as parameters so it is testable
// against a synthetic tree without a real /proc.
func cliSessionPID(startPID int, commOf func(int) string, ppidOf func(int) (int, bool)) int {
	pid := startPID
	for i := 0; i < maxWalkDepth; i++ {
		if pid <= 1 {
			return 0
		}
		if isCLIComm(commOf(pid)) {
			return pid
		}
		next, ok := ppidOf(pid)
		if !ok {
			return 0
		}
		if next == pid {
			return 0 // loop guard (shouldn't happen)
		}
		pid = next
	}
	return 0
}

// bestEffortCallerPID walks UP from startPPID (INCLUSIVE — startPPID is meant
// to be the caller's os.Getppid()) and returns the first CLI ancestor. Same
// strict predicate, same depth cap. Returns 0 if none found. Pure; the public
// BestEffortCallerPID wires in the real /proc accessors.
func bestEffortCallerPID(startPPID int, commOf func(int) string, ppidOf func(int) (int, bool)) int {
	return cliSessionPID(startPPID, commOf, ppidOf)
}

// CLISessionPID resolves the real CLI session pid for a registered stub's pid
// by walking the live /proc tree. For a Claude stub registered under the
// adapter pid, this returns the claude ancestor (skipping the adapter); for a
// stub registered directly under the CLI pid, it returns that pid unchanged.
// Returns 0 on non-Linux or when no CLI ancestor is found.
func CLISessionPID(startPID int) int {
	if runtime.GOOS != "linux" {
		return 0
	}
	return cliSessionPID(startPID, ProcComm, procPPID)
}

// BestEffortCallerPID resolves the calling CLI session's pid for a transient
// CLI subprocess (c3-broker ping / sessions). It walks up from os.Getppid()
// to the first real-CLI ancestor. On non-Linux it degrades to a single-level
// os.Getppid() guess (the broker matches that pid against its stub registry,
// so even a single-level guess is useful); 0 only if the Linux walk finds no
// CLI ancestor.
func BestEffortCallerPID() int {
	if runtime.GOOS != "linux" {
		// Single-level fallback. The shell-out chain typically lands at the
		// shell pid, not the CLI — best-effort only, tolerable degradation.
		return os.Getppid()
	}
	return bestEffortCallerPID(os.Getppid(), ProcComm, procPPID)
}

// ProcComm returns the "Name:" field from /proc/<pid>/status (the
// 15-char-truncated process comm). Empty string if /proc is missing or the
// file can't be read. Exported so callers that need the raw comm (and tests)
// can reuse the one canonical reader.
func ProcComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		}
	}
	return ""
}

// procPPID returns the "PPid:" field from /proc/<pid>/status. ok=false if the
// process has exited or /proc is missing.
func procPPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			s := strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))
			n, err := strconv.Atoi(s)
			if err != nil {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}
