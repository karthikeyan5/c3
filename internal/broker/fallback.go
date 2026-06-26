package broker

import (
	"fmt"
	"sync"
	"time"
)

// fallbackTracker enforces the cooldown-fallback dedup rule from spec §4.4.3:
// when an inbound for (channel, chat, *topic) arrives but no stub holds the
// claim, the broker sends a single "no CLI attached" reply and records the
// timestamp. Subsequent inbounds for the same key within `cooldown` are
// silently dropped (no second fallback) until the window passes.
//
// Default cooldown is 300s (5 minutes); spec-configurable per channel via
// mappings.json:channels.<chan>.fallback_cooldown_s.
type fallbackTracker struct {
	mu        sync.Mutex
	lastByKey map[RouteKey]time.Time
	// heldMsgByKey remembers the message_id of the live "held — N queued" reply
	// per route, on channels that can edit messages. BUG #3 fix: instead of
	// suppressing every held-reply after the first behind the cooldown (which
	// froze the visible count while the queue kept growing), the first held
	// inbound SENDS the reply and records its id here; later held inbounds for
	// the same route EDIT that message to the true count. Cleared when the route
	// goes live again — either a live delivery (worker.go:626) OR any session
	// (re)claiming the route via attach/recover (clearHeldReplyOnClaim) — so the
	// next detach starts a fresh held-reply message rather than editing a buried
	// one. The claim-path clear matters for the pull-drain resume flow, where no
	// live delivery ever fires (review finding, 2026-06-25).
	heldMsgByKey map[RouteKey]int64
	cooldown     time.Duration
}

// newFallbackTracker returns a tracker with the given cooldown.
func newFallbackTracker(cooldown time.Duration) *fallbackTracker {
	return &fallbackTracker{
		lastByKey:    map[RouteKey]time.Time{},
		heldMsgByKey: map[RouteKey]int64{},
		cooldown:     cooldown,
	}
}

// HeldMessageID returns the message_id of the live held-reply tracked for key,
// and whether one is currently tracked.
func (f *fallbackTracker) HeldMessageID(key RouteKey) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.heldMsgByKey[key]
	return id, ok
}

// SetHeldMessageID records the message_id of the held-reply just sent for key,
// so subsequent held inbounds edit it in place instead of re-sending.
func (f *fallbackTracker) SetHeldMessageID(key RouteKey, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heldMsgByKey[key] = id
}

// ClearHeldMessageID forgets the tracked held-reply for key (the route went
// live again, or the tracked message became uneditable). The next held inbound
// will SEND a fresh held-reply rather than editing a stale/buried one.
func (f *fallbackTracker) ClearHeldMessageID(key RouteKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.heldMsgByKey, key)
}

// ShouldSend returns true and updates the timestamp if cooldown has elapsed
// since the last fallback for key. Returns false otherwise (caller should
// silently drop).
func (f *fallbackTracker) ShouldSend(key RouteKey) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	last := f.lastByKey[key]
	if time.Since(last) < f.cooldown {
		return false
	}
	f.lastByKey[key] = time.Now()
	return true
}

const defaultFallbackCooldown = 300 * time.Second

// fallbackText is the boilerplate reply sent on a no-claim inbound.
const fallbackText = "No CLI is currently attached to this topic. Run `c3-broker status` to see attached terminals, or open a CLI in the project directory and `attach`."

// heldReplyText is the "held, nothing lost" auto-reply sent when an inbound is
// queued because no session is attached. It reassures and carries the running
// count of queued messages. Cadence is the existing 5-min fallback cooldown.
func heldReplyText(n int) string {
	plural := "messages"
	if n == 1 {
		plural = "message"
	}
	return fmt.Sprintf("📨 Held — nothing lost.\n%d %s queued.\n\n\nSend /status to check.", n, plural)
}
