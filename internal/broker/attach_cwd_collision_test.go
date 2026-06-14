package broker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// SYMPTOM-3 (2026-06-04): multiple `claude` instances launched from the
// SAME parent dir (/home/karthi/arogara) report identical os.Getwd(). A
// bare `/c3:attach` (cwd-default, no name) from a session that MEANT a
// different sub-project silently resolves the saved parent→topic mapping
// and races/steals the topic already held by a live sibling session.
//
// The chosen UX (Karthi): when a BARE cwd-default attach resolves to a
// topic ALREADY HELD by a DIFFERENT LIVE session, do NOT silently claim
// and do NOT show only the raw force_steal y/n prompt. Return a guided
// AttachStatusCwdDefaultCollision message that names the holder and
// suggests attach-by-name (the likely-correct action) or --steal.

// collisionSetup wires a broker whose saved cwd-mapping points the launch
// dir at topic "c3" (281), then has a FIRST live session claim that topic.
// Returns the broker for a SECOND session to race on.
func collisionSetup(t *testing.T, launchCWD string) (*Broker, *fakeChannel) {
	t.Helper()
	mf := mfWithTelegram()
	// Saved mapping: the shared launch dir defaults to topic c3 (281).
	mf.Mappings[launchCWD] = mappings.Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
	}
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	return b, fc
}

// TestAttach_CwdDefault_HeldByDifferentLiveSession_WarnsCollision is the
// core RED test: a bare attach whose saved mapping resolves to a topic a
// DIFFERENT live session already holds must return OK:false with
// Status==cwd_default_collision and a message naming the holder, plus the
// two escape hatches. Before the fix this hit the raw force_steal proposal.
func TestAttach_CwdDefault_HeldByDifferentLiveSession_WarnsCollision(t *testing.T) {
	const launch = "/home/karthi/arogara"
	b, _ := collisionSetup(t, launch)
	defer b.Shutdown()

	// FIRST live session claims topic c3 directly. Register it WITH a
	// non-nil conn so Stub.IsAlive() reports true via IsConnected() — the
	// synthetic PID 9823 is not a real process on this machine, so a nil
	// conn would fall through to isPIDAlive(9823)==false and the collision
	// would never fire. The non-nil conn keeps the holder unambiguously
	// live while still pinning PID 9823 for the holder-identity assertions
	// below.
	holder := b.Stubs.Register("claude", 9823, launch, struct{}{})
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	if !b.tryClaim(nil, holder, key, "c3", false, false) {
		t.Fatal("holder's initial claim should succeed")
	}

	// SECOND session: bare cwd-default attach (no Name) from the SAME
	// launch dir → saved mapping resolves to c3, which holder owns.
	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, launch)
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: launch}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.AttachedMsg
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}

	if ack.OK {
		t.Fatalf("cwd-default attach to a held topic must NOT succeed; got OK with Name=%q", ack.Name)
	}
	if ack.Status != ipc.AttachStatusCwdDefaultCollision {
		t.Fatalf("Status=%q want %q (got Proposal=%+v Err=%q)",
			ack.Status, ipc.AttachStatusCwdDefaultCollision, ack.Proposal, ack.Err)
	}
	// The raw force_steal proposal must NOT be what's surfaced here — the
	// whole point is to replace it with the guided message.
	if ack.Proposal != nil && ack.Proposal.Action == "force_steal" {
		t.Errorf("cwd-default collision must NOT surface the raw force_steal proposal")
	}
	// Fields the formatter needs.
	if ack.Name != "c3" {
		t.Errorf("collision Name=%q want c3 (the resolved topic)", ack.Name)
	}
	if ack.Holder == nil || ack.Holder.PID != 9823 || ack.Holder.CLI != "claude" {
		t.Errorf("collision Holder=%+v want claude pid 9823", ack.Holder)
	}
	// Err carries the guided text with the holder + both suggestions.
	for _, w := range []string{"c3", "9823", "claude", "steal"} {
		if !strings.Contains(ack.Err, w) {
			t.Errorf("collision Err missing %q: %q", w, ack.Err)
		}
	}
	if !strings.Contains(strings.ToLower(ack.Err), "name") {
		t.Errorf("collision Err should suggest attach-by-name: %q", ack.Err)
	}
}

// TestAttach_CwdDefault_TopicFree_ClaimsNormally is the no-regression
// guard: when the cwd-default-resolved topic is FREE (no live holder),
// the bare attach claims it normally — Status==ok, no collision warning.
// The accepted residual "wrong but free" case stays silent (attach-by-name
// is the documented tool; there is no signal to detect "wrong project").
func TestAttach_CwdDefault_TopicFree_ClaimsNormally(t *testing.T) {
	const launch = "/home/karthi/arogara"
	b, _ := collisionSetup(t, launch)
	defer b.Shutdown()

	// No prior holder — topic c3 is free.
	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, launch)
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: launch}); err != nil {
		t.Fatal(err)
	}
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if !ack.OK {
		t.Fatalf("cwd-default attach to a FREE topic must claim normally; got Err=%q Status=%q", ack.Err, ack.Status)
	}
	if ack.Status != ipc.AttachStatusOK {
		t.Errorf("Status=%q want %q", ack.Status, ipc.AttachStatusOK)
	}
	if ack.Status == ipc.AttachStatusCwdDefaultCollision {
		t.Error("free topic must NOT warn collision")
	}
	if ack.Name != "c3" {
		t.Errorf("Name=%q want c3", ack.Name)
	}
}

