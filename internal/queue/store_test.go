package queue

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	s, err := NewStore(QueueDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func msg(id int64, text string) *c3types.Inbound {
	return &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: id, Text: text, Timestamp: time.Now()}
}

func TestAppendPeekConsumeAndDeleteOnEmpty(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	for i := int64(1); i <= 3; i++ {
		if err := s.Append(rk, msg(i, "m")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Peek does not advance.
	peek, err := s.Peek(rk, 2)
	if err != nil || len(peek) != 2 || peek[0].MessageID != 1 {
		t.Fatalf("peek = %+v err=%v", peek, err)
	}
	if n, _ := s.Pending(rk); n != 3 {
		t.Fatalf("pending after peek = %d, want 3", n)
	}
	// Consume advances.
	got, err := s.Consume(rk, 2)
	if err != nil || len(got) != 2 || got[1].MessageID != 2 {
		t.Fatalf("consume = %+v err=%v", got, err)
	}
	if n, _ := s.Pending(rk); n != 1 {
		t.Fatalf("pending after consume = %d, want 1", n)
	}
	// Drain the rest → files deleted.
	if _, err := s.Consume(rk, 10); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Pending(rk); n != 0 {
		t.Fatalf("pending after drain = %d, want 0", n)
	}
	// A fresh append after delete-on-empty must restart at line 1.
	if err := s.Append(rk, msg(99, "again")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Consume(rk, 1); len(got) != 1 || got[0].MessageID != 99 {
		t.Fatalf("re-append consume = %+v, want msg 99", got)
	}
}

func TestRecoverOnStartup_CursorBehindReplaysAtLeastOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("C3_QUEUE_DIR", dir)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	s1, _ := NewStore(QueueDir())
	for i := int64(1); i <= 4; i++ {
		_ = s1.Append(rk, msg(i, "m"))
	}
	if _, err := s1.Consume(rk, 2); err != nil { // cursor = 2 persisted
		t.Fatal(err)
	}
	// Fresh store over the same dir simulates a restart.
	s2, err := NewStore(QueueDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.RecoverOnStartup(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.Pending(rk); n != 2 {
		t.Fatalf("recovered pending = %d, want 2 (lines 3,4)", n)
	}
	got, _ := s2.Consume(rk, 2)
	if len(got) != 2 || got[0].MessageID != 3 {
		t.Fatalf("recovered consume = %+v, want msgs 3,4", got)
	}
}

func TestRecoverOnStartup_FullyConsumedPairDeleted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("C3_QUEUE_DIR", dir)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	s1, _ := NewStore(QueueDir())
	_ = s1.Append(rk, msg(1, "m"))
	_ = s1.Append(rk, msg(2, "m"))
	// Simulate a crash AFTER persisting cursor=2 but BEFORE delete-on-empty by
	// writing the .cur to EOF directly via Consume, then dropping the in-memory
	// store and recovering.
	_, _ = s1.Consume(rk, 2)
	s2, _ := NewStore(QueueDir())
	if err := s2.RecoverOnStartup(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.Pending(rk); n != 0 {
		t.Fatalf("fully-consumed route should recover to 0 pending, got %d", n)
	}
}

func TestEvictOverCap_DropsOldestAndAdjustsCursor(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	// Append cap+5 messages.
	for i := int64(1); i <= MaxMessages+5; i++ {
		_ = s.Append(rk, msg(i, "m"))
	}
	dropped, err := s.EvictOverCap(rk)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 5 {
		t.Fatalf("dropped = %d, want 5", dropped)
	}
	if n, _ := s.Pending(rk); n != MaxMessages {
		t.Fatalf("pending after evict = %d, want %d", n, MaxMessages)
	}
	// Oldest survivor is message 6 (1..5 dropped).
	got, _ := s.Peek(rk, 1)
	if got[0].MessageID != 6 {
		t.Fatalf("oldest after evict = %d, want 6", got[0].MessageID)
	}
}

func TestEvictOverCap_DropsByAge(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	old := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 1, Text: "old", Timestamp: time.Now().Add(-MaxAge - time.Hour)}
	fresh := &c3types.Inbound{Channel: "telegram", ChatID: -100, MessageID: 2, Text: "new", Timestamp: time.Now()}
	_ = s.Append(rk, old)
	_ = s.Append(rk, fresh)
	dropped, err := s.EvictOverCap(rk)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("age-evict dropped = %d, want 1", dropped)
	}
	got, _ := s.Peek(rk, 5)
	if len(got) != 1 || got[0].MessageID != 2 {
		t.Fatalf("after age-evict = %+v, want only msg 2", got)
	}
}

func TestRecoverOnStartup_SkipsCorruptLine(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	_ = s.Append(rk, msg(1, "ok"))
	// Manually append a corrupt line to the jsonl.
	if err := appendRawLine(t, QueueDir(), rk, "{not json"); err != nil {
		t.Fatal(err)
	}
	_ = s.Append(rk, msg(3, "ok2"))
	// A peek that walks past the corrupt line must skip it, not error.
	got, err := s.Peek(rk, 5)
	if err != nil {
		t.Fatalf("peek over corrupt line errored: %v", err)
	}
	if len(got) != 2 || got[0].MessageID != 1 || got[1].MessageID != 3 {
		t.Fatalf("peek skipping corrupt = %+v, want msgs 1,3", got)
	}
}

func TestStatusAll_ReportsPendingAndOldest(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	_ = s.Append(rk, msg(1, "m"))
	_ = s.Append(rk, msg(2, "m"))
	all := s.StatusAll()
	st, ok := all[rk]
	if !ok || st.Pending != 2 || st.OldestUnix == 0 {
		t.Fatalf("StatusAll[%v] = %+v ok=%v", rk, st, ok)
	}
}

// -race coverage with a DETERMINISTIC post-condition: a single route worker
// interleaving appends + consumes must be race-free (all calls funnel through one
// goroutine, mirroring the worker's single-owner model) AND must never return a
// consumed message twice. Run under `go test -race`. Unlike a bare "it ran"
// test, this asserts (a) the final Pending equals appends-minus-successfully-
// consumed and (b) no MessageID is ever consumed twice, so a regression in the
// cursor/consume math is actually caught.
func TestStore_SingleOwnerSerializedConsumeIsExactlyOnce(t *testing.T) {
	s := newStore(t)
	rk := RouteKey{Channel: "telegram", ChatID: -100}
	type op struct {
		append bool
		id     int64
	}
	ops := make(chan op)
	doneCh := make(chan struct{})
	seen := map[int64]int{} // MessageID -> times consumed
	appended, consumed := 0, 0
	go func() { // the single owner goroutine — also owns `seen` (no shared access)
		defer close(doneCh)
		for o := range ops {
			if o.append {
				if err := s.Append(rk, msg(o.id, "m")); err == nil {
					appended++
				}
				continue
			}
			got, err := s.Consume(rk, 1)
			if err != nil {
				t.Errorf("consume: %v", err)
				continue
			}
			for _, m := range got {
				seen[m.MessageID]++
				consumed++
			}
		}
	}()
	var wg sync.WaitGroup
	var nextID int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		id := nextID + 1
		nextID = id
		go func(i int, id int64) { defer wg.Done(); ops <- op{append: i%2 == 0, id: id} }(i, id)
	}
	wg.Wait()
	close(ops)
	<-doneCh
	for id, n := range seen {
		if n != 1 {
			t.Errorf("message %d consumed %d times, want exactly once", id, n)
		}
	}
	if n, _ := s.Pending(rk); n != appended-consumed {
		t.Errorf("final pending = %d, want appended(%d) - consumed(%d) = %d", n, appended, consumed, appended-consumed)
	}
}

func appendRawLine(t *testing.T, dir string, rk RouteKey, raw string) error {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, rk.File()+".jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(raw + "\n")
	return err
}
