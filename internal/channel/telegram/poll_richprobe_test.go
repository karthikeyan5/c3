package telegram

import (
	"encoding/json"
	"testing"
)

func TestParseUpdates_PairsRichByIndex(t *testing.T) {
	// One plain text update, one with a rich_message — same array, same order.
	raw := []byte(`[
		{"update_id":1,"message":{"message_id":10,"chat":{"id":-100},"text":"plain"}},
		{"update_id":2,"message":{"message_id":11,"chat":{"id":-100},"rich_message":{"blocks":[{"type":"paragraph","text":"hi"}]}}}]`)
	ups, probes, err := parseUpdates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || len(probes) != 2 {
		t.Fatalf("len ups=%d probes=%d", len(ups), len(probes))
	}
	if ups[0].UpdateId != 1 || ups[1].UpdateId != 2 {
		t.Errorf("update ids: %d %d", ups[0].UpdateId, ups[1].UpdateId)
	}
	if rr := richRawFor(probes[0]); len(rr) != 0 {
		t.Errorf("update 0 should have no rich: %s", rr)
	}
	rr := richRawFor(probes[1])
	if len(rr) == 0 {
		t.Fatal("update 1 should carry rich_message raw")
	}
	md, _, ok := decodeRichMessage(rr)
	if !ok || md != "hi" {
		t.Errorf("decoded rich: md=%q ok=%v", md, ok)
	}
}

func TestRichRawFor_EditedMessage(t *testing.T) {
	var p updateProbe
	if err := json.Unmarshal([]byte(`{"edited_message":{"rich_message":{"blocks":[{"type":"paragraph","text":"e"}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	if len(richRawFor(p)) == 0 {
		t.Error("edited_message rich_message not captured")
	}
}

func TestParseUpdates_Malformed(t *testing.T) {
	if _, _, err := parseUpdates([]byte(`{not an array`)); err == nil {
		t.Error("expected error on malformed array")
	}
}
