package broker

import (
	"log"
	"sync"
)

// Routes is the in-memory ROUTES map. Single-claim-per-route invariant
// (spec §4.2.1).
type Routes struct {
	mu sync.RWMutex
	m  map[RouteKey]*Stub
}

// NewRoutes returns an empty routes table.
func NewRoutes() *Routes {
	return &Routes{m: map[RouteKey]*Stub{}}
}

// Claim attempts to insert (key → stub). Returns (current_holder, false) if
// the route is held by a different stub; (stub, true) on success or no-op
// re-claim by the same stub.
//
// Logs every claim attempt and outcome — diagnosing route-claim races
// (codex auto-attach stealing claude's claim, etc.) requires the full
// sequence of who-claimed-when.
func (r *Routes) Claim(key RouteKey, stub *Stub) (*Stub, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.m[key]; ok {
		if existing.ConnID == stub.ConnID {
			log.Printf("routes Claim IDEMPOTENT key=%s by cli=%s pid=%d conn=%d",
				routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID)
			return existing, true
		}
		log.Printf("routes Claim COLLISION key=%s wanted-by cli=%s pid=%d conn=%d, held-by cli=%s pid=%d conn=%d",
			routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID,
			existing.CLI, existing.PID, existing.ConnID)
		return existing, false
	}
	r.m[key] = stub
	log.Printf("routes Claim OK key=%s by cli=%s pid=%d conn=%d cwd=%q",
		routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID, stub.CWD)
	return stub, true
}

// Release drops the claim for key, but only if the holder matches connID. No-op
// if the route is held by someone else or unheld.
func (r *Routes) Release(key RouteKey, connID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.m[key]; ok && existing.ConnID == connID {
		delete(r.m, key)
		log.Printf("routes Release key=%s conn=%d cli=%s pid=%d",
			routeKeyStr(key), connID, existing.CLI, existing.PID)
	}
}

// ReleaseAllByConnID drops every claim held by connID. Used on adapter
// disconnect (clean or unclean). Returns the released keys.
func (r *Routes) ReleaseAllByConnID(connID uint64) []RouteKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var released []RouteKey
	for k, s := range r.m {
		if s.ConnID == connID {
			log.Printf("routes ReleaseAllByConnID key=%s conn=%d cli=%s pid=%d",
				routeKeyStr(k), connID, s.CLI, s.PID)
			delete(r.m, k)
			released = append(released, k)
		}
	}
	return released
}

// routeKeyStr renders a RouteKey for log lines. Diagnostic only.
func routeKeyStr(k RouteKey) string {
	if !k.HasTopic {
		return formatInt64(k.ChatID) + "/dm"
	}
	return formatInt64(k.ChatID) + "/" + formatInt64(k.TopicID)
}

// Holder returns the current holder of key, or nil if unheld.
func (r *Routes) Holder(key RouteKey) (*Stub, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.m[key]
	return s, ok
}

// Snapshot returns a slice of (key, stub) pairs for diagnostics.
func (r *Routes) Snapshot() []RouteEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RouteEntry, 0, len(r.m))
	for k, s := range r.m {
		out = append(out, RouteEntry{Key: k, Stub: s})
	}
	return out
}

// RouteEntry is one row of Routes.Snapshot.
type RouteEntry struct {
	Key  RouteKey
	Stub *Stub
}
