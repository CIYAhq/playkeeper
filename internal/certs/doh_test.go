package certs

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The testdata/doh answers were captured once from Cloudflare's (cf_) and
// Google's (google_) DNS-over-HTTPS JSON APIs.

type fakeDoH struct {
	t       *testing.T
	answers map[string]string // "name type" → testdata/doh file
	mu      sync.Mutex
	queries []string
}

func startDoH(t *testing.T, answers map[string]string) (*fakeDoH, *httptest.Server) {
	f := &fakeDoH{t: t, answers: answers}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeDoH) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") != "application/dns-json" {
		http.Error(w, "missing Accept: application/dns-json", http.StatusBadRequest)
		return
	}
	key := r.URL.Query().Get("name") + " " + r.URL.Query().Get("type")
	f.mu.Lock()
	f.queries = append(f.queries, key)
	f.mu.Unlock()
	file, ok := f.answers[key]
	if !ok {
		f.t.Errorf("unexpected DNS query %q", key)
		http.Error(w, "unexpected", http.StatusInternalServerError)
		return
	}
	b, err := os.ReadFile(filepath.Join("testdata", "doh", file))
	if err != nil {
		f.t.Error(err)
		return
	}
	w.Header().Set("Content-Type", "application/dns-json")
	w.Write(b)
}

func (f *fakeDoH) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func fixtureAnswers(provider string) map[string]string {
	return map[string]string{
		"www.github.com 1":                      provider + "_a_cname.json",
		"www.github.com 28":                     provider + "_nodata.json",
		"dns.google 28":                         provider + "_aaaa.json",
		"github.com 28":                         provider + "_nodata.json",
		"_minecraft._tcp.play.wynncraft.com 33": provider + "_nxdomain.json",
		"dnssec-failed.org 1":                   provider + "_servfail.json",
		"_minecraft._tcp.2b2t.org 33":           provider + "_srv.json",
		"example.com 16":                        provider + "_txt.json",
		"google._domainkey.github.com 16":       provider + "_txt_multi.json",
	}
}

func TestPublicResolverFixtures(t *testing.T) {
	ctx := context.Background()
	var dkim []string
	for _, provider := range []string{"cf", "google"} {
		t.Run(provider, func(t *testing.T) {
			f, srv := startDoH(t, fixtureAnswers(provider))
			r := PublicResolver{Endpoints: []string{srv.URL + "/dns-query"}, Client: srv.Client()}

			addrs, err := r.LookupNetIP(ctx, "ip4", "www.github.com.")
			if err != nil || !slices.Equal(addrs, []netip.Addr{netip.MustParseAddr("140.82.112.4")}) {
				t.Errorf("A www.github.com = %v, %v (the CNAME must be skipped)", addrs, err)
			}
			addrs, err = r.LookupNetIP(ctx, "ip", "www.github.com")
			if err != nil || len(addrs) != 1 {
				t.Errorf("ip www.github.com = %v, %v", addrs, err)
			}
			addrs, err = r.LookupNetIP(ctx, "ip6", "dns.google")
			slices.SortFunc(addrs, netip.Addr.Compare)
			if err != nil || !slices.Equal(addrs, []netip.Addr{netip.MustParseAddr("2001:4860:4860::8844"), netip.MustParseAddr("2001:4860:4860::8888")}) {
				t.Errorf("AAAA dns.google = %v, %v", addrs, err)
			}
			if _, err := r.LookupNetIP(ctx, "ip6", "github.com"); !isNotFound(err) {
				t.Errorf("no AAAA records: %v, want not found", err)
			}
			if _, _, err := r.LookupSRV(ctx, "minecraft", "tcp", "play.wynncraft.com"); !isNotFound(err) {
				t.Errorf("NXDOMAIN: %v, want not found", err)
			}
			_, err = r.LookupNetIP(ctx, "ip4", "dnssec-failed.org")
			var de *net.DNSError
			if !asDNSError(err, &de) || de.IsNotFound || !de.IsTemporary || !strings.Contains(de.Err, "SERVFAIL") {
				t.Errorf("SERVFAIL: %#v", err)
			}
			cname, srvs, err := r.LookupSRV(ctx, "minecraft", "tcp", "2b2t.org")
			if err != nil || cname != "_minecraft._tcp.2b2t.org." || len(srvs) != 1 || *srvs[0] != (net.SRV{Target: "connect.2b2t.org.", Port: 25565, Priority: 10, Weight: 10}) {
				t.Errorf("SRV 2b2t.org = %q %+v %v", cname, srvs, err)
			}
			txt, err := r.LookupTXT(ctx, "example.com.")
			if err != nil || !slices.Equal(txt, []string{"v=spf1 -all", "_k2n1y4vw3qtb4skdx9e7dxt97qrmmq9"}) {
				t.Errorf("TXT example.com = %q, %v", txt, err)
			}
			txt, err = r.LookupTXT(ctx, "google._domainkey.github.com")
			if err != nil || len(txt) != 1 || !strings.HasPrefix(txt[0], "v=DKIM1; k=rsa; p=MIIB") || !strings.Contains(txt[0], "RNAPKu/OPoA7dlR") || !strings.HasSuffix(txt[0], "IDAQAB") {
				t.Errorf("TXT in two parts = %q, %v", txt, err)
			}
			dkim = append(dkim, txt...)
			if f.count() != 10 {
				t.Errorf("%d queries, want 10: %q", f.count(), f.queries)
			}
		})
	}
	if len(dkim) == 2 && dkim[0] != dkim[1] {
		t.Errorf("Cloudflare's and Google's TXT answers differ:\n%q\n%q", dkim[0], dkim[1])
	}
}

