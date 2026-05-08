package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// allowedUpdates is the conservative default opt-in. message_reaction is
// included so plugins (future) can hook reactions. forum-related updates
// arrive as fields on Message, not as separate update types — no opt-in
// needed there.
var allowedUpdates = []string{"message", "edited_message", "callback_query", "message_reaction"}

// pollLoop runs Bot.GetUpdates in a loop, converting each Message into an
// Inbound and emitting via host.Emit. Honors c.ctx cancellation. Backoff on
// errors: 1s base, doubles up to 30s, resets on success.
//
// Long-poll timeout: 25 seconds (server-side hold). Telegram allows up to
// 50; 25 is a balance between latency and connection churn.
func (c *Channel) pollLoop() {
	const longPollTimeout = 25 // seconds

	var offset int64
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		opts := &gotgbot.GetUpdatesOpts{
			Offset:         offset,
			Timeout:        longPollTimeout,
			AllowedUpdates: allowedUpdates,
		}
		updates, err := c.bot.GetUpdates(opts)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			c.host.Logf("telegram: GetUpdates error: %v (backoff %v)", err, backoff)
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		// Reset backoff on success.
		backoff = time.Second

		for _, u := range updates {
			c.dispatchUpdate(&u)
			if u.UpdateId >= offset {
				offset = u.UpdateId + 1
			}
		}
	}
}

// dispatchUpdate converts a single Update into an Inbound (or several) and
// emits to the host. EditedMessage is treated like a fresh Message for v1 —
// edits flow as new inbound. Plugins (future) can dedupe if needed.
func (c *Channel) dispatchUpdate(u *gotgbot.Update) {
	switch {
	case u.Message != nil:
		if in := convertInbound(c.Name(), u.Message, c.cfg.STTPrefix); in != nil {
			c.host.Emit(in)
		}
	case u.EditedMessage != nil:
		if in := convertInbound(c.Name(), u.EditedMessage, c.cfg.STTPrefix); in != nil {
			c.host.Emit(in)
		}
	default:
		// Other update types (callback_query, message_reaction) are accepted
		// in allowed_updates but not yet surfaced. Plan 8+ adds reaction
		// hooks for plugins.
	}
}
