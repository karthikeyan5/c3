// Package c3types holds wire-shaped Go types shared by channels, plugins,
// and the broker. Spec §4.1.
package c3types

import "time"

// Inbound is one message received from a Channel, post-debounce and
// post-plugin-pipeline, on its way to a CLI adapter. The broker emits
// this as ipc.OpInbound when a stub holds the route claim.
//
// TopicID semantics for Telegram: nil = DM (no topic), &1 = General
// (Telegram's reserved root topic), and any positive int64 > 1 = a
// custom forum topic.
//
// ChatID sign convention follows Telegram's Bot API: positive = user
// or DM, negative = group/channel, -100… = supergroup/channel.
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

// Sender identifies the originator of an Inbound or ReplyContext. UserID
// is the channel's canonical user identifier; Username is a display-only
// secondary that may be empty (e.g. a user without a Telegram @handle).
type Sender struct {
	UserID   int64
	Username string
}

// Attachment is one piece of attached media on an Inbound. Channel
// implementations fill the fields they can; absent metadata is the
// zero value. Kind is an open enum on Telegram terms.
type Attachment struct {
	Kind   string // "voice", "audio", "video", "video_note", "document", "photo", "sticker"
	FileID string
	Size   int64
	MIME   string
	Name   string
}

// ReplyContext describes the message an Inbound is replying to (quote-
// reply). MessageID is the target message's id; User is its author;
// Text is a snippet of the target's content for context (may be empty
// or truncated by the channel).
type ReplyContext struct {
	MessageID int64
	User      Sender
	Text      string
}

// Outbound is one tool-call from a CLI adapter to a Channel for delivery.
// Used for both the `reply` tool and as the base shape for derived
// args (see ReplyArgs alias). Files is a list of local paths the
// channel uploads; ParseMode is channel-specific markup (e.g.
// "HTML" / "MarkdownV2" for Telegram).
type Outbound struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	Text      string
	Files     []string
	ParseMode string
	ReplyTo   *int64
}

// ReplyArgs is the argument shape for the `reply` MCP tool. Aliased to
// Outbound because the wire shape is the same — channels accept the
// same struct for both internal forwarding and adapter-initiated
// replies.
type ReplyArgs = Outbound

// EditArgs is the argument shape for the `edit_message` MCP tool.
// MessageID identifies the message to edit; Text is the new content;
// ParseMode follows the same convention as Outbound.
type EditArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Text      string
	ParseMode string
}

// EditResult is returned from Channel.EditMessage. MessageID echoes
// back the edited message's id (channels may rewrite it on edit; today
// Telegram doesn't, but the contract leaves room).
type EditResult struct {
	MessageID int64
}

// ReactArgs is the argument shape for the `react` MCP tool. Emoji must
// be a single-codepoint Telegram-supported reaction emoji; non-emoji
// values are rejected by the channel.
type ReactArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Emoji     string
}

// VoicePayload is the input the plugin host passes to OnVoiceReceived
// callbacks (the STT plugin's entry point). FileID identifies the
// voice attachment on the source channel; MIME and Size are hints
// for choosing the right transcription provider.
type VoicePayload struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	MessageID int64
	FileID    string
	MIME      string
	Size      int64
}
