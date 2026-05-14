package broker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestWorker_IdleShutdown(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, 50*time.Millisecond, nil)
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on idle within 500ms")
	}
}

func TestWorker_StopExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	go w.Stop()
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on Stop")
	}
}

func TestWorker_ReleaseJobExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	if !w.Submit(Job{Kind: JobRelease}) {
		t.Fatal("Submit should succeed before stop")
	}
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on JobRelease")
	}
}

func TestWorker_SubmitAfterStopReturnsFalse(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	w.Stop()
	if w.Submit(Job{Kind: JobInbound}) {
		t.Error("Submit after Stop should return false")
	}
}

// Regression test for 2026-05-14: when the STT handler script went missing,
// the plugin silently disabled itself at startup and voice messages reached
// the agent as a bare "(voice message)" with no indication anything was
// wrong. Broker-side defense-in-depth: if a voice attachment arrives and no
// plugin produced a transcript AND no caption exists, surface a marker.
func TestFlushInbounds_VoiceWithoutSTTPluginGetsMarker(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   42,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1", Size: 1000}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if !strings.Contains(in.Text, "[STT FAILED:") || !strings.Contains(in.Text, "no_transcript_plugin") {
		t.Errorf("voice with no STT plugin: in.Text=%q, want marker with no_transcript_plugin reason", in.Text)
	}
}

func TestFlushInbounds_VoiceWithCaptionKeepsCaptionWhenSTTAbsent(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   43,
		Text:        "user-typed caption",
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if in.Text != "user-typed caption" {
		t.Errorf("voice with caption but no STT plugin: in.Text=%q, want caption preserved (don't clobber user-deliberate text with marker)", in.Text)
	}
}

func TestFlushInbounds_VoiceWithSTTPluginUsesTranscript(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	b.Plugins.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		return "transcribed text", nil
	})

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   44,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if !strings.Contains(in.Text, "transcribed text") {
		t.Errorf("voice with STT plugin: in.Text=%q, want transcript embedded", in.Text)
	}
}

// Regression test for 2026-05-14: after /mcp reconnect, Claude Code killed
// the old adapter; the broker kept the now-stale claim "while pid alive"
// and every inbound failed with `holder.Conn is not *ipc.Conn` because the
// MarkDisconnected'd stub had nil Conn. Worse, no Telegram fallback fired
// either (the stale claim made `claimed=true`). Fix: liveness check at
// dispatch time, release-and-fall-through when holder PID is dead.
func TestForwardOrFallback_StaleClaim_ReleasesAndFallsThrough(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1003990699908, &tid)

	// Stub with a PID that's definitively dead. PIDs above PID_MAX
	// (typically 4194304 on Linux, 99999 on macOS) reliably return
	// ESRCH from kill(2). 1<<30 is safely beyond both. MarkDisconnected
	// mirrors the post-conn-drop state.
	deadStub := &Stub{
		CLI: "claude", PID: 1 << 30, CWD: "/home/u/proj", ConnID: 99,
	}
	deadStub.MarkDisconnected()
	b.Routes.Claim(key, deadStub)
	if _, held := b.Routes.Holder(key); !held {
		t.Fatal("test setup: claim should be in place before delivery")
	}

	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:   "telegram",
		ChatID:    -1003990699908,
		TopicID:   &tid,
		MessageID: 1133,
		Text:      "Hi",
	}
	w.forwardOrFallback(context.Background(), in)

	// Stale claim must be released.
	if _, held := b.Routes.Holder(key); held {
		t.Error("stale dead-holder claim should have been released on dispatch")
	}
	// Fallback should have fired since claim was cleared.
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Errorf("expected fallback SendReply after releasing stale claim, got %d sends", got)
	}
}

// Companion: a holder that IS alive but currently has nil Conn (between
// reconnects) must NOT be released; deliver-skip is the correct behavior,
// because the adapter will be back on the wire shortly. Otherwise a
// momentary network blip would race against the inbound flow.
func TestForwardOrFallback_AliveButDisconnectedHolder_SkipsDelivery(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1003990699908, &tid)

	// Use this test process's PID — guaranteed alive.
	aliveStub := &Stub{
		CLI: "claude", PID: os.Getpid(), CWD: "/home/u/proj", ConnID: 99,
	}
	aliveStub.MarkDisconnected() // alive but disconnected
	b.Routes.Claim(key, aliveStub)

	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -1003990699908, TopicID: &tid,
		MessageID: 1134, Text: "Hi",
	}
	w.forwardOrFallback(context.Background(), in)

	if _, held := b.Routes.Holder(key); !held {
		t.Error("alive-but-disconnected claim must be preserved (adapter may reconnect)")
	}
	if got := len(fc.sendRepliesSnapshot()); got != 0 {
		t.Errorf("expected no fallback for alive disconnected holder, got %d sends", got)
	}
}

func TestWorker_OutboundStubReturnsErr(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	defer w.Stop()

	resultCh := make(chan OutboundResult, 1)
	if !w.Submit(Job{Kind: JobOutbound, Outbound: &OutboundJob{Tool: "reply", ResultCh: resultCh}}) {
		t.Fatal("Submit failed")
	}
	select {
	case r := <-resultCh:
		if r.Err == nil {
			t.Error("expected stub error in Phase 4A")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no result within 500ms")
	}
}
