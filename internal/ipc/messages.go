package ipc

import (
	"encoding/json"
	"fmt"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// InboundMsg is the broker → adapter push for a single normalized inbound
// message. The adapter translates this into its host CLI's notification
// dialect (Claude Code: notifications/claude/channel; Codex: log + inbox).
type InboundMsg struct {
	Op      Op              `json:"op"` // = OpInbound
	Inbound c3types.Inbound `json:"inbound"`
}

// ToolCallReq is the adapter → broker forward of an MCP tool call. The broker
// dispatches to the channel module identified by Inbound.Channel-or-claim.
type ToolCallReq struct {
	Op   Op             `json:"op"` // = OpToolCall
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// ToolResultMsg is the broker → adapter response to a ToolCallReq.
type ToolResultMsg struct {
	Op     Op             `json:"op"` // = OpToolResult
	ID     string         `json:"id"`
	Result map[string]any `json:"result,omitempty"`
	Error  *ErrorPayload  `json:"error,omitempty"`
}

// ErrorPayload carries a tool call's error.
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HelloMsg is sent by the adapter on connect.
type HelloMsg struct {
	Op           Op       `json:"op"` // = OpHello
	CLI          string   `json:"cli"`
	PID          int      `json:"pid"`
	CWD          string   `json:"cwd"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// HelloAckMsg is the broker's response to HelloMsg.
type HelloAckMsg struct {
	Op           Op       `json:"op"` // = OpHelloAck
	ConnID       uint64   `json:"conn_id"`
	AutoAttached bool     `json:"auto_attached"`
	Mapping      *Mapping `json:"mapping,omitempty"`
	ClaimHolder  *Holder  `json:"claim_holder,omitempty"`
	NoConfig     bool     `json:"no_config,omitempty"`
	NoMapping    bool     `json:"no_mapping,omitempty"`
}

// Holder identifies a claim holder for diagnostic responses.
type Holder struct {
	CLI string `json:"cli"`
	PID int    `json:"pid"`
	CWD string `json:"cwd"`
}

// Mapping is the wire-shape mirror of mappings.Mapping (avoiding a circular
// import). Populated by the broker on hello_ack when an auto-attach is
// possible.
type Mapping struct {
	Channel string `json:"channel"`
	ChatID  int64  `json:"chat_id"`
	TopicID *int64 `json:"topic_id,omitempty"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
}

// ReleaseReq is sent by the adapter to drop its claim without disconnecting.
type ReleaseReq struct {
	Op Op `json:"op"` // = OpRelease
}

// ByeReq is sent by the adapter for clean disconnect.
type ByeReq struct {
	Op Op `json:"op"` // = OpBye
}

// ListTopicsReq is sent by the adapter to fetch the topics registry.
type ListTopicsReq struct {
	Op Op `json:"op"` // = OpListTopics
}

// TopicsListMsg is the broker's response to ListTopicsReq.
type TopicsListMsg struct {
	Op     Op           `json:"op"` // = OpTopicsList
	Topics []TopicEntry `json:"topics"`
}

// ListClaimsReq is sent by a status-style client (CLI subcommand or any
// adapter) to fetch a snapshot of the broker's live route-claim table.
type ListClaimsReq struct {
	Op Op `json:"op"` // = OpListClaims
}

// ClaimsListMsg is the broker's response to ListClaimsReq. Each entry is a
// (route, holder) pair as the broker currently sees it. Renders into
// `c3-broker status` output for the operator.
type ClaimsListMsg struct {
	Op     Op           `json:"op"` // = OpClaimsList
	Claims []ClaimEntry `json:"claims"`
}

// ClaimEntry is one row of ClaimsListMsg.Claims. Includes the topic name
// when the route corresponds to a known topic in mappings.json (lookup is
// best-effort; empty when the route is a DM or a yet-unregistered topic).
type ClaimEntry struct {
	Channel    string `json:"channel"`
	ChatID     int64  `json:"chat_id"`
	HasTopic   bool   `json:"has_topic"`
	TopicID    int64  `json:"topic_id,omitempty"`
	TopicName  string `json:"topic_name,omitempty"`
	GroupName  string `json:"group_name,omitempty"`
	HolderCLI  string `json:"holder_cli"`
	HolderPID  int    `json:"holder_pid"`
	HolderCWD  string `json:"holder_cwd,omitempty"`
	ConnID     uint64 `json:"conn_id"`
	Connected  bool   `json:"connected"`
}

// TopicEntry is one row in TopicsListMsg.Topics. Also reused by Proposal.Existing
// to describe a found-but-not-claimed topic (in which case ClaimedBy is nil).
type TopicEntry struct {
	Channel   string  `json:"channel"`
	ChatID    int64   `json:"chat_id"`
	TopicID   int64   `json:"topic_id"`
	Name      string  `json:"name"`
	Group     string  `json:"group,omitempty"`
	ClaimedBy *Holder `json:"claimed_by,omitempty"`
}

// AttachReq is the adapter → broker attach request. Spec §4.4.1.
//
// Two ways to specify the target:
//
//  1. Structured: any of Name / Target / TopicID (with optional Group/Channel).
//  2. Freeform: Expr — a single user-supplied string the broker parses.
//     Lets every CLI's slash-command wrapper be a one-liner —
//     `attach(expr=$ARGUMENTS)` — instead of duplicating arg-parsing logic.
//     Parsing rules in the broker:
//       ""                  → fall back to cwd-saved mapping
//       "dm" (any case)     → target=dm (with disambiguation if a topic
//                              named "dm" also exists)
//       "<int>"             → topic_id=<int>
//       "<name>" / "create <name>" / "-y <name>"
//                           → name=<name> (create=true if prefix used)
type AttachReq struct {
	Op      Op     `json:"op"` // = OpAttach
	CWD     string `json:"cwd,omitempty"`
	Name    string `json:"name,omitempty"`
	Target  string `json:"target,omitempty"`
	TopicID *int64 `json:"topic_id,omitempty"`
	Group   string `json:"group,omitempty"`
	Channel string `json:"channel,omitempty"`
	Create  bool   `json:"create,omitempty"`
	Expr    string `json:"expr,omitempty"`

	// Steal: when true, evict any existing alive holder of the target
	// route before claiming. Used in the force_steal proposal flow —
	// LLM asks the user for confirmation, then re-invokes with steal=true.
	// The "broker is authority" principle (claims survive PID-alive) is
	// preserved: only an explicit user-confirmed steal can displace a live
	// holder.
	Steal bool `json:"steal,omitempty"`

	// Replay: set true by the adapter when this attach is being re-sent
	// after a broker reconnect (see replayLastAttach in
	// cmd/c3-claude-adapter/main.go). The broker uses this to suppress
	// the on-attach welcome message — a broker bounce or transient
	// disconnect shouldn't surface as "👋 Hi! I'm connected…" since the
	// user didn't initiate anything. User-typed attach calls leave this
	// false (default) and get the friendly welcome.
	Replay bool `json:"replay,omitempty"`

	// Confirm carries the prior proposal for sibling-stub race detection
	// (spec §4.4.1). Optional; v1 broker doesn't yet validate it but the
	// field is plumbed for forward-compat.
	Confirm *Proposal `json:"confirm,omitempty"`
}

// AttachedMsg is the broker → adapter response.
type AttachedMsg struct {
	Op                Op        `json:"op"` // = OpAttached
	OK                bool      `json:"ok"`
	Channel           string    `json:"channel,omitempty"`
	ChatID            int64     `json:"chat_id,omitempty"`
	TopicID           *int64    `json:"topic_id,omitempty"`
	Name              string    `json:"name,omitempty"`
	Group             string    `json:"group,omitempty"`
	NeedsConfirmation bool      `json:"needs_confirmation,omitempty"`
	Proposal          *Proposal `json:"proposal,omitempty"`
	Err               string    `json:"err,omitempty"`
}

// Proposal describes what the broker would do if the agent confirms.
// Action is one of:
//   - "create" — create a new topic with the given Name in Group
//   - "use_existing_other_group" — adopt Existing topic from a different group
//   - "claim_existing" — claim the Existing topic (same group)
//   - "disambiguate_dm" — a topic named "dm" exists; agent asks user
//     whether they meant the topic or the actual Telegram DM (Existing
//     describes the topic; pass target="dm" to confirm DM, or topic_id to
//     confirm topic).
//   - "force_steal" — the requested route is held by Holder; agent asks
//     user for confirmation, then re-invokes attach with steal=true to
//     evict and claim.
type Proposal struct {
	Action      string      `json:"action"`
	Channel     string      `json:"channel"`
	Group       string      `json:"group"`
	Name        string      `json:"name"`
	Existing    *TopicEntry `json:"existing,omitempty"`    // populated for use_existing_* / disambiguate_dm
	Alternative *Proposal   `json:"alternative,omitempty"` // recursion: e.g. "or create new in default group"
	Holder      *Holder     `json:"holder,omitempty"`      // populated for force_steal
}

// ErrorMsg is sent by either side on an unrecoverable error.
type ErrorMsg struct {
	Op  Op     `json:"op"` // = OpError
	Err string `json:"err"`
}

// PeekOp parses the "op" field from a raw JSON envelope without unmarshaling
// the full payload. Used by the dispatcher to route to the right handler.
func PeekOp(raw []byte) (Op, error) {
	var env struct {
		Op Op `json:"op"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("ipc: parse envelope: %w", err)
	}
	if env.Op == "" {
		return "", fmt.Errorf("ipc: missing op field")
	}
	return env.Op, nil
}
