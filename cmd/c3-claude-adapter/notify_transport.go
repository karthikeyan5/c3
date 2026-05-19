// notifyTransport is a thin wrapper around an mcp.Transport that captures
// the live mcp.Connection so the adapter can emit MCP notifications under
// arbitrary method strings (specifically Claude Code's custom
// `notifications/claude/channel` method that the official SDK doesn't
// register in its sending-method table).
//
// SDK background: the modelcontextprotocol/go-sdk's ServerSession only
// exposes typed Notify* methods for spec-defined notifications
// (NotifyProgress, Log, …). Custom notification methods are not in
// `clientMethodInfos`, so `defaultSendingMethodHandler` returns
// `jsonrpc2.ErrNotHandled`. The supported escape hatch is the Transport
// layer: a Transport hands out a `mcp.Connection`, and `Connection.Write`
// accepts any `jsonrpc.Message`. We hold the live Connection in this
// wrapper and synthesize `*jsonrpc.Request` notifications (no ID → it
// serializes as a notification) with our custom method on demand.
//
// Wire shape: `jsonrpc.EncodeMessage(msg) + '\n'` (see SDK's
// transport.go:617-665 ioConn.Write). This is byte-identical to what
// the hand-rolled MCP server emitted before migration:
//
//	{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{…}}\n
package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// notifyTransport wraps an inner Transport and exposes the live Connection
// for custom notifications.
type notifyTransport struct {
	inner mcp.Transport

	mu   sync.Mutex
	conn mcp.Connection
}

func newNotifyTransport(inner mcp.Transport) *notifyTransport {
	return &notifyTransport{inner: inner}
}

// Connect implements mcp.Transport. It delegates to the wrapped transport
// and captures the resulting Connection.
func (t *notifyTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = c
	t.mu.Unlock()
	return c, nil
}

// Disconnect clears the stored Connection so a subsequent Notify will
// return the "connection not yet established" sentinel error rather
// than writing to a possibly-dead Connection.
//
// The adapter does NOT call this today: the SDK's IOTransport /
// StdioTransport do not transparently reconnect — they fail-and-let-
// caller-restart, and the adapter restarts the whole process on a
// dropped transport. The method exists as preventative scaffolding for
// a future SDK version that adds transparent reconnect. If/when the
// SDK gains that capability, the adapter should call Disconnect
// before the reconnect path so the wrapper's captured Connection
// reference doesn't outlive the underlying transport.
//
// Closes report MINOR m2 (2026-05-19).
func (t *notifyTransport) Disconnect() {
	t.mu.Lock()
	t.conn = nil
	t.mu.Unlock()
}

// Notify writes a JSON-RPC notification with the given method and params
// onto the captured connection. params is JSON-marshaled with the same
// settings the SDK uses internally (no HTML escaping is handled by the
// jsonrpc encoder downstream from us).
func (t *notifyTransport) Notify(ctx context.Context, method string, params any) error {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c == nil {
		return errors.New("notifyTransport: connection not yet established")
	}
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = data
	}
	// A jsonrpc.Request with no ID serializes as a notification
	// (id field omitted via omitempty on the wire struct).
	msg := &jsonrpc.Request{Method: method, Params: raw}
	return c.Write(ctx, msg)
}
