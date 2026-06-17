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
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// longPollTimeoutSeconds is the server-side hold for getUpdates. Telegram
// allows up to 50; 25 balances latency vs connection churn. Used by
// timeoutFor to size the HTTP timeout for getUpdates calls.
const longPollTimeoutSeconds = 25

// auth401Threshold is the consecutive-401 count that trips the auth breaker.
// 10 leaves headroom for transient auth weirdness while still cutting off a
// retry-storm before it gets the bot banned.
const auth401Threshold = 10

// endpointFailoverThreshold is the consecutive-TRANSIENT-failure count that
// triggers an endpoint advance when more than one Bot-API base is configured.
// Transient failures (network/timeout/5xx) are the IP-block signature; 5 gives
// the active endpoint a fair retry budget before swapping to the next one. Only
// consulted when len(endpoints) > 1 (a single-endpoint setup never fails over).
const endpointFailoverThreshold = 5

// telegramAPIURLEnv is the env override for the primary Bot-API base URL. It
// wins over APIBaseURL in mappings.json, matching the C3_LOG_FILE precedent
// (env beats file). Empty/unset leaves the config field untouched.
const telegramAPIURLEnv = "C3_TELEGRAM_API_URL"

// Name is the canonical channel name used in mappings.json:channels.telegram.*.
const Name = "telegram"

