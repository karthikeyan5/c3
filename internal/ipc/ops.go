// Package ipc defines the wire types for broker ↔ adapter communication
// over the unix socket at $XDG_RUNTIME_DIR/c3.sock (or /tmp/c3-$UID.sock).
//
// Schema reference: docs/specs/2026-05-08-c3-rearch-design.md §4.4.1.
package ipc

// Op is the op-code present on every IPC message. Adapters and broker
// dispatch on Op.
type Op string

const (
	// adapter → broker
	OpHello           Op = "hello"
	OpServerInfo      Op = "server_info"
	OpToolsList       Op = "tools_list"
	OpAttach          Op = "attach"
	OpRelease         Op = "release"
	OpListTopics      Op = "list_topics"
	OpListClaims      Op = "list_claims"
	OpListHealth      Op = "list_health"
	OpToolCall        Op = "tool_call"
	OpPairModeStart   Op = "pair_mode_start"
	OpPingThisSession Op = "ping_this_session"
	OpListSessions    Op = "list_sessions"
	OpBye             Op = "bye"

	// broker → adapter
	OpHelloAck             Op = "hello_ack"
	OpAttached             Op = "attached"
	OpToolResult           Op = "tool_result"
	OpInbound              Op = "inbound"
	OpTopicsList           Op = "topics_list"
	OpClaimsList           Op = "claims_list"
	OpHealthList           Op = "health_list"
	OpPairModeReply        Op = "pair_mode_reply"
	OpPingThisSessionReply Op = "ping_this_session_reply"
	OpListSessionsReply    Op = "list_sessions_reply"
	OpError                Op = "error"
)
