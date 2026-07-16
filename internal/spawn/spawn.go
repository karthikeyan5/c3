// Package spawn centralises the detached-child launch helper shared by the
// C3 adapters (cmd/c3-claude-adapter, cmd/c3-codex-adapter). Both adapters
// fork a `c3-broker` when the socket is unreachable; the launch semantics
// (new session via setsid, no inherited stdio, async reap) must be identical
// across adapters, so the implementation lives here once instead of being
// duplicated verbatim in each adapter (D7 / recovery-hardening 2026-06-22).
package spawn

import (
	"os/exec"
)

// Detached starts cmd in a new session (detached from the caller's process
// group via setsid) with no inherited stdio, then reaps it asynchronously so
// a child that exits never lingers as a <defunct> zombie.
//
// Why reap: spawnBroker is racy by design — several adapters can call it at
// once, but the broker's singleton flock lets only ONE win; the losers exit
// within milliseconds. setsid creates a new SESSION but does NOT reparent the
// child to init, so until the calling process itself exits an unreaped loser
// stays a zombie child of the caller (the <defunct> accumulation observed
// 2026-06-22). The Wait() goroutine reaps it the moment it dies.
//
// Stderr is explicitly NOT inherited from the caller. The adapter's stderr is
// piped to the CLI host's plugin host; piping broker log lines through that
// channel made the plugin host appear distressed (lots of unexplained stderr
// noise during normal broker bounces), which we suspect contributes to the
// host closing the adapter's stdin during a broker restart. The broker has its
// own structured log at $XDG_STATE_HOME/c3/broker.log via SetupLogging; the
// host has no reason to see broker stderr.
func Detached(cmd *exec.Cmd) error {
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = sysSetsid()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap on exit so a broker that loses the flock race (or dies later) never
	// lingers as a <defunct> zombie. One goroutine per spawn; for the winning
	// broker it simply blocks for the daemon's lifetime, and if the caller
	// exits first the broker is reparented to init, which reaps it instead.
	go func() { _ = cmd.Wait() }()
	return nil
}