// Config is the channel-specific config under mappings.json:channels.telegram.
type Config struct {
	BotToken            string                          `json:"bot_token"`
	DefaultGroup        string                          `json:"default_group"`
	Groups              map[string]mappings.GroupConfig `json:"groups"`
	DMChatID            int64                           `json:"dm_chat_id"`
	MasterUserID        int64                           `json:"master_user_id"`
	DebounceMS          int                             `json:"debounce_ms"`
	DebounceMaxMessages int                             `json:"debounce_max_messages"`
	FallbackCooldownS   int                             `json:"fallback_cooldown_s"`
	STTPrefix           string                          `json:"stt_prefix"`

	// APIBaseURL is the Bot-API base the channel talks to. Empty means
	// gotgbot's default (api.telegram.org) — byte-for-byte today's behavior.
	// Telegram's IPs are null-routed in India, so the maintainer can point this
	// at a reverse proxy they OWN (e.g. a Cloudflare Worker on a custom domain).
	// The bot token is interpolated into every request path, so this must ONLY
	// ever be a maintainer-controlled host (validated https:// at Start). The
	// C3_TELEGRAM_API_URL env var overrides this field (see Start).
	APIBaseURL string `json:"api_base_url,omitempty"`
	// APIBaseURLs is an optional ordered failover list appended after
	// APIBaseURL. The poll loop advances to the next endpoint after a run of
	// transient failures (the IP-block signature) and len(endpoints) > 1.
	APIBaseURLs []string `json:"api_base_urls,omitempty"`
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
	sentPolls  *sentPollMap // pollID → route+owner for poll-result routing (P4)
	httpClient *http.Client // shared transport for non-gotgbot calls (file downloads)

	// endpoints is the ordered, deduped list of Bot-API base URLs the channel
	// may use. It is always non-empty: with no config it is [""], where "" means
	// gotgbot's DefaultAPIURL (api.telegram.org) — byte-for-byte today's default
	// behavior. With config it is [APIBaseURL-or-"", ...APIBaseURLs] deduped.
	// requestOptsFor reads endpoints[activeEndpoint] as the per-call APIURL, so a
	// "" entry transparently resolves to gotgbot's default via GetAPIURL.
	endpoints []string
	// activeEndpoint indexes endpoints. The poll loop advances it on a run of
	// transient failures (P2 failover) when len(endpoints) > 1; every other call
	// site reads it via requestOptsFor, so the swap is picked up on the next call.
	activeEndpoint atomic.Int32

	// health is the single fetch-health state machine. It is the ONLY source
	// of "is Telegram reachable?" — it replaced the two prior competing
	// false-positive watchdogs (stallWatchdog + heartbeat's HEARTBEAT-FAILED
	// alarm). Driven from pollLoop (RecordSuccess/RecordFailure), the
	// silenceWatchdog (CheckSilence), and the heartbeat (RecordFailure on
	// getMe failure). The machine's own lastSuccess timestamp now owns
	// silence detection (the old standalone lastPollReturn atomic was
	// retired). On an edge it fires host.NotifyHealth OUTSIDE the machine's
	// lock — see reportHealth.
	health *fetchHealth

	// pollDone is closed when pollLoop returns (cleanly via 409 conflict
	// or ctx-cancel). The stall watchdog watches this so it stops emitting
	// "STALL DETECTED" once polling has cleanly stopped — the actual
	// problem at that point is upstream (broker should exit / supervisor
	// should restart it), not a stalled call.
	pollDone chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// primaryBaseFromEnv applies the C3_TELEGRAM_API_URL env override to a config
// primary base: a non-empty (trimmed) env value WINS over the config value
// (env-beats-file, matching the C3_LOG_FILE precedent); an empty/unset env
// leaves the config value untouched. Pure function so the precedence is unit-
// testable without a live bot.
func primaryBaseFromEnv(cfgPrimary string) string {
	if v := strings.TrimSpace(os.Getenv(telegramAPIURLEnv)); v != "" {
		return v
	}
	return cfgPrimary
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

	// Env override for the primary Bot-API base URL (env beats mappings.json,
	// matching the C3_LOG_FILE precedent). Empty/unset leaves cfg.APIBaseURL.
	c.cfg.APIBaseURL = primaryBaseFromEnv(c.cfg.APIBaseURL)

	// Build the ordered, deduped endpoint list. [APIBaseURL-or-"" , ...APIBaseURLs].
	// Both empty ⇒ [""] which means gotgbot's DefaultAPIURL (api.telegram.org) —
	// byte-for-byte today's behavior. Each NON-empty base is validated so a typo
	// can never send the bot token to a bad host. We never log the token or a
	// token-bearing URL; only the active endpoint's host is logged.
	endpoints, err := buildEndpoints(c.cfg.APIBaseURL, c.cfg.APIBaseURLs)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	c.endpoints = endpoints
	c.activeEndpoint.Store(0)
	if active := c.endpoints[0]; active != "" {
		// Log only the host (never the token-bearing path).
		if u, perr := url.Parse(active); perr == nil {
			host.Logf("telegram: using Bot-API endpoint host=%s (%d configured)", u.Host, len(c.endpoints))
		}
	}

	// Custom HTTP transport with explicit network-layer timeouts so a
	// half-open TCP socket (NAT timeout, mid-stream firewall drop) gets
	// surfaced as an error well before a request hangs forever. Defaults
	// in net/http are MaxIdleConns=100, no per-component timeouts —
	// fine for normal use but gives no upper bound on a stuck connection.
	//
	// Sub-agent research (2026-05-09, prior TypeScript bot + grammyjs/runner): the
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
	//
	// APIURL on DefaultRequestOpts is the client-level fallback base (the active
	// endpoint at start). Per-call requestOptsFor sets APIURL too and takes
	// precedence (GetAPIURL: per-call → default → DefaultAPIURL), so failover
	// still works; this just ensures any call that ever forgets a per-call APIURL
	// still honors the configured proxy. "" stays gotgbot's default.
	botClient := &gotgbot.BaseBotClient{
		Client: http.Client{Transport: httpTransport},
		DefaultRequestOpts: &gotgbot.RequestOpts{
			Timeout: 20 * time.Second,
			APIURL:  c.activeEndpointURL(),
		},
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
		RequestOpts: c.requestOptsFor("getMe"),
	})
	if err != nil {
		return fmt.Errorf("telegram: NewBot: %w", err)
	}
	c.bot = bot
	c.host = host
	c.authBrk = newAuthBreaker(auth401Threshold)
	c.health = newFetchHealth()
	c.dedup = newUpdateDedup(2000, 5*time.Minute)
	c.rate = newRateLimiter()
	c.sentPolls = newSentPollMap(2000)
	if store, sErr := newOffsetStore(Name); sErr == nil {
		c.offsets = store
	} else {
		host.Logf("telegram: offset store unavailable (%v); restarts will re-process the last 24h of updates", sErr)
	}
	c.ctx, c.cancel = context.WithCancel(ctx)

	host.Logf("telegram: connected as @%s", bot.Username)

	// The fetch-health machine seeds its lastSuccess to now on construction
	// (see newFetchHealth), so the first ~90s after start don't spuriously
	// trip the silence arm before any GetUpdates has returned.
	c.pollDone = make(chan struct{})

	// Start the long-poll loop in a goroutine. Returns immediately after
	// kicking off — Telegram-side processing is async from the broker's
	// startup path.
	go func() {
		defer close(c.pollDone)
		c.pollLoop()
	}()
	go c.silenceWatchdog()
	go c.heartbeat()
	return nil
}

