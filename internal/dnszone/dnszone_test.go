package dnszone

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net/netip"
	"strings"
	"testing"
)

const (
	testID   = 0x1234
	typeCH   = 3
	opNotify = 4
)

var testZone = Zone{Name: "beta.example.com", Nameserver: "ns-beta.example.com", Serial: 2026092901, Records: []Record{
	{Name: "", Type: TypeA, Value: "203.0.113.5"},
	{Name: "m1", Type: TypeA, Value: "203.0.113.5"},
	{Name: "m1", Type: TypeAAAA, Value: "2001:db8::5"},
	{Name: "alex", Type: TypeA, Value: "203.0.113.5", TTL: 300},
	{Name: "_minecraft._tcp.alex", Type: TypeSRV, Value: "m1.beta.example.com", Port: 25567},
	{Name: "_acme-challenge", Type: TypeTXT, Value: "token-1"},
}}

// query is a query for name and qtype, with flags as the third and fourth
// header bytes.
func query(name string, qtype uint16, flags ...byte) []byte {
	q := make([]byte, headerLen)
	binary.BigEndian.PutUint16(q[0:2], testID)
	q[2] = 0x01 // RD
	if len(flags) > 0 {
		q[2] = flags[0]
	}
	binary.BigEndian.PutUint16(q[4:6], 1)
	for _, l := range strings.Split(name, ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	q = append(q, 0)
	q = binary.BigEndian.AppendUint16(q, qtype)
	return binary.BigEndian.AppendUint16(q, classIN)
}

// answer is a parsed reply.
type answer struct {
	rcode          int
	aa, tc         bool
	qd, an, ns, ar int
	question       string // as echoed
	records        []parsedRR
}

type parsedRR struct {
	name  string
	rtype uint16
	ttl   uint32
	data  []byte
}

// readName reads a wire name at i, with no compression, as a dotted
// string.
func readName(t *testing.T, b []byte, i int) (string, int) {
	t.Helper()
	var labels []string
	for {
		n := int(b[i])
		if n == 0 {
			return strings.Join(labels, "."), i + 1
		}
		if n&0xc0 != 0 {
			t.Fatalf("a compressed name at %d", i)
		}
		labels = append(labels, string(b[i+1:i+1+n]))
		i += 1 + n
	}
}

func parseReply(t *testing.T, b []byte) answer {
	t.Helper()
	if len(b) < headerLen {
		t.Fatalf("a reply of %d bytes", len(b))
	}
	if binary.BigEndian.Uint16(b[0:2]) != testID || b[2]&0x80 == 0 {
		t.Fatalf("not a reply to the query: % x", b[:4])
	}
	a := answer{rcode: int(b[3] & 0x0f), aa: b[2]&0x04 != 0, tc: b[2]&0x02 != 0,
		qd: int(binary.BigEndian.Uint16(b[4:6])), an: int(binary.BigEndian.Uint16(b[6:8])), ns: int(binary.BigEndian.Uint16(b[8:10])), ar: int(binary.BigEndian.Uint16(b[10:12]))}
	i := headerLen
	if a.qd == 1 {
		a.question, i = readName(t, b, i)
		i += 4
	}
	for range a.an + a.ns {
		var r parsedRR
		r.name, i = readName(t, b, i)
		r.rtype = binary.BigEndian.Uint16(b[i:])
		r.ttl = binary.BigEndian.Uint32(b[i+4:])
		n := int(binary.BigEndian.Uint16(b[i+8:]))
		r.data = b[i+10 : i+10+n]
		i += 10 + n
		a.records = append(a.records, r)
	}
	if i != len(b) {
		t.Fatalf("a reply of %d bytes read to %d", len(b), i)
	}
	return a
}

func newAnswerer(z Zone) *Answerer {
	a := &Answerer{}
	a.Set(z)
	return a
}

func ask(t *testing.T, a *Answerer, q []byte) answer {
	t.Helper()
	return parseReply(t, a.Answer(q, MaxUDP))
}

// The zone's records are answered with authority, names in any case, the
// question echoed as asked: A, AAAA, SRV and TXT records, and the zone's
// own SOA and NS.
func TestAnswersTheZonesRecords(t *testing.T) {
	a := newAnswerer(testZone)
	got := ask(t, a, query("ALEX.Beta.example.com", typeA))
	if got.rcode != rcodeNoError || !got.aa || got.an != 1 || got.question != "ALEX.Beta.example.com" || got.records[0].ttl != 300 || netip.AddrFrom4([4]byte(got.records[0].data)).String() != "203.0.113.5" {
		t.Fatalf("alex's A record: %+v", got)
	}
	got = ask(t, a, query("_minecraft._tcp.alex.beta.example.com", typeSRV))
	if got.an != 1 || got.records[0].ttl != DefaultTTL {
		t.Fatalf("alex's SRV record: %+v", got)
	}
	d := got.records[0].data
	if prio, weight, port := binary.BigEndian.Uint16(d[0:2]), binary.BigEndian.Uint16(d[2:4]), binary.BigEndian.Uint16(d[4:6]); prio != 0 || weight != 0 || port != 25567 {
		t.Fatalf("alex's SRV record: priority %d, weight %d, port %d", prio, weight, port)
	}
	if target, _ := readName(t, d, 6); target != "m1.beta.example.com" {
		t.Fatalf("alex's SRV target: %q", target)
	}
	got = ask(t, a, query("_acme-challenge.beta.example.com", typeTXT))
	if got.an != 1 || string(got.records[0].data) != "\x07token-1" {
		t.Fatalf("the TXT record: %+v", got)
	}
	got = ask(t, a, query("m1.beta.example.com", typeAAAA))
	if got.an != 1 || netip.AddrFrom16([16]byte(got.records[0].data)).String() != "2001:db8::5" {
		t.Fatalf("m1's AAAA record: %+v", got)
	}
	got = ask(t, a, query("beta.example.com", typeSOA))
	if got.an != 1 || got.records[0].rtype != typeSOA {
		t.Fatalf("the SOA record: %+v", got)
	}
	soa := got.records[0].data
	mname, i := readName(t, soa, 0)
	_, i = readName(t, soa, i)
	if mname != "ns-beta.example.com" || binary.BigEndian.Uint32(soa[i:]) != testZone.Serial {
		t.Fatalf("the SOA record names %q, serial %d", mname, binary.BigEndian.Uint32(soa[i:]))
	}
	got = ask(t, a, query("beta.example.com", typeNS))
	if ns, _ := readName(t, got.records[0].data, 0); got.an != 1 || ns != "ns-beta.example.com" {
		t.Fatalf("the NS record: %+v", got)
	}
}

// A name the zone hasn't got is missing, with the SOA record saying how long
// that may be remembered; a name it has without the type asked, and a name
// with names below it, is there with nothing of that type.
func TestMissingNamesAndTypes(t *testing.T) {
	a := newAnswerer(testZone)
	for _, c := range []struct {
		name  string
		qtype uint16
		rcode int
	}{
		{"bob.beta.example.com", typeA, rcodeNXDomain},
		{"alex.beta.example.com", typeAAAA, rcodeNoError},
		{"_tcp.alex.beta.example.com", typeSRV, rcodeNoError},
		{"deep.alex.beta.example.com", typeA, rcodeNXDomain},
	} {
		got := ask(t, a, query(c.name, c.qtype))
		if got.rcode != c.rcode || !got.aa || got.an != 0 || got.ns != 1 || got.records[0].rtype != typeSOA {
			t.Fatalf("%s %d: %+v", c.name, c.qtype, got)
		}
	}
}

// wildZone is testZone with a wildcard.
func wildZone() Zone {
	z := testZone
	z.Records = append(append([]Record{}, testZone.Records...), Record{Name: "*", Type: TypeA, Value: "203.0.113.9"})
	return z
}

// A wildcard answers the names the zone hasn't got, however deep, with the
// name asked as the owner: a server the zone doesn't list yet, or the name
// Playkeeper's own check makes up. A name the zone has keeps its records, a
// name with names below it has nothing of a type it hasn't got, and a name
// below one the zone has isn't the wildcard's.
func TestAWildcardAnswersNamesTheZoneHasnt(t *testing.T) {
	a := newAnswerer(wildZone())
	for _, name := range []string{"playkeeper-0a1b2c.beta.example.com", "New.Server.beta.example.com"} {
		got := ask(t, a, query(name, typeA))
		if got.rcode != rcodeNoError || !got.aa || got.an != 1 || got.records[0].name != name || netip.AddrFrom4([4]byte(got.records[0].data)).String() != "203.0.113.9" {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	got := ask(t, a, query("alex.beta.example.com", typeA))
	if got.an != 1 || netip.AddrFrom4([4]byte(got.records[0].data)).String() != "203.0.113.5" {
		t.Fatalf("alex's own A record: %+v", got)
	}
	for _, c := range []struct {
		name  string
		qtype uint16
		rcode int
	}{
		{"bob.beta.example.com", typeSRV, rcodeNoError},
		{"_tcp.alex.beta.example.com", typeA, rcodeNoError},
		{"deep.alex.beta.example.com", typeA, rcodeNXDomain},
	} {
		got := ask(t, a, query(c.name, c.qtype))
		if got.rcode != c.rcode || got.an != 0 || got.ns != 1 || got.records[0].rtype != typeSOA {
			t.Fatalf("%s %d: %+v", c.name, c.qtype, got)
		}
	}
}

// Names outside the zone, including ones that only end in its letters, and
// classes other than IN are refused without authority, as they are by a
// machine with no zone at all; ANY is not implemented.
func TestRefusesWhatIsntItsZone(t *testing.T) {
	a := newAnswerer(testZone)
	for _, q := range [][]byte{query("example.com", typeA), query("xbeta.example.com", typeA), query("beta.example.com.evil.example", typeA), query("google.com", typeA)} {
		if got := ask(t, a, q); got.rcode != rcodeRefused || got.aa || got.an != 0 || got.ns != 0 {
			t.Fatalf("a query outside the zone: %+v", got)
		}
	}
	ch := query("alex.beta.example.com", typeA)
	binary.BigEndian.PutUint16(ch[len(ch)-2:], typeCH)
	if got := ask(t, a, ch); got.rcode != rcodeRefused {
		t.Fatalf("a query in class CH: %+v", got)
	}
	if got := ask(t, a, query("alex.beta.example.com", typeANY)); got.rcode != rcodeNotImp || got.an != 0 {
		t.Fatalf("ANY: %+v", got)
	}
	if got := ask(t, &Answerer{}, query("alex.beta.example.com", typeA)); got.rcode != rcodeRefused {
		t.Fatalf("a query with no zone: %+v", got)
	}
}

// A packet too short for a header, or a reply, gets no answer; a malformed
// query gets "format error", and one that isn't a query "not implemented".
func TestMalformedQueries(t *testing.T) {
	a := newAnswerer(testZone)
	if got := a.Answer([]byte{1, 2, 3}, MaxUDP); got != nil {
		t.Fatalf("a short packet was answered: % x", got)
	}
	if got := a.Answer(query("alex.beta.example.com", typeA, 0x80), MaxUDP); got != nil {
		t.Fatalf("a reply was answered: % x", got)
	}
	good := query("alex.beta.example.com", typeA)
	two := append([]byte{}, good...)
	binary.BigEndian.PutUint16(two[4:6], 2)
	pointer := append(append([]byte{}, good[:headerLen]...), 0xc0, 0x0c, 0, 1, 0, 1)
	long := append([]byte{}, good[:headerLen]...)
	for range 5 {
		long = append(long, 63)
		long = append(long, strings.Repeat("a", 63)...)
	}
	long = append(long, 0, 0, 1, 0, 1)
	wide := append(append(append([]byte{}, good[:headerLen]...), 64), strings.Repeat("a", 64)...)
	wide = append(wide, 0, 0, 1, 0, 1)
	for name, q := range map[string][]byte{
		"two questions":       two,
		"a cut question":      good[:len(good)-3],
		"a cut name":          good[:headerLen+3],
		"a label over 63":     append(append([]byte{}, good[:headerLen]...), 64),
		"a whole label of 64": wide,
		"a compressed name":   pointer,
		"a name over 255":     long,
		"no question at all":  append(append([]byte{}, good[:4]...), 0, 0, 0, 0, 0, 0, 0, 0),
	} {
		if got := ask(t, a, q); got.rcode != rcodeFormErr || got.an != 0 {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if got := ask(t, a, query("alex.beta.example.com", typeA, opNotify<<3)); got.rcode != rcodeNotImp {
		t.Fatalf("a NOTIFY: %+v", got)
	}
}

// A reply too large for UDP keeps its question, loses its records and is
// marked truncated, for the asker to try again over TCP, where it's whole.
func TestALargeReplyIsTruncated(t *testing.T) {
	z := testZone
	for i := range 40 {
		z.Records = append(z.Records, Record{Name: "many", Type: TypeTXT, Value: fmt.Sprintf("%02d-%s", i, strings.Repeat("x", 40))})
	}
	a := newAnswerer(z)
	q := query("many.beta.example.com", typeTXT)
	if got := ask(t, a, q); !got.tc || got.an != 0 || got.qd != 1 {
		t.Fatalf("a large reply over UDP: %+v", got)
	}
	if got := parseReply(t, a.Answer(q, 65535)); got.tc || got.an != 40 {
		t.Fatalf("a large reply over TCP: tc %v, %d answers", got.tc, got.an)
	}
}

// A zone is checked: its names, addresses, targets, ports, texts, types,
// times to live and size.
func TestZoneCheck(t *testing.T) {
	if err := testZone.Check(); err != nil {
		t.Fatalf("a good zone: %v", err)
	}
	wild := wildZone()
	wild.Records = append(wild.Records, Record{Name: "*.sub", Type: TypeTXT, Value: "x"})
	if err := wild.Check(); err != nil {
		t.Fatalf("a zone with wildcards: %v", err)
	}
	for name, bad := range map[string]func(z *Zone){
		"a star in a label":        func(z *Zone) { z.Records = append(z.Records, Record{Name: "*a", Type: TypeA, Value: "203.0.113.5"}) },
		"a star below":             func(z *Zone) { z.Records = append(z.Records, Record{Name: "a.*", Type: TypeA, Value: "203.0.113.5"}) },
		"two stars":                func(z *Zone) { z.Records = append(z.Records, Record{Name: "*.*", Type: TypeA, Value: "203.0.113.5"}) },
		"a star and a dot":         func(z *Zone) { z.Records = append(z.Records, Record{Name: "*.", Type: TypeA, Value: "203.0.113.5"}) },
		"a star in the zone":       func(z *Zone) { z.Name = "*.example.com" },
		"a top-level zone":         func(z *Zone) { z.Name = "com" },
		"an upper-case zone":       func(z *Zone) { z.Name = "Beta.example.com" },
		"no nameserver":            func(z *Zone) { z.Nameserver = "" },
		"a bad record name":        func(z *Zone) { z.Records = append(z.Records, Record{Name: "a..b", Type: TypeA, Value: "203.0.113.5"}) },
		"a dash at a label's edge": func(z *Zone) { z.Records = append(z.Records, Record{Name: "-a", Type: TypeA, Value: "203.0.113.5"}) },
		"an IPv6 address in an A":  func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: TypeA, Value: "2001:db8::1"}) },
		"an IPv4 address in AAAA":  func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: TypeAAAA, Value: "203.0.113.5"}) },
		"not an address":           func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: TypeA, Value: "localhost"}) },
		"an SRV without a port": func(z *Zone) {
			z.Records = append(z.Records, Record{Name: "a", Type: TypeSRV, Value: "m1.beta.example.com"})
		},
		"an SRV to no host":       func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: TypeSRV, Value: "_x", Port: 1}) },
		"a TXT with a line break": func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: TypeTXT, Value: "a\nb"}) },
		"a TXT over 255": func(z *Zone) {
			z.Records = append(z.Records, Record{Name: "a", Type: TypeTXT, Value: strings.Repeat("a", 256)})
		},
		"a CNAME": func(z *Zone) { z.Records = append(z.Records, Record{Name: "a", Type: "CNAME", Value: "b.example.com"}) },
		"a TTL too short": func(z *Zone) {
			z.Records = append(z.Records, Record{Name: "a", Type: TypeA, Value: "203.0.113.5", TTL: 5})
		},
		"too many records": func(z *Zone) {
			for range MaxRecords + 1 {
				z.Records = append(z.Records, Record{Name: "a", Type: TypeA, Value: "203.0.113.5"})
			}
		},
	} {
		z := testZone
		z.Records = append([]Record{}, testZone.Records...)
		bad(&z)
		if err := z.Check(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Whatever arrives, the answerer doesn't panic, and its replies are whole.
func FuzzAnswer(f *testing.F) {
	for _, q := range [][]byte{query("alex.beta.example.com", typeA), query("new.beta.example.com", typeA), query("x", typeSRV), {0, 1, 0, 0, 0, 1}} {
		f.Add(q)
	}
	r := rand.New(rand.NewSource(1))
	for range 64 {
		b := make([]byte, r.Intn(64))
		r.Read(b)
		f.Add(b)
	}
	a := newAnswerer(wildZone())
	f.Fuzz(func(t *testing.T, q []byte) {
		out := a.Answer(q, MaxUDP)
		if out != nil && (len(out) < headerLen || len(out) > MaxUDP && out[2]&0x02 == 0) {
			t.Fatalf("a reply of %d bytes to % x", len(out), q)
		}
	})
}
