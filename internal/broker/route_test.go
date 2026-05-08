package broker

import "testing"

func TestMakeRouteKey_NilTopic(t *testing.T) {
	k := MakeRouteKey("telegram", -100, nil)
	if k.HasTopic {
		t.Error("HasTopic should be false for nil")
	}
	if k.TopicID != 0 {
		t.Errorf("TopicID = %d, want 0 for nil topic", k.TopicID)
	}
}

func TestMakeRouteKey_GeneralTopic(t *testing.T) {
	one := int64(1)
	k := MakeRouteKey("telegram", -100, &one)
	if !k.HasTopic {
		t.Error("HasTopic should be true for &1")
	}
	if k.TopicID != 1 {
		t.Errorf("TopicID = %d, want 1", k.TopicID)
	}
}

func TestMakeRouteKey_CustomTopic(t *testing.T) {
	id := int64(281)
	k := MakeRouteKey("telegram", -100, &id)
	if !k.HasTopic || k.TopicID != 281 {
		t.Errorf("got %+v, want HasTopic=true TopicID=281", k)
	}
}

func TestRouteKey_MapKeyEqualityForGeneralTopic(t *testing.T) {
	a := int64(1)
	b := int64(1)

	m := map[RouteKey]string{}
	m[MakeRouteKey("telegram", -100, &a)] = "first"
	m[MakeRouteKey("telegram", -100, &b)] = "second"

	if len(m) != 1 {
		t.Errorf("expected 1 entry (collision), got %d (RouteKey not value-comparable)", len(m))
	}
	if m[MakeRouteKey("telegram", -100, &a)] != "second" {
		t.Errorf("expected second to overwrite first, got %q", m[MakeRouteKey("telegram", -100, &a)])
	}
}

func TestRouteKey_NilDistinctFromZero(t *testing.T) {
	zero := int64(0)
	kNil := MakeRouteKey("telegram", -100, nil)
	kZero := MakeRouteKey("telegram", -100, &zero)

	if kNil == kZero {
		t.Error("RouteKey for nil topic_id MUST NOT equal RouteKey for &0")
	}
}
