package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// offline is an HTTP client for calls the client must refuse before sending.
func offline(t *testing.T) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return nil, errors.New("no request expected")
	})}
}

// checkSigned reads a request's body and checks its signature the way the
// service does.
func checkSigned(t *testing.T, r *http.Request) []byte {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
	}
	if _, err := VerifyRequest(r.Header, r.Method, r.URL.RequestURI(), body, DefaultBase, testNow); err != nil {
		t.Errorf("%s %s: %v", r.Method, r.URL, err)
	}
	return body
}

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		ServiceURL: srv.URL, Key: ed25519.NewKeyFromSeed(testSeed), Name: "alice",
		HTTP: srv.Client(), Now: func() time.Time { return testNow },
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func challengeValue() string {
	sum := sha256.Sum256([]byte("evaGxfADs6pSRb2LAv9IZf17Dt3juxGJ-PCt92wr-oA.9jg46WB3rR_AHD-EBXdN7cBkH1WOu0tA3M9fm21mqTI"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestCheckServiceURLAllowsHTTPSAndLocalHTTPOnly(t *testing.T) {
	for _, ok := range []string{
		"https://names.playkeeper.io",
		"https://names.playkeeper.io/",
		"https://names.example.com:8443",
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
	} {
		if _, err := CheckServiceURL(ok); err != nil {
			t.Errorf("CheckServiceURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"names.playkeeper.io",
		"http://names.playkeeper.io",
		"http://10.0.0.1:8080",
		"http://localhost.example.com",
		"ftp://names.playkeeper.io",
		"https://names.playkeeper.io/v1",
		"https://names.playkeeper.io/?x=1",
		"https://names.playkeeper.io/#top",
		"https://admin:hunter2@names.playkeeper.io",
	} {
		_, err := CheckServiceURL(bad)
		if err == nil {
			t.Errorf("CheckServiceURL(%q) must fail", bad)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the error shows the password: %v", err)
		}
	}
}

func TestChallengesForOtherRecordsAreRefusedBeforeSending(t *testing.T) {
	value := challengeValue()
	c := &Client{Key: ed25519.NewKeyFromSeed(testSeed), Name: "alice", HTTP: offline(t)}
	for _, tc := range []struct{ fqdn, value string }{
		{"_acme-challenge.bob.playkeeper.io", value},
		{"_acme-challenge.alice.example.com", value},
		{"alice.playkeeper.io", value},
		{"_acme-challenge.survival.alice.playkeeper.io", value},
		{"_acme-challenge.alice.playkeeper.io.example.com", value},
		{"_acme-challenge.alice.playkeeper.io", "short"},
		{"_acme-challenge.alice.playkeeper.io", value[:42] + "="},
		{"_acme-challenge.alice.playkeeper.io", value[:40] + "/.."},
		{"_acme-challenge.alice.playkeeper.io", value + "a"},
	} {
		for op, f := range map[string]func(context.Context, string, string) error{"SetTXT": c.SetTXT, "ClearTXT": c.ClearTXT} {
			if err := f(context.Background(), tc.fqdn, tc.value); code(err) != CodeInvalidChallenge {
				t.Errorf("%s(%q, %q) = %v, want %s", op, tc.fqdn, tc.value, err, CodeInvalidChallenge)
			}
		}
	}
	c.Name = ""
	if err := c.SetTXT(context.Background(), "_acme-challenge.alice.playkeeper.io", value); code(err) != CodeNoName {
		t.Errorf("without a name: got %v, want %s", err, CodeNoName)
	}
}

func TestSetTXTReturnsOnceCloudflareHasTheRecord(t *testing.T) {
	value := challengeValue()
	var mu sync.Mutex
	var seen []string
	dns := DNSOK
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkSigned(t, r)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, Challenge{FQDN: ChallengeFQDN("alice", DefaultBase), Value: value, ExpiresAt: testNow.Add(time.Hour), DNS: dns})
	})
	ctx := context.Background()
	if err := c.SetTXT(ctx, "_acme-challenge.Alice.playkeeper.io.", value); err != nil {
		t.Fatalf("a trailing dot and capitals must be accepted: %v", err)
	}
	if err := c.ClearTXT(ctx, "_acme-challenge.alice.playkeeper.io", value); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	dns = DNSPending
	mu.Unlock()
	if err := c.SetTXT(ctx, "_acme-challenge.alice.playkeeper.io", value); code(err) != CodeDNSPending {
		t.Errorf("a record Cloudflare does not have yet: got %v, want %s", err, CodeDNSPending)
	}
	want := []string{
		"PUT /v1/names/alice/acme-challenge/" + value,
		"DELETE /v1/names/alice/acme-challenge/" + value,
		"PUT /v1/names/alice/acme-challenge/" + value,
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, want) {
		t.Errorf("requests %q, want %q", seen, want)
	}
}

func TestServiceRefusalsKeepTheirCodeHintParamsAndRetryAfter(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkSigned(t, r)
		w.Header().Set("Retry-After", "120")
		writeJSON(w, http.StatusTooManyRequests, ErrorBody{
			Error: "This key sent too many requests.", Code: CodeRateLimited,
			Hint: "Try again in 2 minutes.", Params: map[string]any{"retryAfterSeconds": 120},
		})
	})
	_, err := c.Claim(context.Background(), "alice")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusTooManyRequests || e.Code != CodeRateLimited ||
		e.Hint != "Try again in 2 minutes." || e.RetryAfter != 2*time.Minute || e.Params["retryAfterSeconds"] != float64(120) {
		t.Fatalf("got %#v", err)
	}
	if err.Error() != "This key sent too many requests. Try again in 2 minutes." {
		t.Errorf("message %q", err.Error())
	}
}

