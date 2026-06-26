package broker

import (
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/ipc"
)

// askCallbackPrefix is the opaque callback_data namespace for `ask` keyboards:
// "ask:<askID>:<optIdx>". The askID is 8-char base32 (no colon), so the LAST
// colon always separates the option index — robust against any future askID
// alphabet that the parser would otherwise mis-split.
const askCallbackPrefix = "ask:"

// pendingAsk is one registered, not-yet-answered `ask` awaiting a button tap. It
// is registered BEFORE the question is sent (the fast-tap race: a human could
// tap before the sendMessage round-trip returns), and removed atomically on
// resolution so a stale/double tap can't resolve it twice.
//
// options/question are kept so the broker can map the tapped index → option
// string and re-render the message ("✅ <chosen>") after clearing the keyboard.
type pendingAsk struct {
	askID     string
	route     RouteKey
	question  string
	options   []string
	messageID int64
}

// askRegistry holds the broker's in-flight asks keyed by askID. Mutex-guarded:
// register runs on the connection handler goroutine, resolveAsk on a route
// worker goroutine.
type askRegistry struct {
	mu sync.Mutex
	m  map[string]*pendingAsk
}

func newAskRegistry() *askRegistry {
	return &askRegistry{m: map[string]*pendingAsk{}}
}

// register inserts p. Returns false if askID is already registered (the rare
// adapter-side collision) so the caller can fail the registration fast rather
// than clobber a live ask.
func (r *askRegistry) register(p *pendingAsk) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[p.askID]; exists {
		return false
	}
	r.m[p.askID] = p
	return true
}

// setMessageID records the sent message's id so resolution can edit it.
func (r *askRegistry) setMessageID(askID string, id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.m[askID]; ok {
		p.messageID = id
	}
}

// delete removes an ask (e.g. when the send failed after registration).
func (r *askRegistry) delete(askID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, askID)
}

// has reports whether askID is currently registered (diagnostics/tests).
func (r *askRegistry) has(askID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[askID]
	return ok
}

// take atomically removes and returns the pendingAsk (resolve-once). The second
// concurrent tap for the same ask gets ok=false.
func (r *askRegistry) take(askID string) (*pendingAsk, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[askID]
	if ok {
		delete(r.m, askID)
	}
	return p, ok
}

// askKeyboard builds the single-select inline keyboard for an ask: one button
// per option, each on its own row, callback_data "ask:<askID>:<idx>". The
// telegram channel enforces the 64-byte callback_data ceiling and the row count;
// with an 8-char askID the payload is "ask:"+8+":"+digits — comfortably under 64
// for any deliverable option count.
func askKeyboard(askID string, options []string) [][]c3types.Button {
	rows := make([][]c3types.Button, 0, len(options))
	for i, opt := range options {
		rows = append(rows, []c3types.Button{{
			Text: opt,
			Data: askCallbackData(askID, i),
		}})
	}
	return rows
}

// askCallbackData formats the opaque callback payload for option idx.
func askCallbackData(askID string, idx int) string {
	return askCallbackPrefix + askID + ":" + strconv.Itoa(idx)
}

// parseAskData parses an "ask:<askID>:<idx>" callback payload. ok=false for any
// non-ask or malformed payload (so the generic event path proceeds untouched).
// The Phase-2 "ask:<id>:done" form naturally returns ok=false here (Atoi fails),
// which is correct for Phase 1 (no Done button exists yet).
func parseAskData(data string) (askID string, idx int, ok bool) {
	if !strings.HasPrefix(data, askCallbackPrefix) {
		return "", 0, false
	}
	rest := data[len(askCallbackPrefix):]
	sep := strings.LastIndex(rest, ":")
	if sep <= 0 || sep == len(rest)-1 {
		return "", 0, false
	}
	askID = rest[:sep]
	n, err := strconv.Atoi(rest[sep+1:])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return askID, n, true
}

