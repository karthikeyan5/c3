// Package mcp is a minimal MCP stdio server. It speaks JSON-RPC 2.0
// newline-framed over stdin/stdout. The writer is mutex-protected so
// background goroutines (e.g. the broker → adapter inbound forwarder) can
// emit notifications/claude/channel frames concurrently with synchronous
// request/response handling without interleaving.
//
// Spec §4.4.4 — the Go MCP SDK doesn't expose a public Notify(method, params)
// for arbitrary custom notifications, so the Claude adapter manually frames
// the JSON-RPC envelope. This package keeps the framing logic isolated from
// the broker IPC to avoid any cross-package writer-mutex confusion.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Handler is the application-level dispatcher for MCP requests. The server
// dispatches each parsed Request to Handler.Dispatch and writes back the
// returned Response (if any). Handler may also call server.Notify to push
// unsolicited notifications at any time from any goroutine.
type Handler interface {
	Dispatch(ctx context.Context, req *Request) *Response
}

// Server runs the MCP stdio loop with a write mutex so notifications and
// responses don't interleave on stdout.
type Server struct {
	r       io.Reader
	w       io.Writer
	wmu     sync.Mutex
	handler Handler
}

// New returns a Server reading from r, writing to w, dispatching to handler.
// In production: r=os.Stdin, w=os.Stdout.
func New(r io.Reader, w io.Writer, handler Handler) *Server {
	return &Server{r: r, w: w, handler: handler}
}

// Run reads requests until r returns EOF or ctx is canceled. Returns when the
// stdin loop exits.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024) // up to 4MB per frame

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			// Malformed: respond with error if we can find an id.
			s.replyParseError(line)
			continue
		}
		if resp := s.handler.Dispatch(ctx, &req); resp != nil {
			s.writeJSON(resp)
		}
	}
	return scanner.Err()
}

// Notify writes an unsolicited notification frame. Safe for concurrent use.
func (s *Server) Notify(method string, params any) error {
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		frame["params"] = params
	}
	return s.writeJSON(frame)
}

// writeJSON marshals v and writes one newline-terminated frame. Mutex-guarded.
func (s *Server) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcp: marshal: %w", err)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.w.Write(data); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

func (s *Server) replyParseError(raw []byte) {
	// Try to extract id even from malformed JSON.
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(raw, &probe)
	resp := Response{
		JSONRPC: "2.0",
		ID:      probe.ID,
		Error:   &Error{Code: -32700, Message: "Parse error"},
	}
	s.writeJSON(resp)
}

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification returns true when the request has no id (per spec, a
// notification — server must not respond).
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC error payload.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
