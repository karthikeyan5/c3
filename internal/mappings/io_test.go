package mappings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	contents := `{
		"schema_version": 1,
		"channels": {
			"telegram": {
				"bot_token": "abc",
				"default_group": "main",
				"groups": {"main": {"chat_id": -100, "title": "G"}},
				"dm_chat_id": 42,
				"topics": [{"chat_id": -100, "topic_id": 281, "name": "c3", "group": "main"}]
			}
		},
		"mappings": {
			"/home/u/proj": {
				"channel": "telegram",
				"chat_id": -100,
				"topic_id": 281,
				"name": "c3",
				"group": "main",
				"created_at": "2026-04-21T22:00:00Z"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	mf, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if mf.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", mf.SchemaVersion)
	}
	tg, ok := mf.Channels["telegram"]
	if !ok {
		t.Fatal("missing telegram channel")
	}
	if tg.BotToken != "abc" {
		t.Errorf("BotToken = %q, want %q", tg.BotToken, "abc")
	}
	if len(tg.Topics) != 1 || tg.Topics[0].TopicID != 281 {
		t.Errorf("topics = %+v, want one entry with TopicID=281", tg.Topics)
	}
	m, ok := mf.Mappings["/home/u/proj"]
	if !ok {
		t.Fatal("missing /home/u/proj mapping")
	}
	if m.TopicID != 281 {
		t.Errorf("Mappings[/home/u/proj].TopicID = %d, want 281", m.TopicID)
	}
}

func TestRead_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")

	_, err := Read(path)
	if !os.IsNotExist(err) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestRead_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]ChannelConfig{
			"telegram": {BotToken: "tok", DefaultGroup: "main"},
		},
		Mappings: map[string]Mapping{},
	}

	if err := Write(path, mf); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read after Write failed: %v", err)
	}
	if got.Channels["telegram"].BotToken != "tok" {
		t.Errorf("round-trip lost bot token; got %+v", got)
	}
}

func TestWrite_FileMode600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	mf := &MappingsFile{SchemaVersion: 1}

	if err := Write(path, mf); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestWrite_BackupOnRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mappings.json")
	bak := path + ".bak"

	mf1 := &MappingsFile{SchemaVersion: 1, Channels: map[string]ChannelConfig{"telegram": {BotToken: "first"}}}
	if err := Write(path, mf1); err != nil {
		t.Fatal(err)
	}
	// First write — no .bak yet (nothing to back up).
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Errorf(".bak should not exist after first write, got: %v", err)
	}

	mf2 := &MappingsFile{SchemaVersion: 1, Channels: map[string]ChannelConfig{"telegram": {BotToken: "second"}}}
	if err := Write(path, mf2); err != nil {
		t.Fatal(err)
	}
	// Second write — .bak should now contain mf1's contents.
	bakBody, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("expected .bak to exist, got: %v", err)
	}
	if !contains(string(bakBody), `"first"`) {
		t.Errorf(".bak should contain previous bot_token; got: %s", bakBody)
	}
	// .bak mode is 0600.
	info, _ := os.Stat(bak)
	if info.Mode().Perm() != 0600 {
		t.Errorf(".bak mode = %o, want 0600", info.Mode().Perm())
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
