package telegram

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// rateLimiter pre-throttles outbound Telegram calls to stay inside Telegram's
// published rate windows:
//
//   - global: 30 req/sec across the bot
//   - per group chat: 20 messages/minute
//   - per private chat: 1 message/second (burst 5)
//
// Waiting blocks the worker until a slot opens — same shape as OpenClaw's
// `@grammyjs/transformer-throttler`. Each call waits on global AND per-chat
// in sequence; the per-chat limiter is created lazily by chat-id sign:
// negative→group, positive→private.
type rateLimiter struct {
	global *rate.Limiter

	mu      sync.Mutex
	perChat map[int64]*rate.Limiter
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		global:  rate.NewLimiter(rate.Every(time.Second/30), 30),
		perChat: map[int64]*rate.Limiter{},
	}
}

// Wait blocks until a token is available globally and for the chat. Returns
// the context's error if it's canceled while waiting (in which case the
// caller should abort the outbound call).
func (l *rateLimiter) Wait(ctx context.Context, chatID int64) error {
	if err := l.global.Wait(ctx); err != nil {
		return err
	}
	return l.perChatLimiter(chatID).Wait(ctx)
}

func (l *rateLimiter) perChatLimiter(chatID int64) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lim, ok := l.perChat[chatID]; ok {
		return lim
	}
	var lim *rate.Limiter
	if chatID < 0 {
		// Group/supergroup/channel — Telegram's documented 20/min ceiling.
		lim = rate.NewLimiter(rate.Every(time.Minute/20), 20)
	} else {
		// Private chat — 1/sec sustained, modest burst headroom for paste.
		lim = rate.NewLimiter(rate.Every(time.Second), 5)
	}
	l.perChat[chatID] = lim
	return lim
}
