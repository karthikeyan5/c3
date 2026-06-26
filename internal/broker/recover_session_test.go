package broker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
	"github.com/karthikeyan5/c3/internal/queue"
)

// recoverViaPeer runs hello then a RecoverSessionReq over a peer pair and
// returns the decoded response. The peer conn is left open for further reads.
func recoverViaPeer(t *testing.T, b *Broker, cwd, stableID string) (*ipc.Conn, func(), ipc.RecoverSessionResp) {
	t.Helper()
	peer, done := peerPair(t, b)
	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadFrame(); err != nil { // hello ack
		t.Fatal(err)
	}
	if err := peer.WriteJSON(ipc.RecoverSessionReq{Op: ipc.OpRecoverSession, StableSessionID: stableID, CWD: cwd}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var resp ipc.RecoverSessionResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return peer, done, resp
}

func TestHandleRecoverSession_NoRouteRecovers(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	_, done, resp := recoverViaPeer(t, b, "/anywhere", "sess-1")
	defer done()

	if !resp.Recovered {
		t.Fatalf("expected Recovered=true, got %+v", resp)
	}
	if resp.Name != "c3" || resp.Channel != "telegram" || resp.TopicID == nil || *resp.TopicID != 281 {
		t.Fatalf("recover response fields wrong: %+v", resp)
	}
	// The route must be claimed by the recovering stub.
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	if _, ok := b.Routes.Holder(key); !ok {
		t.Fatal("route should be claimed after a successful recover")
	}
}

// TestHandleRecoverSession_CarriesBacklogPreview covers BUG #2: a recovered
// resume must carry a compact backlog PREVIEW (QueuedSummary), not just a count,
// so the adapter can surface the actual held messages into the resumed session.
func TestHandleRecoverSession_CarriesBacklogPreview(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	tid := int64(281)
	qrk := queue.RouteKey{Channel: "telegram", ChatID: -100, TopicID: &tid}
	for i := int64(1); i <= 4; i++ {
		if err := b.Queue.Append(qrk, &c3types.Inbound{
			Channel: "telegram", ChatID: -100, TopicID: &tid, MessageID: i,
			Sender: c3types.Sender{Username: "k"}, Text: "held msg", Timestamp: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, done, resp := recoverViaPeer(t, b, "/anywhere", "sess-1")
	defer done()

	if !resp.Recovered || resp.QueuedCount != 4 {
		t.Fatalf("expected recovered with 4 queued, got %+v", resp)
	}
	if len(resp.QueuedSummary) == 0 {
		t.Fatal("BUG #2: recover response must carry a backlog preview, not just a count")
	}
	if resp.QueuedSummary[0].MessageID != 1 || resp.QueuedSummary[0].Preview == "" {
		t.Fatalf("first preview item malformed: %+v", resp.QueuedSummary[0])
	}
}

func TestHandleRecoverSession_TombstonedNoRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), true) // tombstoned
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	_, done, resp := recoverViaPeer(t, b, "/x", "sess-1")
	defer done()
	if resp.Recovered {
		t.Fatal("tombstoned attachment must not recover")
	}
	tid := int64(281)
	if _, ok := b.Routes.Holder(MakeRouteKey("telegram", -100, &tid)); ok {
		t.Fatal("no route should be claimed for a tombstoned attachment")
	}
}

func TestHandleRecoverSession_ExpiredNoRecovery(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC().Add(-40*24*time.Hour), false)
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	_, done, resp := recoverViaPeer(t, b, "/x", "sess-1")
	defer done()
	if resp.Recovered {
		t.Fatal("expired attachment must not recover")
	}
}

func TestHandleRecoverSession_MissingNoRecovery(t *testing.T) {
	mf := mfWithTelegram() // no attachment seeded
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	_, done, resp := recoverViaPeer(t, b, "/x", "unknown-sess")
	defer done()
	if resp.Recovered {
		t.Fatal("missing attachment must not recover")
	}
}

func TestHandleRecoverSession_HeldByAnotherLiveSession(t *testing.T) {
	mf := mfWithTelegram()
	seedSessionAttachment(mf, "sess-1", time.Now().UTC(), false) // → c3 / 281
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	// Another LIVE session already holds c3 (topic 281). conn non-nil → alive.
	tid := int64(281)
	key := MakeRouteKey("telegram", -100, &tid)
	holder := b.Stubs.Register("claude", 9999, "/other", struct{}{})
	if _, ok := b.Routes.Claim(key, holder); !ok {
		t.Fatal("precondition: holder should claim the route")
	}

	_, done, resp := recoverViaPeer(t, b, "/anywhere", "sess-1")
	defer done()

	if resp.Recovered {
		t.Fatal("recovery must be skipped when the topic is held by another live session")
	}
	if h, ok := b.Routes.Holder(key); !ok || h.ConnID != holder.ConnID {
		t.Fatal("the original live holder must keep the claim")
	}
}

