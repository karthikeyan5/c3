package broker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// seedSessionAttachment adds a recoverable c3/topic-281 attachment for id.
func seedSessionAttachment(mf *mappings.MappingsFile, id string, lastAttached time.Time, detached bool) {
	tid := int64(281)
	mf.UpsertSessionAttachment(id, mappings.SessionAttachment{
		Channel: "telegram", ChatID: -100, TopicID: &tid, Name: "c3", Group: "main",
		LastAttachedAt: lastAttached, Detached: detached,
	})
}

func TestBuildHelloAck_SessionRecoveryHit(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 4242, "/anywhere", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 4242, CWD: "/anywhere", SessionID: "sess-1"}, stub)

	if !ack.AutoAttached || ack.Mapping == nil || ack.Mapping.Name != "c3" {
		t.Fatalf("expected AutoAttached to c3, got AutoAttached=%v mapping=%+v", ack.AutoAttached, ack.Mapping)
	}
	if ack.NoMapping {
		t.Fatal("NoMapping must not be set when recovered")
	}
	rk := stub.CurrentRoute()
	if rk == nil || !rk.HasTopic || rk.TopicID != 281 {
		t.Fatalf("stub route not claimed to topic 281: %+v", rk)
	}
}

func TestBuildHelloAck_SessionRecoveryBeatsCwdMapping(t *testing.T) {
	mf := mfWithTelegram()
	// cwd /proj maps to a DIFFERENT topic (412 / feature-x).
	mf.UpsertMapping("/proj", mappings.Mapping{Channel: "telegram", ChatID: -200, TopicID: 412, Name: "feature-x", Group: "work"})
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 4242, "/proj", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 4242, CWD: "/proj", SessionID: "sess-1"}, stub)

	if !ack.AutoAttached || ack.Mapping == nil || ack.Mapping.Name != "c3" {
		t.Fatalf("session-id recovery must beat the cwd mapping; got mapping=%+v", ack.Mapping)
	}
	if rk := stub.CurrentRoute(); rk == nil || rk.TopicID != 281 {
		t.Fatalf("expected claim to the session's topic 281, got %+v", rk)
	}
}

func TestBuildHelloAck_TombstonedNoRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), true) // tombstoned
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"}, stub)
	if ack.AutoAttached {
		t.Fatal("tombstoned attachment must not auto-recover")
	}
	if stub.CurrentRoute() != nil {
		t.Fatal("no route should be claimed for a tombstoned attachment")
	}
}

func TestBuildHelloAck_ExpiredNoRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC().Add(-40*24*time.Hour), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"}, stub)
	if ack.AutoAttached {
		t.Fatal("expired attachment must not auto-recover")
	}
}

func TestBuildHelloAck_EmptySessionIDNoRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	// No SessionID on hello and an unmapped cwd → NoMapping, no recovery.
	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/x", SessionID: ""}, stub)
	if ack.AutoAttached {
		t.Fatal("no recovery without a session id")
	}
	if !ack.NoMapping {
		t.Fatal("expected NoMapping for an unmapped cwd with no recovery")
	}
}

func TestBuildHelloAck_RecoversDMRoute(t *testing.T) {
	mf := mfWithTelegram()
	// DM attachment: nil TopicID.
	mf.UpsertSessionAttachment("dm-sess", mappings.SessionAttachment{
		Channel: "telegram", ChatID: 42, TopicID: nil, Name: "dm",
		LastAttachedAt: time.Now().UTC(),
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/anywhere", "dm-sess", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/anywhere", SessionID: "dm-sess"}, stub)

	if !ack.AutoAttached || ack.Mapping == nil || ack.Mapping.Name != "dm" {
		t.Fatalf("expected DM auto-recovery, got AutoAttached=%v mapping=%+v", ack.AutoAttached, ack.Mapping)
	}
	rk := stub.CurrentRoute()
	if rk == nil || rk.HasTopic {
		t.Fatalf("DM recovery should claim a no-topic route, got %+v", rk)
	}
}

func TestAttachDM_RecordsSessionAttachment(t *testing.T) {
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	// hello WITH a session id (the helloAck helper omits it).
	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x", SessionID: "dm-sess"}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadFrame(); err != nil { // hello ack
		t.Fatal(err)
	}
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, Target: "dm"}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.AttachedMsg
	if err := json.Unmarshal(raw, &ack); err != nil || !ack.OK {
		t.Fatalf("DM attach failed: ack=%+v err=%v", ack, err)
	}
	// recordSessionAttachment runs synchronously before the ack is written.
	sa, ok := b.Mappings().LookupSessionAttachment("dm-sess")
	if !ok {
		t.Fatal("DM attach must record a session attachment")
	}
	if sa.Name != "dm" || sa.ChatID != 42 || sa.TopicID != nil {
		t.Fatalf("DM session attachment = %+v (want name=dm chat=42 topic=nil)", sa)
	}
}

