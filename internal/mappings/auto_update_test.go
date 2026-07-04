package mappings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoUpdateEnabled_Default(t *testing.T) {
	// absent field ⇒ false (v1 default: notices fire, self-install off)
	if got := (&MappingsFile{SchemaVersion: 1}).AutoUpdateEnabled(); got {
		t.Errorf("absent field: got %v, want false", got)
	}
	// explicit true ⇒ true
	if got := (&MappingsFile{AutoUpdate: true}).AutoUpdateEnabled(); !got {
		t.Errorf("explicit true: got %v, want true", got)
	}
	// nil receiver ⇒ false (defensive)
	var nilmf *MappingsFile
	if got := nilmf.AutoUpdateEnabled(); got {
		t.Errorf("nil receiver: got %v, want false", got)
	}
}

func TestAutoUpdate_JSONParse(t *testing.T) {
	// A pre-feature config with no field parses as disabled (migration-safe).
	var absent MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"channels":{},"mappings":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.AutoUpdateEnabled() {
		t.Error("absent field parsed: want auto-update disabled")
	}
	var on MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"auto_update":true}`), &on); err != nil {
		t.Fatal(err)
	}
	if !on.AutoUpdateEnabled() {
		t.Error("auto_update:true parsed: want enabled")
	}
}

// TestAutoUpdate_OmitemptyAndRoundTrip locks in that a disabled gate is omitted
// from the serialized file (byte-identical to pre-feature config) and that an
// enabled gate survives a marshal/unmarshal round trip.
func TestAutoUpdate_OmitemptyAndRoundTrip(t *testing.T) {
	off, err := json.Marshal(&MappingsFile{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(off); strings.Contains(got, "auto_update") {
		t.Errorf("disabled gate must be omitted from JSON; got %s", got)
	}
	data, err := json.Marshal(&MappingsFile{SchemaVersion: 1, AutoUpdate: true})
	if err != nil {
		t.Fatal(err)
	}
	var back MappingsFile
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.AutoUpdateEnabled() {
		t.Error("enabled gate must survive a marshal/unmarshal round trip")
	}
}

// TestAutoUpdate_ClonePreserved guards the copy-on-write path: a field added to
// MappingsFile but not to Clone would be silently reset to false on the next
// mutation (and thus on every attach), disabling an opted-in gate. Clone must
// carry it.
func TestAutoUpdate_ClonePreserved(t *testing.T) {
	orig := &MappingsFile{SchemaVersion: 1, AutoUpdate: true}
	if got := orig.Clone(); !got.AutoUpdateEnabled() {
		t.Error("Clone must preserve auto_update=true")
	}
	off := &MappingsFile{SchemaVersion: 1}
	if got := off.Clone(); got.AutoUpdateEnabled() {
		t.Error("Clone must preserve auto_update=false")
	}
}
