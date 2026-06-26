// Package c3types holds wire-shaped Go types shared by channels, plugins,
// and the broker. Spec §4.1.
package c3types

import "time"

// Inbound is one message received from a Channel, post-debounce and
// post-plugin-pipeline, on its way to a CLI adapter. The broker emits
// this as ipc.OpInbound when a stub holds the route claim.
//
// TopicID semantics for Telegram: nil = DM (no topic), &1 = General
// (Telegram's reserved root topic), and any positive int64 > 1 = a
// custom forum topic.
//
// ChatID sign convention follows Telegram's Bot API: positive = user
// or DM, negative = group/channel, -100… = supergroup/channel.
type Inbound struct {
	Channel     string
	ChatID      int64
	TopicID     *int64 // nil = no topic, &1 = General, >1 = custom
	MessageID   int64
	Sender      Sender
	Text        string
	Attachments []Attachment
	ReplyTo     *ReplyContext
	Timestamp   time.Time

	// Kind classifies this inbound. The zero value ("") is an ordinary text/
	// media message — every pre-existing caller and the whole delivery path are
	// unchanged when Kind is unset (back-compat). A non-empty Kind marks a
	// synthesized channel EVENT (poll result, reaction, callback) whose payload
	// rides in Event. The route worker treats a Kind != "" inbound specially:
	// it flushes ALONE (never merged into a text debounce batch) and BYPASSES
	// voice/STT handling (CB-1). See internal/broker/worker.go.
	Kind InboundKind `json:",omitempty"`
	// Event carries the channel-neutral payload for a non-message Kind. Nil for
	// an ordinary message.
	Event *InboundEvent `json:",omitempty"`
}

// IsEvent reports whether this inbound is a synthesized channel event (poll
// result / reaction / callback) rather than an ordinary text/media message.
// The route worker uses this to keep events out of the text-debounce/STT path.
func (in *Inbound) IsEvent() bool {
	return in != nil && in.Kind != InboundMessage
}

// InboundKind classifies an Inbound. The zero value is an ordinary message; the
// other kinds mark synthesized channel events surfaced to the agent. All values
// are channel-neutral — no Telegram identifier leaks into this type.
type InboundKind string

const (
	// InboundMessage is the zero value: an ordinary text/media message. Existing
	// callers that never set Kind produce exactly this, so delivery is unchanged.
	InboundMessage InboundKind = ""
	// InboundPollResult is an aggregate poll tally (counts per option, total
	// voters, is_closed). Surfaced on poll close / stop_poll, never per-voter.
	InboundPollResult InboundKind = "poll_result"
	// InboundReaction is a change of reactions on a message (added/removed set).
	InboundReaction InboundKind = "reaction"
	// InboundCallback is an inline-keyboard button press (already auto-acked by
	// the channel before this is surfaced).
	InboundCallback InboundKind = "callback"
	// InboundSystem is a broker-originated system advisory (NOT user input):
	// e.g. a channel-health alert broadcast to every live CLI session. It is
	// trusted (broker-sourced) and therefore bypasses the inbound allowlist
	// gate — see broker.broadcastSystemEvent. The payload rides in
	// InboundEvent.System.
	InboundSystem InboundKind = "system"
)

// InboundEvent is the channel-neutral payload for a non-message Inbound. Exactly
// one field is set, matching the owning Inbound.Kind. No Telegram/gotgbot types
// appear here — the channel converts its wire shapes into these neutral structs.
type InboundEvent struct {
	PollResult *PollResult    `json:",omitempty"`
	Reaction   *ReactionEvent `json:",omitempty"`
	Callback   *CallbackEvent `json:",omitempty"`
	System     *SystemEvent   `json:",omitempty"`
}

