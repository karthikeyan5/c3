package broker

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// pollChannel is a test channel whose Capabilities advertise poll support and
// which records the parts it is asked to send. Used to assert dispatchPoll
// routes a poll through the gate and onto a SendReply part.
type pollChannel struct {
	mu    sync.Mutex
	polls bool
	sent  []c3types.ReplyArgs
}

func (p *pollChannel) Name() string                              { return "telegram" }
func (p *pollChannel) Start(context.Context, channel.Host) error { return nil }
func (p *pollChannel) Stop() error                               { return nil }
func (p *pollChannel) Capabilities() c3types.Capabilities {
	return c3types.Capabilities{Channel: "telegram", Polls: p.polls}
}
func (p *pollChannel) SendReply(args c3types.ReplyArgs) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, args)
	return int64(len(p.sent)), nil
}
func (p *pollChannel) SendTyping(int64, *int64) error { return nil }
func (p *pollChannel) EditMessage(c3types.EditArgs) (*c3types.EditResult, error) {
	return &c3types.EditResult{}, nil
}
func (p *pollChannel) React(c3types.ReactArgs) error             { return nil }
func (p *pollChannel) DownloadAttachment(string) (string, error) { return "", nil }
func (p *pollChannel) CreateTopic(int64, string) (int64, error)  { return 0, nil }
func (p *pollChannel) ValidateTopic(int64, int64) error          { return nil }

func (p *pollChannel) sentSnapshot() []c3types.ReplyArgs {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]c3types.ReplyArgs, len(p.sent))
	copy(out, p.sent)
	return out
}

func pollArgs() map[string]any {
	return map[string]any{
		"question": "Lunch?",
		"options":  []any{"Pizza", "Tacos"},
	}
}

// TestDispatchPoll_RejectedWhenUnsupported asserts the gate hard-rejects a poll
// on a channel that does not support polls, and NOTHING is sent.
func TestDispatchPoll_RejectedWhenUnsupported(t *testing.T) {
	ch := &pollChannel{polls: false}
	key := RouteKey{Channel: "telegram", ChatID: -100}
	res, err := dispatchPoll(ch, key, pollArgs())
	if err == nil {
		t.Fatalf("expected a hard-reject error when polls unsupported; got result %v", res)
	}
	if !strings.Contains(err.Error(), "poll") {
		t.Errorf("error should mention polls; got %v", err)
	}
	if got := ch.sentSnapshot(); len(got) != 0 {
		t.Errorf("nothing should be sent on a rejected poll; got %d sends", len(got))
	}
}

