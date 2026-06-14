// Package capability is the pure, channel-neutral validate+degrade choke point
// (the "Gate") between the broker and a Channel, plus the agent-facing
// capability guidance renderer. It is L2 in the Capability-Manifest-Gate
// architecture (spec 2026-06-14-channel-capability-architecture §"Layers").
//
// Purity contract (load-bearing, enforced by the P7 grep-guard): this package
// imports ONLY internal/c3types + the Go standard library. It does NOT log,
// stat files, dial the network, or import broker/channel/gotgbot. The gate
// returns structured Alterations; the impure broker dispatch writes the durable
// log line from them.
package capability

import (
	"fmt"
	"unicode/utf16"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Gate is the single validate+degrade choke point for an outbound message. It
// takes a channel's capability manifest and one logical Outbound and returns:
//
//   - parts: the Outbound split into one or more ordered messages, each whose
//     text fits the channel's MaxMessageRunes (measured in UTF-16 code units —
//     what Telegram counts). Each part carries the same Markup/ChatID/TopicID;
//     ReplyTo applies only to the FIRST part (reply-threading targets the first
//     message of a multi-part reply).
//   - notes: human-readable strings the dispatch surfaces back to the agent so
//     it knows what was altered (or rejected).
//   - alts: structured Alteration records the impure dispatch turns into durable
//     log lines.
//   - err: a non-nil error signals a HARD REJECTION — the message cannot be
//     delivered as authored and dispatch must surface the error to the agent
//     WITHOUT sending anything (matching how dispatch surfaces tool errors).
//
// v1 responsibilities:
//   - Validate: reject a poll when the channel can't do polls (!c.Polls).
//   - Degrade markup: force Markup=none when the channel has no rich text.
//   - Chunk text: construct-aware split of the SOURCE markdown so a split never
//     bisects a fenced code block, a [label](url) link, or a blockquote run.
//   - Degrade media (P3): demote an unsupported Kind (photo→file when the
//     channel can't send a compressed photo but can send an original file;
//     otherwise drop the item with a note + Alteration).
//
// Single-part content contract (P3): each emitted part carries AT MOST ONE of
// {text, a single media item, a poll}. The layout is:
//
//	parts = [text chunks...] ++ [one part per media item] ++ [poll part if any]
//
// ReplyTo applies only to the FIRST part overall (reply-threading targets the
// first message of a multi-part reply). Album grouping is descoped in v1 — every
// media item is its own single send.
func Gate(c c3types.Capabilities, out c3types.Outbound) (parts []c3types.Outbound, notes []string, alts []c3types.Alteration, err error) {
	// --- Validation (hard rejection) -------------------------------------
	// A poll on a channel that can't render one cannot be silently dropped or
	// degraded into something else server-side — the agent must know so it can
	// re-render the choices as numbered text. Surface this as an error so
	// dispatch tells the agent (no send), matching tool-error surfacing.
	if out.Poll != nil && !c.Polls {
		return nil, nil,
			[]c3types.Alteration{{
				Kind:   "poll_rejected",
				Detail: "poll dropped: channel does not support polls",
			}},
			fmt.Errorf("polls are not supported on this channel — render the choices as numbered text in a normal reply instead")
	}

	// --- Markup degradation ----------------------------------------------
	markup := out.Markup
	if !c.RichText && markup != c3types.MarkupNone {
		notes = append(notes, "rich text is not supported on this channel — markdown was sent as plain text")
		alts = append(alts, c3types.Alteration{
			Kind:   "markup_degraded",
			Detail: fmt.Sprintf("markup %q downgraded to none (channel RichText=false)", string(markup)),
		})
		markup = c3types.MarkupNone
	}

	// --- Text chunking (construct-aware, UTF-16 measured) ----------------
	// MaxMessageRunes <= 0 means "no advertised limit" — emit a single part.
	limit := c.MaxMessageRunes
	var textParts []string
	if limit <= 0 || utf16Len(out.Text) <= limit {
		textParts = []string{out.Text}
	} else {
		var chunkAlts []c3types.Alteration
		textParts, chunkAlts = chunkMarkdown(out.Text, limit)
		if len(textParts) > 1 {
			notes = append(notes, fmt.Sprintf("reply was split into %d messages (edits/replies reference the first)", len(textParts)))
		}
		alts = append(alts, chunkAlts...)
		for _, a := range chunkAlts {
			if a.Kind == "hard_split" {
				notes = append(notes, "a single construct exceeded the message limit and was hard-split — formatting may be affected")
				break
			}
		}
	}

	// --- Media degradation (P3, data-driven) -----------------------------
	// For each media item, if its Kind is not in the channel's MediaKinds set,
	// demote it (photo→file when the channel can't compress but can send an
	// original file) or drop it (with a note + Alteration). For Telegram all
	// kinds are supported so this is a no-op; it is generic so a leaner channel
	// degrades correctly.
	mediaItems := make([]c3types.MediaItem, 0, len(out.Media))
	for _, m := range out.Media {
		if mediaKindSupported(c, m.Kind) {
			mediaItems = append(mediaItems, m)
			continue
		}
		// Unsupported kind. The only sanctioned demotion is photo→file: a
		// compressed-photo preview can fall back to the original-file send.
		if m.Kind == c3types.MediaPhoto && !c.CompressedPhoto && c.OriginalFile &&
			mediaKindSupported(c, c3types.MediaFile) {
			demoted := m
			demoted.Kind = c3types.MediaFile
			mediaItems = append(mediaItems, demoted)
			notes = append(notes, "a photo was sent as a file (this channel cannot send a compressed photo preview)")
			alts = append(alts, c3types.Alteration{
				Kind:   "media_demoted",
				Detail: "photo demoted to file (channel CompressedPhoto=false, OriginalFile=true)",
			})
			continue
		}
		// No safe demotion — drop it so the agent knows.
		notes = append(notes, fmt.Sprintf("a %q media item was dropped: this channel cannot send it", string(m.Kind)))
		alts = append(alts, c3types.Alteration{
			Kind:   "media_dropped",
			Detail: fmt.Sprintf("media kind %q unsupported and not demotable (channel MediaKinds=%v)", string(m.Kind), c.MediaKinds),
		})
	}

	// --- Emit parts ------------------------------------------------------
	// Layout: [text chunks...] ++ [one part per media item] ++ [poll part].
	// Each part carries AT MOST ONE of {text, single media item, poll}. ReplyTo
	// rides only the FIRST part overall.
	parts = make([]c3types.Outbound, 0, len(textParts)+len(mediaItems)+1)
	// Suppress empty text parts ONLY when there is other content (media/poll) to
	// carry the reply — otherwise an empty text part would send a bogus empty
	// message ahead of the media. When text is the only content, an empty part is
	// preserved so dispatch surfaces the natural "empty message" error as before.
	hasOtherContent := len(mediaItems) > 0 || out.Poll != nil
	for _, t := range textParts {
		if t == "" && hasOtherContent {
			continue
		}
		parts = append(parts, c3types.Outbound{
			Channel: out.Channel,
			ChatID:  out.ChatID,
			TopicID: out.TopicID,
			Text:    t,
			Markup:  markup,
		})
	}
	for i := range mediaItems {
		parts = append(parts, c3types.Outbound{
			Channel: out.Channel,
			ChatID:  out.ChatID,
			TopicID: out.TopicID,
			Markup:  markup,
			Media:   []c3types.MediaItem{mediaItems[i]},
		})
	}
	if out.Poll != nil {
		parts = append(parts, c3types.Outbound{
			Channel: out.Channel,
			ChatID:  out.ChatID,
			TopicID: out.TopicID,
			Poll:    out.Poll,
		})
	}
	// ReplyTo rides only the FIRST part overall (text if any, else first media,
	// else the poll). textParts always has >=1 element ("" when Text empty), so
	// parts is non-empty whenever there is any content.
	if out.ReplyTo != nil && len(parts) > 0 {
		parts[0].ReplyTo = out.ReplyTo
	}
	return parts, notes, alts, nil
}

// mediaKindSupported reports whether the channel's manifest lists kind in its
// sendable MediaKinds.
func mediaKindSupported(c c3types.Capabilities, kind c3types.MediaKind) bool {
	for _, k := range c.MediaKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// utf16Len returns the length of s in UTF-16 code units — the unit Telegram
// counts a message length in. A BMP rune is 1 unit; an astral rune (e.g. most
// emoji) is a surrogate pair = 2 units. We measure on []rune→utf16.Encode so
// the count matches Telegram's, not Go's byte or rune count.
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}
