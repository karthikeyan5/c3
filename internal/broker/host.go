package broker

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// BrokerHost is the broker's concrete implementation of channel.Host.
// One Host is created per channel registration.
//
// Plan 4A scope: scaffold for Plan 4B (Telegram). Plan 5 adds plugin.Host
// concrete impl with hook chain + plugin tools.
type BrokerHost struct {
	broker  *Broker
	channel string // channel name (e.g. "telegram")
}

// NewBrokerHost binds a Host to a (broker, channel-name) pair.
func NewBrokerHost(b *Broker, chanName string) *BrokerHost {
	return &BrokerHost{broker: b, channel: chanName}
}

// Config marshals mappings.json:channels.<name> via JSON-roundtrip into target.
// Returns error if the channel section is missing.
func (h *BrokerHost) Config(name string, target any) error {
	if h.broker.Mappings() == nil || h.broker.Mappings().Channels == nil {
		return fmt.Errorf("broker host: no channels in mappings.json")
	}
	cc, ok := h.broker.Mappings().Channels[name]
	if !ok {
		return fmt.Errorf("broker host: channel %q not in mappings.json", name)
	}
	data, err := json.Marshal(cc)
	if err != nil {
		return fmt.Errorf("broker host: marshal channel config: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("broker host: unmarshal channel config: %w", err)
	}
	return nil
}

// Emit submits an inbound to the per-route worker pool. The worker drains
// the pipeline (STT, OnInbound chain, debounce, forward to claimed stub).
// Phase 4A: dispatch is stubbed; Plan 4B+5 wire the real pipeline.
func (h *BrokerHost) Emit(in *c3types.Inbound) {
	if in == nil {
		return
	}
	key := MakeRouteKey(in.Channel, in.ChatID, in.TopicID)
	if !h.broker.Workers.Submit(key, Job{Kind: JobInbound, Inbound: in}) {
		log.Printf("emit DROP chan=%s chat=%d topic=%s msg=%d: worker queue full or stopped",
			in.Channel, in.ChatID, TopicPtrStr(in.TopicID), in.MessageID)
	}
}

// Logf writes to the broker's structured log (currently stdlib log).
func (h *BrokerHost) Logf(format string, args ...any) {
	log.Printf(format, args...)
}

// Done returns the broker's shutdown channel.
func (h *BrokerHost) Done() <-chan struct{} {
	return h.broker.ctx.Done()
}

// channelRegistration entries inside the broker.
type channelRegistration struct {
	Channel channel.Channel
	Host    *BrokerHost
}
