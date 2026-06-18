package broker

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// LogPath returns the broker log file path:
//
//	$C3_LOG_FILE          (override, if set)
//	$XDG_STATE_HOME/c3/broker.log  (preferred)
//	$HOME/.local/state/c3/broker.log  (fallback)
func LogPath() string {
	if env := os.Getenv("C3_LOG_FILE"); env != "" {
		return env
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "c3", "broker.log")
}

// HealthFilePath returns the path of the connectivity status file the Claude
// Code status line reads. Mirrors LogPath()'s XDG_STATE_HOME convention so it
// sits next to broker.log ($XDG_STATE_HOME/c3/health.json, fallback
// $HOME/.local/state/c3/health.json). C3_HEALTH_FILE overrides the path (a real
// runtime override, mirroring C3_LOG_FILE).
// IMPORTANT: do NOT use plugin_host.go:stateRoot() — it appends /state, which
// the status-line script does not look in.
func HealthFilePath() string {
	if env := os.Getenv("C3_HEALTH_FILE"); env != "" {
		return env
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "c3", "health.json")
}

// SetupLogging wires the stdlib log package to LogPath() (append mode) and
// also tees to stderr while a controlling tty exists. Returns the open file
// (caller defers Close) and the path. On any failure, falls back to stderr
// only and returns (nil, "").
//
// The log file is the durable signal — broker stderr is unreliable once the
// spawning adapter exits (its socket fd dangles). Always read the file when
// debugging.
//
// Content policy: log delivery metadata (chan / chat_id / topic_id / msg_id /
// kind / outcome) only. NEVER log message text, sender username, or anything
// the user typed. See DEBUGGING.md.
func SetupLogging() (*os.File, string) {
	path := LogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.SetOutput(os.Stderr)
		return nil, ""
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.SetOutput(os.Stderr)
		return nil, ""
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return f, path
}

// TopicKeyStr renders a RouteKey topic as "-" (no topic) or its int.
// Diagnostic only — used in log lines.
func TopicKeyStr(k RouteKey) string {
	if !k.HasTopic {
		return "-"
	}
	return formatInt64(k.TopicID)
}

// TopicPtrStr renders a *int64 topic as "-" or its int. Diagnostic only.
func TopicPtrStr(t *int64) string {
	if t == nil {
		return "-"
	}
	return formatInt64(*t)
}

// formatInt64 stringifies an int64 in base 10. Thin wrapper over
// strconv.FormatInt; the hand-rolled version this replaced was premature
// optimization (strconv is already imported in five sibling files in
// this package).
func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