func TestBuildHelloAck_SkipsRefreshWhenFresh(t *testing.T) {
	mf := mfWithTelegram()
	t0 := time.Now().UTC().Add(-time.Minute) // fresh: < sessionRefreshInterval
	mf.UpsertSessionAttachment("sess-1", mappings.SessionAttachment{
		Channel: "telegram", ChatID: -100, TopicID: func() *int64 { t := int64(281); return &t }(),
		Name: "c3", LastAttachedAt: t0,
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"}, stub)
	if !ack.AutoAttached {
		t.Fatal("fresh attachment should still recover")
	}
	sa, _ := b.Mappings().LookupSessionAttachment("sess-1")
	if !sa.LastAttachedAt.Equal(t0) {
		t.Fatalf("fresh attachment must NOT be rewritten (churn); LastAttachedAt %v != %v", sa.LastAttachedAt, t0)
	}
}

func TestBuildHelloAck_RefreshesWhenStale(t *testing.T) {
	mf := mfWithTelegram()
	t0 := time.Now().UTC().Add(-2 * time.Hour) // stale: > sessionRefreshInterval
	mf.UpsertSessionAttachment("sess-1", mappings.SessionAttachment{
		Channel: "telegram", ChatID: -100, TopicID: func() *int64 { t := int64(281); return &t }(),
		Name: "c3", LastAttachedAt: t0,
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	_ = b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 1, CWD: "/x", SessionID: "sess-1"}, stub)
	sa, _ := b.Mappings().LookupSessionAttachment("sess-1")
	if !sa.LastAttachedAt.After(t0) {
		t.Fatalf("stale attachment must be refreshed; LastAttachedAt %v not after %v", sa.LastAttachedAt, t0)
	}
}

func TestHandleRelease_TombstonesSessionAttachment(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	tid := int64(281)
	b.Routes.Claim(MakeRouteKey("telegram", -100, &tid), stub)
	stub.SetRoute(func() *RouteKey { k := MakeRouteKey("telegram", -100, &tid); return &k }())

	b.handleRelease(stub)

	if stub.CurrentRoute() != nil {
		t.Fatal("release should clear the stub's route")
	}
	sa, ok := b.Mappings().LookupSessionAttachment("sess-1")
	if !ok || !sa.Detached {
		t.Fatalf("explicit detach must tombstone the session attachment; got %+v ok=%v", sa, ok)
	}
}

func TestHandleRelease_EmptySessionIDNoOp(t *testing.T) {
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()
	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "", nil)
	b.handleRelease(stub) // must not panic; nothing to tombstone
}

func TestConnDrop_DoesNotTombstone(t *testing.T) {
	// The HandleConn conn-drop defer releases claims via ReleaseAllByConnID
	// directly (NOT handleRelease), so a quit-without-detach must leave the
	// session attachment recoverable.
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.RegisterWithSession("claude", 1, "/x", "sess-1", nil)
	b.Routes.ReleaseAllByConnID(stub.ConnID) // the conn-drop path

	sa, ok := b.Mappings().LookupSessionAttachment("sess-1")
	if !ok || sa.Detached {
		t.Fatalf("conn-drop must NOT tombstone; got %+v ok=%v", sa, ok)
	}
}

func TestBuildHelloAck_CollisionSkipsRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	// Another LIVE session already holds c3 (topic 281). conn non-nil → IsConnected → alive.
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	holder := b.Stubs.Register("claude", 9999, "/other", struct{}{})
	if _, ok := b.Routes.Claim(key, holder); !ok {
		t.Fatal("precondition: holder should claim the route")
	}

	stub := b.Stubs.RegisterWithSession("claude", 4242, "/anywhere", "sess-1", nil)
	ack := b.buildHelloAck(ipc.HelloMsg{CLI: "claude", PID: 4242, CWD: "/anywhere", SessionID: "sess-1"}, stub)

	if ack.AutoAttached {
		t.Fatal("recovery must be skipped when the topic is held by another live session")
	}
	if stub.CurrentRoute() != nil {
		t.Fatal("no route should be claimed on collision")
	}
	if h, ok := b.Routes.Holder(key); !ok || h.ConnID != holder.ConnID {
		t.Fatal("the original live holder must keep the claim")
	}
}