// SystemEvent is a broker-originated advisory surfaced to the agent (Inbound.Kind
// == InboundSystem). It carries NO user content — it is an operational signal
// (e.g. "the Telegram fetch is DOWN, your phone messages won't arrive"). It is
// channel-neutral: Source names which channel/subsystem raised it as a plain
// string; no Telegram/gotgbot type appears here. Level is a coarse severity the
// adapters can render ("warn"/"info"). Title is a short headline; Message is the
// one-line detail.
type SystemEvent struct {
	Source  string // e.g. "telegram" — the channel/subsystem that raised this
	Level   string // "warn" | "info"
	Title   string
	Message string
}

// HealthState is the coarse fetch-health state of a channel's inbound path.
type HealthState string

const (
	// HealthStateUp means the channel's inbound fetch is healthy (recently
	// succeeded).
	HealthStateUp HealthState = "up"
	// HealthStateDown means the channel cannot reach its upstream to fetch
	// inbound — phone messages will not arrive until it recovers.
	HealthStateDown HealthState = "down"
)

// HealthEvent is a channel-neutral fetch-health transition the broker fans out
// to its out-of-band sinks (desktop notify, CLI broadcast, status line, log).
// It is emitted EXACTLY on an edge (UP→DOWN / DOWN→UP), never per attempt, so a
// consumer sees two loud signals per outage cycle. No Telegram/gotgbot type
// appears here — the channel translates its transport state into this neutral
// shape.
type HealthEvent struct {
	Channel string      // e.g. "telegram"
	State   HealthState // "up" | "down"
	Since   time.Time   // when the channel entered this state
	Consec  int         // consecutive transport failures (for a DOWN edge)
	Reason  string      // short human cause, e.g. "dial failures" / "timeout"
	DownFor time.Duration
}

// PollResult is an aggregate poll tally. Q-RESULT-1 = AGGREGATE + FINAL-ON-CLOSE:
// it surfaces counts per option + total voters + is_closed, NEVER per-individual-
// voter identity.
type PollResult struct {
	PollID      string
	Question    string
	TotalVoters int
	IsClosed    bool
	Options     []PollOptionTally
}

// PollOptionTally is one option's vote count in a PollResult.
type PollOptionTally struct {
	Text       string
	VoterCount int
}

// ReactionEvent is a change of reactions on a single message. Added/Removed are
// the set-difference of the new vs old reaction lists, rendered as display
// strings (standard emoji verbatim; custom/paid reactions as the sentinels
// "[custom]"/"[paid]" so the agent sees that SOMETHING reacted, never silently
// dropped).
type ReactionEvent struct {
	MessageID int64
	Actor     Sender
	Added     []string
	Removed   []string
}

// CallbackEvent is an inline-keyboard button press. CallbackID is the id the
// channel needs to answerCallbackQuery (it auto-acks before surfacing this).
// Data is the opaque callback payload string attached to the button.
type CallbackEvent struct {
	CallbackID string
	MessageID  int64
	Actor      Sender
	Data       string
}

// Sender identifies the originator of an Inbound or ReplyContext. UserID
// is the channel's canonical user identifier; Username is a display-only
// secondary that may be empty (e.g. a user without a Telegram @handle).
type Sender struct {
	UserID   int64
	Username string
}

// Attachment is one piece of attached media on an Inbound. Channel
// implementations fill the fields they can; absent metadata is the
// zero value. Kind is an open enum on Telegram terms.
type Attachment struct {
	Kind   string // "voice", "audio", "video", "video_note", "document", "photo", "sticker"
	FileID string
	Size   int64
	MIME   string
	Name   string
}

// ReplyContext describes the message an Inbound is replying to (quote-
// reply). MessageID is the target message's id; User is its author;
// Text is a snippet of the target's content for context (may be empty
// or truncated by the channel).
type ReplyContext struct {
	MessageID int64
	User      Sender
	Text      string
}

