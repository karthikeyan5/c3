// c3-broker is the C3 daemon. It owns the unix socket, the in-memory
// routes/stubs registries, and (in subsequent phases) the channel modules.
//
// Singleton-per-machine via flock on $XDG_RUNTIME_DIR/c3-broker.pid (or
// fallback). Spawned by adapters via exec.Command + setsid; runs until its
// parent process group is killed or it receives SIGTERM/SIGINT.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	pidFile := broker.PidFilePath()
	lock, err := broker.AcquireSingleton(pidFile)
	if err != nil {
		// Sibling broker already running; expected when an adapter races and
		// we lose. Exit silently.
		return nil
	}
	defer lock.Release()

	mfPath, err := mappings.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve mappings path: %w", err)
	}
	var mf *mappings.MappingsFile
	mf, err = mappings.Read(mfPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Spec §5.1 first-install path: write a minimal skeleton, keep running.
			mf = &mappings.MappingsFile{
				SchemaVersion: 1,
				Channels:      map[string]mappings.ChannelConfig{},
				Mappings:      map[string]mappings.Mapping{},
			}
			if err := os.MkdirAll(filepath.Dir(mfPath), 0700); err != nil {
				return fmt.Errorf("mkdir mappings parent: %w", err)
			}
			if err := mappings.Write(mfPath, mf); err != nil {
				return fmt.Errorf("write skeleton mappings: %w", err)
			}
			fmt.Fprintf(os.Stderr, "c3-broker: wrote skeleton %s — run /c3-setup to configure\n", mfPath)
		} else {
			// Corruption recovery (spec §4.3): log and exit. No silent fallback.
			return fmt.Errorf("read mappings %s: %w", mfPath, err)
		}
	}
	if err := mf.Validate(); err != nil {
		return fmt.Errorf("validate mappings: %w", err)
	}

	br := broker.New(mf)
	srv, err := broker.Listen(broker.SocketPath(), br)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	fmt.Fprintf(os.Stderr, "c3-broker: listening on %s (pid %d)\n", broker.SocketPath(), os.Getpid())

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGTERM, syscall.SIGINT)
	<-sigC

	srv.Stop()
	return nil
}
