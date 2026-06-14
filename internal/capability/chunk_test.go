package capability

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// allPar,partsWithinLimit asserts every returned part fits the UTF-16 limit.
func partsWithinLimit(t *testing.T, parts []string, limit int) {
	t.Helper()
	for i, p := range parts {
		if got := utf16Len(p); got > limit {
			t.Errorf("part %d is %d UTF-16 units, over limit %d: %q", i, got, limit, p)
		}
	}
}

func hasHardSplit(alts []c3types.Alteration) bool {
	for _, a := range alts {
		if a.Kind == "hard_split" {
			return true
		}
	}
	return false
}

// joinedContains is a soft assertion that a substring survives somewhere in the
// concatenation of the parts — used to confirm an indivisible construct was not
// bisected (the whole construct appears intact in exactly one part).
func constructInOnePart(parts []string, construct string) bool {
	for _, p := range parts {
		if strings.Contains(p, construct) {
			return true
		}
	}
	return false
}

func TestChunkMarkdown_LinkNotBisected(t *testing.T) {
	limit := 40
	link := "[docs](https://ex.com/path)" // 27 units — fits the 40 limit on its own
	// Pad before the link so a naive cut at unit 40 would fall INSIDE the link;
	// a construct-aware split must instead keep the whole link in one part.
	src := "see this " + link + " for more details please"
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	if !constructInOnePart(parts, link) {
		t.Errorf("link was bisected across parts; parts=%q", parts)
	}
}

func TestChunkMarkdown_BlockquoteNotBisected(t *testing.T) {
	limit := 50
	// A blockquote run that as a whole exceeds the limit, packed with a
	// neighboring paragraph so a naive packer would want to split mid-run.
	quote := "> first quoted line here\n> second quoted line here\n> third quoted line here"
	src := "intro paragraph\n\n" + quote + "\n\noutro paragraph"
	parts, alts := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	// The blockquote run is atomic but it exceeds the limit on its own, so it is
	// hard-split (with an Alteration) rather than packed by ordinary boundaries.
	if !hasHardSplit(alts) {
		t.Errorf("expected a hard_split Alteration for an over-limit blockquote run; alts=%+v", alts)
	}
	// Each blockquote LINE must stay intact (the run is split only between its
	// own lines by hardSplit at rune boundaries, never inside a quoted line that
	// fits). Assert the second line is intact in one part.
	if !constructInOnePart(parts, "> second quoted line here") {
		t.Errorf("a blockquote line was bisected; parts=%q", parts)
	}
}

func TestChunkMarkdown_FencedCodeNotBisected(t *testing.T) {
	limit := 60
	// A fenced code block that as a whole fits the limit but straddles the
	// boundary against a neighboring paragraph: it must move whole to its own
	// part, never split.
	code := "```\nx := 1\ny := 2\n```"
	src := "before the code block here padding padding\n\n" + code + "\n\nafter"
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	if !constructInOnePart(parts, code) {
		t.Errorf("fenced code block was bisected across parts; parts=%q", parts)
	}
}

func TestChunkMarkdown_SingleLongLinkHardSplit(t *testing.T) {
	limit := 30
	// One indivisible link longer than the limit: there is no safe boundary
	// inside it, so it MUST hard-split and record the Alteration.
	link := "[label](https://example.com/" + strings.Repeat("a", 80) + ")"
	parts, alts := chunkMarkdown(link, limit)
	partsWithinLimit(t, parts, limit)
	if len(parts) < 2 {
		t.Fatalf("expected the over-limit link to split into >1 part; got %d", len(parts))
	}
	if !hasHardSplit(alts) {
		t.Errorf("expected a hard_split Alteration for an over-limit single link; alts=%+v", alts)
	}
}

