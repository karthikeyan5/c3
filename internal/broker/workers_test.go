package broker

import (
	"context"
	"testing"
	"time"
)

func TestWorkerPool_LazySpawnAndReap(t *testing.T) {
	pool := NewWorkerPool(context.Background(), 30*time.Millisecond)
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

func TestWorkerPool_StopDrains(t *testing.T) {
	pool := NewWorkerPool(context.Background(), time.Hour)
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
