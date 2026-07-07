package telegram

import (
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// reachability combines the inbound (fetchHealth) and outbound (outboundHealth)
// sub-states into the ONE telegram reachability signal the status line shows.
// The broker/status-line contract is one health entry per channel NAME
// (healthFileEntry, health.json), so both directions MUST surface on the single
// "telegram" entry: inbound-down and outbound-down for one root cause (the wire
// is down ⇒ both fail) must NEVER produce two separate notifications.
//
//	combinedDown = inboundDown || outboundDown
//
// A notification fires ONLY on the combined edge: UP→DOWN when the FIRST sub
// goes down, DOWN→UP when the LAST sub clears. Sub-state churn while already
// combined-DOWN (e.g. outbound flips down after inbound already took the state
// down) is silent.
//
// Locking (CRITIQUE FOLD #5): recordInbound/recordOutbound run on MANY
// goroutines — inbound from the poll loop, the silence watchdog, and the
// heartbeat; outbound from the route worker (SendReply) and detached echo
// goroutines (readback). ONE mutex (r.mu) covers BOTH methods. The sub-machines
// (fetchHealth/outboundHealth) each return their transition under their OWN lock
// FIRST; the caller THEN takes r.mu here — the two locks are taken SEQUENTIALLY,
// never nested, so there is no lock-ordering deadlock.
type reachability struct {
	mu           sync.Mutex
	inboundDown  bool
	outboundDown bool
	combinedDown bool
	since        time.Time // when the combined state was entered
	now          func() time.Time
}

// newReachability returns a combiner on the wall clock (both sub-states UP).
func newReachability() *reachability {
	return newReachabilityWithClock(time.Now)
}

// newReachabilityWithClock is the test seam — inject a deterministic clock.
func newReachabilityWithClock(now func() time.Time) *reachability {
	return &reachability{now: now, since: now()}
}

// recordInbound updates the inbound sub-state and returns the combined edge (if
// any). On inbound RECOVERY (down=false) it ALSO clears outboundDown: a
// successful getUpdates proves the wire+token work, so the combined state must
// not stay DOWN waiting for a send to confirm outbound. (The outboundHealth
// MACHINE itself is reset by the caller via ForceReset, OUTSIDE this lock — see
// reportHealth — so the two locks stay sequential, never nested.) consec is the
// driving side's failure count, copied into the combined event.
func (r *reachability) recordInbound(down bool, consec int) (bool, c3types.HealthEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inboundDown = down
	if !down {
		r.outboundDown = false // wire-proof: a healthy fetch clears outbound
	}
	return r.recomputeLocked(consec)
}

// recordOutbound updates the outbound sub-state and returns the combined edge
// (if any). consec is the outbound failure-event count for the combined event.
func (r *reachability) recordOutbound(down bool, consec int) (bool, c3types.HealthEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outboundDown = down
	return r.recomputeLocked(consec)
}

// recomputeLocked recomputes combinedDown and returns (fire, event) ONLY on a
// combined edge. On no edge it returns (false, zero-event). Caller holds r.mu.
func (r *reachability) recomputeLocked(consec int) (bool, c3types.HealthEvent) {
	newCombined := r.inboundDown || r.outboundDown
	if newCombined == r.combinedDown {
		return false, c3types.HealthEvent{}
	}
	prevSince := r.since // when the previous combined state was entered
	r.combinedDown = newCombined
	r.since = r.now()
	ev := c3types.HealthEvent{
		Channel: Name,
		Since:   r.since,
		Consec:  consec,
	}
	if newCombined {
		ev.State = c3types.HealthStateDown
		ev.Reason = reachReason(r.inboundDown, r.outboundDown)
	} else {
		ev.State = c3types.HealthStateUp
		ev.Reason = ""                      // recovered; both directions clear
		ev.DownFor = r.since.Sub(prevSince) // how long the combined state was DOWN (for the recovered log line)
	}
	return true, ev
}

// reachReason names which direction(s) are unreachable for the combined DOWN
// event, computed from the current sub-state at fire time.
func reachReason(inboundDown, outboundDown bool) string {
	switch {
	case inboundDown && outboundDown:
		return "unreachable (inbound + outbound)"
	case inboundDown:
		return "inbound unreachable"
	case outboundDown:
		return "outbound send failing"
	default:
		return ""
	}
}
