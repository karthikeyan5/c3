package c3types

import "testing"

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

func TestReplyArgsAlias(t *testing.T) {
	// ReplyArgs = Outbound, exercise type-alias equivalence.
	id := int64(1)
	r := ReplyArgs{Channel: "telegram", ChatID: -100, TopicID: &id, Text: "hi"}
	o := Outbound(r)
	if o.Text != "hi" {
		t.Errorf("alias roundtrip lost field: %+v", o)
	}
}
