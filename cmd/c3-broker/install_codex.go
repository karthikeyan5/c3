package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runInstallCodexShim() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	launcher := filepath.Join(filepath.Dir(exe), "codex")
	if _, err := os.Stat(launcher); err != nil {
		return fmt.Errorf("codex launcher not found next to c3-broker at %s; run `go install ./cmd/...` first", launcher)
	}
	installed, err := installCodexShims(home, launcher)
	if err != nil {
		return err
	}
	for _, path := range installed {
		fmt.Printf("%s -> %s\n", path, launcher)
	}
	return nil
}

func installCodexShims(home, launcher string) ([]string, error) {
	targets := []string{filepath.Join(home, ".local", "bin", "codex")}
	nvmBins, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin"))
	if err != nil {
		return nil, err
	}
	for _, bin := range nvmBins {
		targets = append(targets, filepath.Join(bin, "codex"))
	}

	installed := make([]string, 0, len(targets))
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return installed, err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return installed, err
		}
		if err := os.Symlink(launcher, target); err != nil {
			return installed, err
		}
		installed = append(installed, target)
	}
	return installed, nil
}
