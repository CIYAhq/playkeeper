package certs

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// fakeResolver answers from maps; names not in a map do not exist. Like
// the Go resolver, it returns IPv4 addresses in their IPv6 form.
type fakeResolver struct {
	a, aaaa map[string][]string
	srv     map[string][]*net.SRV
	txt     map[string][]string
	fail    map[string]error // by "A name", "AAAA name", "SRV name" or "TXT name"
}

func notFoundErr(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// rooted strips the trailing dot the checks must send, failing the lookup
// without it.
func rooted(name string) (string, error) {
	n, ok := strings.CutSuffix(name, ".")
	if !ok {
		return "", &net.DNSError{Err: "the name has no trailing dot, so search domains would be tried", Name: name}
	}
	return n, nil
}

func (f *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	host, err := rooted(host)
	if err != nil {
		return nil, err
	}
	typ, m := "A", f.a
	if network == "ip6" {
		typ, m = "AAAA", f.aaaa
	}
	if err := f.fail[typ+" "+host]; err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, s := range m[host] {
		a := netip.MustParseAddr(s)
		out = append(out, netip.AddrFrom16(a.As16()))
	}
	if len(out) == 0 {
		return nil, notFoundErr(host)
	}
	return out, nil
}

func (f *fakeResolver) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	name, err := rooted(name)
	if err != nil {
		return "", nil, err
	}
	q := "_" + service + "._" + proto + "." + name
	if err := f.fail["SRV "+q]; err != nil {
		return "", nil, err
	}
	if len(f.srv[q]) == 0 {
		return "", nil, notFoundErr(q)
	}
	return q + ".", f.srv[q], nil
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err := f.fail["TXT "+name]; err != nil {
		return nil, err
	}
	if len(f.txt[name]) == 0 {
		return nil, notFoundErr(name)
	}
	return f.txt[name], nil
}

var (
	here4     = netip.MustParseAddr("203.0.113.10")
	here6     = netip.MustParseAddr("2001:db8::10")
	servfail  = &net.DNSError{Err: "server misbehaving", Name: "mc.example.com", IsTemporary: true}
	bothHere  = []netip.Addr{here4, here6}
	onlyIPv6  = []netip.Addr{here6}
	withLocal = []netip.Addr{here4, netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("fe80::1")}
)

func TestCheckName(t *testing.T) {
	cases := []struct {
		name     string
		a, aaaa  []string
		fail     string
		expected []netip.Addr
		code     string
		kind     string
		params   map[string]string
		message  string
	}{
		{name: "points here", a: []string{"203.0.113.10"}, expected: bothHere, code: CodeNameOK,
			message: "mc.example.com points at this server."},
		{name: "A and AAAA here", a: []string{"203.0.113.10"}, aaaa: []string{"2001:db8::10"}, expected: bothHere, code: CodeNameOK},
		{name: "missing", expected: bothHere, code: CodeNameMissing, params: map[string]string{"ipv4": "203.0.113.10", "ipv6": "2001:db8::10"}},
		{name: "elsewhere", a: []string{"198.51.100.7"}, expected: bothHere, code: CodeNameElsewhere,
			params:  map[string]string{"found": "198.51.100.7"},
			message: "mc.example.com points at 198.51.100.7, which is not this server."},
		{name: "one A elsewhere", a: []string{"203.0.113.10", "198.51.100.7"}, expected: bothHere, code: CodeNameMixed,
			params: map[string]string{"elsewhere": "198.51.100.7"}},
		{name: "stale AAAA", a: []string{"203.0.113.10"}, aaaa: []string{"2001:db8::99"}, expected: bothHere, code: CodeNameMixed, kind: "stale_aaaa",
			params:  map[string]string{"elsewhere": "2001:db8::99", "ipv6": "2001:db8::10"},
			message: "mc.example.com has an IPv6 (AAAA) record, 2001:db8::99, that is not this server."},
		{name: "only AAAA, elsewhere", aaaa: []string{"2001:db8::99"}, expected: bothHere, code: CodeNameElsewhere,
			params: map[string]string{"found": "2001:db8::99"}},
		{name: "Cloudflare proxy", a: []string{"104.20.23.154", "172.66.147.243"}, expected: bothHere, code: CodeNameProxied},
		{name: "Cloudflare proxy IPv6", aaaa: []string{"2606:4700:10::6816:1797"}, expected: bothHere, code: CodeNameProxied},
		{name: "private", a: []string{"192.168.1.20"}, expected: bothHere, code: CodeNamePrivate,
			params: map[string]string{"found": "192.168.1.20", "ipv4": "203.0.113.10"}},
		{name: "carrier-grade NAT", a: []string{"100.64.3.4"}, expected: bothHere, code: CodeNamePrivate},
		{name: "public IPv4 unknown behind NAT", a: []string{"198.51.100.7"}, expected: onlyIPv6, code: CodeNameUnverified,
			params: map[string]string{"found": "198.51.100.7"}},
		{name: "NAT with a stale AAAA", a: []string{"198.51.100.7"}, aaaa: []string{"2001:db8::99"}, expected: onlyIPv6, code: CodeNameMixed, kind: "stale_aaaa"},
		{name: "NAT and AAAA here", a: []string{"198.51.100.7"}, aaaa: []string{"2001:db8::10"}, expected: onlyIPv6, code: CodeNameUnverified},
		{name: "local addresses are not expected", a: []string{"10.0.0.5"}, expected: withLocal, code: CodeNamePrivate},
		{name: "lookup fails", a: []string{"203.0.113.10"}, fail: "AAAA", expected: bothHere, code: CodeNameLookupFailed,
			params: map[string]string{"type": "AAAA"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeResolver{a: map[string][]string{"mc.example.com": c.a}, aaaa: map[string][]string{"mc.example.com": c.aaaa}}
			if c.fail != "" {
				r.fail = map[string]error{c.fail + " mc.example.com": servfail}
			}
			got := CheckName(context.Background(), r, "MC.Example.com.", c.expected)
			if got.Code != c.code || got.Params["kind"] != c.kind {
				t.Fatalf("CheckName = %s/%s (%s), want %s/%s", got.Code, got.Params["kind"], got.Message, c.code, c.kind)
			}
			if got.Name != "mc.example.com" || got.OK != (c.code == CodeNameOK) {
				t.Errorf("Name %q, OK %v", got.Name, got.OK)
			}
			for k, v := range c.params {
				if got.Params[k] != v {
					t.Errorf("param %s = %q, want %q", k, got.Params[k], v)
				}
			}
			if c.message != "" && got.Message != c.message {
				t.Errorf("message %q, want %q", got.Message, c.message)
			}
			if strings.ContainsAny(got.Message+got.Hint, "{}") {
				t.Errorf("unfilled text: %q / %q", got.Message, got.Hint)
			}
			if c.fail == "" && len(got.Records) != len(c.a)+len(c.aaaa) {
				t.Errorf("records %+v", got.Records)
			}
		})
	}
}