// TestDispatchPoll_SendsPollPart asserts a poll is routed onto a SendReply part
// carrying the PollSpec when the channel supports polls.
func TestDispatchPoll_SendsPollPart(t *testing.T) {
	ch := &pollChannel{polls: true}
	key := RouteKey{Channel: "telegram", ChatID: -100}
	args := pollArgs()
	args["multiple"] = true
	args["anonymous"] = false
	if _, err := dispatchPoll(ch, key, args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sent := ch.sentSnapshot()
	if len(sent) != 1 {
		t.Fatalf("expected exactly 1 part sent (the poll); got %d", len(sent))
	}
	part := sent[0]
	if part.Poll == nil {
		t.Fatalf("the sent part should carry the poll; got %+v", part)
	}
	if part.Poll.Question != "Lunch?" || len(part.Poll.Options) != 2 {
		t.Errorf("poll spec mismatch: %+v", part.Poll)
	}
	if !part.Poll.MultipleAnswers || part.Poll.Anonymous {
		t.Errorf("poll flags not honored: %+v", part.Poll)
	}
	if part.Text != "" || len(part.Media) != 0 {
		t.Errorf("poll part should carry only the poll; got %+v", part)
	}
}

// TestDispatchPoll_RequiresTwoOptions asserts dispatchPoll rejects a poll with
// fewer than 2 options before reaching the channel.
func TestDispatchPoll_RequiresTwoOptions(t *testing.T) {
	ch := &pollChannel{polls: true}
	key := RouteKey{Channel: "telegram", ChatID: -100}
	_, err := dispatchPoll(ch, key, map[string]any{
		"question": "One?",
		"options":  []any{"only"},
	})
	if err == nil {
		t.Fatalf("expected an error for a 1-option poll")
	}
	if got := ch.sentSnapshot(); len(got) != 0 {
		t.Errorf("nothing should be sent for an invalid poll; got %d", len(got))
	}
}

// TestDispatchReply_MediaArgSplitsIntoParts asserts the `media` array arg is
// parsed and the gate splits it into one SendReply part per item, after the
// text part.
func TestDispatchReply_MediaArgSplitsIntoParts(t *testing.T) {
	ch := &mediaReplyChannel{}
	key := RouteKey{Channel: "telegram", ChatID: -100}
	args := map[string]any{
		"text": "see attached",
		"media": []any{
			map[string]any{"kind": "file", "path": "/tmp/a.pdf", "caption": "report"},
			map[string]any{"kind": "photo", "url": "https://example.com/x.jpg", "spoiler": true},
		},
	}
	if _, err := dispatchReply(ch, key, args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sent := ch.sentSnapshot()
	// 1 text part + 2 media parts.
	if len(sent) != 3 {
		t.Fatalf("expected 3 parts (text + 2 media); got %d", len(sent))
	}
	if sent[0].Text != "see attached" || len(sent[0].Media) != 0 {
		t.Errorf("part 0 should be the text part; got %+v", sent[0])
	}
	if len(sent[1].Media) != 1 || sent[1].Media[0].Kind != c3types.MediaFile ||
		sent[1].Media[0].Path != "/tmp/a.pdf" || sent[1].Media[0].Caption != "report" {
		t.Errorf("part 1 should carry the file item; got %+v", sent[1].Media)
	}
	if len(sent[2].Media) != 1 || sent[2].Media[0].Kind != c3types.MediaPhoto ||
		sent[2].Media[0].URL != "https://example.com/x.jpg" || !sent[2].Media[0].Spoiler {
		t.Errorf("part 2 should carry the photo URL item with spoiler; got %+v", sent[2].Media)
	}
}

// mediaReplyChannel is a test channel with the full media-kind manifest that
// records the parts it is asked to send.
type mediaReplyChannel struct {
	mu   sync.Mutex
	sent []c3types.ReplyArgs
}

func (m *mediaReplyChannel) Name() string                              { return "telegram" }
func (m *mediaReplyChannel) Start(context.Context, channel.Host) error { return nil }
func (m *mediaReplyChannel) Stop() error                               { return nil }
func (m *mediaReplyChannel) Capabilities() c3types.Capabilities {
	return c3types.Capabilities{
		Channel:  "telegram",
		RichText: true,
		MediaKinds: []c3types.MediaKind{
			c3types.MediaPhoto, c3types.MediaFile, c3types.MediaVideo,
			c3types.MediaAudio, c3types.MediaVoice, c3types.MediaAnimation,
		},
		CompressedPhoto: true,
		OriginalFile:    true,
	}
}
func (m *mediaReplyChannel) SendReply(args c3types.ReplyArgs) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, args)
	return int64(len(m.sent)), nil
}
func (m *mediaReplyChannel) SendTyping(int64, *int64) error { return nil }
func (m *mediaReplyChannel) EditMessage(c3types.EditArgs) (*c3types.EditResult, error) {
	return &c3types.EditResult{}, nil
}
func (m *mediaReplyChannel) React(c3types.ReactArgs) error             { return nil }
func (m *mediaReplyChannel) DownloadAttachment(string) (string, error) { return "", nil }
func (m *mediaReplyChannel) CreateTopic(int64, string) (int64, error)  { return 0, nil }
func (m *mediaReplyChannel) ValidateTopic(int64, int64) error          { return nil }

func (m *mediaReplyChannel) sentSnapshot() []c3types.ReplyArgs {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]c3types.ReplyArgs, len(m.sent))
	copy(out, m.sent)
	return out
}
