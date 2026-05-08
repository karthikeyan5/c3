package broker

import (
	"fmt"
	"os"
	"path/filepath"
)

// SocketPath returns the broker's listening socket path:
//
//	$XDG_RUNTIME_DIR/c3.sock  (preferred — user-private tmpfs)
//	/tmp/c3-$UID.sock         (fallback)
//
// Spec §4.2.2: never bare /tmp/c3.sock, to avoid multi-user clobbering.
func SocketPath() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "c3.sock")
	}
	return fmt.Sprintf("/tmp/c3-%d.sock", os.Getuid())
}

// PidFilePath returns the broker's flock pid-file path:
//
//	$XDG_RUNTIME_DIR/c3-broker.pid  (preferred)
//	$HOME/.cache/c3/c3-broker.pid   (fallback)
//
// Spec §4.2.2.
func PidFilePath() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "c3-broker.pid")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "c3", "c3-broker.pid")
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0700)
}
