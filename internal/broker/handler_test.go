package broker

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/ipc"
	"github.com/karthikeyan5/c3/internal/mappings"
)

func runHandlerWithPeer(t *testing.T, mf *mappings.MappingsFile) (*ipc.Conn, func()) {
	t.Helper()
	a, b := net.Pipe()
	br := &Broker{
		Mappings: mf,
		Stubs:    NewStubRegistry(),
		Routes:   NewRoutes(),
	}
	go br.HandleConn(a)
	return ipc.NewConn(b), func() {
		_ = a.Close()
		_ = b.Close()
	}
}

func emptyMappings() *mappings.MappingsFile {
	return &mappings.MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]mappings.ChannelConfig{},
		Mappings:      map[string]mappings.Mapping{},
	}
}

func TestHandle_HelloAck_NoConfig(t *testing.T) {
	mf := &mappings.MappingsFile{SchemaVersion: 1}
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	if err := peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"}); err != nil {
		t.Fatal(err)
	}
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var ack ipc.HelloAckMsg
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Op != ipc.OpHelloAck {
		t.Errorf("op=%q, want hello_ack", ack.Op)
	}
	if !ack.NoConfig {
		t.Error("expected NoConfig=true when channels map is empty")
	}
	if ack.ConnID == 0 {
		t.Error("ConnID should be assigned")
	}
}

func TestHandle_HelloAck_NoMapping(t *testing.T) {
	mf := emptyMappings()
	mf.Channels["telegram"] = mappings.ChannelConfig{DefaultGroup: "main"}
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/unknown"})
	raw, _ := peer.ReadFrame()
	var ack ipc.HelloAckMsg
	_ = json.Unmarshal(raw, &ack)

	if ack.NoConfig {
		t.Error("NoConfig should be false when channel exists")
	}
	if !ack.NoMapping {
		t.Error("NoMapping should be true for unknown cwd")
	}
}

func TestHandle_ListTopics(t *testing.T) {
	mf := emptyMappings()
	mf.Channels["telegram"] = mappings.ChannelConfig{
		DefaultGroup: "main",
		Groups:       map[string]mappings.GroupConfig{"main": {ChatID: -100}},
		Topics: []mappings.Topic{
			{ChatID: -100, TopicID: 281, Name: "c3", Group: "main"},
		},
	}
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	_, _ = peer.ReadFrame() // consume hello_ack

	_ = peer.WriteJSON(ipc.ListTopicsReq{Op: ipc.OpListTopics})
	raw, _ := peer.ReadFrame()

	var resp ipc.TopicsListMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Op != ipc.OpTopicsList {
		t.Errorf("op=%q, want topics_list", resp.Op)
	}
	if len(resp.Topics) != 1 || resp.Topics[0].Name != "c3" {
		t.Errorf("topics = %+v, want one entry name=c3", resp.Topics)
	}
}

func TestHandle_ByeClosesCleanly(t *testing.T) {
	mf := emptyMappings()
	peer, done := runHandlerWithPeer(t, mf)
	defer done()

	_ = peer.WriteJSON(ipc.HelloMsg{Op: ipc.OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	_, _ = peer.ReadFrame()

	_ = peer.WriteJSON(ipc.ByeReq{Op: ipc.OpBye})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := peer.ReadFrame()
		if err == io.EOF {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("broker did not close conn after bye within 2s")
}
