package broker

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

const testOperatorUID = int64(42857190)

// permBrokerWithOperator builds a broker whose allowlist clears testOperatorUID
// as a DM operator (the sender-gate set for permission relay), plus a holder
// stub claiming `key` whose conn is the broker side of a pipe. Returns the
// broker, the fakeChannel, and the agent-side conn (the test reads pushed
// verdicts from it).
func permBrokerWithOperator(t *testing.T, key RouteKey) (*Broker, *fakeChannel, *ipc.Conn) {
	t.Helper()
	mf := mfWithTelegram()
	mf.AddAllowedUser(testOperatorUID)
	fc := &fakeChannel{caps: &c3types.Capabilities{Channel: "telegram", InlineKeyboards: true}}
	b := brokerWithChannel(t, mf, fc)

	agentSide, brokerSide := net.Pipe()
	t.Cleanup(func() { _ = agentSide.Close(); _ = brokerSide.Close() })
	agentConn := ipc.NewConn(agentSide)
	stub := b.Stubs.Register("claude", 4242, "/work", ipc.NewConn(brokerSide))
	if _, ok := b.Routes.Claim(key, stub); !ok {
		t.Fatal("claim failed")
	}
	stub.SetRoute(&key)
	return b, fc, agentConn
}

// TestResolvePerm_AllowDeny drives the broker-side permission resolution: a
// registered pendingPerm, tapped by the operator with "perm:allow:<id>",
// resolves with behavior "allow" pushed to the holder conn as an
// OpPermissionVerdict and the message edited + keyboard cleared. An unknown id
// returns false. A second tap (already-resolved) returns false.
func TestResolvePerm_AllowDeny(t *testing.T) {
	key := RouteKey{Channel: "telegram", ChatID: 42, HasTopic: false}
	b, fc, agentConn := permBrokerWithOperator(t, key)
	defer b.Shutdown()

	b.Perms.register(&pendingPerm{requestID: "abcde", route: key, toolName: "Bash", messageID: 77})

	// A non-perm callback must NOT resolve a perm (generic event path proceeds).
	if b.resolvePerm(key, &c3types.CallbackEvent{Data: "ask:abcde:0", Actor: c3types.Sender{UserID: testOperatorUID}}) {
		t.Fatal("a non-perm callback must not resolve a perm")
	}
	// A perm callback for an UNKNOWN id must NOT resolve and must leave our live perm.
	if b.resolvePerm(key, &c3types.CallbackEvent{Data: "perm:allow:zzzzz", Actor: c3types.Sender{UserID: testOperatorUID}}) {
		t.Fatal("a callback for an unknown perm id must not resolve")
	}
	if !b.Perms.has("abcde") {
		t.Fatal("a non-matching callback must leave the pending perm registered")
	}

	// Start the holder reader BEFORE resolving (net.Pipe writes block until read).
	done := make(chan ipc.PermissionVerdictMsg, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			close(done)
			return
		}
		var m ipc.PermissionVerdictMsg
		_ = json.Unmarshal(raw, &m)
		done <- m
	}()

	if !b.resolvePerm(key, &c3types.CallbackEvent{Data: "perm:allow:abcde", MessageID: 77, Actor: c3types.Sender{UserID: testOperatorUID}}) {
		t.Fatal("a matching operator perm tap must resolve (and suppress the generic event)")
	}

	select {
	case m, ok := <-done:
		if !ok {
			t.Fatal("holder conn read failed before an OpPermissionVerdict was pushed")
		}
		if m.Op != ipc.OpPermissionVerdict {
			t.Fatalf("pushed op = %q, want %q", m.Op, ipc.OpPermissionVerdict)
		}
		if m.RequestID != "abcde" {
			t.Fatalf("request id = %q, want abcde", m.RequestID)
		}
		if m.Behavior != "allow" {
			t.Fatalf("behavior = %q, want allow", m.Behavior)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no OpPermissionVerdict pushed to the holder conn")
	}

	if b.Perms.has("abcde") {
		t.Fatal("a resolved perm must be removed from the registry")
	}
	edits := fc.editSnapshot()
	if len(edits) != 1 {
		t.Fatalf("resolve should edit the message exactly once (mark + clear keyboard); got %d", len(edits))
	}
	if edits[0].MessageID != 77 {
		t.Fatalf("edit targeted message %d, want 77", edits[0].MessageID)
	}
	if edits[0].Buttons == nil || len(edits[0].Buttons) != 0 {
		t.Fatalf("resolve must clear the keyboard via a non-nil empty Buttons; got %+v", edits[0].Buttons)
	}
	if !strings.Contains(edits[0].Text, "Allowed") {
		t.Fatalf("resolve edit body should record the verdict; got %q", edits[0].Text)
	}

	// A second (stale) tap for the already-resolved perm must NOT resolve.
	if b.resolvePerm(key, &c3types.CallbackEvent{Data: "perm:allow:abcde", Actor: c3types.Sender{UserID: testOperatorUID}}) {
		t.Fatal("a stale tap for an already-resolved perm must return false")
	}
}

