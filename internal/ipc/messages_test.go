package ipc

import (
	"encoding/json"
	"testing"
)

func TestHelloMsg_Roundtrip(t *testing.T) {
	in := HelloMsg{
		Op:           OpHello,
		CLI:          "claude",
		PID:          12345,
		CWD:          "/home/u/proj",
		Capabilities: []string{"claude/channel"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out HelloMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Op != OpHello || out.CLI != "claude" || out.PID != 12345 {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestPeekOp_Hello(t *testing.T) {
	raw := `{"op":"hello","cli":"claude","pid":1,"cwd":"/x"}`
	op, err := PeekOp([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if op != OpHello {
		t.Errorf("got op=%q, want %q", op, OpHello)
	}
}

func TestPeekOp_MissingOp(t *testing.T) {
	raw := `{"cli":"claude"}`
	_, err := PeekOp([]byte(raw))
	if err == nil {
		t.Error("expected error for missing op, got nil")
	}
}

func TestErrorMsg_Roundtrip(t *testing.T) {
	in := ErrorMsg{Op: OpError, Err: "broker unavailable"}
	data, _ := json.Marshal(in)
	var out ErrorMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Err != "broker unavailable" {
		t.Errorf("Err=%q, want broker unavailable", out.Err)
	}
}
