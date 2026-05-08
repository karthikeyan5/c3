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
	"time"

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
	token := readLine(r)
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
	fmt.Println("Restart your Claude Code session for the broker to pick up the new config.")
	return nil
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
