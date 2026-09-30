package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/update"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// checkingEnv is updateEnv with the automatic check on: its first look
// within first of starting, then every interval, give or take a fifth.
func checkingEnv(t *testing.T, first, interval time.Duration, setup func(e *agentEnv)) (*agentEnv, *fakeRelease, ed25519.PrivateKey) {
	t.Helper()
	var rel *fakeRelease
	var priv ed25519.PrivateKey
	e := newAgentEnvWith(t, func(e *agentEnv) {
		rel, priv = e.useReleases()
		e.tweak = func(o *Options) { o.UpdateCheckFirst, o.UpdateCheckInterval = first, interval }
		if setup != nil {
			setup(e)
		}
	})
	return e, rel, priv
}

func (e *agentEnv) autoCheck(on bool) api.UpdateInfo {
	e.t.Helper()
	code, out := e.call("PUT", "/v1/update/auto", map[string]any{"on": on, "actor": "admin"})
	if code != 200 {
		e.t.Fatalf("PUT /v1/update/auto: %d %v", code, out)
	}
	return decodeAs[api.UpdateInfo](e.t, out)
}

// Checks wait about the interval, anywhere from four fifths of it to six
// fifths, at random: machines that started together drift apart.
func TestChecksWaitAboutTheIntervalGiveOrTakeAFifth(t *testing.T) {
	const interval = 30 * time.Minute
	for r, want := range map[float64]time.Duration{0: 24 * time.Minute, 0.5: 30 * time.Minute, 0.999999: 36 * time.Minute} {
		if got := checkWait(interval, 0, 0, r); got < want-time.Second || got > want {
			t.Errorf("at random %v, a check waits %v, want %v", r, got, want)
		}
	}
	var lo, hi, sum time.Duration = time.Hour, 0, 0
	const n = 2000
	for range n {
		w := checkWait(interval, 0, 0, mrand.Float64())
		if w < 24*time.Minute || w >= 36*time.Minute {
			t.Fatalf("a check waits %v, outside 24 to 36 minutes", w)
		}
		lo, hi, sum = min(lo, w), max(hi, w), sum+w
	}
	if lo > 25*time.Minute || hi < 35*time.Minute {
		t.Errorf("%d checks waited from %v to %v: they must spread over 24 to 36 minutes", n, lo, hi)
	}
	if mean := sum / n; mean < 29*time.Minute || mean > 31*time.Minute {
		t.Errorf("checks wait %v on average, want about 30 minutes", mean)
	}
}

// A check whose first source failed backs off: twice the wait for each
// failure in a row, up to six hours, and never less than a source's
// Retry-After.
func TestFailedChecksBackOff(t *testing.T) {
	const interval = 30 * time.Minute
	for failures, want := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		if got := checkWait(interval, failures, 0, 0.5); got != want {
			t.Errorf("after %d failures in a row a check waits %v, want %v", failures, got, want)
		}
	}
	if got := checkWait(interval, 1, 3*time.Hour, 0.5); got != 3*time.Hour {
		t.Errorf("a source that asked for three hours is asked again after %v", got)
	}
	if got := checkWait(interval, 20, 0, 0.999999); got > 6*time.Hour*6/5 {
		t.Errorf("a long outage makes checks wait %v, more than six hours and a fifth", got)
	}
	if got := checkWait(12*time.Hour, 3, 0, 0.5); got != 12*time.Hour {
		t.Errorf("an interval longer than the backoff's cap waits %v after failures, want the interval", got)
	}
}

// The agent looks for a release by itself when it starts, then again about
// every interval, and the later looks ask only whether the release changed:
// nobody presses anything, and the dashboard reads what it found.
func TestTheAgentChecksWhenItStartsAndAboutEveryInterval(t *testing.T) {
	e, rel, _ := checkingEnv(t, 20*time.Millisecond, 150*time.Millisecond, nil)
	e.waitFor("the check when the agent starts", func() bool { return e.a.Machine(context.Background()).UpdateAvailable == "0.2.1" })
	code, out := e.call("GET", "/v1/update", nil)
	if info := decodeAs[api.UpdateInfo](t, out); code != 200 || !info.Available || info.Latest != "0.2.1" || info.CheckedAt == nil || !info.AutoCheck {
		t.Fatalf("what the dashboard reads after the check at start: %d %+v", code, info)
	}
	start := rel.count(update.SignatureFile)
	e.waitFor("two checks after the first", func() bool { return rel.count(update.SignatureFile) >= start+2 })
	if n, cond, not := rel.counts(update.SignatureFile); cond < n-1 || not < n-1 {
		t.Errorf("of %d signature requests, %d asked whether it changed and %d were answered 304: every check after the first must ask only that", n, cond, not)
	}
	if n := rel.count(update.ManifestFile); n != 1 {
		t.Errorf("the manifest was fetched %d times while the release stayed the same, want once", n)
	}
}

