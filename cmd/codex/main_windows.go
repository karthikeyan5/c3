//go:build windows

package main

import (
	"fmt"
	"os"
)

// The C3 Codex launcher is unix-only (it manages a /tmp-hosted app-server keyed
// on the unix uid). Windows is not a supported target for it; this stub lets the
// module cross-compile while making the unsupported status explicit at runtime.
func main() {
	fmt.Fprintln(os.Stderr, "c3 codex launcher is not supported on Windows")
	os.Exit(1)
}
