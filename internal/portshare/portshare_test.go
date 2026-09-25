package portshare

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func dial(t *testing.T, addr net.Addr) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func send(t *testing.T, c net.Conn, s string) {
	t.Helper()
	if _, err := io.WriteString(c, s); err != nil {
		t.Fatal(err)
	}
}

// accept returns the next connection l accepts, failing after 10 seconds.
func accept(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		t.Cleanup(func() { r.c.Close() })
		return r.c
	case <-time.After(10 * time.Second):
		t.Fatal("nothing to accept after 10 seconds")
		return nil
	}
}

func readN(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// wantClosed fails unless the other end closes c within 10 seconds.
func wantClosed(t *testing.T, c net.Conn, what string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Errorf("%s is still open: %v", what, err)
	}
}

// waitPending waits until n connections await their first byte.
func waitPending(t *testing.T, s *Splitter, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.pending)
		s.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections await their first byte, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// tcpPair returns both ends of a TCP connection.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln := listen(t)
	client = dial(t, ln.Addr())
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return client, server
}

type tempError struct{}

func (tempError) Error() string   { return "accept: too many open files" }
func (tempError) Temporary() bool { return true }
func (tempError) Timeout() bool   { return false }

// fakeListener returns its results in turn, connections or errors, then
// blocks until it is closed.
type fakeListener struct {
	mu      sync.Mutex
	results []any
	calls   int
	closed  chan struct{}
	once    sync.Once
}

func newFakeListener(results ...any) *fakeListener {
	return &fakeListener{results: results, closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	if len(l.results) > 0 {
		r := l.results[0]
		l.results = l.results[1:]
		l.mu.Unlock()
		if err, ok := r.(error); ok {
			return nil, err
		}
		return r.(net.Conn), nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestSplitRoutes(t *testing.T) {
	ln := listen(t)
	s := Split(ln, Options{})
	defer s.Close()
	if s.timeout != 10*time.Second {
		t.Errorf("default peek timeout %v, want 10s", s.timeout)
	}
	if s.TLS().Addr() != ln.Addr() || s.Plain().Addr() != ln.Addr() {
		t.Error("the listeners don't report the port's address")
	}
	for _, tc := range []struct {
		first string
		tls   bool
	}{
		{"\x16\x03\x01\x02\x00", true},
		{"\x16", true},
		{"GET / HTTP/1.1\r\n", false},
		{"PRI * HTTP/2.0\r\n", false},
		{"\x00", false},
		{"\x15\x03\x01", false},
		{"\x17\x03\x03", false},
		{"\xff", false},
	} {
		c := dial(t, ln.Addr())
		send(t, c, tc.first)
		l := s.Plain()
		if tc.tls {
			l = s.TLS()
		}
		got := accept(t, l)
		if r := readN(t, got, len(tc.first)); r != tc.first {
			t.Errorf("read %q, want %q", r, tc.first)
		}
		if got.RemoteAddr().String() != c.LocalAddr().String() {
			t.Errorf("accepted a connection from %v, want %v", got.RemoteAddr(), c.LocalAddr())
		}
		send(t, got, "reply")
		if r := readN(t, c, 5); r != "reply" {
			t.Errorf("the client read %q", r)
		}
	}
}

func TestSlowConnections(t *testing.T) {
	ln := listen(t)
	s := Split(ln, Options{PeekTimeout: time.Minute})
	defer s.Close()
	var silent []net.Conn
	for range 20 {
		silent = append(silent, dial(t, ln.Addr()))
	}
	waitPending(t, s, len(silent))

	c := dial(t, ln.Addr())
	send(t, c, "G")
	if r := readN(t, accept(t, s.Plain()), 1); r != "G" {
		t.Errorf("read %q", r)
	}

	unaccepted := dial(t, ln.Addr())
	send(t, unaccepted, "\x16")
	c = dial(t, ln.Addr())
	send(t, c, "P")
	if r := readN(t, accept(t, s.Plain()), 1); r != "P" {
		t.Errorf("read %q", r)
	}
	if r := readN(t, accept(t, s.TLS()), 1); r != "\x16" {
		t.Errorf("read %q from the connection nobody accepted at first", r)
	}

	send(t, silent[7], "\x16late")
	if r := readN(t, accept(t, s.TLS()), 5); r != "\x16late" {
		t.Errorf("read %q from a connection slow to speak", r)
	}
	waitPending(t, s, len(silent)-1)
}

func TestPeekTimeout(t *testing.T) {
	ln := listen(t)
	s := Split(ln, Options{PeekTimeout: 50 * time.Millisecond})
	defer s.Close()
	start := time.Now()
	wantClosed(t, dial(t, ln.Addr()), "a silent connection")
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("a silent connection was closed after %v, before its timeout", d)
	}
	waitPending(t, s, 0)

	c := dial(t, ln.Addr())
	send(t, c, "G")
	if r := readN(t, accept(t, s.Plain()), 1); r != "G" {
		t.Errorf("after a timeout, read %q", r)
	}
}

func TestHandoffTimeout(t *testing.T) {
	if s := Split(listen(t), Options{}); s.handoff != 10*time.Second {
		t.Errorf("default handoff timeout %v, want 10s", s.handoff)
	}
	ln := listen(t)
	s := Split(ln, Options{HandoffTimeout: 50 * time.Millisecond})
	defer s.Close()
	start := time.Now()
	c := dial(t, ln.Addr())
	send(t, c, "GET / HTTP/1.1\r\n")
	wantClosed(t, c, "a plain connection nobody accepted")
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("an unaccepted connection was closed after %v, before its timeout", d)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		got, err := s.Plain().Accept()
		if err != nil {
			t.Errorf("Accept after a handoff timeout: %v", err)
		}
		accepted <- got
	}()
	c = dial(t, ln.Addr())
	send(t, c, "G")
	select {
	case got := <-accepted:
		if got == nil {
			return
		}
		defer got.Close()
		if r := readN(t, got, 1); r != "G" {
			t.Errorf("after a handoff timeout, read %q", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nothing accepted after a handoff timeout")
	}
}

func TestClose(t *testing.T) {
	ln := listen(t)
	s := Split(ln, Options{PeekTimeout: time.Minute})
	silent := dial(t, ln.Addr())
	waitPending(t, s, 1)
	unaccepted := dial(t, ln.Addr())
	send(t, unaccepted, "\x16")

	if err := s.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	for name, l := range map[string]net.Listener{"TLS": s.TLS(), "plain": s.Plain()} {
		if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s Accept after Close = %v", name, err)
		}
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the port still accepts: %v", err)
	}
	wantClosed(t, silent, "a connection awaiting its first byte")
	wantClosed(t, unaccepted, "a connection nobody accepted")
	if err := s.Close(); err != nil {
		t.Errorf("closing again = %v", err)
	}
	if err := s.TLS().Close(); err != nil {
		t.Errorf("closing a listener after the Splitter = %v", err)
	}
}

func TestClosingBothListeners(t *testing.T) {
	ln := listen(t)
	s := Split(ln, Options{})
	if err := s.TLS().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TLS().Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept on a closed listener = %v", err)
	}
	c := dial(t, ln.Addr())
	send(t, c, "G")
	if r := readN(t, accept(t, s.Plain()), 1); r != "G" {
		t.Errorf("with the TLS listener closed, read %q", r)
	}
	c = dial(t, ln.Addr())
	send(t, c, "\x16")
	wantClosed(t, c, "a TLS connection with its listener closed")

	if err := s.Plain().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("with both listeners closed, the port still accepts: %v", err)
	}
	if _, err := s.Plain().Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept on a closed listener = %v", err)
	}
}