// The first look waits a random part of UpdateCheckFirst after the agent
// starts: at once for one machine, near its end for another.
func TestTheFirstCheckComesAtARandomMomentOfTheFirstMinute(t *testing.T) {
	soon, relSoon, _ := checkingEnv(t, time.Hour, time.Hour, func(e *agentEnv) {
		tweak := e.tweak
		e.tweak = func(o *Options) { tweak(o); o.UpdateJitter = func() float64 { return 0 } }
	})
	soon.waitFor("the first check, at once", func() bool { return relSoon.count(update.SignatureFile) == 1 })
	late, relLate, _ := checkingEnv(t, time.Hour, time.Hour, func(e *agentEnv) {
		tweak := e.tweak
		e.tweak = func(o *Options) { tweak(o); o.UpdateJitter = func() float64 { return 0.9 } }
	})
	time.Sleep(300 * time.Millisecond)
	if n := relLate.count(update.SignatureFile); n != 0 {
		t.Fatalf("a machine whose first check is 54 minutes after it starts checked %d times at once", n)
	}
	late.a.upd.mu.Lock()
	after := late.a.upd.next.Sub(late.a.started)
	late.a.upd.mu.Unlock()
	if after < 53*time.Minute || after > 55*time.Minute {
		t.Errorf("its first check is %v after it started, want 54 minutes", after)
	}
}

// A source that fails is asked less and less often, and no sooner than its
// Retry-After; once it answers again, checks come every interval again.
func TestAFailingSourceIsAskedLessOften(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, 40*time.Millisecond, func(e *agentEnv) {
		e.reconcileInterval = 5 * time.Millisecond
		tweak := e.tweak
		e.tweak = func(o *Options) { tweak(o); o.UpdateJitter = func() float64 { return 0.5 } }
	})
	rel.answer(http.StatusInternalServerError, "")
	e.waitFor("four failed checks", func() bool { return rel.count(update.SignatureFile) >= 4 })
	rel.mu.Lock()
	times := append([]time.Time(nil), rel.sigTimes[:4]...)
	rel.mu.Unlock()
	for i := 1; i < len(times); i++ {
		want := 40 * time.Millisecond << i
		if gap := times[i].Sub(times[i-1]); gap < want*19/20 {
			t.Errorf("failed checks %d and %d were %v apart, want at least %v: each failure in a row doubles the wait", i, i+1, gap, want)
		}
	}
	if info := e.checkNow(); !strings.Contains(info.CheckError, "answered HTTP 500") {
		t.Fatalf("a failed check says why: %+v", info)
	}

	rel.answer(0, "")
	if info := e.checkNow(); info.CheckError != "" || info.Latest != "0.2.1" {
		t.Fatalf("the check once the source answers again: %+v", info)
	}
	n := rel.count(update.SignatureFile)
	e.waitFor("checks every interval again", func() bool { return rel.count(update.SignatureFile) >= n+3 })

	rel.answer(http.StatusServiceUnavailable, "1")
	e.waitFor("a check that is told to wait a second", func() bool { return e.a.updateInfo().CheckError != "" })
	told := rel.count(update.SignatureFile)
	time.Sleep(600 * time.Millisecond)
	if got := rel.count(update.SignatureFile); got != told {
		t.Errorf("%d checks came within a second of a Retry-After of a second", got-told)
	}
	e.waitFor("the check after the second", func() bool { return rel.count(update.SignatureFile) > told })
}

func (e *agentEnv) checkNow() api.UpdateInfo {
	e.t.Helper()
	code, out := e.call("POST", "/v1/update/check", map[string]any{"actor": "admin"})
	if code != 200 {
		e.t.Fatalf("POST /v1/update/check: %d %v", code, out)
	}
	return decodeAs[api.UpdateInfo](e.t, out)
}

// However many tabs have the dashboard open, the machine checks once: tabs
// read what the last check found, and presses of Check for updates while a
// check runs wait for it instead of asking again.
func TestOneCheckHoweverManyTabsAreOpen(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, time.Hour, nil)
	e.waitFor("the check when the agent starts", func() bool { return e.a.updateInfo().Latest == "0.2.1" })
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			for range 10 {
				for _, p := range []string{"/v1/machine", "/v1/update"} {
					if code, _ := e.call("GET", p, nil); code != 200 {
						t.Errorf("GET %s: %d", p, code)
					}
				}
			}
		})
	}
	wg.Wait()
	if n := rel.count(update.SignatureFile); n != 1 {
		t.Fatalf("30 tabs reading the machine and its update ten times each made %d checks, want the one when the agent started", n)
	}

	let := rel.hold()
	t.Cleanup(let)
	infos := make(chan api.UpdateInfo, 20)
	for range 20 {
		wg.Go(func() { infos <- e.checkNow() })
	}
	e.waitFor("one check at the release location, and the other presses waiting for it", func() bool {
		rel.mu.Lock()
		pending := rel.pending
		rel.mu.Unlock()
		e.a.upd.mu.Lock()
		defer e.a.upd.mu.Unlock()
		return pending == 1 && e.a.upd.waiting == 19
	})
	let()
	wg.Wait()
	close(infos)
	for info := range infos {
		if info.Latest != "0.2.1" || info.CheckError != "" {
			t.Errorf("a press that waited for the running check: %+v", info)
		}
	}
	if n := rel.count(update.SignatureFile); n != 2 {
		t.Errorf("20 presses of Check for updates at once made %d checks, want one", n-1)
	}
}

