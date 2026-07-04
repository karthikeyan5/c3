package mappings

import "time"

// MappingsFile is the root structure of ~/.config/c3/mappings.json.
//
// Schema reference: docs/specs/2026-05-08-c3-rearch-design.md §4.3.
type MappingsFile struct {
	SchemaVersion int                       `json:"schema_version"`
	Channels      map[string]ChannelConfig  `json:"channels"`
	Codex         *CodexConfig              `json:"codex,omitempty"`
	Mappings      map[string]Mapping        `json:"mappings"`
	Plugins       map[string]map[string]any `json:"plugins,omitempty"`
	Allowlist     *Allowlist                `json:"allowlist,omitempty"`
	Notifications *NotificationsConfig      `json:"notifications,omitempty"`
	// SessionAttachments maps a host CLI's stable session id → the route it
	// was last attached to, powering auto-attach-on-resume. omitempty keeps
	// pre-feature config files byte-identical until the first session attaches.
	SessionAttachments map[string]SessionAttachment `json:"session_attachments,omitempty"`
	// AutoAttachOnResume gates whether a resumed session is automatically
	// re-attached to its last topic (broker.handleRecoverSession). Absent or
	// false ⇒ DISABLED, the v1 default: the SessionStart hook still records the
	// resume handoff, but the broker does not auto-re-claim the last route. Set
	// true to opt in to the full behavior. A plain bool (not *bool) is correct
	// here BECAUSE the desired default is the zero value, false — unlike
	// notifications.invasive / channels.rich_inbound, which default true and so
	// need a pointer to tell "unset" apart from an explicit false. omitempty
	// keeps pre-feature and opted-out config files byte-identical.
	AutoAttachOnResume bool `json:"auto_attach_on_resume,omitempty"`
	// AutoUpdate gates whether the broker installs a newer C3 release ITSELF when
	// its ~6h check finds one (downloads + checksum-verifies + atomic-swaps the
	// binaries, then drains and exits so adapters reconnect onto the new broker).
	// Absent or false ⇒ DISABLED, the v1 default: the always-on "update available"
	// notice (status line + log) STILL fires — only the self-install is opt-in.
	// A plain bool (not *bool) is correct here for the same reason as
	// AutoAttachOnResume: the desired default IS the zero value, false. omitempty
	// keeps pre-feature and opted-out config files byte-identical.
	AutoUpdate bool `json:"auto_update,omitempty"`
}

// NotificationsConfig governs the "invasive" health-notification surfaces
// (desktop popup + CLI turn-injection). The ambient status-line indicator is
// always on and is NOT governed here.
type NotificationsConfig struct {
	// Invasive gates the desktop popup + CLI fallback. nil ⇒ default true.
	// (A plain bool would zero-value to false and silently disable alerts for
	// every user who never set it.)
	Invasive *bool `json:"invasive,omitempty"`
}

// InvasiveNotifications reports whether invasive health notifications (desktop
// popup + CLI fallback) are enabled. Absent config ⇒ true (preserve behavior).
func (mf *MappingsFile) InvasiveNotifications() bool {
	if mf == nil || mf.Notifications == nil || mf.Notifications.Invasive == nil {
		return true
	}
	return *mf.Notifications.Invasive
}

// AutoAttachOnResumeEnabled reports whether auto-attach-on-resume is enabled.
// A nil file or an absent field ⇒ false (the v1 default: the feature is off, so
// a resumed session stays unattached until it attaches explicitly).
func (mf *MappingsFile) AutoAttachOnResumeEnabled() bool {
	return mf != nil && mf.AutoAttachOnResume
}

// AutoUpdateEnabled reports whether broker-driven self-update is enabled. A nil
// file or an absent field ⇒ false (the v1 default: notices fire, but the broker
// does not install updates on its own). SIGHUP-reloadable like other mappings.
func (mf *MappingsFile) AutoUpdateEnabled() bool {
	return mf != nil && mf.AutoUpdate
}

