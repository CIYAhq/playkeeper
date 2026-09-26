package templates

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/packs"
)

func TestPackAddresses(t *testing.T) {
	for _, tt := range []struct {
		addr   string
		public bool
	}{
		{"93.184.216.34", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"2001:4860:4860::8888", true},
		{"64:ff9b::5db8:d822", true},
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fd12:3456::1", false},
		{"fd00:ec2::254", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"100.64.0.1", false},
		{"100.100.100.200", false},
		{"192.0.0.192", false},
		{"198.18.0.1", false},
		{"203.0.113.7", false},
		{"224.0.0.251", false},
		{"255.255.255.255", false},
		{"ff02::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:169.254.169.254", false},
		{"64:ff9b::a9fe:a9fe", false},
		{"64:ff9b:1::1", false},
		{"2001::1", false},
		{"2001:db8::1", false},
		{"2002:7f00:1::1", false},
	} {
		if got := publicAddr(netip.MustParseAddr(tt.addr)); got != tt.public {
			t.Errorf("%s: public %v, want %v", tt.addr, got, tt.public)
		}
	}
	if publicAddrPort(netip.MustParseAddrPort("93.184.216.34:8443")) {
		t.Error("a public address on another port was allowed")
	}
}

func counting(hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "pack")
	}
}

// The name could resolve anywhere; the dialer sees the address it resolved
// to and refuses one that isn't public.
func TestPackClientRefusesPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(counting(&hits))
	defer srv.Close()
	_, err := PackClient().Get(srv.URL + "/pack.zip")
	var addr *addrRefused
	if !errors.As(err, &addr) {
		t.Fatalf("got %v, want the connection to %s refused", err, srv.Listener.Addr())
	}
	if hits.Load() != 0 {
		t.Error("the server was reached")
	}
}

const (
	proxyChildURL = "PLAYKEEPER_TEST_PACK_URL"
	proxyChildCA  = "PLAYKEEPER_TEST_PACK_CA"
)

// Through a proxy, the dialer would check the proxy's address instead of
// the pack host's. net/http reads the proxy variables once per process, so a
// child copy of this test binary fetches with all four set, pointing at a
// listener that counts connections. The child asks for localhost. with a
// trailing dot: Go never proxies localhost or a loopback address, but it
// would proxy that name, which still reaches the test server.
func TestPackClientIgnoresProxyVariables(t *testing.T) {
	if u := os.Getenv(proxyChildURL); u != "" {
		ca, err := os.ReadFile(os.Getenv(proxyChildCA))
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(ca) {
			t.Fatalf("the test server's certificate: %v", err)
		}
		hc := packClient(func(netip.AddrPort) bool { return true })
		hc.Transport.(httpsOnly).rt.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com"}
		resp, err := hc.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(b) != "pack" {
			t.Fatalf("the pack server answered %d %q", resp.StatusCode, b)
		}
		return
	}

	var hits, conns atomic.Int32
	srv := httptest.NewTLSServer(counting(&hits))
	defer srv.Close()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		for {
			c, err := proxy.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	via := "http://" + proxy.Addr().String()
	child := exec.Command(os.Args[0], "-test.run=^TestPackClientIgnoresProxyVariables$")
	child.Env = append(os.Environ(), proxyChildURL+"=https://localhost.:"+port+"/pack.zip", proxyChildCA+"="+ca,
		"HTTPS_PROXY="+via, "HTTP_PROXY="+via, "https_proxy="+via, "http_proxy="+via, "NO_PROXY=", "no_proxy=")
	out, err := child.CombinedOutput()
	if err != nil || hits.Load() != 1 || conns.Load() != 0 {
		t.Fatalf("with the proxy variables set, the pack server answered %d times and the proxy saw %d connections (%v):\n%s", hits.Load(), conns.Load(), err, out)
	}
}

func TestPackClientRefusesPlainHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(counting(&hits))
	defer srv.Close()
	hc := packClient(func(netip.AddrPort) bool { return true })
	if _, err := hc.Get(srv.URL + "/pack.zip"); !errors.Is(err, errPlainHTTP) {
		t.Fatalf("got %v, want plain HTTP refused", err)
	}
	if hits.Load() != 0 {
		t.Error("the server was reached")
	}
}

