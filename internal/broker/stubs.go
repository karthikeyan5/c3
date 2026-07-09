package broker

import (
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Stub is the broker's view of a connected adapter. ConnID is the
// late-result-discard token described in spec §4.5.1.
//
// A stub is "alive" if (a) Conn is non-nil OR (b) Conn is nil but its PID
// is still alive in the OS process table. Claims tied to a stub stay valid
// for as long as the stub is alive — a momentary conn drop does NOT release
// the claim. This keeps the broker as the authoritative owner of "who has
// what topic" and prevents racing adapters (codex auto-attach, claude
// reconnect-replay, etc.) from stealing each other's claims during conn
// churn.
//
// Process death is detected via `kill -0 PID` (Linux/Unix). On a
// confirmed-dead holder, Routes.Claim will release the stale claim and
// grant the new one.
type Stub struct {
	CLI    string
	PID    int
	CWD    string
	ConnID uint64

	// Conn is opaque from the registry's POV — broker package wires it after
	// constructing, used by route workers to write inbound to the right
	// adapter. Type is *ipc.Conn but kept as any here to avoid the import
	// cycle in the registry file. Nil when the stub is in disconnected
	// (waiting-for-reconnect) state.
	connMu sync.RWMutex
	Conn   any

	// Disconnected is the time the conn dropped, or zero if connected.
	// Holders keep their route claims while disconnected as long as the PID
	// is still alive.
	Disconnected time.Time

	// Route is the currently-claimed route for this stub (one per connection
	// in v1; re-attach replaces). nil when unclaimed. Set/cleared by the
	// broker handler under stubMu.
	//
	// hasReplied records whether this connection has successfully dispatched at
	// least one `reply` to its claimed route. It is the deterministic "this
	// session is in Telegram mode" proxy that gates the typing relay (P5 /
	// spec R3): typing is only pulsed for a route whose holder has replied,
	// avoiding "typing…" noise for default CLI-mode sessions that never reply
	// to Telegram. It lives on the per-connection Stub (NOT the per-RouteKey
	// RouteWorker, which outlives sessions) so it resets naturally when a new
	// adapter connects. Guarded by stubMu.
	stubMu sync.Mutex
	Route  *RouteKey
	// routeConfirmed records that the CURRENT claim (Route) was set by a
	// LEGITIMATE claim site — an explicit/own-recover attach through tryClaim or
	// recoverSession — as opposed to any future code path that might bind a route
	// without a real claim. The two destructive consume paths (handleFetchQueue's
	// Ack=true fetch and handleInboundDelivered's live-push ack) refuse to consume
	// unless this is set, so a silent-bind regression can never drain a queue the
	// session didn't choose. It is a property of the current claim, not the
	// connection: SetRoute(nil) (detach/release) clears it, re-arming the tripwire.
	// Honest scope (spec §5): every legitimate claim sets it via MarkRouteConfirmed,
	// so today it is always true on a live claim — this is fail-closed insurance
	// against a future regression, NOT a cure for a misbehaving LLM courier (§8).
	// Deliberately NOT set inside SetRoute(&key): binding a route and CONFIRMING it
	// are separate acts, so a future silent SetRoute leaves the tripwire armed.
	// Guarded by stubMu.
	routeConfirmed bool
	// cannotRender is set from HelloMsg.CannotRenderChannels: the host silently
	// drops channel push notifications (a Claude Code session launched without the
	// development-channels flag — typically a --fork-session background job). When
	// true, forwardOrFallback never marks this holder's durable inbound delivered:
	// human messages fall through to the queue + held-notice (recoverable via
	// fetch_queue) while the session keeps its claim for OUTBOUND. Default false
	// (renderable) so old adapters and the normal fast path are unaffected. Set
	// once at hello via SetCannotRender; read on the delivery path via
	// CanRenderPush. Guarded by stubMu.
	cannotRender bool
	// stableSessionID is the host CLI's STABLE per-session id (Claude: the
	// transcript / --resume id), learned from the SessionStart-hook handoff and
	// delivered to the broker via RecoverSessionReq AFTER hello. The broker keys
	// both recording (persistMapping / recordSessionAttachment) and recovery on
	// it. Empty until a recover op arrives (or never, for non-hook sessions —
	// fail-closed → no recording, no recovery). Guarded by stubMu; mutated via
	// SetStableSessionID.
	stableSessionID string
	hasReplied      bool
}

// MarkDisconnected records that the stub's conn has dropped. The claim
// survives as long as the PID is alive.
func (s *Stub) MarkDisconnected() {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.Conn = nil
	s.Disconnected = time.Now()
}

// Reattach swaps in a fresh conn (e.g., after the adapter reconnects). The
// stub's identity (CLI, PID, CWD) is unchanged; ConnID is bumped by the
// caller before this is invoked. Clears the disconnected timestamp.
func (s *Stub) Reattach(conn any, newConnID uint64) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.Conn = conn
	s.ConnID = newConnID
	s.Disconnected = time.Time{}
}

// IsConnected reports whether the stub currently has an active conn.
func (s *Stub) IsConnected() bool {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.Conn != nil
}

// ConnValue returns the stub's current conn under the conn lock (or nil when
// disconnected). Use this for a race-free read of Conn from outside the handler
// goroutine — e.g. broker.broadcastSystemEvent, which writes to every live
// session concurrently with MarkDisconnected/Reattach.
func (s *Stub) ConnValue() any {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.Conn
}

