package telegram

import (
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// makeChannelWithPolls builds a test Channel with a fakeHost and an initialized
// sentPolls map (the poll-update path needs it).
func makeChannelWithPolls(host channel.Host) *Channel {
	c := makeChannel(host)
	c.sentPolls = newSentPollMap(16)
	return c
}

// TestDispatchPollUpdate_ClosedEmitsStampedResult covers the headline read path
// + CB-2: a CLOSED aggregate poll update for a poll we sent is converted into a
// poll_result Inbound stamped with the route owner (so it passes the DM gate)
// and emitted.
func TestDispatchPollUpdate_ClosedEmitsStampedResult(t *testing.T) {
	const owner = int64(555)
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h)
	// Pretend we sent this poll on a DM route (ChatID == owner's user id).
	c.sentPolls.Put("p1", &sentPoll{ChatID: owner, MessageID: 42, OwnerUserID: owner})

	poll := &gotgbot.Poll{
		Id: "p1", Question: "Lunch?", TotalVoterCount: 3, IsClosed: true,
		Options: []gotgbot.PollOption{
			{Text: "Pizza", VoterCount: 2},
			{Text: "Tacos", VoterCount: 1},
		},
	}
	c.dispatchPollUpdate(7, poll)

	if h.emitCount() != 1 {
		t.Fatalf("a closed poll update should emit exactly 1 poll_result; got %d", h.emitCount())
	}
	in := h.emitted[0]
	if in.Kind != c3types.InboundPollResult {
		t.Fatalf("wrong kind: %q", in.Kind)
	}
	if in.Sender.UserID != owner {
		t.Errorf("CB-2: poll_result must be stamped with the route owner %d; got %d", owner, in.Sender.UserID)
	}
	if in.ChatID != owner || in.MessageID != 42 {
		t.Errorf("poll_result not routed to the original route: chat=%d msg=%d", in.ChatID, in.MessageID)
	}
	pr := in.Event.PollResult
	if pr == nil || pr.TotalVoters != 3 || !pr.IsClosed || len(pr.Options) != 2 {
		t.Fatalf("poll_result tally wrong: %+v", pr)
	}
	if pr.Options[0].Text != "Pizza" || pr.Options[0].VoterCount != 2 {
		t.Errorf("option tally wrong: %+v", pr.Options)
	}
}

// TestDispatchPollUpdate_OpenNotSurfaced asserts the FINAL-ON-CLOSE rule: an
// OPEN aggregate update is retained (latest tally) but NOT surfaced to the agent
// (no per-vote spam).
func TestDispatchPollUpdate_OpenNotSurfaced(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h)
	c.sentPolls.Put("p1", &sentPoll{ChatID: 555, MessageID: 42, OwnerUserID: 555})

	poll := &gotgbot.Poll{Id: "p1", Question: "Q", TotalVoterCount: 1, IsClosed: false,
		Options: []gotgbot.PollOption{{Text: "A", VoterCount: 1}, {Text: "B", VoterCount: 0}}}
	c.dispatchPollUpdate(7, poll)

	if h.emitCount() != 0 {
		t.Fatalf("an OPEN poll update must NOT be surfaced (final-on-close); got %d emits", h.emitCount())
	}
	// But the latest tally is retained for an on-demand read.
	sp, ok := c.sentPolls.Get("p1")
	if !ok || sp.LatestTally == nil || sp.LatestTally.TotalVoters != 1 {
		t.Errorf("open-poll tally should be retained; got %+v", sp)
	}
}

// TestDispatchPollUpdate_UnknownPollDropped asserts a poll we never sent (absent
// from sentPolls) is dropped (nothing to route).
func TestDispatchPollUpdate_UnknownPollDropped(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h)
	c.dispatchPollUpdate(7, &gotgbot.Poll{Id: "unknown", IsClosed: true})
	if h.emitCount() != 0 {
		t.Errorf("an unknown poll id must be dropped; got %d emits", h.emitCount())
	}
}

