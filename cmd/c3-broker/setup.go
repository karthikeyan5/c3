package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/karthikeyan5/c3/internal/mappings"
)

// runSetup is the c3-broker setup subcommand. Interactive: prompts on
// stdin/stdout for bot token, DM chat id, group chat id (named).
//
// Validates the bot token via Telegram getMe BEFORE writing mappings.json.
// On success writes the file at mode 0600.
//
// If a config already exists, asks before overwriting.
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

	fmt.Print("Telegram bot token (from @BotFather): ")
	token, err := readPassword(r)
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	fmt.Println() // newline after the silent prompt
	if token == "" {
		return errors.New("bot token is required")
	}

	// Validate via getMe BEFORE writing.
	username, err := validateBotToken(token)
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	fmt.Printf("✓ token valid; bot is @%s\n", username)

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
	if err := os.MkdirAll(filepath.Dir(mfPath), 0700); err != nil {
		return fmt.Errorf("mkdir mappings parent: %w", err)
	}
	if err := mappings.Write(mfPath, mf); err != nil {
		return fmt.Errorf("write mappings: %w", err)
	}

	fmt.Printf("✓ wrote %s (mode 0600)\n", mfPath)

	// Optional: STT (voice transcription) — most users want this since
	// voice messages are the primary input channel for c3.
	if err := promptSTTSetup(r); err != nil {
		// Non-fatal: STT setup is optional. Surface the error and keep
		// the mappings.json write that already succeeded.
		fmt.Fprintf(os.Stderr, "warning: STT setup skipped: %v\n", err)
	}

	fmt.Println()
	fmt.Println("Restart your Claude Code session for the broker to pick up the new config.")
	return nil
}

// promptSTTSetup asks the user if they want voice transcription, and if
// so collects API keys for the bundled provider chain (Gemini via
// OpenRouter as the default first, Sarvam as fallback). Writes a 0600
// env file at ~/.claude/stt.env that the broker's STT subprocess
// inherits.
//
// Also tells the user where their personal vocabulary file lives and
// the standing pattern for adding terms as they encounter STT mistakes.
func promptSTTSetup(r *bufio.Reader) error {
	fmt.Println()
	fmt.Println("Voice transcription setup (optional)")
	fmt.Println("c3 ships a provider-chain STT pipeline (Gemini 3 Flash → Sarvam Saaras v3).")
	fmt.Println("Voice messages from Telegram get transcribed and surfaced to the CLI as text.")
	fmt.Println()
	fmt.Print("Set up STT? [Y/n]: ")
	yes := readBoolDefault(r, true)
	if !yes {
		fmt.Println("Skipping STT setup. Voice messages will surface as `[STT FAILED: handler_missing]` until you configure it.")
		return nil
	}

	envPath := filepath.Join(os.Getenv("HOME"), ".claude", "stt.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(envPath), err)
	}

	fmt.Println()
	fmt.Println("Provide API keys for at least one provider. Empty = skip that provider.")
	fmt.Println("  • OpenRouter (Gemini 3 Flash): https://openrouter.ai/keys")
	fmt.Println("  • Sarvam (Saaras v3, good for Indic languages): https://dashboard.sarvam.ai")
	fmt.Println()

	fmt.Print("OPENROUTER_API_KEY (leave blank to skip): ")
	openrouter, err := readPassword(r)
	if err != nil {
		return fmt.Errorf("read OPENROUTER_API_KEY: %w", err)
	}
	fmt.Println()

	fmt.Print("SARVAM_API_KEY (leave blank to skip): ")
	sarvam, err := readPassword(r)
	if err != nil {
		return fmt.Errorf("read SARVAM_API_KEY: %w", err)
	}
	fmt.Println()

	if openrouter == "" && sarvam == "" {
		fmt.Println("No keys provided — skipping STT env file write.")
		return nil
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

	if err := os.WriteFile(envPath, []byte(sb.String()), 0600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	fmt.Printf("✓ wrote %s (mode 0600)\n", envPath)

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
	return nil
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