func asDNSError(err error, target **net.DNSError) bool {
	de, ok := err.(*net.DNSError)
	*target = de
	return ok
}

func TestPublicResolverFailover(t *testing.T) {
	ctx := context.Background()
	answers := fixtureAnswers("cf")
	good, goodSrv := startDoH(t, answers)
	var broken atomic.Int32
	brokenSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		broken.Add(1)
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}))
	defer brokenSrv.Close()
	// An endpoint that hangs up without answering. A closed server's port
	// won't do: another package's test, run in parallel, can take it and answer.
	hangsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("the endpoint could not hang up: %v", err)
			return
		}
		conn.Close()
	}))
	defer hangsUp.Close()

	r := PublicResolver{Endpoints: []string{brokenSrv.URL, hangsUp.URL, goodSrv.URL}, Client: goodSrv.Client()}
	if addrs, err := r.LookupNetIP(ctx, "ip4", "www.github.com"); err != nil || len(addrs) != 1 {
		t.Fatalf("failover lookup = %v, %v", addrs, err)
	}
	if broken.Load() != 1 || good.count() != 1 {
		t.Errorf("broken asked %d times, good %d times", broken.Load(), good.count())
	}

	nx, nxSrv := startDoH(t, answers)
	later, laterSrv := startDoH(t, answers)
	r = PublicResolver{Endpoints: []string{nxSrv.URL, laterSrv.URL}, Client: nxSrv.Client()}
	if _, _, err := r.LookupSRV(ctx, "minecraft", "tcp", "play.wynncraft.com"); !isNotFound(err) {
		t.Errorf("NXDOMAIN = %v", err)
	}
	if nx.count() != 1 || later.count() != 0 {
		t.Errorf("a final answer was asked again: %d, %d", nx.count(), later.count())
	}

	r = PublicResolver{Endpoints: []string{brokenSrv.URL, hangsUp.URL}, Client: goodSrv.Client()}
	_, err := r.LookupTXT(ctx, "example.com")
	if err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Errorf("all endpoints down = %v, want the last endpoint's error", err)
	}
}

func TestPublicResolverRefuses(t *testing.T) {
	ctx := context.Background()
	big := `{"Status":0,"Answer":[{"name":"example.com","type":16,"data":"` + strings.Repeat("x", maxDoHSize) + `"}]}`
	var hits, elsewhereHits atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhereHits.Add(1) }))
	defer elsewhere.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Length", strconv.Itoa(len(big)))
			w.Write([]byte(big))
		case "/big-chunked":
			for i := 0; i < len(big); i += 4096 {
				w.Write([]byte(big[i:min(i+4096, len(big))]))
				w.(http.Flusher).Flush()
			}
		case "/redirect":
			http.Redirect(w, r, elsewhere.URL+"/dns-query?"+r.URL.RawQuery, http.StatusFound)
		case "/html":
			w.Write([]byte("<html>captive portal</html>"))
		case "/refused":
			w.Write([]byte(`{"Status":5}`))
		}
	}))
	defer srv.Close()
	cases := []struct {
		path, want string
	}{
		{"/big", "too large"},
		{"/big-chunked", "too large"},
		{"/redirect", "could not reach"},
		{"/html", "unexpected answer"},
		{"/refused", "DNS error code 5"},
	}
	for _, c := range cases {
		r := PublicResolver{Endpoints: []string{srv.URL + c.path}, Client: srv.Client()}
		_, err := r.LookupTXT(ctx, "example.com")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.path, err, c.want)
		}
	}
	if elsewhereHits.Load() != 0 {
		t.Error("a redirect was followed")
	}

	hits.Store(0)
	for _, r := range []PublicResolver{
		{Endpoints: []string{strings.Replace(srv.URL, "https://", "http://", 1)}, Client: srv.Client()},
		{Endpoints: []string{"not a url"}},
	} {
		if _, err := r.LookupTXT(ctx, "example.com"); err == nil {
			t.Errorf("endpoint %q was accepted", r.Endpoints[0])
		}
	}
	r := PublicResolver{Endpoints: []string{srv.URL}, Client: srv.Client()}
	for _, name := range []string{"", ".", "bad name.example.com", strings.Repeat("a", 254)} {
		if _, err := r.LookupTXT(ctx, name); err == nil {
			t.Errorf("name %.20q was looked up", name)
		}
	}
	if _, err := r.LookupNetIP(ctx, "tcp", "example.com"); err == nil {
		t.Error("network tcp was accepted")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests were sent for invalid input", n)
	}
}

func TestParseTXT(t *testing.T) {
	for in, want := range map[string]string{
		`v=spf1 -all`:              `v=spf1 -all`,
		`"v=spf1 -all"`:            `v=spf1 -all`,
		`"part one " "part two"`:   `part one part two`,
		`"say \"hi\""`:             `say "hi"`,
		`"back\\slash"`:            `back\slash`,
		`"\065BC"`:                 `ABC`,
		`"\256"`:                   `256`,
		`"unterminated \`:          `unterminated \`,
		`"a" junk-outside "b"`:     `ab`,
		`"with ; semicolon" "and"`: `with ; semicolonand`,
	} {
		if got := parseTXT(in); got != want {
			t.Errorf("parseTXT(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckNameThroughPublicResolver(t *testing.T) {
	_, srv := startDoH(t, map[string]string{
		"example.com 1":  "cf_a_proxied.json",
		"example.com 28": "cf_nodata.json",
	})
	r := PublicResolver{Endpoints: []string{srv.URL}, Client: srv.Client()}
	got := CheckName(context.Background(), r, "example.com", bothHere)
	if got.Code != CodeNameProxied || len(got.Records) != 2 || got.Records[0].Kind != kindCloudflare {
		t.Errorf("CheckName = %+v", got)
	}
}
