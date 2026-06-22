package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/karthikeyan5/c3/internal/mappings"
)

// printShimInstallFailure renders the structured failure surface for a
// failed compulsory claude-shim install. Writes the same block to BOTH
// stdout AND stderr so the user sees it regardless of whether setup is
// driven from a Claude Code agent (which captures stdout in its
// transcript and only intermittently surfaces stderr) or from a raw
// shell (which surfaces stderr more reliably but where the long setup
// transcript can bury a single line).
//
// The block is greppable via the literal header
// `[claude shim NOT installed]`. The actionable next-step (`c3-broker
// install-claude-shim --force`) and an explanation of what --force
// trades off are included in the same block so the user does not have
// to consult docs to recover.
//
// Closes M2 from the 2026-05-19 code review.
func printShimInstallFailure(stdout, stderr io.Writer, err error) {
	block := fmt.Sprintf(`[claude shim NOT installed]
  error: %v

  The shim is required for c3 channels to surface in Claude Code.
  Most common cause: an existing non-shim `+"`~/.local/bin/claude`"+`
  (e.g. one installed by NVM, npm, or a manual symlink to the real
  claude binary).

  To overwrite the existing file:
    c3-broker install-claude-shim --force

  --force overwrites a non-shim file at ~/.local/bin/claude. If this
  is the first time you have run install-claude-shim, the original
  claude symlink target may not have been persisted to
  ~/.config/c3/claude-shim.json yet, and --force will lose the path
  to your real claude binary. Verify a successful one-time install
  without --force first when possible — it persists the resolved
  real-claude path before the shim takes over.
`, err)
	// Write errors on stdout/stderr at this point in setup are
	// non-actionable: the process is about to print the restart
	// instruction and exit; a partial failure-block write is still
	// more useful than a panic. Match the rest of setup.go which
	// also ignores fmt.Fprintf errors on these streams.
	_, _ = io.WriteString(stdout, block)
	_, _ = io.WriteString(stderr, block)
}

