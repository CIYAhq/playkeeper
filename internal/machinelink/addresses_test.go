package machinelink

import (
	"context"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// names is a dialer that reaches hubs by made-up names, and remembers what
// it dialled and the connections it made.
type names struct {
	mu sync.Mutex
	// to maps a name ("host:port") to the address that answers it; a name
	// that isn't there doesn't exist.
	to     map[string]string
	dialed []string
	conns  []net.Conn
}

func (n *names) set(name, addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.to[name] = addr
}

func (n *names) dial(ctx context.Context, network, name string) (net.Conn, error) {
	n.mu.Lock()
	n.dialed = append(n.dialed, name)
	addr, ok := n.to[name]
	n.mu.Unlock()
	if !ok {
		host, _, _ := net.SplitHostPort(name)
		return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}}
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err == nil {
		n.mu.Lock()
		n.conns = append(n.conns, c)
		n.mu.Unlock()
	}
	return c, err
}

// cut closes every connection made so far.
func (n *names) cut() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.conns {
		c.Close()
	}
}

func (n *names) dialledAny(name string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Contains(n.dialed, name)
}

func mustAddress(t *testing.T, s string) Address {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A dashboard that moves to another address keeps its machines: they try
// the new address first and the old one after it, and say when they
// connected at the new one, so it can be kept.
func TestLinkFollowsTheDashboardToItsNewAddress(t *testing.T) {
	th := startHub(t, nil)
	d, id := th.join(t, th.addr, "home")
	d.Address = "old.example:8443"
	dns := &names{to: map[string]string{"old.example:8443": th.addr}}
	var mu sync.Mutex
	var followed []Address
	tl := startLink(t, d, id, newFakeAgent("home"), func(o *LinkOptions) {
		o.Addresses, o.Dial = []string{"new.example:8443", "old.example:8443"}, dns.dial
		o.OnAddress = func(a Address) {
			mu.Lock()
			defer mu.Unlock()
			followed = append(followed, a)
		}
	})
	eventually(t, "connected at the old address", func() bool { return th.Connected(d.MachineID) && tl.Status().State == LinkConnected })
	mu.Lock()
	if len(followed) != 0 {
		t.Errorf("connecting at the old address was reported as moving: %v", followed)
	}
	mu.Unlock()
	if s := tl.Status(); s.Dashboard != mustAddress(t, "old.example:8443").String() || !dns.dialledAny("new.example:8443") {
		t.Fatalf("status %+v after trying %v", s, dns.dialed)
	}

	dns.set("new.example:8443", th.addr)
	dns.cut()
	eventually(t, "connected again at the new address", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(followed) == 1 && th.Connected(d.MachineID)
	})
	if followed[0] != mustAddress(t, "new.example:8443") || tl.Status().Dashboard != followed[0].String() {
		t.Errorf("followed %v, status %+v", followed, tl.Status())
	}
}

// Only an address that leads nowhere, or to something without the
// dashboard's key, gives way to the next one.
func TestLinkTriesTheNextAddressOnlyWhenTheDashboardIsNotThere(t *testing.T) {
	th := startHub(t, nil)
	other := startHub(t, nil)
	d, id := th.join(t, th.addr, "home")
	d.Address = "second.example:8443"
	dns := &names{to: map[string]string{"first.example:8443": other.addr, "second.example:8443": th.addr}}
	var moved atomic.Int32
	startLink(t, d, id, newFakeAgent("home"), func(o *LinkOptions) {
		o.Addresses, o.Dial = []string{"first.example:8443", "second.example:8443"}, dns.dial
		o.OnAddress = func(Address) { moved.Add(1) }
	})
	eventually(t, "connected past another dashboard's key", func() bool { return th.Connected(d.MachineID) })
	if n := other.events.count(EventConnected); n != 0 || moved.Load() != 0 {
		t.Errorf("%d connections to the other dashboard, %d moves", n, moved.Load())
	}

	ghost := Dashboard{Address: "first.example:8443", Key: th.id.PublicKey(), MachineID: "abcdefghij", Name: "ghost"}
	refused := &names{to: map[string]string{"first.example:8443": th.addr, "second.example:8443": th.addr}}
	tl := startLink(t, ghost, mustIdentity(t), newFakeAgent("ghost"), func(o *LinkOptions) {
		o.Addresses, o.Dial, o.RetryLater = []string{"first.example:8443", "second.example:8443"}, refused.dial, time.Hour
	})
	eventually(t, "the dashboard's refusal", func() bool {
		s := tl.Status()
		return s.State == LinkRetrying && s.Problem != nil && s.Problem.Code == CodeMachineUnknown
	})
	if refused.dialledAny("second.example:8443") {
		t.Error("the link tried another address after the dashboard refused it")
	}
	if _, err := NewLink(LinkOptions{Dashboard: d, Identity: id, Handler: newFakeAgent("home"), Addresses: []string{"https://x.example"}}); err == nil {
		t.Error("an address that is not host:port was accepted")
	}
}
