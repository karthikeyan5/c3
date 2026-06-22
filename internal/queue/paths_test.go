package queue

import (
	"path/filepath"
	"testing"
)

func TestRouteKeyFile_TopicAndDM(t *testing.T) {
	tid := int64(914)
	withTopic := RouteKey{Channel: "telegram", ChatID: -1003990699908, TopicID: &tid}.File()
	if withTopic != "telegram__-1003990699908__914" {
		t.Errorf("topic file = %q, want telegram__-1003990699908__914", withTopic)
	}
	dm := RouteKey{Channel: "telegram", ChatID: 12345, TopicID: nil}.File()
	if dm != "telegram__12345__none" {
		t.Errorf("dm file = %q, want telegram__12345__none", dm)
	}
}

func TestQueueDir_EnvOverrideAndXDG(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", "/custom/q")
	if got := QueueDir(); got != "/custom/q" {
		t.Errorf("override QueueDir = %q, want /custom/q", got)
	}
	t.Setenv("C3_QUEUE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xs")
	if got := QueueDir(); got != filepath.Join("/tmp/xs", "c3", "queue") {
		t.Errorf("xdg QueueDir = %q", got)
	}
}
