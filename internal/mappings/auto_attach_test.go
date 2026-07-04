package mappings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoAttachOnResumeEnabled_Default(t *testing.T) {
	// absent field ⇒ false (v1 default: feature off)
	if got := (&MappingsFile{SchemaVersion: 1}).AutoAttachOnResumeEnabled(); got {
		t.Errorf("absent field: got %v, want false", got)
	}
	// explicit true ⇒ true
	if got := (&MappingsFile{AutoAttachOnResume: true}).AutoAttachOnResumeEnabled(); !got {
		t.Errorf("explicit true: got %v, want true", got)
	}
	// nil receiver ⇒ false (defensive)
	var nilmf *MappingsFile
	if got := nilmf.AutoAttachOnResumeEnabled(); got {
		t.Errorf("nil receiver: got %v, want false", got)
	}
}

func TestAutoAttachOnResume_JSONParse(t *testing.T) {
	// A pre-feature config with no field parses as disabled (migration-safe).
	var absent MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"channels":{},"mappings":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.AutoAttachOnResumeEnabled() {
		t.Error("absent field parsed: want auto-attach disabled")
	}
	var on MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"auto_attach_on_resume":true}`), &on); err != nil {
		t.Fatal(err)
	}
	if !on.AutoAttachOnResumeEnabled() {
		t.Error("auto_attach_on_resume:true parsed: want enabled")
	}
}

// TestAutoAttachOnResume_OmitemptyAndRoundTrip locks in that a disabled gate is
// omitted from the serialized file (byte-identical to pre-feature config) and
// that an enabled gate survives a marshal/unmarshal round trip.
func TestAutoAttachOnResume_OmitemptyAndRoundTrip(t *testing.T) {
	off, err := json.Marshal(&MappingsFile{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(off); strings.Contains(got, "auto_attach_on_resume") {
		t.Errorf("disabled gate must be omitted from JSON; got %s", got)
	}
	data, err := json.Marshal(&MappingsFile{SchemaVersion: 1, AutoAttachOnResume: true})
	if err != nil {
		t.Fatal(err)
	}
	var back MappingsFile
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.AutoAttachOnResumeEnabled() {
		t.Error("enabled gate must survive a marshal/unmarshal round trip")
	}
}

// TestAutoAttachOnResume_ClonePreserved guards the copy-on-write path: a field
// added to MappingsFile but not to Clone would be silently reset to false on the
// next mutation (and thus on every attach/recover-refresh), disabling an
// opted-in gate. Clone must carry it.
func TestAutoAttachOnResume_ClonePreserved(t *testing.T) {
	orig := &MappingsFile{SchemaVersion: 1, AutoAttachOnResume: true}
	if got := orig.Clone(); !got.AutoAttachOnResumeEnabled() {
		t.Error("Clone must preserve auto_attach_on_resume=true")
	}
	off := &MappingsFile{SchemaVersion: 1}
	if got := off.Clone(); got.AutoAttachOnResumeEnabled() {
		t.Error("Clone must preserve auto_attach_on_resume=false")
	}
}
