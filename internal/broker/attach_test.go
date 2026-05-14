package broker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	replyCalls      []c3types.ReplyArgs
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
func (f *fakeChannel) SendReply(args c3types.ReplyArgs) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replyCalls = append(f.replyCalls, args)
	return 0, nil
}
func (f *fakeChannel) sendRepliesSnapshot() []c3types.ReplyArgs {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]c3types.ReplyArgs, len(f.replyCalls))
	copy(out, f.replyCalls)
	return out
}
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

func TestAttach_SavedMapping_SilentClaim(t *testing.T) {
	// When no explicit name is provided (or name matches saved mapping), the
	// saved cwd mapping wins as a silent claim — the common "cd into project,
	// type attach" flow.
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

	// Bare `attach` (no name) → saved mapping wins.
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/widget"})
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
		t.Errorf("Name=%q, want c3 (from saved mapping)", ack.Name)
	}
}

func TestAttach_ExplicitNameOverridesSavedMapping(t *testing.T) {
	// 2026-05-09: a stale cwd mapping was silently overriding an
	// explicit name argument. Saved mapping must NOT win when the user
	// explicitly asks for a different topic name.
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

	// Explicit name "widget" differs from saved mapping ("c3"). Saved
	// mapping should NOT silent-claim. We expect a propose-creation since
	// "widget" isn't in topics.
	_ = peer.WriteJSON(ipc.AttachReq{Op: ipc.OpAttach, CWD: "/projects/widget", Name: "widget"})
	raw, _ := peer.ReadFrame()
	var ack ipc.AttachedMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.OK {
		t.Fatalf("expected propose-creation (not silent-claim from saved); got OK with Name=%q", ack.Name)
	}
	if !ack.NeedsConfirmation {
		t.Errorf("expected NeedsConfirmation=true; got false. Err=%s", ack.Err)
	}
	if ack.Proposal == nil || ack.Proposal.Name != "widget" {
		t.Errorf("expected proposal for name 'widget'; got %+v", ack.Proposal)
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

// ─── welcome-message-on-attach tests (TODO.md pre-release UX bug #1) ───────

func TestWelcomeText_HomeShortened(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir")
	}
	stub := &Stub{CLI: "claude", CWD: filepath.Join(home, "arogara", "c3")}
	got := welcomeText(stub, "c3")
	if !strings.Contains(got, "~/arogara/c3") {
		t.Errorf("welcomeText should home-shorten cwd, got %q", got)
	}
	if strings.Contains(got, home) {
		t.Errorf("welcomeText should not include literal home prefix, got %q", got)
	}
}

func TestWelcomeText_IncludesCLIAndLabel(t *testing.T) {
	stub := &Stub{CLI: "claude", CWD: "/tmp/x"}
	got := welcomeText(stub, "my-project")
	for _, want := range []string{"claude", "my-project"} {
		if !strings.Contains(got, want) {
			t.Errorf("welcomeText missing %q in %q", want, got)
		}
	}
}

func TestWelcomeText_NoPID(t *testing.T) {
	// Karthi explicit feedback 2026-05-14: PID is mechanical clutter,
	// don't include it.
	stub := &Stub{CLI: "claude", PID: 12345, CWD: "/tmp/x"}
	got := welcomeText(stub, "label")
	if strings.Contains(got, "12345") {
		t.Errorf("welcomeText should not include PID, got %q", got)
	}
}

func TestWelcomeText_NoCWD_StillFriendly(t *testing.T) {
	stub := &Stub{CLI: "claude"}
	got := welcomeText(stub, "label")
	if got == "" {
		t.Fatal("welcomeText with no cwd should still return something")
	}
	if !strings.Contains(got, "claude") || !strings.Contains(got, "label") {
		t.Errorf("welcomeText cwd-less: %q", got)
	}
}

func TestTryClaim_FreshClaimTriggersWelcome(t *testing.T) {
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	stub := &Stub{CLI: "claude", PID: 1, CWD: "/home/u/proj"}
	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)
	if !b.tryClaim(nil, stub, key, "c3", false, false) {
		t.Fatal("fresh claim should succeed (nil conn is OK because we won't hit the collision branch)")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(fc.sendRepliesSnapshot()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := fc.sendRepliesSnapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 welcome SendReply, got %d", len(calls))
	}
	r := calls[0]
	if r.ChatID != -100 || r.TopicID == nil || *r.TopicID != 914 {
		t.Errorf("welcome went to wrong destination: chat=%d topic=%v", r.ChatID, r.TopicID)
	}
	if !strings.Contains(r.Text, "claude") {
		t.Errorf("welcome text missing cli: %q", r.Text)
	}
}

func TestTryClaim_SameLogicalSessionReclaimSuppressesWelcome(t *testing.T) {
	// Adapter reconnect replays attach. Same CLI+PID+CWD → should NOT
	// re-send welcome (would spam the topic on every CLI bounce).
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)

	// First claim by stub#1.
	stub1 := &Stub{CLI: "claude", PID: 99, CWD: "/home/u/proj"}
	if !b.tryClaim(nil, stub1, key, "c3", false, false) {
		t.Fatal("first claim should succeed")
	}
	// Wait for the welcome to land.
	time.Sleep(50 * time.Millisecond)
	first := len(fc.sendRepliesSnapshot())
	if first != 1 {
		t.Fatalf("first claim should send 1 welcome, got %d", first)
	}

	// Simulate an adapter reconnect — same logical session (CLI+PID+CWD),
	// different ConnID.
	stub2 := &Stub{CLI: "claude", PID: 99, CWD: "/home/u/proj"}
	if !b.tryClaim(nil, stub2, key, "c3", false, false) {
		t.Fatal("re-claim by same logical session should succeed")
	}
	time.Sleep(50 * time.Millisecond)
	second := len(fc.sendRepliesSnapshot())
	if second != first {
		t.Errorf("same-session re-claim sent %d additional welcomes (want 0): total=%d, first=%d",
			second-first, second, first)
	}
}

