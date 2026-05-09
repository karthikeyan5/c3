package broker

import (
	"io"
	"log"
	"os"
	"path/filepath"
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

func formatInt64(v int64) string {
	// Avoid pulling strconv into the broker package's hot paths; do it here.
	const max = 20
	var buf [max]byte
	i := max
	neg := v < 0
	if neg {
		v = -v
	}
	if v == 0 {
		return "0"
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
