package main

import "syscall"

// sysSetsid returns SysProcAttr with Setsid=true so the spawned broker
// detaches from our process group. Linux/Darwin only — Windows would need a
// different approach.
func sysSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
