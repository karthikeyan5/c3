package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// runtimeDir returns the per-user runtime directory for the broker's socket
// + pidfile. CRITICAL: must be deterministic across all broker invocations,
// regardless of the calling process's env.
//
// 2026-05-09 incident: two brokers spawned with different
// XDG_RUNTIME_DIR (one from a shell with the env set, one from a codex-side
// spawn without). The env-fallback `/tmp/c3-$UID.sock` was used by one
// while the other used `/run/user/$UID/c3.sock` — two listen sockets,
// two pollers, both 409'd against Telegram, and adapters scattered between
// them depending on each adapter's own env. Symptom: messages delivered to
// the wrong broker → fallback fired despite a valid claim on the other one.
//
// Resolution: probe `/run/user/$UID` directly first (the systemd-logind
// convention on every modern Linux distro), independent of env. Only fall
// back to `/tmp/c3-$UID/` if that path doesn't exist.
func runtimeDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, "AppData", "Local")
			} else {
				base = os.TempDir()
			}
		}
		dir := filepath.Join(base, "c3")
		_ = os.MkdirAll(dir, 0700)
		return dir
	}
	uid := os.Getuid()
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		// Use env if set, but check that it exists.
		if st, err := os.Stat(x); err == nil && st.IsDir() {
			return x
		}
	}
	// Independent probe of the canonical Linux per-user runtime dir.
	canonical := fmt.Sprintf("/run/user/%d", uid)
	if st, err := os.Stat(canonical); err == nil && st.IsDir() {
		return canonical
	}
	// Last resort: a per-uid tmp dir we create ourselves.
	tmp := fmt.Sprintf("/tmp/c3-%d", uid)
	_ = os.MkdirAll(tmp, 0700)
	return tmp
}

// SocketPath returns the broker's listening socket path. Deterministic
// across invocations (see runtimeDir for why this matters).
func SocketPath() string {
	return filepath.Join(runtimeDir(), "c3.sock")
}

// PidFilePath returns the broker's flock pid-file path. Same dir as the
// socket — single source of truth, no env-fork-induced split.
func PidFilePath() string {
	return filepath.Join(runtimeDir(), "c3-broker.pid")
}

// CapsFilePath returns the broker's capabilities-marker file path.
// Written by the broker at daemon startup with one capability per line
// (e.g. "sighup-reload\n"). The /c3:reload-config slash command reads
// this file to decide whether the running broker supports SIGHUP-driven
// config reload — sending SIGHUP to a pre-2026-05-15 broker terminates
// the process (Go's default handler) and indirectly kills the MCP
// adapter via CC's recycle behavior, so the slash command refuses to
// fire when the capability isn't advertised.
func CapsFilePath() string {
	return filepath.Join(runtimeDir(), "c3-broker.caps")
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0700)
}
