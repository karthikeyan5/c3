package telegram

import (
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func TestUpdateDedup_FirstSeenReturnsFalse(t *testing.T) {
	d := newUpdateDedup(10, time.Minute)
	u := &gotgbot.Update{
		UpdateId: 100,
		Message:  &gotgbot.Message{MessageId: 5, Chat: gotgbot.Chat{Id: -1}},
	}
	if d.SeenOrAdd(u) {
		t.Fatal("first SeenOrAdd should return false")
	}
}

func TestUpdateDedup_SecondCallReturnsTrue(t *testing.T) {
	d := newUpdateDedup(10, time.Minute)
	u := &gotgbot.Update{
		UpdateId: 100,
		Message:  &gotgbot.Message{MessageId: 5, Chat: gotgbot.Chat{Id: -1}},
	}
	d.SeenOrAdd(u)
	if !d.SeenOrAdd(u) {
		t.Fatal("second SeenOrAdd should return true (duplicate detected)")
	}
}

func TestUpdateDedup_DistinctUpdatesIndependent(t *testing.T) {
	d := newUpdateDedup(10, time.Minute)
	a := &gotgbot.Update{UpdateId: 1, Message: &gotgbot.Message{MessageId: 1, Chat: gotgbot.Chat{Id: -1}}}
	b := &gotgbot.Update{UpdateId: 2, Message: &gotgbot.Message{MessageId: 2, Chat: gotgbot.Chat{Id: -1}}}
	if d.SeenOrAdd(a) {
		t.Error("a: first call should be false")
	}
	if d.SeenOrAdd(b) {
		t.Error("b: first call should be false (different update_id)")
	}
}

func TestUpdateDedup_NilUpdateIsNoOp(t *testing.T) {
	d := newUpdateDedup(10, time.Minute)
	if d.SeenOrAdd(nil) {
		t.Fatal("nil update should return false, not crash")
	}
}

func TestUpdateDedup_TTLExpiry(t *testing.T) {
	d := newUpdateDedup(10, 50*time.Millisecond)
	u := &gotgbot.Update{UpdateId: 1, Message: &gotgbot.Message{MessageId: 1, Chat: gotgbot.Chat{Id: -1}}}
	if d.SeenOrAdd(u) {
		t.Fatal("first call: want false")
	}
	if !d.SeenOrAdd(u) {
		t.Fatal("second call within TTL: want true")
	}
	time.Sleep(80 * time.Millisecond)
	if d.SeenOrAdd(u) {
		t.Fatal("after TTL: want false (entry expired)")
	}
}

func TestUpdateDedup_CapacityEvictsOldest(t *testing.T) {
	d := newUpdateDedup(2, time.Hour)
	a := &gotgbot.Update{UpdateId: 1, Message: &gotgbot.Message{MessageId: 1, Chat: gotgbot.Chat{Id: -1}}}
	b := &gotgbot.Update{UpdateId: 2, Message: &gotgbot.Message{MessageId: 2, Chat: gotgbot.Chat{Id: -1}}}
	c := &gotgbot.Update{UpdateId: 3, Message: &gotgbot.Message{MessageId: 3, Chat: gotgbot.Chat{Id: -1}}}
	d.SeenOrAdd(a)
	d.SeenOrAdd(b)
	d.SeenOrAdd(c)
	// `a` should have been evicted; re-adding returns false.
	if d.SeenOrAdd(a) {
		t.Fatal("after capacity overflow, oldest entry should be evicted")
	}
	// `c` (most recent) should still be deduped.
	if !d.SeenOrAdd(c) {
		t.Fatal("most-recent entry should still be deduped")
	}
}

func TestDedupKey_DistinguishesEditedFromOriginal(t *testing.T) {
	orig := &gotgbot.Update{UpdateId: 1, Message: &gotgbot.Message{MessageId: 5, Chat: gotgbot.Chat{Id: -1}}}
	edited := &gotgbot.Update{UpdateId: 1, EditedMessage: &gotgbot.Message{MessageId: 5, Chat: gotgbot.Chat{Id: -1}}}
	if dedupKey(orig) == dedupKey(edited) {
		t.Errorf("original and edited variants must produce different keys, both got %q", dedupKey(orig))
	}
}

func TestDedupKey_CallbackQuery(t *testing.T) {
	cq := &gotgbot.Update{UpdateId: 99, CallbackQuery: &gotgbot.CallbackQuery{Id: "abc"}}
	if dedupKey(cq) == "" {
		t.Error("callback query should produce a non-empty dedup key")
	}
}

func TestDedupKey_EmptyForUnknownUpdateShape(t *testing.T) {
	empty := &gotgbot.Update{UpdateId: 1} // no message/edited/cq/reaction
	if dedupKey(empty) != "" {
		t.Errorf("update with no useful fields should return empty key, got %q", dedupKey(empty))
	}
}
