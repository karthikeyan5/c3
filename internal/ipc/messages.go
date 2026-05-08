package ipc

import (
	"encoding/json"
	"fmt"
)

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