// TestResolvePerm_Deny: a "perm:deny:<id>" operator tap resolves with behavior
// "deny" and records a Denied outcome.
func TestResolvePerm_Deny(t *testing.T) {
	key := RouteKey{Channel: "telegram", ChatID: 42, HasTopic: false}
	b, fc, agentConn := permBrokerWithOperator(t, key)
	defer b.Shutdown()

	b.Perms.register(&pendingPerm{requestID: "bcdef", route: key, toolName: "Write", messageID: 8})

	done := make(chan ipc.PermissionVerdictMsg, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			close(done)
			return
		}
		var m ipc.PermissionVerdictMsg
		_ = json.Unmarshal(raw, &m)
		done <- m
	}()

	if !b.resolvePerm(key, &c3types.CallbackEvent{Data: "perm:deny:bcdef", MessageID: 8, Actor: c3types.Sender{UserID: testOperatorUID}}) {
		t.Fatal("a matching operator deny tap must resolve")
	}
	select {
	case m := <-done:
		if m.Behavior != "deny" {
			t.Fatalf("behavior = %q, want deny", m.Behavior)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no verdict pushed")
	}
	edits := fc.editSnapshot()
	if len(edits) != 1 || !strings.Contains(edits[0].Text, "Denied") {
		t.Fatalf("deny edit body should record Denied; got %+v", edits)
	}
}

// TestResolvePerm_NonOperatorIgnored: a tap from a user who is NOT an
// allowlisted operator must NOT be honored — no verdict pushed, no message
// edited, and the pending perm stays live so the real operator can still
// approve. resolvePerm returns false (the tap is already auto-acked).
func TestResolvePerm_NonOperatorIgnored(t *testing.T) {
	key := RouteKey{Channel: "telegram", ChatID: 42, HasTopic: false}
	b, fc, agentConn := permBrokerWithOperator(t, key)
	defer b.Shutdown()

	b.Perms.register(&pendingPerm{requestID: "cdefg", route: key, toolName: "Bash", messageID: 9})

	// Any unexpected write to the holder conn would block; guard with a reader
	// that records whether anything arrived.
	got := make(chan struct{}, 1)
	go func() {
		if _, err := agentConn.ReadFrame(); err == nil {
			got <- struct{}{}
		}
	}()

	const intruderUID = int64(99999999)
	if b.resolvePerm(key, &c3types.CallbackEvent{Data: "perm:allow:cdefg", MessageID: 9, Actor: c3types.Sender{UserID: intruderUID}}) {
		t.Fatal("a non-operator tap must NOT be honored (resolvePerm must return false)")
	}
	if !b.Perms.has("cdefg") {
		t.Fatal("a non-operator tap must leave the pending perm live for the real operator")
	}
	select {
	case <-got:
		t.Fatal("a non-operator tap must not push a verdict to the holder")
	case <-time.After(150 * time.Millisecond):
	}
	if len(fc.editSnapshot()) != 0 {
		t.Fatal("a non-operator tap must not edit the message")
	}
}

// TestPermRegistry_ExpiresStale drives the perm reaper: a pendingPerm older than
// permExpiryTTL is removed by sweepExpiredPerms, which best-effort clears its
// live keyboard; a fresh perm survives.
func TestPermRegistry_ExpiresStale(t *testing.T) {
	key := RouteKey{Channel: "telegram", ChatID: 42, HasTopic: false}
	mf := mfWithTelegram()
	fc := &fakeChannel{caps: &c3types.Capabilities{Channel: "telegram", InlineKeyboards: true}}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	stale := &pendingPerm{
		requestID: "stale", route: key, toolName: "Bash", messageID: 55,
		createdAt: time.Now().Add(-permExpiryTTL - time.Minute),
	}
	if !b.registerPerm(stale) {
		t.Fatal("register stale failed")
	}
	fresh := &pendingPerm{
		requestID: "fresh", route: key, toolName: "Read", messageID: 66, createdAt: time.Now(),
	}
	if !b.registerPerm(fresh) {
		t.Fatal("register fresh failed")
	}

	b.sweepExpiredPerms()

	if b.Perms.has("stale") {
		t.Fatal("a stale perm must be removed by the sweep")
	}
	if !b.Perms.has("fresh") {
		t.Fatal("a fresh perm must survive the sweep")
	}
	edits := fc.editSnapshot()
	if len(edits) != 1 {
		t.Fatalf("sweep must clear exactly the stale perm's keyboard (1 edit); got %d", len(edits))
	}
	if edits[0].MessageID != 55 {
		t.Fatalf("sweep edit targeted msg %d, want 55", edits[0].MessageID)
	}
	if edits[0].Buttons == nil || len(edits[0].Buttons) != 0 {
		t.Fatalf("sweep must clear the keyboard via a non-nil empty Buttons; got %+v", edits[0].Buttons)
	}
}

