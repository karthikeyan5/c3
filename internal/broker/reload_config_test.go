package broker

import (
	"testing"

	"github.com/karthikeyan5/c3/internal/mappings"
)

// TestBroker_SetMappings_AtomicSwap asserts the SIGHUP-driven config reload
// path: SetMappings replaces the pointer, and subsequent reads through
// Broker.Mappings see the new file. The promise is "no torn reads" — a
// goroutine that loads Broker.Mappings between SetMappings calls will see
// either the old pointer or the new pointer, never a half-updated struct.
func TestBroker_SetMappings_AtomicSwap(t *testing.T) {
	old := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {BotToken: "old-token", DefaultGroup: "main"},
		},
	}
	b := New(old)
	defer b.Shutdown()

	if got := b.Mappings.Channels["telegram"].BotToken; got != "old-token" {
		t.Fatalf("initial token: got %q, want old-token", got)
	}

	fresh := &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {BotToken: "new-token", DefaultGroup: "main"},
		},
		Mappings: map[string]mappings.Mapping{
			"/home/u/proj": {Channel: "telegram", ChatID: -100, TopicID: 914, Name: "proj"},
		},
	}
	b.SetMappings(fresh)

	if got := b.Mappings.Channels["telegram"].BotToken; got != "new-token" {
		t.Errorf("after reload: token %q, want new-token", got)
	}
	if _, ok := b.Mappings.LookupByCwd("/home/u/proj"); !ok {
		t.Error("after reload: new mapping not visible via LookupByCwd")
	}
}
