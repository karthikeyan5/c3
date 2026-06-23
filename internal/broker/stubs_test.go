package broker

import "testing"

func TestStubRegistry_AssignsMonotonicConnID(t *testing.T) {
	r := NewStubRegistry()
	a := r.Register("claude", 1, "/x", nil)
	b := r.Register("codex", 2, "/y", nil)
	c := r.Register("claude", 3, "/x", nil)

	if !(a.ConnID < b.ConnID && b.ConnID < c.ConnID) {
		t.Errorf("ConnIDs not monotonic: %d, %d, %d", a.ConnID, b.ConnID, c.ConnID)
	}
}

func TestStub_StableSessionID(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 42, "/x", nil)
	// Register leaves the stable id empty (it arrives later via RecoverSessionReq).
	if s.StableSessionIDValue() != "" {
		t.Fatalf("fresh stub stable id = %q, want empty", s.StableSessionIDValue())
	}
	s.SetStableSessionID("sess-1")
	if s.StableSessionIDValue() != "sess-1" {
		t.Fatalf("StableSessionIDValue = %q, want sess-1", s.StableSessionIDValue())
	}
	// Last write wins (idempotent re-key).
	s.SetStableSessionID("sess-2")
	if s.StableSessionIDValue() != "sess-2" {
		t.Fatalf("StableSessionIDValue = %q, want sess-2 after re-set", s.StableSessionIDValue())
	}
}

func TestStubRegistry_GetByConnID(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 1, "/x", nil)

	got, ok := r.Get(s.ConnID)
	if !ok {
		t.Fatal("expected to find by ConnID")
	}
	if got.PID != 1 || got.CWD != "/x" {
		t.Errorf("got %+v", got)
	}
}

func TestStubRegistry_Unregister(t *testing.T) {
	r := NewStubRegistry()
	s := r.Register("claude", 1, "/x", nil)
	r.Unregister(s.ConnID)
	if _, ok := r.Get(s.ConnID); ok {
		t.Error("expected stub gone after Unregister")
	}
}
