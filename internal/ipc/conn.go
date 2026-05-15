package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Conn wraps a net.Conn with newline-JSON framing and a write mutex. Multiple
// goroutines can safely call WriteJSON concurrently; only one frame at a time
// reaches the wire.
//
// Spec §4.4.1: "the IPC socket is duplex and a single connection carries
// both request/response (synchronous tool calls) and unsolicited push
// (broker → adapter inbound). Both sides use a single bufio.Writer guarded
// by a sync.Mutex; line-by-line frames are atomic."
type Conn struct {
	c   net.Conn
	w   *bufio.Writer
	wmu sync.Mutex
	r   *bufio.Reader
}

// maxFrameSize bounds the largest IPC frame we'll accept on a single
// ReadFrame call. Without a cap a peer (broker, adapter, or any process
// that opened the socket) could stream bytes without a newline and
// exhaust memory. 4 MiB is well above any legitimate frame: the largest
// real frames are MCP tool-call results that embed Telegram message
// content, on the order of tens of KB. Mirrors mcp.Server's bufio buffer
// size (internal/mcp/server.go).
const maxFrameSize = 4 * 1024 * 1024

// NewConn wraps a net.Conn. Owner is responsible for calling Close.
func NewConn(c net.Conn) *Conn {
	return &Conn{
		c: c,
		w: bufio.NewWriter(c),
		r: bufio.NewReaderSize(c, maxFrameSize),
	}
}

// WriteJSON marshals v and writes one newline-terminated frame to the wire.
// Safe for concurrent use.
func (c *Conn) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(data); err != nil {
		return err
	}
	if err := c.w.WriteByte('\n'); err != nil {
		return err
	}
	return c.w.Flush()
}

// ReadFrame reads one \n-terminated frame and returns its bytes (without the
// trailing newline). Returns io.EOF when the peer closes cleanly. Returns
// an error if the frame exceeds maxFrameSize (defense against a peer
// streaming bytes without a newline).
//
// NOT safe for concurrent use — only one reader goroutine per Conn.
func (c *Conn) ReadFrame() ([]byte, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("ipc: frame exceeds %d bytes (peer not respecting newline framing)", maxFrameSize)
		}
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			return line, nil
		}
		return nil, err
	}
	n := len(line)
	if n > 0 && line[n-1] == '\n' {
		n--
		if n > 0 && line[n-1] == '\r' {
			n--
		}
	}
	return line[:n], nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.c.Close()
}
