//go:build windows

package osutil

import (
	"fmt"
	"os"
	"syscall"
)

// _PROCESS_QUERY_LIMITED_INFORMATION is the access right needed to probe a
// process's existence (Vista+). Not exported by the std syscall package.
const _PROCESS_QUERY_LIMITED_INFORMATION = 0x1000

// ReloadSignals returns the signals that mean "reload config". Windows has no
// SIGHUP, so there are none.
func ReloadSignals() []os.Signal { return nil }

// IsReloadSignal reports whether s is a config-reload signal. Never on Windows.
func IsReloadSignal(os.Signal) bool { return false }

// SignalReload asks a process to reload its config. Windows has no SIGHUP-style
// reload; callers should restart the broker (or use the IPC reload path).
func SignalReload(int) error {
	return fmt.Errorf("config reload via signal is not supported on Windows; restart the broker")
}

// TerminateProcess terminates the target process (no graceful SIGTERM on Windows).
func TerminateProcess(pid int) error {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}

// ProcessSignalable reports whether pid names a live process we can open — the
// Windows analogue of the unix kill(pid,0) liveness probe. Same pid-reuse caveat.
func ProcessSignalable(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(_PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}
