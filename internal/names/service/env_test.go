package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

var testStart = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer collects the service's log.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testEnv is the service behind an httptest server whose only trusted proxy
// is 127.0.0.1, talking to a fake Cloudflare, on a test clock.
type testEnv struct {
	t   *testing.T
	clk *testClock
	cf  *fakeCloudflare
	cfg Config
	svc *Service
	srv *httptest.Server
	log *syncBuffer
}

func testConfig(t *testing.T, clk *testClock, cf *fakeCloudflare, log io.Writer) Config {
	return Config{
		Base: testBase, CloudflareToken: testToken, CloudflareZone: testZoneID,
		DataDir:        t.TempDir(),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		MaxNamesPerKey: 1, ClaimsPerDay: DefaultClaimsPerDay, RecordReserve: DefaultRecordReserve,
		Log:           slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:           clk.Now,
		HTTP:          cf.srv.Client(),
		cloudflareAPI: cf.srv.URL + "/client/v4",
		pageSize:      2,
	}
}

// newEnv starts a test environment; setup may change the configuration and
// the fake Cloudflare before the service starts.
func newEnv(t *testing.T, setup ...func(*testEnv)) *testEnv {
	t.Helper()
	e := &testEnv{t: t, clk: &testClock{t: testStart}, log: &syncBuffer{}}
	e.cf = newFakeCloudflare(t)
	e.cfg = testConfig(t, e.clk, e.cf, e.log)
	for _, f := range setup {
		f(e)
	}
	svc, err := New(context.Background(), e.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.svc = svc
	e.srv = httptest.NewServer(svc.Handler())
	t.Cleanup(func() {
		e.srv.Close()
		e.svc.Close()
		if strings.Contains(e.log.String(), testToken) {
			t.Error("the Cloudflare token appears in the service's log")
		}
	})
	return e
}

func (e *testEnv) tick() { e.svc.tick(context.Background()) }

// grown lets the names claimed so far reach the age at which they may have
// server addresses.
func (e *testEnv) grown() {
	e.clk.Add(serverAddressAge)
	e.tick()
}

// machine is where an install runs: its public addresses, which the
// service's proxy reports in X-Forwarded-For. An empty address means the
// machine has no route of that IP version.
type machine struct {
	mu     sync.Mutex
	v4, v6 string
}

func newMachine(v4, v6 string) *machine { return &machine{v4: v4, v6: v6} }

func (m *machine) set(v4, v6 string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.v4, m.v6 = v4, v6
}

func (m *machine) addr(f names.Family) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case f == names.IPv4:
		return m.v4
	case f == names.IPv6:
		return m.v6
	case m.v4 != "":
		return m.v4
	}
	return m.v6
}

func noRoute(f names.Family) error {
	network := "tcp4"
	if f == names.IPv6 {
		network = "tcp6"
	}
	return &net.OpError{Op: "dial", Net: network, Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH}}
}

func testKey(seed string) ed25519.PrivateKey {
	sum := sha256.Sum256([]byte(seed))
	return ed25519.NewKeyFromSeed(sum[:])
}

// install is a Playkeeper install with its own key on m, using the real
// client.
func (e *testEnv) install(seed string, m *machine) *names.Client {
	via := func(f names.Family) *http.Client {
		next := e.srv.Client().Transport
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			addr := m.addr(f)
			if addr == "" {
				return nil, noRoute(f)
			}
			r = r.Clone(r.Context())
			r.Header.Set("X-Forwarded-For", addr)
			return next.RoundTrip(r)
		})}
	}
	return &names.Client{
		ServiceURL: e.srv.URL, Base: testBase, Key: testKey(seed),
		HTTP: via(names.AnyFamily), HTTP4: via(names.IPv4), HTTP6: via(names.IPv6), Now: e.clk.Now,
	}
}

// claimed is an install that claimed name and refreshed it once.
func (e *testEnv) claimed(seed, name string, m *machine) *names.Client {
	e.t.Helper()
	c := e.install(seed, m)
	if _, err := c.Claim(context.Background(), name); err != nil {
		e.t.Fatalf("claim %s: %v", name, err)
	}
	c.Name = name
	if _, err := c.Refresh(context.Background()); err != nil {
		e.t.Fatalf("refresh %s: %v", name, err)
	}
	return c
}

// signedRequest builds a request signed with key at the given time, as
// if the proxy saw it come from xff.
func (e *testEnv) signedRequest(method, path string, body []byte, key ed25519.PrivateKey, at time.Time, xff string) *http.Request {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	names.SignRequest(req, key, testBase, body, at)
	req.Header.Set("X-Forwarded-For", xff)
	return req
}

// send sends req and decodes the answer; the returned error body is empty
// for a success.
func (e *testEnv) send(req *http.Request) (*http.Response, names.ErrorBody) {
	e.t.Helper()
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body names.ErrorBody
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		if err := json.Unmarshal(b, &body); err != nil {
			e.t.Fatalf("%s %s: HTTP %d with a body that is not an error: %q", req.Method, req.URL.Path, resp.StatusCode, b)
		}
	}
	return resp, body
}

func (e *testEnv) get(path, xff string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// row reads name straight from the database.
func (e *testEnv) row(name string) *nameRow {
	e.t.Helper()
	r, err := e.svc.getName(context.Background(), e.svc.db, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func codeOf(err error) string {
	var e *names.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func acmeValue(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const (
	aliceV4  = "5.75.160.99"
	aliceV6  = "2a01:4f8:c012:6f3a::1"
	aliceFQD = "alice.playkeeper.io"
)
