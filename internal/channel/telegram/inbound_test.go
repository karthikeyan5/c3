package telegram

import (
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func TestConvertInbound_TextMessage(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId: 868,
		From:      &gotgbot.User{Id: 42, Username: "skarthi"},
		Chat:      gotgbot.Chat{Id: -1003990699908},
		Date:      1715151931,
		Text:      "hello",
	}
	in := convertInbound("telegram", msg, "")
	if in == nil {
		t.Fatal("expected Inbound, got nil")
	}
	if in.Text != "hello" {
		t.Errorf("Text=%q", in.Text)
	}
	if in.ChatID != -1003990699908 {
		t.Errorf("ChatID=%d", in.ChatID)
	}
	if in.Sender.UserID != 42 || in.Sender.Username != "skarthi" {
		t.Errorf("Sender=%+v", in.Sender)
	}
	if in.TopicID != nil {
		t.Errorf("TopicID should be nil for non-topic, got %v", in.TopicID)
	}
}

func TestConvertInbound_TopicMessage(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId:       100,
		MessageThreadId: 281,
		From:            &gotgbot.User{Id: 42},
		Chat:            gotgbot.Chat{Id: -100},
		Text:            "in topic",
	}
	in := convertInbound("telegram", msg, "")
	if in.TopicID == nil || *in.TopicID != 281 {
		t.Errorf("TopicID = %v, want &281", in.TopicID)
	}
}

func TestConvertInbound_VoiceMessage(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId: 1,
		From:      &gotgbot.User{Id: 42},
		Chat:      gotgbot.Chat{Id: -100},
		Voice: &gotgbot.Voice{
			FileId:       "AwACAgUAAyEFAAT...",
			FileUniqueId: "u1",
			Duration:     5,
			MimeType:     "audio/ogg",
			FileSize:     2997348,
		},
	}
	in := convertInbound("telegram", msg, "[Transcribed voice]: ")
	if in == nil {
		t.Fatal("nil")
	}
	if len(in.Attachments) != 1 || in.Attachments[0].Kind != "voice" {
		t.Errorf("Attachments = %+v", in.Attachments)
	}
	if in.Attachments[0].FileID != "AwACAgUAAyEFAAT..." {
		t.Errorf("FileID = %q", in.Attachments[0].FileID)
	}
}

func TestConvertInbound_ReplyContext(t *testing.T) {
	parent := &gotgbot.Message{
		MessageId: 281,
		From:      &gotgbot.User{Id: 999, Username: "OCDWaterBot"},
		Chat:      gotgbot.Chat{Id: -100},
		Text:      "previous message",
	}
	msg := &gotgbot.Message{
		MessageId:      900,
		From:           &gotgbot.User{Id: 42, Username: "skarthi"},
		Chat:           gotgbot.Chat{Id: -100},
		ReplyToMessage: parent,
		Text:           "reply",
	}
	in := convertInbound("telegram", msg, "")
	if in.ReplyTo == nil {
		t.Fatal("ReplyTo nil")
	}
	if in.ReplyTo.MessageID != 281 {
		t.Errorf("ReplyTo.MessageID=%d", in.ReplyTo.MessageID)
	}
	if in.ReplyTo.User.Username != "OCDWaterBot" {
		t.Errorf("ReplyTo.User=%+v", in.ReplyTo.User)
	}
	if in.ReplyTo.Text != "previous message" {
		t.Errorf("ReplyTo.Text=%q", in.ReplyTo.Text)
	}
}

func TestConvertInbound_PhotoPicksHighestResolution(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId: 1,
		From:      &gotgbot.User{Id: 42},
		Chat:      gotgbot.Chat{Id: -100},
		Photo: []gotgbot.PhotoSize{
			{FileId: "small", Width: 100, Height: 100},
			{FileId: "big", Width: 1024, Height: 1024},
			{FileId: "med", Width: 500, Height: 500},
		},
		Caption: "look",
	}
	in := convertInbound("telegram", msg, "")
	if len(in.Attachments) != 1 || in.Attachments[0].Kind != "photo" {
		t.Fatalf("Attachments=%+v", in.Attachments)
	}
	if in.Attachments[0].FileID != "big" {
		t.Errorf("expected highest-res FileID=big, got %q", in.Attachments[0].FileID)
	}
	if in.Text != "look" {
		t.Errorf("Caption should populate Text, got %q", in.Text)
	}
}

func TestConvertInbound_ServiceMessageDropped(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId:         5,
		Chat:              gotgbot.Chat{Id: -100},
		ForumTopicCreated: &gotgbot.ForumTopicCreated{Name: "new topic", IconColor: 0},
	}
	if in := convertInbound("telegram", msg, ""); in != nil {
		t.Errorf("expected nil for forum_topic_created service message, got %+v", in)
	}
}

func TestConvertInbound_NilMessageReturnsNil(t *testing.T) {
	if in := convertInbound("telegram", nil, ""); in != nil {
		t.Errorf("expected nil, got %+v", in)
	}
}
