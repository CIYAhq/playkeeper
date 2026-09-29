package agent

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// newDNSEnv is an agent that answers DNS on the loopback addresses addrs
// give (a free port of 127.0.0.1 by default).
func newDNSEnv(t *testing.T, addrs ...string) *agentEnv {
	t.Helper()
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:0"}
	}
	return newAgentEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.DNSAddrs = func() ([]string, error) { return addrs, nil } }
	})
}

var betaZone = map[string]any{"name": "beta.example.com", "nameserver": "ns-beta.example.com", "records": []map[string]any{
	{"name": "alex", "type": "A", "value": "203.0.113.5"},
	{"name": "m1", "type": "A", "value": "203.0.113.5"},
	{"name": "_minecraft._tcp.alex", "type": "SRV", "value": "m1.beta.example.com", "port": 25567},
}}

func (e *agentEnv) setZone(zone any) (int, api.DNSZoneStatus) {
	e.t.Helper()
	var st api.DNSZoneStatus
	code := e.callInto("PUT", "/v1/dns-zone", map[string]any{"zone": zone, "actor": "admin"}, &st)
	return code, st
}

func (e *agentEnv) zoneStatus() api.DNSZoneStatus {
	e.t.Helper()
	var st api.DNSZoneStatus
	if code := e.callInto("GET", "/v1/dns-zone", nil, &st); code != 200 {
		e.t.Fatalf("dns zone: %d", code)
	}
	return st
}

