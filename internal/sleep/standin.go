package sleep

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	// legacyWait is how long to wait for the rest of a legacy ping after its
	// first byte. Clients send it in one piece.
	legacyWait = 250 * time.Millisecond
	// drainWait and maxDrain bound what is read after the last reply.
	drainWait = 2 * time.Second
	maxDrain  = 64 << 10
)

// standIn answers on the game port during one sleep.
type standIn struct {
	m    *Manager
	ln   net.Listener
	done chan struct{}
	wg   sync.WaitGroup

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]string
	perIP  map[string]int
}

func newStandIn(m *Manager, ln net.Listener) *standIn {
	return &standIn{m: m, ln: ln, done: make(chan struct{}), conns: map[net.Conn]string{}, perIP: map[string]int{}}
}

func (s *standIn) start() {
	s.wg.Add(1)
	go s.serve()
}

func (s *standIn) serve() {
	defer s.wg.Done()
	var delay time.Duration
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Out of file descriptors or similar: back off and carry on.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			select {
			case <-s.done:
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		if !s.track(c) {
			_ = c.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			s.handle(c)
		}()
	}
}

// addrKey groups connections for the per-address cap. An IPv6 host
// usually has a whole /64.
func addrKey(a net.Addr) string {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return a.String()
	}
	if ip4 := ta.IP.To4(); ip4 != nil {
		return ip4.String()
	}
	return ta.IP.Mask(net.CIDRMask(64, 128)).String()
}

func (s *standIn) track(c net.Conn) bool {
	key := addrKey(c.RemoteAddr())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.conns) >= s.m.cfg.MaxConns || s.perIP[key] >= s.m.cfg.MaxConnsPerIP {
		return false
	}
	s.conns[c] = key
	s.perIP[key]++
	return true
}

func (s *standIn) untrack(c net.Conn) {
	_ = c.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.conns[c]
	delete(s.conns, c)
	if s.perIP[key]--; s.perIP[key] <= 0 {
		delete(s.perIP, key)
	}
}

// close stops accepting and frees the port, gives replies being written up
// to grace to finish, then cuts off the rest. It returns once every
// connection is closed.
func (s *standIn) close(grace time.Duration) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	_ = s.ln.Close()
	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-finished:
		return
	case <-t.C:
	}
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	<-finished
}

// session is one client connection. The deadline covers the whole
// exchange, so a client that sends slowly is cut off all the same.
type session struct {
	c        net.Conn
	br       *bufio.Reader
	deadline time.Time
}

func (s *standIn) handle(c net.Conn) {
	ss := &session{c: c, br: bufio.NewReaderSize(c, 256), deadline: time.Now().Add(s.m.cfg.ConnTimeout)}
	_ = c.SetDeadline(ss.deadline)
	first, err := ss.br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0xFE && s.legacy(ss) {
		return
	}
	p, err := readFrame(ss.br, maxHandshake)
	if err != nil {
		return
	}
	hs, err := parseHandshake(p)
	if err != nil {
		return
	}
	switch hs.intent {
	case intentStatus:
		s.status(ss, hs.protocol)
	case intentLogin, intentTransfer:
		s.login(ss)
	}
}

// status answers a server list ping: the status request, then the ping.
func (s *standIn) status(ss *session, protocol int32) {
	p, err := readFrame(ss.br, maxSmallPacket)
	if err != nil {
		return
	}
	if p.id == 0x00 && p.r.Len() == 0 {
		reply := frame(0x00, appendString(nil, statusJSON(s.m.status(), protocol)))
		if _, err := ss.c.Write(reply); err != nil {
			return
		}
		if p, err = readFrame(ss.br, maxSmallPacket); err != nil {
			return
		}
	}
	if p.id != 0x01 || p.r.Len() != 8 {
		return
	}
	payload := make([]byte, 8)
	_, _ = io.ReadFull(p.r, payload)
	_, _ = ss.c.Write(frame(0x01, payload))
}

// login answers a player who tries to join: it may wake the server, and it
// always disconnects them with a message saying what happens next.
func (s *standIn) login(ss *session) {
	p, err := readFrame(ss.br, maxLoginStart)
	if err != nil {
		return
	}
	name, err := parseLoginStart(p)
	if err != nil {
		return
	}
	msg := s.m.joinAttempt(name)
	if _, err := ss.c.Write(frame(0x00, appendString(nil, disconnectJSON(msg)))); err != nil {
		return
	}
	ss.finish()
}

// legacy answers a server list ping from a client older than 1.7. It
// reports false when the bytes turn out to start a modern packet whose
// length begins with 0xFE, as a 254-byte handshake's does.
func (s *standIn) legacy(ss *session) bool {
	wait := time.Now().Add(legacyWait)
	if ss.deadline.Before(wait) {
		wait = ss.deadline
	}
	_ = ss.c.SetReadDeadline(wait)
	b, _ := ss.br.Peek(3)
	_ = ss.c.SetReadDeadline(ss.deadline)
	switch {
	case len(b) >= 2 && b[1] != 0x01, len(b) == 3 && b[2] != 0xFA:
		return false
	}
	if _, err := ss.c.Write(legacyKick(legacyStatus(s.m.status(), len(b) == 1))); err == nil {
		ss.finish()
	}
	return true
}

// finish ends the connection after the last reply: it half-closes and
// briefly reads what the client still sends, because closing with unread
// data resets the connection and the client may lose the reply.
func (ss *session) finish() {
	if cw, ok := ss.c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	wait := time.Now().Add(drainWait)
	if ss.deadline.Before(wait) {
		wait = ss.deadline
	}
	_ = ss.c.SetReadDeadline(wait)
	_, _ = io.CopyN(io.Discard, ss.br, maxDrain)
}