// TestHandlePermissionRequest_SendsKeyboard drives the broker handler: a valid
// OpPermissionRequest on a claimed route registers a pendingPerm (before send),
// sends the Allow/Deny keyboard, and stores the sent message id. No ack frame is
// written (fire-and-forget — there is no blocking tool to unblock).
func TestHandlePermissionRequest_SendsKeyboard(t *testing.T) {
	key := RouteKey{Channel: "telegram", ChatID: 42, HasTopic: false}
	mf := mfWithTelegram()
	fc := &fakeChannel{caps: &c3types.Capabilities{Channel: "telegram", InlineKeyboards: true}}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	stub := b.Stubs.Register("claude", 1, "/work", nil)
	if _, ok := b.Routes.Claim(key, stub); !ok {
		t.Fatal("claim failed")
	}
	stub.SetRoute(&key)

	raw := mustMarshalJSON(t, ipc.PermissionReq{
		Op: ipc.OpPermissionRequest, RequestID: "abcde", ToolName: "Bash", Preview: "rm -rf /tmp/x",
	})
	b.handlePermissionRequest(nil, stub, raw)

	if !b.Perms.has("abcde") {
		t.Fatal("a valid permission_request must register a pending perm")
	}
	replies := fc.sendRepliesSnapshot()
	if len(replies) != 1 {
		t.Fatalf("handler must send exactly one keyboard message; got %d", len(replies))
	}
	if !strings.Contains(replies[0].Text, "Bash") {
		t.Fatalf("prompt text should name the tool; got %q", replies[0].Text)
	}
	if len(replies[0].Buttons) == 0 {
		t.Fatal("handler must attach the Allow/Deny inline keyboard")
	}
	// Flatten and assert both verbs are present in callback data.
	var datas []string
	for _, row := range replies[0].Buttons {
		for _, btn := range row {
			datas = append(datas, btn.Data)
		}
	}
	joined := strings.Join(datas, " ")
	if !strings.Contains(joined, "perm:allow:abcde") || !strings.Contains(joined, "perm:deny:abcde") {
		t.Fatalf("keyboard must carry perm:allow / perm:deny callbacks; got %v", datas)
	}
}

// TestHandlePermissionRequest_NoRouteDrops: a permission_request with no claimed
// route is dropped (logged) without registering or sending — there is no blocking
// tool to error.
func TestHandlePermissionRequest_NoRouteDrops(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{caps: &c3types.Capabilities{Channel: "telegram", InlineKeyboards: true}}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	stub := b.Stubs.Register("claude", 1, "/work", nil) // no route claimed
	raw := mustMarshalJSON(t, ipc.PermissionReq{
		Op: ipc.OpPermissionRequest, RequestID: "abcde", ToolName: "Bash",
	})
	b.handlePermissionRequest(nil, stub, raw)

	if b.Perms.has("abcde") {
		t.Fatal("a routeless permission_request must not register a pending perm")
	}
	if len(fc.sendRepliesSnapshot()) != 0 {
		t.Fatal("a routeless permission_request must not send anything")
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestParsePermCallback covers the callback_data parser.
func TestParsePermCallback(t *testing.T) {
	cases := []struct {
		data     string
		wantID   string
		wantBeh  string
		wantOK   bool
	}{
		{"perm:allow:abcde", "abcde", "allow", true},
		{"perm:deny:abcde", "abcde", "deny", true},
		{"ask:abcde:0", "", "", false},
		{"perm:allow:", "", "", false},
		{"perm:bogus:abcde", "", "", false},
		{"perm:allow", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		id, beh, ok := parsePermCallback(c.data)
		if id != c.wantID || beh != c.wantBeh || ok != c.wantOK {
			t.Errorf("parsePermCallback(%q) = (%q,%q,%v); want (%q,%q,%v)",
				c.data, id, beh, ok, c.wantID, c.wantBeh, c.wantOK)
		}
	}
}