// dnsQuery is a query for name and qtype.
func dnsQuery(name string, qtype uint16) []byte {
	q := []byte{0xab, 0xcd, 0x01, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range strings.Split(name, ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	return append(q, 0, byte(qtype>>8), byte(qtype), 0, 1)
}

// dnsReply is a reply's code, answer count and its first answer's data.
func dnsReply(t *testing.T, b []byte, q []byte) (rcode, answers int, data []byte) {
	t.Helper()
	if len(b) < 12 || b[0] != 0xab || b[1] != 0xcd || b[2]&0x80 == 0 {
		t.Fatalf("not a reply: % x", b)
	}
	rcode, answers = int(b[3]&0x0f), int(binary.BigEndian.Uint16(b[6:8]))
	i := len(q)
	if answers > 0 {
		for b[i] != 0 {
			i += int(b[i]) + 1
		}
		i += 11
		n := int(binary.BigEndian.Uint16(b[i-2 : i]))
		data = b[i : i+n]
	}
	return rcode, answers, data
}

// askUDP asks addr over UDP, and gives nil when nothing answers in time.
func askUDP(t *testing.T, addr string, q []byte) []byte {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write(q)
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

func askTCP(t *testing.T, addr string, q []byte) []byte {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(q))))
	c.Write(q)
	var size [2]byte
	if _, err := io.ReadFull(c, size[:]); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(c, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The dashboard sets the zone and the machine answers it at once, over UDP
// and TCP, numbering it by when it changed; setting it again unchanged
// keeps its number. It lasts across a restart, and turning it off stops the
// answers.
func TestTheMachineAnswersTheZoneTheDashboardSets(t *testing.T) {
	e := newDNSEnv(t)
	if st := e.zoneStatus(); st.Zone.Name != "" || len(st.Listening) != 0 {
		t.Fatalf("a machine with no zone: %+v", st)
	}
	code, st := e.setZone(betaZone)
	if code != 200 || len(st.Listening) != 1 || st.Problem != "" || st.Zone.Serial < uint32(time.Now().Add(-time.Hour).Unix()) {
		t.Fatalf("setting the zone: %d %+v", code, st)
	}
	addr := st.Listening[0]
	q := dnsQuery("alex.beta.example.com", 1)
	if rcode, n, data := dnsReply(t, askUDP(t, addr, q), q); rcode != 0 || n != 1 || net.IP(data).String() != "203.0.113.5" {
		t.Fatalf("alex's A record over UDP: %d, %d answers, % x", rcode, n, data)
	}
	q = dnsQuery("_minecraft._tcp.alex.beta.example.com", 33)
	if rcode, n, data := dnsReply(t, askTCP(t, addr, q), q); rcode != 0 || n != 1 || binary.BigEndian.Uint16(data[4:6]) != 25567 {
		t.Fatalf("alex's SRV record over TCP: %d, %d answers, % x", rcode, n, data)
	}
	q = dnsQuery("example.org", 1)
	if rcode, _, _ := dnsReply(t, askUDP(t, addr, q), q); rcode != 5 {
		t.Fatalf("a name outside the zone: %d", rcode)
	}
	if _, again := e.setZone(betaZone); again.Zone.Serial != st.Zone.Serial {
		t.Fatalf("the same zone again was numbered %d, then %d", st.Zone.Serial, again.Zone.Serial)
	}
	if !e.auditHas("dns_zone.set", "succeeded", "beta.example.com, 3 records") {
		t.Fatal("the audit doesn't say the zone was set")
	}

	e.stop()
	e.start()
	st = e.zoneStatus()
	if st.Zone.Name != "beta.example.com" || len(st.Listening) != 1 {
		t.Fatalf("the zone after a restart: %+v", st)
	}
	q = dnsQuery("m1.beta.example.com", 1)
	if rcode, n, _ := dnsReply(t, askUDP(t, st.Listening[0], q), q); rcode != 0 || n != 1 {
		t.Fatalf("m1's A record after a restart: %d, %d answers", rcode, n)
	}
	old := st.Listening[0]
	if code, st := e.setZone(map[string]any{"name": ""}); code != 200 || st.Zone.Name != "" || len(st.Listening) != 0 {
		t.Fatalf("turning the answers off: %d %+v", code, st)
	}
	if got := askUDP(t, old, dnsQuery("alex.beta.example.com", 1)); got != nil {
		t.Fatalf("an answer once turned off: % x", got)
	}
}

// A zone that isn't one is refused, and the machine answers what it did.
func TestABadZoneIsRefused(t *testing.T) {
	e := newDNSEnv(t)
	e.setZone(betaZone)
	for _, bad := range []map[string]any{
		{"name": "com", "nameserver": "ns.example.com"},
		{"name": "beta.example.com", "nameserver": ""},
		{"name": "beta.example.com", "nameserver": "ns-beta.example.com", "records": []map[string]any{{"name": "alex", "type": "A", "value": "not an address"}}},
		{"name": "beta.example.com", "nameserver": "ns-beta.example.com", "records": []map[string]any{{"name": "alex", "type": "CNAME", "value": "evil.example.org"}}},
	} {
		if code, _ := e.setZone(bad); code != 400 {
			t.Fatalf("the zone %v: %d", bad, code)
		}
	}
	if st := e.zoneStatus(); st.Zone.Name != "beta.example.com" || len(st.Zone.Records) != 3 {
		t.Fatalf("the zone after refusals: %+v", st)
	}
}

// An address another program answers DNS on is left out, and the status
// says so.
func TestAnAddressInUseIsSaid(t *testing.T) {
	taken, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	e := newDNSEnv(t, taken.LocalAddr().String(), "127.0.0.1:0")
	_, st := e.setZone(betaZone)
	if len(st.Listening) != 1 || !strings.Contains(st.Problem, "Another program answers DNS on "+taken.LocalAddr().String()) {
		t.Fatalf("with an address taken: %+v", st)
	}
}

// DNS is answered on one port over UDP and TCP. A port whose TCP side
// another program holds isn't answered on, and its UDP side is let go
// again; a free port is one both sides get.
func TestDNSListensOnOnePortOverUDPAndTCP(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	addr := tcp.Addr().String()
	if _, _, err := listenDNS(addr); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("a port whose TCP side is taken: %v", err)
	}
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("its UDP side afterwards: %v", err)
	}
	udp.Close()
	for range 20 {
		pc, ln, err := listenDNS("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if pc.LocalAddr().String() != ln.Addr().String() {
			t.Fatalf("UDP on %s, TCP on %s", pc.LocalAddr(), ln.Addr())
		}
		pc.Close()
		ln.Close()
	}
}