// TestDispatchReaction_SetDiff covers the Pass-1 C3 set-diff: added/removed are
// the difference of new vs old; custom/paid render as sentinels, never dropped.
func TestDispatchReaction_SetDiff(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h)

	mr := &gotgbot.MessageReactionUpdated{
		Chat:      gotgbot.Chat{Id: -100},
		MessageId: 321,
		User:      &gotgbot.User{Id: 7, Username: "ada"},
		OldReaction: []gotgbot.ReactionType{
			gotgbot.ReactionTypeEmoji{Emoji: "👍"},
		},
		NewReaction: []gotgbot.ReactionType{
			gotgbot.ReactionTypeEmoji{Emoji: "🔥"},
			gotgbot.ReactionTypeCustomEmoji{CustomEmojiId: "x"},
		},
	}
	c.dispatchReaction(9, mr)

	if h.emitCount() != 1 {
		t.Fatalf("expected 1 reaction emit; got %d", h.emitCount())
	}
	re := h.emitted[0].Event.Reaction
	if re == nil {
		t.Fatal("reaction event payload missing")
	}
	// 👍 removed, 🔥 + [custom] added.
	if !contains(re.Added, "🔥") || !contains(re.Added, "[custom]") {
		t.Errorf("added set wrong: %+v (custom must render as sentinel, not dropped)", re.Added)
	}
	if !contains(re.Removed, "👍") {
		t.Errorf("removed set wrong: %+v", re.Removed)
	}
	if re.Actor.Username != "ada" {
		t.Errorf("actor not carried: %+v", re.Actor)
	}
}

// TestDispatchCallback_InaccessibleMessageDropped covers Pass-1 C4: when
// CallbackQuery.Message is the inaccessible variant we cannot route it, so it is
// dropped (after the auto-ack, which is a no-op here since bot is nil) — and NOT
// surfaced with garbage coordinates.
func TestDispatchCallback_InaccessibleMessageDropped(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h) // bot is nil → auto-ack is skipped, drop still works

	cq := &gotgbot.CallbackQuery{
		Id:      "cb1",
		From:    gotgbot.User{Id: 7},
		Data:    "approve:42",
		Message: gotgbot.InaccessibleMessage{MessageId: 1, Chat: gotgbot.Chat{Id: -100}},
	}
	c.dispatchCallback(9, cq)
	if h.emitCount() != 0 {
		t.Errorf("an inaccessible-message callback must not be routed; got %d emits", h.emitCount())
	}
}

// TestDispatchCallback_AccessibleSurfaced asserts an accessible callback is
// converted into a callback InboundEvent carrying the callback id + data and
// routed to the message's chat. (bot is nil so the auto-ack is skipped; the
// surfacing is the unit under test.)
func TestDispatchCallback_AccessibleSurfaced(t *testing.T) {
	h := &fakeHost{decision: channel.GateInboundAllow}
	c := makeChannelWithPolls(h)

	cq := &gotgbot.CallbackQuery{
		Id:   "cb1",
		From: gotgbot.User{Id: 7, Username: "ada"},
		Data: "approve:42",
		Message: &gotgbot.Message{
			MessageId: 200,
			Chat:      gotgbot.Chat{Id: -100},
		},
	}
	c.dispatchCallback(9, cq)
	if h.emitCount() != 1 {
		t.Fatalf("expected 1 callback emit; got %d", h.emitCount())
	}
	in := h.emitted[0]
	if in.Kind != c3types.InboundCallback {
		t.Fatalf("wrong kind: %q", in.Kind)
	}
	cb := in.Event.Callback
	if cb == nil || cb.CallbackID != "cb1" || cb.Data != "approve:42" || cb.MessageID != 200 {
		t.Errorf("callback payload wrong: %+v", cb)
	}
	if in.ChatID != -100 {
		t.Errorf("callback should route to the message chat; got %d", in.ChatID)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
