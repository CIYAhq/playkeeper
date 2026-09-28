package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

const testToken = "0123456789abcdef0123456789abcdef-read"

type env struct {
	t   *testing.T
	dir string
	svc *Service
	h   http.Handler
	log *syncBuffer

	mu  sync.Mutex
	now time.Time
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

// newEnv is a service at 2026-10-01 12:34 UTC that trusts a proxy at
// 10.0.1.5, with the read token set unless change says otherwise.
func newEnv(t *testing.T, change ...func(*Config)) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir(), log: &syncBuffer{}, now: time.Date(2026, 10, 1, 12, 34, 0, 0, time.UTC)}
	cfg := Config{DataDir: e.dir, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.1.5/32")}, ReadToken: testToken,
		Log: slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: e.clock}
	for _, c := range change {
		c(&cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	e.svc, e.h = svc, svc.Handler()
	return e
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// do sends a request from the address from, through the trusted proxy when
// from isn't a proxy.
func (e *env) do(method, path, from string, body any, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		r = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = "10.0.1.5:40000"
	req.Header.Set("X-Forwarded-For", from)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func id(n int) string { return fmt.Sprintf("%032x", n) }

func system(source string) usage.System {
	return usage.System{Version: "0.4.4", OS: "ubuntu", OSVersion: "24.04", Arch: "amd64", Source: source, Kind: usage.KindDashboard}
}

func beat(n int, source string, servers, running int) usage.Heartbeat {
	return usage.Heartbeat{ID: id(n), System: system(source), Address: usage.AddressIP, Servers: servers, Running: running}
}

func event(n int, ev, source, step string) usage.Install {
	return usage.Install{ID: id(n), Event: ev, Step: step, System: system(source)}
}

func (e *env) send(path string, report any) {
	e.t.Helper()
	if w := e.do("POST", path, "198.51.100.7", report); w.Code != http.StatusNoContent {
		e.t.Fatalf("POST %s answered %d: %s", path, w.Code, w.Body)
	}
}

func (e *env) summary() *Summary {
	e.t.Helper()
	w := e.do("GET", "/v1/summary", "198.51.100.8", nil, "Authorization", "Bearer "+testToken)
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /v1/summary answered %d: %s", w.Code, w.Body)
	}
	var s Summary
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		e.t.Fatal(err)
	}
	return &s
}

// storedBytes is everything the service wrote to disk.
func (e *env) storedBytes() []byte {
	e.t.Helper()
	e.svc.backup(e.clock())
	var all []byte
	filepath.Walk(e.dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			all = append(all, b...)
		}
		return nil
	})
	if len(all) == 0 {
		e.t.Fatal("the service wrote nothing")
	}
	return all
}

// The address a report comes from decides the rate limit and is dropped,
// with every header; so is any field the report types don't have.
func TestReportsAreKeptWithoutTheirAddressHeadersOrExtraFields(t *testing.T) {
	e := newEnv(t)
	addrs := []string{"203.0.113.77", "2001:db8:77:1::7"}
	for i, a := range addrs {
		h := beat(i+1, usage.SourceSite, 2, 1)
		b, _ := json.Marshal(h)
		extra := strings.Replace(string(b), "{", `{"hostname":"alice-vps","players":["Notch"],`, 1)
		if w := e.do("POST", usage.PathHeartbeat, a, extra, "User-Agent", "playkeeper/0.4.4 alice-agent", "Cookie", "session=alice-cookie"); w.Code != http.StatusNoContent {
			t.Fatalf("a heartbeat with fields the service doesn't keep answered %d: %s", w.Code, w.Body)
		}
		if w := e.do("POST", usage.PathInstall, a, event(i+1, usage.EventSucceeded, usage.SourceSite, "")); w.Code != http.StatusNoContent {
			t.Fatalf("an install event answered %d: %s", w.Code, w.Body)
		}
	}
	stored := e.storedBytes()
	for _, secret := range append(addrs, "2001:db8:77:1::", "203.0.113", "alice", "Notch", "session=") {
		if bytes.Contains(stored, []byte(secret)) {
			t.Errorf("the service's files contain %q", secret)
		}
		if strings.Contains(e.log.String(), secret) {
			t.Errorf("the service's log contains %q", secret)
		}
	}
	if !bytes.Contains(stored, []byte(id(1))) {
		t.Fatal("the stored files don't have the install at all, so the check above proves nothing")
	}
	sum := e.summary()
	if a := sum.Active["1d"]; a.Installs != 2 || a.Servers != 4 || a.Running != 2 {
		t.Errorf("active: %+v", a)
	}
}

