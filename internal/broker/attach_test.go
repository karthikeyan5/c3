package broker

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

// fakeChannel is a test-only channel.Channel implementation. Tracks calls
// and returns canned responses.
type fakeChannel struct {
	mu              sync.Mutex
	createCalls     []createCall
	validateCalls   []validateCall
	createReturnID  int64
	createReturnErr error
	validateErr     error
}

type createCall struct {
	chatID int64
	name   string
}
type validateCall struct {
	chatID int64
	tid    int64
}

func (f *fakeChannel) Name() string                                 { return "telegram" }
func (f *fakeChannel) Start(_ context.Context, _ channel.Host) error { return nil }
func (f *fakeChannel) Stop() error                                  { return nil }
func (f *fakeChannel) SendReply(c3types.ReplyArgs) (int64, error)   { return 0, nil }
func (f *fakeChannel) SendTyping(int64, *int64) error               { return nil }
func (f *fakeChannel) EditMessage(c3types.EditArgs) (*c3types.EditResult, error) {
	return &c3types.EditResult{}, nil
}
func (f *fakeChannel) React(c3types.ReactArgs) error            { return nil }
func (f *fakeChannel) DownloadAttachment(string) (string, error) { return "", nil }

func (f *fakeChannel) CreateTopic(chatID int64, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, createCall{chatID, name})
	if f.createReturnErr != nil {
		return 0, f.createReturnErr
	}
	return f.createReturnID, nil
}

func (f *fakeChannel) ValidateTopic(chatID int64, tid int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validateCalls = append(f.validateCalls, validateCall{chatID, tid})
	return f.validateErr
}

// brokerWithChannel builds a Broker pre-wired with a fakeChannel and the
// supplied mappings. Returns the broker and a function to feed the broker a
// scratch mappings.json path so SaveMappings doesn't clobber the user's real
// config during tests.
func brokerWithChannel(t *testing.T, mf *mappings.MappingsFile, fc *fakeChannel) *Broker {
	t.Helper()
	// Redirect SaveMappings to a scratch file via XDG_CONFIG_HOME.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := New(mf)
	// Bypass Broker.RegisterChannel (which would call fc.Start with a
	// real broker context); register manually.
	b.chMu.Lock()
	b.channels[fc.Name()] = &channelRegistration{Channel: fc}
	b.chMu.Unlock()
	return b
}

// peer pair: returns the adapter-side ipc.Conn and the broker-side closure
// that runs HandleConn against the broker-side end. Caller calls returned
// closure to start the handler in a goroutine.
func peerPair(t *testing.T, b *Broker) (*ipc.Conn, func()) {
	t.Helper()
	a, brokerSide := net.Pipe()
	go b.HandleConn(brokerSide)
	return ipc.NewConn(a), func() { _ = a.Close() }
}

func mfWithTelegram() *mappings.MappingsFile {
	return &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels: map[string]mappings.ChannelConfig{
			"telegram": {
				DefaultGroup: "main",
				Groups: map[string]mappings.GroupConfig{
					"main": {ChatID: -100},
					"work": {ChatID: -200},
				},
				DMChatID: 42,
				Topics: []mappings.Topic{
					{ChatID: -100, TopicID: 281, Name: "c3", Group: "main"},
					{ChatID: -200, TopicID: 412, Name: "feature-x", Group: "work"},
				},
			},
		},
		Mappings: map[string]mappings.Mapping{},
	}
}

func helloAck(t *testing.T, peer *ipc.Conn, cwd string) {
	t.Helper()
	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadFrame(); err != nil {
		t.Fatal(err)
	}
}

func TestAttach_DM(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, Target: "dm"}); err != nil {
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
	if !ack.OK {
		t.Fatalf("DM attach failed: %s", ack.Err)
	}
	if ack.ChatID != 42 {
		t.Errorf("ChatID=%d, want 42", ack.ChatID)
	}
	if ack.Name != "dm" {
		t.Errorf("Name=%q, want dm", ack.Name)
	}
	if ack.TopicID != nil {
		t.Errorf("DM should have nil TopicID, got %v", ack.TopicID)
	}
}

func TestAttach_NameInDefaultGroup_SilentClaim(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/c3")

	if err := peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/c3", Name: "c3"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if !ack.OK {
		t.Fatalf("attach failed: %s", ack.Err)
	}
	if ack.NeedsConfirmation {
		t.Error("found-in-default-group should be silent claim, not proposal")
	}
	if ack.TopicID == nil || *ack.TopicID != 281 {
		t.Errorf("TopicID=%v, want &281", ack.TopicID)
	}
	// Mapping persisted.
	if _, ok := b.Mappings.LookupByCwd("/projects/c3"); !ok {
		t.Error("expected /projects/c3 mapping to be persisted")
	}
}

