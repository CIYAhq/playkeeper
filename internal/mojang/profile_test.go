package mojang

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testdata holds answers captured from api.minecraftservices.com on
// 2026-09-25: found_notch.json, found_jeb.json (asked for as "jeB_"),
// not_found.json and endpoint_missing.json (a misspelt lookup path).
// rate_limited.json is the 429 body Mojang documents, and found_demo.json
// is a made-up account with the documented demo field.

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 25, 15, 4, 51, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeMojang struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	respond http.HandlerFunc
}

func (f *fakeMojang) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.EscapedPath())
	f.headers = append(f.headers, r.Header.Clone())
	respond := f.respond
	f.mu.Unlock()
	respond(w, r)
}

func (f *fakeMojang) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func (f *fakeMojang) request(i int) (string, http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths[i], f.headers[i]
}

func (f *fakeMojang) setRespond(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = h
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// mojangLike answers like the profile service: names match in any
// capitalisation, unknown names get the captured 404, and any other path
// gets the 404 for a missing address.
func mojangLike(t *testing.T) http.HandlerFunc {
	known := map[string][]byte{
		"notch":     fixture(t, "found_notch.json"),
		"jeb_":      fixture(t, "found_jeb.json"),
		"pipdemo42": fixture(t, "found_demo.json"),
	}
	notFound := string(fixture(t, "not_found.json"))
	missing := fixture(t, "endpoint_missing.json")
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, lookupPath)
		if !ok || r.Method != http.MethodGet || name == "" || strings.Contains(name, "/") {
			writeJSON(w, http.StatusNotFound, missing)
			return
		}
		if body, ok := known[strings.ToLower(name)]; ok {
			w.Header().Set("Cache-Control", "max-age=300")
			writeJSON(w, http.StatusOK, body)
			return
		}
		writeJSON(w, http.StatusNotFound, []byte(strings.ReplaceAll(notFound, "xq7zz9pknope1", name)))
	}
}

// anyone answers every lookup with a profile, for cache and budget tests.
func anyone(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, lookupPath)
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(name))))[:32]
	writeJSON(w, http.StatusOK, fmt.Appendf(nil, `{"id":"%s","name":"%s"}`, id, name))
}

