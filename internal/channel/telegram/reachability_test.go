package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// newWiredChannel builds a Channel with the inbound machine, the outbound
// machine, and the combiner all wired (deterministic shared clock) plus a
// fakeHost to capture NotifyHealth edges — the production wiring, unlike the
// bare-struct legacy-path tests in health_wiring_test.go.
func newWiredChannel() (*Channel, *fakeHost) {
	h := &fakeHost{}
	clk := &fakeClock{t: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)}
	c := &Channel{
		host:      h,
		cfg:       Config{},
		ctx:       context.Background(),
		health:    newFetchHealthWithClock(clk.now),
		outHealth: newOutboundHealthWithClock(clk.now),
		reach:     newReachabilityWithClock(clk.now),
	}
	return c, h
}

// --- outboundHealth machine ---------------------------------------------------

// downAfter=2 distinct failure EVENTS: the first is a no-op edge, the second
// flips DOWN, further failures while DOWN are de-spammed, and the first success
// recovers.
func TestOutboundHealth_TwoEventsToDown(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)}
	o := newOutboundHealthWithClock(clk.now)
	if tr := o.RecordFailure("a"); tr != healthNoChange {
		t.Fatalf("fail #1 = %v, want healthNoChange (downAfter=2)", tr)
	}
	if tr := o.RecordFailure("a"); tr != healthWentDown {
		t.Fatalf("fail #2 = %v, want healthWentDown", tr)
	}
	for i := 0; i < 3; i++ {
		if tr := o.RecordFailure("a"); tr != healthNoChange {
			t.Fatalf("fail while DOWN #%d = %v, want healthNoChange (de-spam)", i, tr)
		}
	}
	if tr := o.RecordSuccess(); tr != healthRecovered {
		t.Fatalf("first success after DOWN = %v, want healthRecovered", tr)
	}
	if tr := o.RecordSuccess(); tr != healthNoChange {
		t.Fatalf("success while UP = %v, want healthNoChange", tr)
	}
}

// ForceReset clears a DOWN machine to UP WITHOUT an edge, and a fresh run of 2
// failures is needed to re-trip (the counter really reset, not left at 2).
func TestOutboundHealth_ForceResetClears(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)}
	o := newOutboundHealthWithClock(clk.now)
	o.RecordFailure("a")
	o.RecordFailure("a") // DOWN
	o.ForceReset()
	if down, consec, _, _, _ := o.snapshot(); down || consec != 0 {
		t.Fatalf("after ForceReset: down=%v consec=%d, want down=false consec=0", down, consec)
	}
	if tr := o.RecordFailure("b"); tr != healthNoChange {
		t.Fatalf("post-reset fail #1 = %v, want healthNoChange", tr)
	}
	if tr := o.RecordFailure("b"); tr != healthWentDown {
		t.Fatalf("post-reset fail #2 = %v, want healthWentDown", tr)
	}
}

// --- reachReason --------------------------------------------------------------

func TestReachReason(t *testing.T) {
	cases := []struct {
		in, out bool
		want    string
	}{
		{true, false, "inbound unreachable"},
		{false, true, "outbound send failing"},
		{true, true, "unreachable (inbound + outbound)"},
		{false, false, ""},
	}
	for _, tc := range cases {
		if got := reachReason(tc.in, tc.out); got != tc.want {
			t.Errorf("reachReason(%v,%v) = %q, want %q", tc.in, tc.out, got, tc.want)
		}
	}
}

// --- combiner edges (direct) --------------------------------------------------

// The combined edge fires on the FIRST sub going down and stays silent while a
// second sub flips down under it; inbound recovery is wire-proof (it clears
// outbound too), so it fires the UP edge even with outbound still nominally down.
func TestReachability_CombinedEdges(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)}
	r := newReachabilityWithClock(clk.now)

	fire, ev := r.recordInbound(true, 3)
	if !fire || ev.State != c3types.HealthStateDown || ev.Reason != "inbound unreachable" || ev.Consec != 3 || ev.Channel != Name {
		t.Fatalf("first inbound-down edge = (%v, %+v), want fire DOWN reason='inbound unreachable' consec=3 channel=telegram", fire, ev)
	}
	if fire, _ := r.recordOutbound(true, 2); fire {
		t.Fatal("outbound going down while already combined-DOWN must NOT fire")
	}
	// Inbound recovers → wire-proof clears outbound too → combined UP edge fires.
	fire, ev = r.recordInbound(false, 0)
	if !fire || ev.State != c3types.HealthStateUp {
		t.Fatalf("inbound recovery (wire-proof) edge = (%v, %+v), want fire UP", fire, ev)
	}
}