// TestHeldByDifferentLiveSession_Predicate directly pins the helper that
// drives the collision check, exercising each branch of the reused
// holder-detection (held? / same-logical-session? / IsAlive?) in isolation
// — independent of the hello-stage reconnect transfer that an end-to-end
// peer test would trigger first.
func TestHeldByDifferentLiveSession_Predicate(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	caller := &Stub{CLI: "claude", PID: 1, CWD: "/home/karthi/arogara"}

	// (a) Unheld → no collision.
	if h, c := b.heldByDifferentLiveSession(key, caller); c || h != nil {
		t.Errorf("unheld key must not collide; got holder=%+v collides=%v", h, c)
	}

	// (b) Held by a DIFFERENT live session → collision. Use the running
	// test process's own PID so IsAlive() is unambiguously true.
	live := &Stub{CLI: "claude", PID: os.Getpid(), CWD: "/home/karthi/arogara", Conn: struct{}{}}
	if _, ok := b.Routes.Claim(key, live); !ok {
		t.Fatal("seeding the live holder claim should succeed")
	}
	h, c := b.heldByDifferentLiveSession(key, caller)
	if !c || h == nil || h.PID != live.PID {
		t.Errorf("different live holder must collide; got holder=%+v collides=%v", h, c)
	}

	// (c) Held by the SAME logical session (caller IS the holder) → no
	// collision (it's a re-attach, the claim would succeed). Seed a holder
	// with the caller's exact identity (CLI+PID+CWD) on a separate key.
	selfKey := MakeRouteKey("telegram", -200, &[]int64{412}[0])
	selfHolder := &Stub{CLI: caller.CLI, PID: caller.PID, CWD: caller.CWD, Conn: struct{}{}}
	if _, ok := b.Routes.Claim(selfKey, selfHolder); !ok {
		t.Fatal("seeding the self holder claim should succeed")
	}
	if h, c := b.heldByDifferentLiveSession(selfKey, caller); c || h != nil {
		t.Errorf("same-logical-session holder must NOT collide; got holder=%+v collides=%v", h, c)
	}

	// (d) Held by a DEAD different session → no collision (stale claim
	// would be displaced by Claim anyway). PID 0 is never alive. Inject
	// directly so the seeding itself doesn't go through Claim's liveness
	// path.
	dead := &Stub{CLI: "codex", PID: 0, CWD: "/elsewhere"}
	deadKey := MakeRouteKey("telegram", -100, &[]int64{412}[0])
	b.Routes.mu.Lock()
	b.Routes.m[deadKey] = dead
	b.Routes.mu.Unlock()
	if h, c := b.heldByDifferentLiveSession(deadKey, caller); c || h != nil {
		t.Errorf("dead holder must NOT collide; got holder=%+v collides=%v", h, c)
	}
}

// TestAttach_ExplicitName_HeldTopic_StillForceSteal locks the scope rule:
// an EXPLICIT `/c3:attach c3` to a held topic must STILL behave as today
// (force_steal proposal), NOT the new cwd-default collision status. The
// user explicitly asked for that topic, so the held-topic prompt is
// correct there.
func TestAttach_ExplicitName_HeldTopic_StillForceSteal(t *testing.T) {
	const launch = "/home/karthi/arogara"
	b, _ := collisionSetup(t, launch)
	defer b.Shutdown()

	// Holder owns c3. Register WITH a non-nil conn so IsAlive() is true via
	// IsConnected() (PID 9823 is synthetic and not a live process here).
	holder := b.Stubs.Register("claude", 9823, launch, struct{}{})
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	if !b.tryClaim(nil, holder, key, "c3", false, false) {
		t.Fatal("holder claim should succeed")
	}

	// SECOND session: EXPLICIT name "c3" (not cwd-default).
	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, launch)
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: launch, Name: "c3"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Fatal("explicit attach to held topic must not silently succeed")
	}
	if ack.Status == ipc.AttachStatusCwdDefaultCollision {
		t.Error("explicit-name attach must NOT use the cwd-default collision status")
	}
	if !ack.NeedsConfirmation || ack.Proposal == nil || ack.Proposal.Action != "force_steal" {
		t.Errorf("explicit-name held topic should still be force_steal proposal; got Status=%q Proposal=%+v",
			ack.Status, ack.Proposal)
	}
}

// TestAttach_CwdDefault_HeldBySameLogicalSession_NoWarn covers the
// reconnect/self case: if the cwd-default-resolved topic is held by the
// SAME logical session (same CLI+PID+CWD), the caller IS the holder — a
// re-attach, not a collision. Must claim (transfer) normally, no warning.
func TestAttach_CwdDefault_HeldBySameLogicalSession_NoWarn(t *testing.T) {
	const launch = "/home/karthi/arogara"
	b, _ := collisionSetup(t, launch)
	defer b.Shutdown()

	// The holder is the SAME logical session that will re-attach: same
	// CLI+PID+CWD. We register it and claim, then drive an attach over a
	// peer conn whose hello carries the SAME identity (claude / PID / cwd).
	// helloAck() uses CLI="claude", PID=1, so make the holder match that.
	holder := b.Stubs.Register("claude", 1, launch, nil)
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	if !b.tryClaim(nil, holder, key, "c3", false, false) {
		t.Fatal("holder claim should succeed")
	}

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, launch) // CLI=claude, PID=1, CWD=launch → same logical session
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: launch}); err != nil {
		t.Fatal(err)
	}
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.Status == ipc.AttachStatusCwdDefaultCollision {
		t.Fatalf("same-logical-session re-attach must NOT warn collision; got Err=%q", ack.Err)
	}
	if !ack.OK {
		t.Fatalf("same-logical-session re-attach should claim (transfer); got Err=%q Status=%q", ack.Err, ack.Status)
	}
	if ack.Name != "c3" {
		t.Errorf("Name=%q want c3", ack.Name)
	}
}