func TestSummaryCountsInstallsAndActiveInstallsByWindow(t *testing.T) {
	e := newEnv(t)
	start := e.clock()
	// 40 days ago: an install from playkeeper.io that succeeded and ran
	// for a while, then went quiet.
	e.advance(-40 * day)
	e.send(usage.PathInstall, event(1, usage.EventStarted, usage.SourceSite, ""))
	e.send(usage.PathInstall, event(1, usage.EventSucceeded, usage.SourceSite, ""))
	e.send(usage.PathHeartbeat, beat(1, usage.SourceSite, 1, 1))
	// 10 days ago: one from GitHub's get.sh that failed at its services,
	// one refused for memory and its port, and one still running.
	e.advance(30 * day)
	e.send(usage.PathInstall, event(2, usage.EventStarted, usage.SourceGitHub, ""))
	e.send(usage.PathInstall, event(2, usage.EventFailed, usage.SourceGitHub, "services"))
	e.send(usage.PathInstall, event(3, usage.EventRefused, usage.SourceGitHub, "memory+port"))
	e.send(usage.PathInstall, event(4, usage.EventStarted, usage.SourceTarball, ""))
	e.send(usage.PathInstall, event(4, usage.EventSucceeded, usage.SourceTarball, ""))
	hb := beat(4, usage.SourceTarball, 3, 2)
	hb.OS, hb.OSVersion, hb.Arch, hb.Address = "debian", "13", "arm64", usage.AddressOwn
	e.send(usage.PathHeartbeat, hb)
	// Today: one from a creator's playkeeper.io/install/cygnus that
	// succeeded, a joined machine, one that started three hours ago and
	// never said more, and an install from before 0.4.4 that only sends
	// heartbeats.
	e.advance(10*day - 3*time.Hour)
	e.send(usage.PathInstall, event(5, usage.EventStarted, usage.SourceSite, ""))
	e.advance(3 * time.Hour)
	in := event(6, usage.EventSucceeded, usage.SourceSite, "")
	in.Channel = "cygnus"
	e.send(usage.PathInstall, in)
	hb = beat(6, usage.SourceSite, 0, 0)
	hb.Channel, hb.Address = "cygnus", usage.AddressFree
	e.send(usage.PathHeartbeat, hb)
	joined := beat(7, usage.SourceSite, 12, 3)
	joined.Kind = usage.KindJoined
	e.send(usage.PathHeartbeat, joined)
	old := beat(8, "", 2, 2)
	old.Version = "0.4.3"
	e.send(usage.PathHeartbeat, old)
	e.send(usage.PathHeartbeat, beat(4, usage.SourceTarball, 3, 2))
	if !e.clock().Equal(start) {
		t.Fatalf("the clock is at %v", e.clock())
	}

	s := e.summary()
	want := map[string]Installs{
		"1d":  {Outcomes: Outcomes{Started: 2, Succeeded: 1}, Unfinished: 1},
		"7d":  {Outcomes: Outcomes{Started: 2, Succeeded: 1}, Unfinished: 1},
		"30d": {Outcomes: Outcomes{Started: 4, Succeeded: 2, Failed: 1, Refused: 1}, Unfinished: 1},
		"all": {Outcomes: Outcomes{Started: 5, Succeeded: 3, Failed: 1, Refused: 1}, Unfinished: 1},
	}
	for w, x := range want {
		got := s.Installs[w]
		if got == nil || got.Outcomes != x.Outcomes || got.Unfinished != x.Unfinished {
			t.Errorf("installs %s: %+v, want %+v", w, got, x)
		}
	}
	m := s.Installs["30d"]
	if m.BySource[usage.SourceGitHub] != (Outcomes{Started: 1, Failed: 1, Refused: 1}) || m.BySource[usage.SourceSite] != (Outcomes{Started: 2, Succeeded: 1}) ||
		m.FailedSteps["services"] != 1 || m.RefusedChecks["memory"] != 1 || m.RefusedChecks["port"] != 1 || m.ByChannel["cygnus"] != (Outcomes{Started: 1, Succeeded: 1}) {
		t.Errorf("installs 30d by source, step and channel: %+v", m)
	}

	a := s.Active["1d"]
	if a.Installs != 4 || a.OnOurDomain != 2 || a.OffOurDomain != 1 || a.UnknownSource != 1 {
		t.Errorf("active 1d: %+v", a)
	}
	if a.ByKind[usage.KindJoined] != 1 || a.ByKind[usage.KindDashboard] != 3 || a.ByVersion["0.4.3"] != 1 || a.ByChannel["cygnus"] != 1 || a.BySource["unknown"] != 1 {
		t.Errorf("active 1d by kind, version, channel and source: %+v", a)
	}
	if a.Servers != 17 || a.Running != 7 || a.ServersPerInstall["0"] != 1 || a.ServersPerInstall["2"] != 1 || a.ServersPerInstall["3-5"] != 1 || a.ServersPerInstall["11+"] != 1 {
		t.Errorf("active 1d servers: %+v", a)
	}
	if a.ByOS["ubuntu 24.04"] != 4 || a.ByArch["amd64"] != 4 || a.ByAddress[usage.AddressFree] != 1 || a.ByAddress[usage.AddressIP] != 3 {
		t.Errorf("each install is described by its last heartbeat: %+v", a)
	}
	if s.Active["30d"].Installs != 4 || s.Active["7d"].Installs != 4 {
		t.Errorf("the install that went quiet 40 days ago is still active: 7d %d, 30d %d", s.Active["7d"].Installs, s.Active["30d"].Installs)
	}

	if len(s.Daily) != dailyDays || s.Daily[dailyDays-1].Day != "2026-10-01" || s.Daily[0].Day != "2026-09-02" {
		t.Fatalf("daily: %d days, from %s to %s", len(s.Daily), s.Daily[0].Day, s.Daily[len(s.Daily)-1].Day)
	}
	today, tenAgo := s.Daily[dailyDays-1], s.Daily[dailyDays-11]
	if today.Active != 4 || today.Started != 1 || today.Succeeded != 1 || tenAgo.Active != 1 || tenAgo.Started != 2 || tenAgo.Succeeded != 1 {
		t.Errorf("daily: today %+v, ten days ago %+v", today, tenAgo)
	}
	if strings.Contains(e.do("GET", "/v1/summary", "198.51.100.8", nil, "Authorization", "Bearer "+testToken).Body.String(), id(1)) {
		t.Error("the summary shows an install ID")
	}
}

