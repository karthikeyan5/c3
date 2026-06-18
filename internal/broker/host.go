package broker

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/ipc"
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

// Compile-time check: BrokerHost implements channel.Host.
var _ channel.Host = (*BrokerHost)(nil)

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

// GateInbound runs the inbound through the broker's allowlist + pairing
// gate. The channel layer calls this before Emit; only GateInboundAllow
// proceeds downstream. See internal/broker/pairing.go.
func (h *BrokerHost) GateInbound(in *c3types.Inbound) channel.GateInboundDecision {
	switch h.broker.Gate(in) {
	case GateAllow:
		return channel.GateInboundAllow
	case GatePairConsumed:
		return channel.GateInboundPairConsumed
	default:
		return channel.GateInboundDrop
	}
}

func (h *BrokerHost) NotifyHealth(ev c3types.HealthEvent) {
	// --- Ambient tier: always on, synchronous, never gated. ---
	// (c) status cache for `c3-broker status`.
	h.broker.setLastHealth(ev)

	// (d) broker log — one loud edge line.
	if ev.State == c3types.HealthStateDown {
		log.Printf("HEALTH chan=%s state=DOWN since=%s consec=%d reason=%q — inbound offline; desktop primary, CLI fallback, status line",
			ev.Channel, ev.Since.Format("15:04:05"), ev.Consec, ev.Reason)
	} else {
		log.Printf("HEALTH chan=%s state=UP (recovered, was down %s) — inbound restored",
			ev.Channel, ev.DownFor.Round(time.Second))
	}

	// (e) status file the Claude Code status line reads.
	h.broker.WriteHealthFile()

	// --- Invasive tier: desktop popup primary, CLI broadcast fallback. ---
	// Gated by notifications.invasive (default true). Read the toggle ONCE
	// here (a single atomic snapshot load) — never inside the goroutine, so a
	// concurrent SIGHUP SetMappings can't tear the read.
	if !h.broker.Mappings().InvasiveNotifications() {
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("health-notify: invasive sink panic recovered: %v", r)
			}
		}()
		// Desktop popup is the primary surface.
		delivered := false
		if h.broker.desktopNotifier != nil {
			delivered = h.broker.desktopNotifier.Notify(ev)
		}
		// CLI turn-injection is the FALLBACK: only when the popup did not
		// deliver, and only on a DOWN edge. Recovery never injects into the
		// CLI — the status line clearing is the closure.
		if !delivered && ev.State == c3types.HealthStateDown {
			h.broker.broadcastSystemEvent(systemEventForHealth(ev, true))
		}
	}()
}

// systemEventForHealth renders a channel-neutral SystemEvent advisory for a
// health edge. The message is operational (no user content); it tells the agent
// whether phone messages will arrive. Level is "warn" for DOWN, "info" for UP.
func systemEventForHealth(ev c3types.HealthEvent, desktopUnavailable bool) *c3types.SystemEvent {
	ch := ev.Channel
	if ch == "" {
		ch = "channel"
	}
	if ev.State == c3types.HealthStateDown {
		msg := fmt.Sprintf("Cannot reach %s since %s (%d consecutive %s). Your phone messages won't arrive until this recovers.",
			ch, ev.Since.Format("15:04"), ev.Consec, strings.TrimSpace(ev.Reason))
		if desktopUnavailable {
			msg += " (desktop notification unavailable — shown here instead)"
		}
		return &c3types.SystemEvent{
			Source:  ev.Channel,
			Level:   "warn",
			Title:   fmt.Sprintf("%s fetch DOWN", ch),
			Message: msg,
		}
	}
	return &c3types.SystemEvent{
		Source:  ev.Channel,
		Level:   "info",
		Title:   fmt.Sprintf("%s fetch RECOVERED", ch),
		Message: fmt.Sprintf("%s is reachable again (was down %s). Phone messages will arrive normally now.", ch, ev.DownFor.Round(time.Second)),
	}
}

// broadcastSystemEvent writes a broker-originated system InboundEvent to EVERY
// live CLI session. Whichever CLI the user is looking at sees the advisory.
//
// SECURITY BOUNDARY (explicit): this BYPASSES the inbound allowlist gate
// (host.GateInbound / broker.Gate) and the per-route worker pool / debounce.
// That bypass is sound ONLY because the event is BROKER-ORIGINATED and TRUSTED
// — it carries no user content and is not user input, so the default-deny
// allowlist (which exists to keep STRANGERS' messages out) does not apply. This
// path must NEVER be used to deliver anything user-sourced; user inbound ALWAYS
// goes through host.Emit → GateInbound. The only producer of these events is
// NotifyHealth, deep inside the broker.
//
// Delivery is a direct write to each alive stub's conn (mirrors the worker's
// forwardOrFallback write), best-effort per conn: a failed write to one session
// is logged and skipped, never fatal.
func (b *Broker) broadcastSystemEvent(sysev *c3types.SystemEvent) {
	if sysev == nil {
		return
	}
	in := c3types.Inbound{
		Channel: sysev.Source,
		Kind:    c3types.InboundSystem,
		Event:   &c3types.InboundEvent{System: sysev},
		// No ChatID/Sender/Text — this is broker-originated, not a routed
		// user message.
	}
	delivered := 0
	for _, s := range b.Stubs.Snapshot() {
		// Skip the transient CLI clients (status/topics/etc.) — they're not
		// long-lived agent sessions and close immediately.
		if s.CLI == "c3-broker-cli" {
			continue
		}
		conn, ok := s.ConnValue().(*ipc.Conn)
		if !ok || conn == nil {
			continue
		}
		if err := conn.WriteJSON(ipc.InboundMsg{Op: ipc.OpInbound, Inbound: in}); err != nil {
			log.Printf("health-broadcast: write to cli=%s pid=%d conn=%d failed: %v",
				s.CLI, s.PID, s.ConnID, err)
			continue
		}
		delivered++
	}
	log.Printf("health-broadcast: system advisory %q delivered to %d live CLI session(s)",
		sysev.Title, delivered)
}

// channelRegistration entries inside the broker.
type channelRegistration struct {
	Channel channel.Channel
	Host    *BrokerHost
}
