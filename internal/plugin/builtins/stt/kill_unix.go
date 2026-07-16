//go:build !windows

package stt

import (
	"os/exec"
	"syscall"
)

// sttSysProcAttr makes the handler the leader of its OWN process group (Setpgid)
// so sttKillTree can signal the whole group — reaping the Python grandchild,
// ffprobe, and any in-flight provider HTTP together with the handler.
func sttSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// sttKillTree SIGKILLs the handler's entire process group (negative PID).
func sttKillTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
