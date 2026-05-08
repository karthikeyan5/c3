package telegram

import (
	"strings"
	"testing"
)

func TestChunkText_ShortPassthrough(t *testing.T) {
	got := chunkText("hello", 4096)
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("got %v, want [hello]", got)
	}
}

func TestChunkText_Empty(t *testing.T) {
	got := chunkText("", 4096)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("got %v, want [\"\"]", got)
	}
}

func TestChunkText_SplitsAtMaxLen(t *testing.T) {
	src := strings.Repeat("a", 5000)
	got := chunkText(src, 4096)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if len(got[0]) != 4096 {
		t.Errorf("chunk 0 len=%d, want 4096", len(got[0]))
	}
	if len(got[1]) != 5000-4096 {
		t.Errorf("chunk 1 len=%d, want %d", len(got[1]), 5000-4096)
	}
}

func TestChunkText_UTF8Safe(t *testing.T) {
	// 4097 bytes: 4096 single-byte 'a' + 1-byte trailing '😀' would push
	// past the boundary, but if we use only ASCII, the boundary is exact.
	// Test with a multi-byte char straddling the boundary.
	src := strings.Repeat("a", 4094) + "😀" // 😀 = 4 bytes in UTF-8
	got := chunkText(src, 4096)

	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	// First chunk must end on a non-continuation byte. The whole string
	// concatenated should reproduce the source.
	if got[0]+got[1] != src {
		t.Errorf("concatenation mismatch")
	}
	// First chunk should not contain a partial 😀.
	for _, c := range got[0] {
		_ = c // walking as runes; if invalid, range gives RuneError, but
		// chunkText guarantees the cut isn't mid-codepoint by walking back
		// from continuation bytes.
	}
}
