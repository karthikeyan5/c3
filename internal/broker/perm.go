package broker

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// Permission relay (spec docs/superpowers/specs/2026-06-26-c3-permission-relay-
// design.md). A Claude Code tool-use permission prompt is relayed to the route's
// Telegram topic as an Allow/Deny inline keyboard; the operator's tap pushes an
// OpPermissionVerdict back to the holder, which emits it into CC.
//
// This mirrors `ask` (ask.go) but is SIMPLER: it is fire-and-forget into CC, not
// a blocking tool. There is no caller to unblock, so there is no answer-timeout
// and no registration ack. The reaper still expires stale pending perms and
// clears their now-dead keyboards (registry hygiene), like the ask reaper.
const (
	// permExpiryTTL bounds how long a pending perm may live before the reaper
	// removes it and best-effort clears its keyboard. There is NO adapter-side
	// answer timeout for perms (a CC permission prompt waits indefinitely in the
	// TUI), so this is pure hygiene: long enough that a still-relevant prompt is
	// not cleared out from under the operator, short enough that an abandoned
	// prompt does not leak a live keyboard forever. Clearing the keyboard does not
	// affect CC — it just removes the (now non-functional) Allow/Deny buttons.
	permExpiryTTL = 30 * time.Minute
	// maxPendingPerms caps the registry; register evicts the oldest entry (and
	// clears its keyboard) when full, so a flood can't grow the map unbounded.
	maxPendingPerms = 1000
)

// permCallbackPrefix is the opaque callback_data namespace for permission
// keyboards: "perm:<verb>:<requestID>" where verb is "allow" | "deny". The
// requestID (5 letters [a-km-z]) carries no colon, so the FIRST colon after the
// prefix separates the verb from the id. Matches the reference Telegram plugin's
// `perm:more|allow|deny:<id>` shape.
const permCallbackPrefix = "perm:"

// pendingPerm is one relayed, not-yet-resolved permission prompt awaiting a tap.
// Registered BEFORE the keyboard is sent (the fast-tap race) and removed
// atomically on resolution (resolve-once). toolName is kept so the outcome edit
// can name the tool ("🔐 <tool>: ✅ Allowed").
type pendingPerm struct {
	requestID string
	route     RouteKey
	toolName  string
	messageID int64

	// createdAt is when the perm was registered, stamped by register. The reaper
	// expires perms older than permExpiryTTL; the size cap evicts the entry with
	// the smallest createdAt.
	createdAt time.Time
}

// permRegistry holds the broker's in-flight permission prompts keyed by
// requestID. Mutex-guarded: register runs on the connection handler goroutine,
// resolvePerm on a route worker goroutine.
type permRegistry struct {
	mu sync.Mutex
	m  map[string]*pendingPerm
}

func newPermRegistry() *permRegistry {
	return &permRegistry{m: map[string]*pendingPerm{}}
}

// register inserts p, stamping createdAt (if unset). ok=false (evicted=nil) when
// requestID is already registered, so the caller can fail fast rather than
// clobber a live perm. At maxPendingPerms it first evicts the OLDEST entry and
// returns it as `evicted` so the caller can best-effort clear its keyboard.
func (r *permRegistry) register(p *pendingPerm) (evicted *pendingPerm, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[p.requestID]; exists {
		return nil, false
	}
	if p.createdAt.IsZero() {
		p.createdAt = time.Now()
	}
	if len(r.m) >= maxPendingPerms {
		evicted = r.evictOldestLocked()
	}
	r.m[p.requestID] = p
	return evicted, true
}

// evictOldestLocked removes and returns the entry with the smallest createdAt.
// Caller holds r.mu.
func (r *permRegistry) evictOldestLocked() *pendingPerm {
	var oldest *pendingPerm
	for _, p := range r.m {
		if oldest == nil || p.createdAt.Before(oldest.createdAt) {
			oldest = p
		}
	}
	if oldest != nil {
		delete(r.m, oldest.requestID)
	}
	return oldest
}

// sweepExpired removes and returns every perm older than ttl. Used by the reaper.
func (r *permRegistry) sweepExpired(now time.Time, ttl time.Duration) []*pendingPerm {
	r.mu.Lock()
	defer r.mu.Unlock()
	var expired []*pendingPerm
	for id, p := range r.m {
		if now.Sub(p.createdAt) >= ttl {
			expired = append(expired, p)
			delete(r.m, id)
		}
	}
	return expired
}

// setMessageID records the sent message's id so resolution can edit it.
func (r *permRegistry) setMessageID(requestID string, id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.m[requestID]; ok {
		p.messageID = id
	}
}

// delete removes a perm (e.g. when the send failed after registration).
func (r *permRegistry) delete(requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, requestID)
}

// has reports whether requestID is currently registered (diagnostics/tests).
func (r *permRegistry) has(requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[requestID]
	return ok
}

