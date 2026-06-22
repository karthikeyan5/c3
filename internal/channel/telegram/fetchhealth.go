package telegram

import (
	"sync"
	"time"
)

// fetchHealth is the single source of truth for "can we reach Telegram to fetch
// inbound?". It replaces the two prior competing false-positive watchdogs
// (stallWatchdog + heartbeat's HEARTBEAT-FAILED alarm) with one state machine
// and one notification edge.
//
// THE FALSE-POSITIVE FIX (the whole reason this exists): a successful GetUpdates
// that returns ZERO updates is HEALTHY — that's a normal long-poll timeout on a
// quiet night. It MUST drive RecordSuccess(), not a failure. DOWN is driven
// ONLY by classified transport failures (transient network/timeout/5xx, 409
// conflict, a tripped permanent/token-revoked breaker). 429 (rate-limited) is
// the server actively pushing back — proof it is reachable — so it is NEVER
// treated as down.
//
// Transitions:
//   - UP → DOWN when consecFails >= downAfterFails (default 3) OR
//     time.Since(lastSuccess) > maxSilence (default ~90s). The max-silence arm
//     folds in the old stallWatchdog's job: a hang that neither succeeds nor
//     errors fast still flips DOWN.
//   - DOWN → UP on the FIRST RecordSuccess (recovery is good news; no debounce).
//
// De-spam: the transition enum is returned from RecordSuccess/RecordFailure so
// the caller fires the notify callback EXACTLY ONCE per edge — never while
// already DOWN (further failures only bump counters) and never on a repeated
// success while already UP. Two loud lines per outage cycle: DOWN, RECOVERED.
type fetchHealth struct {
	mu          sync.Mutex
	down        bool
	consecFails int
	lastSuccess time.Time
	since       time.Time // when the current state was entered
	lastReason  string    // cause of the most recent failure (for the DOWN edge)
	downAfter   int
	maxSilence  time.Duration
	now         func() time.Time // injectable clock for tests
}

// healthTransition is the edge (if any) a Record* call produced. The caller
// fires the out-of-band notify callback only when this is not healthNoChange.
type healthTransition int

const (
	healthNoChange  healthTransition = iota
	healthWentDown                   // UP → DOWN
	healthRecovered                  // DOWN → UP
)

const (
	// defaultDownAfterFails is the consecutive-transport-failure count that
	// flips UP → DOWN.
	defaultDownAfterFails = 3
	// defaultMaxSilence folds in the old stallWatchdog threshold: if no success
	// has landed in this long, declare DOWN even without a fast error (covers a
	// hung call that neither returns success nor errors quickly).
	defaultMaxSilence = 90 * time.Second
)

// newFetchHealth returns a fetch-health machine that starts in the UP state with
// lastSuccess seeded to now (so the maxSilence arm doesn't trip during the first
// ~90s of an idle startup before any GetUpdates has returned).
func newFetchHealth() *fetchHealth {
	return newFetchHealthWithClock(time.Now)
}

// newFetchHealthWithClock is the test seam — inject a deterministic clock.
func newFetchHealthWithClock(now func() time.Time) *fetchHealth {
	t := now()
	return &fetchHealth{
		lastSuccess: t,
		since:       t,
		downAfter:   defaultDownAfterFails,
		maxSilence:  defaultMaxSilence,
		now:         now,
	}
}

// RecordSuccess records a healthy fetch (a GetUpdates that returned without a
// transport error — INCLUDING the zero-updates quiet-night case). It clears the
// failure counter and, if currently DOWN, returns healthRecovered (the DOWN→UP
// edge). The first success always wins recovery — no debounce.
func (h *fetchHealth) RecordSuccess() healthTransition {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.lastSuccess = now
	h.consecFails = 0
	if h.down {
		h.down = false
		h.since = now
		h.lastReason = ""
		return healthRecovered
	}
	return healthNoChange
}

// RecordFailure records a classified transport failure (transient/conflict/
// tripped-permanent). reason is a short human cause for the eventual DOWN edge.
// 429 (rate-limited) MUST NOT be routed here — it is a healthy, reachable server
// pushing back. Returns healthWentDown only on the UP→DOWN edge; subsequent
// failures while already DOWN return healthNoChange (de-spam).
func (h *fetchHealth) RecordFailure(reason string) healthTransition {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecFails++
	h.lastReason = reason
	if h.down {
		return healthNoChange
	}
	if h.consecFails >= h.downAfter {
		return h.goDownLocked()
	}
	return healthNoChange
}

// CheckSilence is the belt-and-suspenders arm for a hung call that neither
// succeeds nor errors fast: if more than maxSilence has elapsed since the last
// success and we are still UP, flip DOWN. Called on a ticker by the watchdog.
// Returns healthWentDown only on the edge.
func (h *fetchHealth) CheckSilence() healthTransition {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.down {
		return healthNoChange
	}
	if h.now().Sub(h.lastSuccess) > h.maxSilence {
		// Silence is its own DOWN cause (a hung getUpdates that neither succeeds
		// nor errors fast), distinct from a fast-error streak. Stamp a
		// silence-specific reason UNCONDITIONALLY so a stale earlier fast-error
		// reason can't mislabel it, and make the reported consec consistent with
		// the documented threshold so the alert text isn't self-contradictory
		// (the old behavior could surface "consec=2" against a "down after 3"
		// threshold because the silence arm never incremented consecFails).
		h.lastReason = "no successful fetch (silence > max-silence; hung/stalled)"
		if h.consecFails < h.downAfter {
			h.consecFails = h.downAfter
		}
		return h.goDownLocked()
	}
	return healthNoChange
}

// goDownLocked transitions to DOWN and stamps the since timestamp. Caller holds
// h.mu.
func (h *fetchHealth) goDownLocked() healthTransition {
	h.down = true
	h.since = h.now()
	return healthWentDown
}

// snapshot returns the current state for building a HealthEvent. Caller holds no
// lock; this takes it.
func (h *fetchHealth) snapshot() (down bool, consec int, since time.Time, reason string, downFor time.Duration, lastSuccess time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	df := time.Duration(0)
	if h.down {
		df = h.now().Sub(h.since)
	}
	return h.down, h.consecFails, h.since, h.lastReason, df, h.lastSuccess
}
