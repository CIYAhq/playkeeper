package service

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

var errForwardedFor = errors.New("the X-Forwarded-For header from the proxy is not a list of addresses")

// clientAddr is the address a request came from: the connection's peer, or,
// when the peer is a trusted proxy, the rightmost X-Forwarded-For address
// that is not a trusted proxy itself. Each proxy appends the address it
// saw, so everything left of that could have been written by the client.
func (s *Service) clientAddr(r *http.Request) (netip.Addr, error) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	addr := ap.Addr().Unmap().WithZone("")
	if !s.trusted(addr) {
		if r.Header.Get("X-Forwarded-For") != "" && (addr.IsPrivate() || addr.IsLoopback()) && s.warnedProxy.CompareAndSwap(false, true) {
			s.log.Warn("A request came through a proxy that "+EnvTrustedProxies+" does not list, so the client address it forwarded was ignored. Set "+EnvTrustedProxies+" to the proxy's network (see services/names/README.md).", "proxy", addr.String())
		}
		return addr, nil
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, errForwardedFor
		}
		addr = a.Unmap().WithZone("")
		if !s.trusted(addr) {
			return addr, nil
		}
	}
	return addr, nil
}

func (s *Service) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// notGlobal are special-purpose ranges (IANA registries) that pass
// netip's IsGlobalUnicast but are not a server's public address.
var notGlobal = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var globalIPv6 = netip.MustParsePrefix("2000::/3")

// cloudflareEdge are Cloudflare's proxy ranges (https://www.cloudflare.com/ips/).
// A request from one of them means the names record is proxied ("orange
// cloud") instead of DNS only, so the service cannot see who is asking.
var cloudflareEdge = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
)

func inAny(list []netip.Prefix, a netip.Addr) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// publicUnicast reports whether a name may point at a: a globally routed
// unicast address, not a private, shared, reserved or documentation one.
func publicUnicast(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() || inAny(notGlobal, a) {
		return false
	}
	return a.Is4() || globalIPv6.Contains(a)
}

// addrBucket groups addresses for rate limits: one IPv4 address, or an IPv6
// network of the given size, since one machine usually has a whole /64.
func addrBucket(a netip.Addr, v6bits int) string {
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(v6bits)
	return p.String()
}

func family(a netip.Addr) string {
	if a.Is4() {
		return "ipv4"
	}
	return "ipv6"
}
