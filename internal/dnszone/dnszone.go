// Package dnszone answers DNS queries for one zone Playkeeper is the
// authority for, such as beta.playkeeper.me for port-free addresses: each
// server's SRV record takes Java players to its machine and port.
//
// It is an authoritative server only. It answers names in its zone from the
// records it was given, refuses every other name, never recurses, and
// answers ANY with "not implemented", so it can't be used to reach other
// names or to amplify traffic. Queries come from anyone on the internet, so
// they are parsed with bounds on every length, and a reply too large for a
// UDP packet is cut short with its truncation bit set, for the asker to try
// again over TCP.
package dnszone

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

// Record types answered.
const (
	TypeA    = "A"
	TypeAAAA = "AAAA"
	TypeSRV  = "SRV"
	TypeTXT  = "TXT"
)

const (
	typeA    uint16 = 1
	typeNS   uint16 = 2
	typeSOA  uint16 = 6
	typeTXT  uint16 = 16
	typeAAAA uint16 = 28
	typeSRV  uint16 = 33
	typeANY  uint16 = 255
	classIN  uint16 = 1

	rcodeNoError  = 0
	rcodeFormErr  = 1
	rcodeNXDomain = 3
	rcodeNotImp   = 4
	rcodeRefused  = 5

	headerLen = 12
	// MaxUDP is the largest reply sent over UDP; a larger one is cut short
	// and marked truncated.
	MaxUDP = 512
	// MaxRecords bounds a zone.
	MaxRecords = 10000
	// DefaultTTL is a record's time to live when it names none: short, so a
	// server moved to another machine is found there soon.
	DefaultTTL = 60
	zoneTTL    = 3600
)

// Record is one record of the zone. Name is relative to the zone: "" for
// the zone itself, "alex" for alex.<zone>, "_minecraft._tcp.alex" for its
// SRV record, and "*" for a wildcard, which answers for the names below
// the zone it hasn't got (RFC 4592). Value is an A or AAAA record's
// address, a TXT record's text, or an SRV record's target, a full host
// name; Port is an SRV record's.
type Record struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Port  int    `json:"port,omitempty"`
	TTL   int    `json:"ttl,omitempty"`
}

// Zone is what a machine answers for: Name, such as beta.playkeeper.me,
// its nameserver as its parent's delegation names it, and its records.
// Serial numbers its versions, as its SOA record says.
type Zone struct {
	Name       string   `json:"name"`
	Nameserver string   `json:"nameserver"`
	Serial     uint32   `json:"serial"`
	Records    []Record `json:"records"`
}

// Check refuses a zone that isn't one: its names, addresses, targets,
// ports, texts and times to live are each checked, and it holds at most
// MaxRecords.
func (z Zone) Check() error {
	if err := checkHost(z.Name); err != nil {
		return fmt.Errorf("the zone %q: %v", z.Name, err)
	}
	if !strings.Contains(z.Name, ".") {
		return fmt.Errorf("the zone %q is a top-level domain", z.Name)
	}
	if err := checkHost(z.Nameserver); err != nil {
		return fmt.Errorf("the nameserver %q: %v", z.Nameserver, err)
	}
	if len(z.Records) > MaxRecords {
		return fmt.Errorf("a zone has at most %d records", MaxRecords)
	}
	for _, r := range z.Records {
		if err := r.check(z.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r Record) check(zone string) error {
	// A wildcard's star is its whole first label.
	name := r.Name
	if name == "*" {
		name = ""
	} else if rest, ok := strings.CutPrefix(name, "*."); ok && rest != "" {
		name = rest
	}
	if name != "" {
		if err := checkLabels(name, true); err != nil {
			return fmt.Errorf("the record %q: %v", r.Name, err)
		}
	}
	if full := len(r.Name) + 1 + len(zone); full > 253 {
		return fmt.Errorf("the record %q makes a name longer than 253 bytes", r.Name)
	}
	if r.TTL != 0 && (r.TTL < 30 || r.TTL > 86400) {
		return fmt.Errorf("the record %q lives from 30 seconds to a day", r.Name)
	}
	switch r.Type {
	case TypeA, TypeAAAA:
		ip, err := netip.ParseAddr(r.Value)
		if err != nil || ip.Is4() != (r.Type == TypeA) || ip.Zone() != "" {
			return fmt.Errorf("the %s record %q has the address %q", r.Type, r.Name, r.Value)
		}
	case TypeSRV:
		if err := checkHost(r.Value); err != nil {
			return fmt.Errorf("the SRV record %q's target %q: %v", r.Name, r.Value, err)
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("the SRV record %q has the port %d", r.Name, r.Port)
		}
	case TypeTXT:
		if len(r.Value) > 255 || strings.ContainsFunc(r.Value, func(c rune) bool { return c < 0x20 || c > 0x7e }) {
			return fmt.Errorf("the TXT record %q is at most 255 printable characters", r.Name)
		}
	default:
		return fmt.Errorf("the record %q has the type %q", r.Name, r.Type)
	}
	return nil
}

// checkHost checks a full host name: lower-case letters, digits and dashes
// in labels of at most 63 bytes, at most 253 bytes in all.
func checkHost(name string) error {
	if name == "" || len(name) > 253 {
		return errors.New("a host name is 1 to 253 bytes")
	}
	return checkLabels(name, false)
}

// checkLabels checks a dotted name's labels. underscores allows the
// leading underscore of names like _minecraft._tcp and _acme-challenge.
func checkLabels(name string, underscores bool) error {
	for _, l := range strings.Split(name, ".") {
		if l == "" || len(l) > 63 {
			return errors.New("each part is 1 to 63 bytes")
		}
		for i, c := range l {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(l)-1:
			case c == '_' && underscores && i == 0:
			default:
				return fmt.Errorf("%q isn't lower-case letters, digits and dashes", l)
			}
		}
	}
	return nil
}

// Answerer answers queries for the zone it holds, which can be replaced as
// it answers.
type Answerer struct {
	mu   sync.RWMutex
	zone Zone
	// extra are the records answered besides the zone's (SetExtra).
	extra []Record
	// names are the records by full lower-case name, and nodes every name
	// that has records or names below it with records.
	names map[string][]Record
	nodes map[string]bool
}

// Set replaces the zone answered for; the zero Zone answers nothing.
func (a *Answerer) Set(z Zone) {
	z.Name = strings.ToLower(z.Name)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.zone = z
	a.index()
}

// SetExtra replaces the records answered besides the zone's, such as the
// TXT records a certificate's DNS-01 check needs meanwhile. They're named
// as the zone's are, answered while there's a zone, and aren't part of
// Zone.
func (a *Answerer) SetExtra(records []Record) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.extra = slices.Clone(records)
	a.index()
}

