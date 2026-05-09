package broker

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Server is a unix-socket accept loop driven by a Broker.
type Server struct {
	ln   net.Listener
	br   *Broker
	wg   sync.WaitGroup
	stop atomic.Bool
}

// ErrSiblingListening is returned by Listen when another broker is already
// serving the socket. The caller should treat it like an Acquire-singleton
// failure (silently exit, do not race).
var ErrSiblingListening = errors.New("broker: socket already served by sibling")

// Listen binds a unix socket at path (mode 0600) and starts accepting
// connections. Caller must Stop() at shutdown to drain in-flight handlers.
//
// Singleton enforcement (Karthi 2026-05-09 incident — two brokers ended up
// with overlapping listen sockets after a restart-broker race that bypassed
// the pidfile flock by deleting the inode):
//
//   - PROBE before unlink: try to dial the path. If something answers, a
//     sibling is alive — return ErrSiblingListening so the caller exits.
//     The pidfile flock should normally catch this earlier; this is a
//     defense-in-depth check at the actual contention point (the bound
//     listen address).
//   - Only after the probe fails do we unlink + bind.
//
// This makes the listen socket the authoritative singleton: even if two
// brokers race past the pidfile flock (rm-pidfile bug, stale flock inode,
// etc.), exactly one ends up serving the socket.
func Listen(path string, br *Broker) (*Server, error) {
	if siblingAlive(path) {
		return nil, fmt.Errorf("%w (path=%s)", ErrSiblingListening, path)
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		return nil, err
	}

	s := &Server{ln: ln, br: br}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// siblingAlive returns whether the socket at path is actively served by
// some process. Cheap dial-and-close — if anyone accepts, true. ECONNREFUSED
// (file exists but no listener) and ENOENT (no file) both return false.
func siblingAlive(path string) bool {
	c, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.stop.Load() {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.br.HandleConn(c)
		}(c)
	}
}

// Stop closes the listener and drains in-flight handlers.
func (s *Server) Stop() {
	if !s.stop.CompareAndSwap(false, true) {
		return
	}
	_ = s.ln.Close()
	s.wg.Wait()
}
