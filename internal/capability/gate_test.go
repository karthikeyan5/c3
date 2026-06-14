package capability

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// richCaps is a Telegram-like manifest: rich text on, 4096 limit, polls on.
func richCaps() c3types.Capabilities {
	return c3types.Capabilities{
		Channel:         "telegram",
		RichText:        true,
		MaxMessageRunes: 4096,
		Polls:           true,
	}
}

func altKinds(alts []c3types.Alteration) []string {
	var out []string
	for _, a := range alts {
		out = append(out, a.Kind)
	}
	return out
}

func notesContain(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestGate_ShortText_SinglePart(t *testing.T) {
	parts, notes, alts, err := Gate(richCaps(), c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: "hello world", Markup: c3types.MarkupMarkdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected exactly 1 part for short text; got %d", len(parts))
	}
	if parts[0].Text != "hello world" {
		t.Errorf("part text mutated: %q", parts[0].Text)
	}
	if parts[0].Markup != c3types.MarkupMarkdown {
		t.Errorf("markup should be preserved on a rich-text channel; got %q", parts[0].Markup)
	}
	if len(notes) != 0 {
		t.Errorf("expected no notes for an unaltered short reply; got %v", notes)
	}
	if len(alts) != 0 {
		t.Errorf("expected no alterations; got %v", altKinds(alts))
	}
}

func TestGate_NoRichText_DegradesMarkup(t *testing.T) {
	caps := richCaps()
	caps.RichText = false
	parts, notes, alts, err := Gate(caps, c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: "**bold**", Markup: c3types.MarkupMarkdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 || parts[0].Markup != c3types.MarkupNone {
		t.Fatalf("expected markup degraded to none; parts=%+v", parts)
	}
	if !notesContain(notes, "rich text is not supported") {
		t.Errorf("expected a degradation note; got %v", notes)
	}
	wantAlt := false
	for _, a := range alts {
		if a.Kind == "markup_degraded" {
			wantAlt = true
		}
	}
	if !wantAlt {
		t.Errorf("expected a markup_degraded Alteration; got %v", altKinds(alts))
	}
}

func TestGate_PollUnsupported_HardReject(t *testing.T) {
	caps := richCaps()
	caps.Polls = false
	parts, _, alts, err := Gate(caps, c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: "vote",
		Poll: &c3types.PollSpec{Question: "q?", Options: []string{"a", "b"}},
	})
	if err == nil {
		t.Fatalf("expected a hard-reject error when polls unsupported")
	}
	if parts != nil {
		t.Errorf("expected no parts on hard reject; got %+v", parts)
	}
	found := false
	for _, a := range alts {
		if a.Kind == "poll_rejected" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a poll_rejected Alteration; got %v", altKinds(alts))
	}
}

func TestGate_LongText_SplitsWithNote(t *testing.T) {
	caps := richCaps()
	caps.MaxMessageRunes = 50
	// Two paragraphs that together exceed 50 units -> split into >1 part.
	long := strings.Repeat("alpha ", 12) + "\n\n" + strings.Repeat("beta ", 12)
	parts, notes, _, err := Gate(caps, c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: long, Markup: c3types.MarkupMarkdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected the long reply to split into >1 part; got %d", len(parts))
	}
	for i, p := range parts {
		if utf16Len(p.Text) > caps.MaxMessageRunes {
			t.Errorf("part %d over limit: %d > %d", i, utf16Len(p.Text), caps.MaxMessageRunes)
		}
	}
	if !notesContain(notes, "split into") {
		t.Errorf("expected a split note; got %v", notes)
	}
}

func TestGate_FirstPartCarriesReplyMediaPoll(t *testing.T) {
	caps := richCaps()
	caps.MaxMessageRunes = 50
	rt := int64(99)
	long := strings.Repeat("alpha ", 12) + "\n\n" + strings.Repeat("beta ", 12)
	parts, _, _, err := Gate(caps, c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: long, Markup: c3types.MarkupMarkdown,
		ReplyTo: &rt,
		Media:   []c3types.MediaItem{{Kind: c3types.MediaFile, Path: "/tmp/x"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("need >1 part to test first-part-only carry; got %d", len(parts))
	}
	// First part carries ReplyTo + Media.
	if parts[0].ReplyTo == nil || *parts[0].ReplyTo != rt {
		t.Errorf("first part should carry ReplyTo; got %+v", parts[0].ReplyTo)
	}
	if len(parts[0].Media) != 1 {
		t.Errorf("first part should carry Media; got %+v", parts[0].Media)
	}
	// Subsequent parts must NOT.
	for i := 1; i < len(parts); i++ {
		if parts[i].ReplyTo != nil {
			t.Errorf("part %d should not carry ReplyTo", i)
		}
		if len(parts[i].Media) != 0 {
			t.Errorf("part %d should not carry Media", i)
		}
		if parts[i].Poll != nil {
			t.Errorf("part %d should not carry Poll", i)
		}
	}
}

func TestGate_NoLimit_SinglePart(t *testing.T) {
	caps := richCaps()
	caps.MaxMessageRunes = 0 // no advertised limit
	long := strings.Repeat("x", 100000)
	parts, _, _, err := Gate(caps, c3types.Outbound{
		Channel: "telegram", ChatID: 1, Text: long, Markup: c3types.MarkupMarkdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Errorf("expected a single part when MaxMessageRunes<=0; got %d", len(parts))
	}
}
