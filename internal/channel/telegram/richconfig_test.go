package telegram

import "testing"

func TestRichInboundEnabled(t *testing.T) {
	if !(Config{}).RichInboundEnabled() {
		t.Error("nil RichInbound should default to true (enabled)")
	}
	yes := true
	if !(Config{RichInbound: &yes}).RichInboundEnabled() {
		t.Error("RichInbound=true should be enabled")
	}
	no := false
	if (Config{RichInbound: &no}).RichInboundEnabled() {
		t.Error("RichInbound=false should be disabled")
	}
}