// An install from within the last day counts in it, though the service
// keeps its time only to the hour.
func TestAnInstallFromTheLastDayCountsInIt(t *testing.T) {
	e := newEnv(t)
	e.advance(-day + 10*time.Minute)
	e.send(usage.PathInstall, event(1, usage.EventStarted, usage.SourceSite, ""))
	e.send(usage.PathInstall, event(1, usage.EventSucceeded, usage.SourceSite, ""))
	e.advance(day - 10*time.Minute)
	if in := e.summary().Installs["1d"]; in.Started != 1 || in.Succeeded != 1 {
		t.Errorf("an install 23 h 50 min ago: %+v", in)
	}
}

// The project's own test installs never count; the summary says how many
// there were, which a working setup keeps at zero.
func TestTestInstallsAreKeptOutOfEveryCount(t *testing.T) {
	e := newEnv(t)
	ci := event(1, usage.EventStarted, usage.SourceGitHub, "")
	ci.Test = true
	e.send(usage.PathInstall, ci)
	ci.Event = usage.EventSucceeded
	e.send(usage.PathInstall, ci)
	hb := beat(1, usage.SourceGitHub, 1, 1)
	e.send(usage.PathHeartbeat, hb) // a later heartbeat that forgot the mark keeps it
	real := beat(2, usage.SourceSite, 1, 0)
	e.send(usage.PathHeartbeat, real)
	s := e.summary()
	if s.Installs["1d"].Started != 0 || s.Active["1d"].Installs != 1 || s.Active["1d"].OnOurDomain != 1 || s.Daily[dailyDays-1].Active != 1 {
		t.Errorf("a test install counted: installs %+v, active %+v, today %+v", s.Installs["1d"], s.Active["1d"], s.Daily[dailyDays-1])
	}
	if s.Test != (TestInstalls{Started30d: 1, Active7d: 1}) {
		t.Errorf("test installs: %+v", s.Test)
	}
}