// runSetup is the c3-broker setup subcommand. The flow (2026-05-19,
// items #4 + #5 of TODO install-feedback):
//
//  1. Existing-config check.
//  2. Educational preamble.
//  3. Consent gate ("Install C3 for you?"); skippable via C3_NO_PROMPT=1.
//  4. Bot token (only true prerequisite); validate via Telegram getMe.
//  5. Kick off `go install ./cmd/...` in a goroutine.
//  6. Walk user through the 6-step bot + group setup checklist.
//  7. Collect DM chat id + group name + group chat id.
//  8. Write mappings.json.
//  9. Join the background install.
//  10. promptSTTSetup, host-specific Codex / Claude shim installs.
//  11. Restart instruction.
//
// The goal: the user's reading time during steps 6 covers `go install`'s
// runtime, so wall-clock setup feels shorter.
func runSetup() error {
	mfPath, err := mappings.DefaultPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(mfPath); err == nil {
		fmt.Printf("Existing config found at %s. Overwrite? [y/N]: ", mfPath)
		if !readBool(false) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	r := bufio.NewReader(os.Stdin)

	// 2 + 3: preamble then consent gate. Consent honours C3_NO_PROMPT
	// so Codex's non-interactive setup path doesn't deadlock.
	printPreamble()
	if !confirmInstall(r) {
		fmt.Println()
		fmt.Println(consentDeclinedMsg)
		return nil
	}
	fmt.Println()

	// 4: bot token ask + validation. Must come before background
	// install kicks off — the install is meaningless if the user
	// abandons setup at the token step.
	fmt.Print("Telegram bot token (from @BotFather): ")
	token, err := readPassword(r)
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	fmt.Println() // newline after the silent prompt
	if token == "" {
		return errors.New("bot token is required")
	}

	// Validate via getMe BEFORE doing anything else.
	username, err := validateBotToken(token)
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	fmt.Printf("✓ token valid; bot is @%s\n", username)
	fmt.Println()

	// 5: kick off background `go install ./cmd/...`. The walk through
	// the bot+group checklist (~2-5 minutes of user reading) should
	// cover this comfortably; cold rebuild of C3 is ~30s, warm ~5s.
	//
	// Cancellable via ctx so a Ctrl-C during the interactive walk
	// doesn't leave a dangling go subprocess.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installCh := startBackgroundInstall(ctx)

	// 6: walk through the 6-step manual checklist inline.
	walkBotGroupChecklist(r)

	// 7: collect remaining identifiers (DM chat id, group name, group
	// chat id). These need the bot+group to exist already, which is
	// why they come AFTER the walk.
	fmt.Println()
	fmt.Print("Your Telegram user id (DM chat id, positive int — ask @userinfobot if unknown): ")
	dmChatID, err := readInt64(r)
	if err != nil {
		return fmt.Errorf("dm chat id: %w", err)
	}

	fmt.Print("Group name to use as the default for new topics (e.g. \"main\"): ")
	groupName := readLine(r)
	if groupName == "" {
		groupName = "main"
	}

	fmt.Printf("Chat id of the %q supergroup (negative int starting with -100): ", groupName)
	groupChatID, err := readInt64(r)
	if err != nil {
		return fmt.Errorf("group chat id: %w", err)
	}

	// 8: write mappings.json. Do this BEFORE joining the install
	// goroutine — even if `go install` failed, the user's already-
	// entered config is worth persisting (they don't want to re-enter
	// the bot token on retry).
	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {
				BotToken:     token,
				DefaultGroup: groupName,
				Groups: map[string]mappings.GroupConfig{
					groupName: {ChatID: groupChatID},
				},
				DMChatID:     dmChatID,
				MasterUserID: dmChatID,
			},
		},
		Mappings: map[string]mappings.Mapping{},
	}
	if err := os.MkdirAll(filepath.Dir(mfPath), 0o700); err != nil {
		return fmt.Errorf("mkdir mappings parent: %w", err)
	}
	if err := mappings.Write(mfPath, mf); err != nil {
		return fmt.Errorf("write mappings: %w", err)
	}

	fmt.Printf("✓ wrote %s (mode 0600)\n", mfPath)

	// 9: join the background install. Almost always instant after the
	// chat-id prompts, but if the user was fast we wait visibly.
	if err := joinBackgroundInstall(installCh); err != nil {
		// Non-fatal for the rest of the flow — mappings.json is
		// already written, the user can manually run `/c3:build` or
		// `go install ./cmd/...` after. Surface the error and continue.
		fmt.Fprintf(os.Stderr, "warning: background `go install` failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "  Run `/c3:build` (Claude) or `go install ./cmd/...` (Codex / plain shell) to retry.")
	}

	host := DetectHostCLI()

	// 10: optional STT setup (most users want this — voice is the
	// primary input channel for c3).
	sttWritten, sttErr := promptSTTSetup(r)
	if sttErr != nil {
		// Non-fatal: STT setup is optional. Surface the error and keep
		// the mappings.json write that already succeeded.
		fmt.Fprintf(os.Stderr, "warning: STT setup skipped: %v\n", sttErr)
	}

	// #9 (2026-05-18): when setup is driven from Codex CLI, register the
	// c3 MCP server into Codex's persistent config so the user doesn't
	// have to do it by hand. (The `codex` launcher shim already wires up
	// per-invocation config via -c flags, but that only kicks in when
	// the user launches Codex through the shim — a vanilla `codex` would
	// otherwise miss the MCP server entirely.) Idempotent: existing
	// [mcp_servers.c3_codex] section is left untouched.
	if host == HostCodex {
		path, didWrite, err := ensureCodexMCPRegistration()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: could not register C3 MCP server in %s: %v\n", path, err)
		case didWrite:
			fmt.Printf("✓ registered C3 MCP server in %s\n", path)
		default:
			fmt.Printf("✓ C3 MCP server already registered in %s\n", path)
		}

		// #11 (2026-05-18): Codex's MCP host does NOT read the
		// `instructions` field from the initialize response (parallel
		// agent investigation, openai/codex#6148 closed-not-planned).
		// Install the OUTPUT MODE PROTOCOL + MULTI-PART REPLY PROTOCOL
		// into ~/.codex/AGENTS.md instead — Codex concatenates that
		// file into developer_instructions. Idempotent: replaces the
		// delimited block on rerun, leaves the rest of the file alone.
		amdPath, amdWrote, amdErr := ensureCodexAgentsMd()
		switch {
		case amdErr != nil:
			fmt.Fprintf(os.Stderr, "warning: could not install C3 protocol block in %s: %v\n", amdPath, amdErr)
		case amdWrote:
			fmt.Printf("✓ installed C3 protocol block in %s\n", amdPath)
		default:
			fmt.Printf("✓ C3 protocol block already up to date in %s\n", amdPath)
		}
	}

	// #17 (2026-05-18): when setup is driven from Claude Code, install the
	// claude-shim symlink at ~/.local/bin/claude unconditionally. Karthi's
	// call: this is COMPULSORY, no prompt, no opt-out — the shim is the
	// only supported path for getting the dev-channels flag right, and a
	// failed/skipped install is the misconfiguration item #18 was meant to
	// catch (now closed as subsumed). Non-fatal if it fails (e.g. a real
	// `claude` binary already lives at the target path without a sentinel)
	// — surface a hint so the user can run install manually with --force.
	if err := maybeInstallClaudeShim(host); err != nil {
		// Compulsory under HostClaude — a silent skip is the exact
		// failure mode item #18 was meant to close. Print to BOTH
		// stdout and stderr so the agent transcript (stdout) and the
		// raw shell (stderr) both see the structured failure block.
		// Reasoning recorded in the 2026-05-19 code-review pass (item M2).
		printShimInstallFailure(os.Stdout, os.Stderr, err)
	}

	// #10 (2026-05-18): under Codex, setup typically runs without a
	// connected TTY (the agent invokes it programmatically), so the STT
	// prompts get auto-skipped with empty responses. Tell the user
	// explicitly when no STT env file was written so they don't end up
	// with voice messages silently failing.
	if !sttWritten {
		sttHint(host)
	}

	fmt.Println()
	switch host {
	case HostCodex:
		fmt.Println(codexRestartInstruction())
	default:
		fmt.Println(claudeRestartInstruction())
	}
	return nil
}