// Outbound-only down then recover fires exactly one DOWN and one UP on the
// combined state (the "last sub clears" recovery, no wire-proof involved).
func TestReachability_OutboundOnlyDownRecovers(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)}
	r := newReachabilityWithClock(clk.now)
	fire, ev := r.recordOutbound(true, 2)
	if !fire || ev.State != c3types.HealthStateDown || ev.Reason != "outbound send failing" {
		t.Fatalf("outbound-down edge = (%v, %+v), want fire DOWN reason='outbound send failing'", fire, ev)
	}
	fire, ev = r.recordOutbound(false, 0)
	if !fire || ev.State != c3types.HealthStateUp {
		t.Fatalf("outbound-recovery edge = (%v, %+v), want fire UP", fire, ev)
	}
}

// --- spec §2e behaviors (wired channel) --------------------------------------

// (i) Wire drops → inbound ×3 → combined DOWN fires ONCE; outbound then fails ×2
// while combined already down → NO second NotifyHealth (the core anti-spam case).
func TestReach_WireDrop_InboundDownThenOutbound_SingleNotify(t *testing.T) {
	c, h := newWiredChannel()
	for i := 0; i < 3; i++ {
		c.reportHealth(c.health.RecordFailure("transient (network/timeout/5xx)"))
	}
	evs := h.healthEvents()
	if len(evs) != 1 || evs[0].State != c3types.HealthStateDown {
		t.Fatalf("after inbound ×3, events = %+v, want exactly one DOWN", evs)
	}
	if evs[0].Reason != "inbound unreachable" || evs[0].Channel != Name || evs[0].Consec != 3 {
		t.Fatalf("DOWN event = %+v, want reason='inbound unreachable' channel=telegram consec=3", evs[0])
	}
	// Outbound now fails ×2 while combined already down → no new notification.
	c.feedOutboundFailure(rbTGErr(500), "SendReply transient send error")
	c.feedOutboundFailure(rbTGErr(500), "SendReply transient send error")
	if n := len(h.healthEvents()); n != 1 {
		t.Fatalf("outbound failing while already combined-DOWN must not re-notify; calls=%d, want 1", n)
	}
}

// (ii) Sends failing while polls OK → outbound ×2 → combined DOWN fires once.
func TestReach_OutboundOnly_SingleNotify(t *testing.T) {
	c, h := newWiredChannel()
	c.feedOutboundFailure(rbTGErr(500), "SendReply transient send error")
	if n := len(h.healthEvents()); n != 0 {
		t.Fatalf("first outbound failure must not fire (downAfter=2); calls=%d, want 0", n)
	}
	c.feedOutboundFailure(rbTGErr(500), "SendReply transient send error")
	evs := h.healthEvents()
	if len(evs) != 1 || evs[0].State != c3types.HealthStateDown {
		t.Fatalf("after outbound ×2, events = %+v, want exactly one DOWN", evs)
	}
	if evs[0].Reason != "outbound send failing" || evs[0].Consec != 2 || evs[0].Channel != Name {
		t.Fatalf("DOWN event = %+v, want reason='outbound send failing' consec=2 channel=telegram", evs[0])
	}
}

// (iii) Recovery: inbound success clears BOTH (wire-proof) → combined RECOVERED
// fires once; and the outbound machine is genuinely reset (a lone post-recovery
// failure does not immediately re-trip).
func TestReach_InboundRecoveryClearsBoth(t *testing.T) {
	c, h := newWiredChannel()
	for i := 0; i < 3; i++ {
		c.reportHealth(c.health.RecordFailure("transient (network/timeout/5xx)"))
	}
	c.feedOutboundFailure(rbTGErr(500), "x")
	c.feedOutboundFailure(rbTGErr(500), "x")
	if n := len(h.healthEvents()); n != 1 {
		t.Fatalf("precondition: exactly one combined DOWN, got %d", n)
	}
	// Inbound recovers → wire-proof → combined RECOVERED once.
	c.reportHealth(c.health.RecordSuccess())
	evs := h.healthEvents()
	if len(evs) != 2 || evs[1].State != c3types.HealthStateUp {
		t.Fatalf("after recovery, events = %+v, want [DOWN, UP]", evs)
	}
	// The outbound machine was reset (consec cleared): a single failure now must
	// NOT re-trip (it would if consec had been left at 2).
	c.feedOutboundFailure(rbTGErr(500), "x")
	if n := len(h.healthEvents()); n != 2 {
		t.Fatalf("outbound machine not reset by wire-proof; a lone post-recovery failure re-fired: calls=%d, want 2", n)
	}
}