func TestUnexpectedAnswersAreReportedPlainly(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		want string
	}{
		{"proxy error page", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html><body><h1>502 Bad Gateway</h1></body></html>")
		}, "not available right now (HTTP 502)"},
		{"JSON without a code", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "down"})
		}, "not available right now (HTTP 503)"},
		{"oversized answer", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte(" "), maxResponse+1))
		}, "larger than expected"},
		{"not JSON", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}, "unexpected answer"},
	} {
		c := testClient(t, tc.h)
		if _, err := c.IP(context.Background(), AnyFamily); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	c := &Client{ServiceURL: srv.URL}
	_, err := c.IP(context.Background(), AnyFamily)
	if code(err) != CodeUnavailable || !strings.Contains(err.Error(), "redirect") || followed.Load() {
		t.Fatalf("got %v (followed: %v)", err, followed.Load())
	}
}

func TestLocalChecksNeedNoRequest(t *testing.T) {
	ctx := context.Background()
	c := &Client{Key: ed25519.NewKeyFromSeed(testSeed), Name: "alice", HTTP: offline(t), HTTP4: offline(t), HTTP6: offline(t)}
	a, err := c.Available(ctx, "Alice!")
	if err != nil || a.Available || a.Code != CodeInvalidName || a.Params["problem"] != ProblemCharacters || a.Message == "" {
		t.Errorf("Available of an invalid name = %+v, %v", a, err)
	}
	if _, err := c.Claim(ctx, "xn--80ak6aa92e"); code(err) != CodeInvalidName {
		t.Errorf("Claim of an invalid name: %v", err)
	}
	if _, err := c.SetServer(ctx, "a.b", 25565); code(err) != CodeInvalidServer {
		t.Errorf("SetServer with a dotted label: %v", err)
	}
	for _, port := range []int{0, -1, 65536} {
		if _, err := c.SetServer(ctx, "survival", port); code(err) != CodeInvalidPort {
			t.Errorf("SetServer with port %d: %v", port, err)
		}
	}
	c.Name = "Alice"
	if err := c.RemoveServer(ctx, "survival"); code(err) != CodeInvalidName {
		t.Errorf("an invalid stored name must not reach a path: %v", err)
	}
	c.Name = ""
	if _, err := c.Refresh(ctx); code(err) != CodeNoName {
		t.Errorf("Refresh without a name: %v", err)
	}
	c.Name, c.Key = "alice", nil
	if _, err := c.Release(ctx); err == nil || !strings.Contains(err.Error(), "no key") {
		t.Errorf("Release without a key: %v", err)
	}
}

