package main

import "syscall"

// sysSetsid returns SysProcAttr with Setsid=true so the spawned broker
// detaches from our process group.
func sysSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