// (iv) A 429 (rate-limited) send — and a permanent 4xx — never drive outbound
// DOWN (429 = a reachable server pushing back; permanent = the token breaker's
// job).
func TestReach_429AndPermanentDoNotDriveOutboundDown(t *testing.T) {
	c, h := newWiredChannel()
	for i := 0; i < 5; i++ {
		c.feedOutboundFailure(rbTGErr(429), "x") // rate-limited
		c.feedOutboundFailure(rbTGErr(400), "x") // permanent
	}
	if n := len(h.healthEvents()); n != 0 {
		t.Fatalf("429/permanent sends must never drive outbound DOWN; calls=%d, want 0", n)
	}
	if down, consec, _, _, _ := c.outHealth.snapshot(); down || consec != 0 {
		t.Fatalf("429/permanent must not touch the outbound machine; down=%v consec=%d", down, consec)
	}
}

// (v) A ctx-cancel readback give-up does NOT drive outbound DOWN: retryReadbackSend
// returns ctx.Err() at the first backoff (readback.go:419-420), BEFORE the
// give-up feed at :424 is ever reached.
func TestReach_CtxCancelReadbackGiveUp_NoOutboundDown(t *testing.T) {
	c, h := newWiredChannel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ctx = ctx
	_, err := c.retryReadbackSend(func() (int64, error) { return 0, rbTGErr(500) })
	if err == nil {
		t.Fatal("want ctx error on cancelled readback")
	}
	if n := len(h.healthEvents()); n != 0 {
		t.Fatalf("ctx-cancel give-up must not drive outbound DOWN; calls=%d, want 0", n)
	}
	if down, consec, _, _, _ := c.outHealth.snapshot(); down || consec != 0 {
		t.Fatalf("ctx-cancel must not touch the outbound machine; down=%v consec=%d", down, consec)
	}
}

// (vi) Every combined event carries Channel="telegram" so the broker collapses
// them onto ONE status-line entry, and the state reflects the combined
// reachability across a full down→recover cycle.
func TestReach_SingleTelegramEntryCombinedState(t *testing.T) {
	c, h := newWiredChannel()
	for i := 0; i < 3; i++ {
		c.reportHealth(c.health.RecordFailure("t"))
	}
	c.feedOutboundFailure(rbTGErr(500), "x")
	c.feedOutboundFailure(rbTGErr(500), "x")
	c.reportHealth(c.health.RecordSuccess())
	evs := h.healthEvents()
	if len(evs) != 2 || evs[0].State != c3types.HealthStateDown || evs[1].State != c3types.HealthStateUp {
		t.Fatalf("want exactly [DOWN, UP] on the combined state, got %+v", evs)
	}
	for i, ev := range evs {
		if ev.Channel != Name {
			t.Fatalf("event %d channel=%q, want %q (broker keys ONE entry per channel name)", i, ev.Channel, Name)
		}
	}
}

// --- concurrency (run under -race) -------------------------------------------

// The combiner mutex must serialize concurrent recordInbound/recordOutbound with
// no data race and no deadlock, and leave a consistent final state.
func TestReachability_ConcurrentRecords_NoRace(t *testing.T) {
	r := newReachability()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); r.recordInbound(n%2 == 0, n) }(i)
		go func(n int) { defer wg.Done(); r.recordOutbound(n%3 == 0, n) }(i)
	}
	wg.Wait()
	r.mu.Lock()
	consistent := r.combinedDown == (r.inboundDown || r.outboundDown)
	r.mu.Unlock()
	if !consistent {
		t.Fatal("combinedDown inconsistent with inboundDown||outboundDown after concurrent records")
	}
}

// The real call shapes — inbound edges (fetchHealth-lock → combiner-lock),
// outbound failures (outHealth-lock → combiner-lock), and outbound successes —
// driven concurrently as production does, must be race- and deadlock-free
// (sub-machine lock taken first, then the combiner lock; never nested).
func TestReach_ChannelConcurrentFeeds_NoRace(t *testing.T) {
	c := &Channel{
		host:      &fakeHost{},
		ctx:       context.Background(),
		health:    newFetchHealth(),
		outHealth: newOutboundHealth(),
		reach:     newReachability(),
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); c.reportHealth(c.health.RecordFailure("t")) }()
		go func() { defer wg.Done(); c.feedOutboundFailure(rbTGErr(500), "x") }()
		go func() { defer wg.Done(); c.recordOutboundSuccess() }()
	}
	wg.Wait()
}
