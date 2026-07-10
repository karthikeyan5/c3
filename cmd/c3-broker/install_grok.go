package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runInstallGrok configures the host for C3 × Grok Build:
//   - ensures [cli] use_leader = true (required for live inject)
//   - pins [mcp_servers.c3] command = c3-grok-adapter
//   - prints plugin install + reload instructions
//
// Non-destructive: only appends/updates the keys we own; leaves other config alone.
func runInstallGrok(args []string) error {
	_ = args
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(raw)
	text, changed := patchGrokConfig(text)
	if changed {
		if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
			return err
		}
		fmt.Printf("updated %s\n", cfgPath)
	} else {
		fmt.Printf("already configured: %s\n", cfgPath)
	}

	// Verify adapter on PATH.
	if p, err := lookPath("c3-grok-adapter"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: c3-grok-adapter not on PATH — run: go install ./cmd/c3-grok-adapter\n")
	} else {
		fmt.Printf("adapter: %s\n", p)
	}

	pluginSrc := findGrokPluginSource()
	fmt.Println()
	fmt.Println("Next:")
	if pluginSrc != "" {
		fmt.Printf("  grok plugin install %s --trust\n", pluginSrc)
	} else {
		fmt.Println("  grok plugin install <path-to-c3>/plugins/c3-grok --trust")
	}
	fmt.Println("  # ensure leader mode, then start Grok:")
	fmt.Println("  grok --leader")
	fmt.Println("  # in session: attach name=<topic>")
	fmt.Println()
	fmt.Println("After rebuilding c3-grok-adapter, reload MCP (/mcps) or restart the leader")
	fmt.Println("so the new binary is spawned (TUI restart alone keeps the old MCP process).")
	return nil
}

func patchGrokConfig(text string) (string, bool) {
	orig := text
	if text == "" {
		text = "# managed in part by c3-broker install-grok\n"
	}
	// use_leader
	if !strings.Contains(text, "use_leader") {
		if strings.Contains(text, "[cli]") {
			text = strings.Replace(text, "[cli]", "[cli]\nuse_leader = true", 1)
		} else {
			text += "\n[cli]\nuse_leader = true\n"
		}
	} else if strings.Contains(text, "use_leader = false") {
		text = strings.ReplaceAll(text, "use_leader = false", "use_leader = true")
	}

	// mcp_servers.c3 pin
	const block = `
# C3 Grok adapter (live Telegram inject via leader)
[mcp_servers.c3]
command = "c3-grok-adapter"
enabled = true
`
	if !strings.Contains(text, "[mcp_servers.c3]") {
		text += block
	} else if !strings.Contains(text, "c3-grok-adapter") {
		// crude replace of command line under that section is fragile; append a
		// comment and block if the wrong adapter is still referenced.
		text = strings.ReplaceAll(text, `command = "c3-claude-adapter"`, `command = "c3-grok-adapter"`)
		if !strings.Contains(text, "c3-grok-adapter") {
			text += block
		}
	}

	// plugins enabled
	if strings.Contains(text, "[plugins]") && !strings.Contains(text, `"c3"`) {
		if strings.Contains(text, "enabled = [") {
			// leave as-is; user may already list plugins
		} else {
			text += "\n[plugins]\nenabled = [\"c3\"]\n"
		}
	}

	return text, text != orig
}

func lookPath(file string) (string, error) {
	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, file)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found")
}

func findGrokPluginSource() string {
	// Prefer plugin next to this binary's source tree if installed from a checkout.
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// $GOBIN/c3-broker → walk up unlikely; try common arogara path.
	candidates := []string{
		filepath.Join(filepath.Dir(exe), "..", "arogara", "c3", "plugins", "c3-grok"),
		filepath.Join(os.Getenv("HOME"), "arogara", "c3", "plugins", "c3-grok"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, ".mcp.json")); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}
