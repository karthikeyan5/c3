// Package mcp is a minimal MCP stdio server. It speaks JSON-RPC 2.0 over
// stdin/stdout, accepting both newline-delimited JSON and Content-Length
// framing. The writer is mutex-protected so
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
	"strconv"
	"strings"
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
	fmu     sync.Mutex
	framing string
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
	reader := bufio.NewReaderSize(s.r, 4*1024*1024)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := s.readFrame(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
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
}

func (s *Server) readFrame(r *bufio.Reader) ([]byte, error) {
	for {
		b, err := r.Peek(1)
		if err != nil {
			return nil, err
		}
		switch b[0] {
		case '\n', '\r':
			_, _ = r.ReadByte()
			continue
		case '{':
			line, err := r.ReadBytes('\n')
			if err != nil && err != io.EOF {
				return nil, err
			}
			s.setFraming("newline")
			return []byte(strings.TrimSpace(string(line))), nil
		default:
			return s.readHeaderFrame(r)
		}
	}
}

func (s *Server) readHeaderFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return []byte(line), nil
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return []byte(line), nil
			}
			length = n
		}
	}
	if length < 0 {
		return []byte{}, nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	s.setFraming("content-length")
	return buf, nil
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
	if s.currentFraming() == "content-length" {
		if _, err := fmt.Fprintf(s.w, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
			return err
		}
		if _, err := s.w.Write(data); err != nil {
			return err
		}
		return nil
	}
	if _, err := s.w.Write(data); err != nil {
		return err
	}
	_, err = s.w.Write([]byte{'\n'})
	return err
}

func (s *Server) setFraming(framing string) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	if s.framing == "" {
		s.framing = framing
	}
}

func (s *Server) currentFraming() string {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.framing
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
