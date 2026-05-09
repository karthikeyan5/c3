package telegram

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// errClass tags a Telegram error so the polling loop and outbound call sites
// can pick the right recovery: backoff, sleep retry-after, exit cleanly, or
// trip a circuit-breaker.
//
// Adapted from OpenClaw's `extensions/telegram/src/fetch.ts` error
// classification — they're TS+grammy, we're Go+gotgbot, but the shape of
// "what telegram errors mean and how to react" is the same.
type errClass int

const (
	errClassNone        errClass = iota // success / nil
	errClassPermanent                   // 401, 403, other 4xx — won't fix on retry
	errClassRateLimited                 // 429 with retry_after seconds
	errClassConflict                    // 409 — another poller holds this token
	errClassTransient                   // network / timeout / 5xx — retry with backoff
)

func (c errClass) String() string {
	switch c {
	case errClassPermanent:
		return "permanent"
	case errClassRateLimited:
		return "rate_limited"
	case errClassConflict:
		return "conflict"
	case errClassTransient:
		return "transient"
	default:
		return "none"
	}
}

// classifyError returns the error class and (for rate-limited) the
// retry_after value Telegram requested in seconds.
func classifyError(err error) (errClass, int) {
	if err == nil {
		return errClassNone, 0
	}
	var tg *gotgbot.TelegramError
	if errors.As(err, &tg) {
		switch tg.Code {
		case 401, 403:
			return errClassPermanent, 0
		case 409:
			return errClassConflict, 0
		case 429:
			ra := 0
			if tg.ResponseParams != nil {
				ra = int(tg.ResponseParams.RetryAfter)
			}
			return errClassRateLimited, ra
		}
		// Other 4xx: bad request, chat-not-found, etc. Permanent — retrying
		// won't help.
		if tg.Code >= 400 && tg.Code < 500 {
			return errClassPermanent, 0
		}
		// 5xx: Telegram's problem, keep trying.
		return errClassTransient, 0
	}
	// Non-TelegramError: usually network / context-deadline / dns. All
	// transient by default — better to retry an unknown than to crash.
	if isTransientNetworkError(err) {
		return errClassTransient, 0
	}
	return errClassTransient, 0
}

// isTransientNetworkError matches the kinds of errors gotgbot+net/http surface
// when the wire is flaky. Inspired by OpenClaw's FALLBACK_RETRY_ERROR_CODES
// (`extensions/telegram/src/fetch.ts`).
func isTransientNetworkError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	s := err.Error()
	for _, needle := range []string{
		"connection refused",
		"connection reset",
		"no such host",
		"i/o timeout",
		"broken pipe",
		"EOF",
		"server closed",
		"use of closed network connection",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// timeoutFor picks the per-method HTTP timeout. Long-poll gets the budget for
// its server-side hold + slack; control calls (getMe, setMyCommands, etc.)
// get a short budget; sends/edits get medium; downloads get long.
//
// Generalizes the lesson from the 2026-05-09 polling-timeout bug: a single
// global HTTP timeout either starves the long-poll or makes everything else
// hang. Per-method matches OpenClaw's `bot-core.ts` resolveTelegramRequestTimeoutMs.
func timeoutFor(method string, longPollSeconds int) time.Duration {
	switch method {
	case "getUpdates":
		// Server holds up to longPollSeconds, then needs additional time to
		// flush the response. 30s slack covers high-latency networks. The
		// 2026-05-09 fix used 10s slack and still hit occasional timeouts.
		return time.Duration(longPollSeconds)*time.Second + 30*time.Second
	case "getMe", "deleteWebhook", "setMyCommands", "setWebhook", "logOut", "close":
		return 10 * time.Second
	case "getFile":
		return 30 * time.Second
	default:
		// Sends, edits, reactions, typing, forum-topic CRUD — all should
		// complete in a couple seconds normally; 20s buys slack for slow
		// networks without making failures hang.
		return 20 * time.Second
	}
}

// requestOptsFor returns gotgbot.RequestOpts with the per-method timeout
// already filled in. Use this in every outbound call so we never accidentally
// fall back to gotgbot's 5s DefaultTimeout.
func requestOptsFor(method string, longPollSeconds int) *gotgbot.RequestOpts {
	return &gotgbot.RequestOpts{Timeout: timeoutFor(method, longPollSeconds)}
}

// authBreaker tracks consecutive 401s on the channel. Once `threshold` is
// reached, the breaker trips and the channel suspends polling — preventing a
// retry-storm against a revoked token (which Telegram can interpret as abuse
// and respond to by banning the bot, per OpenClaw's
// `sendchataction-401-backoff.ts` warning).
type authBreaker struct {
	mu        sync.Mutex
	consec    int
	threshold int
	tripped   bool
}

func newAuthBreaker(threshold int) *authBreaker {
	return &authBreaker{threshold: threshold}
}

// RecordFail increments the consecutive-failure counter. Returns whether the
// breaker is now tripped.
func (b *authBreaker) RecordFail() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consec++
	if b.consec >= b.threshold {
		b.tripped = true
	}
	return b.tripped
}

// RecordSuccess clears any consecutive-failure state.
func (b *authBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consec = 0
	b.tripped = false
}

// IsTripped returns whether the breaker is currently tripped.
func (b *authBreaker) IsTripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

// Consec returns the current consecutive-failure count (diagnostic).
func (b *authBreaker) Consec() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consec
}

// recordOutboundErr classifies an outbound call's error and feeds the auth
// breaker. Called from every Send/Edit/React/etc. on err. Logs loud on
// permanent (401/403/4xx) so the user sees the underlying problem; transient
// errors are quieter (the agent already sees them surface as tool errors).
//
// 429 retry_after is logged but NOT auto-retried at this layer — the agent
// can decide whether to retry based on the error.
func (c *Channel) recordOutboundErr(err error) {
	class, retryAfter := classifyError(err)
	switch class {
	case errClassPermanent:
		tripped := c.authBrk.RecordFail()
		c.host.Logf("telegram: outbound permanent error (consec=%d, tripped=%v): %v",
			c.authBrk.Consec(), tripped, err)
	case errClassRateLimited:
		c.host.Logf("telegram: outbound 429 rate-limited; retry_after=%ds", retryAfter)
	case errClassConflict:
		c.host.Logf("telegram: outbound 409 CONFLICT (unexpected on outbound): %v", err)
	}
}

// recordOutboundSuccess clears the auth breaker. Any successful API call is
// proof the token still works.
func (c *Channel) recordOutboundSuccess() {
	if c.authBrk != nil {
		c.authBrk.RecordSuccess()
	}
}