func TestReportsThatArentOneAreRefused(t *testing.T) {
	e := newEnv(t)
	good, _ := json.Marshal(beat(1, usage.SourceSite, 1, 1))
	for _, c := range []struct {
		name, body, contentType string
		code                    int
		errCode                 string
	}{
		{"not JSON", "servers=1", "application/json", 400, "invalid_request"},
		{"two reports", string(good) + string(good), "application/json", 400, "invalid_request"},
		{"form", string(good), "application/x-www-form-urlencoded", 415, "unsupported_media_type"},
		{"too large", strings.Replace(string(good), "{", `{"pad":"`+strings.Repeat("x", usage.MaxBody)+`",`, 1), "application/json", 413, "too_large"},
		{"a host name for the system", strings.Replace(string(good), `"os":"ubuntu"`, `"os":"alice.example.com/x"`, 1), "application/json", 400, "invalid_request"},
		{"no ID", strings.Replace(string(good), id(1), "", 1), "application/json", 400, "invalid_request"},
	} {
		req := httptest.NewRequest("POST", usage.PathHeartbeat, strings.NewReader(c.body))
		req.RemoteAddr = "198.51.100.9:1234"
		req.Header.Set("Content-Type", c.contentType)
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, req)
		var body apiError
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != c.code || body.Code != c.errCode {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "alice") {
			t.Errorf("%s: the refusal repeats the value: %s", c.name, w.Body)
		}
	}
	if w := e.do("GET", usage.PathHeartbeat, "198.51.100.9", nil); w.Code != 405 {
		t.Errorf("GET of the heartbeat path answered %d", w.Code)
	}
	if w := e.do("GET", "/v1/installs", "198.51.100.9", nil); w.Code != 404 {
		t.Errorf("an unknown path answered %d", w.Code)
	}
	if s := e.summary(); s.Active["30d"].Installs != 0 {
		t.Errorf("a refused report was kept: %+v", s.Active["30d"])
	}
}