// Outbound is one tool-call from a CLI adapter to a Channel for delivery.
// Used for both the `reply` tool and as the base shape for derived
// args (see ReplyArgs alias).
//
// Outbound is channel-neutral: Markup is the markup intent (none/markdown/
// native) the channel renders for; Media is the channel-neutral media list;
// Poll is an optional outbound poll. No Telegram-specific identifier (e.g.
// "HTML"/"MarkdownV2" parse modes, file paths) leaks into this shape — the
// channel translates Markup/Media/Poll into its own dialect.
type Outbound struct {
	Channel string
	ChatID  int64
	TopicID *int64
	Text    string
	Markup  Markup
	Media   []MediaItem
	Poll    *PollSpec
	ReplyTo *int64

	// Buttons is an optional inline keyboard attached to the message: rows of
	// Buttons (so [][]Button is rows-of-buttons). The zero value (nil/empty)
	// means NO keyboard — a message without buttons is byte-identical to today,
	// so this is fully back-compat. A channel that does not advertise inline-
	// keyboard support degrades by dropping the keyboard (with a note); the
	// channel that does support it translates these neutral Buttons into its own
	// wire markup. No channel-specific limit (e.g. callback-data byte ceiling)
	// is encoded here — those are enforced in the channel implementation.
	Buttons [][]Button `json:",omitempty"`
}

// Button is one channel-neutral inline-keyboard button. Text is the visible
// label. EXACTLY ONE of Data or URL is set:
//   - Data is an opaque callback payload (callback_data): tapping the button
//     comes back to the agent as an InboundCallback event carrying this string,
//     so the agent can act on it (e.g. SSHGate-style approve/deny). Keep it
//     short — channels cap callback payloads (Telegram: 1-64 bytes), enforced in
//     the channel.
//   - URL is a link the button opens; tapping it does NOT come back to the agent.
//
// A Button with neither (or both) of Data/URL is invalid; the reply-tool parser
// and the channel reject it with a clear error rather than silently dropping it.
type Button struct {
	Text string
	Data string `json:",omitempty"`
	URL  string `json:",omitempty"`
}

// ReplyArgs is the argument shape for the `reply` MCP tool. Aliased to
// Outbound because the wire shape is the same — channels accept the
// same struct for both internal forwarding and adapter-initiated
// replies.
type ReplyArgs = Outbound

// EditArgs is the argument shape for the `edit_message` MCP tool.
// MessageID identifies the message to edit; Text is the new content;
// Markup is the channel-neutral markup intent (none/markdown/native),
// the same convention as Outbound — so edits join the markup system.
type EditArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Text      string
	Markup    Markup

	// Buttons replaces the message's inline keyboard (same rows-of-buttons shape
	// as Outbound.Buttons). Semantics:
	//   - nil  → leave the existing keyboard UNTOUCHED (back-compat: edit_progress
	//     / placeholder edits never carry buttons and must not strip one).
	//   - non-nil EMPTY (`[][]Button{}`) → CLEAR the keyboard (Telegram removes it
	//     when sent an empty inline_keyboard). This is how the `ask` round-trip
	//     drops the buttons once the question is answered.
	//   - non-empty → set that keyboard.
	// A channel without inline-keyboard support ignores this field.
	Buttons [][]Button `json:",omitempty"`
}

// EditResult is returned from Channel.EditMessage. MessageID echoes
// back the edited message's id (channels may rewrite it on edit; today
// Telegram doesn't, but the contract leaves room).
type EditResult struct {
	MessageID int64
}

// ReactArgs is the argument shape for the `react` MCP tool. Emoji must
// be a single-codepoint Telegram-supported reaction emoji; non-emoji
// values are rejected by the channel.
type ReactArgs struct {
	Channel   string
	ChatID    int64
	MessageID int64
	Emoji     string
}

// VoicePayload is the input the plugin host passes to OnVoiceReceived
// callbacks (the STT plugin's entry point). FileID identifies the
// voice attachment on the source channel; MIME and Size are hints
// for choosing the right transcription provider.
type VoicePayload struct {
	Channel   string
	ChatID    int64
	TopicID   *int64
	MessageID int64
	FileID    string
	MIME      string
	Size      int64
}
