package broker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestWorker_IdleShutdown(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, 50*time.Millisecond, nil)
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on idle within 500ms")
	}
}

func TestWorker_StopExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	go w.Stop()
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on Stop")
	}
}

func TestWorker_ReleaseJobExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	if !w.Submit(Job{Kind: JobRelease}) {
		t.Fatal("Submit should succeed before stop")
	}
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on JobRelease")
	}
}

func TestWorker_SubmitAfterStopReturnsFalse(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	w.Stop()
	if w.Submit(Job{Kind: JobInbound}) {
		t.Error("Submit after Stop should return false")
	}
}

// Regression test for 2026-05-14: when the STT handler script went missing,
// the plugin silently disabled itself at startup and voice messages reached
// the agent as a bare "(voice message)" with no indication anything was
// wrong. Broker-side defense-in-depth: if a voice attachment arrives and no
// plugin produced a transcript AND no caption exists, surface a marker.
func TestFlushInbounds_VoiceWithoutSTTPluginGetsMarker(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   42,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1", Size: 1000}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if !strings.Contains(in.Text, "[STT FAILED:") || !strings.Contains(in.Text, "no_transcript_plugin") {
		t.Errorf("voice with no STT plugin: in.Text=%q, want marker with no_transcript_plugin reason", in.Text)
	}
}

func TestFlushInbounds_VoiceWithCaptionKeepsCaptionWhenSTTAbsent(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   43,
		Text:        "user-typed caption",
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if in.Text != "user-typed caption" {
		t.Errorf("voice with caption but no STT plugin: in.Text=%q, want caption preserved (don't clobber user-deliberate text with marker)", in.Text)
	}
}

func TestFlushInbounds_VoiceWithSTTPluginUsesTranscript(t *testing.T) {
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	b.Plugins.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		return "transcribed text", nil
	})

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:     "telegram",
		ChatID:      -100,
		MessageID:   44,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "v1"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	if !strings.Contains(in.Text, "transcribed text") {
		t.Errorf("voice with STT plugin: in.Text=%q, want transcript embedded", in.Text)
	}
}

// Regression test for 2026-05-14: after /mcp reconnect, Claude Code killed
// the old adapter; the broker kept the now-stale claim "while pid alive"
// and every inbound failed with `holder.Conn is not *ipc.Conn` because the
// MarkDisconnected'd stub had nil Conn. Worse, no Telegram fallback fired
// either (the stale claim made `claimed=true`). Fix: liveness check at
// dispatch time, release-and-fall-through when holder PID is dead.
func TestForwardOrFallback_StaleClaim_ReleasesAndFallsThrough(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)

	// Stub with a PID that's definitively dead. PIDs above PID_MAX
	// (typically 4194304 on Linux, 99999 on macOS) reliably return
	// ESRCH from kill(2). 1<<30 is safely beyond both. MarkDisconnected
	// mirrors the post-conn-drop state.
	deadStub := &Stub{
		CLI: "claude", PID: 1 << 30, CWD: "/home/u/proj", ConnID: 99,
	}
	deadStub.MarkDisconnected()
	b.Routes.Claim(key, deadStub)
	if _, held := b.Routes.Holder(key); !held {
		t.Fatal("test setup: claim should be in place before delivery")
	}

	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel:   "telegram",
		ChatID:    -1001234567890,
		TopicID:   &tid,
		MessageID: 1133,
		Text:      "Hi",
	}
	w.forwardOrFallback(context.Background(), in)

	// Stale claim must be released.
	if _, held := b.Routes.Holder(key); held {
		t.Error("stale dead-holder claim should have been released on dispatch")
	}
	// Fallback should have fired since claim was cleared.
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Errorf("expected fallback SendReply after releasing stale claim, got %d sends", got)
	}
}

// Companion: a holder that IS alive but currently has nil Conn (between
// reconnects) must NOT be released; deliver-skip is the correct behavior,
// because the adapter will be back on the wire shortly. Otherwise a
// momentary network blip would race against the inbound flow.
func TestForwardOrFallback_AliveButDisconnectedHolder_SkipsDelivery(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)

	// Use this test process's PID — guaranteed alive.
	aliveStub := &Stub{
		CLI: "claude", PID: os.Getpid(), CWD: "/home/u/proj", ConnID: 99,
	}
	aliveStub.MarkDisconnected() // alive but disconnected
	b.Routes.Claim(key, aliveStub)

	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -1001234567890, TopicID: &tid,
		MessageID: 1134, Text: "Hi",
	}
	w.forwardOrFallback(context.Background(), in)

	if _, held := b.Routes.Holder(key); !held {
		t.Error("alive-but-disconnected claim must be preserved (adapter may reconnect)")
	}
	if got := len(fc.sendRepliesSnapshot()); got != 0 {
		t.Errorf("expected no fallback for alive disconnected holder, got %d sends", got)
	}
}

