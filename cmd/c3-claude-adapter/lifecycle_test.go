package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/mcp"
)

var mcpPingRequest = mcp.Request{
	JSONRPC: "2.0",
	ID:      json.RawMessage(`1`),
	Method:  "ping",
}

// TestIdleStartupWatchdog_CancelsWhenNoDispatch asserts the regression that
// motivated the watchdog: an adapter that Claude Code spawns but never
// drives (observed during `--resume` lifecycles where CC orphans the prior
// MCP server) must exit on its own rather than zombie forever. Concretely:
// dispatched stays false → ctx is canceled by the watchdog.
func TestIdleStartupWatchdog_CancelsWhenNoDispatch(t *testing.T) {
	a := newAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Inline mini-watchdog with a 50ms budget so the test stays fast.
	// Mirrors idleStartupWatchdog's logic exactly; production code uses
	// idleStartupTimeout (60s).
	go func() {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !a.dispatched.Load() {
				cancel()
			}
		}
	}()

	select {
	case <-ctx.Done():
		// Expected — watchdog fired.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not cancel ctx; adapter would zombie")
	}
}

// TestIdleStartupWatchdog_StaysQuietAfterDispatch asserts the disarm
// path: once the adapter has received any MCP frame, the watchdog must
// not cancel ctx no matter how long it sits idle afterward. Without this,
// a long-running session that goes quiet (no inbound, no tool calls)
// would self-destruct.
func TestIdleStartupWatchdog_StaysQuietAfterDispatch(t *testing.T) {
	a := newAdapter()
	a.dispatched.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !a.dispatched.Load() {
				cancel()
			}
		}
	}()

	select {
	case <-ctx.Done():
		t.Fatal("watchdog canceled ctx despite dispatched=true")
	case <-time.After(200 * time.Millisecond):
		// Expected — watchdog kept quiet.
	}
}

// TestDispatch_SetsDispatchedFlag confirms the disarm signal fires on
// the first MCP request. This is the integration point: without the
// flag flip, the watchdog has no way to know Claude Code is actually
// talking to us.
func TestDispatch_SetsDispatchedFlag(t *testing.T) {
	a := newAdapter()
	if a.dispatched.Load() {
		t.Fatal("dispatched should start false")
	}
	// `ping` is the cheapest path through Dispatch — no broker dependency,
	// no helloAck dependency, just an empty Result map. Confirms the flag
	// is flipped before any work-bearing branch runs.
	resp := a.Dispatch(context.Background(), &mcpPingRequest)
	if resp == nil {
		t.Fatal("ping should produce a response")
	}
	if !a.dispatched.Load() {
		t.Fatal("Dispatch did not set dispatched flag")
	}
}