// take atomically removes and returns the pendingPerm (resolve-once). The second
// concurrent tap for the same perm gets ok=false.
func (r *permRegistry) take(requestID string) (*pendingPerm, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[requestID]
	if ok {
		delete(r.m, requestID)
	}
	return p, ok
}

// permKeyboard builds the Allow/Deny inline keyboard for a relayed permission
// prompt: one row, "[✅ Allow][❌ Deny]", callback_data "perm:allow:<id>" /
// "perm:deny:<id>". With a 5-letter id the payload is well under Telegram's
// 64-byte callback_data ceiling.
func permKeyboard(requestID string) [][]c3types.Button {
	return [][]c3types.Button{
		{
			{Text: "✅ Allow", Data: permAllowData(requestID)},
			{Text: "❌ Deny", Data: permDenyData(requestID)},
		},
	}
}

func permAllowData(id string) string { return permCallbackPrefix + "allow:" + id }
func permDenyData(id string) string  { return permCallbackPrefix + "deny:" + id }

// parsePermCallback parses a "perm:<verb>:<requestID>" callback payload into its
// (requestID, behavior). behavior is "allow" | "deny". ok=false for a non-perm,
// malformed, or unknown-verb payload, so the generic event path proceeds
// untouched. The verb comes FIRST (the requestID has no colon), so the first
// colon after the prefix separates it.
func parsePermCallback(data string) (requestID, behavior string, ok bool) {
	if !strings.HasPrefix(data, permCallbackPrefix) {
		return "", "", false
	}
	rest := data[len(permCallbackPrefix):] // "<verb>:<id>"
	sep := strings.IndexByte(rest, ':')
	if sep <= 0 || sep == len(rest)-1 {
		return "", "", false
	}
	verb := rest[:sep]
	id := rest[sep+1:]
	switch verb {
	case "allow":
		return id, "allow", true
	case "deny":
		return id, "deny", true
	}
	return "", "", false
}

// resolvePerm attempts to resolve a relayed permission prompt from an
// inline-keyboard callback whose Data is "perm:<verb>:<id>". It returns true ONLY
// when the callback matched a live perm on this route AND the tapper is an
// authorized operator — in which case the caller (flushEvent) must SUPPRESS the
// generic event (the tap was the verdict, not a fresh <channel> event). It
// returns false for a non-perm payload, an unknown/already-resolved id, a route
// mismatch, OR a non-operator tap — the channel already auto-acked the callback
// either way, so Telegram stops spinning regardless.
//
// SENDER-GATE (Security §): inbound callbacks already pass host.GateInbound
// (allowlist) before reaching the broker, but for a permission approval — higher
// trust than a chat reply — we additionally require the tapper to be an
// allowlisted DM-cleared OPERATOR (mappings.IsUserAllowed(cb.Actor.UserID)), not
// merely a member of an allowlisted group. A non-operator tap is ignored and the
// pending perm is LEFT LIVE so the real operator can still approve.
//
// TODO(perm, 2026-06-26): "allowlisted DM operator" is the current operator set.
// A tighter model would bind a route to a single owner user_id (the session
// owner) and honor only that one. There is no per-route owner-identity concept in
// the broker today, so we gate to the DM-cleared user set; revisit if/when a
// single-operator identity lands (see the trusted-operator hook spec).
func (b *Broker) resolvePerm(route RouteKey, cb *c3types.CallbackEvent) bool {
	if b == nil || b.Perms == nil || cb == nil {
		return false
	}
	requestID, behavior, ok := parsePermCallback(cb.Data)
	if !ok {
		return false
	}
	// Sender-gate BEFORE take, so a non-operator tap leaves the pending perm live.
	if !b.Mappings().IsUserAllowed(cb.Actor.UserID) {
		log.Printf("perm GATE-DROP chan=%s chat=%d topic=%s id=%s actor=%d: non-operator tap ignored",
			route.Channel, route.ChatID, TopicKeyStr(route), requestID, cb.Actor.UserID)
		return false
	}
	p, ok := b.Perms.take(requestID)
	if !ok {
		// Unknown / already-resolved / expired. Already auto-acked; fall through.
		return false
	}
	if p.route != route {
		// Tap arrived on a different route than the prompt was sent to — re-register
		// and fall through (mirrors resolveAskDone's defensive re-register).
		b.Perms.register(p)
		return false
	}
	b.deliverPermVerdict(route, requestID, behavior)
	b.editPermMessage(route, requestID, p.messageID, permOutcomeText(p.toolName, behavior), [][]c3types.Button{})
	log.Printf("perm RESOLVED chan=%s chat=%d topic=%s id=%s tool=%s behavior=%s actor=%d",
		route.Channel, route.ChatID, TopicKeyStr(route), requestID, p.toolName, behavior, cb.Actor.UserID)
	return true
}

