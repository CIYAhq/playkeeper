package fetch

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostsAllowOnlyHTTPSOnListedHosts(t *testing.T) {
	h := Hosts{"cdn.modrinth.com", "127.0.0.1:8443"}
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://cdn.modrinth.com/data/x.jar", true},
		{"https://CDN.Modrinth.com/data/x.jar", true},
		{"https://cdn.modrinth.com:443/data/x.jar", true},
		{"https://127.0.0.1:8443/x.jar", true},
		{"http://cdn.modrinth.com/data/x.jar", false},
		{"https://cdn.modrinth.com.evil.example/x.jar", false},
		{"https://evil.example/cdn.modrinth.com/x.jar", false},
		{"https://user:pw@cdn.modrinth.com/x.jar", false},
		{"https://cdn.modrinth.com:8443/x.jar", false},
		{"https://127.0.0.1/x.jar", false},
		{"ftp://cdn.modrinth.com/x.jar", false},
		{"//cdn.modrinth.com/x.jar", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Allows(u); got != c.want {
			t.Errorf("Allows(%s) = %v, want %v", c.raw, got, c.want)
		}
		_, err = h.Check(c.raw)
		var he *HostError
		if c.want != (err == nil) || (err != nil && !errors.As(err, &he)) {
			t.Errorf("Check(%s) = %v", c.raw, err)
		}
	}
	if _, err := h.Check("http://cdn.modrinth.com/x.jar"); err == nil || !strings.Contains(err.Error(), "not an HTTPS address") {
		t.Errorf("plain HTTP refusal should say so, got %v", err)
	}
}

type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

func newAPI(t *testing.T, h http.HandlerFunc, maxWait time.Duration) (*API, *fakeClock, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	clk := &fakeClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	a := New("Modrinth", Options{BaseURL: srv.URL + "/v2", UserAgent: "test-agent/1", HTTP: srv.Client(), Now: clk.Now, Sleep: clk.Sleep, MaxWait: maxWait})
	return a, clk, &hits
}

func TestGetSendsUserAgentAndDecodes(t *testing.T) {
	a, _, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/project/chunky" || r.URL.Query().Get("x") != "[\"a b\"]" {
			t.Errorf("request %s", r.URL)
		}
		if r.Header.Get("User-Agent") != "test-agent/1" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers %v", r.Header)
		}
		w.Write([]byte(`{"id":"fALzjamp"}`))
	}, 0)
	var v struct{ ID string }
	if err := a.Get(context.Background(), "/project/chunky", url.Values{"x": {`["a b"]`}}, &v); err != nil || v.ID != "fALzjamp" {
		t.Fatalf("Get = %+v, %v", v, err)
	}
}

func TestPostSendsJSON(t *testing.T) {
	a, _, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %v", r.Method, r.Header)
		}
		b := make([]byte, 100)
		n, _ := r.Body.Read(b)
		if string(b[:n]) != `{"hashes":["ab"],"algorithm":"sha512"}` {
			t.Errorf("body %s", b[:n])
		}
		w.Write([]byte(`{}`))
	}, 0)
	body := struct {
		Hashes    []string `json:"hashes"`
		Algorithm string   `json:"algorithm"`
	}{[]string{"ab"}, "sha512"}
	if err := a.Post(context.Background(), "/version_files", body, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorAnswersCarryTheServiceExplanation(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		notFound bool
		detail   string
	}{
		{404, `{"message":"Unknown value 'x' for path variable 'slugOrId'","isHangarApiException":true}`, true, "Unknown value 'x' for path variable 'slugOrId'"},
		{400, `{"error":"request_error","description":"searching projects"}`, false, "searching projects"},
		{500, `<html>oops</html>`, false, ""},
		{400, `{"description":"line\nbreak\u0000"}`, false, "line break"},
	}
	for _, c := range cases {
		a, _, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}, 0)
		err := a.Get(context.Background(), "/search", nil, &struct{}{})
		var se *StatusError
		if !errors.As(err, &se) || se.Status != c.status || se.Detail != c.detail || se.Path != "/search" {
			t.Errorf("status %d: err = %#v", c.status, err)
			continue
		}
		if errors.Is(err, ErrNotFound) != c.notFound {
			t.Errorf("status %d: errors.Is(ErrNotFound) = %v", c.status, !c.notFound)
		}
	}
}