// brokerWithGenericChannel wires a broker pre-registered with an arbitrary
// channel.Channel (not just *fakeChannel), mirroring brokerWithChannel's manual
// registration so the typing-relay tests can use a SendTyping-recording channel.
func brokerWithGenericChannel(t *testing.T, mf *mappings.MappingsFile, ch channel.Channel) *Broker {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := New(mf)
	b.chMu.Lock()
	b.channels[ch.Name()] = &channelRegistration{Channel: ch}
	b.chMu.Unlock()
	return b
}

// claimedHolder registers a connected, alive stub holding key and returns it.
func claimedHolder(t *testing.T, b *Broker, key RouteKey) *Stub {
	t.Helper()
	s := &Stub{CLI: "claude", PID: os.Getpid(), CWD: "/proj", ConnID: 7, Conn: "live"}
	b.Routes.Claim(key, s)
	return s
}

// TestTypingRelay_ArmsOnlyWhenHolderRepliedAndTypingCap covers the deterministic
// arm gate (P5): the typing ticker arms on a delivered inbound ONLY IF the holder
// has replied (Telegram-mode proxy) AND the channel advertises Typing.
func TestTypingRelay_ArmsOnlyWhenHolderRepliedAndTypingCap(t *testing.T) {
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}

	t.Run("not armed before holder has replied", func(t *testing.T) {
		ch := &typingRecorderChannel{}
		b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
		defer b.Shutdown()
		holder := claimedHolder(t, b, key)
		w := newRouteWorker(context.Background(), key, time.Hour, b)
		defer w.Stop()

		w.armTyping(holder) // holder.HasReplied() == false
		if w.typingTicker != nil || w.typingC != nil {
			t.Fatal("typing must NOT arm before the holder has replied (CLI-mode gate)")
		}
	})

	t.Run("armed once holder has replied", func(t *testing.T) {
		ch := &typingRecorderChannel{} // Typing: true
		b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
		defer b.Shutdown()
		holder := claimedHolder(t, b, key)
		holder.MarkReplied()
		w := newRouteWorker(context.Background(), key, time.Hour, b)
		defer w.Stop()

		w.armTyping(holder)
		if w.typingTicker == nil || w.typingC == nil {
			t.Fatal("typing should arm once the holder has replied and Typing cap is set")
		}
		// A pulse must fire SendTyping for the route's chat/topic.
		w.pulseTyping(context.Background())
		got := ch.typingSnapshot()
		if len(got) != 1 || got[0].chatID != -100 || got[0].threadID == nil || *got[0].threadID != tid {
			t.Fatalf("pulse should SendTyping for the route; got %+v", got)
		}
	})

	t.Run("not armed when channel lacks Typing cap", func(t *testing.T) {
		ch := &noTypingChannel{} // Typing: false
		b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
		defer b.Shutdown()
		holder := claimedHolder(t, b, key)
		holder.MarkReplied()
		w := newRouteWorker(context.Background(), key, time.Hour, b)
		defer w.Stop()

		w.armTyping(holder)
		if w.typingTicker != nil {
			t.Fatal("typing must NOT arm when the channel does not advertise Typing")
		}
	})
}

// TestTypingRelay_DisarmIsIdempotentAndStopsTicker covers disarm (P5): the first
// reply of a turn disarms the ticker; disarm is idempotent and clears state.
func TestTypingRelay_DisarmIsIdempotentAndStopsTicker(t *testing.T) {
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}
	ch := &typingRecorderChannel{}
	b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
	defer b.Shutdown()
	holder := claimedHolder(t, b, key)
	holder.MarkReplied()
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	w.armTyping(holder)
	if w.typingTicker == nil {
		t.Fatal("setup: ticker should be armed")
	}
	w.disarmTyping()
	if w.typingTicker != nil || w.typingC != nil {
		t.Fatal("disarm should stop the ticker and clear state")
	}
	w.disarmTyping() // idempotent — must not panic on a nil ticker
}

// TestTypingRelay_ReArmKeepsCadence covers re-arm (P5): re-arming an already-armed
// ticker is a no-op (keeps the same ticker), so a turn's tool calls don't reset
// the cadence.
func TestTypingRelay_ReArmKeepsCadence(t *testing.T) {
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}
	ch := &typingRecorderChannel{}
	b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
	defer b.Shutdown()
	holder := claimedHolder(t, b, key)
	holder.MarkReplied()
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	w.armTyping(holder)
	first := w.typingTicker
	w.armTyping(holder) // re-arm
	if w.typingTicker != first {
		t.Fatal("re-arm should keep the existing ticker (steady cadence), not replace it")
	}
}

// noTypingChannel advertises Typing:false so the arm gate can be tested.
type noTypingChannel struct{ typingRecorderChannel }

