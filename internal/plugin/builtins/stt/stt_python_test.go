package stt

import (
	"testing"
)

func TestPythonExe_ExplicitConfigWins(t *testing.T) {
	if got := pythonExe(Config{Python: "/opt/py/bin/python"}); got != "/opt/py/bin/python" {
		t.Fatalf("pythonExe with explicit Python = %q; want /opt/py/bin/python", got)
	}
}

func TestPythonExe_FallsBackToPython3(t *testing.T) {
	// No explicit interpreter → bare python3. STT needs only the standard
	// library now, so there's no venv to auto-detect.
	if got := pythonExe(Config{}); got != "python3" {
		t.Fatalf("pythonExe with no override = %q; want python3", got)
	}
}
