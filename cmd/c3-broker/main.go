// c3-broker is the C3 daemon and operational tool. Subcommands:
//
//	c3-broker             (default) — run as daemon
//	c3-broker setup       — interactive config; calls Telegram getMe; writes mappings.json
//	c3-broker status      — read-only health check
//	c3-broker validate    — parse + validate mappings.json
//	c3-broker release CWD — drop the claim on a route bound to CWD
//	c3-broker reload-config — re-read mappings.json without dropping live claims (running broker only)
//	c3-broker install-codex-shim — install Codex launcher symlinks
//	c3-broker install-claude-shim — install Claude Code launcher wrapper
//	c3-broker uninstall-claude-shim — remove the installed Claude Code wrapper
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

// Exit codes follow BSD sysexits(3) where applicable so shell scripts can
// branch on the cause. Generic runtime failures still use 1 (EX_GENERIC).
const (
	exitOK       = 0  // success
	exitFailure  = 1  // generic runtime error
	exitUsage    = 2  // unknown subcommand / malformed args
	exitDataErr  = 65 // EX_DATAERR — mappings.json invalid
	exitNoInput  = 66 // EX_NOINPUT — mappings.json missing/unreadable
	exitConfig   = 78 // EX_CONFIG — config-time failure (setup, install-codex-shim)
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "setup":
			if err := runSetup(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker setup: %v\n", err)
				os.Exit(exitConfig)
			}
			return
		case "status":
			if err := runStatus(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker status: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "topics":
			if err := runTopics(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker topics: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "validate":
			if err := runValidate(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker validate: %v\n", err)
				// validate's job is exactly to test config integrity, so
				// surface EX_DATAERR (invalid contents) when we can't
				// even read/parse it.
				os.Exit(exitDataErr)
			}
			return
		case "release":
			if err := runRelease(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker release: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "install-codex-shim":
			if err := runInstallCodexShim(); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker install-codex-shim: %v\n", err)
				os.Exit(exitConfig)
			}
			return
		case "pair":
			if err := runPair(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker pair: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "ping":
			if err := runPing(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker ping: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "sessions":
			if err := runSessions(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker sessions: %v\n", err)
				os.Exit(exitFailure)
			}
			return
		case "install-claude-shim":
			if err := runInstallClaudeShim(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker install-claude-shim: %v\n", err)
				os.Exit(exitConfig)
			}
			return
		case "uninstall-claude-shim":
			if err := runUninstallClaudeShim(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "c3-broker uninstall-claude-shim: %v\n", err)
				os.Exit(exitConfig)
			}
			return
		case "--help", "-h", "help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "c3-broker: unknown subcommand %q\n%s", os.Args[1], usage)
			os.Exit(exitUsage)
		}
	}

	if err := runDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "c3-broker: %v\n", err)
		os.Exit(exitFailure)
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
  c3-broker install-claude-shim [--force] [--path PATH]
                        Install a Claude Code wrapper at PATH (default
                        ~/.local/bin/claude) that auto-injects
                        --dangerously-load-development-channels plugin:c3@c3
                        when the user runs claude. Idempotent — preserves
                        the flag if already passed.
  c3-broker uninstall-claude-shim [--force] [--path PATH]
                        Remove the installed Claude Code wrapper.
  c3-broker pair [dm|group <chat_id>]
                        Arm a pairing window on the running broker and print
                        the generated 4-digit code. Default target is "dm".
  c3-broker ping        Send a one-shot "this is me" reply to the Telegram
                        route the calling session currently holds, so the
                        human reading Telegram can identify which CLI tab
                        owns the topic. Matches by CWD against live stubs.
  c3-broker sessions    List every live Claude Code / Codex session the
                        broker is currently tracking, with its CWD and
                        attached topic. Marks the calling session if it
                        can be matched via a parent-PID walk.
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

	// One-time startup write of the connectivity status file the Claude Code
	// status line reads. At boot lastHealth is empty ⇒ writes "{}", which
	// clears any stale file left by a prior crash without falsely asserting
	// up/down. Ambient-only (never via NotifyHealth — no spurious popup). Done
	// before channels start emitting so the empty boot snapshot can never race
	// a detection-driven write.
	br.WriteHealthFile()

	if cc, ok := mf.Channels["telegram"]; ok && cc.BotToken != "" {
		if err := br.RegisterChannel(telegram.New()); err != nil {
			return fmt.Errorf("register telegram channel: %w", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "c3-broker: no telegram bot_token in mappings.json — running without inbound transport")
	}

	// Default-deny posture (TODO #1): if no users are allowlisted yet,
	// auto-arm DM pairing so the operator can register their account
	// without manually editing mappings.json. The code is logged to
	// broker.log and echoed on stderr so the human sees it on a fresh
	// install. No auto re-arm on TTL expiry — manual /c3:pair after that.
	if code := br.AutoStartDMPairingIfEmpty(); code != "" {
		fmt.Fprintf(os.Stderr,
			"c3-broker: PAIRING — send `%s` to your bot within %v to pair (DM)\n",
			code, broker.PairTTL)
	}

	// Capability marker: tells `/c3:reload-config` we support SIGHUP-driven
	// config reload. Old brokers (pre-2026-05-15) lack this file and the
	// slash command refuses to fire — sending SIGHUP to a broker without
	// a handler terminates it (Go default) and indirectly kills the MCP
	// adapter via CC's recycle behavior. Rewritten on every startup;
	// removed at clean shutdown so a stale file from a crashed broker
	// doesn't falsely advertise capabilities for a future older broker.
	capsPath := broker.CapsFilePath()
	if err := os.WriteFile(capsPath, []byte("sighup-reload\npair-mode-start\n"), 0600); err != nil {
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