func TestRequestsUseTheRightPathsAndOnlyChangesAreSigned(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		signedReq := r.Header.Get(HeaderSignature) != ""
		body := checkSignedIf(t, r, signedReq)
		mu.Lock()
		seen = append(seen, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		mu.Unlock()
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "playkeeper ") {
			t.Errorf("User-Agent %q", ua)
		}
		switch {
		case r.URL.Path == "/v1/ip" || r.Method == http.MethodGet && r.URL.Path == "/v1/names/bob":
			if signedReq {
				t.Errorf("%s %s must not be signed", r.Method, r.URL.Path)
			}
		case !signedReq:
			t.Errorf("%s %s must be signed", r.Method, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/v1/ip":
			writeJSON(w, http.StatusOK, IPInfo{IP: "5.75.160.99", Family: "ipv4", Public: true})
		case r.URL.Path == "/v1/names/bob":
			writeJSON(w, http.StatusOK, Availability{Name: "bob", Address: "bob.playkeeper.io", Available: true, Message: "bob.playkeeper.io is free."})
		case r.URL.Path == "/v1/names":
			writeJSON(w, http.StatusOK, NameList{Names: []Name{{Name: "alice", State: StateReleased}}})
		case strings.Contains(r.URL.Path, "/servers/"):
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req ServerRequest
			_ = json.Unmarshal(body, &req)
			writeJSON(w, http.StatusOK, Server{Port: req.Port, DNS: DNSOK})
		default:
			writeJSON(w, http.StatusOK, Name{Name: "alice", State: StateActive, DNS: DNSOK})
		}
	})
	ctx := context.Background()
	if info, err := c.IP(ctx, AnyFamily); err != nil || info.IP != "5.75.160.99" {
		t.Errorf("IP = %+v, %v", info, err)
	}
	if a, err := c.Available(ctx, "bob"); err != nil || !a.Available {
		t.Errorf("Available = %+v, %v", a, err)
	}
	if _, err := c.Claim(ctx, "alice"); err != nil {
		t.Error(err)
	}
	if s, err := c.SetServer(ctx, "", 25565); err != nil || s.Port != 25565 {
		t.Errorf("SetServer = %+v, %v", s, err)
	}
	if _, err := c.SetServer(ctx, "survival", 25566); err != nil {
		t.Error(err)
	}
	if err := c.RemoveServer(ctx, "survival"); err != nil {
		t.Error(err)
	}
	if l, err := c.Names(ctx); err != nil || len(l) != 1 || l[0].State != StateReleased {
		t.Errorf("Names = %+v, %v", l, err)
	}
	if _, err := c.Release(ctx); err != nil {
		t.Error(err)
	}
	want := []string{
		"GET /v1/ip",
		"GET /v1/names/bob",
		"PUT /v1/names/alice",
		`PUT /v1/names/alice/servers/@ {"port":25565}`,
		`PUT /v1/names/alice/servers/survival {"port":25566}`,
		"DELETE /v1/names/alice/servers/survival",
		"GET /v1/names",
		"DELETE /v1/names/alice",
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, want) {
		t.Errorf("requests\n%q\nwant\n%q", seen, want)
	}
}

func checkSignedIf(t *testing.T, r *http.Request, signed bool) []byte {
	if signed {
		return checkSigned(t, r)
	}
	body, _ := io.ReadAll(r.Body)
	return body
}

// refreshService answers address refreshes the way the real service does,
// taking the address from the IP version the request came over (which the
// test transports put in a header).
type refreshService struct {
	t    *testing.T
	mu   sync.Mutex
	name Name
	seen []string
}

const (
	machineIPv4 = "5.75.160.99"
	machineIPv6 = "2a01:4f8:c012:6f3a::1"
)

func (s *refreshService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := checkSigned(s.t, r)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/names/alice/address" {
		s.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}
	var req RefreshRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.t.Errorf("refresh body %q: %v", body, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	family := r.Header.Get("X-Test-Family")
	switch family {
	case "4":
		s.name.IPv4 = machineIPv4
		if req.ClearOther {
			s.name.IPv6 = ""
		}
	case "6":
		s.name.IPv6 = machineIPv6
		if req.ClearOther {
			s.name.IPv4 = ""
		}
	}
	if req.ClearOther {
		family += " clear"
	}
	s.seen = append(s.seen, family)
	writeJSON(w, http.StatusOK, s.name)
}

func overFamily(srv *httptest.Server, family string, fail error) *http.Client {
	next := srv.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if fail != nil {
			return nil, fail
		}
		r = r.Clone(r.Context())
		r.Header.Set("X-Test-Family", family)
		return next.RoundTrip(r)
	})}
}

var (
	errNoIPv6Route = &net.OpError{Op: "dial", Net: "tcp6", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH}}
	errNoIPv4      = &net.OpError{Op: "dial", Net: "tcp4", Err: &net.AddrError{Err: "no suitable address found", Addr: "names.playkeeper.io"}}
	errIPv6Timeout = &net.OpError{Op: "read", Net: "tcp6", Err: os.ErrDeadlineExceeded}
)

