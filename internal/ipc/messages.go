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

// TopicEntry is one row in TopicsListMsg.Topics.
type TopicEntry struct {
	Channel   string  `json:"channel"`
	ChatID    int64   `json:"chat_id"`
	TopicID   int64   `json:"topic_id"`
	Name      string  `json:"name"`
	Group     string  `json:"group,omitempty"`
	ClaimedBy *Holder `json:"claimed_by,omitempty"`
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
