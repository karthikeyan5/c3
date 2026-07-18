package telegram

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// updateDedup is a small TTL-bounded LRU keyed by a synthetic
// (update_id, chat_id, message_id, media_group_id) tuple. Defense-in-depth
// against Telegram occasionally re-delivering the same update — happens when
// the offset ack races with a server-side restart, or after long downtimes.
//
// Capacity-bounded so an attacker can't grow it unboundedly. Entries time
// out independently of capacity so a quiet hour doesn't keep stale records.
//
// Inspired by a prior TypeScript Telegram bot's dedupe cache (`bot-updates.ts`
// "5-minute TTL and 2000-item max").
type updateDedup struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	order    *list.List               // back = newest, front = oldest
	index    map[string]*list.Element // key → element holding *dedupEntry
}

type dedupEntry struct {
	key      string
	updateID int64 // source update_id, so forget(update_id) can evict this entry
	insertAt time.Time
}

func newUpdateDedup(capacity int, ttl time.Duration) *updateDedup {
	return &updateDedup{
		capacity: capacity,
		ttl:      ttl,
		order:    list.New(),
		index:    map[string]*list.Element{},
	}
}

// SeenOrAdd returns true if the update was seen recently. On false, the
// update is recorded and future calls return true. nil-update is no-op false.
func (d *updateDedup) SeenOrAdd(u *gotgbot.Update) bool {
	if u == nil {
		return false
	}
	key := dedupKey(u)
	if key == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.evictExpiredLocked()
	if _, ok := d.index[key]; ok {
		return true
	}
	entry := &dedupEntry{key: key, updateID: u.UpdateId, insertAt: time.Now()}
	el := d.order.PushBack(entry)
	d.index[key] = el
	for d.order.Len() > d.capacity {
		front := d.order.Front()
		if front == nil {
			break
		}
		d.order.Remove(front)
		delete(d.index, front.Value.(*dedupEntry).key)
	}
	return false
}

// forget removes any dedup entry recorded for updateID, so a subsequent Telegram
// redelivery of that same update is NOT dedup-suppressed and genuinely
// re-dispatches. Used on a durable-Append FAILURE (worker → onPersistFailed): the
// offset deliberately HOLDS the un-persisted update (loss-free), so Telegram
// redelivers it — but the first dispatch already recorded it here (5-min TTL), so
// without this eviction the redelivery spins as a dedup-skip and the "loss-free
// retry" never actually retries the Append until the TTL lapses (or forever, on a
// permanent failure). A no-op when no entry matches. update_ids are unique per
// update, so at most one entry matches; we still scan (bounded by capacity) so a
// stale duplicate can never survive.
func (d *updateDedup) forget(updateID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for e := d.order.Front(); e != nil; {
		next := e.Next()
		if entry := e.Value.(*dedupEntry); entry.updateID == updateID {
			d.order.Remove(e)
			delete(d.index, entry.key)
		}
		e = next
	}
}

// evictExpiredLocked walks from the oldest until it finds a non-expired
// entry. Caller must hold d.mu.
func (d *updateDedup) evictExpiredLocked() {
	cutoff := time.Now().Add(-d.ttl)
	for {
		front := d.order.Front()
		if front == nil {
			return
		}
		entry := front.Value.(*dedupEntry)
		if entry.insertAt.After(cutoff) {
			return
		}
		d.order.Remove(front)
		delete(d.index, entry.key)
	}
}

// dedupKey synthesizes the dedup key for an Update. Pulls fields that
// uniquely identify a single delivery; messages with no useful coordinates
// return "" and aren't deduped (rare: callback-only / poll-result-only
// updates).
func dedupKey(u *gotgbot.Update) string {
	if msg := u.Message; msg != nil {
		return fmt.Sprintf("u%d|c%d|m%d|g%s", u.UpdateId, msg.Chat.Id, msg.MessageId, msg.MediaGroupId)
	}
	if msg := u.EditedMessage; msg != nil {
		return fmt.Sprintf("ue%d|c%d|m%d|g%s", u.UpdateId, msg.Chat.Id, msg.MessageId, msg.MediaGroupId)
	}
	if cq := u.CallbackQuery; cq != nil {
		return fmt.Sprintf("cq%d|i%s", u.UpdateId, cq.Id)
	}
	if mr := u.MessageReaction; mr != nil {
		// User may be nil for anonymous reactions; update_id is enough to dedupe.
		return fmt.Sprintf("mr%d|c%d|m%d", u.UpdateId, mr.Chat.Id, mr.MessageId)
	}
	return ""
}
