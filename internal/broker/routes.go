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
// the route is held by a different LIVING stub; (stub, true) on success,
// no-op re-claim by the same stub, or successful displacement of a dead
// holder.
//
// Liveness rule (the "broker is the authority" principle, Karthi 2026-05-09):
// A claim is sacrosanct as long as the holding adapter's process is alive.
// A momentary conn drop does NOT free the claim — we wait for the holder
// to reconnect. Only if the holder's PID is confirmed dead do we release
// and grant.
//
// This prevents:
//   - codex's auto-attach from stealing claude's claim during a conn blip
//   - parallel claude/codex sessions racing for the same cwd-mapped topic
//   - the "attach succeeded but actually didn't" lying-success bug
func (r *Routes) Claim(key RouteKey, stub *Stub) (*Stub, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.m[key]; ok {
		if existing.ConnID == stub.ConnID {
			log.Printf("routes Claim IDEMPOTENT key=%s by cli=%s pid=%d conn=%d",
				routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID)
			return existing, true
		}
		// Same logical session reconnecting (same CLI+PID+CWD)? Transfer.
		if sameLogicalSession(existing, stub) {
			log.Printf("routes Claim TRANSFER key=%s from conn=%d to conn=%d (same cli=%s pid=%d cwd=%q)",
				routeKeyStr(key), existing.ConnID, stub.ConnID, stub.CLI, stub.PID, stub.CWD)
			r.m[key] = stub
			return stub, true
		}
		// Different session. Is the existing holder still alive?
		if existing.IsAlive() {
			log.Printf("routes Claim COLLISION key=%s wanted-by cli=%s pid=%d conn=%d, held-by ALIVE cli=%s pid=%d conn=%d",
				routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID,
				existing.CLI, existing.PID, existing.ConnID)
			return existing, false
		}
		// Stale claim — holder process is dead. Release and grant.
		log.Printf("routes Claim DISPLACE key=%s held by DEAD cli=%s pid=%d conn=%d, granting to cli=%s pid=%d conn=%d",
			routeKeyStr(key), existing.CLI, existing.PID, existing.ConnID,
			stub.CLI, stub.PID, stub.ConnID)
		delete(r.m, key)
	}
	r.m[key] = stub
	log.Printf("routes Claim OK key=%s by cli=%s pid=%d conn=%d cwd=%q",
		routeKeyStr(key), stub.CLI, stub.PID, stub.ConnID, stub.CWD)
	return stub, true
}

// sameLogicalSession returns whether two stubs represent the same adapter
// process — same CLI, same PID, same CWD. Used to detect a reconnect of
// an existing session and let it transfer its claim instead of fighting
// for it.
func sameLogicalSession(a, b *Stub) bool {
	return a.CLI == b.CLI && a.PID == b.PID && a.CWD == b.CWD
}

// FindByLogicalSession returns the (first) stub matching the (CLI, PID,
// CWD) triple, or nil if none. Used during hello to detect a reconnect
// and transfer claims atomically.
func (r *Routes) FindByLogicalSession(cli string, pid int, cwd string) *Stub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.m {
		if s.CLI == cli && s.PID == pid && s.CWD == cwd {
			return s
		}
	}
	return nil
}

// ForceReleaseKey unconditionally evicts whatever holds key, returning the
// previous holder (or nil). Used by the force_steal flow — only invoked
// after the user has explicitly confirmed via the slash command's
// AskUserQuestion prompt.
func (r *Routes) ForceReleaseKey(key RouteKey) *Stub {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[key]
	if !ok {
		return nil
	}
	delete(r.m, key)
	log.Printf("routes ForceReleaseKey key=%s evicted cli=%s pid=%d conn=%d (user-confirmed steal)",
		routeKeyStr(key), existing.CLI, existing.PID, existing.ConnID)
	return existing
}

// TransferAllByConnID re-points every claim from oldConnID to newStub
// in-place. Used when an adapter reconnects with a fresh ConnID — its
// existing claims should keep working without the adapter having to
// re-attach.
func (r *Routes) TransferAllByConnID(oldConnID uint64, newStub *Stub) []RouteKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var transferred []RouteKey
	for k, s := range r.m {
		if s.ConnID == oldConnID {
			r.m[k] = newStub
			transferred = append(transferred, k)
			log.Printf("routes Transfer key=%s from conn=%d to conn=%d (cli=%s pid=%d)",
				routeKeyStr(k), oldConnID, newStub.ConnID, newStub.CLI, newStub.PID)
		}
	}
	return transferred
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