func TestChunkMarkdown_SingleLongFencedBlockHardSplit(t *testing.T) {
	limit := 40
	// A single fenced code block longer than the limit: atomic + over-limit =>
	// hard-split with an Alteration.
	code := "```\n" + strings.Repeat("line of code text\n", 10) + "```"
	parts, alts := chunkMarkdown(code, limit)
	partsWithinLimit(t, parts, limit)
	if len(parts) < 2 {
		t.Fatalf("expected the over-limit fenced block to split into >1 part; got %d", len(parts))
	}
	if !hasHardSplit(alts) {
		t.Errorf("expected a hard_split Alteration for an over-limit fenced block; alts=%+v", alts)
	}
}

func TestChunkMarkdown_UTF16Counting(t *testing.T) {
	// Emoji are astral (surrogate pair = 2 UTF-16 units each) and CJK are BMP
	// (1 unit each). With a limit of 6 units, "😀😀😀" is exactly 6 units and a
	// fourth would overflow. We build a single word (no safe boundary) so it
	// hard-splits at a rune boundary and never bisects a surrogate pair.
	limit := 6
	src := strings.Repeat("😀", 5) // 10 UTF-16 units, indivisible word
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	// Reassembly must reproduce the source (no runes dropped or duplicated).
	if got := strings.Join(parts, ""); got != src {
		t.Errorf("reassembly mismatch: got %q want %q", got, src)
	}
	// No part may contain a Unicode replacement char (would mean a bisected pair).
	for i, p := range parts {
		if strings.ContainsRune(p, '�') {
			t.Errorf("part %d contains a replacement char (surrogate pair bisected): %q", i, p)
		}
	}

	// CJK: a run of CJK chars at limit 3 -> each part holds <=3 CJK chars.
	cjk := "中文字符测试内容" // 8 BMP runes = 8 UTF-16 units
	cparts, _ := chunkMarkdown(cjk, 3)
	partsWithinLimit(t, cparts, 3)
	if got := strings.Join(cparts, ""); got != cjk {
		t.Errorf("CJK reassembly mismatch: got %q want %q", got, cjk)
	}
}

func TestChunkMarkdown_ParagraphBoundaryPreferred(t *testing.T) {
	// Two paragraphs that each fit but together exceed the limit: the split must
	// fall on the paragraph boundary, so each part is exactly one paragraph.
	limit := 20
	p1 := "first paragraph ok"  // 18 units
	p2 := "second paragraph ok" // 19 units
	src := p1 + "\n\n" + p2
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	if len(parts) != 2 {
		t.Fatalf("expected split on the paragraph boundary into 2 parts; got %d: %q", len(parts), parts)
	}
	if parts[0] != p1 || parts[1] != p2 {
		t.Errorf("paragraph boundary not preferred: got %q", parts)
	}
}

func TestChunkMarkdown_LineThenWordBoundary(t *testing.T) {
	// A single paragraph (one block, multiple lines) over the limit splits on
	// line boundaries first; a single over-limit line splits on word boundaries.
	limit := 20
	src := "alpha beta gamma\ndelta epsilon zeta eta theta iota kappa"
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	// No part should bisect a word: every space-separated word appears intact.
	for _, w := range strings.Fields(src) {
		if !constructInOnePart(parts, w) {
			t.Errorf("word %q was bisected across parts; parts=%q", w, parts)
		}
	}
}

func TestChunkMarkdown_RepacksShortLines(t *testing.T) {
	// Many short lines in one paragraph that together exceed the limit get
	// repacked greedily: more than one short line per part where they fit, and
	// fewer parts than lines.
	limit := 25
	lines := []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii", "jj", "kk", "ll"}
	src := strings.Join(lines, "\n")
	parts, _ := chunkMarkdown(src, limit)
	partsWithinLimit(t, parts, limit)
	if len(parts) >= len(lines) {
		t.Errorf("expected short lines to be repacked into fewer parts than lines (%d); got %d parts: %q",
			len(lines), len(parts), parts)
	}
	// At least one part must contain more than one line (proof of repacking).
	repacked := false
	for _, p := range parts {
		if strings.Count(p, "\n") >= 1 {
			repacked = true
			break
		}
	}
	if !repacked {
		t.Errorf("expected at least one part to repack multiple lines; parts=%q", parts)
	}
}
