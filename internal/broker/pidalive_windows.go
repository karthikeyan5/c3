//go:build windows

package broker

import "syscall"

// _PROCESS_QUERY_LIMITED_INFORMATION is the minimal access right that lets us
// probe a process's existence (Vista+). Not exported by the std syscall package.
const _PROCESS_QUERY_LIMITED_INFORMATION = 0x1000

// isPIDAlive: a process exists if we can open a query handle to it. pid reuse has
// the same caveat as the unix Kill(0) check.
func isPIDAlive(pid int) bool {
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
