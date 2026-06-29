package broker

import (
	"context"
	"testing"
	"time"

	"github.com/karthikeyan5/c3/internal/mappings"
)

func TestWorkerPool_LazySpawnAndReap(t *testing.T) {
	pool := NewWorkerPool(context.Background(), 30*time.Millisecond, nil)
	defer pool.Stop()

	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0", pool.Active())
	}

	pool.Submit(RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 281}, Job{Kind: JobInbound})

	if pool.Active() != 1 {
		t.Errorf("active=%d, want 1 after submit", pool.Active())
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && pool.Active() > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0 after idle reap", pool.Active())
	}
}

// TestWorkerPool_SubmitAfterIdleExitRespawns pins A2 part 2: when the map holds a
// worker that has already EXITED (the post-exit / pre-reap window — the reaper
// goroutine has not yet run its delete), Submit must treat that worker as absent,
// spawn a fresh one, and route the job to it. Pre-fix, Submit handed the job to
// the dead worker, whose Submit returns false (a stopped worker, per A1), so
// WorkerPool.Submit propagated a spurious failure instead of respawning.
//
// We simulate the window deterministically by injecting an already-stopped worker
// into the map (the reaper's delete is exactly what hasn't happened yet), then
// proving the respawned worker actually runs the job via a JobFetch whose ResultCh
// resolves.
func TestWorkerPool_SubmitAfterIdleExitRespawns(t *testing.T) {
	t.Setenv("C3_QUEUE_DIR", t.TempDir())
	b := New(&mappings.MappingsFile{SchemaVersion: 1})
	defer b.Shutdown()

	pool := NewWorkerPool(context.Background(), time.Hour, b)
	defer pool.Stop()
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 281}

	// Exited-but-still-mapped worker (post-exit, pre-reap).
	dead := newRouteWorker(pool.ctx, key, time.Hour, b)
	dead.Stop()
	pool.mu.Lock()
	pool.workers[key] = dead
	pool.mu.Unlock()

	resultCh := make(chan FetchResult, 1)
	if !pool.Submit(key, Job{Kind: JobFetch, Fetch: &FetchJob{All: true, ResultCh: resultCh}}) {
		t.Fatal("Submit must respawn a fresh worker for an exited mapped worker and return true")
	}
	select {
	case r := <-resultCh:
		if r.Err != nil {
			t.Fatalf("respawned worker should process the fetch cleanly; got err %v", r.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("respawned worker did not process the fetch (job stranded)")
	}
}

// TestWorkerPool_ReaperDoesNotDeleteRespawn is the race regression for A2 part 1
// (delete-if-same): an OLD worker's reaper must remove the map entry ONLY if it
// still points at THAT worker, so a respawn that already replaced the entry is not
// clobbered. We force the exact interleave — respawn lands BEFORE the old worker's
// reaper deletes — by holding p.mu across both the old worker's exit and the
// respawn install, parking the old reaper until W2 is mapped.
func TestWorkerPool_ReaperDoesNotDeleteRespawn(t *testing.T) {
	pool := NewWorkerPool(context.Background(), time.Hour, nil)
	defer pool.Stop()
	key := RouteKey{Channel: "telegram", ChatID: -100, HasTopic: true, TopicID: 281}

	if !pool.Submit(key, Job{Kind: JobInbound}) {
		t.Fatal("seed submit failed")
	}
	pool.mu.Lock()
	w1 := pool.workers[key]
	pool.mu.Unlock()
	if w1 == nil {
		t.Fatal("W1 should be mapped after submit")
	}

	// Hold p.mu across (a) forcing W1 to exit and (b) installing the respawn W2, so
	// W1's production reaper R1 — unblocked by W1.Done() — is parked waiting for
	// p.mu the whole time and can only run its delete AFTER W2 is mapped.
	pool.mu.Lock()
	w1.Stop() // closes W1.done; R1 unblocks and parks on p.mu (held here)
	w2 := newRouteWorker(pool.ctx, key, time.Hour, nil)
	pool.workers[key] = w2
	pool.wg.Add(1)
	go func() {
		<-w2.Done()
		pool.mu.Lock()
		if cur, ok := pool.workers[key]; ok && cur == w2 {
			delete(pool.workers, key)
		}
		pool.mu.Unlock()
		pool.wg.Done()
	}()
	pool.mu.Unlock()

	// R1 now runs. delete-if-same must leave W2 in the map (Active stays 1); the old
	// unconditional delete would clobber the respawn (Active drops to 0).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := pool.Active(); got != 1 {
			t.Fatalf("respawn clobbered by old worker's reaper: Active=%d, want 1", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWorkerPool_StopDrains(t *testing.T) {
	pool := NewWorkerPool(context.Background(), time.Hour, nil)
	for i := 0; i < 5; i++ {
		pool.Submit(RouteKey{Channel: "telegram", ChatID: int64(i)}, Job{Kind: JobInbound})
	}
	if pool.Active() == 0 {
		t.Fatal("expected workers active after submits")
	}
	pool.Stop()
	if pool.Active() != 0 {
		t.Errorf("active=%d, want 0 after Stop", pool.Active())
	}
}