// installResult carries the outcome of the background `go install`.
type installResult struct {
	err      error
	output   []byte // combined stdout+stderr from `go install` (for failure reporting)
	duration time.Duration
	skipped  bool // true if source dir wasn't discoverable; not an error
}

// installRunFn is the package-level indirection through which
// startBackgroundInstall actually runs the build. Tests swap in a
// fake to assert ordering / propagate fake errors without touching
// the user's GOBIN. Same pattern as installClaudeShimFn.
var installRunFn = defaultInstallRun

// defaultInstallRun is the production implementation: locate the
// source dir, run `go install ./cmd/...` from it. Skipped (not an
// error) if the source dir can't be discovered — the user can
// rebuild manually after setup completes.
func defaultInstallRun(ctx context.Context) installResult {
	srcDir, ok := discoverSourceDir()
	if !ok {
		return installResult{skipped: true}
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", "install", "./cmd/...")
	cmd.Dir = srcDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return installResult{
		err:      err,
		output:   out,
		duration: time.Since(start),
	}
}

// startBackgroundInstall launches the install in a goroutine and
// returns a buffered channel that emits exactly one installResult
// when done. Buffered so the goroutine doesn't block if the join is
// late.
func startBackgroundInstall(ctx context.Context) <-chan installResult {
	ch := make(chan installResult, 1)
	go func() {
		ch <- installRunFn(ctx)
	}()
	return ch
}

// joinBackgroundInstall blocks on the channel, prints a status line,
// and returns any underlying error (nil on skip or success).
func joinBackgroundInstall(ch <-chan installResult) error {
	// Show a heartbeat if we're going to wait more than a tick — the
	// usual case is "user finished prompts after install completed"
	// so the receive is instant.
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	var result installResult
	select {
	case result = <-ch:
		// instant — let defer Stop() reclaim the timer
	case <-timer.C:
		fmt.Println("Waiting for `go install ./cmd/...` to finish in the background...")
		result = <-ch
	}
	if result.skipped {
		fmt.Println("(skipped background build — source dir not found near c3-broker; run `/c3:build` or `go install ./cmd/...` manually if you need fresh binaries)")
		return nil
	}
	if result.err != nil {
		tail := result.output
		const maxTail = 4096
		if len(tail) > maxTail {
			tail = tail[len(tail)-maxTail:]
		}
		fmt.Fprintf(os.Stderr, "go install failed after %s:\n%s\n", result.duration.Round(time.Millisecond), tail)
		return result.err
	}
	fmt.Printf("✓ binaries built (`go install ./cmd/...`, %s)\n", result.duration.Round(time.Millisecond))
	return nil
}

// discoverSourceDir locates the C3 source tree (one with go.mod
// module = github.com/karthikeyan5/c3) so `go install ./cmd/...` has
// a working directory.
//
// Resolution order:
//  1. $C3_SRC_DIR override (testing / unusual installs).
//  2. Walk up from os.Executable() looking for a matching go.mod.
//  3. ~/src/c3 (canonical clone path from docs/INSTALL.md).
//
// Returns ("", false) if nothing resolves — caller should skip the
// background build and tell the user to rebuild manually.
func discoverSourceDir() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("C3_SRC_DIR")); v != "" {
		if isC3SourceDir(v) {
			return v, true
		}
		return "", false
	}
	if exe, err := os.Executable(); err == nil {
		if dir := walkUpForC3GoMod(filepath.Dir(exe)); dir != "" {
			return dir, true
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		guess := filepath.Join(home, "src", "c3")
		if isC3SourceDir(guess) {
			return guess, true
		}
	}
	return "", false
}