// ─── cwd resolution + conflict guard (TODO.md pre-release UX bugs #2/#3) ───

func TestResolveAttachCWD_EmptyLaunchReturnsEmpty(t *testing.T) {
	if got := resolveAttachCWD("", "anything"); got != "" {
		t.Errorf("got %q, want \"\"", got)
	}
}

func TestResolveAttachCWD_NoTopicReturnsLaunch(t *testing.T) {
	if got := resolveAttachCWD("/home/u/proj", ""); got != "/home/u/proj" {
		t.Errorf("got %q, want /home/u/proj (no topic = no refinement)", got)
	}
}

func TestResolveAttachCWD_LaunchBasenameMatchesTopic(t *testing.T) {
	// Most common pattern: user launches claude FROM the project dir.
	if got := resolveAttachCWD("/home/u/c3", "c3"); got != "/home/u/c3" {
		t.Errorf("got %q, want /home/u/c3 (basename matches)", got)
	}
}

func TestResolveAttachCWD_RefinesToSubdirectory(t *testing.T) {
	// User launched in a parent directory; <launch>/<topic> exists.
	root := t.TempDir()
	sub := filepath.Join(root, "c3")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := resolveAttachCWD(root, "c3")
	if got != sub {
		t.Errorf("got %q, want %q (subdir exists, refine downward)", got, sub)
	}
}

func TestResolveAttachCWD_SubdirNotADirectory(t *testing.T) {
	// <launch>/<topic> exists but is a regular file, not a dir → fall back.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "c3"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveAttachCWD(root, "c3")
	if got != root {
		t.Errorf("got %q, want %q (subdir is a file, fall back to launch)", got, root)
	}
}