func TestPackRedirectsAreCheckedAgain(t *testing.T) {
	for _, tt := range []struct {
		to string
		ok bool
	}{
		{"https://cdn.example.org/pack.zip?sig=abc", true},
		{"https://cdn.example.org:443/pack.zip", true},
		{"http://cdn.example.org/pack.zip", false},
		{"https://cdn.example.org:8443/pack.zip", false},
		{"https://user@cdn.example.org/pack.zip", false},
		{"https://169.254.169.254/latest/meta-data/", false},
		{"https://[::1]/pack.zip", false},
		{"https://localhost/pack.zip", false},
		{"https://metadata.google.internal/computeMetadata/v1/", false},
		{"https://router.lan/pack.zip", false},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.to, nil)
		if err := checkRedirect(req, make([]*http.Request, 1)); (err == nil) != tt.ok {
			t.Errorf("%s: got %v, want allowed %v", tt.to, err, tt.ok)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "https://cdn.example.org/pack.zip", nil)
	if err := checkRedirect(req, make([]*http.Request, maxPackRedirects)); err == nil {
		t.Error("a sixth redirect was followed")
	}
}

// packServer serves routes over TLS and hands back a client that reaches it
// for any host name, with PackClient's HTTPS and redirect rules but not its
// address check, which the tests above cover.
func packServer(t *testing.T, routes map[string]http.HandlerFunc) (*http.Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: httpsOnly{tr}, CheckRedirect: checkRedirect}, &hits
}

func TestFetchPack(t *testing.T) {
	body := []byte("PK\x03\x04 a data pack")
	s256 := sha256.Sum256(body)
	s1 := sha1.Sum(body)
	pack := Pack{Kind: DataPack, Name: "tweaks", URL: "https://example.com/packs/tweaks.zip", SHA256: hex.EncodeToString(s256[:])}
	serve := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }
	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
	}
	hc, hits := packServer(t, map[string]http.HandlerFunc{
		"/packs/tweaks.zip": serve,
		"/signed.zip":       serve,
		"/to-signed.zip":    redirect("https://example.com/signed.zip?sig=abc"),
		"/to-http.zip":      redirect("http://example.com/packs/tweaks.zip"),
		"/to-metadata.zip":  redirect("https://169.254.169.254/latest/meta-data/"),
		"/gone.zip":         http.NotFound,
	})
	ctx := context.Background()
	fetch := func(p Pack, lim packs.Limits) ([]byte, error) {
		f, n, err := FetchPack(ctx, hc, t.TempDir(), p, lim)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.NewSectionReader(f, 0, n))
		return b, err
	}
	with := func(edit func(p *Pack)) Pack {
		p := pack
		edit(&p)
		return p
	}

	for name, p := range map[string]Pack{
		"SHA-256":                     pack,
		"SHA-1":                       with(func(p *Pack) { p.SHA256, p.SHA1 = "", strings.ToUpper(hex.EncodeToString(s1[:])) }),
		"a redirect to a signed link": with(func(p *Pack) { p.URL = "https://example.com/to-signed.zip" }),
	} {
		if got, err := fetch(p, packs.Limits{}); err != nil || string(got) != string(body) {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}

	for _, tt := range []struct {
		name string
		p    Pack
		kind Kind
	}{
		{"a changed file", with(func(p *Pack) { p.SHA256 = strings.Repeat("0", 64) }), KindPackHash},
		{"a missing file", with(func(p *Pack) { p.URL = "https://example.com/gone.zip" }), KindPackUnreachable},
		{"a redirect to plain HTTP", with(func(p *Pack) { p.URL = "https://example.com/to-http.zip" }), KindPackAddress},
		{"a redirect to the metadata service", with(func(p *Pack) { p.URL = "https://example.com/to-metadata.zip" }), KindPackAddress},
	} {
		_, err := fetch(tt.p, packs.Limits{})
		refused(t, err, tt.kind)
	}

	before := hits.Load()
	for name, p := range map[string]Pack{
		"plain HTTP":      with(func(p *Pack) { p.URL = "http://example.com/packs/tweaks.zip" }),
		"an IP address":   with(func(p *Pack) { p.URL = "https://127.0.0.1/packs/tweaks.zip" }),
		"a resource pack": with(func(p *Pack) { p.Kind = ResourcePack }),
		"no checksum":     with(func(p *Pack) { p.SHA256 = "" }),
	} {
		_, err := fetch(p, packs.Limits{})
		if e := refused(t, err, KindPackAddress); e.Params["name"] != "tweaks" {
			t.Errorf("%s: params %v", name, e.Params)
		}
	}
	if hits.Load() != before {
		t.Error("a refused pack was requested")
	}

	if _, err := fetch(pack, packs.Limits{MaxBytes: 4}); err == nil {
		t.Error("an oversized pack was kept")
	}
}
