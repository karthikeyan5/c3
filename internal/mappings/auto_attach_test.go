package mappings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoAttachOnResumeEnabled_Default(t *testing.T) {
	// absent field (nil pointer) ⇒ true (redesign default: feature on)
	if got := (&MappingsFile{SchemaVersion: 1}).AutoAttachOnResumeEnabled(); !got {
		t.Errorf("absent field: got %v, want true", got)
	}
	// explicit true ⇒ true
	if got := (&MappingsFile{AutoAttachOnResume: boolp(true)}).AutoAttachOnResumeEnabled(); !got {
		t.Errorf("explicit true: got %v, want true", got)
	}
	// explicit false ⇒ false (the only way to disable now)
	if got := (&MappingsFile{AutoAttachOnResume: boolp(false)}).AutoAttachOnResumeEnabled(); got {
		t.Errorf("explicit false: got %v, want false", got)
	}
	// nil receiver ⇒ false (defensive)
	var nilmf *MappingsFile
	if got := nilmf.AutoAttachOnResumeEnabled(); got {
		t.Errorf("nil receiver: got %v, want false", got)
	}
}

func TestAutoAttachOnResume_JSONParse(t *testing.T) {
	// A pre-feature config with no field parses as ENABLED (redesign default).
	var absent MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"channels":{},"mappings":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if !absent.AutoAttachOnResumeEnabled() {
		t.Error("absent field parsed: want auto-attach enabled")
	}
	var on MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"auto_attach_on_resume":true}`), &on); err != nil {
		t.Fatal(err)
	}
	if !on.AutoAttachOnResumeEnabled() {
		t.Error("auto_attach_on_resume:true parsed: want enabled")
	}
	// Only an explicit false disables it.
	var off MappingsFile
	if err := json.Unmarshal([]byte(`{"schema_version":1,"auto_attach_on_resume":false}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.AutoAttachOnResumeEnabled() {
		t.Error("auto_attach_on_resume:false parsed: want disabled")
	}
}

// TestAutoAttachOnResume_OmitemptyAndRoundTrip locks in that the default (nil)
// gate is omitted from the serialized file (byte-identical to pre-feature
// config), an explicit false IS written (so an opt-out survives), and both
// explicit values survive a marshal/unmarshal round trip.
func TestAutoAttachOnResume_OmitemptyAndRoundTrip(t *testing.T) {
	def, err := json.Marshal(&MappingsFile{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(def); strings.Contains(got, "auto_attach_on_resume") {
		t.Errorf("default (nil) gate must be omitted from JSON; got %s", got)
	}
	// An explicit false must be written — omitempty on a *bool omits only nil.
	offBytes, err := json.Marshal(&MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(offBytes); !strings.Contains(got, `"auto_attach_on_resume":false`) {
		t.Errorf("explicit false must be written to JSON; got %s", got)
	}
	data, err := json.Marshal(&MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(true)})
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
// added to MappingsFile but not to Clone would be silently reset on the next
// mutation (and thus on every attach/recover-refresh). Clone must carry it, and
// must deep-copy the pointer (not alias the original's).
func TestAutoAttachOnResume_ClonePreserved(t *testing.T) {
	orig := &MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(true)}
	got := orig.Clone()
	if !got.AutoAttachOnResumeEnabled() {
		t.Error("Clone must preserve auto_attach_on_resume=true")
	}
	if got.AutoAttachOnResume == orig.AutoAttachOnResume {
		t.Error("Clone must deep-copy the pointer, not alias it")
	}
	off := &MappingsFile{SchemaVersion: 1, AutoAttachOnResume: boolp(false)}
	if got := off.Clone(); got.AutoAttachOnResumeEnabled() {
		t.Error("Clone must preserve an explicit auto_attach_on_resume=false")
	}
	// A nil field stays nil (default-on) across Clone.
	def := &MappingsFile{SchemaVersion: 1}
	if got := def.Clone(); got.AutoAttachOnResume != nil || !got.AutoAttachOnResumeEnabled() {
		t.Error("Clone must preserve a nil (default-on) auto_attach_on_resume")
	}
}
