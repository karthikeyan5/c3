// Package telegram is the Telegram channel for C3, implementing the
// internal/channel.Channel interface against the Bot API via gotgbot/v2.
//
// Spec §6 — cleanroom Go rewrite of what the Python POC demonstrated.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/channel"
)

// longPollTimeoutSeconds is the server-side hold for getUpdates. Telegram
// allows up to 50; 25 balances latency vs connection churn. Used by
// timeoutFor to size the HTTP timeout for getUpdates calls.
const longPollTimeoutSeconds = 25

// auth401Threshold is the consecutive-401 count that trips the auth breaker.
// 10 leaves headroom for transient auth weirdness while still cutting off a
// retry-storm before it gets the bot banned.
const auth401Threshold = 10

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
	bot        *gotgbot.Bot
	host       channel.Host
	cfg        Config
	authBrk    *authBreaker
	offsets    *offsetStore
	dedup      *updateDedup
	rate       *rateLimiter
	httpClient *http.Client // shared transport for non-gotgbot calls (file downloads)

	// lastPollReturn is the unix-nanos of the most recent moment the
	// pollLoop's GetUpdates call returned (success OR error). The stall
	// watchdog reads it to detect the half-open / silent-death case where
	// the call hangs past gotgbot's own request timeout. See pollLoop +
	// stallWatchdog. Pattern from grammyjs/runner POLL_STALL_THRESHOLD_MS.
	lastPollReturn atomic.Int64

	// pollDone is closed when pollLoop returns (cleanly via 409 conflict
	// or ctx-cancel). The stall watchdog watches this so it stops emitting
	// "STALL DETECTED" once polling has cleanly stopped — the actual
	// problem at that point is upstream (broker should exit / supervisor
	// should restart it), not a stalled call.
	pollDone chan struct{}

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

	// Custom HTTP transport with explicit network-layer timeouts so a
	// half-open TCP socket (NAT timeout, mid-stream firewall drop) gets
	// surfaced as an error well before a request hangs forever. Defaults
	// in net/http are MaxIdleConns=100, no per-component timeouts —
	// fine for normal use but gives no upper bound on a stuck connection.
	//
	// Sub-agent research (2026-05-09, openclaw + grammyjs/runner): the
	// "polling silently stops" failure mode comes from a hung getUpdates
	// where the kernel never sees a FIN. ResponseHeaderTimeout caps each
	// HTTP response-header wait; combined with the long-poll's own server
	// timeout (25s), this gives gotgbot's request-context a hard ceiling.
	// The stall watchdog (see pollLoop / stallWatchdog) is the second line
	// of defense for cases where this network-layer cap somehow doesn't fire.
	httpTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: time.Duration(longPollTimeoutSeconds+10) * time.Second,
	}

	// Custom BaseBotClient with DefaultRequestOpts set to the "send" budget
	// (20s). Per-call sites pass RequestOpts with method-specific timeouts via
	// requestOptsFor — getUpdates gets the long-poll budget, getMe gets a
	// short control budget, etc. The default catches anything we forget to
	// override and prevents falling back to gotgbot's 5s.
	botClient := &gotgbot.BaseBotClient{
		Client:             http.Client{Transport: httpTransport},
		DefaultRequestOpts: &gotgbot.RequestOpts{Timeout: 20 * time.Second},
	}
	// Reuse the same transport for file downloads (DownloadAttachment).
	// http.DefaultClient has Timeout: 0 (infinite) and no transport-layer
	// timeouts; relying on it would bypass the entire timeout discipline
	// the gotgbot path goes through (daemon.md §11.1-§11.2).
	c.httpClient = &http.Client{
		Transport: httpTransport,
		Timeout:   60 * time.Second, // Bot API caps at 20MB; a healthy download is seconds.
	}
	bot, err := gotgbot.NewBot(c.cfg.BotToken, &gotgbot.BotOpts{
		BotClient:   botClient,
		RequestOpts: requestOptsFor("getMe", longPollTimeoutSeconds),
	})
	if err != nil {
		return fmt.Errorf("telegram: NewBot: %w", err)
	}
	c.bot = bot
	c.host = host
	c.authBrk = newAuthBreaker(auth401Threshold)
	c.dedup = newUpdateDedup(2000, 5*time.Minute)
	c.rate = newRateLimiter()
	if store, sErr := newOffsetStore(Name); sErr == nil {
		c.offsets = store
	} else {
		host.Logf("telegram: offset store unavailable (%v); restarts will re-process the last 24h of updates", sErr)
	}
	c.ctx, c.cancel = context.WithCancel(ctx)

	host.Logf("telegram: connected as @%s", bot.Username)

	// Initialize the stall-watchdog clock so the first ~90s after start
	// don't spuriously trip the watchdog (no actual GetUpdates returns yet).
	c.lastPollReturn.Store(time.Now().UnixNano())
	c.pollDone = make(chan struct{})

	// Start the long-poll loop in a goroutine. Returns immediately after
	// kicking off — Telegram-side processing is async from the broker's
	// startup path.
	go func() {
		defer close(c.pollDone)
		c.pollLoop()
	}()
	go c.stallWatchdog()
	go c.heartbeat()
	return nil
}

