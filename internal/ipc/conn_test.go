package ipc

import (
	"net"
	"sync"
	"testing"
)

func newPipePair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	return NewConn(a), NewConn(b)
}

func TestConn_RoundtripFrame(t *testing.T) {
	a, b := newPipePair(t)
	defer a.Close()
	defer b.Close()

	go func() {
		_ = a.WriteJSON(HelloMsg{Op: OpHello, CLI: "claude", PID: 1, CWD: "/x"})
	}()

	raw, err := b.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	op, err := PeekOp(raw)
	if err != nil {
		t.Fatal(err)
	}
	if op != OpHello {
		t.Errorf("op=%q, want %q", op, OpHello)
	}
}

func TestConn_ConcurrentWritesAreFramed(t *testing.T) {
	a, b := newPipePair(t)
	defer a.Close()
	defer b.Close()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = a.WriteJSON(HelloMsg{Op: OpHello, CLI: "claude", PID: i, CWD: "/x"})
		}(i)
	}

	seen := 0
	for seen < N {
		raw, err := b.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		op, err := PeekOp(raw)
		if err != nil {
			t.Fatalf("frame %d malformed: %v (raw=%s)", seen, err, raw)
		}
		if op != OpHello {
			t.Fatalf("frame %d op=%q, want %q", seen, op, OpHello)
		}
		seen++
	}
	wg.Wait()
}
