// Package c3types holds wire-shaped Go types shared by channels, plugins,
// and the broker. Spec §4.1.
package c3types

import "time"

type Inbound struct {
	Channel     string
	ChatID      int64
	TopicID     *int64 // nil = no topic, &1 = General, >1 = custom
	MessageID   int64
	Sender      Sender
	Text        string
	Attachments []Attachment
	ReplyTo     *ReplyContext
	Timestamp   time.Time
}

type Sender struct {
	UserID   int64
	Username string
}

type Attachment struct {
	Kind   string // "voice", "audio", "video", "video_note", "document", "photo", "sticker"
	FileID string
	Size   int64
	MIME   string
	Name   string
}

type ReplyContext struct {
	MessageID int64
	User      Sender
	Text      string
}

type Outbound struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	Text      string
	Files     []string
	ParseMode string
	ReplyTo   *int64
}

type ReplyArgs = Outbound

type EditArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Text      string
	ParseMode string
}

type EditResult struct {
	MessageID int64
}

type ReactArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Emoji     string
}

type VoicePayload struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	MessageID int64
	FileID    string
	MIME      string
	Size      int64
}
