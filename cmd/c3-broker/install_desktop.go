package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// runInstallDesktop configures Claude Desktop (Windows / macOS) to load the C3
// MCP adapter. It MERGES an `mcpServers.c3` entry into Claude Desktop's
// claude_desktop_config.json, preserving every other key and every other MCP
// server. The adapter is poll-only: Claude Desktop cannot push a Telegram
// message into a chat, so inbound sits in C3's durable queue and the user pulls
// it by asking Claude to call `fetch_queue`.
//
// Config path is per-OS (runtime.GOOS):
//   - windows: %APPDATA%\Claude\claude_desktop_config.json
//   - darwin:  ~/Library/Application Support/Claude/claude_desktop_config.json
//   - linux:   no Claude Desktop build exists; only a --config/--path override
//     lets you stage/test a file.
//
// An explicit `--config <path>` / `--path <path>` override takes precedence on
// every OS.
func runInstallDesktop(args []string) error {
	override := parseDesktopConfigOverride(args)

	cfgPath, note, err := desktopConfigPath(override)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Print(note)
	}
	if cfgPath == "" {
		// Linux, no override — nothing to write. desktopConfigPath already
		// printed the explanatory note via the caller above.
		return nil
	}

	// Resolve the adapter path. Claude Desktop's docs require an ABSOLUTE
	// command path, so we prefer the resolved PATH entry (made absolute); if the
	// binary isn't built/installed yet we fall back to the bare command name and
	// warn the user to edit it.
	adapterBin := exeName("c3-desktop-adapter")
	adapterPath := "c3-desktop-adapter" // bare fallback per contract
	adapterResolved, lookErr := lookPath(adapterBin)
	if lookErr == nil {
		if abs, aerr := filepath.Abs(adapterResolved); aerr == nil {
			adapterResolved = abs
		}
		adapterPath = adapterResolved
	}

	// Load-or-create the config, then MERGE — never clobber.
	cfg := map[string]any{}
	raw, rerr := os.ReadFile(cfgPath)
	switch {
	case rerr == nil:
		if len(strings.TrimSpace(string(raw))) > 0 {
			if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
				// Present but unparseable — protect the user's config; do NOT
				// overwrite it.
				return fmt.Errorf("existing Claude Desktop config is not valid JSON:\n  %s\n  (%v)\n"+
					"Refusing to overwrite it. Fix or remove the file, then re-run `c3-broker install-desktop`.", cfgPath, jerr)
			}
		}
	case os.IsNotExist(rerr):
		// Fresh install — create the parent dir; cfg stays an empty map.
		if mkErr := os.MkdirAll(filepath.Dir(cfgPath), 0o755); mkErr != nil {
			return fmt.Errorf("create config dir %s: %w", filepath.Dir(cfgPath), mkErr)
		}
	default:
		return fmt.Errorf("read %s: %w", cfgPath, rerr)
	}

	// Ensure an mcpServers object exists, preserving every other server.
	var servers map[string]any
	if existing, ok := cfg["mcpServers"]; ok {
		servers, ok = existing.(map[string]any)
		if !ok {
			return fmt.Errorf("existing %s has a non-object \"mcpServers\" value; refusing to modify it.\n"+
				"Fix the file, then re-run `c3-broker install-desktop`.", cfgPath)
		}
	} else {
		servers = map[string]any{}
	}
	servers["c3"] = map[string]any{"command": adapterPath}
	cfg["mcpServers"] = servers

	// json.MarshalIndent escapes backslashes in the (Windows) command path, so
	// the on-disk value is double-backslash-safe.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	fmt.Printf("Wrote Claude Desktop MCP config:\n  %s\n\n", cfgPath)
	fmt.Printf("Merged entry:\n  mcpServers.c3 = {\"command\": %q}\n\n", adapterPath)

	if lookErr != nil {
		fmt.Fprintf(os.Stderr,
			"warning: %s not found on PATH — wrote the bare command %q.\n"+
				"         Claude Desktop requires an ABSOLUTE command path (per its docs), so\n"+
				"         build/install the adapter (make build && make install) and re-run this,\n"+
				"         or hand-edit the c3 entry's \"command\" to the adapter's full path.\n",
			adapterBin, adapterPath)
	}

	// Next steps.
	fmt.Println("Next steps:")
	fmt.Println("  1. Fully QUIT Claude Desktop (tray icon → Quit — closing the window is not")
	fmt.Println("     enough) and restart it so it re-reads the config and spawns the c3 server.")
	fmt.Println("  2. In a chat, tell Claude:  attach name=<topic>")
	fmt.Println("     then \"check my messages\" to pull anything waiting.")
	fmt.Println()
	fmt.Println("  Inbound is POLL-ONLY. Claude Desktop cannot surface a Telegram message on its")
	fmt.Println("  own — messages wait in C3's durable queue. Ask Claude to \"check messages\"")
	fmt.Println("  (it calls fetch_queue) to pull them; reply/react to send back.")
	fmt.Println()
	fmt.Println("  Microsoft Store (MSIX) install? Edits to %APPDATA%\\Claude\\ are IGNORED —")
	fmt.Println("  the config that actually loads is under:")
	fmt.Println("    ...\\Packages\\Claude_*\\LocalCache\\Roaming\\Claude\\claude_desktop_config.json")
	fmt.Println("  Re-run with --config <that path>, or hand-edit it there.")
	fmt.Println()

	// Verify adapter and broker on PATH (mirrors install-agy's tail).
	if lookErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %s not on PATH — run: make build && make install\n", adapterBin)
	} else {
		fmt.Printf("adapter: %s\n", adapterResolved)
	}
	if p, err := lookPath(exeName("c3-broker")); err != nil {
		fmt.Fprintf(os.Stderr, "warning: c3-broker not on PATH — run: make build && make install\n")
	} else {
		fmt.Printf("broker:  %s\n", p)
	}

	return nil
}

// exeName appends the Windows executable suffix so PATH lookups match the real
// binary name on Windows (c3-desktop-adapter.exe) while staying bare elsewhere.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// desktopConfigPath resolves Claude Desktop's config path for the current OS.
// An explicit override always wins. Returns ("", note, nil) on Linux with no
// override — Claude Desktop has no Linux build, so there is nothing to write and
// the note explains why.
func desktopConfigPath(override string) (path string, note string, err error) {
	switch runtime.GOOS {
	case "windows":
		if override != "" {
			return override, "", nil
		}
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", "", fmt.Errorf("APPDATA is not set; cannot locate Claude Desktop config. Pass --config <path>.")
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json"), "", nil
	case "darwin":
		if override != "" {
			return override, "", nil
		}
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", herr
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), "", nil
	default:
		if override != "" {
			return override, "note: Claude Desktop has no Linux build — writing the --config override path\n" +
				"      for staging/testing only; it won't be read by a Claude Desktop app here.\n\n", nil
		}
		return "", "note: Claude Desktop is Windows/macOS only — there is no Linux build, so there\n" +
			"      is nothing to configure here. Pass --config <path> (or --path <path>) to\n" +
			"      stage/test a claude_desktop_config.json at an explicit location.\n", nil
	}
}

// parseDesktopConfigOverride pulls a `--config <path>` / `--path <path>` (or the
// `--config=<path>` / `--path=<path>` form) out of args. Empty if absent.
func parseDesktopConfigOverride(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "--path":
			if i+1 < len(args) {
				return args[i+1]
			}
		default:
			if v, ok := strings.CutPrefix(args[i], "--config="); ok {
				return v
			}
			if v, ok := strings.CutPrefix(args[i], "--path="); ok {
				return v
			}
		}
	}
	return ""
}
