//go:build !windows

package broker

import "syscall"

// isPIDAlive returns true if a process with the given PID exists.
// Sends signal 0 (a no-op) and checks for ESRCH.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// ESRCH = no such process. Anything else (e.g. EPERM) means we can't
	// signal it but it's still alive.
	return err != syscall.ESRCH
}
