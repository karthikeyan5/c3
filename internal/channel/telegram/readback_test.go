package telegram

import (
	"fmt"
	"strings"
	"testing"
)

// rbSentenceDoc builds an ASCII transcript (u16 == byte len == rune count) of
// roughly target chars made of uniquely-tagged sentences "S0 wwww.". Returns
// (text, tokens) where token i ("S{i}") begins sentence i. Mirrors the Python
// test's sentence_doc so the band fixtures are comparable across the two suites.
func rbSentenceDoc(target, sentLen int) (string, []string) {
	var parts, tokens []string
	i, total := 0, 0
	for total < target {
		tag := fmt.Sprintf("S%d", i)
		fillN := sentLen - len(tag) - 2
		if fillN < 1 {
			fillN = 1
		}
		s := tag + " " + strings.Repeat("w", fillN) + "."
		parts = append(parts, s)
		tokens = append(tokens, tag)
		total += len(s) + 1
		i++
	}
	return strings.Join(parts, " "), tokens
}

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"latin", "One. Two. Three.", []string{"One.", "Two.", "Three."}},
		{"mixed terminators", "Hi! Are you ok? Yes.", []string{"Hi!", "Are you ok?", "Yes."}},
		{"devanagari danda", "क ख। ग घ। च", []string{"क ख।", "ग घ।", "च"}},
		{"ellipsis", "Well… I think so.", []string{"Well…", "I think so."}},
		{"runon no punctuation", "word0 word1 word2 word3", []string{"word0 word1 word2 word3"}},
		{"trailing whitespace trimmed", "  A.  B.  ", []string{"A.", "B."}},
		{"empty", "   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSentences(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitSentences(%q) = %#v; want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitSentences(%q)[%d] = %q; want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestBuildPreview(t *testing.T) {
	// >= 13 sentences: first 3 / last 3 / M = total − 6.
	thirteen := []string{"a.", "b.", "c.", "d.", "e.", "f.", "g.", "h.", "i.", "j.", "k.", "l.", "m."}
	f3, l3, more := buildPreview(thirteen)
	if f3 != "a. b. c." {
		t.Errorf("first3 = %q; want %q", f3, "a. b. c.")
	}
	if l3 != "k. l. m." {
		t.Errorf("last3 = %q; want %q", l3, "k. l. m.")
	}
	if more != 7 {
		t.Errorf("more = %d; want 7 (13 − 6)", more)
	}

	// < 13 sentences: all joined as first3, empty last3, M = 0 (no elision).
	twelve := []string{"a.", "b.", "c.", "d.", "e.", "f.", "g.", "h.", "i.", "j.", "k.", "l."}
	want := "a. b. c. d. e. f. g. h. i. j. k. l."
	f3, l3, more = buildPreview(twelve)
	if f3 != want || l3 != "" || more != 0 {
		t.Errorf("buildPreview(12) = (%q, %q, %d); want (%q, \"\", 0)", f3, l3, more, want)
	}
}