func (c *noTypingChannel) Capabilities() c3types.Capabilities {
	return c3types.Capabilities{Channel: "telegram", Typing: false}
}

// TestTypingRelay_IdlesOutWhenNoReply is the regression test for the
// 2026-06-15 triple-review finding: a typing tick must NOT extend the worker's
// idle lifetime. Before the fix, the run loop reset the idle timer on EVERY
// select iteration (including the typing-tick case); since typingInterval <
// idle, an armed ticker re-armed idle forever, so a worker that took an inbound
// (or a non-reply tool call) but never replied — e.g. the user switched to CLI
// mode mid-turn — pulsed "typing…" to Telegram indefinitely and never idled out.
//
// Here we arm the relay via a successful non-reply tool call (react), which arms
// typing in the worker goroutine (race-free), then never reply. With a short
// idle and short pulse cadence the worker MUST idle out and disarm the relay.
func TestTypingRelay_IdlesOutWhenNoReply(t *testing.T) {
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}
	ch := &typingRecorderChannel{} // Typing: true; react returns nil
	b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
	defer b.Shutdown()
	holder := claimedHolder(t, b, key)
	holder.MarkReplied() // arm gate: holder must have replied at least once

	w := newRouteWorker(context.Background(), key, 150*time.Millisecond, b)
	w.typingIvl = 20 * time.Millisecond // many pulses fit inside the idle window
	defer w.Stop()

	// A successful non-reply tool call arms the relay from the worker goroutine.
	resultCh := make(chan OutboundResult, 1)
	if !w.Submit(Job{Kind: JobOutbound, Outbound: &OutboundJob{
		Tool: "react", Args: map[string]any{"message_id": int64(1), "emoji": "👍"}, ResultCh: resultCh,
	}}) {
		t.Fatal("Submit react job failed")
	}
	if r := <-resultCh; r.Err != nil {
		t.Fatalf("react tool call should succeed (arms typing); got err %v", r.Err)
	}

	// The worker must idle out despite the typing ticker pulsing the whole time.
	select {
	case <-w.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did NOT idle out — a typing tick is still extending the idle timer (unbounded pulsing)")
	}

	// After the run goroutine has exited (Done closed), no concurrent writes
	// remain, so reading typing state here is race-free. The defer disarmTyping()
	// in run() must have stopped the relay.
	if w.typingTicker != nil || w.typingC != nil {
		t.Fatal("typing relay must be disarmed once the worker idles out")
	}

	// Pulsing happened (proof the relay was live) but is bounded — it did not run
	// away. Far fewer than a runaway count.
	if n := len(ch.typingSnapshot()); n == 0 {
		t.Fatal("expected at least one typing pulse while the relay was armed")
	}
}

// TestTypingRelay_DisarmsAfterMaxPulses covers the belt-and-suspenders bound:
// even if (hypothetically) the idle timeout never trips, the relay self-disarms
// after maxTypingPulses consecutive unanswered pulses. We drive pulseTyping
// directly (worker goroutine semantics: armTyping then pulseTyping are the same
// calls the run loop makes) on a long-idle worker so only the cap can stop it.
func TestTypingRelay_DisarmsAfterMaxPulses(t *testing.T) {
	tid := int64(281)
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: tid}
	ch := &typingRecorderChannel{}
	b := brokerWithGenericChannel(t, mfWithTelegram(), ch)
	defer b.Shutdown()
	holder := claimedHolder(t, b, key)
	holder.MarkReplied()

	// Long idle + manual stepping so the run loop's own typing-tick never races
	// our direct calls (Stop cancels the loop before we touch state).
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	w.Stop() // stop the run goroutine; we drive the relay calls directly below

	w.armTyping(holder)
	if w.typingTicker == nil {
		t.Fatal("setup: relay should arm")
	}
	// Pulse up to the cap: pulses 1..maxTypingPulses succeed; the next disarms.
	for i := 0; i < maxTypingPulses; i++ {
		w.pulseTyping(context.Background())
		if w.typingTicker == nil {
			t.Fatalf("relay disarmed too early at pulse %d (cap is %d)", i+1, maxTypingPulses)
		}
	}
	w.pulseTyping(context.Background()) // exceeds the cap
	if w.typingTicker != nil || w.typingC != nil {
		t.Fatalf("relay must self-disarm after exceeding %d consecutive unanswered pulses", maxTypingPulses)
	}
}

func TestWorker_OutboundStubReturnsErr(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	defer w.Stop()

	resultCh := make(chan OutboundResult, 1)
	if !w.Submit(Job{Kind: JobOutbound, Outbound: &OutboundJob{Tool: "reply", ResultCh: resultCh}}) {
		t.Fatal("Submit failed")
	}
	select {
	case r := <-resultCh:
		if r.Err == nil {
			t.Error("expected stub error in Phase 4A")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no result within 500ms")
	}
}
