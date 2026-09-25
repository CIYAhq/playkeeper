package certs

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Resolver looks up the DNS records the checks need. *net.Resolver
// satisfies it, as do PublicResolver and the resolvers DNSServer returns.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// DNSServer returns a resolver that sends every query to the DNS server at
// addr ("host:port"). Like every Go resolver it answers A and AAAA lookups
// from /etc/hosts first; SRV and TXT lookups always ask the server.
func DNSServer(addr string) *net.Resolver {
	var d net.Dialer
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
	}
}

// ExpectedAddrs is where this machine's names should point: the public
// addresses of its network interfaces plus extra candidates, such as the
// address the admin opened the dashboard with (behind NAT the public IPv4
// address is on no interface). Private, loopback and link-local addresses
// are left out.
func ExpectedAddrs(extra ...netip.Addr) []netip.Addr {
	var all []netip.Addr
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					all = append(all, ip)
				}
			}
		}
	}
	return publicAddrs(append(all, extra...))
}

func publicAddrs(in []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range in {
		a = a.Unmap().WithZone("")
		if !a.IsValid() || addrKind(a) == kindPrivate || slices.Contains(out, a) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// AddrRecord is one A or AAAA record found for a name.
type AddrRecord struct {
	Type string     `json:"type"` // "A" or "AAAA"
	Addr netip.Addr `json:"addr"`
	// Here means the address is one of this machine's.
	Here bool `json:"here"`
	// Kind is "private" or "cloudflare" for addresses of those kinds.
	Kind string `json:"kind,omitempty"`
}

const (
	kindPrivate    = "private"
	kindCloudflare = "cloudflare"
)

// NameCheck is where a name points, compared with this machine.
type NameCheck struct {
	Note
	Name    string       `json:"name"`
	OK      bool         `json:"ok"`
	Records []AddrRecord `json:"records,omitempty"`
}

// CheckName looks up name's A and AAAA records and compares them with
// expected, this machine's addresses (see ExpectedAddrs). Use a
// PublicResolver to see what Let's Encrypt and players see: this machine's
// own resolver may answer from a cache or /etc/hosts.
func CheckName(ctx context.Context, r Resolver, name string, expected []netip.Addr) NameCheck {
	n, err := NormalizeName(name)
	if err != nil {
		var p *Problem
		errors.As(err, &p)
		return NameCheck{Note: p.Note, Name: displayName(name)}
	}
	expected = publicAddrs(expected)
	c := NameCheck{Name: n}
	var failed []string
	for _, q := range []struct{ network, typ string }{{"ip4", "A"}, {"ip6", "AAAA"}} {
		addrs, err := r.LookupNetIP(ctx, q.network, n)
		if err != nil {
			if !isNotFound(err) {
				failed = append(failed, q.typ)
			}
			continue
		}
		for _, a := range addrs {
			a = a.Unmap().WithZone("")
			if a.Is4() != (q.typ == "A") || slices.ContainsFunc(c.Records, func(r AddrRecord) bool { return r.Addr == a }) {
				continue
			}
			c.Records = append(c.Records, AddrRecord{Type: q.typ, Addr: a, Here: slices.Contains(expected, a), Kind: addrKind(a)})
		}
	}
	params := map[string]string{"name": n}
	for _, a := range expected {
		switch {
		case a.Is4() && params["ipv4"] == "":
			params["ipv4"] = a.String()
		case a.Is6() && params["ipv6"] == "":
			params["ipv6"] = a.String()
		}
	}
	code := classifyName(c.Records, expected, params)
	if len(failed) > 0 {
		code = CodeNameLookupFailed
		params = map[string]string{"name": n, "type": strings.Join(failed, ",")}
	}
	c.OK = code == CodeNameOK
	c.Note, _ = note(code, params)
	return c
}

// classifyName picks the name check's code and fills the addresses the
// message mentions into params.
func classifyName(recs []AddrRecord, expected []netip.Addr, params map[string]string) string {
	if len(recs) == 0 {
		return CodeNameMissing
	}
	all := func(rs []AddrRecord) string {
		s := make([]string, len(rs))
		for i, r := range rs {
			s[i] = r.Addr.String()
		}
		return strings.Join(s, ", ")
	}
	if slices.ContainsFunc(recs, func(r AddrRecord) bool { return r.Kind == kindCloudflare }) {
		return CodeNameProxied
	}
	if !slices.ContainsFunc(recs, func(r AddrRecord) bool { return r.Kind != kindPrivate }) {
		params["found"] = all(recs)
		return CodeNamePrivate
	}
	hasV4 := slices.ContainsFunc(expected, netip.Addr.Is4)
	var here, elsewhere, unknown []AddrRecord
	for _, r := range recs {
		switch {
		case r.Here:
			here = append(here, r)
		case r.Type == "A" && !hasV4 && r.Kind != kindPrivate:
			// Behind NAT this machine's public IPv4 address may be unknown.
			// IPv6 is not translated, so an AAAA record that is not an
			// interface address is wrong.
			unknown = append(unknown, r)
		default:
			elsewhere = append(elsewhere, r)
		}
	}
	switch {
	case len(elsewhere) > 0:
		onlyAAAA := !slices.ContainsFunc(elsewhere, func(r AddrRecord) bool { return r.Type == "A" })
		if onlyAAAA && len(here)+len(unknown) > 0 {
			params["kind"] = "stale_aaaa"
			params["elsewhere"] = all(elsewhere)
			return CodeNameMixed
		}
		if len(here) > 0 {
			params["elsewhere"] = all(elsewhere)
			return CodeNameMixed
		}
		params["found"] = all(recs)
		return CodeNameElsewhere
	case len(unknown) > 0:
		params["found"] = all(unknown)
		return CodeNameUnverified
	}
	return CodeNameOK
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
	// cloudflarePrefixes are Cloudflare's proxy ranges, from
	// https://www.cloudflare.com/ips/ (they rarely change).
	cloudflarePrefixes = mustPrefixes(
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
		"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	)
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// addrKind is "private" for addresses that cannot be reached from the
// internet, "cloudflare" for Cloudflare's proxies, and "" otherwise.
func addrKind(a netip.Addr) string {
	switch {
	case !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) || (a.Is4() && a.As4()[0] == 0):
		return kindPrivate
	case slices.ContainsFunc(cloudflarePrefixes, func(p netip.Prefix) bool { return p.Contains(a) }):
		return kindCloudflare
	}
	return ""
}