// stallWatchdog observes lastPollReturn and logs a loud warning if the
// pollLoop hasn't completed a GetUpdates call (success or error) in
// pollStallThreshold. Intended for the "silent death" failure mode where
// HTTP-layer timeouts somehow fail to fire and the call hangs indefinitely
// — patterned after grammyjs/runner's POLL_STALL_THRESHOLD_MS=90s
// (sub-agent research 2026-05-09).
//
// Currently observe-and-log only. If it fires, we log loud enough that
// it's obvious in DEBUGGING.md flow. A future iteration can promote this
// to force-cancel by switching pollLoop to use a per-call context the
// watchdog can cancel; doing so safely requires teaching gotgbot to
// accept our context (it currently builds its own from RequestOpts).
const (
	pollStallThreshold = 90 * time.Second
	stallCheckInterval = 30 * time.Second
)

func (c *Channel) stallWatchdog() {
	ticker := time.NewTicker(stallCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.pollDone:
			// pollLoop exited (e.g., 409 conflict, ctx cancel). The stall
			// concept doesn't apply anymore; stop nagging. The broker
			// supervisor (or operator) should restart at this point.
			return
		case <-ticker.C:
			since := time.Since(time.Unix(0, c.lastPollReturn.Load()))
			if since > pollStallThreshold {
				c.host.Logf("telegram: STALL DETECTED — %v since last GetUpdates return (threshold %v). Polling loop appears hung; HTTP transport timeouts should have fired by now. Manual intervention may be needed (restart broker).",
					since.Round(time.Second), pollStallThreshold)
			}
		}
	}
}

// heartbeat pings getMe at a fixed interval as an independent liveness
// probe. If the bot is "silently dead" (Telegram-side rotated us off, or
// our token revoked, or our network broke in a way pollLoop hasn't
// surfaced), this catches it within a few minutes regardless of whether
// any users are sending messages.
//
// On consecutive failures past heartbeatFailThreshold, log loud — the
// auth breaker (also tied to non-2xx errors) provides the actual
// retry-storm protection; this is the diagnostic.
const (
	heartbeatInterval       = 5 * time.Minute
	heartbeatFailThreshold  = 3
)

func (c *Channel) heartbeat() {
	// Wait one full interval before the first probe so startup races
	// don't cause spurious early failures.
	timer := time.NewTimer(heartbeatInterval)
	defer timer.Stop()
	consecFails := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
		}
		_, err := c.bot.GetMe(&gotgbot.GetMeOpts{
			RequestOpts: requestOptsFor("getMe", longPollTimeoutSeconds),
		})
		if err != nil {
			consecFails++
			c.host.Logf("telegram: heartbeat getMe failed (consec=%d): %v",
				consecFails, err)
			if consecFails >= heartbeatFailThreshold {
				c.host.Logf("telegram: HEARTBEAT FAILED %d consecutive times — bot may be silently dead. Polling loop status: %v since last GetUpdates return.",
					consecFails, time.Since(time.Unix(0, c.lastPollReturn.Load())).Round(time.Second))
			}
		} else if consecFails > 0 {
			c.host.Logf("telegram: heartbeat recovered after %d failures", consecFails)
			consecFails = 0
		}
		timer.Reset(heartbeatInterval)
	}
}

// Stop halts the polling loop and shuts down the bot.
func (c *Channel) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// Outbound tool implementations live in outbound.go.
