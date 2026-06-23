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

func TestStubRegistry_RegisterWithSession(t *testing.T) {
	r := NewStubRegistry()
	s := r.RegisterWithSession("claude", 42, "/x", "sess-1", nil)
	if s.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want sess-1", s.SessionID)
	}
	// Plain Register leaves it empty (no behavior change for old callers).
	if r.Register("claude", 7, "/y", nil).SessionID != "" {
		t.Fatal("Register must leave SessionID empty")
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
