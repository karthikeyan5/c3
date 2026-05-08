package broker

import (
	"sync"
	"sync/atomic"
)

// Stub is the broker's view of a connected adapter. ConnID is the
// late-result-discard token described in spec §4.5.1.
type Stub struct {
	CLI    string
	PID    int
	CWD    string
	ConnID uint64

	// Conn is opaque from the registry's POV — broker package wires it after
	// constructing, used by route workers to write inbound to the right
	// adapter. Type is *ipc.Conn but kept as any here to avoid the import
	// cycle in the registry file.
	Conn any

	// Route is the currently-claimed route for this stub (one per connection
	// in v1; re-attach replaces). nil when unclaimed. Set/cleared by the
	// broker handler under stubMu.
	stubMu sync.Mutex
	Route  *RouteKey
}

// SetRoute atomically sets the stub's current claim.
func (s *Stub) SetRoute(key *RouteKey) {
	s.stubMu.Lock()
	defer s.stubMu.Unlock()
	if key == nil {
		s.Route = nil
		return
	}
	k := *key
	s.Route = &k
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

// Register creates a new Stub with a monotonic ConnID and returns it.
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
