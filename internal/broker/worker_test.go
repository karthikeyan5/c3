package broker

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// TestMain isolates the durable queue to a throwaway dir for the whole broker
// test package, so any test that drives a message through the delivery pipeline
// (flushInbounds/Emit/forwardOrFallback now persist to disk) never writes to the
// user's real ~/.local/state/c3/queue. Individual tests may still override with
// t.Setenv("C3_QUEUE_DIR", t.TempDir()) for a fresh per-test dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "c3-broker-queue-test")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("C3_QUEUE_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

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
// wrong. The broker now surfaces a self-documenting recovery message: the
// agent learns the audio exists, the exact file_id, and that it can fetch
// (download_attachment) or retry (retranscribe) without the user resending.
func TestFlushInbounds_VoiceWithoutSTTPluginGetsSelfDocumentingFailure(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -100, MessageID: 42,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VFILE", MIME: "audio/ogg", Size: 1000}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	for _, want := range []string{"transcription failed", "VFILE", "download_attachment", "retranscribe", "does not need to resend"} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("STT failure text missing %q; got %q", want, in.Text)
		}
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

// Phase 3 (spec §E): the per-inbound STT call in flushInbounds runs on the
// worker run-loop ctx. Before the fix it had NO per-call deadline (only the STT
// builtin's ~300s subprocess budget), so a download that hangs BEFORE that
// budget applies blocks the worker goroutine — and any JobFetch/JobConsume
// queued behind it — for up to ~5 min. The fix wraps each call in a per-call
// timeout (sttFlushTimeout). A timed-out call returns "" and falls through to
// the existing self-documenting sttFailureText placeholder path. This test
// registers a stub STT plugin that BLOCKS until its ctx is cancelled, shortens
// sttFlushTimeout, and asserts flushInbounds returns promptly with the
// placeholder (not a ~5min hang, not an empty transcript).
func TestFlushInbounds_VoiceSTTTimeout_FallsBackToPlaceholder(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())

	orig := sttFlushTimeout
	sttFlushTimeout = 50 * time.Millisecond
	defer func() { sttFlushTimeout = orig }()

	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	// Stub STT that hangs until its (per-call) ctx is cancelled, then returns
	// an empty transcript — exactly what a download stuck past the deadline does.
	b.Plugins.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		<-ctx.Done()
		return "", nil
	})

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -100, MessageID: 45,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "HUNGFILE", MIME: "audio/ogg", Size: 1000}},
	}

	done := make(chan struct{})
	go func() {
		w.flushInbounds(context.Background(), []*c3types.Inbound{in})
		close(done)
	}()

	// Must return in ~the timeout, not the ~300s STT budget or ~5min hang.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flushInbounds did not return within 5s — STT call is not bounded by sttFlushTimeout")
	}

	for _, want := range []string{"transcription failed", "HUNGFILE", "download_attachment", "retranscribe", "does not need to resend"} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("STT-timeout fallback text missing %q; got %q", want, in.Text)
		}
	}
}

// Regression test for 2026-05-14: after /mcp reconnect, Claude Code killed
// the old adapter; the broker kept the now-stale claim "while pid alive"
// and every inbound failed with `holder.Conn is not *ipc.Conn` because the
// MarkDisconnected'd stub had nil Conn. Worse, no Telegram fallback fired
// either (the stale claim made `claimed=true`). Fix: liveness check at
// dispatch time, release-and-fall-through when holder PID is dead.
func TestForwardOrFallback_StaleClaim_ReleasesAndFallsThrough(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
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
	w.forwardOrFallback(context.Background(), in, 1)

	// Stale claim must be released.
	if _, held := b.Routes.Holder(key); held {
		t.Error("stale dead-holder claim should have been released on dispatch")
	}
	// Fallback should have fired since claim was cleared.
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Errorf("expected fallback SendReply after releasing stale claim, got %d sends", got)
	}
}

// Companion (D2 / adapter-ipc-1): a holder that IS alive but currently has nil
// Conn (between adapter reconnects) must keep its claim — the adapter will be
// back on the wire shortly, so we don't release it. But THIS inbound must NOT
// be dropped silently: a message sent during the reconnect window used to be
// lost forever with nothing to replay it. Instead it bounces to the SAME
// Telegram fallback the STALE branch uses, so the user is told to resend.
func TestForwardOrFallback_AliveButDisconnectedHolder_BouncesToFallback(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
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
	w.forwardOrFallback(context.Background(), in, 1)

	// The claim is preserved: the holder is alive and will rebind on reconnect.
	if _, held := b.Routes.Holder(key); !held {
		t.Error("alive-but-disconnected claim must be preserved (adapter may reconnect)")
	}
	// The inbound is bounced to the Telegram fallback (not silently dropped) so
	// the user knows to resend once the adapter reconnects.
	if got := len(fc.sendRepliesSnapshot()); got != 1 {
		t.Errorf("expected fallback SendReply for alive-but-disconnected holder, got %d sends", got)
	}
}