func TestAnswersAboveTheLimitAreRefused(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", 2048) + `"}`
	for _, chunked := range []bool{false, true} {
		a, _, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
			if chunked {
				w.Write([]byte(big[:10]))
				w.(http.Flusher).Flush()
				w.Write([]byte(big[10:]))
				return
			}
			w.Write([]byte(big))
		}, 0)
		a.o.MaxBody = 1024
		var tl *TooLargeError
		if err := a.Get(context.Background(), "/x", nil, &map[string]any{}); !errors.As(err, &tl) {
			t.Errorf("chunked=%v: err = %v, want TooLargeError", chunked, err)
		}
	}
}

func TestRateLimitWaitsBrieflyThenRetriesOnce(t *testing.T) {
	calls := 0
	a, clk, hits := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-Ratelimit-Limit", "300")
			w.Header().Set("X-Ratelimit-Remaining", "0")
			w.Header().Set("X-Ratelimit-Reset", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{}`))
	}, 10*time.Second)
	if err := a.Get(context.Background(), "/search", nil, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if *hits != 2 || len(clk.slept) != 1 || clk.slept[0] != 2*time.Second {
		t.Fatalf("hits %d, slept %v; want a 2s pause and one retry", *hits, clk.slept)
	}
}

func TestLongRateLimitIsReportedAndHonoured(t *testing.T) {
	a, clk, hits := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}, 10*time.Second)
	err := a.Get(context.Background(), "/search", nil, nil)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second || rl.Service != "Modrinth" {
		t.Fatalf("err = %v, want a 120s RateLimitError", err)
	}
	if !strings.Contains(err.Error(), "try again in 2m0s") {
		t.Errorf("message %q", err)
	}
	if err := a.Get(context.Background(), "/search", nil, nil); !errors.As(err, &rl) || *hits != 1 {
		t.Fatalf("second call: err %v after %d requests; want an immediate refusal without a request", err, *hits)
	}
	clk.Sleep(context.Background(), 121*time.Second)
	a.Get(context.Background(), "/search", nil, nil)
	if *hits != 2 {
		t.Fatalf("after the pause the client should ask again, hits = %d", *hits)
	}
}

func TestRepeated429GivesUpAfterOneRetry(t *testing.T) {
	a, _, hits := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}, time.Minute)
	var rl *RateLimitError
	if err := a.Get(context.Background(), "/x", nil, nil); !errors.As(err, &rl) || rl.RetryAfter != 5*time.Second {
		t.Fatalf("err = %v, want RateLimitError with the 5s default", err)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2", *hits)
	}
}

func TestExhaustedWindowHoldsTheNextRequest(t *testing.T) {
	a, clk, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "3")
		w.Write([]byte(`{}`))
	}, 10*time.Second)
	a.Get(context.Background(), "/a", nil, nil)
	if len(clk.slept) != 0 {
		t.Fatalf("slept before the first request: %v", clk.slept)
	}
	a.Get(context.Background(), "/b", nil, nil)
	if len(clk.slept) != 1 || clk.slept[0] != 3*time.Second {
		t.Fatalf("slept %v, want one 3s pause before the second request", clk.slept)
	}
}

func TestAPIRefusesRedirectsToOtherHosts(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target was contacted")
	}))
	defer other.Close()
	a, _, _ := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v2/x", http.StatusFound)
	}, 0)
	var re *RedirectError
	if err := a.Get(context.Background(), "/x", nil, nil); !errors.As(err, &re) {
		t.Fatalf("err = %v, want RedirectError", err)
	}
}

func TestAPISendsItsHeaderOnlyToItsOwnHost(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect target was contacted with key %q", r.Header.Get("X-Api-Key"))
	}))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key-123" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/v1/away" {
			http.Redirect(w, r, other.URL+"/v1/x", http.StatusFound)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	h := http.Header{"X-Api-Key": {"test-key-123"}}
	a := New("CurseForge", Options{BaseURL: srv.URL + "/v1", UserAgent: "ua", HTTP: srv.Client(), Header: h})
	if err := a.Get(context.Background(), "/mods/1", nil, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 {
		t.Errorf("the caller's header was changed: %v", h)
	}
	var re *RedirectError
	err := a.Get(context.Background(), "/away", nil, nil)
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RedirectError", err)
	}
	if strings.Contains(err.Error(), "test-key-123") {
		t.Errorf("the key leaked into %q", err)
	}
}

func TestAPIRequiresHTTPS(t *testing.T) {
	a := New("Hangar", Options{BaseURL: "http://hangar.papermc.io/api/v1"})
	var he *HostError
	if err := a.Get(context.Background(), "/projects", nil, nil); !errors.As(err, &he) {
		t.Fatalf("err = %v, want HostError", err)
	}
}

func cdn(t *testing.T, h http.HandlerFunc) (*httptest.Server, Hosts) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return srv, Hosts{u.Host}
}

func sums(b []byte) (string, string) {
	s5 := sha512.Sum512(b)
	s2 := sha256.Sum256(b)
	return hex.EncodeToString(s5[:]), hex.EncodeToString(s2[:])
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		t.Errorf("left behind: %s", e.Name())
	}
}

