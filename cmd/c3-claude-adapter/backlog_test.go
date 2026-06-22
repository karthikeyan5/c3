package main

import (
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

func TestRenderBacklogSummary(t *testing.T) {
	got := renderBacklogSummary(2, []ipc.QueuedItem{
		{MessageID: 5, Sender: "@k", Kind: "text", Preview: "hello"},
		{MessageID: 6, Sender: "@k", Kind: "voice", Preview: ""},
	})
	// Assert per-ITEM content, not just any '2': both items' message_ids, the
	// text preview, and the voice item's "(voice)" fallback must render, plus the
	// fetch_queue hint. This catches a broken item-rendering loop.
	for _, want := range []string{"fetch_queue", "[5]", "hello", "[6]", "(voice)"} {
		if !strings.Contains(got, want) {
			t.Errorf("backlog summary missing %q; got %q", want, got)
		}
	}
}

// count > len(items) must render the "…and N more" truncation line.
func TestRenderBacklogSummary_AndMore(t *testing.T) {
	got := renderBacklogSummary(5, []ipc.QueuedItem{
		{MessageID: 1, Sender: "@k", Kind: "text", Preview: "a"},
		{MessageID: 2, Sender: "@k", Kind: "text", Preview: "b"},
		{MessageID: 3, Sender: "@k", Kind: "text", Preview: "c"},
	})
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("backlog summary = %q, want '…and 2 more' truncation line", got)
	}
}

func TestRenderBacklogSummary_Empty(t *testing.T) {
	if got := renderBacklogSummary(0, nil); got != "" {
		t.Errorf("empty backlog summary = %q, want empty string", got)
	}
}

func TestPendingNudge(t *testing.T) {
	if got := pendingNudge(3); !strings.Contains(got, "3 pending") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("pendingNudge(3) = %q", got)
	}
	if got := pendingNudge(0); got != "" {
		t.Errorf("pendingNudge(0) = %q, want empty", got)
	}
}

func TestDecoratePushContent_CarriesNudgeOnBacklog(t *testing.T) {
	got := decoratePushContent("hello", 2)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "2 pending") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("push content = %q, want body + '2 pending' + fetch_queue nudge", got)
	}
	if got := decoratePushContent("hello", 0); got != "hello" {
		t.Errorf("no-backlog push content = %q, want unchanged 'hello'", got)
	}
}

func TestRenderFetchedMessages_ExposesAttachmentFileID(t *testing.T) {
	got := renderFetchedMessages([]c3types.Inbound{{
		Channel: "telegram", ChatID: -100, MessageID: 7,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VOICE123", MIME: "audio/ogg", Size: 2048, Name: "note.ogg"}},
	}}, 0)
	for _, want := range []string{"VOICE123", "audio/ogg", "message_id=7"} {
		if !strings.Contains(got, want) {
			t.Errorf("fetch_queue render missing %q; got %q", want, got)
		}
	}
}

// renderFetchedMessages with no messages must report the empty-queue line.
func TestRenderFetchedMessages_Empty(t *testing.T) {
	if got := renderFetchedMessages(nil, 0); !strings.Contains(got, "empty") {
		t.Errorf("empty fetch render = %q, want an 'empty' line", got)
	}
}

// A non-zero Remaining must append the pending nudge so the agent keeps draining.
func TestRenderFetchedMessages_RemainingNudge(t *testing.T) {
	got := renderFetchedMessages([]c3types.Inbound{{
		Channel: "telegram", MessageID: 1, Text: "hi",
	}}, 4)
	if !strings.Contains(got, "4 pending") || !strings.Contains(got, "fetch_queue") {
		t.Errorf("fetch render with remaining=4 missing nudge; got %q", got)
	}
}
