package broker

import "testing"

func TestHealthFilePath_XDGStateHome(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "") // override off
	t.Setenv("XDG_STATE_HOME", "/tmp/xdgstate")
	if got := HealthFilePath(); got != "/tmp/xdgstate/c3/health.json" {
		t.Errorf("HealthFilePath with XDG set = %q, want /tmp/xdgstate/c3/health.json", got)
	}
}

func TestHealthFilePath_FallbackHome(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/tester")
	want := "/home/tester/.local/state/c3/health.json"
	if got := HealthFilePath(); got != want {
		t.Errorf("HealthFilePath fallback = %q, want %q", got, want)
	}
}

func TestHealthFilePath_EnvOverride(t *testing.T) {
	t.Setenv("C3_HEALTH_FILE", "/custom/h.json")
	if got := HealthFilePath(); got != "/custom/h.json" {
		t.Errorf("HealthFilePath override = %q, want /custom/h.json", got)
	}
}
