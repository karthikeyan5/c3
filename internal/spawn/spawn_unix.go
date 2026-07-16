//go:build !windows

package spawn

import "syscall"

// sysSetsid returns SysProcAttr with Setsid=true so the spawned child detaches
// from the caller's process group. Linux/Darwin only — the Windows sibling in
// spawn_windows.go uses CreationFlags instead.
func sysSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
