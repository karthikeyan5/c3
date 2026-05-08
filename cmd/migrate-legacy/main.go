// migrate-legacy ports the Python MVP's bot token (.env) and chat ids
// (mvp/config.json) into a fresh ~/.config/c3/mappings.json. Idempotent —
// refuses to overwrite an existing output file.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/karthikeyan5/c3/internal/mappings"
)

func main() {
	defaultOut, err := mappings.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate-legacy: cannot resolve default mappings path: %v\n", err)
		os.Exit(1)
	}
	envPath := flag.String("env", filepath.Join(os.Getenv("HOME"), ".claude", "channels", "telegram", ".env"), "path to legacy .env")
	cfgPath := flag.String("config", "mvp/config.json", "path to legacy mvp/config.json")
	outPath := flag.String("out", defaultOut, "path to write new mappings.json")
	flag.Parse()

	if err := migrate(*envPath, *cfgPath, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "migrate-legacy: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("migrate-legacy: wrote %s (mode 0600). Verify, then you can delete the old mvp/config.json.\n", *outPath)
}

func migrate(envPath, cfgPath, outPath string) error {
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing %s", outPath)
	}

	token, err := readEnvKey(envPath, "TELEGRAM_BOT_TOKEN")
	if err != nil {
		return fmt.Errorf("read env: %w", err)
	}
	if token == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN missing from %s", envPath)
	}

	cfg, err := readLegacyConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("read legacy config: %w", err)
	}

	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {
				BotToken:     token,
				DefaultGroup: "main",
				Groups: map[string]mappings.GroupConfig{
					"main": {ChatID: cfg.GroupChatID, Title: "(migrated)"},
				},
				DMChatID: cfg.DMChatID,
			},
		},
		Mappings: map[string]mappings.Mapping{},
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	return mappings.Write(outPath, mf)
}

func readEnvKey(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	prefix := key + "="
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):]), nil
		}
	}
	return "", sc.Err()
}

type legacyConfig struct {
	GroupChatID int64 `json:"group_chat_id"`
	DMChatID    int64 `json:"dm_chat_id"`
}

func readLegacyConfig(path string) (*legacyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg legacyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