func TestHandleRecoverSession_DualPathRecordsCurrentRoute(t *testing.T) {
	// Attach-first: the stub already holds a route (claimed by cwd) BEFORE the
	// recover op arrives. The recover op must NOT re-claim, but must RECORD the
	// current route under the stable id (dual-path recording).
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadFrame(); err != nil { // hello ack
		t.Fatal(err)
	}
	// Attach by topic id 412 (feature-x) first.
	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, TopicID: func() *int64 { t := int64(412); return &t }(), Group: "work"}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.AttachedMsg
	if err := json.Unmarshal(raw, &ack); err != nil || !ack.OK {
		t.Fatalf("attach failed: ack=%+v err=%v", ack, err)
	}

	// Now the recover op arrives — already attached.
	if err := peer.WriteJSON(ipc.RecoverSessionReq{Op: ipc.OpRecoverSession, StableSessionID: "late-sess", CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	raw, err = peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var resp ipc.RecoverSessionResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Recovered {
		t.Fatal("attach-first must NOT report Recovered (no re-claim)")
	}
	// The current route (feature-x / 412) must be recorded under the stable id.
	sa, ok := b.Mappings().LookupSessionAttachment("late-sess")
	if !ok {
		t.Fatal("dual-path: the current route must be recorded under the stable id")
	}
	if sa.Name != "feature-x" || sa.TopicID == nil || *sa.TopicID != 412 || sa.Group != "work" {
		t.Fatalf("recorded attachment = %+v (want feature-x / 412 / work)", sa)
	}
}

func TestHandleRecoverSession_BadRequest(t *testing.T) {
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	// Empty stable id → fail-closed error response, no recovery.
	if err := peer.WriteJSON(ipc.RecoverSessionReq{Op: ipc.OpRecoverSession, StableSessionID: ""}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var resp ipc.RecoverSessionResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Recovered || resp.Err == "" {
		t.Fatalf("empty stable id must produce an error response, got %+v", resp)
	}
}

func TestRecoverSession_RefreshesStaleLastAttachedAt(t *testing.T) {
	mf := mfWithTelegram()
	t0 := time.Now().UTC().Add(-2 * time.Hour) // stale: > sessionRefreshInterval
	mf.UpsertSessionAttachment("sess-1", mappings.SessionAttachment{
		Channel: "telegram", ChatID: -100, TopicID: func() *int64 { t := int64(281); return &t }(),
		Name: "c3", LastAttachedAt: t0,
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.Register("claude", 1, "/x", nil)
	stub.SetStableSessionID("sess-1")
	if _, _, ok := b.recoverSession(stub); !ok {
		t.Fatal("expected recoverSession to succeed")
	}
	sa, _ := b.Mappings().LookupSessionAttachment("sess-1")
	if !sa.LastAttachedAt.After(t0) {
		t.Fatalf("stale attachment must be refreshed; %v not after %v", sa.LastAttachedAt, t0)
	}
}

func TestRecoverSession_SkipsRefreshWhenFresh(t *testing.T) {
	mf := mfWithTelegram()
	t0 := time.Now().UTC().Add(-time.Minute) // fresh: < sessionRefreshInterval
	mf.UpsertSessionAttachment("sess-1", mappings.SessionAttachment{
		Channel: "telegram", ChatID: -100, TopicID: func() *int64 { t := int64(281); return &t }(),
		Name: "c3", LastAttachedAt: t0,
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	stub := b.Stubs.Register("claude", 1, "/x", nil)
	stub.SetStableSessionID("sess-1")
	if _, _, ok := b.recoverSession(stub); !ok {
		t.Fatal("fresh attachment should still recover")
	}
	sa, _ := b.Mappings().LookupSessionAttachment("sess-1")
	if !sa.LastAttachedAt.Equal(t0) {
		t.Fatalf("fresh attachment must NOT be rewritten (churn); %v != %v", sa.LastAttachedAt, t0)
	}
}

func TestRecoverSession_DMRoute(t *testing.T) {
	mf := mfWithTelegram()
	mf.UpsertSessionAttachment("dm-sess", mappings.SessionAttachment{
		Channel: "telegram", ChatID: 42, TopicID: nil, Name: "dm",
		LastAttachedAt: time.Now().UTC(),
	})
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()

	_, done, resp := recoverViaPeer(t, b, "/anywhere", "dm-sess")
	defer done()
	if !resp.Recovered || resp.Name != "dm" || resp.TopicID != nil {
		t.Fatalf("expected DM recovery (name=dm, no topic), got %+v", resp)
	}
}

func TestRecoverSession_EmptyStableIDNoOp(t *testing.T) {
	mf := mfWithTelegram()
	b := brokerWithChannel(t, mf, &fakeChannel{})
	defer b.Shutdown()
	stub := b.Stubs.Register("claude", 1, "/x", nil) // no stable id
	if _, _, ok := b.recoverSession(stub); ok {
		t.Fatal("recoverSession must be a no-op without a stable id")
	}
}