func startFake(t *testing.T, respond http.HandlerFunc) (*fakeMojang, *httptest.Server) {
	f := &fakeMojang{respond: respond}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func newTestClient(t *testing.T, srv *httptest.Server, clock *fakeClock, o Options) *Client {
	t.Helper()
	o.BaseURL, o.HTTP, o.Now = srv.URL, srv.Client(), clock.Now
	c, err := NewClient(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wantErr(t *testing.T, err, kind error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	return e
}

func TestLookupFound(t *testing.T) {
	for _, tc := range []struct {
		ask  string
		want Profile
	}{
		{"Notch", Profile{ID: "069a79f4-44e9-4726-a5be-fca90e38aaf5", Name: "Notch"}},
		{"nOtCh", Profile{ID: "069a79f4-44e9-4726-a5be-fca90e38aaf5", Name: "Notch"}},
		{"jeB_", Profile{ID: "853c80ef-3c37-49fd-aa49-938b674adae6", Name: "jeb_"}},
		{"pipdemo42", Profile{ID: "5f1d3c2a-9b8e-4d7c-ae6f-0b1a2c3d4e5f", Name: "PipDemo42", Demo: true}},
	} {
		t.Run(tc.ask, func(t *testing.T) {
			f, srv := startFake(t, mojangLike(t))
			c := newTestClient(t, srv, newClock(), Options{})
			got, err := c.Lookup(context.Background(), tc.ask)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			path, header := f.request(0)
			if path != lookupPath+tc.ask {
				t.Errorf("asked for %q", path)
			}
			if ua := header.Get("User-Agent"); ua != userAgent {
				t.Errorf("User-Agent = %q", ua)
			}
		})
	}
}

func TestLookupNotFound(t *testing.T) {
	_, srv := startFake(t, mojangLike(t))
	c := newTestClient(t, srv, newClock(), Options{})
	_, err := c.Lookup(context.Background(), "xq7zz9pknope1")
	e := wantErr(t, err, ErrNotFound)
	if e.Error() != "No Minecraft: Java Edition account has that name." {
		t.Errorf("message = %q", e.Error())
	}

	// api.mojang.com once answered unknown names with 204 and no body.
	_, srv = startFake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	c = newTestClient(t, srv, newClock(), Options{})
	_, err = c.Lookup(context.Background(), "Nobody_here")
	wantErr(t, err, ErrNotFound)
}

// A 404 for the address itself must not read as "no such player", or a
// moved endpoint would turn every friend away as a typo.
func TestLookupMissingEndpointIsUnavailable(t *testing.T) {
	missing := fixture(t, "endpoint_missing.json")
	f, srv := startFake(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusNotFound, missing) })
	c := newTestClient(t, srv, newClock(), Options{})
	for range 2 {
		_, err := c.Lookup(context.Background(), "Notch")
		wantErr(t, err, ErrUnavailable)
	}
	if f.requests() != 2 {
		t.Errorf("%d requests, want 2: an unexpected answer must not be cached", f.requests())
	}
}

func TestLookupRateLimited(t *testing.T) {
	body := fixture(t, "rate_limited.json")
	f, srv := startFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusTooManyRequests, body)
	})
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{})

	_, err := c.Lookup(context.Background(), "Notch")
	if e := wantErr(t, err, ErrRateLimited); e.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", e.RetryAfter)
	}

	clock.Advance(10 * time.Second)
	_, err = c.Lookup(context.Background(), "jeb_")
	if e := wantErr(t, err, ErrRateLimited); e.RetryAfter != 20*time.Second {
		t.Errorf("RetryAfter = %v, want the 20s left", e.RetryAfter)
	}
	if f.requests() != 1 {
		t.Fatalf("%d requests while Mojang asked to wait", f.requests())
	}

	f.setRespond(mojangLike(t))
	clock.Advance(20 * time.Second)
	if _, err := c.Lookup(context.Background(), "jeb_"); err != nil {
		t.Fatal(err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := newClock().Now()
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"30", 30 * time.Second},
		{" 5 ", 5 * time.Second},
		{"", time.Minute},
		{"soon", time.Minute},
		{"0", time.Minute},
		{"-5", time.Minute},
		{"99999999999999999", 10 * time.Minute},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), time.Minute},
		{now.Add(24 * time.Hour).Format(http.TimeFormat), 10 * time.Minute},
	} {
		if got := retryAfter(tc.header, now); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestLookupBudget(t *testing.T) {
	f, srv := startFake(t, anyone)
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{PerMinute: 2})
	ctx := context.Background()

	for _, name := range []string{"Alex", "Steve"} {
		if _, err := c.Lookup(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.Lookup(ctx, "Herobrine")
	if e := wantErr(t, err, ErrRateLimited); e.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", e.RetryAfter)
	}
	if _, err := c.Lookup(ctx, "ALEX"); err != nil {
		t.Errorf("a cached answer should not need the budget: %v", err)
	}
	if f.requests() != 2 {
		t.Fatalf("%d requests, want 2", f.requests())
	}

	clock.Advance(30 * time.Second)
	if _, err := c.Lookup(ctx, "Herobrine"); err != nil {
		t.Fatal(err)
	}
}

func TestLookupMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":      `<html>Service Unavailable</html>`,
		"array":         `[]`,
		"null":          `null`,
		"empty object":  `{}`,
		"no name":       `{"id":"069a79f444e94726a5befca90e38aaf5"}`,
		"bad id":        `{"id":"069a79f444e94726a5befca90e38aaz5","name":"Notch"}`,
		"short id":      `{"id":"069a79f4","name":"Notch"}`,
		"invalid name":  `{"id":"069a79f444e94726a5befca90e38aaf5","name":"No tch"}`,
		"someone else":  `{"id":"853c80ef3c3749fdaa49938b674adae6","name":"jeb_"}`,
		"cut off":       `{"id":"069a79f444e94726a5befca90e38aaf5","na`,
		"wrong id type": `{"id":42,"name":"Notch"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := startFake(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, []byte(body)) })
			c := newTestClient(t, srv, newClock(), Options{})
			for range 2 {
				_, err := c.Lookup(context.Background(), "Notch")
				wantErr(t, err, ErrUnavailable)
			}
			if f.requests() != 2 {
				t.Errorf("%d requests, want 2: a malformed answer must not be cached", f.requests())
			}
		})
	}
}

func TestLookupOversized(t *testing.T) {
	padded := `{"id":"069a79f444e94726a5befca90e38aaf5","name":"Notch"` + strings.Repeat(" ", maxAnswerBytes) + `}`
	for name, respond := range map[string]http.HandlerFunc{
		"declared length": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, []byte(padded))
		},
		"chunked": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for i := 0; i < len(padded); i += 1024 {
				w.Write([]byte(padded[i:min(i+1024, len(padded))]))
				w.(http.Flusher).Flush()
			}
		},
		"not found": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, []byte(`{"path":"`+strings.Repeat("x", maxAnswerBytes)+`"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, srv := startFake(t, respond)
			c := newTestClient(t, srv, newClock(), Options{})
			_, err := c.Lookup(context.Background(), "Notch")
			wantErr(t, err, ErrUnavailable)
		})
	}
}

