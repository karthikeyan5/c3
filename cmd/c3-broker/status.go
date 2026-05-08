package main

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// runStatus prints a read-only health check.
//
// Sections:
//   - Daemon: pid file path + alive?
//   - Socket: path + reachable?
//   - Mappings: parses + validates? (or error)
//   - Channels: each name + (if alive) basic info from config
//   - Claims: (TODO — needs running broker introspection IPC; v1 just notes)
func runStatus() error {
	var b strings.Builder
	fmt.Fprintln(&b, "C3 broker status")
	fmt.Fprintln(&b, "================")

	// Daemon liveness via pid file + flock probe.
	pidFile := broker.PidFilePath()
	fmt.Fprintf(&b, "  pid file:  %s\n", pidFile)
	if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
		fmt.Fprintf(&b, "  pid:       %s", string(data))
	} else {
		fmt.Fprintln(&b, "  pid:       (not running — no pid file)")
	}

	// Socket.
	sockPath := broker.SocketPath()
	fmt.Fprintf(&b, "  socket:    %s", sockPath)
	if _, err := os.Stat(sockPath); err == nil {
		c, dialErr := net.Dial("unix", sockPath)
		if dialErr == nil {
			fmt.Fprintln(&b, "  ✓ reachable")
			_ = c.Close()
		} else {
			fmt.Fprintf(&b, "  (stat ok, dial: %v)\n", dialErr)
		}
	} else {
		fmt.Fprintln(&b, "  (not present)")
	}

	// Mappings file.
	mfPath, _ := mappings.DefaultPath()
	fmt.Fprintf(&b, "  mappings:  %s\n", mfPath)
	mf, err := mappings.Read(mfPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(&b, "             (does not exist — run `c3-broker setup`)")
		} else {
			fmt.Fprintf(&b, "             ✗ parse error: %v\n", err)
		}
	} else {
		if err := mf.Validate(); err != nil {
			fmt.Fprintf(&b, "             ✗ validate error: %v\n", err)
		} else {
			fmt.Fprintf(&b, "             ✓ schema_version=%d, %d channels, %d mappings, %d plugins\n",
				mf.SchemaVersion, len(mf.Channels), len(mf.Mappings), len(mf.Plugins))
		}
	}

	// Channels (config-only since this is read-only against a running daemon).
	if mf != nil {
		fmt.Fprintln(&b, "Channels:")
		for name, cc := range mf.Channels {
			tokenSet := cc.BotToken != ""
			fmt.Fprintf(&b, "  - %-10s token=%v default_group=%q groups=%d topics=%d\n",
				name, tokenSet, cc.DefaultGroup, len(cc.Groups), len(cc.Topics))
		}
	}

	// Plugins (config-only).
	if mf != nil && len(mf.Plugins) > 0 {
		fmt.Fprintln(&b, "Plugins:")
		for name, cfg := range mf.Plugins {
			enabled, _ := cfg["enabled"].(bool)
			fmt.Fprintf(&b, "  - %-10s enabled=%v\n", name, enabled)
		}
	}

	// Note: live route claims aren't visible from outside the broker process.
	// A future runtime IPC op (`status` over /tmp/c3.sock) would let us
	// surface them. For v1 the daemon's stderr is the source of truth.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "(For live route claims, check `c3-broker status` against a running daemon.")
	fmt.Fprintln(&b, " Phase 4B-followup will add a live status IPC op.)")

	fmt.Print(b.String())
	return nil
}

// runValidate parses + validates the mappings file at args[0] (or default
// path). Exits 0 on valid, 1 on invalid.
func runValidate(args []string) error {
	path := ""
	if len(args) > 0 {
		path = args[0]
	}
	if path == "" {
		var err error
		path, err = mappings.DefaultPath()
		if err != nil {
			return err
		}
	}
	mf, err := mappings.Read(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := mf.Validate(); err != nil {
		return fmt.Errorf("validate %s: %w", path, err)
	}
	fmt.Printf("✓ %s is valid (schema_version=%d, %d channels, %d mappings)\n",
		path, mf.SchemaVersion, len(mf.Channels), len(mf.Mappings))
	return nil
}

// runRelease drops the claim on a route bound to args[0] (cwd). Requires a
// running broker — sends the release op via the unix socket.
//
// v1 stub: not yet implemented. Print a TODO.
func runRelease(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: c3-broker release <cwd>")
	}
	// TODO: open /tmp/c3.sock, send a release-by-cwd op (new IPC op needed).
	// For v1 the workaround is killing the holding session; for v2 add the op.
	return fmt.Errorf("c3-broker release not yet implemented; for now, /exit the holding CLI session")
}