// index builds names and nodes from the zone's records and the extra ones.
// a.mu is held.
func (a *Answerer) index() {
	names, nodes := map[string][]Record{}, map[string]bool{}
	if z := a.zone.Name; z != "" {
		nodes[z] = true
		for _, r := range slices.Concat(a.zone.Records, a.extra) {
			full := z
			if r.Name != "" {
				full = r.Name + "." + z
			}
			names[full] = append(names[full], r)
			for n := full; n != z; n = n[strings.IndexByte(n, '.')+1:] {
				nodes[n] = true
			}
		}
	}
	a.names, a.nodes = names, nodes
}

// Zone is the zone answered for.
func (a *Answerer) Zone() Zone {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.zone
}

// question is what a query asks.
type question struct {
	raw   []byte // the name as asked, for the reply to echo
	name  string // lower-case, without the final dot
	qtype uint16
	class uint16
	end   int // where the question ends in the query
}

var errMalformed = errors.New("dnszone: malformed query")

// parse reads a query's header and its one question. A query with more
// than one question, or one that isn't a query, is malformed; so is a name
// with a compression pointer, a label over 63 bytes or a name over 255.
func parse(q []byte) (question, error) {
	var qs question
	if len(q) < headerLen {
		return qs, errMalformed
	}
	if q[2]&0x80 != 0 || binary.BigEndian.Uint16(q[4:6]) != 1 {
		return qs, errMalformed
	}
	i := headerLen
	var labels []string
	for {
		if i >= len(q) {
			return qs, errMalformed
		}
		n := int(q[i])
		if n == 0 {
			i++
			break
		}
		if n > 63 || i+1+n > len(q) || i+1+n-headerLen > 255 {
			return qs, errMalformed
		}
		labels = append(labels, strings.ToLower(string(q[i+1:i+1+n])))
		i += 1 + n
	}
	if i+4 > len(q) {
		return qs, errMalformed
	}
	qs.raw = q[headerLen:i]
	qs.name = strings.Join(labels, ".")
	qs.qtype = binary.BigEndian.Uint16(q[i : i+2])
	qs.class = binary.BigEndian.Uint16(q[i+2 : i+4])
	qs.end = i + 4
	return qs, nil
}