func TestAttach_NameInOtherGroup_ProposesDisambiguation(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/feature-x")

	// feature-x exists in group "work" but not "main" (default).
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/feature-x", Name: "feature-x"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Error("expected proposal not OK")
	}
	if !ack.NeedsConfirmation {
		t.Errorf("expected NeedsConfirmation=true, got %+v", ack)
	}
	if ack.Proposal == nil || ack.Proposal.Action != "use_existing_other_group" {
		t.Errorf("Proposal=%+v, want use_existing_other_group", ack.Proposal)
	}
	if ack.Proposal.Existing == nil || ack.Proposal.Existing.Group != "work" {
		t.Errorf("Existing.Group=%v", ack.Proposal.Existing)
	}
	if ack.Proposal.Alternative == nil || ack.Proposal.Alternative.Action != "create" {
		t.Errorf("Alternative=%+v, want action=create", ack.Proposal.Alternative)
	}
}

func TestAttach_UnknownName_ProposesCreation(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/widget-foo")

	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/widget-foo", Name: "widget-foo"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Error("expected proposal not OK")
	}
	if ack.Proposal == nil || ack.Proposal.Action != "create" || ack.Proposal.Name != "widget-foo" {
		t.Errorf("Proposal=%+v", ack.Proposal)
	}
	// No CreateTopic call yet (proposal phase).
	if len(fc.createCalls) != 0 {
		t.Errorf("createCalls=%d, want 0 in proposal phase", len(fc.createCalls))
	}
}

func TestAttach_CreateTrue_CallsChannelCreateAndPersists(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{createReturnID: 917}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/widget-foo")

	_ = peer.WriteJSON(ipc.AttachReq{
		Op: ipc.OpAttach, CWD: "/projects/widget-foo",
		Name: "widget-foo", Create: true,
	})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if !ack.OK {
		t.Fatalf("create failed: %s", ack.Err)
	}
	if ack.TopicID == nil || *ack.TopicID != 917 {
		t.Errorf("TopicID=%v, want &917", ack.TopicID)
	}
	if len(fc.createCalls) != 1 {
		t.Errorf("expected 1 CreateTopic call, got %d", len(fc.createCalls))
	}
	if fc.createCalls[0].name != "widget-foo" {
		t.Errorf("CreateTopic name=%q", fc.createCalls[0].name)
	}
	if _, ok := b.Mappings.LookupTopicByID("telegram", -100, 917); !ok {
		t.Error("topic should have been registered")
	}
	if _, ok := b.Mappings.LookupByCwd("/projects/widget-foo"); !ok {
		t.Error("cwd mapping should have been persisted")
	}
}

func TestAttach_TopicID_ValidatesAndClaims(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/x")

	tid := int64(999)
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/x", TopicID: &tid})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if !ack.OK {
		t.Fatalf("attach by id failed: %s", ack.Err)
	}
	if len(fc.validateCalls) != 1 || fc.validateCalls[0].tid != 999 {
		t.Errorf("expected one ValidateTopic call for 999, got %+v", fc.validateCalls)
	}
	if _, ok := b.Mappings.LookupTopicByID("telegram", -100, 999); !ok {
		t.Error("placeholder topic should be in registry after attach-by-id")
	}
}

func TestAttach_SavedMapping_SilentClaimNoSearch(t *testing.T) {
	mf := mfWithTelegram()
	mf.Mappings["/projects/widget"] = mappings.Mapping{
		Channel: "telegram", ChatID: -100, TopicID: 281,
		Name: "c3", Group: "main",
	}
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	peer, done := peerPair(t, b)
	defer done()
	helloAck(t, peer, "/projects/widget")

	// User says `attach widget` — saved mapping wins regardless of name search.
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/widget", Name: "widget"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if !ack.OK {
		t.Fatalf("saved-mapping attach failed: %s", ack.Err)
	}
	if ack.NeedsConfirmation {
		t.Error("saved mapping should be silent claim")
	}
	if ack.Name != "c3" {
		t.Errorf("Name=%q, want c3 (from saved mapping, NOT widget)", ack.Name)
	}
}

func TestAttach_AlreadyClaimed_ReturnsHolderError(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	// First connection claims c3 topic.
	peer1, done1 := peerPair(t, b)
	defer done1()
	helloAck(t, peer1, "/p1")
	_ = peer1.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/p1", Name: "c3"})
	raw, _ := peer1.ReadFrame()
	var ack1 ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack1)
	if !ack1.OK {
		t.Fatalf("first attach failed: %s", ack1.Err)
	}

	// Second connection tries the same — should fail.
	peer2, done2 := peerPair(t, b)
	defer done2()
	helloAck(t, peer2, "/p2")
	_ = peer2.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/p2", Name: "c3"})
	raw2, _ := peer2.ReadFrame()
	var ack2 ipc.AttachedMsg
	_ = json.Unmarshal(raw2, &ack2)
	if ack2.OK {
		t.Error("second attach to held route should fail")
	}
	if ack2.Err == "" {
		t.Error("expected error message identifying the holder")
	}
}

// _ = filepath kept to silence the unused-import warning since this test
// file may evolve; remove if removed elsewhere.
var _ = filepath.Join
var _ = time.Now
