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
//
// Media SENDING is P3; the gate leaves Media on the parts as-is in v1.
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

	// --- Emit parts ------------------------------------------------------
	parts = make([]c3types.Outbound, 0, len(textParts))
	for i, t := range textParts {
		p := c3types.Outbound{
			Channel: out.Channel,
			ChatID:  out.ChatID,
			TopicID: out.TopicID,
			Text:    t,
			Markup:  markup,
			// Media stays on the FIRST part only (media sending is P3; this is a
			// no-op in practice because Media is normally empty in P2b). Keeping
			// it on part 0 avoids duplicating media across split text parts.
		}
		if i == 0 {
			p.Media = out.Media
			p.Poll = out.Poll
			p.ReplyTo = out.ReplyTo
		}
		parts = append(parts, p)
	}
	return parts, notes, alts, nil
}

// utf16Len returns the length of s in UTF-16 code units — the unit Telegram
// counts a message length in. A BMP rune is 1 unit; an astral rune (e.g. most
// emoji) is a surrogate pair = 2 units. We measure on []rune→utf16.Encode so
// the count matches Telegram's, not Go's byte or rune count.
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}
