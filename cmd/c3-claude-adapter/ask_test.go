package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karthikeyan5/c3/internal/ipc"
)

func askRequest(t *testing.T, question string, options []string) *mcp.CallToolRequest {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"question": question, "options": options})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: raw}}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// TestAskTool_ReturnsTappedAnswer drives the full adapter-side blocking flow over
// a net.Pipe fake broker: toolAsk sends an AskRegisterReq, the broker acks
// OK, then pushes the tapped answer as an OpAskResult — and the tool returns that
// answer string ("B") as its result.
func TestAskTool_ReturnsTappedAnswer(t *testing.T) {
	a, peer := adapterWithConn(t)

	resultCh := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := a.toolAsk(context.Background(), askRequest(t, "Pick one", []string{"A", "B", "C"}))
		resultCh <- res
	}()

	// The tool must send an AskRegisterReq carrying the question + options + askID.
	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatalf("read ask_register frame: %v", err)
	}
	var reg ipc.AskRegisterReq
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("unmarshal ask_register: %v", err)
	}
	if reg.Op != ipc.OpAskRegister {
		t.Fatalf("op = %q, want %q", reg.Op, ipc.OpAskRegister)
	}
	if reg.Question != "Pick one" || len(reg.Options) != 3 {
		t.Fatalf("unexpected ask_register payload: %+v", reg)
	}
	if reg.AskID == "" {
		t.Fatal("ask_register must carry a generated ask id")
	}

	// Broker acks registration OK, then pushes the tapped answer "B".
	a.dispatchAskRegistered(mustMarshal(t, ipc.AskRegisteredMsg{
		Op: ipc.OpAskRegistered, AskID: reg.AskID, OK: true, MessageID: 99,
	}))
	a.dispatchAskResult(mustMarshal(t, ipc.AskResultMsg{
		Op: ipc.OpAskResult, AskID: reg.AskID, Answer: ipc.AskAnswer{Selected: []string{"B"}},
	}))

	select {
	case res := <-resultCh:
		if res.IsError {
			t.Fatalf("unexpected tool error: %q", resultText(t, res))
		}
		if got := resultText(t, res); got != "B" {
			t.Fatalf("tool result = %q, want %q", got, "B")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask tool did not return after the answer was pushed")
	}
}

// TestAskTool_Timeout: once registered, if no answer is pushed within the answer
// timeout the tool returns a (non-error) result that reports the timeout so the
// agent can recover.
func TestAskTool_Timeout(t *testing.T) {
	prev := askAnswerTimeout
	askAnswerTimeout = 30 * time.Millisecond
	defer func() { askAnswerTimeout = prev }()

	a, peer := adapterWithConn(t)

	resultCh := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := a.toolAsk(context.Background(), askRequest(t, "Pick one", []string{"A", "B"}))
		resultCh <- res
	}()

	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatalf("read ask_register frame: %v", err)
	}
	var reg ipc.AskRegisterReq
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("unmarshal ask_register: %v", err)
	}

	// Ack registration OK but NEVER push an answer → the tool must time out.
	a.dispatchAskRegistered(mustMarshal(t, ipc.AskRegisteredMsg{
		Op: ipc.OpAskRegistered, AskID: reg.AskID, OK: true,
	}))

	select {
	case res := <-resultCh:
		got := strings.ToLower(resultText(t, res))
		if !strings.Contains(got, "timed out") {
			t.Fatalf("timeout result = %q, want it to report a timeout", resultText(t, res))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask tool did not return on timeout")
	}
}

// TestAskTool_BailsFastOnRegisterFailure: an OK=false registration ack (e.g.
// ask-before-attach / oversized keyboard) must return the broker's error as the
// tool result immediately, without waiting for the answer timeout.
func TestAskTool_BailsFastOnRegisterFailure(t *testing.T) {
	a, peer := adapterWithConn(t)

	resultCh := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := a.toolAsk(context.Background(), askRequest(t, "Pick one", []string{"A", "B"}))
		resultCh <- res
	}()

	raw, err := peer.ReadFrame()
	if err != nil {
		t.Fatalf("read ask_register frame: %v", err)
	}
	var reg ipc.AskRegisterReq
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("unmarshal ask_register: %v", err)
	}

	a.dispatchAskRegistered(mustMarshal(t, ipc.AskRegisteredMsg{
		Op: ipc.OpAskRegistered, AskID: reg.AskID, OK: false, Err: "ask before attach: no route claimed",
	}))

	select {
	case res := <-resultCh:
		if !res.IsError {
			t.Fatalf("a failed registration must be an in-band tool error; got non-error %q", resultText(t, res))
		}
		if !strings.Contains(resultText(t, res), "ask before attach") {
			t.Fatalf("error result = %q, want it to carry the broker's reason", resultText(t, res))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask tool did not bail fast on a failed registration")
	}
}

// TestAskTool_RequiresOptions: Phase 1 is single-select; an ask without options
// is rejected locally with a clear error (no broker round-trip).
func TestAskTool_RequiresOptions(t *testing.T) {
	a, _ := adapterWithConn(t)
	res, _ := a.toolAsk(context.Background(), askRequest(t, "Free text?", nil))
	if !res.IsError {
		t.Fatalf("ask without options must be an error in Phase 1; got %q", resultText(t, res))
	}
}
