package certs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/version"
)

// maxDoHSize bounds a DNS-over-HTTPS answer.
const maxDoHSize = 64 << 10

// DNS record types.
const (
	typeA    = 1
	typeTXT  = 16
	typeAAAA = 28
	typeSRV  = 33
)

func defaultDoHEndpoints() []string {
	return []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"}
}

// PublicResolver asks public DNS-over-HTTPS services (the JSON API of
// Cloudflare and Google) directly. It sees what Let's Encrypt and players
// see, without this machine's resolver, its cache or /etc/hosts.
type PublicResolver struct {
	// Endpoints are the services' URLs, tried in order until one answers;
	// empty means Cloudflare, then Google. Only these hosts are contacted,
	// over HTTPS, and redirects are refused.
	Endpoints []string
	// Client makes the requests; nil means one with a 10-second timeout.
	Client *http.Client
}

type dohMessage struct {
	Status int `json:"Status"`
	Answer []struct {
		Name string `json:"name"`
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// LookupNetIP looks up A ("ip4"), AAAA ("ip6") or both ("ip") records.
func (p PublicResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	var types []int
	switch network {
	case "ip4":
		types = []int{typeA}
	case "ip6":
		types = []int{typeAAAA}
	case "ip":
		types = []int{typeA, typeAAAA}
	default:
		return nil, &net.DNSError{Err: "unknown network " + network, Name: host}
	}
	var out []netip.Addr
	var firstErr error
	for _, t := range types {
		data, err := p.query(ctx, host, t)
		if err != nil {
			if firstErr == nil || !isNotFound(err) {
				firstErr = err
			}
			continue
		}
		for _, d := range data {
			if a, err := netip.ParseAddr(d); err == nil && a.Is4() == (t == typeA) {
				out = append(out, a)
			}
		}
	}
	if len(out) == 0 {
		if firstErr == nil {
			firstErr = notFound(host, "")
		}
		return nil, firstErr
	}
	return out, nil
}

// LookupSRV looks up the SRV records of _service._proto.name, or of name
// when service and proto are empty, like net.Resolver.LookupSRV.
func (p PublicResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	qname := strings.TrimSuffix(name, ".")
	if service != "" || proto != "" {
		qname = "_" + service + "._" + proto + "." + qname
	}
	data, err := p.query(ctx, qname, typeSRV)
	if err != nil {
		return "", nil, err
	}
	var out []*net.SRV
	for _, d := range data {
		f := strings.Fields(d)
		if len(f) != 4 {
			continue
		}
		prio, err1 := strconv.ParseUint(f[0], 10, 16)
		weight, err2 := strconv.ParseUint(f[1], 10, 16)
		port, err3 := strconv.ParseUint(f[2], 10, 16)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, &net.SRV{Target: strings.TrimSuffix(f[3], ".") + ".", Port: uint16(port), Priority: uint16(prio), Weight: uint16(weight)})
	}
	if len(out) == 0 {
		return "", nil, notFound(qname, "")
	}
	slices.SortStableFunc(out, func(a, b *net.SRV) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(b.Weight, a.Weight))
	})
	return qname + ".", out, nil
}

// LookupTXT looks up TXT records, each returned as one string with its
// parts joined, like net.Resolver.LookupTXT.
func (p PublicResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	data, err := p.query(ctx, name, typeTXT)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(data))
	for i, d := range data {
		out[i] = parseTXT(d)
	}
	return out, nil
}

// query returns the data of the answers of type t, trying each endpoint in
// turn. "No such name" and "no such record" answers are final.
func (p PublicResolver) query(ctx context.Context, name string, t int) ([]string, error) {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 || strings.ContainsAny(name, " \t\r\n") {
		return nil, &net.DNSError{Err: "invalid name", Name: name}
	}
	endpoints := p.Endpoints
	if len(endpoints) == 0 {
		endpoints = defaultDoHEndpoints()
	}
	client := p.client()
	var last error
	for _, ep := range endpoints {
		data, err := ask(ctx, client, ep, name, t)
		if err == nil || isNotFound(err) || ctx.Err() != nil {
			return data, err
		}
		last = err
	}
	return nil, last
}

func (p PublicResolver) client() *http.Client {
	c := &http.Client{Timeout: 10 * time.Second}
	if p.Client != nil {
		copied := *p.Client
		c = &copied
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	return c
}

func ask(ctx context.Context, client *http.Client, endpoint, name string, t int) ([]string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, &net.DNSError{Err: "DNS-over-HTTPS endpoint is not an https:// URL", Name: name, Server: endpoint}
	}
	server := u.Host
	q := u.Query()
	q.Set("name", name)
	q.Set("type", strconv.Itoa(t))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &net.DNSError{Err: err.Error(), Name: name, Server: server}
	}
	req.Header.Set("Accept", "application/dns-json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, &net.DNSError{Err: "could not reach " + server, Name: name, Server: server, IsTemporary: true, UnwrapErr: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &net.DNSError{Err: fmt.Sprintf("%s answered HTTP %d", server, resp.StatusCode), Name: name, Server: server, IsTemporary: true}
	}
	if resp.ContentLength > maxDoHSize {
		return nil, &net.DNSError{Err: "answer too large", Name: name, Server: server}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDoHSize+1))
	if err != nil {
		return nil, &net.DNSError{Err: "reading the answer: " + err.Error(), Name: name, Server: server, IsTemporary: true}
	}
	if len(body) > maxDoHSize {
		return nil, &net.DNSError{Err: "answer too large", Name: name, Server: server}
	}
	var msg dohMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, &net.DNSError{Err: "unexpected answer", Name: name, Server: server}
	}
	switch msg.Status {
	case 0:
	case 3:
		return nil, notFound(name, server)
	case 2:
		return nil, &net.DNSError{Err: "server failure (SERVFAIL)", Name: name, Server: server, IsTemporary: true}
	default:
		return nil, &net.DNSError{Err: fmt.Sprintf("DNS error code %d", msg.Status), Name: name, Server: server}
	}
	var out []string
	for _, a := range msg.Answer {
		if a.Type == t {
			out = append(out, a.Data)
		}
	}
	if len(out) == 0 {
		return nil, notFound(name, server)
	}
	return out, nil
}

func notFound(name, server string) error {
	return &net.DNSError{Err: "no such host", Name: name, Server: server, IsNotFound: true}
}

// parseTXT joins the quoted parts of a TXT record as Cloudflare returns it
// ("\"part one\" \"part two\""); Google returns the joined text already.
func parseTXT(d string) string {
	if !strings.HasPrefix(d, `"`) {
		return d
	}
	var b strings.Builder
	in := false
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c == '"':
			in = !in
		case !in:
		case c == '\\' && i+3 < len(d) && isDigits(d[i+1:i+4]) && d[i+1:i+4] <= "255":
			n, _ := strconv.Atoi(d[i+1 : i+4])
			b.WriteByte(byte(n))
			i += 3
		case c == '\\' && i+1 < len(d):
			i++
			b.WriteByte(d[i])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isDigits(s string) bool {
	return strings.Trim(s, "0123456789") == "" && s != ""
}

// userAgent identifies Playkeeper to certificate authorities and DNS
// services.
func userAgent() string {
	return "playkeeper/" + version.Version + " (+https://github.com/CIYAhq/playkeeper)"
}