// Companion to the bounce: a synthesized EVENT (not a human message) on an
// alive-but-disconnected route must NOT bounce the fallback boilerplate into
// the chat — events are never bounced (a late poll close shouldn't spam the
// topic). It falls through and is dropped with a metadata-only log, same as the
// unclaimed-event path.
func TestForwardOrFallback_AliveButDisconnectedHolder_EventNotBounced(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)

	aliveStub := &Stub{CLI: "claude", PID: os.Getpid(), CWD: "/home/u/proj", ConnID: 99}
	aliveStub.MarkDisconnected()
	b.Routes.Claim(key, aliveStub)

	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	event := &c3types.Inbound{
		Channel: "telegram", ChatID: -1001234567890, TopicID: &tid,
		MessageID: 2201, Kind: c3types.InboundPollResult,
		Event: &c3types.InboundEvent{PollResult: &c3types.PollResult{PollID: "p-late", IsClosed: true}},
	}
	w.forwardOrFallback(context.Background(), event, 0)

	if _, held := b.Routes.Holder(key); !held {
		t.Error("alive-but-disconnected claim must be preserved for an event too")
	}
	if got := len(fc.sendRepliesSnapshot()); got != 0 {
		t.Errorf("an event must not bounce the fallback boilerplate; got %d sends", got)
	}
}

// A synthesized channel EVENT (e.g. a late timed/auto-close poll_result) that is
// forwarded on a route with NO live claim must NOT bounce the "no CLI attached"
// fallback boilerplate into the chat — it is not a human message awaiting a
// reply. It is dropped with a metadata-only log instead. Regression for the
// completeness-batch finding: a poll closing after the session detached spammed
// the topic with the fallback text.
func TestForwardOrFallback_UnclaimedEvent_DoesNotBounceFallback(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -1001234567890, &tid)

	// No claim is registered: the route is unclaimed (session detached).
	w := newRouteWorker(context.Background(), key, time.Hour, b)
	defer w.Stop()

	event := &c3types.Inbound{
		Channel:   "telegram",
		ChatID:    -1001234567890,
		TopicID:   &tid,
		MessageID: 2200,
		Kind:      c3types.InboundPollResult,
		Event: &c3types.InboundEvent{PollResult: &c3types.PollResult{
			PollID: "p-late", IsClosed: true,
		}},
	}
	w.forwardOrFallback(context.Background(), event, 0)

	// The event must be dropped — no fallback boilerplate sent to the chat.
	if got := len(fc.sendRepliesSnapshot()); got != 0 {
		t.Errorf("expected no fallback send for an unclaimed event, got %d sends", got)
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
		// Stop the run goroutine before driving the relay calls directly: armTyping
		// and the run loop's `case <-w.typingC` both touch worker-goroutine-owned
		// state (typingC/typingTicker) with no lock, so a direct call from the test
		// goroutine while run is live races the select. Stop cancels run first.
		w.Stop()

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
		// Stop run before driving the relay directly (see the first subtest's note):
		// armTyping/pulseTyping are worker-goroutine semantics being driven manually.
		w.Stop()

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
		// Stop run before driving the relay directly (see the first subtest's note).
		w.Stop()

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
	// Stop run before driving the relay directly: arm/disarm and the run loop's
	// `case <-w.typingC` both touch unlocked worker-goroutine state, so a direct
	// call while run is live races the select. Stop cancels run first.
	w.Stop()

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
	// Stop run before driving the relay directly (see DisarmIsIdempotent's note).
	w.Stop()

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

// TestWorker_StoppedAfterIdleExit pins A1's mutual-exclusion invariant: once a
// worker's run() has exited (here via the IDLE timeout), Submit must report the
// worker stopped (return false) rather than accepting a job into the buffer of a
// dead worker whose run goroutine will never read it (the original strand). This
// complements TestWorker_SubmitAfterStopReturnsFalse (which covers the explicit
// Stop() path) by covering the idle exit path, where pre-fix `stopped` stayed
// false for the worker's whole life.
func TestWorker_StoppedAfterIdleExit(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, 20*time.Millisecond, nil)
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not idle out within 1s")
	}
	if w.Submit(Job{Kind: JobInbound}) {
		t.Error("Submit after idle exit must return false (worker is stopped)")
	}
}

