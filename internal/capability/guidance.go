package capability

import (
	"fmt"
	"strings"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// GuidanceFor renders the agent-facing capability + formatting guidance for a
// channel, derived ENTIRELY from the manifest c so it can never drift from what
// the channel actually does (the anti-drift property the P4 golden test
// asserts). It emits POSITIVE guidance for supported capabilities and explicit
// NEGATIVE guidance for any false capability, so the agent never formulates
// content the channel can't deliver.
//
// Spec 2026-06-14-channel-capability-architecture §"GuidanceFor template".
func GuidanceFor(c c3types.Capabilities) string {
	var b strings.Builder

	fmt.Fprintf(&b, "CHANNEL CAPABILITIES (%s):\n", c.Channel)

	// Rich text.
	if c.RichText {
		b.WriteString("- Rich text: YES. Write standard markdown (bold, italic, links, lists, inline code, code blocks,\n")
		b.WriteString("  block quotes, strikethrough, spoilers). C3 converts + escapes for you — do NOT hand-write HTML or\n")
		if c.MaxMessageRunes > 0 {
			fmt.Fprintf(&b, "  channel tags. A reply longer than ~%d chars is split automatically into several messages\n", c.MaxMessageRunes)
			b.WriteString("  (edits/replies reference the first).\n")
		} else {
			b.WriteString("  channel tags. Long replies are split automatically into several messages (edits/replies reference the first).\n")
		}
	} else {
		b.WriteString("- Rich text: NOT supported — write PLAIN text only; markdown/HTML will appear as literal characters.\n")
		if c.MaxMessageRunes > 0 {
			fmt.Fprintf(&b, "  A reply longer than ~%d chars is split automatically into several messages (edits/replies reference the first).\n", c.MaxMessageRunes)
		}
	}

	// Media.
	if len(c.MediaKinds) > 0 {
		b.WriteString("- Media: send via the `media` arg.")
		if c.OriginalFile {
			b.WriteString(` kind="file" delivers the ORIGINAL bytes (PDFs, logs, originals);`)
		}
		if c.CompressedPhoto {
			b.WriteString("\n  ")
			b.WriteString(`kind="photo" is a COMPRESSED in-chat preview (loses original bytes/EXIF).`)
		}
		others := otherMediaKinds(c.MediaKinds)
		if len(others) > 0 {
			fmt.Fprintf(&b, " Also: %s.", strings.Join(others, ", "))
		}
		if c.MaxSendBytes > 0 {
			fmt.Fprintf(&b, " Max ~%dMB per item.", c.MaxSendBytes/(1024*1024))
		}
		if !c.Albums {
			b.WriteString(" Multiple items are sent one after another.")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("- Media: NOT supported — this channel is text-only.\n")
	}

	// Polls.
	if c.Polls {
		b.WriteString("- Polls: supported via the `poll` tool.\n")
	} else {
		b.WriteString("- Polls: NOT supported — render the choices as numbered text in a normal reply.\n")
	}

	// Reactions.
	if c.Reactions {
		if c.ReactionsSingle {
			b.WriteString("- Reactions: supported via the `react` tool (ONE emoji per message).\n")
		} else {
			b.WriteString("- Reactions: supported via the `react` tool.\n")
		}
	} else {
		b.WriteString("- Reactions: NOT supported.\n")
	}

	// Edits.
	if c.EditMessages {
		b.WriteString("- Editing: supported via the `edit_message` tool (an edit is a single message — it cannot be split).\n")
	} else {
		b.WriteString("- Editing: NOT supported — send a new message instead.\n")
	}

	// Typing.
	if c.Typing {
		b.WriteString("- Typing: shown automatically while you work — do NOT call any typing tool.\n")
	} else {
		b.WriteString("- Typing: not shown on this channel.\n")
	}

	// Streaming.
	if c.Stream.StreamViaEdit {
		b.WriteString("- Streaming of reasoning: available on this channel.\n")
	} else {
		b.WriteString("- Streaming of reasoning: NOT available on this channel.\n")
	}

	return b.String()
}

// otherMediaKinds returns the supported media kinds other than photo/file (which
// have dedicated positive lines above), in a stable order, for the "Also: …"
// clause.
func otherMediaKinds(kinds []c3types.MediaKind) []string {
	order := []c3types.MediaKind{
		c3types.MediaVideo,
		c3types.MediaAudio,
		c3types.MediaVoice,
		c3types.MediaAnimation,
	}
	set := make(map[c3types.MediaKind]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	var out []string
	for _, k := range order {
		if set[k] {
			out = append(out, string(k))
		}
	}
	return out
}
