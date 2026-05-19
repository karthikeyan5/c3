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
}

// Allowlist enforces default-deny inbound traffic. The channel layer drops
// any inbound that doesn't match either the user_id set (DM-cleared users)
// or the chat_id set (group-cleared chats). Populated by the pairing flow;
// see broker.PairingState and the /c3:pair slash command.
//
// Per Karthi's 2026-05-18 design:
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