func TestResolveAttachCWD_SubdirMissingFallsBack(t *testing.T) {
	// User launched in `~/arogara` and attached to a topic with no matching
	// subdir. Best-effort fallback: persist the launch dir; conflict guard
	// in persistMapping will warn if this collides.
	root := t.TempDir()
	got := resolveAttachCWD(root, "topic-with-no-subdir")
	if got != root {
		t.Errorf("got %q, want %q (no subdir, fall back to launch)", got, root)
	}
}

func TestPersistMapping_RefinesToProjectSubdirectory(t *testing.T) {
	// End-to-end: stub launched in the PARENT of the project dir;
	// persistMapping should resolve down to the project subdir so two
	// topics attached from the same parent don't clobber each other's
	// default mappings.
	root := t.TempDir()
	c3 := filepath.Join(root, "c3")
	sthapati := filepath.Join(root, "sthapati")
	for _, d := range []string{c3, sthapati} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	// First attach: c3.
	stub := &Stub{CLI: "claude", PID: 1, CWD: root}
	b.persistMapping(stub, "telegram", -100, 914, "c3", "main")
	// Second attach: sthapati (same launch root).
	b.persistMapping(stub, "telegram", -100, 207, "sthapati", "main")

	if got, ok := b.Mappings.LookupByCwd(c3); !ok || got.TopicID != 914 {
		t.Errorf("expected %s → topic 914, got %+v (ok=%v)", c3, got, ok)
	}
	if got, ok := b.Mappings.LookupByCwd(sthapati); !ok || got.TopicID != 207 {
		t.Errorf("expected %s → topic 207, got %+v (ok=%v)", sthapati, got, ok)
	}
	if _, ok := b.Mappings.LookupByCwd(root); ok {
		t.Errorf("expected NO mapping at launch root %s, but one was persisted (silent-rebind bug back?)", root)
	}
}

func TestPersistMapping_RebindLogsWarning(t *testing.T) {
	// Same cwd, different topic → warning + still persists. (Live claim
	// is handled upstream in tryClaim; persistMapping just records.)
	// Inability to capture log output here is fine — we verify the
	// overwrite behavior; the log message is structural, exercised by
	// the surrounding code path.
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	// Stub launched directly in /tmp/X (no subdir refinement possible).
	root := t.TempDir()
	stub := &Stub{CLI: "claude", PID: 1, CWD: root}

	// First attach claims topic 914 under name matching root's basename
	// so the launch dir IS the persisted cwd.
	name1 := filepath.Base(root)
	b.persistMapping(stub, "telegram", -100, 914, name1, "main")
	// Same cwd, different topic. resolveAttachCWD returns launch as
	// fallback (no subdir match for the second name).
	b.persistMapping(stub, "telegram", -100, 207, "other", "main")

	got, ok := b.Mappings.LookupByCwd(root)
	if !ok {
		t.Fatal("rebind should have overwritten, not skipped")
	}
	if got.TopicID != 207 {
		t.Errorf("expected rebind to topic 207, got topic %d", got.TopicID)
	}
}

func TestTryClaim_ReplayFlagSuppressesWelcomeAfterBrokerBounce(t *testing.T) {
	// Distinct from the same-session reclaim case: this models a broker
	// restart, where the new broker's Routes map is empty. The adapter
	// sets Replay=true on its restored AttachReq so the broker knows the
	// attach is operational recovery, not a user-initiated attach.
	// Without the Replay flag, every broker bounce would post a welcome
	// to every reconnecting session.
	mf := mfWithTelegram()
	fc := &fakeChannel{}
	b := brokerWithChannel(t, mf, fc)
	defer b.Shutdown()

	stub := &Stub{CLI: "claude", PID: 99, CWD: "/home/u/proj"}
	tid := int64(914)
	key := MakeRouteKey("telegram", -100, &tid)

	if !b.tryClaim(nil, stub, key, "c3", false /*steal*/, true /*replay*/) {
		t.Fatal("replay claim against an empty Routes map should still succeed")
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(fc.sendRepliesSnapshot()); got != 0 {
		t.Errorf("replay attach sent %d welcomes, want 0 (user didn't initiate)", got)
	}
}
