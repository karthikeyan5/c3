package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Equal.
		{"v1.0.0", "v1.0.0", 0},
		{"1.0.0", "v1.0.0", 0}, // leading v optional
		{"v1.2.3", "v1.2.3", 0},
		// Patch / minor / major ordering.
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.1.0", "v1.0.9", 1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.9.9", "v2.0.0", -1},
		// Missing fields treated as 0.
		{"v1.2", "v1.2.0", 0},
		{"v1", "v1.0.0", 0},
		{"v1.3", "v1.2.9", 1},
		// Prerelease sorts below its release core.
		{"v1.0.0", "v1.0.0-rc1", 1},
		{"v1.0.0-rc1", "v1.0.0", -1},
		{"v1.0.0-rc2", "v1.0.0-rc1", 1},
		{"v1.0.0-rc1", "v1.0.0-rc1", 0},
		// A higher core beats a prerelease of a lower core.
		{"v1.0.1", "v1.1.0-rc1", -1},
		{"v1.1.0-rc1", "v1.0.1", 1},
		// Build metadata is ignored.
		{"v1.0.0+abc", "v1.0.0", 0},
		// Dev / junk sorts lowest.
		{"dev", "v1.0.0", -1},
		{"", "v0.0.1", -1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("v1.0.1", "v1.0.0") {
		t.Error("v1.0.1 should be newer than v1.0.0")
	}
	if IsNewer("v1.0.0", "v1.0.0") {
		t.Error("equal versions: not newer")
	}
	if IsNewer("v1.0.0", "v1.0.1") {
		t.Error("older is not newer")
	}
	// A prerelease is never "newer" than its own release core.
	if IsNewer("v1.0.0-rc1", "v1.0.0") {
		t.Error("prerelease of same core is not newer than the release")
	}
}

func TestIsPrerelease(t *testing.T) {
	yes := []string{"v1.0.0-rc1", "v1.0.0-beta", "1.2.3-alpha.1", "v2.0.0-rc.2"}
	no := []string{"v1.0.0", "1.2.3", "v2.0.0", "v1.0.0+build.5"}
	for _, s := range yes {
		if !IsPrerelease(s) {
			t.Errorf("IsPrerelease(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsPrerelease(s) {
			t.Errorf("IsPrerelease(%q) = true, want false", s)
		}
	}
}

func TestCurrentAndIsDev(t *testing.T) {
	// The test binary is built without -ldflags injection, so Version is "".
	if !IsDev() {
		t.Error("uninjected test binary should report IsDev()=true")
	}
	if got := Current(); got != devVersion {
		t.Errorf("Current() = %q, want %q for a dev build", got, devVersion)
	}

	// Simulate an injected release version for the duration of the test.
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "v1.2.3"
	if IsDev() {
		t.Error("injected version should report IsDev()=false")
	}
	if got := Current(); got != "v1.2.3" {
		t.Errorf("Current() = %q, want v1.2.3", got)
	}
}

func TestIsDevString(t *testing.T) {
	for _, s := range []string{"", "dev", "  dev  ", " "} {
		if !IsDevString(s) {
			t.Errorf("IsDevString(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"v1.0.0", "1.0.0", "v0.0.1"} {
		if IsDevString(s) {
			t.Errorf("IsDevString(%q) = true, want false", s)
		}
	}
}