func TestTemporaryAcceptErrors(t *testing.T) {
	client, server := tcpPair(t)
	send(t, client, "G")
	fl := newFakeListener(tempError{}, tempError{}, server)
	start := time.Now()
	s := Split(fl, Options{})
	defer s.Close()
	if r := readN(t, accept(t, s.Plain()), 1); r != "G" {
		t.Errorf("read %q", r)
	}
	if d := time.Since(start); d < 15*time.Millisecond {
		t.Errorf("retried two failed accepts within %v, want a pause after each", d)
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if fl.calls < 3 {
		t.Errorf("Accept was called %d times", fl.calls)
	}
}

func TestAcceptFailure(t *testing.T) {
	boom := errors.New("accept: listener broke")
	fl := newFakeListener(boom)
	s := Split(fl, Options{})
	for name, l := range map[string]net.Listener{"TLS": s.TLS(), "plain": s.Plain()} {
		if _, err := l.Accept(); err != boom {
			t.Errorf("%s Accept = %v, want the port's error", name, err)
		}
	}
	select {
	case <-fl.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the port wasn't closed after it failed")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close after a failure = %v", err)
	}
}

func TestPeekedConn(t *testing.T) {
	client, server := tcpPair(t)
	send(t, client, "bc")
	c := &peekedConn{Conn: server, first: 'a'}
	if n, err := c.Read(nil); n != 0 || err != nil {
		t.Errorf("an empty read = %d, %v", n, err)
	}
	if r := readN(t, c, 3); r != "abc" {
		t.Errorf("read %q, want the peeked byte first", r)
	}

	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("after CloseWrite the client read %v, want EOF", err)
	}
	send(t, client, "d")
	if r := readN(t, c, 1); r != "d" {
		t.Errorf("after CloseWrite read %q", r)
	}

	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	if err := (&peekedConn{Conn: p1}).CloseWrite(); err != nil {
		t.Errorf("CloseWrite on a connection that can't half-close = %v", err)
	}
}
