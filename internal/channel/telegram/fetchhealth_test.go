package telegram

import (
	"testing"
	"time"
)

// fakeClock is a controllable monotonic-ish clock for the state-machine tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestHealth() (*fetchHealth, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 6, 17, 14, 0, 0, 0, time.UTC)}
	return newFetchHealthWithClock(clk.now), clk
}

// A quiet night — repeated successful fetches that return zero updates — must
// stay HEALTHY and never fire a DOWN transition. This is the central
// false-positive guard.
func TestFetchHealth_QuietNightStaysHealthy(t *testing.T) {
	h, clk := newTestHealth()
	for i := 0; i < 50; i++ {
		clk.advance(25 * time.Second) // long-poll returns empty every ~25s
		if tr := h.RecordSuccess(); tr != healthNoChange {
			t.Fatalf("quiet-night success #%d produced transition %v, want healthNoChange", i, tr)
		}
	}
	if down, _, _, _, _, _ := h.snapshot(); down {
		t.Fatalf("quiet night must stay UP, got down=%v", down)
	}
}

// Three consecutive transport failures flip UP → DOWN on the THIRD, and only
// the third returns the edge.
func TestFetchHealth_ThreeTransientToDown(t *testing.T) {
	h, _ := newTestHealth()
	if tr := h.RecordFailure("dial"); tr != healthNoChange {
		t.Fatalf("fail #1 transition = %v, want healthNoChange", tr)
	}
	if tr := h.RecordFailure("dial"); tr != healthNoChange {
		t.Fatalf("fail #2 transition = %v, want healthNoChange", tr)
	}
	if tr := h.RecordFailure("dial"); tr != healthWentDown {
		t.Fatalf("fail #3 transition = %v, want healthWentDown", tr)
	}
	if down, consec, _, reason, _, _ := h.snapshot(); !down || consec != 3 || reason != "dial" {
		t.Fatalf("after 3 fails: down=%v consec=%d reason=%q, want down=true consec=3 reason=dial", down, consec, reason)
	}
}

// The first success after DOWN recovers immediately (no debounce).
func TestFetchHealth_FirstSuccessRecovers(t *testing.T) {
	h, _ := newTestHealth()
	h.RecordFailure("dial")
	h.RecordFailure("dial")
	if tr := h.RecordFailure("dial"); tr != healthWentDown {
		t.Fatalf("expected DOWN edge, got %v", tr)
	}
	if tr := h.RecordSuccess(); tr != healthRecovered {
		t.Fatalf("first success after DOWN = %v, want healthRecovered", tr)
	}
	if down, _, _, _, _, _ := h.snapshot(); down {
		t.Fatalf("after recovery snapshot down=%v, want false", down)
	}
}

// Edges are idempotent / de-spammed: extra failures while DOWN do not re-fire
// the DOWN edge, and extra successes while UP do not re-fire recovery. Exactly
// one DOWN and one RECOVERED per outage cycle.
func TestFetchHealth_IdempotentEdges(t *testing.T) {
	h, _ := newTestHealth()
	// Drive down.
	h.RecordFailure("x")
	h.RecordFailure("x")
	if tr := h.RecordFailure("x"); tr != healthWentDown {
		t.Fatalf("expected one DOWN edge, got %v", tr)
	}
	// More failures while DOWN: no further edges.
	for i := 0; i < 5; i++ {
		if tr := h.RecordFailure("x"); tr != healthNoChange {
			t.Fatalf("failure while DOWN #%d returned %v, want healthNoChange", i, tr)
		}
	}
	// Recover once.
	if tr := h.RecordSuccess(); tr != healthRecovered {
		t.Fatalf("expected one RECOVERED edge, got %v", tr)
	}
	// More successes while UP: no further edges.
	for i := 0; i < 5; i++ {
		if tr := h.RecordSuccess(); tr != healthNoChange {
			t.Fatalf("success while UP #%d returned %v, want healthNoChange", i, tr)
		}
	}
}

// 429 must NOT be down: the caller never routes a rate-limit through
// RecordFailure, so success keeps the machine UP. This asserts the contract
// from the consumer's side — a sequence that interleaves a (non-recorded) 429
// with healthy successes stays UP across the downAfter threshold count.
func TestFetchHealth_429IsNotDown(t *testing.T) {
	h, clk := newTestHealth()
	// Simulate: server returns 429 (NOT recorded as failure), then a normal
	// success, repeated more than downAfter times. Never goes down.
	for i := 0; i < defaultDownAfterFails+3; i++ {
		// 429 => caller does NOT call RecordFailure. The next loop's success
		// (or even the absence of any record) leaves the machine healthy.
		clk.advance(2 * time.Second)
		if tr := h.RecordSuccess(); tr != healthNoChange {
			t.Fatalf("interleaved success #%d = %v, want healthNoChange", i, tr)
		}
	}
	if down, _, _, _, _, _ := h.snapshot(); down {
		t.Fatalf("429-only traffic must stay UP, got down=%v", down)
	}
}

// The max-silence arm folds in the old stallWatchdog: a hung call that produces
// neither a fast error nor a success flips DOWN once enough time elapses.
func TestFetchHealth_MaxSilenceFlipsDown(t *testing.T) {
	h, clk := newTestHealth()
	// No record at all; just time passing past maxSilence.
	if tr := h.CheckSilence(); tr != healthNoChange {
		t.Fatalf("CheckSilence within window = %v, want healthNoChange", tr)
	}
	clk.advance(defaultMaxSilence + time.Second)
	if tr := h.CheckSilence(); tr != healthWentDown {
		t.Fatalf("CheckSilence past maxSilence = %v, want healthWentDown", tr)
	}
	// Idempotent: another check while DOWN does not re-fire.
	if tr := h.CheckSilence(); tr != healthNoChange {
		t.Fatalf("CheckSilence while already DOWN = %v, want healthNoChange", tr)
	}
	// A success recovers and resets the silence clock.
	if tr := h.RecordSuccess(); tr != healthRecovered {
		t.Fatalf("recovery after silence-down = %v, want healthRecovered", tr)
	}
}

// A recovery resets the consec counter so the next outage needs a fresh full
// run of failures (no carry-over that would short-circuit the next DOWN).
func TestFetchHealth_RecoveryResetsCounter(t *testing.T) {
	h, _ := newTestHealth()
	h.RecordFailure("a")
	h.RecordFailure("a")
	h.RecordFailure("a") // DOWN
	h.RecordSuccess()    // UP, consec reset
	// Now it should again take 3 failures to go DOWN.
	if tr := h.RecordFailure("b"); tr != healthNoChange {
		t.Fatalf("post-recovery fail #1 = %v, want healthNoChange", tr)
	}
	if tr := h.RecordFailure("b"); tr != healthNoChange {
		t.Fatalf("post-recovery fail #2 = %v, want healthNoChange", tr)
	}
	if tr := h.RecordFailure("b"); tr != healthWentDown {
		t.Fatalf("post-recovery fail #3 = %v, want healthWentDown", tr)
	}
}
