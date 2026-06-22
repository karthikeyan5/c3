package telegram

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// funcBotClient is a scripted gotgbot.BotClient test double. Each getUpdates
// call invokes fn(callNumber) and returns its (raw, err); an optional per-call
// delay (ctx-aware) throttles the loop so a success path that re-polls
// immediately can't busy-spin. Bot.RequestWithContext delegates straight here
// with no error wrapping, so an *gotgbot.TelegramError returned by fn reaches
// classifyError verbatim.
type funcBotClient struct {
	mu    sync.Mutex
	n     int
	fn    func(call int) (json.RawMessage, error)
	delay time.Duration
}

func (f *funcBotClient) RequestWithContext(ctx context.Context, token, method string, params map[string]any, opts *gotgbot.RequestOpts) (json.RawMessage, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	f.mu.Lock()
	f.n++
	call := f.n
	f.mu.Unlock()
	return f.fn(call)
}

func (f *funcBotClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *funcBotClient) GetAPIURL(*gotgbot.RequestOpts) string             { return "" }
func (f *funcBotClient) FileURL(string, string, *gotgbot.RequestOpts) string { return "" }

func newConflictTestChannel(h *fakeHost, bc gotgbot.BotClient) *Channel {
	c := &Channel{
		host:    h,
		cfg:     Config{},
		bot:     &gotgbot.Bot{Token: "test", BotClient: bc},
		authBrk: newAuthBreaker(auth401Threshold),
		health:  newFetchHealth(),
		// Tiny backoff so the conflict-retry path is exercised without
		// multi-second sleeps (zero would fall back to the production consts).
		conflictBackoffBase: time.Millisecond,
		conflictBackoffMax:  5 * time.Millisecond,
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

func startPollLoop(c *Channel) chan struct{} {
	done := make(chan struct{})
	go func() {
		c.pollLoop()
		close(done)
	}()
	return done
}

func awaitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollLoop did not return within 2s after ctx cancel")
	}
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func hasDownEvent(h *fakeHost) bool {
	for _, ev := range h.healthEvents() {
		if ev.State == c3types.HealthStateDown {
			return true
		}
	}
	return false
}

// TestPollLoop_TransientConflictRetriesAndRecovers is the core regression guard
// for the 2026-06-22 fresh-laptop incident: a flaky-network long-poll timeout
// drew a 409 ("another getUpdates active"), and the OLD pollLoop treated 409 as
// terminal — it exited after exactly one call and inbound stayed dead until a
// human killed and restarted the broker.
//
// Fixed behavior: a 409 backs off and RETRIES, so the call count climbs past 1
// and the next (healthy, empty) getUpdates succeeds — fully automatic recovery,
// no restart. A single transient 409 (consec=1 < downAfter=3) must NOT raise the
// out-of-band DOWN alert.
func TestPollLoop_TransientConflictRetriesAndRecovers(t *testing.T) {
	h := &fakeHost{}
	fb := &funcBotClient{
		delay: time.Millisecond,
		fn: func(call int) (json.RawMessage, error) {
			if call == 1 {
				return nil, &gotgbot.TelegramError{Code: 409, Description: "Conflict: terminated by other getUpdates request"}
			}
			return json.RawMessage("[]"), nil // healthy empty long-poll
		},
	}
	c := newConflictTestChannel(h, fb)
	done := startPollLoop(c)

	if !waitUntil(2*time.Second, func() bool { return fb.count() >= 3 }) {
		c.cancel()
		awaitDone(t, done)
		t.Fatalf("getUpdates called %d times; want >=3 — a terminal 409 calls exactly once and never recovers", fb.count())
	}
	if down, _, _, _, _, _ := c.health.snapshot(); down {
		t.Fatalf("a single transient 409 (consec=1 < downAfter=3) must not flip fetch-health DOWN")
	}

	c.cancel()
	awaitDone(t, done)
}

// TestPollLoop_PersistentConflictAlertsAndKeepsPolling covers the genuine
// second-poller case (e.g. another machine holding the token). The conflict is
// NOT self-healing on its own, so two things must happen: (1) the fetch-health
// machine flips DOWN so the operator is alerted out-of-band, and (2) the loop
// KEEPS polling (never exits) so it self-heals the instant the other poller
// stops — still no manual broker restart. The old code recorded a single
// failure and exited, so neither held.
func TestPollLoop_PersistentConflictAlertsAndKeepsPolling(t *testing.T) {
	h := &fakeHost{}
	fb := &funcBotClient{
		delay: time.Millisecond,
		fn: func(call int) (json.RawMessage, error) {
			return nil, &gotgbot.TelegramError{Code: 409, Description: "Conflict"}
		},
	}
	c := newConflictTestChannel(h, fb)
	done := startPollLoop(c)

	if !waitUntil(2*time.Second, func() bool { return hasDownEvent(h) }) {
		c.cancel()
		awaitDone(t, done)
		t.Fatal("persistent 409 must drive fetch-health DOWN (out-of-band alert); none fired")
	}
	base := fb.count()
	if !waitUntil(2*time.Second, func() bool { return fb.count() > base+2 }) {
		c.cancel()
		awaitDone(t, done)
		t.Fatalf("pollLoop stopped polling under persistent 409 (count stuck at %d); it must keep retrying so it self-heals", fb.count())
	}

	c.cancel()
	awaitDone(t, done)
}
