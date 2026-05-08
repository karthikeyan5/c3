package broker

import (
	"context"
	"testing"
	"time"
)

func TestWorker_IdleShutdown(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, 50*time.Millisecond, nil)
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on idle within 500ms")
	}
}

func TestWorker_StopExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	go w.Stop()
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on Stop")
	}
}

func TestWorker_ReleaseJobExits(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	if !w.Submit(Job{Kind: JobRelease}) {
		t.Fatal("Submit should succeed before stop")
	}
	select {
	case <-w.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not exit on JobRelease")
	}
}

func TestWorker_SubmitAfterStopReturnsFalse(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	w.Stop()
	if w.Submit(Job{Kind: JobInbound}) {
		t.Error("Submit after Stop should return false")
	}
}

func TestWorker_OutboundStubReturnsErr(t *testing.T) {
	w := newRouteWorker(context.Background(), RouteKey{Channel: "x"}, time.Hour, nil)
	defer w.Stop()

	resultCh := make(chan OutboundResult, 1)
	if !w.Submit(Job{Kind: JobOutbound, Outbound: &OutboundJob{Tool: "reply", ResultCh: resultCh}}) {
		t.Fatal("Submit failed")
	}
	select {
	case r := <-resultCh:
		if r.Err == nil {
			t.Error("expected stub error in Phase 4A")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no result within 500ms")
	}
}
