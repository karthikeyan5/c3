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
// Long-poll: server-side Timeout is 25s (Telegram allows up to 50). The
// client-side HTTP RequestOpts.Timeout MUST exceed that; gotgbot's
// DefaultTimeout is 5s and would cancel every long-poll before the server
// ever responds — manifests as "context deadline exceeded" on every cycle
// and zero inbound messages reach the broker.
func (c *Channel) pollLoop() {
	const (
		longPollTimeout    = 25 // seconds, server-side
		longPollHTTPMargin = 10 * time.Second
	)
	httpTimeout := time.Duration(longPollTimeout)*time.Second + longPollHTTPMargin

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
			RequestOpts:    &gotgbot.RequestOpts{Timeout: httpTimeout},
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
		c.dispatchMessage(u.UpdateId, u.Message, false)
	case u.EditedMessage != nil:
		c.dispatchMessage(u.UpdateId, u.EditedMessage, true)
	default:
		// Other update types (callback_query, message_reaction) are accepted
		// in allowed_updates but not yet surfaced. Plan 8+ adds reaction
		// hooks for plugins.
	}
}

// dispatchMessage converts and emits one message. Logs metadata only —
// see DEBUGGING.md for the content policy.
func (c *Channel) dispatchMessage(updateID int64, msg *gotgbot.Message, edited bool) {
	in := convertInbound(c.Name(), msg, c.cfg.STTPrefix)
	if in == nil {
		c.host.Logf("telegram: skip update=%d msg=%d chat=%d thread=%d (unsupported service)",
			updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId)
		return
	}
	kind := "text"
	if len(in.Attachments) > 0 && in.Attachments[0].Kind != "" {
		kind = in.Attachments[0].Kind
	}
	c.host.Logf("telegram: inbound update=%d msg=%d chat=%d thread=%d kind=%s edited=%v",
		updateID, msg.MessageId, msg.Chat.Id, msg.MessageThreadId, kind, edited)
	c.host.Emit(in)
}
