package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// runInstallAgy configures the host for C3 × Antigravity CLI:
//   - creates the plugin directory ~/.gemini/antigravity-cli/plugins/c3
//   - writes plugin.json, mcp_config.json, and hooks.json
//   - prints verification steps
func runInstallAgy(args []string) error {
	_ = args
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	pluginsDir := filepath.Join(home, ".gemini", "antigravity-cli", "plugins", "c3")
	if err := os.MkdirAll(pluginsDir, 0700); err != nil {
		return fmt.Errorf("failed to create plugin directory: %w", err)
	}

	// 1. Write plugin.json
	pluginJSON := `{
  "name": "c3",
  "version": "0.1.0",
  "description": "C3 — command every coding agent you run, from one chat. Multiplexes sessions onto one Telegram bot."
}
`
	if err := os.WriteFile(filepath.Join(pluginsDir, "plugin.json"), []byte(pluginJSON), 0644); err != nil {
		return fmt.Errorf("failed to write plugin.json: %w", err)
	}

	// 2. Write mcp_config.json
	mcpConfig := `{
  "mcpServers": {
    "c3": {
      "command": "c3-agy-adapter"
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(pluginsDir, "mcp_config.json"), []byte(mcpConfig), 0644); err != nil {
		return fmt.Errorf("failed to write mcp_config.json: %w", err)
	}

	// 3. Write hooks.json
	hooksJSON := `{
  "c3": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "c3-broker session-hook"
          }
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(filepath.Join(pluginsDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		return fmt.Errorf("failed to write hooks.json: %w", err)
	}

	fmt.Printf("Successfully installed C3 plugin for Antigravity CLI at:\n  %s\n\n", pluginsDir)

	// Verify adapter and broker on PATH.
	if p, err := lookPath("c3-agy-adapter"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: c3-agy-adapter not on PATH — run: make build && make install\n")
	} else {
		fmt.Printf("adapter: %s\n", p)
	}
	if p, err := lookPath("c3-broker"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: c3-broker not on PATH — run: make build && make install\n")
	} else {
		fmt.Printf("broker:  %s\n", p)
	}

	fmt.Println("\nNext:")
	fmt.Println("  # Start Antigravity CLI:")
	fmt.Println("  agy")
	fmt.Println("  # in session: attach name=<topic>")
	fmt.Println()
	return nil
}
