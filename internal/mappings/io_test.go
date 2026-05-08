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
