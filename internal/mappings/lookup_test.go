package mappings

import "testing"

func newTestFile() *MappingsFile {
	return &MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]ChannelConfig{
			"telegram": {
				DefaultGroup: "main",
				Groups: map[string]GroupConfig{
					"main": {ChatID: -100, Title: "Main"},
					"work": {ChatID: -200, Title: "Work"},
				},
				DMChatID: 42,
				Topics: []Topic{
					{ChatID: -100, TopicID: 281, Name: "c3", Group: "main"},
					{ChatID: -100, TopicID: 207, Name: "sthapati", Group: "main"},
					{ChatID: -200, TopicID: 412, Name: "feature-x", Group: "work"},
				},
			},
		},
		Mappings: map[string]Mapping{
			"/home/u/c3": {
				Channel: "telegram", ChatID: -100, TopicID: 281,
				Name: "c3", Group: "main",
			},
		},
	}
}

func TestLookupByCwd_Found(t *testing.T) {
	mf := newTestFile()
	m, ok := mf.LookupByCwd("/home/u/c3")
	if !ok {
		t.Fatal("expected mapping to be found")
	}
	if m.TopicID != 281 {
		t.Errorf("TopicID = %d, want 281", m.TopicID)
	}
}

func TestLookupByCwd_NotFound(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupByCwd("/home/u/other")
	if ok {
		t.Error("expected mapping to be missing")
	}
}

func TestLookupTopicInDefaultGroup_Found(t *testing.T) {
	mf := newTestFile()
	tp, ok := mf.LookupTopicInDefaultGroup("telegram", "c3")
	if !ok {
		t.Fatal("expected to find c3 in default group main")
	}
	if tp.TopicID != 281 || tp.Group != "main" {
		t.Errorf("got %+v, want TopicID=281 Group=main", tp)
	}
}

func TestLookupTopicInDefaultGroup_NotInDefaultButInOther(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicInDefaultGroup("telegram", "feature-x")
	if ok {
		t.Error("expected NOT to find feature-x in default group main")
	}
}

func TestLookupTopicInDefaultGroup_UnknownChannel(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicInDefaultGroup("slack", "c3")
	if ok {
		t.Error("expected miss for unknown channel")
	}
}

func TestLookupTopicInDefaultGroup_ChannelHasNoDefault(t *testing.T) {
	mf := &MappingsFile{
		Channels: map[string]ChannelConfig{
			"telegram": {Topics: []Topic{{Name: "c3", Group: "main"}}},
		},
	}
	_, ok := mf.LookupTopicInDefaultGroup("telegram", "c3")
	if ok {
		t.Error("expected miss when default_group is empty")
	}
}

func TestLookupTopicAcrossGroups_FoundInOne(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("telegram", "feature-x")
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Group != "work" {
		t.Errorf("hit group = %q, want work", hits[0].Group)
	}
}

func TestLookupTopicAcrossGroups_FoundInMultiple(t *testing.T) {
	mf := newTestFile()
	cc := mf.Channels["telegram"]
	cc.Topics = append(cc.Topics, Topic{ChatID: -200, TopicID: 999, Name: "c3", Group: "work"})
	mf.Channels["telegram"] = cc

	hits := mf.LookupTopicAcrossGroups("telegram", "c3")
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
}

func TestLookupTopicAcrossGroups_None(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("telegram", "nonexistent")
	if len(hits) != 0 {
		t.Errorf("got %d hits, want 0", len(hits))
	}
}

func TestLookupTopicAcrossGroups_UnknownChannel(t *testing.T) {
	mf := newTestFile()
	hits := mf.LookupTopicAcrossGroups("slack", "anything")
	if len(hits) != 0 {
		t.Errorf("got %d hits for unknown channel, want 0", len(hits))
	}
}

func TestLookupTopicByID_Found(t *testing.T) {
	mf := newTestFile()
	tp, ok := mf.LookupTopicByID("telegram", -100, 281)
	if !ok {
		t.Fatal("expected to find topic 281 in chat -100")
	}
	if tp.Name != "c3" {
		t.Errorf("Name = %q, want c3", tp.Name)
	}
}

func TestLookupTopicByID_NotFound(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicByID("telegram", -100, 99999)
	if ok {
		t.Error("expected miss for nonexistent topic id")
	}
}

func TestLookupTopicByID_WrongChat(t *testing.T) {
	mf := newTestFile()
	_, ok := mf.LookupTopicByID("telegram", -200, 281)
	if ok {
		t.Error("expected miss when topic_id matches but chat_id doesn't")
	}
}
