package mappings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// boolp returns a *bool for terse pointer-field literals in tests.
func boolp(b bool) *bool { return &b }

// TestAutoAttachOnResume_IORoundTrip exercises the real on-disk path (io.go
// Write/Read), not just json.Marshal: a nil (default) gate is omitted so a
// pre-feature file stays byte-clean, an explicit false is persisted so an
// opt-out survives a broker restart, and both explicit values round-trip.
func TestAutoAttachOnResume_IORoundTrip(t *testing.T) {
	dir := t.TempDir()

	// nil (default-on) ⇒ key omitted on disk, reads back enabled.
	defPath := filepath.Join(dir, "default.json")
	if err := Write(defPath, &MappingsFile{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(defPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auto_attach_on_resume") {
		t.Errorf("default (nil) gate must not be written to disk; got %s", raw)
	}
	got, err := Read(defPath)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoAttachOnResumeEnabled() {
		t.Error("default file must read back as enabled")
	}

	// explicit false ⇒ key written, reads back disabled.
	offPath := filepath.Join(dir, "off.json")
	if err := Write(offPath, &MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(offPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "auto_attach_on_resume") {
		t.Errorf("explicit false must be written to disk; got %s", raw)
	}
	got, err = Read(offPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoAttachOnResumeEnabled() {
		t.Error("explicit-false file must read back as disabled")
	}

	// explicit true ⇒ reads back enabled.
	onPath := filepath.Join(dir, "on.json")
	if err := Write(onPath, &MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	got, err = Read(onPath)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoAttachOnResumeEnabled() {
		t.Error("explicit-true file must read back as enabled")
	}
}

// TestAutoAttachOnResume_ByteStabilityNoField proves a config that never had the
// field survives a Read → Write cycle without the key appearing — the migration
// guarantee for the ~100% of installs that predate this feature.
func TestAutoAttachOnResume_ByteStabilityNoField(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(src, []byte(`{"schema_version":1,"channels":{},"mappings":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	mf, err := Read(src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "rewritten.json")
	if err := Write(out, mf); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auto_attach_on_resume") {
		t.Errorf("a legacy file with no gate must not gain the field on rewrite; got %s", raw)
	}
}
