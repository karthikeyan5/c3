package mappings

import (
	"testing"
	"time"
)

func TestUpsertTopic_New(t *testing.T) {
	mf := newTestFile()
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 917, Name: "widget-foo", Group: "main"})

	tp, ok := mf.LookupTopicByID("telegram", -100, 917)
	if !ok {
		t.Fatal("expected new topic to be present after Upsert")
	}
	if tp.Name != "widget-foo" {
		t.Errorf("Name = %q, want widget-foo", tp.Name)
	}
}

func TestUpsertTopic_Update(t *testing.T) {
	mf := newTestFile()
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 281, Name: "C3", Group: "main"})

	tp, _ := mf.LookupTopicByID("telegram", -100, 281)
	if tp.Name != "C3" {
		t.Errorf("Name = %q, want C3", tp.Name)
	}
	hits := mf.LookupTopicAcrossGroups("telegram", "C3")
	if len(hits) != 1 {
		t.Errorf("got %d entries with name C3, want 1", len(hits))
	}
}

func TestUpsertTopic_NewChannel(t *testing.T) {
	mf := &MappingsFile{}
	mf.UpsertTopic("telegram", Topic{ChatID: -100, TopicID: 1, Name: "x", Group: "g"})

	if mf.Channels == nil {
		t.Fatal("Channels map should have been created")
	}
	if len(mf.Channels["telegram"].Topics) != 1 {
		t.Errorf("expected 1 topic, got %d", len(mf.Channels["telegram"].Topics))
	}
}

func TestUpsertMapping_New(t *testing.T) {
	mf := newTestFile()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/widget-foo", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 917,
		Name: "widget-foo", Group: "main",
		CreatedAt: now, LastAttachedAt: now,
	})

	m, ok := mf.LookupByCwd("/home/u/widget-foo")
	if !ok {
		t.Fatal("expected mapping to be present")
	}
	if m.TopicID != 917 {
		t.Errorf("TopicID = %d, want 917", m.TopicID)
	}
}

func TestUpsertMapping_UpdatePreservesCreatedAt(t *testing.T) {
	mf := newTestFile()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/proj", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
		CreatedAt: created,
	})

	updated := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	mf.UpsertMapping("/home/u/proj", Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
		LastAttachedAt: updated,
	})

	m, _ := mf.LookupByCwd("/home/u/proj")
	if !m.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want preserved %v", m.CreatedAt, created)
	}
	if !m.LastAttachedAt.Equal(updated) {
		t.Errorf("LastAttachedAt = %v, want %v", m.LastAttachedAt, updated)
	}
}
