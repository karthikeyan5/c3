package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/karthikeyan5/c3/internal/sessionhandoff"
)

// sessionHookInput is the subset of the SessionStart hook's stdin JSON we use.
// Claude Code sends {session_id, cwd, source, transcript_path, hook_event_name};
// extra fields are ignored.
type sessionHookInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Source    string `json:"source"`
}

// runSessionHook implements `c3-broker session-hook`, wired to the c3 plugin's
// SessionStart hook. It maps Claude Code's EPHEMERAL per-MCP-spawn instance id
// (the UUID directory in $CLAUDE_ENV_FILE — which equals the adapter's
// CLAUDE_CODE_SESSION_ID) to the STABLE, --resume-able session id (stdin
// `session_id`, == the transcript filename), writing a handoff the adapter then
// reads to ask the broker to re-attach the resumed session.
//
// CRITICAL: this NEVER touches the broker socket and ALWAYS returns nil (exit 0),
// even on bad input — a SessionStart hook that errors breaks the user's session.
// All "can't do it" cases (unset/malformed CLAUDE_ENV_FILE, empty session id,
// write failure) log a short note to stderr and exit 0 (fail-closed → no handoff
// → the adapter falls through to today's no-recovery behavior).
func runSessionHook() error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker session-hook: read stdin: %v (ignoring)\n", err)
		return nil
	}
	var in sessionHookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker session-hook: parse stdin: %v (ignoring)\n", err)
		return nil
	}

	instanceID := ""
	if env := os.Getenv("CLAUDE_ENV_FILE"); env != "" {
		instanceID = filepath.Base(filepath.Dir(env))
	}
	if instanceID == "" || instanceID == "." || instanceID == string(filepath.Separator) || in.SessionID == "" {
		fmt.Fprintf(os.Stderr, "c3-broker session-hook: no usable instance/session id (instance=%q session_present=%v) — nothing to do\n",
			instanceID, in.SessionID != "")
		return nil
	}

	if err := sessionhandoff.Write(instanceID, sessionhandoff.Entry{
		StableSessionID: in.SessionID,
		CWD:             in.CWD,
		Source:          in.Source,
		UnixNano:        time.Now().UnixNano(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker session-hook: write handoff: %v (ignoring)\n", err)
		return nil
	}
	return nil
}