// walkUpForC3GoMod ascends parent directories looking for a go.mod
// whose module declaration matches github.com/karthikeyan5/c3.
// Returns "" if none found within a small fixed budget (10 levels).
func walkUpForC3GoMod(start string) string {
	dir := start
	for i := 0; i < 10; i++ {
		if isC3SourceDir(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// isC3SourceDir returns true iff dir contains a go.mod whose module
// declaration is the C3 module path.
func isC3SourceDir(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
			return mod == "github.com/karthikeyan5/c3"
		}
	}
	return false
}

// walkBotGroupChecklist guides the user through the 6-step manual
// bot + group setup, one step at a time, with a "press enter when
// done" pause between steps. Mirrors the checklist in docs/INSTALL.md
// section "Prerequisites" — but inline, so the user reads it in the
// terminal rather than having to flip to a doc.
//
// If stdin is piped (non-interactive), we still print each step but
// don't pause — the agent driving setup will move on and the human
// reading the captured output can pause where they need to.
func walkBotGroupChecklist(r *bufio.Reader) {
	fmt.Println("Step-by-step: Telegram bot + group setup")
	fmt.Println()
	fmt.Println("(Use Telegram Desktop, iOS, Android, or macOS — NOT Telegram Web for")
	fmt.Println(" the group steps. Web's Topics + admin-rights UIs are incomplete.)")
	fmt.Println()
	steps := []struct {
		title string
		body  []string
	}{
		{
			"Create the bot (skip if you already did this above)",
			[]string{
				"  Message @BotFather → /newbot → pick a display name and a",
				"  username ending in 'bot'. Copy the HTTP token — that's what",
				"  you pasted earlier.",
			},
		},
		{
			"Disable privacy mode",
			[]string{
				"  Still in @BotFather: /setprivacy → pick your bot → Disable.",
				"  Without this, the bot only sees messages that mention or reply",
				"  to it. Cannot be done over the Bot API.",
			},
		},
		{
			"Create a Telegram group",
			[]string{
				"  A regular group is fine; it auto-promotes to a supergroup",
				"  when you turn Topics on in step 5.",
			},
		},
		{
			"Add your bot to the group",
			[]string{
				"  Group → members → Add → search the bot's @username.",
			},
		},
		{
			"Enable Topics in the group",
			[]string{
				"  Group settings → Topics → On. Do this BEFORE step 6 — the",
				"  \"Allow create topics\" admin right only appears once Topics",
				"  are enabled.",
			},
		},
		{
			"Promote the bot to admin with these rights",
			[]string{
				"  Manage Topics, Send Messages, Delete Messages, Pin Messages.",
				"  Everything else off.",
			},
		},
	}
	// Honor C3_NO_PROMPT in addition to the TTY check so Codex's
	// non-interactive setup path (which already bypasses the consent
	// gate via isNoPromptSet) also skips the step pauses. Without this,
	// a non-interactive driver that's piping prompts through stdin
	// gets a TTY-detection negative (good) but a different one if it's
	// faking a TTY via `script(1)` (bad). Closes report NIT n4
	// (2026-05-19).
	interactive := term.IsTerminal(int(syscall.Stdin)) && !isNoPromptSet()
	for i, s := range steps {
		fmt.Printf("%d. %s\n", i+1, s.title)
		for _, line := range s.body {
			fmt.Println(line)
		}
		fmt.Println()
		if interactive {
			fmt.Printf("   (press enter when done with step %d)", i+1)
			readLine(r)
			fmt.Println()
		}
	}
	fmt.Println("All 6 steps done. Now I need your DM chat id and the group's chat id.")
	fmt.Println("(DM chat id = your own user id, ask @userinfobot. Group chat id is a")
	fmt.Println(" negative integer starting with -100 — send any message in the group")
	fmt.Println(" and the bot will see it, or use @username_to_id_bot.)")
}

// sttHint prints a per-host instruction telling the user how to get STT
// keys wired up when the interactive prompt didn't capture any. Reasons
// it might not have:
//   - user said no to "Set up STT?"
//   - both API-key prompts were empty
//   - non-interactive caller (Codex agent driving stdin) — silently
//     skipped the whole branch
//
// Under Claude Code, the prompts usually surface fine, so this is brief.
// Under Codex, we walk the user through the manual file format because
// the interactive path is unreliable.
func sttHint(host HostCLI) {
	envPath := filepath.Join(os.Getenv("HOME"), ".claude", "stt.env")
	fmt.Println()
	if host == HostCodex {
		fmt.Printf("STT API keys were not wired up. Codex runs setup non-interactively so the\n")
		fmt.Printf("Y/N prompts above auto-skipped. To enable voice transcription, create %s\n", envPath)
		fmt.Println("(mode 0600) with at least one of:")
		fmt.Println()
		fmt.Println("  OPENROUTER_API_KEY=...   # Gemini 3 Flash (https://openrouter.ai/keys)")
		fmt.Println("  SARVAM_API_KEY=...       # Saaras v3 (https://dashboard.sarvam.ai)")
		fmt.Println("  ELEVENLABS_API_KEY=...   # optional, ElevenLabs Scribe v2")
		fmt.Println()
		fmt.Println("Or rerun `c3-broker setup` in a real terminal (e.g. plain shell, not via")
		fmt.Println("the Codex agent) and answer the STT prompts.")
		return
	}
	fmt.Printf("(STT keys not configured — voice messages will surface as `[STT FAILED: ...]`.\n")
	fmt.Printf(" Rerun `c3-broker setup` and answer the STT prompts, or hand-write %s.)\n", envPath)
}

// promptSTTSetup asks the user if they want voice transcription, and if
// so collects API keys for the bundled provider chain (Gemini via
// OpenRouter as the default first, Sarvam as fallback). Writes a 0600
// env file at ~/.claude/stt.env that the broker's STT subprocess
// inherits.
//
// Also tells the user where their personal vocabulary file lives and
// the standing pattern for adding terms as they encounter STT mistakes.
//
// Returns (written, err). written=true iff the env file was created with
// at least one key. The caller uses this to decide whether to print a
// post-setup hint reminding the user how to wire keys up manually.
func promptSTTSetup(r *bufio.Reader) (bool, error) {
	fmt.Println()
	fmt.Println("Voice transcription setup (optional)")
	fmt.Println("c3 ships a provider-chain STT pipeline (Gemini 3 Flash → Sarvam Saaras v3).")
	fmt.Println("Voice messages from Telegram get transcribed and surfaced to the CLI as text.")
	fmt.Println()
	fmt.Print("Set up STT? [Y/n]: ")
	yes := readBoolDefault(r, true)
	if !yes {
		fmt.Println("Skipping STT setup. Voice messages will surface as `[STT FAILED: handler_missing]` until you configure it.")
		return false, nil
	}

	envPath := filepath.Join(os.Getenv("HOME"), ".claude", "stt.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(envPath), err)
	}

	fmt.Println()
	fmt.Println("Provide API keys for at least one provider. Empty = skip that provider.")
	fmt.Println("  • OpenRouter (Gemini 3 Flash): https://openrouter.ai/keys")
	fmt.Println("  • Sarvam (Saaras v3, good for Indic languages): https://dashboard.sarvam.ai")
	fmt.Println()

	fmt.Print("OPENROUTER_API_KEY (leave blank to skip): ")
	openrouter, err := readPassword(r)
	if err != nil {
		return false, fmt.Errorf("read OPENROUTER_API_KEY: %w", err)
	}
	fmt.Println()

	fmt.Print("SARVAM_API_KEY (leave blank to skip): ")
	sarvam, err := readPassword(r)
	if err != nil {
		return false, fmt.Errorf("read SARVAM_API_KEY: %w", err)
	}
	fmt.Println()

	if openrouter == "" && sarvam == "" {
		fmt.Println("No keys provided — skipping STT env file write.")
		return false, nil
	}

	var sb strings.Builder
	sb.WriteString("# c3 STT API keys — written by `c3-broker setup`.\n")
	sb.WriteString("# Add more providers (e.g. ELEVENLABS_API_KEY) by hand if needed.\n")
	if openrouter != "" {
		sb.WriteString("OPENROUTER_API_KEY=" + openrouter + "\n")
	}
	if sarvam != "" {
		sb.WriteString("SARVAM_API_KEY=" + sarvam + "\n")
	}

	if err := os.WriteFile(envPath, []byte(sb.String()), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", envPath, err)
	}
	fmt.Printf("✓ wrote %s (mode 0600)\n", envPath)

	if sarvam != "" {
		fmt.Println()
		fmt.Println("STT Python deps (required for voice notes longer than ~30s):")
		fmt.Println("  The Sarvam batch path needs the `sarvamai` package in a dedicated venv")
		fmt.Println("  (the system python is often externally-managed / PEP 668). Run:")
		fmt.Println("      bash plugins/c3/stt/setup-venv.sh")
		fmt.Println("  C3 auto-detects ~/.config/c3/stt-venv/bin/python. Also install ffmpeg")
		fmt.Println("  (provides ffprobe) via your OS package manager.")
	}

	// Tell the user about the vocabulary override path. This is the
	// "standing instruction" Karthi asked for — agents should learn the
	// path during setup so they can prompt users to add words when STT
	// mishears something.
	vocabPath := filepath.Join(os.Getenv("HOME"), ".config", "c3", "stt-vocabulary.txt")
	fmt.Println()
	fmt.Println("Personal STT vocabulary (optional, recommended)")
	fmt.Printf("If transcription mishears your project / product / personal names, add\n")
	fmt.Printf("them to %s — one term per line. Format:\n", vocabPath)
	fmt.Println()
	fmt.Println("    # context: short description biases providers toward your domain")
	fmt.Println("    YourProjectName != mishearing1, mishearing2 -- optional note")
	fmt.Println("    YourName != commonly-misheard-as")
	fmt.Println()
	fmt.Println("As you use c3, watch for STT mistakes — those are signals to add terms.")
	fmt.Println("Agents using c3 are instructed to prompt you to add words when they see")
	fmt.Println("STT errors that look correctable. See plugins/c3/stt/stt-pkg/README.md")
	fmt.Println("for the full format.")
	return true, nil
}

// installClaudeShimFn is the package-level indirection through which
// maybeInstallClaudeShim invokes the shim installer. Production code
// points it at runInstallClaudeShim; tests swap in a fake to assert
// that setup invoked it (and with what args) without needing a real
// claude-shim launcher binary next to the test exe.
var installClaudeShimFn = runInstallClaudeShim

// maybeInstallClaudeShim runs the claude-shim installer with defaults
// (i.e. no flag args — default install path of ~/.local/bin/claude,
// no --force) when the host CLI is Claude Code. Other hosts (Codex,
// Unknown) are a no-op.
//
// This is the runSetup() Claude-host branch factored into a testable
// helper. See item #17 in TODO.md: the shim install is COMPULSORY
// under Claude Code per Karthi 2026-05-18 — no prompt, no opt-out.
func maybeInstallClaudeShim(host HostCLI) error {
	if host != HostClaude {
		return nil
	}
	return installClaudeShimFn(nil)
}

// readBoolDefault is readBool but uses the explicit default instead of
// the function literal's hard-coded fallback.
func readBoolDefault(r *bufio.Reader, def bool) bool {
	line := readLine(r)
	switch strings.ToLower(line) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// validateBotToken calls Telegram's getMe and returns the bot's username on
// success. Errors on 401 (invalid token), network failure, or non-OK
// responses.
func validateBotToken(token string) (string, error) {
	url := "https://api.telegram.org/bot" + token + "/getMe"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("network: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if !parsed.OK {
		return "", fmt.Errorf("telegram: %s", parsed.Description)
	}
	return parsed.Result.Username, nil
}

func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// readPassword reads a single line of input with terminal echo disabled
// when stdin is a TTY (so the bot token doesn't end up in scroll-back
// or screen-recording footage). Falls back to plain reads when stdin is
// piped/redirected (CI, automation), since there's no terminal to mute.
func readPassword(r *bufio.Reader) (string, error) {
	if term.IsTerminal(int(syscall.Stdin)) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return readLine(r), nil
}

func readBool(def bool) bool {
	r := bufio.NewReader(os.Stdin)
	line := readLine(r)
	switch strings.ToLower(line) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

func readInt64(r *bufio.Reader) (int64, error) {
	s := readLine(r)
	if s == "" {
		return 0, errors.New("empty input")
	}
	return strconv.ParseInt(s, 10, 64)
}
