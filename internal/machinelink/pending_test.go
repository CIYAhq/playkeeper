package machinelink

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func (p *pendingConns) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.list)
}

type closeRecorder struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeRecorder) Close() error {
	c.closed.Store(true)
	return nil
}

func TestPendingConnectionsMakeRoomForNewcomers(t *testing.T) {
	clock := newClock()
	logs := &syncBuffer{}
	p := newPendingConns(4, 2, clock.Now, slog.New(slog.NewTextHandler(logs, nil)))
	conns := map[string]*closeRecorder{}
	releases := map[string]func(){}
	add := func(name, ip string) {
		c := &closeRecorder{}
		conns[name] = c
		releases[name] = p.add(pendingSource(ip+":1"), c)
	}
	want := func(names ...string) {
		t.Helper()
		var closed []string
		for name, c := range conns {
			if c.closed.Load() {
				closed = append(closed, name)
			}
		}
		slices.Sort(closed)
		if !slices.Equal(closed, names) {
			t.Fatalf("closed %v, want %v", closed, names)
		}
	}

	add("a1", "192.0.2.1")
	add("a2", "192.0.2.1")
	want()
	add("a3", "192.0.2.1") // a is at its limit, so its oldest goes
	want("a1")
	add("b1", "192.0.2.2")
	add("c1", "192.0.2.3")
	want("a1")
	add("d1", "192.0.2.4") // full: the source with the most loses its oldest
	want("a1", "a2")
	add("e1", "192.0.2.5") // full, one each: the oldest goes
	want("a1", "a2", "a3")

	releases["b1"]()
	releases["b1"]()
	releases["a3"]() // pushed out already, so this frees nothing
	add("f1", "192.0.2.6")
	want("a1", "a2", "a3")
	if n := p.count(); n != 4 {
		t.Fatalf("%d pending, want 4", n)
	}
	add("g1", "192.0.2.7")
	want("a1", "a2", "a3", "c1")

	if n := strings.Count(logs.String(), "waiting for their handshake"); n != 1 {
		t.Fatalf("warned %d times in a minute, want once:\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), "closed=1 source=192.0.2.1/32") {
		t.Fatalf("the warning doesn't say what was closed:\n%s", logs)
	}
	clock.Add(time.Minute)
	add("h1", "192.0.2.8")
	want("a1", "a2", "a3", "c1", "d1")
	if !strings.Contains(logs.String(), "closed=4 source=192.0.2.4/32") {
		t.Fatalf("the next minute's warning doesn't count what was closed since:\n%s", logs)
	}
}

func TestPendingSources(t *testing.T) {
	for _, s := range [][2]string{
		{"203.0.113.7:50000", "203.0.113.7:50001"},
		{"[::ffff:203.0.113.7]:1", "203.0.113.7:2"},
		{"[2001:db8:1:2::1]:1", "[2001:db8:1:ffff::9]:1"},
		{"not an address", "nor this"},
	} {
		if a, b := pendingSource(s[0]), pendingSource(s[1]); a != b {
			t.Errorf("%s is %s and %s is %s; want one source", s[0], a, s[1], b)
		}
	}
	for _, s := range [][2]string{
		{"203.0.113.7:1", "203.0.113.8:1"},
		{"[2001:db8:1::1]:1", "[2001:db8:2::1]:1"},
		{"203.0.113.7:1", "not an address"},
	} {
		if pendingSource(s[0]) == pendingSource(s[1]) {
			t.Errorf("%s and %s are one source", s[0], s[1])
		}
	}
	if got := pendingSource("[2001:db8:1:2::1]:1"); got != netip.MustParsePrefix("2001:db8:1::/48") {
		t.Errorf("an IPv6 source is %s, want its /48", got)
	}
}

// dialFrom opens a TCP connection to addr from the loopback address src,
// and sends nothing on it.
func dialFrom(t *testing.T, src, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Skipf("can't connect from %s (only some systems route all of 127/8 to loopback): %v", src, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// closedWithin reports whether the other end closes c within d.
func closedWithin(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	_, err := c.Read(make([]byte, 1))
	return err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
}

func TestIdleConnectionsDontKeepMachinesOut(t *testing.T) {
	th := startHub(t, func(o *HubOptions) {
		o.MaxPending, o.MaxPendingPerSource = 4, 2
		o.HandshakeTimeout = 10 * time.Second
	})
	// Connections that never start their TLS handshake fill the hub's
	// waiting room.
	var idle []net.Conn
	for i, src := range []string{"127.0.0.2", "127.0.0.2", "127.0.0.3", "127.0.0.3"} {
		idle = append(idle, dialFrom(t, src, th.addr))
		eventually(t, "the hub counts the connection", func() bool { return th.pending.count() == i+1 })
	}

	if _, err := th.try(mustIdentity(t), th.code(t)); err != nil {
		t.Fatalf("a machine couldn't join past idle connections: %v", err)
	}
	if !closedWithin(idle[0], 5*time.Second) {
		t.Fatal("the oldest idle connection is still open")
	}
	for i := 1; i < len(idle); i++ {
		if closedWithin(idle[i], 50*time.Millisecond) {
			t.Fatalf("idle connection %d was closed too", i)
		}
	}

	dialFrom(t, "127.0.0.3", th.addr)
	if !closedWithin(idle[2], 5*time.Second) {
		t.Fatal("a source at its limit didn't push out its own oldest connection")
	}
	for _, i := range []int{1, 3} {
		if closedWithin(idle[i], 50*time.Millisecond) {
			t.Fatalf("idle connection %d was closed for another's newcomer", i)
		}
	}
	if !strings.Contains(th.logs.String(), "waiting for their handshake") {
		t.Fatalf("nothing in the log:\n%s", th.logs)
	}
}

func TestTheHelloMustFollowTheHandshake(t *testing.T) {
	th := startHub(t, func(o *HubOptions) { o.HelloTimeout = 200 * time.Millisecond })
	tc := rawDial(t, th.addr, mustIdentity(t), th.id.PublicKey())
	start := time.Now()
	var w welcome
	if err := readFrame(tc, &w); err != nil {
		t.Fatalf("reading the refusal: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the hub waited %v for the hello, past its hello timeout of 200ms", took)
	}
	if w.OK || w.Error == nil || w.Error.Code != CodeProtocol {
		t.Fatalf("got %+v, want a protocol error", w)
	}
	eventually(t, "the connection stops counting as pending", func() bool { return th.pending.count() == 0 })
}