// Allowlist enforces default-deny inbound traffic. The channel layer drops
// any inbound that doesn't match either the user_id set (DM-cleared users)
// or the chat_id set (group-cleared chats). Populated by the pairing flow;
// see broker.PairingState and the /c3:pair slash command.
//
// Per the maintainer's 2026-05-18 design:
//   - DM pairing allowlists the user_id (DM with that user is now cleared).
//   - Group pairing allowlists the chat_id (the whole group is cleared;
//     the user_id who happened to type the code is incidental — we trust
//     the group, not the member).
type Allowlist struct {
	Users  []int64 `json:"users,omitempty"`
	Groups []int64 `json:"groups,omitempty"`
}

// ChannelConfig holds per-channel state. v1 only uses telegram.
type ChannelConfig struct {
	BotToken            string                 `json:"bot_token,omitempty"`
	DefaultGroup        string                 `json:"default_group,omitempty"`
	Groups              map[string]GroupConfig `json:"groups,omitempty"`
	DMChatID            int64                  `json:"dm_chat_id,omitempty"`
	MasterUserID        int64                  `json:"master_user_id,omitempty"`
	Topics              []Topic                `json:"topics,omitempty"`
	DebounceMS          int                    `json:"debounce_ms,omitempty"`
	DebounceMaxMessages int                    `json:"debounce_max_messages,omitempty"`
	FallbackCooldownS   int                    `json:"fallback_cooldown_s,omitempty"`
	STTPrefix           string                 `json:"stt_prefix,omitempty"`
	// APIBaseURL points the channel at a maintainer-owned Bot-API reverse proxy
	// (Telegram's IPs are null-routed in India). Empty = the channel's default
	// (api.telegram.org), unchanged behavior. The C3_TELEGRAM_API_URL env var
	// overrides this. Neutral string — the channel validates it (https://) at
	// start so a typo can never leak the bot token to a bad host.
	APIBaseURL string `json:"api_base_url,omitempty"`
	// APIBaseURLs is an optional ordered failover list appended after APIBaseURL.
	APIBaseURLs []string `json:"api_base_urls,omitempty"`
	// RichInbound gates decoding of inbound Bot API 10.1 rich messages into
	// markdown (telegram channel). nil/absent ⇒ true (enabled). A bare bool
	// would zero-value to false and silently disable decoding for everyone who
	// never set it — the trap documented for notifications.invasive.
	RichInbound *bool `json:"rich_inbound,omitempty"`
}

// GroupConfig identifies a Telegram supergroup the bot can create topics in.
type GroupConfig struct {
	ChatID int64  `json:"chat_id"`
	Title  string `json:"title,omitempty"`
}

// Topic is one entry in the per-channel topic registry.
type Topic struct {
	ChatID  int64  `json:"chat_id"`
	TopicID int64  `json:"topic_id"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
}

// CodexConfig holds Codex-bridge-specific tunables that aren't a channel.
type CodexConfig struct {
	SharedRoot        string `json:"shared_root,omitempty"`
	AppServerMetaPath string `json:"app_server_meta_path,omitempty"`
}

// Mapping is one absolute-cwd-keyed entry.
type Mapping struct {
	Channel        string    `json:"channel"`
	ChatID         int64     `json:"chat_id"`
	TopicID        int64     `json:"topic_id"`
	Name           string    `json:"name"`
	Group          string    `json:"group,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastAttachedAt time.Time `json:"last_attached_at,omitempty"`
}

// SessionAttachment records the last topic a CLI session was attached to,
// keyed by the host CLI's stable session id (Claude: CLAUDE_CODE_SESSION_ID),
// so a resumed session re-attaches automatically regardless of its launch dir.
// TopicID is a pointer so a DM route (no topic) is representable as nil.
// Detached is a tombstone: a deliberate `detach` sets it so the resumed session
// stays unattached until it attaches again (which clears it).
type SessionAttachment struct {
	Channel        string    `json:"channel"`
	ChatID         int64     `json:"chat_id"`
	TopicID        *int64    `json:"topic_id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Group          string    `json:"group,omitempty"`
	CWD            string    `json:"cwd,omitempty"`
	LastAttachedAt time.Time `json:"last_attached_at"`
	Detached       bool      `json:"detached,omitempty"`
}

// Recoverable reports whether this attachment may be auto-restored on resume:
// not tombstoned and within ttl of its last attach.
func (sa SessionAttachment) Recoverable(now time.Time, ttl time.Duration) bool {
	return !sa.Detached && now.Sub(sa.LastAttachedAt) < ttl
}
