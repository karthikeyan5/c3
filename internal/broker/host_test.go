package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestBrokerHost_ConfigUnmarshal(t *testing.T) {
	mf := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {BotToken: "tok", DefaultGroup: "main"},
		},
	}
	b := New(mf)
	defer b.Shutdown()

	host := NewBrokerHost(b, "telegram")
	type tgCfg struct {
		BotToken     string `json:"bot_token"`
		DefaultGroup string `json:"default_group"`
	}
	var cfg tgCfg
	if err := host.Config("telegram", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "tok" || cfg.DefaultGroup != "main" {
		t.Errorf("got %+v, want BotToken=tok DefaultGroup=main", cfg)
	}
}

func TestBrokerHost_ConfigMissingChannel(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	host := NewBrokerHost(b, "telegram")
	var cfg struct{}
	err := host.Config("telegram", &cfg)
	if err == nil || !strings.Contains(err.Error(), "mappings.json") {
		t.Errorf("expected missing-channel error mentioning mappings.json, got %v", err)
	}
}

func TestBrokerHost_EmitSubmitsToWorker(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	host := NewBrokerHost(b, "telegram")
	id := int64(281)
	host.Emit(&c3types.Inbound{Channel: "telegram", ChatID: -100, TopicID: &id})

	if b.Workers.Active() == 0 {
		t.Error("expected a worker active after Emit")
	}
}

func TestSystemEventForHealth_DesktopUnavailableNote(t *testing.T) {
	ev := c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateDown, Since: time.Now(), Consec: 3, Reason: "dial failures"}
	withNote := systemEventForHealth(ev, true)
	if !strings.Contains(withNote.Message, "desktop notification unavailable") {
		t.Errorf("desktopUnavailable=true message missing note: %q", withNote.Message)
	}
	noNote := systemEventForHealth(ev, false)
	if strings.Contains(noNote.Message, "desktop notification unavailable") {
		t.Errorf("desktopUnavailable=false message should not have note: %q", noNote.Message)
	}
	if noNote.Level != "warn" {
		t.Errorf("down event level = %q, want warn", noNote.Level)
	}
}

func TestSystemEventForHealth_RecoveryHasNoNote(t *testing.T) {
	ev := c3types.HealthEvent{Channel: "telegram", State: c3types.HealthStateUp, DownFor: 5 * time.Minute}
	up := systemEventForHealth(ev, true) // even with true, a recovery message carries no down note
	if strings.Contains(up.Message, "desktop notification unavailable") {
		t.Errorf("recovery message should never have the desktop note: %q", up.Message)
	}
	if up.Level != "info" {
		t.Errorf("recovery level = %q, want info", up.Level)
	}
}
