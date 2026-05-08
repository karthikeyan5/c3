package broker

import (
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// Server is a unix-socket accept loop driven by a Broker.
type Server struct {
	ln   net.Listener
	br   *Broker
	wg   sync.WaitGroup
	stop atomic.Bool
}

// Listen binds a unix socket at path (mode 0600) and starts accepting
// connections. Caller must Stop() at shutdown to drain in-flight handlers.
func Listen(path string, br *Broker) (*Server, error) {
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