func TestLookupRefusesRedirects(t *testing.T) {
	for _, location := range []string{"https://evil.example/minecraft/profile/lookup/name/Notch", "/elsewhere"} {
		f, srv := startFake(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, location, http.StatusFound)
		})
		c := newTestClient(t, srv, newClock(), Options{})
		_, err := c.Lookup(context.Background(), "Notch")
		wantErr(t, err, ErrUnavailable)
		if f.requests() != 1 {
			t.Errorf("redirect to %s: %d requests, want 1", location, f.requests())
		}
	}
}

func TestLookupServerErrors(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		f, srv := startFake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		c := newTestClient(t, srv, newClock(), Options{})
		for range 2 {
			_, err := c.Lookup(context.Background(), "Notch")
			if e := wantErr(t, err, ErrUnavailable); !strings.Contains(e.Error(), strconv.Itoa(status)) {
				t.Errorf("message %q does not say HTTP %d", e.Error(), status)
			}
		}
		if f.requests() != 2 {
			t.Errorf("HTTP %d was cached", status)
		}
	}
}

func TestLookupInvalidNameAsksNobody(t *testing.T) {
	f, srv := startFake(t, mojangLike(t))
	c := newTestClient(t, srv, newClock(), Options{})
	for _, name := range []string{"", "ab", "abcdefghijklmnopq", "No tch", "../../x", "Notch/", "Notch%2F", "名前名前", "Notch\n"} {
		_, err := c.Lookup(context.Background(), name)
		wantErr(t, err, ErrInvalidName)
	}
	if f.requests() != 0 {
		t.Errorf("%d requests for invalid names", f.requests())
	}
}

func TestLookupCanceled(t *testing.T) {
	f, srv := startFake(t, mojangLike(t))
	c := newTestClient(t, srv, newClock(), Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Lookup(ctx, "Notch")
	wantErr(t, err, ErrUnavailable)
	if _, err := c.Lookup(context.Background(), "Notch"); err != nil {
		t.Fatalf("a canceled lookup must not be cached: %v", err)
	}
	if f.requests() != 1 {
		t.Errorf("%d requests, want 1", f.requests())
	}
}

func TestCacheExpiry(t *testing.T) {
	f, srv := startFake(t, mojangLike(t))
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{})
	ctx := context.Background()
	lookup := func(name string, want error) {
		t.Helper()
		_, err := c.Lookup(ctx, name)
		if want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Fatalf("Lookup(%s) = %v, want %v", name, err, want)
		}
	}
	expect := func(n int) {
		t.Helper()
		if f.requests() != n {
			t.Fatalf("%d requests, want %d", f.requests(), n)
		}
	}

	lookup("Notch", nil)
	lookup("NOTCH", nil)
	expect(1)
	clock.Advance(DefaultFoundTTL - time.Second)
	lookup("notch", nil)
	expect(1)
	clock.Advance(time.Second)
	lookup("Notch", nil)
	expect(2)

	lookup("Nobody_here", ErrNotFound)
	clock.Advance(DefaultNotFoundTTL - time.Second)
	lookup("nobody_here", ErrNotFound)
	expect(3)
	clock.Advance(time.Second)
	lookup("Nobody_here", ErrNotFound)
	expect(4)
}

func TestCacheCustomTTL(t *testing.T) {
	f, srv := startFake(t, mojangLike(t))
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{FoundTTL: 10 * time.Second, NotFoundTTL: 5 * time.Second})
	ctx := context.Background()
	c.Lookup(ctx, "Notch")
	c.Lookup(ctx, "Nobody_here")
	clock.Advance(5 * time.Second)
	c.Lookup(ctx, "Notch")
	c.Lookup(ctx, "Nobody_here")
	if f.requests() != 3 {
		t.Fatalf("%d requests, want 3", f.requests())
	}
	clock.Advance(5 * time.Second)
	c.Lookup(ctx, "Notch")
	if f.requests() != 4 {
		t.Fatalf("%d requests, want 4", f.requests())
	}
}

