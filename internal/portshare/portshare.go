// Package portshare serves TLS and plain connections on one listening
// port. It tells them apart by their first byte: a TLS connection starts
// with a handshake record, byte 0x16, while a plain HTTP request starts
// with a letter. Playkeeper serves its panel over HTTPS and, on the same
// port, resource packs over plain HTTP to players' games, which refuse the
// panel's self-signed certificate.
package portshare

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const tlsHandshake = 0x16

// Options configure Split.
type Options struct {
	// PeekTimeout is how long a new connection may take to send its first
	// byte before it is closed, 10 seconds by default.
	PeekTimeout time.Duration
	// HandoffTimeout is how long a routed connection waits for Accept on
	// its listener before it is closed, 10 seconds by default, so that a
	// listener nobody serves doesn't hold connections open.
	HandoffTimeout time.Duration
}

// Splitter hands the connections accepted on a listener to two listeners
// by their first byte.
type Splitter struct {
	ln      net.Listener
	timeout time.Duration
	handoff time.Duration
	tls     *listener
	plain   *listener

	mu      sync.Mutex
	pending map[net.Conn]struct{}
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

// Split starts accepting connections on ln. Each connection's first byte
// is awaited in a goroutine of its own, so a connection that is slow to
// speak, or never does, holds up no other.
func Split(ln net.Listener, opts Options) *Splitter {
	if opts.PeekTimeout <= 0 {
		opts.PeekTimeout = 10 * time.Second
	}
	if opts.HandoffTimeout <= 0 {
		opts.HandoffTimeout = 10 * time.Second
	}
	s := &Splitter{ln: ln, timeout: opts.PeekTimeout, handoff: opts.HandoffTimeout, pending: map[net.Conn]struct{}{}}
	s.tls = &listener{s: s, conns: make(chan net.Conn), done: make(chan struct{})}
	s.plain = &listener{s: s, conns: make(chan net.Conn), done: make(chan struct{})}
	go s.serve()
	return s
}

// TLS is the listener of the connections that start with a TLS handshake,
// to wrap with tls.NewListener or serve with http.Server.ServeTLS.
func (s *Splitter) TLS() net.Listener { return s.tls }

// Plain is the listener of all other connections.
func (s *Splitter) Plain() net.Listener { return s.plain }

// Close stops accepting connections: it closes the listener given to
// Split and the connections whose first byte is still awaited, and makes
// Accept on both listeners fail. Closing both listeners closes the
// Splitter too.
func (s *Splitter) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		pending := s.pending
		s.pending = nil
		s.mu.Unlock()
		s.closeErr = s.ln.Close()
		for c := range pending {
			c.Close()
		}
		s.tls.shut(net.ErrClosed)
		s.plain.shut(net.ErrClosed)
	})
	return s.closeErr
}

func (s *Splitter) serve() {
	var delay time.Duration
	for {
		c, err := s.ln.Accept()
		if err != nil {
			// Like http.Server, retry errors such as running out of file
			// descriptors after a pause.
			var te interface{ Temporary() bool }
			if errors.As(err, &te) && te.Temporary() && !s.isClosed() {
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			s.tls.shut(err)
			s.plain.shut(err)
			s.Close()
			return
		}
		delay = 0
		go s.route(c)
	}
}

func (s *Splitter) route(c net.Conn) {
	if !s.track(c, true) {
		c.Close()
		return
	}
	var b [1]byte
	c.SetReadDeadline(time.Now().Add(s.timeout))
	_, err := io.ReadFull(c, b[:])
	c.SetReadDeadline(time.Time{})
	if !s.track(c, false) || err != nil {
		c.Close()
		return
	}
	dst := s.plain
	if b[0] == tlsHandshake {
		dst = s.tls
	}
	dst.deliver(&peekedConn{Conn: c, first: b[0]})
}

// track adds c to the connections Close closes, or removes it. It reports
// false once the Splitter is closed.
func (s *Splitter) track(c net.Conn, add bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if add {
		s.pending[c] = struct{}{}
	} else {
		delete(s.pending, c)
	}
	return true
}

func (s *Splitter) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// listener is one of a Splitter's two listeners.
type listener struct {
	s     *Splitter
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, l.err
	default:
	}
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

// Close stops the listener; closing both closes the Splitter.
func (l *listener) Close() error {
	l.shut(net.ErrClosed)
	if l.s.tls.isShut() && l.s.plain.isShut() {
		return l.s.Close()
	}
	return nil
}

func (l *listener) Addr() net.Addr { return l.s.ln.Addr() }

// shut makes Accept fail with err from now on.
func (l *listener) shut(err error) {
	l.once.Do(func() {
		l.err = err
		close(l.done)
	})
}

func (l *listener) isShut() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// deliver waits for Accept to take c, and closes c if the listener shuts
// or the handoff times out first.
func (l *listener) deliver(c net.Conn) {
	t := time.NewTimer(l.s.handoff)
	defer t.Stop()
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
	case <-t.C:
		c.Close()
	}
}

// peekedConn is a connection whose first byte was read to route it, and
// is returned again by the first Read.
type peekedConn struct {
	net.Conn
	mu    sync.Mutex
	first byte
	used  bool
}

func (c *peekedConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if !c.used && len(p) > 0 {
		c.used = true
		c.mu.Unlock()
		p[0] = c.first
		return 1, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}

// CloseWrite shuts the connection's writing side, as http.Server does
// before closing a connection, when the connection can.
func (c *peekedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
