package capability

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// telegramLikeCaps is a full-featured manifest mirroring the Telegram literal,
// used to anti-drift-check the POSITIVE guidance lines.
func telegramLikeCaps() c3types.Capabilities {
	return c3types.Capabilities{
		Channel:         "telegram",
		RichText:        true,
		MaxMessageRunes: 4096,
		MaxCaptionRunes: 1024,
		AutoChunks:      true,
		MediaKinds: []c3types.MediaKind{
			c3types.MediaPhoto, c3types.MediaFile, c3types.MediaVideo,
			c3types.MediaAudio, c3types.MediaVoice, c3types.MediaAnimation,
		},
		CompressedPhoto:  true,
		OriginalFile:     true,
		Albums:           false,
		MaxSendBytes:     50 * 1024 * 1024,
		Polls:            true,
		Reactions:        true,
		ReactionsSingle:  true,
		EditMessages:     true,
		Threads:          true,
		Typing:           true,
		ExpandableQuotes: true,
		Inbound: c3types.InboundCaps{
			DeliversPollResults: true,
			DeliversReactions:   true,
			DeliversCallbacks:   true,
		},
		Stream: c3types.StreamCaps{StreamViaEdit: false},
	}
}

func assertContainsAll(t *testing.T, s string, subs []string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("guidance missing expected substring:\n  want: %q\n  got:\n%s", sub, s)
		}
	}
}

func assertContainsNone(t *testing.T, s string, subs []string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("guidance unexpectedly contains substring %q\n  got:\n%s", sub, s)
		}
	}
}

func TestGuidanceFor_PositiveLines(t *testing.T) {
	g := GuidanceFor(telegramLikeCaps())
	assertContainsAll(t, g, []string{
		"CHANNEL CAPABILITIES (telegram):",
		// Rich text.
		"Rich text: YES",
		"Write standard markdown",
		// Expandable "Show more" blockquote guidance (gated on ExpandableQuotes).
		"collapse behind a 'Show more' chevron",
		"end the\n  blockquote with a line containing only `||`",
		// Media: the load-bearing file-vs-photo distinction.
		`kind="file" delivers the ORIGINAL bytes`,
		`kind="photo" is a COMPRESSED in-chat preview`,
		// Typing is automatic, not a tool.
		"shown automatically while you work",
		"do NOT call any typing tool",
		// Polls supported, including the full P2 surface (quiz/explanation/timed).
		"Polls: supported",
		`type="quiz"`,
		"correct_option",
		"explanation",
		"open_period",
		"close_date",
		// Poll-result reading (P4) — gated on Inbound.DeliversPollResults.
		"delivered automatically as a `<channel>` event when the poll CLOSES",
		"AGGREGATE tallies only",
		"`stop_poll` tool",
		// Inbound reaction + callback events (P4) — gated on the delivery bools.
		"Inbound reactions:",
		"Button presses:",
		"auto-acknowledged",
	})
	// On a fully-supported channel, the feature negatives do not appear.
	// (Streaming is OFF even on the full Telegram manifest in v1, so its
	// negative line is asserted by TestGuidanceFor_NegativeLines, not here.)
	assertContainsNone(t, g, []string{
		"Polls: NOT supported",
		"Rich text: NOT supported",
		"Media: NOT supported",
	})
}

func TestGuidanceFor_NegativeLines(t *testing.T) {
	caps := telegramLikeCaps()
	caps.Polls = false
	caps.Stream.StreamViaEdit = false // explicitly the v1 default
	caps.ExpandableQuotes = false     // a channel without the Show-more affordance
	caps.Inbound.DeliversPollResults = false
	caps.Inbound.DeliversReactions = false
	caps.Inbound.DeliversCallbacks = false
	g := GuidanceFor(caps)
	assertContainsAll(t, g, []string{
		"Polls: NOT supported",
		"Streaming of reasoning: NOT available",
	})
	// Inbound-event + expandable-quote guidance are gated on manifest bools — a
	// channel without those capabilities must NOT advertise them.
	assertContainsNone(t, g, []string{
		"collapse behind a 'Show more' chevron",
		"delivered automatically as a `<channel>` event when the poll CLOSES",
		"Inbound reactions:",
		"Button presses:",
	})
}