func TestUint16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"🎤", 2},            // astral rune = surrogate pair = 2 UTF-16 units
		{"a🎤🎤", 5},          // 1 + 2 + 2
		{"नमस्ते", 6},        // BMP runes count 1 each
	}
	for _, tc := range cases {
		if got := uint16Len(tc.in); got != tc.want {
			t.Errorf("uint16Len(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

func TestHTMLEscape(t *testing.T) {
	// '&' first, no double escaping.
	if got := htmlEscape("a & b < c > d"); got != "a &amp; b &lt; c &gt; d" {
		t.Errorf("htmlEscape = %q", got)
	}
	if strings.Contains(htmlEscape("a < b"), "&amp;lt;") {
		t.Error("htmlEscape double-escaped '<' (& applied after <)")
	}
	// A literal close tag in the transcript is neutralized.
	got := htmlEscape("please </blockquote> literally")
	if !strings.Contains(got, "&lt;/blockquote&gt;") || strings.Contains(got, "</blockquote>") {
		t.Errorf("htmlEscape did not neutralize </blockquote>: %q", got)
	}
}

func TestRenderReadback_BandSelection(t *testing.T) {
	tiny, _ := "One. Two. Three.", 0        // 3 sentences → TINY
	short, _ := rbSentenceDoc(2000, 40)     // ~48 sentences, ~2k visible → SHORT
	deadzone, _ := rbSentenceDoc(5000, 40)  // ~5k visible (4096<x≤9000 dead zone) → DEADZONE <details>
	long, _ := rbSentenceDoc(12000, 40)     // ~12k visible (>9000) but rich ≤32000 → LONG
	huge, _ := rbSentenceDoc(33000, 40)     // rich html >32000 bytes → HUGE

	cases := []struct {
		name       string
		in         string
		wantMethod string
		wantBand   readbackBand
	}{
		{"tiny", tiny, "sendMessage", bandTiny},
		{"short", short, "sendMessage", bandShort},
		{"deadzone", deadzone, "sendRichMessage", bandDeadzone},
		{"long", long, "sendRichMessage", bandLong},
		{"huge", huge, "sendDocument", bandHuge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, payload, band := renderReadback(tc.in)
			if band != tc.wantBand || method != tc.wantMethod {
				t.Fatalf("renderReadback(%s) → method=%q band=%v; want method=%q band=%v",
					tc.name, method, band, tc.wantMethod, tc.wantBand)
			}
			switch band {
			case bandHuge:
				if payload != "" {
					t.Errorf("HUGE band payload should be empty (caller builds the doc), got %q", payload)
				}
			default:
				if payload == "" {
					t.Errorf("%v band payload should be non-empty", band)
				}
			}
			if band == bandDeadzone && !strings.Contains(payload, "<details>") {
				t.Errorf("DEADZONE band payload must contain a <details> collapse, got %q", payload)
			}
		})
	}
}

// TestRenderReadback_ShortExact pins the EXACT assembled SHORT-band string
// (header · word count, first-3 … M more … last-3 summary, the heading, and the
// whole verbatim transcript in an expandable blockquote).
func TestRenderReadback_ShortExact(t *testing.T) {
	in := "S1 alpha. S2 bravo. S3 charlie. S4 delta. S5 echo. S6 foxtrot. S7 golf. " +
		"S8 hotel. S9 india. S10 juliet. S11 kilo. S12 lima. S13 mike."
	method, payload, band := renderReadback(in)
	if band != bandShort || method != "sendMessage" {
		t.Fatalf("got method=%q band=%v; want sendMessage/short", method, band)
	}
	want := "🎤 <b>Voice transcript</b> · ~26 words\n" +
		"S1 alpha. S2 bravo. S3 charlie.\n" +
		"<i>✂️✂️ 7 more sentences ✂️✂️</i>\n" +
		"S11 kilo. S12 lima. S13 mike.\n\n" +
		"<b>Full Transcript</b>\n" +
		"<blockquote expandable>" + in + "</blockquote>"
	if payload != want {
		t.Errorf("SHORT payload mismatch:\n got: %q\nwant: %q", payload, want)
	}
}

// TestRenderReadback_LongAssembly verifies the LONG band's assembly: <p> blocks
// for the summary/heading AND a PLAIN <p> body for the whole transcript (no \n —
// rich messages don't honor it; no blockquote — Telegram's native "Show More"
// collapses the long plain rich message), against the same parts, without
// hardcoding the multi-KB transcript. The input is sized past the 9000 DISPLAYED
// native-collapse floor so it lands in LONG (not the 4k–9k document dead zone).
func TestRenderReadback_LongAssembly(t *testing.T) {
	in, _ := rbSentenceDoc(12000, 40)
	method, payload, band := renderReadback(in)
	if band != bandLong || method != "sendRichMessage" {
		t.Fatalf("got method=%q band=%v; want sendRichMessage/long", method, band)
	}
	sents := splitSentences(in)
	f3, l3, more := buildPreview(sents)
	words := len(strings.Fields(in))
	want := fmt.Sprintf("<p>🎤 <b>Voice transcript</b> · ~%d words</p>", words) +
		"<p>" + htmlEscape(f3) + "</p>" +
		fmt.Sprintf("<p><i>✂️✂️ %d more sentences ✂️✂️</i></p>", more) +
		"<p>" + htmlEscape(l3) + "</p>" +
		"<p><b>Full Transcript</b></p>" +
		"<p>" + htmlEscape(in) + "</p>"
	if payload != want {
		t.Errorf("LONG payload mismatch:\n got (len=%d): %.120q…\nwant (len=%d): %.120q…",
			len(payload), payload, len(want), want)
	}
	if strings.Contains(payload, "\n") {
		t.Error("LONG band must not contain \\n (rich messages don't honor it)")
	}
	if strings.Contains(payload, "<blockquote") {
		t.Error("LONG band must be a plain <p> body, not a blockquote")
	}
	if strings.Contains(payload, "<details") {
		t.Error("LONG band must be a plain <p> body, not a <details> collapse")
	}
	if !strings.Contains(payload, "<p>") {
		t.Error("LONG band must assemble the summary/body as <p> blocks")
	}
	if !strings.Contains(payload, fmt.Sprintf("✂️✂️ %d more sentences ✂️✂️", more)) {
		t.Error("LONG band must carry the elision marker")
	}
	if len(payload) > readbackRichMaxBytes {
		t.Errorf("LONG payload %d bytes exceeds rich budget %d", len(payload), readbackRichMaxBytes)
	}
}

// TestRenderReadback_TinyEscapes confirms the TINY band escapes dynamic text and
// carries no summary/heading/collapse.
func TestRenderReadback_TinyEscapes(t *testing.T) {
	in := "Tom & Jerry < Spike. Render </blockquote> please. Done."
	method, payload, band := renderReadback(in)
	if band != bandTiny || method != "sendMessage" {
		t.Fatalf("got method=%q band=%v; want sendMessage/tiny", method, band)
	}
	if !strings.HasPrefix(payload, "🎤 <b>Voice transcript</b>\n") {
		t.Errorf("TINY payload prefix wrong: %q", payload)
	}
	for _, want := range []string{"&amp;", "&lt;", "&gt;", "&lt;/blockquote&gt;"} {
		if !strings.Contains(payload, want) {
			t.Errorf("TINY payload missing escape %q: %q", want, payload)
		}
	}
	if strings.Contains(payload, "</blockquote>") || strings.Contains(payload, "<blockquote") {
		t.Error("TINY band must not carry a structural blockquote")
	}
	if strings.Contains(payload, "Full Transcript") || strings.Contains(payload, "more sentences") {
		t.Error("TINY band must carry no summary/heading")
	}
}

// TestRenderReadback_TwelveSentencesTiny pins the no-elide threshold: 12
// sentences (= 2× the 6-sentence preview) still render as the bare TINY band,
// because only >12 sentences have a meaningful middle to elide.
func TestRenderReadback_TwelveSentencesTiny(t *testing.T) {
	in := "A. B. C. D. E. F. G. H. I. J. K. L."
	if n := len(splitSentences(in)); n != 12 {
		t.Fatalf("fixture has %d sentences, want 12", n)
	}
	method, payload, band := renderReadback(in)
	if band != bandTiny || method != "sendMessage" {
		t.Fatalf("got method=%q band=%v; want sendMessage/tiny", method, band)
	}
	want := "🎤 <b>Voice transcript</b>\n" + htmlEscape(in)
	if payload != want {
		t.Errorf("TINY payload mismatch:\n got: %q\nwant: %q", payload, want)
	}
	if strings.Contains(payload, "more sentences") || strings.Contains(payload, "Full Transcript") {
		t.Error("TINY band must carry no summary/elision/heading")
	}
}

// TestReadbackCaption keeps the .txt fallback caption within the Telegram cap.
func TestReadbackCaption(t *testing.T) {
	in, _ := rbSentenceDoc(33000, 300)
	capt := readbackCaption(in)
	if uint16Len(capt) > readbackCaptionMaxU16 {
		t.Errorf("caption %d UTF-16 units exceeds %d", uint16Len(capt), readbackCaptionMaxU16)
	}
	if !strings.HasPrefix(capt, "🎤 <b>Voice transcript</b> · ~") {
		t.Errorf("caption prefix wrong: %.60q", capt)
	}
}