func TestRefreshSetsBothVersionsAndClearsOnlyOneThatHasNoRoute(t *testing.T) {
	const old4, old6 = "5.75.160.21", "2a01:4f8:c012:6f3a::2"
	for _, tc := range []struct {
		name           string
		before4        string
		before6        string
		fail4, fail6   error
		seen           []string
		want4, want6   string
		wantUnreachErr bool
	}{
		{name: "both versions", before4: old4, before6: old6, seen: []string{"4", "6"}, want4: machineIPv4, want6: machineIPv6},
		{name: "no IPv6 route removes the old AAAA", before4: old4, before6: old6, fail6: errNoIPv6Route,
			seen: []string{"4", "4 clear"}, want4: machineIPv4},
		{name: "no IPv6 route and no AAAA", before4: old4, fail6: errNoIPv6Route, seen: []string{"4"}, want4: machineIPv4},
		{name: "an IPv6 timeout keeps the AAAA", before4: old4, before6: old6, fail6: errIPv6Timeout,
			seen: []string{"4"}, want4: machineIPv4, want6: old6},
		{name: "no IPv4 address removes the old A", before4: old4, before6: old6, fail4: errNoIPv4,
			seen: []string{"6", "6 clear"}, want6: machineIPv6},
		{name: "neither version", before4: old4, before6: old6, fail4: errNoIPv4, fail6: errNoIPv6Route, wantUnreachErr: true},
	} {
		svc := &refreshService{t: t, name: Name{Name: "alice", State: StateActive, IPv4: tc.before4, IPv6: tc.before6}}
		srv := httptest.NewServer(svc)
		c := &Client{
			ServiceURL: srv.URL, Key: ed25519.NewKeyFromSeed(testSeed), Name: "alice", HTTP: offline(t),
			HTTP4: overFamily(srv, "4", tc.fail4), HTTP6: overFamily(srv, "6", tc.fail6),
			Now: func() time.Time { return testNow },
		}
		n, err := c.Refresh(context.Background())
		srv.Close()
		if tc.wantUnreachErr {
			if !familyUnavailable(err) || !strings.Contains(err.Error(), "could not reach the names service") {
				t.Errorf("%s: got %v, want the connection error", tc.name, err)
			}
			if len(svc.seen) != 0 {
				t.Errorf("%s: the service saw %q", tc.name, svc.seen)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !slices.Equal(svc.seen, tc.seen) {
			t.Errorf("%s: requests %q, want %q", tc.name, svc.seen, tc.seen)
		}
		if n.IPv4 != tc.want4 || n.IPv6 != tc.want6 || svc.name.IPv4 != tc.want4 || svc.name.IPv6 != tc.want6 {
			t.Errorf("%s: got A %q AAAA %q (service A %q AAAA %q), want A %q AAAA %q",
				tc.name, n.IPv4, n.IPv6, svc.name.IPv4, svc.name.IPv6, tc.want4, tc.want6)
		}
	}
}

func TestRefreshReportsTheServicesAnswerOverAnUnreachableVersion(t *testing.T) {
	lapsed := func(w http.ResponseWriter, r *http.Request) {
		checkSigned(t, r)
		writeJSON(w, http.StatusConflict, ErrorBody{Error: "alice.playkeeper.io lapsed because it was not refreshed.", Code: CodeNameLapsed, Hint: "Claim it again."})
	}
	for _, tc := range []struct {
		name  string
		fail4 error
	}{
		{"both versions refused", nil},
		{"IPv4 unreachable, IPv6 refused", errNoIPv4},
	} {
		srv := httptest.NewServer(http.HandlerFunc(lapsed))
		c := &Client{
			ServiceURL: srv.URL, Key: ed25519.NewKeyFromSeed(testSeed), Name: "alice",
			HTTP4: overFamily(srv, "4", tc.fail4), HTTP6: overFamily(srv, "6", nil),
			Now: func() time.Time { return testNow },
		}
		_, err := c.Refresh(context.Background())
		srv.Close()
		if code(err) != CodeNameLapsed {
			t.Errorf("%s: got %v, want %s", tc.name, err, CodeNameLapsed)
		}
	}
}

func TestIPGoesOverTheRequestedVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Family") == "6" {
			writeJSON(w, http.StatusOK, IPInfo{IP: machineIPv6, Family: "ipv6", Public: true})
			return
		}
		writeJSON(w, http.StatusOK, IPInfo{IP: machineIPv4, Family: "ipv4", Public: true})
	}))
	defer srv.Close()
	c := &Client{ServiceURL: srv.URL, HTTP: offline(t), HTTP4: offline(t), HTTP6: overFamily(srv, "6", nil)}
	if info, err := c.IP(context.Background(), IPv6); err != nil || info.IP != machineIPv6 {
		t.Errorf("IP over IPv6 = %+v, %v", info, err)
	}
}
