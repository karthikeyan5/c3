package mappings

import (
	"strings"
	"testing"
)

func TestValidate_Ok(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	if err := mf.Validate(); err != nil {
		t.Errorf("Validate failed on valid file: %v", err)
	}
}

func TestValidate_BadSchemaVersion(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 99
	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("expected schema_version error, got %v", err)
	}
}

func TestValidate_DefaultGroupNotInGroups(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	cc := mf.Channels["telegram"]
	cc.DefaultGroup = "ghost"
	mf.Channels["telegram"] = cc

	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "default_group") {
		t.Errorf("expected default_group error, got %v", err)
	}
}

func TestValidate_TopicGroupNotInGroups(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	cc := mf.Channels["telegram"]
	cc.Topics = append(cc.Topics, Topic{ChatID: -300, TopicID: 5, Name: "x", Group: "phantom"})
	mf.Channels["telegram"] = cc

	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "phantom") {
		t.Errorf("expected phantom-group error, got %v", err)
	}
}

func TestValidate_MappingChannelMissing(t *testing.T) {
	mf := newTestFile()
	mf.SchemaVersion = 1
	mf.Mappings["/home/u/orphan"] = Mapping{
		Channel: "ghost-channel", ChatID: -100, TopicID: 1,
	}
	err := mf.Validate()
	if err == nil || !strings.Contains(err.Error(), "ghost-channel") {
		t.Errorf("expected unknown-channel error, got %v", err)
	}
}