func TestDownloadVerifiesSizeAndHash(t *testing.T) {
	body := []byte("PK\x03\x04 a jar")
	s512, s256 := sums(body)
	srv, hosts := cdn(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "ua" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		w.Write(body)
	})
	for _, want := range []Want{
		{Algo: "sha512", Hash: s512, Size: int64(len(body)), Max: 1 << 20},
		{Algo: "sha256", Hash: strings.ToUpper(s256), Max: 1 << 20},
	} {
		dir := t.TempDir()
		p, err := Download(context.Background(), srv.Client(), hosts, "ua", srv.URL+"/x.jar", dir, want)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != string(body) {
			t.Fatalf("content %q", got)
		}
	}
}

func TestDownloadRefusalsLeaveNothingBehind(t *testing.T) {
	body := []byte("PK\x03\x04 a jar")
	s512, _ := sums(body)
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer other.Close()
	srv, hosts := cdn(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tampered.jar":
			w.Write([]byte("PK\x03\x04 a jaR"))
		case "/redirect.jar":
			http.Redirect(w, r, other.URL+"/x.jar", http.StatusFound)
		case "/chunked.jar":
			w.Write(body[:4])
			w.(http.Flusher).Flush()
			w.Write(body[4:])
		case "/gone.jar":
			http.NotFound(w, r)
		default:
			w.Write(body)
		}
	})
	n := int64(len(body))
	cases := []struct {
		name string
		url  string
		want Want
		as   any
	}{
		{"tampered", srv.URL + "/tampered.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: 1 << 20}, new(*HashError)},
		{"wrong size", srv.URL + "/x.jar", Want{Algo: "sha512", Hash: s512, Size: n - 1, Max: 1 << 20}, new(*SizeError)},
		{"declared too large", srv.URL + "/x.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: n - 1}, new(*TooLargeError)},
		{"body too large", srv.URL + "/chunked.jar", Want{Algo: "sha512", Hash: s512, Max: n - 1}, new(*TooLargeError)},
		{"redirect", srv.URL + "/redirect.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: 1 << 20}, new(*RedirectError)},
		{"other host", other.URL + "/x.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: 1 << 20}, new(*HostError)},
		{"plain http", strings.Replace(srv.URL, "https", "http", 1) + "/x.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: 1 << 20}, new(*HostError)},
		{"missing", srv.URL + "/gone.jar", Want{Algo: "sha512", Hash: s512, Size: n, Max: 1 << 20}, new(*StatusError)},
	}
	for _, c := range cases {
		dir := t.TempDir()
		_, err := Download(context.Background(), srv.Client(), hosts, "ua", c.url, dir, c.want)
		if err == nil || !errors.As(err, c.as) {
			t.Errorf("%s: err = %v, want %T", c.name, err, c.as)
		}
		assertEmpty(t, dir)
	}
	if _, err := Download(context.Background(), srv.Client(), hosts, "ua", srv.URL+"/x.jar", t.TempDir(), Want{Algo: "md5", Hash: "00", Max: 10}); err == nil {
		t.Error("an unsupported hash algorithm was accepted")
	}
	if _, err := Download(context.Background(), srv.Client(), hosts, "ua", srv.URL+"/x.jar", t.TempDir(), Want{Algo: "sha512", Max: 1 << 20}); err == nil {
		t.Error("a download without a hash was accepted")
	}
}

func TestDownloadChecksEveryListedHash(t *testing.T) {
	body := []byte("PK\x03\x04 a jar")
	s512, _ := sums(body)
	s1 := sha1.Sum(body)
	good := hex.EncodeToString(s1[:])
	bad := strings.Repeat("0", 40)
	srv, hosts := cdn(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	get := func(also ...Sum) (string, string, error) {
		dir := t.TempDir()
		p, err := Download(context.Background(), srv.Client(), hosts, "ua", srv.URL+"/x.jar", dir, Want{Algo: "sha512", Hash: s512, Also: also, Max: 1 << 20})
		return dir, p, err
	}
	if _, _, err := get(Sum{"sha1", strings.ToUpper(good)}); err != nil {
		t.Fatalf("both hashes match: %v", err)
	}
	dir, _, err := get(Sum{"sha1", bad})
	var he *HashError
	if !errors.As(err, &he) || he.Algo != "sha1" || he.Got != good {
		t.Fatalf("err = %#v, want a sha1 HashError", err)
	}
	assertEmpty(t, dir)
	if _, _, err := get(Sum{"sha1", "abc"}); err == nil {
		t.Error("a malformed second hash was accepted")
	}
}
