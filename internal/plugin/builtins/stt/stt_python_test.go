package stt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPythonExe_ExplicitConfigWins(t *testing.T) {
	// Even with a venv present, an explicit config value wins.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := pythonExe(Config{Python: "/opt/py/bin/python"}); got != "/opt/py/bin/python" {
		t.Fatalf("pythonExe with explicit Python = %q; want /opt/py/bin/python", got)
	}
}

func TestPythonExe_AutoDetectsVenv(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	venvPy := filepath.Join(cfgHome, "c3", "stt-venv", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(venvPy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(venvPy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pythonExe(Config{}); got != venvPy {
		t.Fatalf("pythonExe auto-detect = %q; want the venv python %q", got, venvPy)
	}
}

func TestPythonExe_FallsBackToPython3(t *testing.T) {
	// Empty config dir → no venv → bare python3.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := pythonExe(Config{}); got != "python3" {
		t.Fatalf("pythonExe with no venv = %q; want python3", got)
	}
}