// TestWorker_IdleExitDrainsPendingFetch pins A1's loss-free drain: when run()
// exits with jobs still buffered in w.queue, every result-channel-bearing job
// must be replied to with an error instead of being left stranded — a caller
// blocked on <-resultCh would otherwise hang forever (the original fetch_queue
// wedge).
//
// All four exit paths (ctx.Done, idleTimer, queue-closed, JobRelease) funnel
// through the SAME single `defer w.shutdown()`, so exercising one exercises the
// drain for all. We drive the JobRelease exit because it is the only
// DETERMINISTIC strand: the idle/ctx selects race the queue read (Go `select`
// picks a ready case at random when a job is also buffered), whereas a
// JobRelease read returns WITHOUT re-entering the select, deterministically
// leaving the jobs queued behind it un-processed for shutdown() to drain.
// Mirrors JobFetch + JobOutbound (Watch-out: JobInbound carries no ResultCh and
// must be dropped silently — exercised implicitly here, asserted by no hang).
func TestWorker_IdleExitDrainsPendingFetch(t *testing.T) {
	fetchCh := make(chan FetchResult, 1)
	outCh := make(chan OutboundResult, 1)

	// Build the worker WITHOUT starting run(), pre-load the queue so all jobs are
	// buffered before the loop reads anything, THEN start run(). The loop reads the
	// leading JobRelease and returns; the JobFetch + JobOutbound behind it are never
	// processed and must be drained on exit. A no-ResultCh JobInbound is also
	// buffered to prove the drain drops it silently (no panic, no hang).
	w := &RouteWorker{
		key:   RouteKey{Channel: "x"},
		queue: make(chan Job, 64),
		idle:  time.Hour,
		done:  make(chan struct{}),
	}
	w.queue <- Job{Kind: JobRelease}
	w.queue <- Job{Kind: JobFetch, Fetch: &FetchJob{All: true, ResultCh: fetchCh}}
	w.queue <- Job{Kind: JobInbound} // no ResultCh — must be dropped silently
	w.queue <- Job{Kind: JobOutbound, Outbound: &OutboundJob{Tool: "reply", ResultCh: outCh}}
	go w.run(context.Background())

	select {
	case r := <-fetchCh:
		if r.Err == nil {
			t.Error("drained JobFetch must carry a non-nil error, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("pending JobFetch was stranded — run() exit did not drain the queue")
	}
	select {
	case r := <-outCh:
		if r.Err == nil {
			t.Error("drained JobOutbound must carry a non-nil error, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("pending JobOutbound was stranded — run() exit did not drain the queue")
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

// readbackRecorderChannel embeds *fakeChannel and additionally implements the
// OPTIONAL SendReadback method, so flushInbounds' echoReadback hook resolves it
// via the readbacker type-assert. A channel WITHOUT this method (the bare
// fakeChannel) is skipped silently — the additive contract.
type readbackRecorderChannel struct {
	*fakeChannel
	rbMu      sync.Mutex
	readbacks []c3types.ReadbackArgs
}

func (r *readbackRecorderChannel) SendReadback(a c3types.ReadbackArgs) (int64, error) {
	r.rbMu.Lock()
	defer r.rbMu.Unlock()
	r.readbacks = append(r.readbacks, a)
	return 99, nil
}

func (r *readbackRecorderChannel) readbackSnapshot() []c3types.ReadbackArgs {
	r.rbMu.Lock()
	defer r.rbMu.Unlock()
	out := make([]c3types.ReadbackArgs, len(r.readbacks))
	copy(out, r.readbacks)
	return out
}

// waitReadbacks polls until at least n readbacks are recorded, or fails after a
// timeout. The echo now fires from a DETACHED goroutine (flushInbounds dispatches
// echoReadback with `go` so a retrying send can't stall the worker's serial
// loop), so tests must wait for it rather than read synchronously.
func (r *readbackRecorderChannel) waitReadbacks(t *testing.T, n int) []c3types.ReadbackArgs {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rbs := r.readbackSnapshot()
		if len(rbs) >= n {
			return rbs
		}
		if time.Now().After(deadline) {
			t.Fatalf("want >= %d SendReadback, got %d after 2s", n, len(rbs))
			return rbs
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitReplyContaining polls the recorder's SendReply calls until one contains
// substr, or fails after a timeout. Same rationale as waitReadbacks: the failure
// notice is now sent from the detached echo goroutine.
func (r *readbackRecorderChannel) waitReplyContaining(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, rp := range r.sendRepliesSnapshot() {
			if strings.Contains(rp.Text, substr) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw a SendReply containing %q within 2s", substr)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func registerReadbackChannel(b *Broker, rc *readbackRecorderChannel) {
	b.chMu.Lock()
	b.channels[rc.Name()] = &channelRegistration{Channel: rc}
	b.chMu.Unlock()
}

// TestFlushInbounds_ReadbackOnSuccess: a REAL transcript drives exactly one
// SendReadback carrying the verbatim transcript (ChatID/TopicID/ReplyTo set), and
// no "couldn't transcribe" failure notice. The agent surface (in.Text) still
// carries the transcript — the echo is purely additive.
func TestFlushInbounds_ReadbackOnSuccess(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	rc := &readbackRecorderChannel{fakeChannel: &fakeChannel{}}
	registerReadbackChannel(b, rc)
	b.Plugins.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		return "hello from the voice note", nil
	})

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	tid := int64(7)
	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: 51,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VF"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	rbs := rc.waitReadbacks(t, 1)
	if len(rbs) != 1 {
		t.Fatalf("want exactly 1 SendReadback on success, got %d", len(rbs))
	}
	if rbs[0].Transcript != "hello from the voice note" {
		t.Errorf("readback transcript = %q; want verbatim transcript", rbs[0].Transcript)
	}
	if rbs[0].ChatID != -100 || rbs[0].ReplyTo == nil || *rbs[0].ReplyTo != 51 ||
		rbs[0].TopicID == nil || *rbs[0].TopicID != 7 {
		t.Errorf("readback routing wrong: %+v", rbs[0])
	}
	if !strings.Contains(in.Text, "hello from the voice note") {
		t.Errorf("agent surface lost the transcript: in.Text=%q", in.Text)
	}
	for _, rp := range rc.sendRepliesSnapshot() {
		if strings.Contains(rp.Text, "Couldn't transcribe") {
			t.Error("a success readback must not also send the failure notice")
		}
	}
}

// TestFlushInbounds_ReadbackFailureNotice: an STT failure marker drives NO
// SendReadback but DOES send the human "couldn't transcribe" notice via the
// channel's normal SendReply. The agent-surface marker (in.Text) is unchanged.
func TestFlushInbounds_ReadbackFailureNotice(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	rc := &readbackRecorderChannel{fakeChannel: &fakeChannel{}}
	registerReadbackChannel(b, rc)
	b.Plugins.OnVoiceReceived(func(ctx context.Context, p c3types.VoicePayload) (string, error) {
		return "[STT FAILED: timeout — see /tmp/broker.log]", nil
	})

	w := newRouteWorker(context.Background(), RouteKey{Channel: "telegram", ChatID: -100}, time.Hour, b)
	defer w.Stop()

	in := &c3types.Inbound{
		Channel: "telegram", ChatID: -100, MessageID: 52,
		Attachments: []c3types.Attachment{{Kind: "voice", FileID: "VF"}},
	}
	w.flushInbounds(context.Background(), []*c3types.Inbound{in})

	// The failure notice is sent from the detached echo goroutine now — wait
	// for it, then assert (no readback on STT failure).
	rc.waitReplyContaining(t, "Couldn't transcribe that voice note")
	if n := len(rc.readbackSnapshot()); n != 0 {
		t.Fatalf("want 0 SendReadback on STT failure, got %d", n)
	}
	sawNotice := false
	for _, rp := range rc.sendRepliesSnapshot() {
		if strings.Contains(rp.Text, "Couldn't transcribe that voice note") {
			sawNotice = true
			if rp.ReplyTo == nil || *rp.ReplyTo != 52 {
				t.Errorf("failure notice should reply-quote the voice msg, got %+v", rp)
			}
		}
	}
	if !sawNotice {
		t.Error("STT failure must send the human 'couldn't transcribe' notice via SendReply")
	}
	if !strings.Contains(in.Text, "[STT FAILED: timeout") {
		t.Errorf("agent-surface marker changed: in.Text=%q", in.Text)
	}
}