// deliverPermVerdict pushes an OpPermissionVerdict to the route holder's conn —
// identical to OpAskResult / OpInbound delivery (survives a same-process broker
// reconnect via the transferred claim). Best-effort: a disconnected/absent holder
// is logged, not fatal (CC simply keeps waiting in the TUI).
func (b *Broker) deliverPermVerdict(route RouteKey, requestID, behavior string) {
	if holder, claimed := b.Routes.Holder(route); claimed {
		if conn, ok := holder.ConnValue().(*ipc.Conn); ok && conn != nil {
			if err := conn.WriteJSON(ipc.PermissionVerdictMsg{
				Op:        ipc.OpPermissionVerdict,
				RequestID: requestID,
				Behavior:  behavior,
			}); err != nil {
				log.Printf("perm deliver FAIL chan=%s chat=%d topic=%s id=%s: %v",
					route.Channel, route.ChatID, TopicKeyStr(route), requestID, err)
			}
		} else {
			log.Printf("perm deliver DROP chan=%s chat=%d topic=%s id=%s: holder disconnected — verdict not delivered",
				route.Channel, route.ChatID, TopicKeyStr(route), requestID)
		}
	} else {
		log.Printf("perm deliver DROP chan=%s chat=%d topic=%s id=%s: no live holder — verdict not delivered",
			route.Channel, route.ChatID, TopicKeyStr(route), requestID)
	}
}

// editPermMessage edits the perm's Telegram message to text + buttons.
// Best-effort: a failed edit must not block a verdict that was already delivered.
// A non-nil EMPTY buttons slice clears the inline keyboard. Reuses the shared
// editKeyboardMessage core (see ask.go) so the channel-edit plumbing has one home.
func (b *Broker) editPermMessage(route RouteKey, requestID string, messageID int64, text string, buttons [][]c3types.Button) {
	if found, err := b.editKeyboardMessage(route, messageID, text, buttons); found && err != nil {
		log.Printf("perm edit FAIL chan=%s chat=%d topic=%s id=%s msg=%d: %v",
			route.Channel, route.ChatID, TopicKeyStr(route), requestID, messageID, err)
	}
}

// registerPerm registers p and, when the size cap forces an eviction, best-effort
// clears the evicted (oldest) perm's now-orphaned keyboard. Returns false on a
// requestID collision so the caller drops the relay.
func (b *Broker) registerPerm(p *pendingPerm) bool {
	evicted, ok := b.Perms.register(p)
	if evicted != nil {
		log.Printf("perm EVICTED chan=%s chat=%d topic=%s id=%s reason=cap(max=%d) — clearing keyboard",
			evicted.route.Channel, evicted.route.ChatID, TopicKeyStr(evicted.route), evicted.requestID, maxPendingPerms)
		b.editPermMessage(evicted.route, evicted.requestID, evicted.messageID, permExpiredText(evicted.toolName), [][]c3types.Button{})
	}
	return ok
}

// sweepExpiredPerms removes perms older than permExpiryTTL and best-effort clears
// each one's live keyboard. Called by the reaper (folded into StartAskReaper).
// Logs the count + per-perm requestID/reason, never the preview body.
func (b *Broker) sweepExpiredPerms() {
	if b == nil || b.Perms == nil {
		return
	}
	expired := b.Perms.sweepExpired(time.Now(), permExpiryTTL)
	if len(expired) == 0 {
		return
	}
	log.Printf("perm sweep: expired %d perm(s) (ttl=%v) — clearing keyboards", len(expired), permExpiryTTL)
	for _, p := range expired {
		log.Printf("perm EXPIRED chan=%s chat=%d topic=%s id=%s reason=ttl",
			p.route.Channel, p.route.ChatID, TopicKeyStr(p.route), p.requestID)
		b.editPermMessage(p.route, p.requestID, p.messageID, permExpiredText(p.toolName), [][]c3types.Button{})
	}
}

// permPromptText renders the relayed prompt body: "🔐 Permission: <tool>" plus an
// optional truncated input preview on its own line. Never carries a secret body —
// the preview is the harness's already-truncated input_preview / description.
func permPromptText(tool, preview string) string {
	head := "🔐 Permission: " + tool
	if strings.TrimSpace(preview) == "" {
		return head
	}
	return head + "\n\n" + preview
}

// permOutcomeText renders the post-verdict body recorded on the message after the
// keyboard clears: "🔐 <tool>: ✅ Allowed" / "❌ Denied".
func permOutcomeText(tool, behavior string) string {
	verdict := "✅ Allowed"
	if behavior == "deny" {
		verdict = "❌ Denied"
	}
	if strings.TrimSpace(tool) == "" {
		return "🔐 " + verdict
	}
	return "🔐 " + tool + ": " + verdict
}

// permExpiredText renders the post-expiry/eviction body so the Telegram view
// records that nothing was captured after the keyboard clears.
func permExpiredText(tool string) string {
	const notice = "⌛ Permission expired — no verdict recorded"
	if strings.TrimSpace(tool) == "" {
		return notice
	}
	return "🔐 " + tool + "\n\n" + notice
}
