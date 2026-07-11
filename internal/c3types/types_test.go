package c3types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboundConstruct(t *testing.T) {
	id := int64(281)
	in := Inbound{
		Channel:   "telegram",
		ChatID:    -100,
		TopicID:   &id,
		MessageID: 868,
		Sender:    Sender{UserID: 42, Username: "x"},
		Text:      "hi",
	}
	if in.TopicID == nil || *in.TopicID != 281 {
		t.Errorf("TopicID round-trip: %+v", in.TopicID)
	}
}

// TestInboundDrainedFromSerialization pins the DrainedFrom omitempty contract:
// an organic (empty) DrainedFrom is OMITTED from the JSON (so every pre-drain
// marshal stays byte-identical); an old JSONL line without the field unmarshals
// to ""; and a set DrainedFrom round-trips.
func TestInboundDrainedFromSerialization(t *testing.T) {
	// Organic line: DrainedFrom empty ⇒ key omitted.
	organic, err := json.Marshal(Inbound{Channel: "telegram", ChatID: -100, MessageID: 1, Text: "hi"})
	if err != nil {
		t.Fatalf("marshal organic: %v", err)
	}
	if strings.Contains(string(organic), "DrainedFrom") {
		t.Fatalf("organic marshal must omit DrainedFrom, got %s", organic)
	}

	// Old JSONL line without the field ⇒ unmarshals to "".
	var old Inbound
	if err := json.Unmarshal([]byte(`{"Channel":"telegram","ChatID":-100,"MessageID":2,"Text":"legacy"}`), &old); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if old.DrainedFrom != "" || old.MessageID != 2 {
		t.Fatalf("legacy line = %+v, want DrainedFrom empty / MessageID 2", old)
	}

	// A set DrainedFrom round-trips.
	drained, err := json.Marshal(Inbound{Channel: "telegram", ChatID: -100, MessageID: 3, DrainedFrom: "telegram__-100__42"})
	if err != nil {
		t.Fatalf("marshal drained: %v", err)
	}
	if !strings.Contains(string(drained), `"DrainedFrom":"telegram__-100__42"`) {
		t.Fatalf("set DrainedFrom must serialize, got %s", drained)
	}
	var back Inbound
	if err := json.Unmarshal(drained, &back); err != nil {
		t.Fatalf("unmarshal drained: %v", err)
	}
	if back.DrainedFrom != "telegram__-100__42" {
		t.Fatalf("DrainedFrom round-trip = %q, want telegram__-100__42", back.DrainedFrom)
	}
}

func TestReplyArgsAlias(t *testing.T) {
	// ReplyArgs = Outbound, exercise type-alias equivalence.
	id := int64(1)
	r := ReplyArgs{Channel: "telegram", ChatID: -100, TopicID: &id, Text: "hi"}
	o := Outbound(r)
	if o.Text != "hi" {
		t.Errorf("alias roundtrip lost field: %+v", o)
	}
}