// IsAlive returns whether the stub is in a state where its claims are
// protected. A connected stub is always alive. A disconnected stub is alive
// if its PID still exists in the OS process table — meaning the user's
// adapter process is still around and we're waiting for it to reconnect.
func (s *Stub) IsAlive() bool {
	if s.IsConnected() {
		return true
	}
	return isPIDAlive(s.PID)
}

// isPIDAlive returns true if a process with the given PID exists.
// Sends signal 0 (a no-op) and checks for ESRCH.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// ESRCH = no such process. Anything else (e.g. EPERM) means we can't
	// signal it but it's still alive.
	return err != syscall.ESRCH
}

// SetRoute atomically sets the stub's current claim. Clearing the route
// (key==nil, e.g. handleRelease's detach) also clears routeConfirmed — "confirmed"
// is a property of the CURRENT claim, so a release re-arms the destructive-consume
// tripwire. Setting a non-nil route deliberately does NOT set routeConfirmed:
// binding and confirming are separate acts (see MarkRouteConfirmed), so a future
// code path that binds a route without a legitimate claim stays fail-closed.
func (s *Stub) SetRoute(key *RouteKey) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	if key == nil {
		s.Route = nil
		s.routeConfirmed = false
		return
	}
	k := *key
	s.Route = &k
}

// MarkRouteConfirmed records that the stub's current route was set by a legitimate
// claim (tryClaim / recoverSession). Called immediately after SetRoute(&key) at
// those two sites. Cleared by SetRoute(nil). See the routeConfirmed field doc for
// why this is separate from SetRoute. Guarded by stubMu.
func (s *Stub) MarkRouteConfirmed() {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.routeConfirmed = true
}

// RouteConfirmed reports whether the current claim was set by a legitimate claim
// site. The destructive consume paths gate on this. Guarded by stubMu.
func (s *Stub) RouteConfirmed() bool {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return s.routeConfirmed
}

// SetStableSessionID records the host CLI's stable per-session id, learned from
// the SessionStart-hook handoff and delivered via RecoverSessionReq. Idempotent;
// last write wins. Guarded by stubMu like SetRoute.
func (s *Stub) SetStableSessionID(id string) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.stableSessionID = id
}

// StableSessionIDValue returns the stub's stable session id (empty until a
// recover op set it, or for a non-hook session). Guarded by stubMu.
func (s *Stub) StableSessionIDValue() string {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return s.stableSessionID
}

// SetCannotRender records whether this session's host silently drops channel
// push notifications (from HelloMsg.CannotRenderChannels). Set once at hello,
// before the stub is claimable. Guarded by stubMu like SetRoute.
func (s *Stub) SetCannotRender(v bool) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.cannotRender = v
}

// CanRenderPush reports whether the broker may push channel notifications to this
// holder (and mark them delivered). False for a host that silently drops them —
// the forked-session blackhole — in which case inbound is held in the queue
// instead. Default true (renderable) for old adapters that never reported the
// flag. Guarded by stubMu.
func (s *Stub) CanRenderPush() bool {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return !s.cannotRender
}

// MarkReplied records that this connection has dispatched ≥1 `reply` to its
// claimed route. Idempotent. Once set it stays set for the life of the
// connection — a session that has replied once is "in Telegram mode" and stays
// eligible for the typing relay across subsequent turns.
func (s *Stub) MarkReplied() {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	s.hasReplied = true
}

// HasReplied reports whether this connection has dispatched ≥1 `reply`. The
// typing relay (P5) arms only when the current holder HasReplied — see the
// hasReplied field doc.
func (s *Stub) HasReplied() bool {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	return s.hasReplied
}

// CurrentRoute returns a copy of the stub's current claim, or nil.
func (s *Stub) CurrentRoute() *RouteKey {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	if s.Route == nil {
		return nil
	}
	k := *s.Route
	return &k
}

// StubRegistry holds connected adapters keyed by ConnID. Concurrent-safe.
type StubRegistry struct {
	mu     sync.RWMutex
	next   atomic.Uint64
	byConn map[uint64]*Stub
}

// NewStubRegistry returns an empty registry. The first ConnID handed out is 1
// (uint64 0 is reserved for "no stub").
func NewStubRegistry() *StubRegistry {
	return &StubRegistry{byConn: map[uint64]*Stub{}}
}

// Register creates a new Stub with a monotonic ConnID and returns it. The
// stable session id (used for auto-attach-on-resume) is NOT set here — it
// arrives later via RecoverSessionReq and is stored with SetStableSessionID.
func (r *StubRegistry) Register(cli string, pid int, cwd string, conn any) *Stub {
	id := r.next.Add(1)
	s := &Stub{CLI: cli, PID: pid, CWD: cwd, ConnID: id, Conn: conn}
	r.mu.Lock()
	r.byConn[id] = s
	r.mu.Unlock()
	return s
}

// Get returns the stub for connID and whether it's present.
func (r *StubRegistry) Get(connID uint64) (*Stub, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byConn[connID]
	return s, ok
}

// Unregister removes the stub. No-op if not present.
func (r *StubRegistry) Unregister(connID uint64) {
	r.mu.Lock()
	delete(r.byConn, connID)
	r.mu.Unlock()
}

// Snapshot returns a copy of all currently-registered stubs. Used by status.
func (r *StubRegistry) Snapshot() []*Stub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Stub, 0, len(r.byConn))
	for _, s := range r.byConn {
		out = append(out, s)
	}
	return out
}
