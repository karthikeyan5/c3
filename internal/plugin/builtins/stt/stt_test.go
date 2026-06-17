package stt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/plugin"
)

// fakeHost is a minimal plugin.Host capturing the OnVoiceReceived callback
// and routing Config / ChannelConfig from in-memory maps. Enough surface for
// the stt plugin's startup + per-call paths.
type fakeHost struct {
	cfg           Config
	channelCfg    map[string]any
	voiceCallback func(ctx context.Context, p c3types.VoicePayload) (string, error)
	logs          []string
}

func (h *fakeHost) OnInbound(fn func(context.Context, *c3types.Inbound) (*c3types.Inbound, bool)) {
}
func (h *fakeHost) OnVoiceReceived(fn func(context.Context, c3types.VoicePayload) (string, error)) {
	h.voiceCallback = fn
}
func (h *fakeHost) OnOutbound(fn func(context.Context, *c3types.Outbound) (*c3types.Outbound, bool)) {
}
func (h *fakeHost) OnAttach(fn func(*plugin.Stub, *plugin.Mapping)) {}
func (h *fakeHost) RegisterTools(fn func(*plugin.ToolRegistry))     {}

func (h *fakeHost) Config(name string, target any) error {
	if name != Name {
		return nil
	}
	t, ok := target.(*Config)
	if !ok {
		return nil
	}
	*t = h.cfg
	return nil
}

func (h *fakeHost) ChannelConfig(name string, target any) error {
	cc, ok := h.channelCfg[name]
	if !ok {
		return nil
	}
	// Mirror the real host: JSON round-trip into whatever struct the caller
	// passes, so adding fields to the read struct (e.g. api_base_url) doesn't
	// break this mock the way an exact type-assertion did.
	b, err := json.Marshal(cc)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func (h *fakeHost) State(name string) plugin.StateDir          { return nil }
func (h *fakeHost) CacheDir(name string) string                { return "" }
func (h *fakeHost) Channel(name string) (channel.Channel, error) { return nil, nil }
func (h *fakeHost) Logf(format string, args ...any) {
	h.logs = append(h.logs, format)
}
func (h *fakeHost) Done() <-chan struct{} { return make(chan struct{}) }

func TestRegister_HandlerMissingAtStartup_StillRegistersCallback(t *testing.T) {
	h := &fakeHost{
		cfg: Config{
			Enabled:     true,
			HandlerPath: "/nonexistent/path/stt-handler.py",
			Timeout:     30,
		},
		channelCfg: map[string]any{
			"telegram": map[string]string{"bot_token": "tok"},
		},
	}
	if err := Register(h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if h.voiceCallback == nil {
		t.Fatal("expected OnVoiceReceived to be registered even when handler is missing — bug repro: 2026-05-14, broken symlink silently disabled STT")
	}

	transcript, err := h.voiceCallback(context.Background(), c3types.VoicePayload{MessageID: 1})
	if err != nil {
		t.Fatalf("callback error: %v", err)
	}
	if !strings.Contains(transcript, "[STT FAILED:") || !strings.Contains(transcript, "handler_missing") {
		t.Errorf("missing-handler transcript = %q, want marker containing handler_missing", transcript)
	}
}

func TestRegister_HandlerAppearsAfterStartup_NextCallTranscribes(t *testing.T) {
	tmp := t.TempDir()
	handler := filepath.Join(tmp, "stt-handler.py")

	h := &fakeHost{
		cfg: Config{
			Enabled:     true,
			HandlerPath: handler,
			Timeout:     5,
		},
		channelCfg: map[string]any{
			"telegram": map[string]string{"bot_token": "tok"},
		},
	}
	if err := Register(h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// First call: handler missing → marker.
	t1, _ := h.voiceCallback(context.Background(), c3types.VoicePayload{MessageID: 1})
	if !strings.Contains(t1, "handler_missing") {
		t.Errorf("first call before handler appears: %q, want handler_missing marker", t1)
	}

	// Restore the handler — a script that just prints a fixed transcript.
	const script = "#!/usr/bin/env python3\nprint('recovered transcript')\n"
	if err := os.WriteFile(handler, []byte(script), 0o755); err != nil {
		t.Fatalf("write handler: %v", err)
	}

	// Second call should now run the handler — no broker restart needed.
	t2, _ := h.voiceCallback(context.Background(), c3types.VoicePayload{MessageID: 2})
	if !strings.Contains(t2, "recovered transcript") {
		t.Errorf("after handler restored: %q, want 'recovered transcript' — graceful recovery without restart", t2)
	}
}

func TestRegister_DisabledInConfig_DoesNotRegister(t *testing.T) {
	h := &fakeHost{
		cfg: Config{Enabled: false},
	}
	if err := Register(h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if h.voiceCallback != nil {
		t.Error("disabled plugin should not register OnVoiceReceived")
	}
}
