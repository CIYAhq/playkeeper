package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

const testOAKey = "oa_rk_stats-test-key-0123456789"

// fakeOA answers Open Analytics' overview and pages for each window, as the
// live API does, and counts the reads.
type fakeOA struct {
	srv   *httptest.Server
	reads atomic.Int32
	fail  atomic.Bool
	slow  atomic.Bool
	bad   atomic.Value // what was wrong with a request, if anything
}

func newFakeOA(t *testing.T) *fakeOA {
	f := &fakeOA{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reads.Add(1)
		q := r.URL.Query()
		from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
		to, errTo := time.Parse(time.RFC3339, q.Get("to"))
		switch {
		case r.Header.Get("Authorization") != "Bearer "+testOAKey:
			f.bad.Store("the read key isn't the bearer token")
		case r.Header.Get("X-OA-Site") != "":
			f.bad.Store("a read key was sent with the site's header, which Open Analytics refuses")
		case errFrom != nil || errTo != nil || q.Get("timezone") != "UTC" || from.Unix()%3600 != 0 || to.Unix()%3600 != 0:
			f.bad.Store("the window isn't whole UTC hours: " + r.URL.RawQuery)
		}
		if f.slow.Load() {
			time.Sleep(500 * time.Millisecond)
		}
		if f.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"code":"INTERNAL <b>","message":"`+testOAKey+`"}}`)
			return
		}
		// The window is its hours, from the start of the hour a day, 7 or
		// 30 days ago, to the end of this one.
		scale := map[int]int{25: 1, 7*24 + 1: 5, 30*24 + 1: 12}[int(to.Sub(from).Hours())]
		if scale == 0 {
			f.bad.Store(fmt.Sprintf("a window of %v", to.Sub(from)))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/read/analytics/overview":
			fmt.Fprintf(w, `{"meta":{"timezone":"UTC"},"totals":{"events":%d,"pageviews":%d,"visitors":%d,"billable_events":%d},"comparison":null}`, 90*scale, 60*scale, 40*scale, 62*scale)
		case "/v1/read/analytics/pages":
			fmt.Fprintf(w, `{"meta":{},"items":[
				{"page_path":"/","views":%[1]d,"visitors":%[1]d,"entrances":%[1]d,"exits":1,"bounces":1,"bounce_rate":0.5},
				{"page_path":"/demo/","views":%[2]d,"visitors":%[2]d,"entrances":3,"exits":1,"bounces":0,"bounce_rate":0},
				{"page_path":"/demo/servers/survival","views":9,"visitors":7,"entrances":%[3]d,"exits":1,"bounces":0,"bounce_rate":0},
				{"page_path":"/demolition","views":5,"visitors":5,"entrances":5,"exits":5,"bounces":5,"bounce_rate":1}]}`, 30*scale, 10*scale, 2*scale)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOA) check(t *testing.T) {
	t.Helper()
	if bad := f.bad.Load(); bad != nil {
		t.Fatal(bad)
	}
}

func copyCommand(e *env, from, channel string) *httptest.ResponseRecorder {
	return e.do("POST", "/v1/site", from, `{"event":"install_copied","channel":"`+channel+`"}`, "Origin", SiteOrigin, "Content-Type", "text/plain;charset=UTF-8")
}

// The funnel starts with the site's visitors and demo opens, read from Open
// Analytics with the service's own key and kept for a while, then follows
// installs made with the playkeeper.io command to those that still run.
func TestTheFunnelFollowsPlaykeeperIoFromTheSitesVisitorsToInstallsThatStillRun(t *testing.T) {
	oa := newFakeOA(t)
	e := newEnv(t, func(c *Config) { c.OAKey, c.OAAPI = testOAKey, oa.srv.URL })
	site := func(n int, ev string) usage.Install { return event(n, ev, usage.SourceSite, "") }
	// Three days ago: one that succeeded and has been quiet since.
	e.advance(-3 * day)
	e.send(usage.PathInstall, site(2, usage.EventStarted))
	e.send(usage.PathInstall, site(2, usage.EventSucceeded))
	e.send(usage.PathHeartbeat, beat(2, usage.SourceSite, 1, 1))
	e.advance(3 * day)
	// Today: one that runs, one that failed, one refused, one from GitHub
	// and one of the project's own tests.
	e.send(usage.PathInstall, site(1, usage.EventStarted))
	e.send(usage.PathInstall, site(1, usage.EventSucceeded))
	e.send(usage.PathHeartbeat, beat(1, usage.SourceSite, 2, 1))
	e.send(usage.PathInstall, site(3, usage.EventStarted))
	e.send(usage.PathInstall, event(3, usage.EventFailed, usage.SourceSite, "docker"))
	e.send(usage.PathInstall, event(6, usage.EventRefused, usage.SourceSite, "memory"))
	e.send(usage.PathInstall, event(4, usage.EventSucceeded, usage.SourceGitHub, ""))
	e.send(usage.PathHeartbeat, beat(4, usage.SourceGitHub, 1, 1))
	ci := site(5, usage.EventSucceeded)
	ci.Test = true
	e.send(usage.PathInstall, ci)
	for i, ch := range []string{"", "hn", ""} {
		if w := copyCommand(e, fmt.Sprintf("198.51.100.%d", 70+i), ch); w.Code != http.StatusNoContent {
			t.Fatalf("a copy answered %d: %s", w.Code, w.Body)
		}
	}

	s := e.summary()
	oa.check(t)
	n := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	for w, want := range map[string][6]int{"1d": {40, 10 + 2, 3, 2, 1, 1}, "7d": {200, 50 + 10, 3, 3, 2, 1}, "30d": {480, 120 + 24, 3, 3, 2, 1}} {
		f := s.Funnel[w]
		if got := [6]int{n(f.Visitors), n(f.DemoOpens), f.CommandCopies, f.Started, f.Succeeded, f.StillRunning}; got != want {
			t.Errorf("funnel %s: visitors, demo opens, copies, started, succeeded, still running = %v, want %v", w, got, want)
		}
	}
	if !s.Site.Configured || s.Site.ReadAt == nil || !s.Site.ReadAt.Equal(e.clock().Truncate(time.Second)) || s.Site.Error != "" {
		t.Errorf("site: %+v", s.Site)
	}
	if got := oa.reads.Load(); got != 6 {
		t.Errorf("reading the site's numbers took %d requests, want an overview and pages for each window", got)
	}

	// Kept for a quarter of an hour, then read again.
	e.advance(10 * time.Minute)
	e.summary()
	if got := oa.reads.Load(); got != 6 {
		t.Errorf("the site's numbers were read again after 10 minutes (%d requests)", got)
	}
	e.advance(6 * time.Minute)
	e.summary()
	if got := oa.reads.Load(); got != 12 {
		t.Errorf("the site's numbers weren't read again after 16 minutes (%d requests)", got)
	}

	// A read that fails keeps the numbers from before and says why, without
	// what Open Analytics echoed; it is tried again only after a while.
	oa.fail.Store(true)
	e.advance(16 * time.Minute)
	s = e.summary()
	if n(s.Funnel["7d"].Visitors) != 200 || !strings.HasPrefix(s.Site.Error, "Open Analytics: it answered HTTP 500 INTERNAL for analytics/") || !s.Site.ReadAt.Equal(e.clock().Add(-16*time.Minute).Truncate(time.Second)) {
		t.Errorf("after a failed read: visitors %d, site %+v", n(s.Funnel["7d"].Visitors), s.Site)
	}
	reads := oa.reads.Load()
	e.advance(time.Minute)
	e.summary()
	if got := oa.reads.Load(); got != reads {
		t.Errorf("a failed read was tried again a minute later")
	}
	oa.fail.Store(false)
	e.advance(2 * time.Minute)
	if s = e.summary(); s.Site.Error != "" || !s.Site.ReadAt.Equal(e.clock().Truncate(time.Second)) {
		t.Errorf("after Open Analytics answered again: %+v", s.Site)
	}

	body := e.do("GET", "/v1/summary", "198.51.100.8", nil, "Authorization", "Bearer "+testToken).Body.String()
	for _, secret := range []string{testOAKey, "<b>"} {
		if strings.Contains(body, secret) || strings.Contains(e.log.String(), secret) {
			t.Errorf("the summary or the log shows %q", secret)
		}
	}
}

// A request for the summary that is dropped while the site's numbers are
// read doesn't stop the read, so the next request has them.
func TestADroppedRequestDoesntStopTheSitesNumbersBeingRead(t *testing.T) {
	oa := newFakeOA(t)
	oa.slow.Store(true)
	e := newEnv(t, func(c *Config) { c.OAKey, c.OAAPI = testOAKey, oa.srv.URL })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = e.svc.Summary(ctx)
	s := e.summary()
	if f := s.Funnel["1d"]; f.Visitors == nil || *f.Visitors != 40 || s.Site.Error != "" {
		t.Errorf("after a dropped request: visitors %v, site %+v", f.Visitors, s.Site)
	}
	oa.check(t)
}

// Without a key, the funnel has the service's own steps and says the site's
// can't be read; nothing is asked of Open Analytics.
func TestWithoutAKeyTheFunnelHasOnlyTheServicesOwnSteps(t *testing.T) {
	oa := newFakeOA(t)
	e := newEnv(t, func(c *Config) { c.OAAPI = oa.srv.URL })
	copyCommand(e, "198.51.100.80", "")
	body := e.do("GET", "/v1/summary", "198.51.100.8", nil, "Authorization", "Bearer "+testToken).Body.String()
	var s struct {
		Funnel map[string]map[string]any `json:"funnel"`
		Site   map[string]any            `json:"site"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	f := s.Funnel["30d"]
	if v, ok := f["visitors"]; !ok || v != nil || f["demoOpens"] != nil || f["commandCopies"] != float64(1) || s.Site["configured"] != false {
		t.Errorf("funnel %v, site %v", f, s.Site)
	}
	if oa.reads.Load() != 0 {
		t.Error("Open Analytics was asked without a key")
	}
}

