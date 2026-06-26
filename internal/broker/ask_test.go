package broker

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// TestResolveAsk_SingleSelect drives the broker-side resolution path: a
// registered pendingAsk, fed a callback "ask:<id>:1", resolves with options[1]
// and pushes an OpAskResult to the route holder's conn; a non-matching callback
// data does NOT resolve (so the generic event path proceeds); and a stale tap
// for an already-resolved ask returns false.
func TestResolveAsk_SingleSelect(t *testing.T) {
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mfWithTelegram(), fc)
	defer b.Shutdown()

	// A holder stub whose conn is the broker side of a pipe; the test reads the
	// pushed OpAskResult from the agent side.
	agentSide, brokerSide := net.Pipe()
	t.Cleanup(func() { _ = agentSide.Close(); _ = brokerSide.Close() })
	agentConn := ipc.NewConn(agentSide)
	stub := b.Stubs.Register("claude", 4242, "/work", ipc.NewConn(brokerSide))

	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 5}
	if _, ok := b.Routes.Claim(key, stub); !ok {
		t.Fatal("claim failed")
	}
	stub.SetRoute(&key)

	options := []string{"A", "B", "C"}
	b.Asks.register(&pendingAsk{askID: "abc12345", route: key, question: "Pick one", options: options, messageID: 77})

	// A non-ask callback must NOT resolve — the generic event path must proceed.
	if b.resolveAsk(key, &c3types.CallbackEvent{Data: "vote:abc12345:1"}) {
		t.Fatal("a non-ask callback must not resolve an ask (generic event must proceed)")
	}
	// An ask callback for an UNKNOWN id must NOT resolve, and must leave our live
	// ask untouched.
	if b.resolveAsk(key, &c3types.CallbackEvent{Data: "ask:deadbeef:0"}) {
		t.Fatal("a callback for an unknown ask id must not resolve")
	}
	if !b.Asks.has("abc12345") {
		t.Fatal("a non-matching callback must leave the pending ask registered")
	}

	// Start the holder-side reader BEFORE resolving (net.Pipe writes block until read).
	done := make(chan ipc.AskResultMsg, 1)
	go func() {
		raw, err := agentConn.ReadFrame()
		if err != nil {
			close(done)
			return
		}
		var m ipc.AskResultMsg
		_ = json.Unmarshal(raw, &m)
		done <- m
	}()

	// The matching tap resolves with options[1] == "B" and suppresses the event.
	if !b.resolveAsk(key, &c3types.CallbackEvent{Data: "ask:abc12345:1", MessageID: 77}) {
		t.Fatal("a matching ask callback must resolve (and suppress the generic event)")
	}

	select {
	case m, ok := <-done:
		if !ok {
			t.Fatal("holder conn read failed before an OpAskResult was pushed")
		}
		if m.Op != ipc.OpAskResult {
			t.Fatalf("pushed op = %q, want %q", m.Op, ipc.OpAskResult)
		}
		if m.AskID != "abc12345" {
			t.Fatalf("ask id = %q, want abc12345", m.AskID)
		}
		if len(m.Answer.Selected) != 1 || m.Answer.Selected[0] != "B" {
			t.Fatalf("resolved answer = %+v, want Selected=[B]", m.Answer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no OpAskResult pushed to the holder conn")
	}

	// The resolved ask is removed; the message was edited (keyboard cleared).
	if b.Asks.has("abc12345") {
		t.Fatal("a resolved ask must be removed from the registry")
	}
	if got := fc.editSnapshot(); len(got) != 1 {
		t.Fatalf("resolve should edit the message exactly once (mark + clear keyboard); got %d edits", len(got))
	} else if got[0].MessageID != 77 {
		t.Fatalf("edit targeted message %d, want 77", got[0].MessageID)
	} else if got[0].Buttons == nil {
		t.Fatal("resolve must clear the keyboard by passing a non-nil (empty) Buttons to EditMessage")
	}

	// A second (stale) tap for the same, already-resolved ask must NOT resolve.
	if b.resolveAsk(key, &c3types.CallbackEvent{Data: "ask:abc12345:0"}) {
		t.Fatal("a stale tap for an already-resolved ask must return false (generic path proceeds)")
	}
}

// TestAskKeyboard_RoundTripsThroughParse pins the broker's keyboard builder
// against its own parser: every button askKeyboard produces must parse back to
// the right (askID, idx), and at the max single-select option count each
// callback_data stays well within Telegram's 64-byte ceiling.
func TestAskKeyboard_RoundTripsThroughParse(t *testing.T) {
	const askID = "abcd2345" // 8-char base32, no colon
	n := 100
	options := make([]string, n)
	for i := range options {
		options[i] = "opt"
	}
	rows := askKeyboard(askID, options)
	if len(rows) != n {
		t.Fatalf("askKeyboard produced %d rows, want %d (one button per row)", len(rows), n)
	}
	for i, row := range rows {
		if len(row) != 1 {
			t.Fatalf("row %d has %d buttons, want 1 (single-select)", i, len(row))
		}
		data := row[0].Data
		if len(data) > 64 {
			t.Fatalf("callback_data %q is %d bytes, over Telegram's 64-byte cap", data, len(data))
		}
		gotID, gotIdx, ok := parseAskData(data)
		if !ok {
			t.Fatalf("parseAskData(%q) failed to parse a callback we generated", data)
		}
		if gotID != askID || gotIdx != i {
			t.Fatalf("round-trip mismatch for %q: got (%q,%d) want (%q,%d)", data, gotID, gotIdx, askID, i)
		}
	}
}
