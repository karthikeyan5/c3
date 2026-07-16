//go:build windows

package stt

import (
	"os/exec"
	"syscall"
)

// sttSysProcAttr puts the handler in its own process group so it doesn't share
// the broker's console-signal fate.
func sttSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// sttKillTree kills the direct child (best-effort). Tree-kill (grandchildren)
// via `taskkill /T` is a future refinement; on Windows the process-group SIGKILL
// trick isn't available.
func sttKillTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
