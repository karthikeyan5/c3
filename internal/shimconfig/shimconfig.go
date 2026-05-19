// Package shimconfig is the single source of truth for the
// claude-shim.json file shared between the c3-broker installer and
// the claude-shim runtime. Lives outside both binaries' main packages
// so each can import it without circular references.
//
// The file records the real-claude path that was at
// ~/.local/bin/claude before `c3-broker install-claude-shim` took
// the symlink over, so the shim runtime can still find the real
// binary after the symlink no longer points at it. See TODO.md #17.
package shimconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// currentSchemaVersion is the version Save writes today. Bump when the
// on-disk JSON shape changes incompatibly (a field is removed or
// repurposed, not just added). Mirrors the schema_version: 1 convention
// in internal/mappings.MappingsFile.
const currentSchemaVersion = 1

// legacyUpgradeWarnOnce gates the one-time stderr log emitted when
// Load reads a file without a schema_version field (legacy v0). Process-
// scoped so a long-running broker that re-reads on SIGHUP doesn't spam
// the operator.
var legacyUpgradeWarnOnce sync.Once

// unsupportedSchemaWarnOnce gates the one-time stderr log emitted when
// Load reads a file with schema_version > currentSchemaVersion. Separate
// from legacyUpgradeWarnOnce so a process that observes both kinds in
// sequence sees both warnings.
var unsupportedSchemaWarnOnce sync.Once

// Path returns the canonical claude-shim.json location:
//
//	$XDG_CONFIG_HOME/c3/claude-shim.json     (if set)
//	<UserHomeDir>/.config/c3/claude-shim.json (otherwise)
//
// Mirrors internal/mappings.DefaultPath so both config files live
// next to each other.
func Path() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "c3", "claude-shim.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "c3", "claude-shim.json"), nil
}

// File is the on-disk JSON shape. SchemaVersion is the migration knob:
// missing/0 means a pre-versioning (legacy v0) write — treated as v1
// in-memory with a one-time upgrade warning on Load. Forward-compatible:
// unknown fields are ignored on read so future broker versions can add
// fields without breaking older shims.
type File struct {
	SchemaVersion int    `json:"schema_version"`
	RealClaude    string `json:"real_claude"`
}

// Load reads the shim config at path. Returns ("", false) for any
// error (missing file, corrupt JSON, empty real_claude, unsupported
// schema version). NEVER returns an error — the shim's contract is
// "silent fallback to PATH walk on config issues; never hard-fail at
// runtime."
//
// Schema-version handling:
//   - 0 or missing → legacy v0; upgraded in-memory to v1. Emits a
//     one-time stderr warning per process.
//   - currentSchemaVersion (1) → used directly.
//   - > currentSchemaVersion → ok=false (shim falls back to PATH walk);
//     emits a one-time stderr warning per process so the operator sees
//     the version mismatch and can downgrade c3 or remove the file.
func Load(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return "", false
	}

	switch {
	case f.SchemaVersion == 0:
		legacyUpgradeWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "shimconfig: upgraded legacy v0 config to v1 in-memory at %s (next Save will write schema_version: %d)\n", path, currentSchemaVersion)
		})
		// Fall through; the legacy shape's only field is real_claude,
		// which is identical to v1 — no in-memory mutation needed.
	case f.SchemaVersion == currentSchemaVersion:
		// Current schema; no action.
	case f.SchemaVersion > currentSchemaVersion:
		unsupportedSchemaWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "shimconfig: schema version %d at %s is unsupported (this c3 understands up to %d); falling back to PATH walk. Downgrade c3 or remove the file to fix.\n", f.SchemaVersion, path, currentSchemaVersion)
		})
		return "", false
	}

	if f.RealClaude == "" {
		return "", false
	}
	return f.RealClaude, true
}

// Save writes a v1 claude-shim.json to path with mode 0600. Creates
// the parent directory (0700) if missing. The on-disk JSON always
// includes schema_version even when callers haven't changed — schema
// management is internal to this package, not the caller's concern.
func Save(path, realClaude string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(File{
		SchemaVersion: currentSchemaVersion,
		RealClaude:    realClaude,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
