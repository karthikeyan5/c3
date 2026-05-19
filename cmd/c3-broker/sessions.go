package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/karthikeyan5/c3/internal/ipc"
)

// runSessions is the `c3-broker sessions` subcommand. Lists every live
// adapter the broker is currently tracking with its attached state,
// marking the session that owns the calling terminal (best effort).
//
// Usage:
//
//	c3-broker sessions
//
// The matching slash command is /c3:sessions (TODO #19e, 2026-05-19).
// Mirrors `c3-broker ping` (cmd/c3-broker/ping.go).
func runSessions(_ []string) error {
	cwd, _ := os.Getwd()
	pid := bestEffortSessionPID()

	conn, err := dialBroker()
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.WriteJSON(ipc.ListSessionsReq{
		Op: ipc.OpListSessions, PID: pid, CWD: cwd,
	}); err != nil {
		return fmt.Errorf("write list_sessions: %w", err)
	}
	raw, err := conn.ReadFrame()
	if err != nil {
		return fmt.Errorf("read list_sessions_reply: %w", err)
	}
	var resp ipc.ListSessionsReplyMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("parse list_sessions_reply: %w", err)
	}
	fmt.Print(renderSessionsTable(resp.Sessions))
	return nil
}

// bestEffortSessionPID walks up the parent-PID chain looking for a
// process whose comm name indicates the user's CLI session (claude,
// codex, or their c3 adapters). Returns the matched PID, or 0 on
// failure (non-Linux, /proc missing, all ancestors exited, depth
// exceeded). On non-Linux platforms, returns os.Getppid() as a
// best-effort single-level fallback — the broker matches this PID
// against its stub registry, so even a single-level guess is useful
// when the slash command is invoked outside a shell-out wrapper.
func bestEffortSessionPID() int {
	if runtime.GOOS != "linux" {
		// Single-level fallback. Slash-command shells-out typically
		// land at the shell PID (sh / bash), not the CLI — so this is
		// best-effort only. Tolerable degradation.
		return os.Getppid()
	}
	// On Linux: walk up. The slash command shell-out chain looks like:
	//   claude (CLI session)
	//     └─ sh -c "c3-broker sessions"
	//          └─ c3-broker sessions   <- us
	// so getppid()==sh, not claude. Walk until we hit a known CLI
	// name. Depth cap so a deep tree doesn't loop unboundedly.
	const maxDepth = 10
	pid := os.Getppid()
	for i := 0; i < maxDepth; i++ {
		if pid <= 1 {
			return 0
		}
		comm := procComm(pid)
		if isUserCLIComm(comm) {
			return pid
		}
		next, ok := procPPID(pid)
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

// isUserCLIComm matches the /proc/<pid>/status "Name:" field (15-char
// truncated comm) against known CLI binaries the user might be
// running. Matches both the direct CLI (claude, codex) AND the c3
// adapters, in case the slash command runs inside an adapter process
// for some reason. Conservative — only matches names that are clearly
// the user's CLI, not e.g. "bash" or generic shell.
func isUserCLIComm(comm string) bool {
	switch comm {
	case "claude", "codex":
		return true
	case "c3-claude-adapt", "c3-claude-adapter": // 15-char truncation
		return true
	case "c3-codex-adapte", "c3-codex-adapter":
		return true
	}
	return false
}

// procComm returns the "Name:" field from /proc/<pid>/status (the
// 15-char-truncated process comm). Empty string if /proc is missing
// or the file can't be read.
func procComm(pid int) string {
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

// procPPID returns the "PPid:" field from /proc/<pid>/status. ok=false
// if the process has exited or /proc is missing.
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

// renderSessionsTable formats a slice of SessionEntry into the
// monospace-friendly table the slash command surfaces to the user.
// CWD is home-shortened to "~/..." for readability. Empty AttachedTo
// renders as "-". IsThisSession=true renders as "yes" in the This?
// column; false renders blank (keeps non-matching rows visually
// quiet so the "you are here" marker stands out).
func renderSessionsTable(sessions []ipc.SessionEntry) string {
	if len(sessions) == 0 {
		return "0 c3 sessions.\n"
	}

	home, _ := os.UserHomeDir()
	rows := make([][]string, 0, len(sessions))
	for _, s := range sessions {
		cwd := s.CWD
		if home != "" && strings.HasPrefix(cwd, home) {
			cwd = "~" + cwd[len(home):]
		}
		attached := s.AttachedTo
		if attached == "" {
			attached = "-"
		}
		thisMark := ""
		if s.IsThisSession {
			thisMark = "yes"
		}
		rows = append(rows, []string{
			s.CLI,
			strconv.Itoa(s.PID),
			cwd,
			attached,
			thisMark,
		})
	}

	headers := []string{"CLI", "PID", "CWD", "Attached", "This?"}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if l := len(c); l > widths[i] {
				widths[i] = l
			}
		}
	}

	var b strings.Builder
	noun := "sessions"
	if len(sessions) == 1 {
		noun = "session"
	}
	fmt.Fprintf(&b, "%d c3 %s:\n\n", len(sessions), noun)
	// Header row.
	writeRow(&b, headers, widths)
	// Body rows.
	for _, r := range rows {
		writeRow(&b, r, widths)
	}
	return b.String()
}

// writeRow renders one row with each cell left-padded to widths[i].
// Two-space separator between columns. The trailing column is
// written without padding so the final line doesn't carry trailing
// spaces.
func writeRow(b *strings.Builder, cells []string, widths []int) {
	for i, c := range cells {
		if i == len(cells)-1 {
			b.WriteString(c)
		} else {
			fmt.Fprintf(b, "%-*s  ", widths[i], c)
		}
	}
	b.WriteString("\n")
}
