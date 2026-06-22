package telegram

import "sync"

// offsetTracker advances the Telegram offset only to the highest CONTIGUOUS
// update_id that is "done" — durably persisted (Append+fsync succeeded), or a
// no-op (gated / dropped / non-message). An update whose message is still
// mid-STT/mid-persist stays in-flight, so the committed offset cannot pass it;
// if the broker crashes there, the offset never advanced and Telegram redelivers
// it (within 24h). Loss-free by construction.
//
// It is goroutine-safe: Register is called from the poll loop, MarkDone from
// both the poll loop (gated/dropped/non-message) and the per-route worker
// goroutine (after Append+fsync), Committed from the poll loop before Save.
type offsetTracker struct {
	mu        sync.Mutex
	committed int64          // highest contiguous-done id
	done      map[int64]bool // ids > committed that are done (sparse)
	inflight  map[int64]bool // ids > committed registered but not yet done
}

// newOffsetTracker seeds committed from the persisted store (0 if none).
func newOffsetTracker(committed int64) *offsetTracker {
	return &offsetTracker{
		committed: committed,
		done:      map[int64]bool{},
		inflight:  map[int64]bool{},
	}
}

// Register marks an accepted update_id as in-flight. Ids <= committed are
// ignored (already accounted for — e.g. a dedup-skip re-seen after restart).
func (t *offsetTracker) Register(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id <= t.committed || t.done[id] {
		return
	}
	t.inflight[id] = true
}

// MarkDone marks an update_id done and advances committed over any now-contiguous
// prefix. Safe to call for an id never Registered (gated/dropped/non-message
// updates are marked done directly).
func (t *offsetTracker) MarkDone(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id <= t.committed {
		return
	}
	delete(t.inflight, id)
	t.done[id] = true
	for t.done[t.committed+1] {
		t.committed++
		delete(t.done, t.committed)
	}
}

// Committed returns the highest contiguous-done update_id. Persist this value;
// the next getUpdates offset is Committed()+1.
func (t *offsetTracker) Committed() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.committed
}
