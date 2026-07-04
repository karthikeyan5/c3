package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestParseSHA256SUMS(t *testing.T) {
	body := "" +
		"aaaa... placeholder replaced below\n"
	// Build a realistic SHA256SUMS with two-space (binary-mode) separators.
	h1 := sha256Hex([]byte("tarball-one"))
	h2 := sha256Hex([]byte("tarball-two"))
	body = h1 + "  c3_v1.0.0_linux_amd64.tar.gz\n" +
		h2 + " *c3_v1.0.0_darwin_arm64.tar.gz\n" + // single-space + '*' binary marker
		"\n" + // blank line skipped
		"not-a-valid-line\n" + // no separator → skipped
		"deadbeef  too-short-digest.tar.gz\n" // <64 hex → skipped

	got := ParseSHA256SUMS([]byte(body))
	if got["c3_v1.0.0_linux_amd64.tar.gz"] != h1 {
		t.Errorf("linux amd64 digest = %q, want %q", got["c3_v1.0.0_linux_amd64.tar.gz"], h1)
	}
	if got["c3_v1.0.0_darwin_arm64.tar.gz"] != h2 {
		t.Errorf("darwin arm64 digest = %q, want %q", got["c3_v1.0.0_darwin_arm64.tar.gz"], h2)
	}
	if _, ok := got["too-short-digest.tar.gz"]; ok {
		t.Error("short digest line should have been skipped")
	}
	if len(got) != 2 {
		t.Errorf("parsed %d entries, want 2: %v", len(got), got)
	}
}

func TestVerifyChecksum_Good(t *testing.T) {
	dir := t.TempDir()
	data := []byte("the release tarball bytes")
	p := writeFile(t, dir, "asset.tar.gz", data)
	if err := VerifyChecksum(p, sha256Hex(data)); err != nil {
		t.Fatalf("good checksum should pass: %v", err)
	}
	// Case-insensitivity: uppercase want must still match.
	up := ""
	for _, c := range sha256Hex(data) {
		if c >= 'a' && c <= 'f' {
			up += string(c - 32)
		} else {
			up += string(c)
		}
	}
	if err := VerifyChecksum(p, up); err != nil {
		t.Fatalf("uppercase want should match: %v", err)
	}
}

func TestVerifyChecksum_Bad(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "asset.tar.gz", []byte("real bytes"))
	wrong := sha256Hex([]byte("different bytes"))
	if err := VerifyChecksum(p, wrong); err == nil {
		t.Fatal("mismatched checksum must error")
	}
}

func TestVerifyChecksum_Missing(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "asset.tar.gz", []byte("bytes"))
	// Empty / malformed want.
	if err := VerifyChecksum(p, ""); err == nil {
		t.Error("empty want must error")
	}
	if err := VerifyChecksum(p, "not-hex"); err == nil {
		t.Error("non-hex want must error")
	}
	// Missing file.
	if err := VerifyChecksum(filepath.Join(dir, "nope.tar.gz"), sha256Hex([]byte("x"))); err == nil {
		t.Error("missing file must error")
	}
}