func TestEachAddressAndEachInstallHasALimit(t *testing.T) {
	e := newEnv(t)
	for i := range requestsPerIPBurst {
		if w := e.do("POST", usage.PathHeartbeat, "198.51.100.10", beat(100+i, usage.SourceSite, 0, 0)); w.Code != http.StatusNoContent {
			t.Fatalf("request %d from one address answered %d", i+1, w.Code)
		}
	}
	w := e.do("POST", usage.PathHeartbeat, "198.51.100.10", beat(999, usage.SourceSite, 0, 0))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("one request too many from one address answered %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// Another address behind the same proxy has its own limit, and so does
	// another IPv6 /64.
	if w := e.do("POST", usage.PathHeartbeat, "198.51.100.11", beat(999, usage.SourceSite, 0, 0)); w.Code != http.StatusNoContent {
		t.Errorf("another address answered %d", w.Code)
	}
	for i := range reportsPerIDBurst {
		if w := e.do("POST", usage.PathHeartbeat, fmt.Sprintf("2001:db8:%x::1", i), beat(1, usage.SourceSite, 0, 0)); w.Code != http.StatusNoContent {
			t.Fatalf("report %d for one install answered %d", i+1, w.Code)
		}
	}
	if w := e.do("POST", usage.PathHeartbeat, "2001:db8:ff::1", beat(1, usage.SourceSite, 0, 0)); w.Code != http.StatusTooManyRequests {
		t.Errorf("one report too many for one install answered %d", w.Code)
	}
	e.advance(time.Hour)
	if w := e.do("POST", usage.PathHeartbeat, "198.51.100.10", beat(1000, usage.SourceSite, 0, 0)); w.Code != http.StatusNoContent {
		t.Errorf("an hour later, the address answered %d", w.Code)
	}
}

// Made-up IDs can't fill the disk: new ones stop at the day's number, while
// installs the service knows keep reporting.
func TestNewInstallsPerDayAreBounded(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.NewPerDay = 3 })
	for i := range 3 {
		e.send(usage.PathHeartbeat, beat(i+1, usage.SourceSite, 0, 0))
	}
	if w := e.do("POST", usage.PathHeartbeat, "198.51.100.20", beat(4, usage.SourceSite, 0, 0)); w.Code != http.StatusTooManyRequests {
		t.Errorf("a fourth new install answered %d", w.Code)
	}
	if w := e.do("POST", usage.PathInstall, "198.51.100.21", event(5, usage.EventStarted, usage.SourceSite, "")); w.Code != http.StatusTooManyRequests {
		t.Errorf("a new install's first event answered %d", w.Code)
	}
	if w := e.do("POST", usage.PathHeartbeat, "198.51.100.22", beat(2, usage.SourceSite, 1, 0)); w.Code != http.StatusNoContent {
		t.Errorf("a known install answered %d", w.Code)
	}
	e.advance(day)
	if w := e.do("POST", usage.PathHeartbeat, "198.51.100.23", beat(4, usage.SourceSite, 0, 0)); w.Code != http.StatusNoContent {
		t.Errorf("the next day, a new install answered %d", w.Code)
	}
}

// Only the proxy the settings name may say whom a request is from, so
// nobody can pick an address to get around the limit.
func TestOnlyATrustedProxyNamesTheClient(t *testing.T) {
	e := newEnv(t)
	for i := range requestsPerIPBurst {
		req := httptest.NewRequest("GET", "/v1/summary", nil)
		req.RemoteAddr = "198.51.100.30:5000"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
		e.h.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest("GET", "/v1/summary", nil)
	req.RemoteAddr = "198.51.100.30:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.250")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("an untrusted peer's X-Forwarded-For moved it to another limit: %d", w.Code)
	}
	// Behind the proxy, a client can put made-up addresses in front of the
	// one the proxy saw; only the proxy's counts.
	for i := range requestsPerIPBurst {
		e.do("GET", "/v1/summary", fmt.Sprintf("192.0.2.%d, 198.51.100.32", i), nil)
	}
	if w := e.do("GET", "/v1/summary", "192.0.2.250, 198.51.100.32", nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("addresses a client made up moved it to another limit: %d", w.Code)
	}
	if w := e.do("GET", "/healthz", "198.51.100.31", nil); w.Code != 200 || w.Body.String() != "ok\n" {
		t.Errorf("/healthz: %d %q", w.Code, w.Body)
	}
	// A private peer passing a client address it may not name is a proxy
	// the settings miss: the log says so, without the address.
	private := httptest.NewRequest("GET", "/v1/summary", nil)
	private.RemoteAddr = "10.0.9.9:5000"
	private.Header.Set("X-Forwarded-For", "203.0.113.251")
	e.h.ServeHTTP(httptest.NewRecorder(), private)
	if l := e.log.String(); !strings.Contains(l, EnvTrustedProxies) || strings.Contains(l, "203.0.113.251") || strings.Contains(l, "10.0.9.9") {
		t.Errorf("the log about an unlisted proxy: %s", l)
	}
}

