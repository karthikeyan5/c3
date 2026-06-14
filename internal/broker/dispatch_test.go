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

// typingRecorderChannel records SendTyping (chatID, threadID) calls so tests can
// assert the legacy send_typing dispatch op and the validate_topic piggyback
// (which routes ValidateTopic → SendTyping) still drive the channel after P5
// removed the agent-facing send_typing tool.
type typingRecorderChannel struct {
	mu     sync.Mutex
	typing []typingCall
}

type typingCall struct {
	chatID   int64
	threadID *int64
}

func (c *typingRecorderChannel) Name() string                              { return "telegram" }
func (c *typingRecorderChannel) Start(context.Context, channel.Host) error { return nil }
func (c *typingRecorderChannel) Stop() error                               { return nil }
func (c *typingRecorderChannel) Capabilities() c3types.Capabilities {
	return c3types.Capabilities{Channel: "telegram", Typing: true}
}
func (c *typingRecorderChannel) SendReply(c3types.ReplyArgs) (int64, error) { return 0, nil }
func (c *typingRecorderChannel) SendTyping(chatID int64, threadID *int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.typing = append(c.typing, typingCall{chatID: chatID, threadID: threadID})
	return nil
}
func (c *typingRecorderChannel) EditMessage(c3types.EditArgs) (*c3types.EditResult, error) {
	return &c3types.EditResult{}, nil
}
func (c *typingRecorderChannel) React(c3types.ReactArgs) error             { return nil }
func (c *typingRecorderChannel) DownloadAttachment(string) (string, error) { return "", nil }
func (c *typingRecorderChannel) CreateTopic(int64, string) (int64, error)  { return 0, nil }

// ValidateTopic piggybacks SendTyping — the same path Telegram uses to confirm
// a thread exists (channel/telegram/outbound.go:ValidateTopic).
func (c *typingRecorderChannel) ValidateTopic(chatID, threadID int64) error {
	return c.SendTyping(chatID, &threadID)
}

func (c *typingRecorderChannel) typingSnapshot() []typingCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]typingCall, len(c.typing))
	copy(out, c.typing)
	return out
}

// TestDispatchTool_LegacySendTypingStillHandled asserts the broker dispatch keeps
// HANDLING a send_typing op (legacy in-flight callers + the internal relay) even
// though P5 removed send_typing from the agent-facing tool set. The op must route
// to the channel's SendTyping with the route's chat/topic.
func TestDispatchTool_LegacySendTypingStillHandled(t *testing.T) {
	ch := &typingRecorderChannel{}
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}

	res, err := dispatchTool(ch, key, "send_typing", map[string]any{})
	if err != nil {
		t.Fatalf("send_typing op should still be handled by dispatch; got err %v", err)
	}
	if res == nil {
		t.Fatal("send_typing op should return a result")
	}
	got := ch.typingSnapshot()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 SendTyping call from the legacy op; got %d", len(got))
	}
	if got[0].chatID != -100 || got[0].threadID == nil || *got[0].threadID != tid {
		t.Errorf("send_typing should target the route's chat/topic; got %+v", got[0])
	}
}

// TestValidateTopic_PiggybacksSendTyping asserts the validate_topic primitive
// still works after P5: ValidateTopic routes through SendTyping (the typing
// action with a thread_id implicitly validates the thread). P5 must not have
// disturbed the channel's SendTyping method that this relies on.
func TestValidateTopic_PiggybacksSendTyping(t *testing.T) {
	ch := &typingRecorderChannel{}
	if err := ch.ValidateTopic(-100, 412); err != nil {
		t.Fatalf("ValidateTopic should succeed; got %v", err)
	}
	got := ch.typingSnapshot()
	if len(got) != 1 {
		t.Fatalf("ValidateTopic should fire exactly 1 SendTyping; got %d", len(got))
	}
	if got[0].chatID != -100 || got[0].threadID == nil || *got[0].threadID != 412 {
		t.Errorf("ValidateTopic should SendTyping for the validated thread; got %+v", got[0])
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
