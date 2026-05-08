// Package telegram is the Telegram channel for C3, implementing the
// internal/channel.Channel interface against the Bot API via gotgbot/v2.
//
// Spec §6 — cleanroom Go rewrite of what the Python POC demonstrated.
package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// Name is the canonical channel name used in mappings.json:channels.telegram.*.
const Name = "telegram"

// Config is the channel-specific config under mappings.json:channels.telegram.
type Config struct {
	BotToken            string                       `json:"bot_token"`
	DefaultGroup        string                       `json:"default_group"`
	Groups              map[string]GroupConfig       `json:"groups"`
	DMChatID            int64                        `json:"dm_chat_id"`
	MasterUserID        int64                        `json:"master_user_id"`
	DebounceMS          int                          `json:"debounce_ms"`
	DebounceMaxMessages int                          `json:"debounce_max_messages"`
	FallbackCooldownS   int                          `json:"fallback_cooldown_s"`
	STTPrefix           string                       `json:"stt_prefix"`
}

// GroupConfig matches mappings.GroupConfig but lives in the telegram package
// to avoid an import cycle.
type GroupConfig struct {
	ChatID int64  `json:"chat_id"`
	Title  string `json:"title,omitempty"`
}

// Channel is the Telegram channel implementation. Construct via New, register
// via the broker's channel registry.
type Channel struct {
	bot  *gotgbot.Bot
	host channel.Host
	cfg  Config

	ctx    context.Context
	cancel context.CancelFunc
}

// New returns an unstarted Telegram Channel. The bot connection is established
// in Start; New just allocates.
func New() *Channel {
	return &Channel{}
}

// Name returns the channel identifier.
func (c *Channel) Name() string { return Name }

// Start reads config from host, creates the gotgbot.Bot, and returns once the
// channel is ready to be polled. The actual getUpdates loop launches in a
// follow-up commit; for the scaffolding pass, Start just validates the token
// via Bot.GetMe so the broker fails fast on bad config.
func (c *Channel) Start(ctx context.Context, host channel.Host) error {
	if err := host.Config(Name, &c.cfg); err != nil {
		return fmt.Errorf("telegram: read config: %w", err)
	}
	if c.cfg.BotToken == "" {
		return errors.New("telegram: bot_token missing in mappings.json:channels.telegram")
	}

	bot, err := gotgbot.NewBot(c.cfg.BotToken, nil)
	if err != nil {
		return fmt.Errorf("telegram: NewBot: %w", err)
	}
	c.bot = bot
	c.host = host
	c.ctx, c.cancel = context.WithCancel(ctx)

	host.Logf("telegram: connected as @%s", bot.Username)
	return nil
}

// Stop halts the polling loop and shuts down the bot.
func (c *Channel) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// SendReply / SendTyping / EditMessage / React / DownloadAttachment /
// CreateTopic / ValidateTopic — implementations land in subsequent commits.
// Phase 4B-scaffolding leaves them stubbed so the package compiles against
// the channel.Channel interface.

// errNotImpl is returned from stubbed methods until they're wired.
var errNotImpl = errors.New("telegram: tool not yet implemented (Phase 4B-followup)")

func (c *Channel) SendReply(args c3types.ReplyArgs) (int64, error) {
	return 0, errNotImpl
}

func (c *Channel) SendTyping(chatID int64, threadID *int64) error {
	return errNotImpl
}

func (c *Channel) EditMessage(args c3types.EditArgs) (*c3types.EditResult, error) {
	return nil, errNotImpl
}

func (c *Channel) React(args c3types.ReactArgs) error {
	return errNotImpl
}

func (c *Channel) DownloadAttachment(fileID string) (string, error) {
	return "", errNotImpl
}

func (c *Channel) CreateTopic(chatID int64, name string) (int64, error) {
	return 0, errNotImpl
}

func (c *Channel) ValidateTopic(chatID int64, threadID int64) error {
	return errNotImpl
}