func TestCheckNameRecords(t *testing.T) {
	r := &fakeResolver{
		a:    map[string][]string{"mc.example.com": {"203.0.113.10", "192.168.1.20", "104.16.1.1"}},
		aaaa: map[string][]string{"mc.example.com": {"2001:db8::10"}},
	}
	got := CheckName(context.Background(), r, "mc.example.com", bothHere)
	want := []AddrRecord{
		{Type: "A", Addr: here4, Here: true},
		{Type: "A", Addr: netip.MustParseAddr("192.168.1.20"), Kind: "private"},
		{Type: "A", Addr: netip.MustParseAddr("104.16.1.1"), Kind: "cloudflare"},
		{Type: "AAAA", Addr: here6, Here: true},
	}
	if !slices.Equal(got.Records, want) {
		t.Errorf("records\n got %+v\nwant %+v", got.Records, want)
	}
	if got.Records[0].Addr.Is4In6() {
		t.Error("IPv4 addresses are not unmapped")
	}

	bad := CheckName(context.Background(), r, "https://mc.example.com/", bothHere)
	if bad.Code != CodeInvalidName || bad.Params["kind"] != "not_a_name" || bad.OK || bad.Name != "https://mc.example.com/" {
		t.Errorf("invalid name check = %+v", bad)
	}
}

func TestExpectedAddrs(t *testing.T) {
	got := ExpectedAddrs(here4, netip.MustParseAddr("::ffff:203.0.113.10"), netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("fe80::1%eth0"), here6)
	if !slices.Contains(got, here4) || !slices.Contains(got, here6) {
		t.Fatalf("ExpectedAddrs = %v, want the extra public addresses", got)
	}
	seen := map[netip.Addr]bool{}
	for _, a := range got {
		if addrKind(a) == kindPrivate || a.Is4In6() || a.Zone() != "" || seen[a] {
			t.Errorf("ExpectedAddrs includes %v", a)
		}
		seen[a] = true
	}
}

func TestAddrKind(t *testing.T) {
	for s, want := range map[string]string{
		"203.0.113.10": "", "8.8.8.8": "", "2001:db8::10": "", "2a01:4f8::1": "",
		"10.0.0.1": kindPrivate, "172.16.5.4": kindPrivate, "192.168.0.1": kindPrivate, "100.64.0.1": kindPrivate,
		"127.0.0.1": kindPrivate, "169.254.1.1": kindPrivate, "0.1.2.3": kindPrivate, "0.0.0.0": kindPrivate,
		"224.0.0.1": kindPrivate, "::1": kindPrivate, "fe80::1": kindPrivate, "fd00::1": kindPrivate, "::": kindPrivate,
		"104.16.0.1": kindCloudflare, "172.67.1.1": kindCloudflare, "188.114.97.1": kindCloudflare, "2606:4700::1": kindCloudflare,
	} {
		if got := addrKind(netip.MustParseAddr(s)); got != want {
			t.Errorf("addrKind(%s) = %q, want %q", s, got, want)
		}
	}
}