// Check for updates automatically, turned off, stops the agent from looking
// by itself, across restarts too; the button still checks, and turning it
// on looks at once.
func TestTurnedOffTheAgentChecksOnlyWhenAsked(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, 60*time.Millisecond, nil)
	e.waitFor("an automatic check", func() bool { return rel.count(update.SignatureFile) >= 1 })
	if info := e.autoCheck(false); info.AutoCheck || info.Latest != "0.2.1" {
		t.Fatalf("turned off: %+v", info)
	}
	time.Sleep(100 * time.Millisecond)
	e.waitFor("the check that was running to end", func() bool {
		e.a.upd.mu.Lock()
		defer e.a.upd.mu.Unlock()
		return e.a.upd.checking == nil
	})
	n := rel.count(update.SignatureFile)
	time.Sleep(400 * time.Millisecond)
	if got := rel.count(update.SignatureFile); got != n {
		t.Fatalf("%d automatic checks ran while they were off", got-n)
	}
	if code, out := e.call("GET", "/v1/update", nil); code != 200 || out["autoCheck"] != false {
		t.Errorf("Settings reads: %d %v", code, out)
	}
	if info := e.checkNow(); info.Latest != "0.2.1" || rel.count(update.SignatureFile) != n+1 {
		t.Fatalf("the button checks while automatic checks are off: %+v", info)
	}

	e.stop()
	e.start()
	time.Sleep(300 * time.Millisecond)
	if got := rel.count(update.SignatureFile); got != n+1 {
		t.Fatalf("after a restart, %d automatic checks ran while they were off", got-n-1)
	}
	if info := e.a.updateInfo(); info.AutoCheck {
		t.Fatal("the switch was forgotten in a restart")
	}
	e.stop()
	e.tweak = func(o *Options) { o.UpdateCheckFirst, o.UpdateCheckInterval = time.Hour, time.Hour }
	e.start()
	if info := e.autoCheck(true); !info.AutoCheck {
		t.Fatalf("turned on: %+v", info)
	}
	e.waitFor("the check turning them on makes", func() bool { return rel.count(update.SignatureFile) > n+1 })

	if code, out := e.call("PUT", "/v1/update/auto", map[string]any{"on": false}); code != 400 {
		t.Errorf("a switch with no actor: %d %v", code, out)
	}
	for _, action := range []string{"update_checks.off", "update_checks.on"} {
		if got := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = ? AND actor = 'admin' AND result = 'succeeded'`, action); got != 1 {
			t.Errorf("the audit log records %s %d times, want once: %v", action, got, e.auditActions())
		}
	}
}

// A machine joined to another dashboard doesn't look by itself: its
// dashboard finds releases and updates it to its own version.
func TestAJoinedMachineLeavesTheChecksToItsDashboard(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, 50*time.Millisecond, func(e *agentEnv) { e.cfg.NoPanel = true })
	time.Sleep(400 * time.Millisecond)
	if n := rel.count(update.SignatureFile); n != 0 {
		t.Fatalf("a joined machine checked %d times by itself", n)
	}
	if info := e.checkNow(); info.Latest != "0.2.1" {
		t.Fatalf("its own button still checks: %+v", info)
	}
}

// The automatic check is the button's check: the same fetch, the same
// signature check, the same answer, down to a release signed by a key this
// build doesn't trust.
func TestTheAutomaticCheckIsTheButtonsCheck(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, 80*time.Millisecond, nil)
	e.waitFor("the check when the agent starts", func() bool { return e.a.updateInfo().Latest == "0.2.1" })
	same := func(why string) {
		t.Helper()
		auto := e.a.updateInfo()
		button := e.checkNow()
		auto.CheckedAt, button.CheckedAt = nil, nil
		if !reflect.DeepEqual(auto, button) {
			t.Errorf("%s: the automatic check found %+v, the button %+v", why, auto, button)
		}
	}
	same("a signed newer release")

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	n := rel.count(update.SignatureFile)
	rel.publish(t, other, "0.2.2")
	e.waitFor("an automatic check of the release signed by another key", func() bool { return rel.count(update.SignatureFile) > n })
	e.waitFor("its answer", func() bool { return e.a.updateInfo().CheckError != "" })
	if info := e.a.updateInfo(); info.Latest != "0.2.1" || !strings.Contains(info.CheckError, "not signed") {
		t.Fatalf("the automatic check trusted a release signed by another key: %+v", info)
	}
	same("a release signed by another key")
}

// What a check found and the validators it kept survive a restart, so the
// check when the agent starts again asks only whether the release changed.
func TestAfterARestartTheCheckAsksOnlyWhetherTheReleaseChanged(t *testing.T) {
	e, rel, _ := checkingEnv(t, 10*time.Millisecond, time.Hour, nil)
	e.waitFor("the check when the agent starts", func() bool { return rel.count(update.SignatureFile) == 1 })
	e.stop()
	e.start()
	e.waitFor("the check when it starts again", func() bool { return rel.count(update.SignatureFile) == 2 })
	if _, cond, not := rel.counts(update.SignatureFile); cond != 1 || not != 1 {
		t.Errorf("after a restart the check sent %d conditional requests with %d 304s, want 1 and 1", cond, not)
	}
	if n := rel.count(update.ManifestFile); n != 1 {
		t.Errorf("the manifest was fetched %d times, want once", n)
	}
	if info := e.a.updateInfo(); info.Latest != "0.2.1" || !info.Available {
		t.Errorf("after the restart: %+v", info)
	}
}

// hostMap sends requests for a host to a test server instead.
type hostMap map[string]*httptest.Server

func (m hostMap) RoundTrip(req *http.Request) (*http.Response, error) {
	srv, ok := m[req.URL.Host]
	if !ok {
		return nil, fmt.Errorf("no route to %s in this test", req.URL.Host)
	}
	u, _ := url.Parse(srv.URL)
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host, r.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

// Without a release location of its own, an install checks playkeeper.io's
// copy of the latest release, and GitHub's when playkeeper.io doesn't answer
// with one; a failure there still backs the next check off.
func TestChecksAskPlaykeeperIoThenGitHub(t *testing.T) {
	var paths sync.Map
	record := func(name string, next func(http.ResponseWriter, *http.Request)) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths.Store(name+" "+r.URL.Path, true)
			next(w, r)
		})
	}
	site, github := &fakeRelease{}, &fakeRelease{}
	site.srv, github.srv = httptest.NewServer(record("site", site.serve)), httptest.NewServer(record("github", github.serve))
	t.Cleanup(site.srv.Close)
	t.Cleanup(github.srv.Close)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	site.publish(t, priv, "0.2.1")
	github.publish(t, priv, "0.2.1")
	old := version.Version
	version.Version = "0.2.0"
	t.Cleanup(func() { version.Version = old })
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.cfg.Dev = false
		e.updateKeys = []ed25519.PublicKey{pub}
		e.tweak = func(o *Options) {
			o.HTTPClient = &http.Client{Transport: hostMap{"playkeeper.io": site.srv, "github.com": github.srv}}
			o.UpdateCheckInterval, o.UpdateJitter = time.Hour, func() float64 { return 0.5 }
		}
	})
	if info := e.checkNow(); info.Latest != "0.2.1" || info.CheckError != "" || github.count(update.SignatureFile) != 0 {
		t.Fatalf("playkeeper.io answers the check: %+v (GitHub asked %d times)", info, github.count(update.SignatureFile))
	}
	for _, p := range []string{"site /releases/latest/playkeeper-release.json.sig", "site /releases/latest/playkeeper-release.json"} {
		if _, ok := paths.Load(p); !ok {
			t.Errorf("playkeeper.io wasn't asked for %s", p)
		}
	}

	site.answer(http.StatusBadGateway, "")
	if info := e.checkNow(); info.Latest != "0.2.1" || info.CheckError != "" || github.count(update.SignatureFile) != 1 {
		t.Fatalf("GitHub answers when playkeeper.io doesn't: %+v", info)
	}
	if _, ok := paths.Load("github /CIYAhq/playkeeper/releases/latest/download/playkeeper-release.json.sig"); !ok {
		t.Error("GitHub wasn't asked at its latest release's address")
	}
	e.a.upd.mu.Lock()
	wait := e.a.upd.next.Sub(e.a.now())
	e.a.upd.mu.Unlock()
	if wait < 110*time.Minute {
		t.Errorf("after playkeeper.io failed, the next check is in %v, want the interval doubled", wait)
	}

	github.answer(http.StatusNotFound, "")
	info := e.checkNow()
	if !strings.Contains(info.CheckError, "answered HTTP 502") || !strings.Contains(info.CheckError, "answered HTTP 404") || info.Latest != "0.2.1" {
		t.Errorf("when neither answers, the check says why for both and keeps what it found before: %+v", info)
	}
}
