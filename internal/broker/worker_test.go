package broker

import (
	"context"
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
