//go:build windows

package main

import (
	"os"
	"os/exec"
)

// execReplace runs the target as a child (Windows has no execve) and exits with
// the child's exit code, so from the caller's shell it behaves like a replace:
// stdin/stdout/stderr are inherited and the launcher returns the child's status.
func execReplace(path string, argv []string, env []string) error {
	c := exec.Command(path, argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr, c.Env = os.Stdin, os.Stdout, os.Stderr, env
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