func TestTheCountsNeedTheReadToken(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"", "Bearer ", "Bearer wrong-token-wrong-token-wrong-token", "Basic " + testToken, testToken} {
		w := e.do("GET", "/v1/summary", "198.51.100.40", nil, "Authorization", auth)
		if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("Authorization %q answered %d", auth, w.Code)
		}
	}
	closed := newEnv(t, func(c *Config) { c.ReadToken = "" })
	if w := closed.do("GET", "/v1/summary", "198.51.100.41", nil, "Authorization", "Bearer "); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), EnvReadToken) {
		t.Errorf("without a read token set: %d %s", w.Code, w.Body)
	}
	if e.summary().GeneratedAt.IsZero() {
		t.Error("the summary has no time")
	}
	if w := e.do("GET", "/", "198.51.100.42", nil); !strings.Contains(w.Body.String(), "#usage-stats") || !strings.Contains(w.Body.String(), "AGPL-3.0") {
		t.Errorf("/ doesn't say what's collected or link the source: %s", w.Body)
	}
}

func TestSettingsComeFromTheEnvironmentWithoutShowingTheToken(t *testing.T) {
	cfg, err := FromEnv(func(k string) string {
		return map[string]string{EnvTrustedProxies: "10.0.1.5, fd00::7", EnvReadToken: testToken, EnvNewPerDay: "100"}[k]
	})
	if err != nil || cfg.DataDir != DefaultDataDir || cfg.Listen != DefaultListen || len(cfg.TrustedProxies) != 2 || cfg.ReadToken != testToken || cfg.NewPerDay != 100 {
		t.Fatalf("%+v %v", cfg, err)
	}
	short := "too-short-a-token"
	_, err = FromEnv(func(k string) string {
		return map[string]string{EnvReadToken: short, EnvTrustedProxies: "0.0.0.0/0", EnvNewPerDay: "0", EnvDataDir: "data"}[k]
	})
	for _, name := range []string{EnvReadToken, EnvTrustedProxies, EnvNewPerDay, EnvDataDir} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("a bad %s: %v", name, err)
		}
	}
	if err != nil && strings.Contains(err.Error(), short) {
		t.Errorf("the error shows the token: %v", err)
	}
}

func TestOldInstallsAreForgottenAndSnapshotsKeptForAWeek(t *testing.T) {
	e := newEnv(t)
	e.send(usage.PathHeartbeat, beat(1, usage.SourceSite, 1, 1))
	e.advance(keepFor - day)
	e.send(usage.PathHeartbeat, beat(2, usage.SourceSite, 1, 1))
	e.advance(2 * day)
	e.svc.prune(context.Background(), e.clock())
	var n, days int
	e.svc.db.QueryRow(`SELECT COUNT(*) FROM installs`).Scan(&n)
	e.svc.db.QueryRow(`SELECT COUNT(*) FROM active_days`).Scan(&days)
	if n != 1 || days != 1 {
		t.Errorf("after 400 days, %d installs and %d days are left, want 1 and 1", n, days)
	}
	for range 9 {
		e.svc.backup(e.clock())
		e.advance(day)
	}
	snaps, _ := filepath.Glob(filepath.Join(e.dir, "backups", "stats-*.db"))
	if len(snaps) != keepBackups {
		t.Errorf("%d snapshots kept, want %d", len(snaps), keepBackups)
	}
}

// A snapshot that couldn't be written is tried again the next hour, not the
// next day.
func TestAFailedSnapshotIsTriedAgainTheNextHour(t *testing.T) {
	e := newEnv(t)
	blocker := filepath.Join(e.dir, "backups")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.svc.backup(e.clock())
	if !strings.Contains(e.log.String(), "backups directory") {
		t.Fatalf("the failure wasn't logged: %s", e.log.String())
	}
	os.Remove(blocker)
	e.advance(time.Hour)
	e.svc.backup(e.clock())
	if snaps, _ := filepath.Glob(filepath.Join(e.dir, "backups", "stats-*.db")); len(snaps) != 1 {
		t.Errorf("an hour after a failed snapshot, %d snapshots", len(snaps))
	}
}