// resolveAsk attempts to resolve a registered ask from an inline-keyboard
// callback whose Data is "ask:<askID>:<idx>". It returns true when the callback
// matched a live ask on this route — in which case the caller (flushEvent) must
// SUPPRESS the generic event (the tap was the answer, not a fresh <channel>
// event). It returns false for a non-ask payload, an unknown/already-resolved
// askID, a route mismatch, or an out-of-range index, so the generic path
// proceeds; the telegram channel has already auto-acked the callback either way,
// so Telegram stops spinning regardless.
//
// On a match it (a) pushes an OpAskResult carrying the chosen option to the route
// holder's conn — the SAME delivery path as OpInbound (survives a same-process
// broker reconnect via the transferred claim) — and (b) edits the message to mark
// the choice and CLEAR the keyboard, preventing double answers / stale taps.
func (b *Broker) resolveAsk(route RouteKey, cb *c3types.CallbackEvent) bool {
	if b == nil || b.Asks == nil || cb == nil {
		return false
	}
	askID, idx, ok := parseAskData(cb.Data)
	if !ok {
		return false
	}
	p, ok := b.Asks.take(askID)
	if !ok {
		// Unknown / already-resolved / expired. Already auto-acked by the channel.
		return false
	}
	// Defense-in-depth: a tap is only honored on the ask's own route, and only for
	// an in-range option. On either mismatch, re-register so a correct later tap
	// can still resolve, and fall through to the generic path.
	if p.route != route || idx < 0 || idx >= len(p.options) {
		b.Asks.register(p)
		return false
	}
	chosen := p.options[idx]

	// Push the answer to the holder adapter (identical to OpInbound delivery).
	if holder, claimed := b.Routes.Holder(route); claimed {
		if conn, ok := holder.ConnValue().(*ipc.Conn); ok && conn != nil {
			if err := conn.WriteJSON(ipc.AskResultMsg{
				Op:     ipc.OpAskResult,
				AskID:  askID,
				Answer: ipc.AskAnswer{Selected: []string{chosen}},
			}); err != nil {
				log.Printf("ask deliver FAIL chan=%s chat=%d topic=%s ask=%s: %v",
					route.Channel, route.ChatID, TopicKeyStr(route), askID, err)
			}
		} else {
			log.Printf("ask deliver DROP chan=%s chat=%d topic=%s ask=%s: holder disconnected — answer not delivered",
				route.Channel, route.ChatID, TopicKeyStr(route), askID)
		}
	} else {
		log.Printf("ask deliver DROP chan=%s chat=%d topic=%s ask=%s: no live holder — answer not delivered",
			route.Channel, route.ChatID, TopicKeyStr(route), askID)
	}

	// Mark the message answered + clear the keyboard (best-effort: a failed edit
	// must not block the answer that was already delivered). A non-nil EMPTY
	// Buttons clears the inline keyboard (see EditMessage).
	if ch, err := b.Channel(route.Channel); err == nil {
		if _, err := ch.EditMessage(c3types.EditArgs{
			Channel:   route.Channel,
			ChatID:    route.ChatID,
			MessageID: p.messageID,
			Text:      askAnsweredText(p.question, chosen),
			Buttons:   [][]c3types.Button{}, // non-nil empty → clears the keyboard
		}); err != nil {
			log.Printf("ask edit FAIL chan=%s chat=%d topic=%s ask=%s msg=%d: %v",
				route.Channel, route.ChatID, TopicKeyStr(route), askID, p.messageID, err)
		}
	}

	log.Printf("ask RESOLVED chan=%s chat=%d topic=%s ask=%s idx=%d",
		route.Channel, route.ChatID, TopicKeyStr(route), askID, idx)
	return true
}

// askAnsweredText renders the post-answer message body: the original question
// followed by a checkmark line naming the chosen option, so the Telegram view
// records what was picked after the keyboard is cleared.
func askAnsweredText(question, chosen string) string {
	if question == "" {
		return "✅ " + chosen
	}
	return question + "\n\n✅ " + chosen
}
