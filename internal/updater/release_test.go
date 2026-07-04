package updater

import (
	"runtime"
	"testing"
)

// latestReleaseFixture is a trimmed real-shape GitHub "latest release" payload.
const latestReleaseFixture = `{
  "tag_name": "v1.2.0",
  "name": "v1.2.0",
  "draft": false,
  "prerelease": false,
  "assets": [
    {"name": "c3_v1.2.0_linux_amd64.tar.gz", "browser_download_url": "https://example.com/dl/c3_v1.2.0_linux_amd64.tar.gz", "size": 12345},
    {"name": "c3_v1.2.0_darwin_arm64.tar.gz", "browser_download_url": "https://example.com/dl/c3_v1.2.0_darwin_arm64.tar.gz", "size": 12340},
    {"name": "SHA256SUMS", "browser_download_url": "https://example.com/dl/SHA256SUMS", "size": 400}
  ]
}`

func TestParseRelease(t *testing.T) {
	rel, err := ParseRelease([]byte(latestReleaseFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rel.TagName != "v1.2.0" {
		t.Errorf("tag = %q, want v1.2.0", rel.TagName)
	}
	if rel.Draft || rel.Prerelease {
		t.Error("fixture is a stable release")
	}
	if got := rel.AssetURL("SHA256SUMS"); got != "https://example.com/dl/SHA256SUMS" {
		t.Errorf("SHA256SUMS url = %q", got)
	}
	if got := rel.AssetURL("c3_v1.2.0_linux_amd64.tar.gz"); got == "" {
		t.Error("linux amd64 asset url missing")
	}
	if got := rel.AssetURL("nonexistent"); got != "" {
		t.Errorf("nonexistent asset url = %q, want empty", got)
	}
}

func TestParseRelease_Errors(t *testing.T) {
	if _, err := ParseRelease([]byte("{not json")); err == nil {
		t.Error("malformed JSON must error")
	}
	if _, err := ParseRelease([]byte(`{"assets":[]}`)); err == nil {
		t.Error("missing tag_name must error")
	}
}

func TestUpdatable(t *testing.T) {
	stable := &Release{TagName: "v1.2.0"}
	if !updatable(stable, "v1.1.0") {
		t.Error("newer stable should be updatable")
	}
	if updatable(stable, "v1.2.0") {
		t.Error("equal version is not updatable")
	}
	if updatable(stable, "v1.3.0") {
		t.Error("older release is not updatable (no downgrade)")
	}
	// updatable() itself does NOT special-case dev: "dev" sorts as 0.0.0, so any
	// real release is "newer". The dev guard lives one level up in Update(), which
	// refuses to act on a dev current before ever calling updatable().
	if !updatable(stable, "dev") {
		t.Error("a real release sorts above a dev current at the updatable() layer")
	}
	// Prerelease + draft guards.
	if updatable(&Release{TagName: "v2.0.0-rc1"}, "v1.0.0") {
		t.Error("prerelease tag must not be updatable")
	}
	if updatable(&Release{TagName: "v2.0.0", Prerelease: true}, "v1.0.0") {
		t.Error("prerelease flag must not be updatable")
	}
	if updatable(&Release{TagName: "v2.0.0", Draft: true}, "v1.0.0") {
		t.Error("draft must not be updatable")
	}
}

func TestTarballName(t *testing.T) {
	if got := TarballNameFor("v1.0.0", "linux", "amd64"); got != "c3_v1.0.0_linux_amd64.tar.gz" {
		t.Errorf("TarballNameFor = %q", got)
	}
	// Current platform variant matches the runtime.
	want := "c3_v1.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if got := TarballName("v1.0.0"); got != want {
		t.Errorf("TarballName = %q, want %q", got, want)
	}
	if got := tarballDir("c3_v1.0.0_linux_amd64.tar.gz"); got != "c3_v1.0.0_linux_amd64" {
		t.Errorf("tarballDir = %q", got)
	}
}
