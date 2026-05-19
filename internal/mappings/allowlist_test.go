package mappings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllowlist_MigrationFromMappingsWithoutAllowlistKey(t *testing.T) {
	// Old-shape mappings.json from a pre-allowlist broker has no
	// "allowlist" key at all. Read must succeed, and IsUserAllowed /
	// IsGroupAllowed must report false without panicking on the nil
	// Allowlist pointer.
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	contents := `{
		"schema_version": 1,
		"channels": {"telegram": {"bot_token": "x"}},
		"mappings": {}
	}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	mf, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if mf.Allowlist != nil {
		t.Errorf("expected nil Allowlist on pre-migration file, got %+v", mf.Allowlist)
	}
	if mf.IsUserAllowed(123) {
		t.Errorf("IsUserAllowed on empty allowlist should be false")
	}
	if mf.IsGroupAllowed(-100) {
		t.Errorf("IsGroupAllowed on empty allowlist should be false")
	}
	al := mf.AllowlistOrEmpty()
	if len(al.Users) != 0 || len(al.Groups) != 0 {
		t.Errorf("AllowlistOrEmpty should return zero-value Allowlist; got %+v", al)
	}
}

func TestAllowlist_AddUserIdempotent(t *testing.T) {
	mf := &MappingsFile{}
	mf.AddAllowedUser(42)
	mf.AddAllowedUser(42) // duplicate
	mf.AddAllowedUser(99)
	if !mf.IsUserAllowed(42) {
		t.Errorf("expected 42 in allowlist")
	}
	if !mf.IsUserAllowed(99) {
		t.Errorf("expected 99 in allowlist")
	}
	if mf.IsUserAllowed(1) {
		t.Errorf("did not expect 1 in allowlist")
	}
	if got := len(mf.Allowlist.Users); got != 2 {
		t.Errorf("dedup failed: got %d users, want 2", got)
	}
}

func TestAllowlist_AddGroupIdempotent(t *testing.T) {
	mf := &MappingsFile{}
	mf.AddAllowedGroup(-100)
	mf.AddAllowedGroup(-100) // duplicate
	mf.AddAllowedGroup(-200)
	if !mf.IsGroupAllowed(-100) {
		t.Errorf("expected -100 in allowlist")
	}
	if !mf.IsGroupAllowed(-200) {
		t.Errorf("expected -200 in allowlist")
	}
	if mf.IsGroupAllowed(-300) {
		t.Errorf("did not expect -300 in allowlist")
	}
	if got := len(mf.Allowlist.Groups); got != 2 {
		t.Errorf("dedup failed: got %d groups, want 2", got)
	}
}

func TestAllowlist_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]ChannelConfig{"telegram": {BotToken: "tok"}},
		Mappings:      map[string]Mapping{},
	}
	mf.AddAllowedUser(42857190)
	mf.AddAllowedGroup(-1009123456789)
	if err := Write(path, mf); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !got.IsUserAllowed(42857190) {
		t.Errorf("user not preserved after round-trip")
	}
	if !got.IsGroupAllowed(-1009123456789) {
		t.Errorf("group not preserved after round-trip")
	}
}

func TestAllowlist_EmptyOmittedFromJSON(t *testing.T) {
	// A mappings file with no allowlist key should round-trip to JSON
	// WITHOUT introducing an empty "allowlist" key — keeps the
	// disk-side schema clean for users who haven't paired yet.
	mf := &MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]ChannelConfig{"telegram": {BotToken: "x"}},
		Mappings:      map[string]Mapping{},
	}
	data, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(data), `"allowlist"`) {
		t.Errorf("allowlist key should be omitted when nil; got: %s", data)
	}
}

func TestAllowlist_CloneDeepCopies(t *testing.T) {
	mf := &MappingsFile{SchemaVersion: 1}
	mf.AddAllowedUser(1)
	mf.AddAllowedGroup(-2)

	clone := mf.Clone()
	// Mutate original; clone must not change.
	mf.AddAllowedUser(99)
	mf.AddAllowedGroup(-99)
	if clone.IsUserAllowed(99) {
		t.Errorf("clone shares Users slice with original (saw 99 propagate)")
	}
	if clone.IsGroupAllowed(-99) {
		t.Errorf("clone shares Groups slice with original (saw -99 propagate)")
	}
	if !clone.IsUserAllowed(1) {
		t.Errorf("clone lost original user 1")
	}
	if !clone.IsGroupAllowed(-2) {
		t.Errorf("clone lost original group -2")
	}
}
