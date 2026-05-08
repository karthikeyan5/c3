package mappings

import (
	"os"
	"path/filepath"
)

// DefaultPath returns the canonical mappings.json location:
//
//	$XDG_CONFIG_HOME/c3/mappings.json     (if set)
//	<UserHomeDir>/.config/c3/mappings.json (otherwise)
//
// Uses os.UserHomeDir() instead of os.Getenv("HOME") so an unset HOME does not
// produce "/.config/c3/mappings.json". Returns ("", err) if neither is
// resolvable — the caller surfaces the error rather than producing a path
// under "/" by accident.
func DefaultPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "c3", "mappings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "c3", "mappings.json"), nil
}