// Answer is the reply to the query q, or nil for a packet not worth one: too
// short to hold a header, or a reply itself. max bounds the reply, as
// MaxUDP does for UDP; a longer one keeps its question, loses its records
// and is marked truncated.
func (a *Answerer) Answer(q []byte, max int) []byte {
	if len(q) < headerLen || q[2]&0x80 != 0 {
		return nil
	}
	qs, err := parse(q)
	if err != nil {
		return header(q, rcodeFormErr, false, false, 0, 0, 0)
	}
	if opcode := q[2] >> 3 & 0x0f; opcode != 0 {
		return reply(q, qs, rcodeNotImp, false, nil, nil)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	zone := a.zone.Name
	inside := zone != "" && (qs.name == zone || strings.HasSuffix(qs.name, "."+zone))
	switch {
	case !inside || qs.class != classIN:
		return reply(q, qs, rcodeRefused, false, nil, nil)
	case qs.qtype == typeANY:
		return reply(q, qs, rcodeNotImp, true, nil, nil)
	}
	var answers []rr
	if qs.name == zone {
		switch qs.qtype {
		case typeSOA:
			answers = append(answers, a.soa())
		case typeNS:
			answers = append(answers, rr{name: qs.raw, rtype: typeNS, ttl: zoneTTL, data: encodeName(a.zone.Nameserver)})
		}
	}
	recs, there := a.names[qs.name], a.nodes[qs.name]
	if !there {
		// A name the zone hasn't got has the records of the wildcard just
		// below its closest encloser, the nearest name above it the zone
		// has, if there's one there.
		ce := qs.name
		for !a.nodes[ce] {
			ce = ce[strings.IndexByte(ce, '.')+1:]
		}
		recs, there = a.names["*."+ce]
	}
	for _, r := range recs {
		if t, data := r.wire(); t == qs.qtype {
			answers = append(answers, rr{name: qs.raw, rtype: t, ttl: uint32(ttlOf(r)), data: data})
		}
	}
	rcode := rcodeNoError
	var authority []rr
	if len(answers) == 0 {
		authority = []rr{a.soa()}
		if !there {
			rcode = rcodeNXDomain
		}
	}
	out := reply(q, qs, rcode, true, answers, authority)
	if len(out) > max {
		out = reply(q, qs, rcode, true, nil, nil)
		out[2] |= 0x02
	}
	return out
}

func ttlOf(r Record) int {
	if r.TTL == 0 {
		return DefaultTTL
	}
	return r.TTL
}

// soa is the zone's SOA record, whose last field is how long a name that
// isn't there may be remembered as missing.
func (a *Answerer) soa() rr {
	data := encodeName(a.zone.Nameserver)
	data = append(data, encodeName("hostmaster."+a.zone.Name)...)
	for _, v := range []uint32{a.zone.Serial, 3600, 600, 604800, DefaultTTL} {
		data = binary.BigEndian.AppendUint32(data, v)
	}
	return rr{name: encodeName(a.zone.Name), rtype: typeSOA, ttl: zoneTTL, data: data}
}

// wire is a record's type and data as a reply carries them.
func (r Record) wire() (uint16, []byte) {
	switch r.Type {
	case TypeA:
		ip := netip.MustParseAddr(r.Value).As4()
		return typeA, ip[:]
	case TypeAAAA:
		ip := netip.MustParseAddr(r.Value).As16()
		return typeAAAA, ip[:]
	case TypeSRV:
		data := make([]byte, 6, 6+len(r.Value)+2)
		binary.BigEndian.PutUint16(data[4:6], uint16(r.Port))
		return typeSRV, append(data, encodeName(r.Value)...)
	case TypeTXT:
		return typeTXT, append([]byte{byte(len(r.Value))}, r.Value...)
	}
	return 0, nil
}

// encodeName is a checked host name in wire form, with its root label.
func encodeName(name string) []byte {
	var out []byte
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(l)))
		out = append(out, l...)
	}
	return append(out, 0)
}

// rr is a resource record of a reply.
type rr struct {
	name  []byte
	rtype uint16
	ttl   uint32
	data  []byte
}

// header is a reply's header to query q, with no question echoed.
func header(q []byte, rcode int, aa, question bool, an, ns, ar int) []byte {
	out := make([]byte, headerLen)
	copy(out[0:2], q[0:2])
	out[2] = 0x80 | q[2]&0x79 // QR, the query's opcode and RD
	if aa {
		out[2] |= 0x04
	}
	out[3] = byte(rcode)
	if question {
		binary.BigEndian.PutUint16(out[4:6], 1)
	}
	binary.BigEndian.PutUint16(out[6:8], uint16(an))
	binary.BigEndian.PutUint16(out[8:10], uint16(ns))
	binary.BigEndian.PutUint16(out[10:12], uint16(ar))
	return out
}

// reply is the reply to query q asking qs, with its answers and authority.
func reply(q []byte, qs question, rcode int, aa bool, answers, authority []rr) []byte {
	out := header(q, rcode, aa, true, len(answers), len(authority), 0)
	out = append(out, q[headerLen:qs.end]...)
	for _, set := range [][]rr{answers, authority} {
		for _, r := range set {
			out = append(out, r.name...)
			out = binary.BigEndian.AppendUint16(out, r.rtype)
			out = binary.BigEndian.AppendUint16(out, classIN)
			out = binary.BigEndian.AppendUint32(out, r.ttl)
			out = binary.BigEndian.AppendUint16(out, uint16(len(r.data)))
			out = append(out, r.data...)
		}
	}
	return out
}
