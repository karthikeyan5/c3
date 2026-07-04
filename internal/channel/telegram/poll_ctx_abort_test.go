package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// TestPollLoop_CtxCancelAbortsInflightSlowHTTP proves the poll loop's getUpdates
// request is tied to the channel ctx and aborts IMMEDIATELY on cancel, even with
// a real HTTP request in flight against a server that never responds. This is the
// property Broker.Shutdown relies on: cancelling the channel ctx must unwedge the
// long-poll rather than wait out its 55s HTTP budget.
//
// Unlike the funcBotClient-based tests, this uses a REAL *gotgbot.Bot over a real
// httptest server, so it exercises the actual net/http request-context path
// (RequestWithContext → http.NewRequestWithContext) end to end.
func TestPollLoop_CtxCancelAbortsInflightSlowHTTP(t *testing.T) {
	reached := make(chan struct{}, 1)
	// stop releases the (single) blocked server handler at teardown so
	// srv.Close() doesn't wait on it. Closed BEFORE srv.Close via defer LIFO.
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that the poll's getUpdates request actually hit the wire.
		select {
		case reached <- struct{}{}:
		default:
		}
		// A long-poll that only ends when the client aborts (ctx cancel on broker
		// shutdown) or the test tears down — never on its own within the loop's
		// patience. The property under test is client-side: the loop's request must
		// abort on ctx cancel regardless of what the server does.
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer srv.Close()
	defer close(stop)

	h := &fakeHost{}
	c := &Channel{
		host:      h,
		cfg:       Config{},
		authBrk:   newAuthBreaker(auth401Threshold),
		health:    newFetchHealth(),
		endpoints: []string{srv.URL}, // requestOptsFor points the real bot here
		bot: &gotgbot.Bot{
			Token:     "test",
			BotClient: &gotgbot.BaseBotClient{Client: http.Client{}},
		},
	}
	c.activeEndpoint.Store(0)
	c.ctx, c.cancel = context.WithCancel(context.Background())

	done := startPollLoop(c)

	// Wait until the getUpdates HTTP request is genuinely in flight on the server.
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		c.cancel()
		awaitDone(t, done)
		t.Fatal("poll loop never issued the getUpdates HTTP request")
	}

	// Cancel (broker shutdown) mid in-flight request; the loop must return promptly,
	// not block until the 55s long-poll HTTP timeout.
	start := time.Now()
	c.cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollLoop did not return within 2s of ctx cancel despite an in-flight slow HTTP request (long-poll not ctx-bound)")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pollLoop took %v to abort the in-flight request after cancel; want prompt (< 2s)", elapsed)
	}
}
