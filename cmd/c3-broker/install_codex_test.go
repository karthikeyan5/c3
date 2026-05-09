package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCodexShimsCreatesLocalAndNVMSymlinks(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "go", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nvmBin := filepath.Join(home, ".nvm", "versions", "node", "v20.19.0", "bin")
	if err := os.MkdirAll(nvmBin, 0o755); err != nil {
		t.Fatal(err)
	}

	installed, err := installCodexShims(home, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 {
		t.Fatalf("installed %d shims, want 2: %#v", len(installed), installed)
	}
	for _, path := range []string{
		filepath.Join(home, ".local", "bin", "codex"),
		filepath.Join(nvmBin, "codex"),
	} {
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatalf("readlink %s: %v", path, err)
		}
		if target != launcher {
			t.Fatalf("%s -> %s, want %s", path, target, launcher)
		}
	}
}
