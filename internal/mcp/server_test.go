package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

type echoHandler struct{}

func (echoHandler) Dispatch(_ context.Context, req *Request) *Response {
	if req.IsNotification() {
		return nil
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"echoed_method": req.Method},
	}
}

func TestServer_Roundtrip(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	var out bytes.Buffer

	srv := New(in, &out, echoHandler{})
	if err := srv.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc=%q", resp.JSONRPC)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id=%q", resp.ID)
	}
}

func TestServer_NotificationProducesNoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer

	srv := New(in, &out, echoHandler{})
	if err := srv.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no response for notification, got %d bytes: %s", out.Len(), out.String())
	}
}

func TestServer_NotifyConcurrentWithDispatch(t *testing.T) {
	// Build a request stream of N requests; for each, a goroutine fires a
	// Notify in parallel. We assert that every output line parses as JSON
	// (i.e. no interleaving).
	const N = 20
	var inBuf bytes.Buffer
	for i := 0; i < N; i++ {
		fmt := `{"jsonrpc":"2.0","id":` + itoa(i) + `,"method":"ping"}` + "\n"
		inBuf.WriteString(fmt)
	}
	var out bytes.Buffer

	srv := New(&inBuf, &out, echoHandler{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Run(context.Background())
	}()

	// Hammer Notify concurrently with the request loop.
	for i := 0; i < N; i++ {
		go func(i int) {
			_ = srv.Notify("notifications/claude/channel", map[string]any{
				"content": []map[string]any{{"type": "text", "text": "msg" + itoa(i)}},
				"meta":    map[string]any{"seq": i},
			})
		}(i)
	}
	wg.Wait()

	// Every line should parse as JSON.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Errorf("line %d not valid JSON: %s", i, line)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