// reportHealth fires host.NotifyHealth for a transition edge, building the
// channel-neutral HealthEvent from the health machine's snapshot. It is called
// OUTSIDE the machine's lock (the Record*/Check* methods return the edge under
// lock; the caller then invokes this) — we never hold the state mutex across a
// notify fan-out. A healthNoChange transition is a no-op. host.NotifyHealth is
// itself non-blocking (the broker fans out asynchronously). The alert NEVER
// re-enters this channel — it is delivered entirely out-of-band (desktop +
// CLI broadcast + status + log), because Telegram is the dead path.
func (c *Channel) reportHealth(tr healthTransition) {
	if tr == healthNoChange {
		return
	}
	_, consec, since, reason, downFor, lastSuccess := c.health.snapshot()
	ev := c3types.HealthEvent{
		Channel: c.Name(),
		Since:   since,
		Consec:  consec,
		Reason:  reason,
		DownFor: downFor,
	}
	switch tr {
	case healthWentDown:
		ev.State = c3types.HealthStateDown
		c.host.Logf("telegram: FETCH DOWN — cannot reach Telegram to fetch updates (consec=%d, reason=%s). Inbound is offline until this recovers; alerting out-of-band (desktop + CLI + status).",
			consec, reason)
	case healthRecovered:
		ev.State = c3types.HealthStateUp
		c.host.Logf("telegram: FETCH RECOVERED — Telegram reachable again (last success %s).",
			lastSuccess.Format("15:04:05"))
	}
	c.host.NotifyHealth(ev)
}

// silenceWatchdog drives the fetch-health machine's max-silence arm: the
// "silent death" failure mode where HTTP-layer timeouts somehow fail to fire and
// GetUpdates hangs past the long-poll budget, producing neither a success nor a
// fast error. It replaced the old observe-and-log-only stallWatchdog: instead of
// emitting a separate "STALL DETECTED" line (a SECOND competing dead-bot
// signal), it folds the 90s threshold into the ONE health machine via
// CheckSilence and routes any resulting edge through the same reportHealth
// notification path. Patterned after grammyjs/runner's POLL_STALL_THRESHOLD_MS
// (sub-agent research 2026-05-09).
const silenceCheckInterval = 30 * time.Second

func (c *Channel) silenceWatchdog() {
	ticker := time.NewTicker(silenceCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.pollDone:
			// pollLoop exited (e.g., 409 conflict, ctx cancel). The silence
			// concept doesn't apply anymore; stop checking. The broker
			// supervisor (or operator) should restart at this point.
			return
		case <-ticker.C:
			c.reportHealth(c.health.CheckSilence())
		}
	}
}

// heartbeat pings getMe at a fixed interval as an independent liveness
// probe. If the bot is "silently dead" (Telegram-side rotated us off, or
// our token revoked, or our network broke in a way pollLoop hasn't
// surfaced), this catches it within a few minutes regardless of whether
// any users are sending messages.
//
// Single-notification-path change (2026-06-17): the heartbeat no longer keeps
// its OWN consecutive-fail count or emits a separate "HEARTBEAT FAILED" line —
// that was a second competing dead-bot signal that produced false-positive
// spam alongside the poll loop. Instead it feeds the SAME fetch-health machine:
//   - getMe error => health.RecordFailure (a transport-class failure to reach
//     Telegram, EXCEPT a 429, which is the server pushing back — reachable, so
//     it is NOT recorded as down), and
//   - getMe success => health.RecordSuccess (proof Telegram is reachable),
//
// routing any edge through the same reportHealth fan-out.
const heartbeatInterval = 5 * time.Minute

func (c *Channel) heartbeat() {
	// Wait one full interval before the first probe so startup races
	// don't cause spurious early failures.
	timer := time.NewTimer(heartbeatInterval)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-timer.C:
		}
		_, err := c.bot.GetMe(&gotgbot.GetMeOpts{
			RequestOpts: c.requestOptsFor("getMe"),
		})
		if err != nil {
			class, _ := classifyError(err)
			// 429 is a reachable server pushing back — never "down". Every
			// other class (transient/permanent/conflict) means we couldn't
			// complete a control call, which feeds the health machine.
			if class != errClassRateLimited {
				c.host.Logf("telegram: heartbeat getMe failed (class=%s): %v", class, err)
				c.reportHealth(c.health.RecordFailure("heartbeat getMe " + class.String()))
			} else {
				c.host.Logf("telegram: heartbeat getMe 429 rate-limited (reachable; not counted as down): %v", err)
			}
		} else {
			c.reportHealth(c.health.RecordSuccess())
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
