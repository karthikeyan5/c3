//go:build !windows

package main

import "syscall"

// execReplace replaces the current process image with the target binary
// (syscall.Exec), preserving stdin/stdout/stderr/tty and signal delivery —
// critical for an interactive TUI launcher.
func execReplace(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}
