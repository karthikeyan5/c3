package telegram

import (
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Bot API limits and ceilings the Telegram manifest advertises. Kept as named
// constants here (inside the telegram package) so no caller above this layer
// names the raw numbers — the spec's no-leak rule (R7).
const (
	// maxMessageRunes is Telegram's per-message text limit (UTF-16 units).
	maxMessageRunes = 4096
	// maxCaptionRunes is Telegram's per-media caption limit (UTF-16 units).
	maxCaptionRunes = 1024
	// maxSendBytes is the Bot API upload ceiling for media we send (50 MiB).
	maxSendBytes = 50 * 1024 * 1024
	// maxDownloadBytes is the Bot API download ceiling for getFile (20 MiB).
	maxDownloadBytes = 20 * 1024 * 1024
	// minEditInterval is the floor between successive edits before Telegram
	// starts rate-limiting; unused while streaming is deferred but reported
	// in the manifest for honesty.
	minEditInterval = 1 * time.Second
)

// Capabilities returns the static Telegram capability manifest. This is the
// authoritative inventory of what the Telegram channel can do; core code and
// the agent surface read it (carried over hello_ack / attached in later
// phases). It is a pure literal — no live bot state is consulted.
//
// v1 descopes: Albums=false (sequential single sends), Stream.StreamViaEdit=
// false (reasoning streaming deferred — no observable source frame).
func (c *Channel) Capabilities() c3types.Capabilities {
	return c3types.Capabilities{
		Channel:         Name,
		RichText:        true,
		MaxMessageRunes: maxMessageRunes,
		MaxCaptionRunes: maxCaptionRunes,
		AutoChunks:      true,
		MediaKinds: []c3types.MediaKind{
			c3types.MediaPhoto,
			c3types.MediaFile,
			c3types.MediaVideo,
			c3types.MediaAudio,
			c3types.MediaVoice,
			c3types.MediaAnimation,
		},
		CompressedPhoto: true,
		OriginalFile:    true,
		Albums:          false, // descoped in v1 — sequential single sends.
		MaxSendBytes:    maxSendBytes,
		Polls:           true,
		Reactions:       true,
		ReactionsSingle: true,
		EditMessages:    true,
		Threads:         true,
		Typing:          true,
		Inbound: c3types.InboundCaps{
			MaxDownloadBytes: maxDownloadBytes,
			// The attachment kinds Telegram delivers inbound. Mapped onto the
			// neutral MediaKind set; sticker/video_note have no neutral kind
			// in v1 and are omitted.
			InboundKinds: []c3types.MediaKind{
				c3types.MediaPhoto,
				c3types.MediaFile,
				c3types.MediaVideo,
				c3types.MediaAudio,
				c3types.MediaVoice,
				c3types.MediaAnimation,
			},
			SupportsReplyContext: true,
		},
		Stream: c3types.StreamCaps{
			StreamViaEdit:   false, // deferred in v1.
			MinEditInterval: minEditInterval,
		},
	}
}
