package telegram

import (
	"container/list"
	"sync"
)

// sentPoll records the routing + ownership context of a poll the bot sent, so a
// later aggregate `poll` update (which carries only the poll id and tallies, NOT
// the originating chat or any user) can be routed back onto the right worker and
// pass the inbound allowlist gate.
//
// CB-2: OwnerUserID is the route-owner the synthesized poll_result Inbound is
// stamped with. For a DM (ChatID > 0) the chat id IS the owner's user id (a
// Telegram private chat's chat.id == the user's id), which is exactly the id the
// user paired into the allowlist — so IsUserAllowed(owner) passes and the
// headline read path is NOT gate-dropped in DMs. For a group the gate clears on
// chat_id (IsGroupAllowed), so OwnerUserID is informational there.
type sentPoll struct {
	ChatID      int64
	TopicID     *int64
	MessageID   int64
	OwnerUserID int64
	// LatestTally is the most recent aggregate result seen for this poll. Kept
	// for on-demand reads; updated each time a `poll` update or stop_poll lands.
	LatestTally *pollTally
}

// pollTally is the in-package aggregate snapshot (mirrors c3types.PollResult
// without importing it here — kept tiny and channel-local).
type pollTally struct {
	Question    string
	TotalVoters int
	IsClosed    bool
	Options     []pollOptionTally
}

type pollOptionTally struct {
	Text       string
	VoterCount int
}

// sentPollMap is a capacity-bounded LRU keyed by poll id. Bounded so a long-
// running broker that sends many polls can't grow it without limit (memory
// leak). Mirrors the bounded-LRU shape of updateDedup (dedup.go).
type sentPollMap struct {
	mu       sync.Mutex
	capacity int
	order    *list.List               // back = newest, front = oldest
	index    map[string]*list.Element // pollID → element holding *sentPollEntry
}

type sentPollEntry struct {
	pollID string
	val    *sentPoll
}

func newSentPollMap(capacity int) *sentPollMap {
	if capacity <= 0 {
		capacity = 1
	}
	return &sentPollMap{
		capacity: capacity,
		order:    list.New(),
		index:    map[string]*list.Element{},
	}
}

// Put records (or refreshes) the routing context for a poll id, evicting the
// oldest entry when over capacity. A re-put moves the entry to the newest slot.
func (m *sentPollMap) Put(pollID string, sp *sentPoll) {
	if m == nil || pollID == "" || sp == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.index[pollID]; ok {
		el.Value.(*sentPollEntry).val = sp
		m.order.MoveToBack(el)
		return
	}
	el := m.order.PushBack(&sentPollEntry{pollID: pollID, val: sp})
	m.index[pollID] = el
	for m.order.Len() > m.capacity {
		front := m.order.Front()
		if front == nil {
			break
		}
		m.order.Remove(front)
		delete(m.index, front.Value.(*sentPollEntry).pollID)
	}
}

// Get returns the routing context for a poll id and whether it was found. A hit
// is promoted to the newest slot (LRU recency).
func (m *sentPollMap) Get(pollID string) (*sentPoll, bool) {
	if m == nil || pollID == "" {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.index[pollID]
	if !ok {
		return nil, false
	}
	m.order.MoveToBack(el)
	return el.Value.(*sentPollEntry).val, true
}