func TestCacheIsBounded(t *testing.T) {
	f, srv := startFake(t, anyone)
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{MaxEntries: 3, PerMinute: 1000})
	ctx := context.Background()
	for i := 1; i <= 10; i++ {
		if _, err := c.Lookup(ctx, fmt.Sprintf("Player%02d", i)); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
		c.mu.Lock()
		n := len(c.cache)
		c.mu.Unlock()
		if n > 3 {
			t.Fatalf("cache holds %d entries, want at most 3", n)
		}
	}
	c.Lookup(ctx, "Player10")
	c.Lookup(ctx, "Player08")
	if f.requests() != 10 {
		t.Fatalf("the newest answers were evicted: %d requests", f.requests())
	}
	c.Lookup(ctx, "Player01")
	if f.requests() != 11 {
		t.Fatalf("the oldest answer was kept: %d requests", f.requests())
	}
}

func TestCacheEvictsExpiredFirst(t *testing.T) {
	f, srv := startFake(t, mojangLike(t))
	clock := newClock()
	c := newTestClient(t, srv, clock, Options{MaxEntries: 2})
	ctx := context.Background()
	c.Lookup(ctx, "Notch")
	c.Lookup(ctx, "Nobody_here")
	clock.Advance(2 * time.Minute)
	c.Lookup(ctx, "jeb_")
	c.Lookup(ctx, "Notch")
	if f.requests() != 3 {
		t.Fatalf("%d requests, want 3: the expired not-found answer should have made room", f.requests())
	}
}

func TestConcurrentLookups(t *testing.T) {
	_, srv := startFake(t, anyone)
	c := newTestClient(t, srv, newClock(), Options{MaxEntries: 8, PerMinute: 1000})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if _, err := c.Lookup(context.Background(), fmt.Sprintf("Player%02d", i%12)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestNewClientBaseURL(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"", "https://api.minecraftservices.com"},
		{"https://api.minecraftservices.com", "https://api.minecraftservices.com"},
		{"https://api.minecraftservices.com/", "https://api.minecraftservices.com"},
		{"https://api.mojang.com", "https://api.mojang.com"},
		{"https://127.0.0.1:8443", "https://127.0.0.1:8443"},
		{"https://[::1]:8443", "https://[::1]:8443"},
		{"http://api.mojang.com", ""},
		{"api.mojang.com", ""},
		{"ftp://api.mojang.com", ""},
		{"https://api.mojang.com:8443", ""},
		{"https://api.mojang.com/users/profiles", ""},
		{"https://api.mojang.com?x=1", ""},
		{"https://api.mojang.com#x", ""},
		{"https://someone@api.mojang.com", ""},
		{"https://api.mojang.com.evil.example", ""},
		{"https://evil.example", ""},
		{"https://localhost:8443", ""},
		{"https://10.0.0.1", ""},
	} {
		c, err := NewClient(Options{BaseURL: tc.raw})
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("%q accepted", tc.raw)
		case tc.want != "" && err != nil:
			t.Errorf("%q refused: %v", tc.raw, err)
		case tc.want != "" && c.base != tc.want:
			t.Errorf("%q became %q, want %q", tc.raw, c.base, tc.want)
		}
	}
}

func TestNewClientLeavesCallersClientAlone(t *testing.T) {
	mine := &http.Client{}
	c, err := NewClient(Options{HTTP: mine})
	if err != nil {
		t.Fatal(err)
	}
	if mine.CheckRedirect != nil || mine.Timeout != 0 {
		t.Error("NewClient changed the caller's http.Client")
	}
	if c.http.CheckRedirect == nil || c.http.Timeout != 10*time.Second {
		t.Error("the client's copy should refuse redirects and time out")
	}
}

func TestNormalizeUUID(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"069a79f444e94726a5befca90e38aaf5", "069a79f4-44e9-4726-a5be-fca90e38aaf5"},
		{"069A79F444E94726A5BEFCA90E38AAF5", "069a79f4-44e9-4726-a5be-fca90e38aaf5"},
		{"069a79f4-44e9-4726-a5be-fca90e38aaf5", "069a79f4-44e9-4726-a5be-fca90e38aaf5"},
		{"069a79f4-44e94726-a5be-fca90e38aaf5-", ""},
		{"069a79f444e94726a5befca90e38aaf", ""},
		{"069a79f444e94726a5befca90e38aaf5a", ""},
		{"069a79f444e94726a5befca90e38aag5", ""},
		{"+69a79f444e94726a5befca90e38aaf5", ""},
		{"", ""},
	} {
		got, ok := NormalizeUUID(tc.in)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("NormalizeUUID(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	e := &Error{Err: ErrUnavailable, Detail: "HTTP 500"}
	if got := e.Error(); got != "Mojang's account service could not be reached or gave an unexpected answer (HTTP 500)." {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(e, ErrUnavailable) || errors.Is(e, ErrNotFound) {
		t.Error("errors.Is does not see the kind")
	}
}
