package mappings

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionAttachment_UpsertLookup(t *testing.T) {
	mf := &MappingsFile{}
	tid := int64(914)
	sa := SessionAttachment{Channel: "telegram", ChatID: -100, TopicID: &tid, Name: "c3", LastAttachedAt: time.Now().UTC()}
	mf.UpsertSessionAttachment("sess-1", sa)
	got, ok := mf.LookupSessionAttachment("sess-1")
	if !ok || got.Name != "c3" || got.TopicID == nil || *got.TopicID != 914 {
		t.Fatalf("lookup = %+v, ok=%v", got, ok)
	}
	if _, ok := mf.LookupSessionAttachment("nope"); ok {
		t.Fatal("unknown id should miss")
	}
	if _, ok := mf.LookupSessionAttachment(""); ok {
		t.Fatal("empty id should miss")
	}
}

func TestSessionAttachment_UpsertEmptyIDNoOp(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("", SessionAttachment{Name: "c3"})
	if len(mf.SessionAttachments) != 0 {
		t.Fatalf("empty id must not be stored; got %d entries", len(mf.SessionAttachments))
	}
}

func TestSessionAttachment_Recoverable(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	ttl := 30 * 24 * time.Hour
	if !(SessionAttachment{LastAttachedAt: now.Add(-time.Hour)}).Recoverable(now, ttl) {
		t.Fatal("fresh should be recoverable")
	}
	if (SessionAttachment{LastAttachedAt: now.Add(-time.Hour), Detached: true}).Recoverable(now, ttl) {
		t.Fatal("tombstoned must not be recoverable")
	}
	if (SessionAttachment{LastAttachedAt: now.Add(-31 * 24 * time.Hour)}).Recoverable(now, ttl) {
		t.Fatal("expired must not be recoverable")
	}
}

func TestSessionAttachment_Tombstone(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("s", SessionAttachment{Name: "c3", LastAttachedAt: time.Now().UTC()})
	mf.TombstoneSessionAttachment("s")
	got, ok := mf.LookupSessionAttachment("s")
	if !ok || !got.Detached {
		t.Fatalf("expected tombstoned entry, got %+v ok=%v", got, ok)
	}
	mf.TombstoneSessionAttachment("missing") // must not panic
}

func TestSessionAttachment_UpsertClearsTombstone(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("s", SessionAttachment{Name: "c3", LastAttachedAt: time.Now().UTC()})
	mf.TombstoneSessionAttachment("s")
	mf.UpsertSessionAttachment("s", SessionAttachment{Name: "c3", LastAttachedAt: time.Now().UTC()})
	got, _ := mf.LookupSessionAttachment("s")
	if got.Detached {
		t.Fatal("re-attach must clear the tombstone")
	}
}

func TestSessionAttachment_Prune(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	ttl := 30 * 24 * time.Hour
	mf := &MappingsFile{}
	mf.UpsertSessionAttachment("old", SessionAttachment{LastAttachedAt: now.Add(-40 * 24 * time.Hour)})
	mf.UpsertSessionAttachment("new", SessionAttachment{LastAttachedAt: now.Add(-time.Hour)})
	if n := mf.PruneSessionAttachments(now, ttl); n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if _, ok := mf.LookupSessionAttachment("old"); ok {
		t.Fatal("old should be pruned")
	}
	if _, ok := mf.LookupSessionAttachment("new"); !ok {
		t.Fatal("new should survive")
	}
}

func TestSessionAttachment_OmitemptyAndRoundTrip(t *testing.T) {
	// Empty store stays off the wire (old config files unchanged).
	b, _ := json.Marshal(&MappingsFile{SchemaVersion: 1})
	if strings.Contains(string(b), "session_attachments") {
		t.Fatalf("empty store must be omitted: %s", b)
	}
	// Round-trip a populated store.
	tid := int64(914)
	mf := &MappingsFile{SchemaVersion: 1}
	mf.UpsertSessionAttachment("s", SessionAttachment{Channel: "telegram", ChatID: -100, TopicID: &tid, Name: "c3", LastAttachedAt: time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)})
	raw, _ := json.Marshal(mf)
	var back MappingsFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sa, ok := back.LookupSessionAttachment("s")
	if !ok || sa.Name != "c3" || sa.TopicID == nil || *sa.TopicID != 914 {
		t.Fatalf("round-trip = %+v ok=%v", sa, ok)
	}
}
