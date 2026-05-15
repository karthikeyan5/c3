// c3-broker is the C3 daemon and operational tool. Subcommands:
//
//	c3-broker             (default) — run as daemon
//	c3-broker setup       — interactive config; calls Telegram getMe; writes mappings.json
//	c3-broker status      — read-only health check
//	c3-broker validate    — parse + validate mappings.json
//	c3-broker release CWD — drop the claim on a route bound to CWD
//	c3-broker reload-config — re-read mappings.json without dropping live claims (running broker only)
//	c3-broker install-codex-shim — install Codex launcher symlinks
//
// Singleton-per-machine via flock on $XDG_RUNTIME_DIR/c3-broker.pid (or
// fallback). Spawned by adapters via exec.Command + setsid; runs until its
// parent process group is killed or it receives SIGTERM/SIGINT.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/karthikeyan5/c3/internal/broker"
	"github.com/karthikeyan5/c3/internal/channel/telegram"
	"github.com/karthikeyan5/c3/internal/mappings"
	"github.com/karthikeyan5/c3/internal/plugin"
	"github.com/karthikeyan5/c3/internal/plugin/builtins/stt"
)

// builtinPlugins lists the plugins compiled into the broker. Order matters
// for chain hooks (lower priority runs first); broker.RegisterBuiltinPlugins
// runs them in slice order.
var builtinPlugins = []broker.BuiltinPlugin{
	{Name: stt.Name, Register: func(h plugin.Host) error { return stt.Register(h) }},
}

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "setup":
			if err := runSetup(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker setup: %v\n", err)
				os.Exit(1)
			}
			return
		case "status":
			if err := runStatus(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker status: %v\n", err)
				os.Exit(1)
			}
			return
		case "topics":
			if err := runTopics(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker topics: %v\n", err)
				os.Exit(1)
			}
			return
		case "validate":
			if err := runValidate(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker validate: %v\n", err)
				os.Exit(1)
			}
			return
		case "release":
			if err := runRelease(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker release: %v\n", err)
				os.Exit(1)
			}
			return
		case "install-codex-shim":
			if err := runInstallCodexShim(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker install-codex-shim: %v\n", err)
				os.Exit(1)
			}
			return
		case "--help", "-h", "help":
			fmt.Print(usage)
			return
		}
	}

	if err := runDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker: %v\n", err)
		os.Exit(1)
	}
}

const usage = `c3-broker — C3 daemon and operational tool.

Usage:
  c3-broker             Run as daemon (singleton-per-machine).
  c3-broker setup       Interactive config — gather bot token, DM chat id,
                        group chat id; validate against Telegram; write
                        ~/.config/c3/mappings.json (mode 0600).
  c3-broker status      Read-only health check (broker, socket, mappings,
                        channels, plugins, live claims).
  c3-broker topics      List known topics + claim state (queries the
                        running broker via the unix socket).
  c3-broker validate [path]
                        Parse + validate mappings.json. Defaults to default
                        path. Exits 0 on valid, 1 on invalid.
  c3-broker release <cwd>
                        Drop the claim on a route bound to <cwd>.
                        (Runtime op against a running broker.)
  c3-broker install-codex-shim
                        Symlink the Go Codex launcher into ~/.local/bin and
                        Node-manager bin directories.
  c3-broker --help      This text.
`

func runDaemon() error {
	pidFile := broker.PidFilePath()
	lock, err := broker.AcquireSingleton(pidFile)
	if err != nil {
		// Sibling broker already running; expected when an adapter races and
		// we lose. Exit silently.
		return nil
	}
	defer lock.Release()

	logFile, logPath := broker.SetupLogging()
	if logFile != nil {
		defer logFile.Close()
		fmt.Fprintf(os.Stderr, "c3-broker: log file %s\n", logPath)
	}

	mfPath, err := mappings.DefaultPath()
	if err != nil {
		return fmt.Errorf("resolve mappings path: %w", err)
	}
	var mf *mappings.MappingsFile
	mf, err = mappings.Read(mfPath)
	if err != nil {
		if os.IsNotExist(err) {
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
			fmt.Fprintf(os.Stderr, "c3-broker: wrote skeleton %s — run `c3-broker setup` to configure\n", mfPath)
		} else {
			return fmt.Errorf("read mappings %s: %w", mfPath, err)
		}
	}
	if err := mf.Validate(); err != nil {
		return fmt.Errorf("validate mappings: %w", err)
	}

	br := broker.New(mf)

	// Register builtin plugins BEFORE channels start emitting inbound, so the
	// plugin pipeline is ready when first messages arrive.
	if err := br.RegisterBuiltinPlugins(builtinPlugins); err != nil {
		return fmt.Errorf("register plugins: %w", err)
	}

	if cc, ok := mf.Channels["telegram"]; ok && cc.BotToken != "" {
		if err := br.RegisterChannel(telegram.New()); err != nil {
			return fmt.Errorf("register telegram channel: %w", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "c3-broker: no telegram bot_token in mappings.json — running without inbound transport")
	}

	// Capability marker: tells `/c3:reload-config` we support SIGHUP-driven
	// config reload. Old brokers (pre-2026-05-15) lack this file and the
	// slash command refuses to fire — sending SIGHUP to a broker without
	// a handler terminates it (Go default) and indirectly kills the MCP
	// adapter via CC's recycle behavior. Rewritten on every startup;
	// removed at clean shutdown so a stale file from a crashed broker
	// doesn't falsely advertise capabilities for a future older broker.
	capsPath := broker.CapsFilePath()
	if err := os.WriteFile(capsPath, []byte("sighup-reload\n"), 0644); err != nil {
		log.Printf("warn: write caps file %s: %v", capsPath, err)
	}
	defer os.Remove(capsPath)

	srv, err := broker.Listen(broker.SocketPath(), br)
	if err != nil {
		// Sibling broker already serving the socket — same silent exit as
		// flock collision. 2026-05-09: prevents the
		// two-brokers-overlapping-on-the-socket bug after a restart race.
		if errors.Is(err, broker.ErrSiblingListening) {
			fmt.Fprintf(os.Stderr, "c3-broker: %v — exiting silently\n", err)
			return nil
		}
		return fmt.Errorf("listen on socket: %w", err)
	}
	fmt.Fprintf(os.Stderr, "c3-broker: listening on %s (pid %d)\n", broker.SocketPath(), os.Getpid())

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for sig := range sigC {
		if sig == syscall.SIGHUP {
			// Config reload — re-read mappings.json from disk and swap
			// the in-memory pointer. The /c3:reload-config slash command
			// sends this. Replaces the old /c3:restart-broker bounce
			// (which killed the adapter as a side effect — see
			// 2026-05-14 RESUME notes).
			newMF, err := mappings.Read(mfPath)
			if err != nil {
				log.Printf("SIGHUP: reload %s failed: %v — keeping existing config", mfPath, err)
				continue
			}
			if err := newMF.Validate(); err != nil {
				log.Printf("SIGHUP: validate %s failed: %v — keeping existing config", mfPath, err)
				continue
			}
			br.SetMappings(newMF)
			log.Printf("SIGHUP: reloaded mappings from %s (channels=%d, mappings=%d, plugins=%d)",
				mfPath, len(newMF.Channels), len(newMF.Mappings), len(newMF.Plugins))
			continue
		}
		// SIGTERM / SIGINT — shut down.
		log.Printf("received signal=%v, shutting down", sig)
		break
	}

	srv.Stop()
	br.Shutdown()
	return nil
}
