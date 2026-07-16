//go:build windows

package spawn

import "syscall"

// _DETACHED_PROCESS detaches the child from the parent's console. Combined with
// CREATE_NEW_PROCESS_GROUP it is the Windows analogue of unix setsid: the child
// runs in its own process group with no controlling console.
const _DETACHED_PROCESS = 0x00000008

// sysSetsid returns SysProcAttr that detaches the spawned child from the
// caller's console + process group (the Windows analogue of Setsid).
func sysSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | _DETACHED_PROCESS}
}
