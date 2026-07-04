package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseSHA256SUMS parses the coreutils `sha256sum` output format that the
// release workflow uploads as the SHA256SUMS asset: one entry per line,
//
//	<64-hex>  <filename>
//
// (two spaces for a binary-mode entry; a single space or " *" is also
// tolerated). Returns a map of filename → lowercase hex digest. Malformed or
// blank lines are skipped. The filename is taken verbatim after the digest +
// separator, so a name with spaces is preserved.
func ParseSHA256SUMS(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Digest is the first whitespace-delimited field; the filename is the
		// remainder (after trimming the separator and an optional '*' binary
		// marker). Using Fields for the split then reconstructing keeps it robust
		// to one-or-two spaces.
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			continue
		}
		digest := strings.ToLower(strings.TrimSpace(line[:sp]))
		name := strings.TrimSpace(line[sp+1:])
		name = strings.TrimPrefix(name, "*") // sha256sum binary-mode marker
		name = strings.TrimSpace(name)
		if len(digest) != 64 || name == "" || !isHex(digest) {
			continue
		}
		out[name] = digest
	}
	return out
}

// VerifyChecksum streams the file at path through SHA-256 and compares the
// result to wantHex (case-insensitive). Returns nil only on an exact match; a
// mismatch, a missing/short want, or a read error is a hard error. This is the
// mandatory gate before any binary is swapped.
func VerifyChecksum(path, wantHex string) error {
	wantHex = strings.ToLower(strings.TrimSpace(wantHex))
	if len(wantHex) != 64 || !isHex(wantHex) {
		return fmt.Errorf("checksum: invalid expected digest %q", wantHex)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("checksum: open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("checksum: read %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHex {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", path, got, wantHex)
	}
	return nil
}

// isHex reports whether s is all lowercase hex digits.
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