// Counts come from playkeeper.io's pages alone, carry an event and a channel
// and nothing else, and are limited for each address and each day.
func TestSiteCountsComeOnlyFromThePagesAndSayNothingElse(t *testing.T) {
	e := newEnv(t)
	good := `{"event":"install_copied","channel":"hn","page":"/secret-page"}`
	for _, c := range []struct {
		name, body string
		header     []string
		code       int
	}{
		{"a copy", good, []string{"Origin", SiteOrigin, "Content-Type", "text/plain;charset=UTF-8"}, 204},
		{"as JSON", `{"event":"install_copied"}`, []string{"Origin", SiteOrigin}, 204},
		{"no origin", good, []string{"Content-Type", "text/plain"}, 403},
		{"another site", good, []string{"Origin", "https://elsewhere.example", "Content-Type", "text/plain"}, 403},
		{"plain http", good, []string{"Origin", "http://playkeeper.io", "Content-Type", "text/plain"}, 403},
		{"a form", good, []string{"Origin", SiteOrigin, "Content-Type", "application/x-www-form-urlencoded"}, 415},
		{"another event", `{"event":"demo_opened"}`, []string{"Origin", SiteOrigin, "Content-Type", "text/plain"}, 400},
		{"a channel with capitals", `{"event":"install_copied","channel":"HN"}`, []string{"Origin", SiteOrigin, "Content-Type", "text/plain"}, 400},
		{"a long channel", `{"event":"install_copied","channel":"` + strings.Repeat("a", 33) + `"}`, []string{"Origin", SiteOrigin, "Content-Type", "text/plain"}, 400},
		{"two counts", good + good, []string{"Origin", SiteOrigin, "Content-Type", "text/plain"}, 400},
		{"too large", `{"event":"install_copied","x":"` + strings.Repeat("x", maxSiteEvent) + `"}`, []string{"Origin", SiteOrigin, "Content-Type", "text/plain"}, 413},
	} {
		if w := e.do("POST", "/v1/site", "203.0.113.90", c.body, c.header...); w.Code != c.code {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	w := copyCommand(e, "203.0.113.92", "")
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != SiteOrigin {
		t.Errorf("a copy from the site isn't answered to the site, so its page sees no answer: %d %v", w.Code, w.Header())
	}
	accepted := 3
	for i := 0; ; i++ {
		w := copyCommand(e, "203.0.113.91", "")
		if w.Code == http.StatusTooManyRequests {
			if i != siteEventsPerIPBurst {
				t.Errorf("one address was stopped after %d counts, want %d", i, siteEventsPerIPBurst)
			}
			break
		}
		if w.Code != http.StatusNoContent || i > siteEventsPerIPBurst {
			t.Fatalf("count %d from one address answered %d", i+1, w.Code)
		}
		accepted++
	}
	if got := e.summary().Funnel["1d"].CommandCopies; got != accepted {
		t.Errorf("copies: %d, want %d", got, accepted)
	}
	stored := e.storedBytes()
	for _, secret := range []string{"203.0.113.90", "203.0.113.91", "secret-page", "playkeeper.io/"} {
		if strings.Contains(string(stored), secret) {
			t.Errorf("the service's files hold %q", secret)
		}
	}
}

func TestTheReadKeyIsCheckedWithoutBeingShown(t *testing.T) {
	for _, c := range []struct{ key, api, want string }{
		{"short", "", EnvOAKey},
		{"oa_rk_good-key-0123456789", "http://analytics.example", EnvOAAPI},
		{"oa_rk_good key with spaces 0123456789", "", EnvOAKey},
	} {
		_, err := FromEnv(func(k string) string {
			return map[string]string{EnvOAKey: c.key, EnvOAAPI: c.api}[k]
		})
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), c.key) {
			t.Errorf("%q, %q: %v", c.key, c.api, err)
		}
	}
	cfg, err := FromEnv(func(k string) string { return map[string]string{EnvOAKey: testOAKey}[k] })
	if err != nil || cfg.OAKey != testOAKey || cfg.OAAPI != DefaultOAAPI {
		t.Errorf("%+v %v", cfg, err)
	}
}
