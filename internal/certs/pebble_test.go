package certs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pebbleVersion is the release of Pebble and pebble-challtestsrv this test
// runs against; CI installs the same.
const pebbleVersion = "v2.10.1"

// TestPebble gets certificates from Pebble, Let's Encrypt's test certificate
// authority, which looks names up in pebble-challtestsrv's DNS server.
func TestPebble(t *testing.T) {
	env := startPebble(t)
	ctx := t.Context()

	fresh := &Issuer{DirectoryURL: env.dirURL, AccountKeyFile: filepath.Join(t.TempDir(), "account.key"), Client: env.client}
	terms, err := fresh.Terms(ctx)
	if err != nil || terms == "" {
		t.Fatalf("Terms = %q, %v", terms, err)
	}
	t.Run("terms not accepted", func(t *testing.T) {
		_, err := fresh.Issue(ctx, Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: t.TempDir()})
		wantProblem(t, err, CodeTermsNotAccepted, "")
	})

	base := t.TempDir()
	is := &Issuer{
		DirectoryURL:   env.dirURL,
		AccountKeyFile: filepath.Join(base, "account.key"),
		Email:          "admin@example.com",
		AgreedTerms:    terms,
		Client:         env.client,
	}
	dir := filepath.Join(base, "certs")

	t.Run("http-01", func(t *testing.T) {
		h := &HTTP01Responder{Addr: loopback(env.httpPort)}
		c, err := is.Issue(ctx, Request{Names: []string{"mc.example.com"}, HTTP01: h, Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		life := c.NotAfter.Sub(c.NotBefore)
		if !slices.Equal(c.Names, []string{"mc.example.com"}) || !strings.HasPrefix(c.Issuer, "Pebble Intermediate CA ") ||
			life < 89*24*time.Hour || life > 91*24*time.Hour || !c.RenewAt.Equal(RenewAt(c.NotBefore, c.NotAfter)) {
			t.Errorf("Issue = %+v", c)
		}
		wantMode(t, c.File, 0o600)
		wantMode(t, is.AccountKeyFile, 0o600)
		if h.srv != nil {
			t.Error("still listening for http-01 checks")
		}

		st, err := NewStore(StoreOptions{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		served := func() string {
			t.Helper()
			leaf, err := handshake(t, st.GetCertificate, &tls.Config{ServerName: "mc.example.com", RootCAs: env.roots})
			if err != nil {
				t.Fatalf("a client that trusts Pebble rejects mc.example.com: %v", err)
			}
			return fingerprint(leaf.Raw)
		}
		if got := served(); got != c.SHA256 {
			t.Errorf("served %s, want %s", got, c.SHA256)
		}

		renewed, err := is.Issue(ctx, Request{Names: []string{"mc.example.com"}, HTTP01: h, Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := served(); got != renewed.SHA256 || renewed.SHA256 == c.SHA256 {
			t.Errorf("after renewal served %s, want %s (was %s)", got, renewed.SHA256, c.SHA256)
		}
	})

	t.Run("dns-01", func(t *testing.T) {
		d := &DNS01{Challenger: challtestsrvDNS{env}, LookupTXT: DNSServer(env.dns).LookupTXT, Interval: 100 * time.Millisecond, Timeout: 10 * time.Second}
		c, err := is.Issue(ctx, Request{Names: []string{"alex.playkeeper.io"}, DNS01: d, Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Names, []string{"alex.playkeeper.io"}) {
			t.Errorf("names = %q", c.Names)
		}
		// challtestsrv answers for a name without records without the
		// authoritative bit, which Go's resolver reports as a lame referral.
		if vals, _ := DNSServer(env.dns).LookupTXT(ctx, alexTXT+"."); len(vals) != 0 {
			t.Errorf("the TXT record was left behind: %q", vals)
		}
		st, err := NewStore(StoreOptions{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handshake(t, st.GetCertificate, &tls.Config{ServerName: "alex.playkeeper.io", RootCAs: env.roots}); err != nil {
			t.Errorf("a client that trusts Pebble rejects alex.playkeeper.io: %v", err)
		}
	})

	t.Run("port 80 closed", func(t *testing.T) {
		_, err := is.Issue(ctx, Request{Names: []string{"closed.example.com"}, HTTP01: &HTTP01Responder{}, Dir: dir})
		p := wantProblem(t, err, CodePort80Unreachable, "")
		if p.Params["name"] != "closed.example.com" || !p.NeedsAction || !strings.Contains(p.Detail, "connection refused") {
			t.Errorf("problem = %+v", p)
		}
	})

	t.Run("another web server on port 80", func(t *testing.T) {
		ln, err := net.Listen("tcp", loopback(env.httpPort))
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.NotFoundHandler()}
		go srv.Serve(ln)
		defer srv.Close()
		_, err = is.Issue(ctx, Request{Names: []string{"proxied.example.com"}, HTTP01: &HTTP01Responder{}, Dir: dir})
		if p := wantProblem(t, err, CodeWrongAnswer, ""); !strings.Contains(p.Detail, "404") {
			t.Errorf("problem = %+v", p)
		}
	})

	t.Run("dns-01 servers failing", func(t *testing.T) {
		fqdn := "_acme-challenge.servfail.playkeeper.io."
		env.manage(t, "/set-servfail", map[string]any{"host": fqdn})
		defer env.manage(t, "/clear-servfail", map[string]any{"host": fqdn})
		d := &DNS01{Challenger: challtestsrvDNS{env}, LookupTXT: DNSServer(env.dns).LookupTXT, Interval: 100 * time.Millisecond, Timeout: time.Second}
		_, err := is.Issue(ctx, Request{Names: []string{"servfail.playkeeper.io"}, DNS01: d, Dir: dir})
		wantProblem(t, err, CodeDNSServersFailing, "")
	})

	t.Run("name refused", func(t *testing.T) {
		_, err := is.Issue(ctx, Request{Names: []string{"blocked.example.com"}, HTTP01: &HTTP01Responder{}, Dir: dir})
		if p := wantProblem(t, err, CodeNameRefused, ""); p.Params["name"] != "blocked.example.com" {
			t.Errorf("params = %v", p.Params)
		}
	})

	t.Run("name check", func(t *testing.T) {
		// Both record types are set: see the lame referral above.
		env.manage(t, "/add-a", map[string]any{"host": "check.example.com.", "addresses": []string{here4.String()}})
		env.manage(t, "/add-aaaa", map[string]any{"host": "check.example.com.", "addresses": []string{here6.String()}})
		env.manage(t, "/add-a", map[string]any{"host": "elsewhere.example.com.", "addresses": []string{"198.51.100.7"}})
		env.manage(t, "/add-aaaa", map[string]any{"host": "elsewhere.example.com.", "addresses": []string{"2001:db8::7"}})
		env.manage(t, "/set-servfail", map[string]any{"host": "broken.example.com."})
		r := DNSServer(env.dns)
		for name, code := range map[string]string{
			"check.example.com":     CodeNameOK,
			"elsewhere.example.com": CodeNameElsewhere,
			"broken.example.com":    CodeNameLookupFailed,
		} {
			if c := CheckName(ctx, r, name, bothHere); c.Code != code {
				t.Errorf("CheckName(%s) = %s, want %s: %+v", name, c.Code, code, c)
			}
		}
	})
}

// pebbleEnv is Pebble with pebble-challtestsrv as its DNS server, both on
// loopback.
type pebbleEnv struct {
	dirURL   string
	client   *http.Client   // trusts Pebble's HTTPS certificate
	roots    *x509.CertPool // what the certificates Pebble issues chain to
	dns      string         // challtestsrv's DNS server
	mgmt     string         // challtestsrv's management API
	httpPort int            // where Pebble sends http-01 checks
}

func loopback(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func startPebble(t *testing.T) *pebbleEnv {
	pebbleBin, challBin := pebbleBinaries(t)
	dir := t.TempDir()
	certFile, keyFile := writeSelfSigned(t, dir)
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	trusted := x509.NewCertPool()
	trusted.AppendCertsFromPEM(certPEM)
	env := &pebbleEnv{
		client:   &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trusted}}},
		dns:      loopback(freePort(t)),
		mgmt:     loopback(freePort(t)),
		httpPort: freePort(t),
	}

	chall := start(t, nil, challBin,
		"-dnsserver", env.dns, "-management", env.mgmt,
		"-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "",
		"-defaultIPv4", "127.0.0.1", "-defaultIPv6", "")
	chall.waitReady(t, func() error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := DNSServer(env.dns).LookupNetIP(ctx, "ip4", "ready.example.com."); err != nil {
			return err
		}
		return env.challtestsrv(ctx, "/clear-txt", map[string]any{"host": "ready.example.com."})
	})

	listen, mgmt := loopback(freePort(t)), loopback(freePort(t))
	config, err := json.Marshal(map[string]any{"pebble": map[string]any{
		"listenAddress":           listen,
		"managementListenAddress": mgmt,
		"certificate":             certFile,
		"privateKey":              keyFile,
		"httpPort":                env.httpPort,
		"tlsPort":                 freePort(t),
		"domainBlocklist":         []string{"blocked.example.com"},
		"keyAlgorithm":            "ecdsa",
		"retryAfter":              map[string]int{"authz": 1, "order": 1},
		"profiles": map[string]any{
			"default": map[string]any{"description": "90 days, like Let's Encrypt", "validityPeriod": 90 * 24 * 60 * 60},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "pebble.json")
	if err := os.WriteFile(configFile, config, 0o600); err != nil {
		t.Fatal(err)
	}
	pebble := start(t, []string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0", "PEBBLE_AUTHZREUSE=0"},
		pebbleBin, "-config", configFile, "-dnsserver", env.dns, "-strict")
	env.dirURL = "https://" + listen + "/dir"
	var root []byte
	pebble.waitReady(t, func() error {
		if _, err := env.fetch(env.dirURL); err != nil {
			return err
		}
		b, err := env.fetch("https://" + mgmt + "/roots/0")
		root = b
		return err
	})
	env.roots = x509.NewCertPool()
	if !env.roots.AppendCertsFromPEM(root) {
		t.Fatalf("Pebble's root is not PEM: %q", root)
	}
	return env
}

// pebbleBinaries finds pebble and pebble-challtestsrv in
// $PLAYKEEPER_PEBBLE_DIR or, when that is not set, on $PATH.
func pebbleBinaries(t *testing.T) (pebble, challtestsrv string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the Pebble integration test in -short mode")
	}
	dir := os.Getenv("PLAYKEEPER_PEBBLE_DIR")
	find := func(name string) string {
		if dir != "" {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("PLAYKEEPER_PEBBLE_DIR is set, but %v", err)
			}
			return p
		}
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("skipping: %s is not installed. To run this test:\n"+
				"\texport PLAYKEEPER_PEBBLE_DIR=$(mktemp -d)\n"+
				"\tGOBIN=$PLAYKEEPER_PEBBLE_DIR go install github.com/letsencrypt/pebble/v2/cmd/pebble@%[2]s github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@%[2]s",
				name, pebbleVersion)
		}
		return p
	}
	return find("pebble"), find("pebble-challtestsrv")
}

// fetch GETs url, which must answer 200.
func (e *pebbleEnv) fetch(url string) ([]byte, error) {
	resp, err := e.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return b, err
}

// challtestsrv calls pebble-challtestsrv's management API.
func (e *pebbleEnv) challtestsrv(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+e.mgmt+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pebble-challtestsrv %s: %s", path, resp.Status)
	}
	return nil
}

// manage is challtestsrv for a test's own DNS records.
func (e *pebbleEnv) manage(t *testing.T, path string, body any) {
	t.Helper()
	if err := e.challtestsrv(t.Context(), path, body); err != nil {
		t.Fatal(err)
	}
}

// challtestsrvDNS publishes DNS-01 records in pebble-challtestsrv, as the
// playkeeper.io DNS service will in its zone.
type challtestsrvDNS struct{ env *pebbleEnv }

func (c challtestsrvDNS) SetTXT(ctx context.Context, fqdn, value string) error {
	return c.env.challtestsrv(ctx, "/set-txt", map[string]string{"host": fqdn + ".", "value": value})
}

func (c challtestsrvDNS) ClearTXT(ctx context.Context, fqdn, _ string) error {
	return c.env.challtestsrv(ctx, "/clear-txt", map[string]string{"host": fqdn + "."})
}

// process is a command that runs until its test ends.
type process struct {
	name   string
	out    *syncBuffer
	exited chan struct{}
}

// start runs bin until the test ends, logging its output if the test fails.
func start(t *testing.T, env []string, bin string, args ...string) *process {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	p := &process{name: filepath.Base(bin), out: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-p.exited
		if t.Failed() {
			t.Logf("%s output:\n%s", p.name, p.out)
		}
	})
	return p
}

// waitReady polls ready until it succeeds, failing the test if the process
// exits or 30 seconds pass first.
func (p *process) waitReady(t *testing.T, ready func() error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := ready()
		if err == nil {
			return
		}
		select {
		case <-p.exited:
			t.Fatalf("%s exited: %v", p.name, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is not ready after 30s: %v", p.name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
