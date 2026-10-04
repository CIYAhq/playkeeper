package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func goodSystem() System {
	return System{Version: "0.4.4", OS: "ubuntu", OSVersion: "24.04", Arch: "amd64", Source: SourceSite, Kind: KindDashboard}
}

func goodHeartbeat() Heartbeat {
	return Heartbeat{ID: "0123456789abcdef0123456789abcdef", System: goodSystem(), Address: AddressFree, Servers: 2, Running: 1}
}

func goodInstall() Install {
	return Install{ID: "0123456789abcdef0123456789abcdef", Event: EventStarted, System: goodSystem()}
}

func TestReportsWithEveryFieldInRangePassTheirCheck(t *testing.T) {
	h := goodHeartbeat()
	for _, change := range []func(*Heartbeat){
		func(h *Heartbeat) {},
		func(h *Heartbeat) { h.Source = "" },
		func(h *Heartbeat) { h.OSVersion = "" },
		func(h *Heartbeat) { h.Channel = "hn" },
		func(h *Heartbeat) { h.Kind, h.Address = KindJoined, AddressIP },
		func(h *Heartbeat) { h.Version = "0.6.0-dev+0123456789ab" },
		func(h *Heartbeat) { h.Servers, h.Running = 0, 0 },
		func(h *Heartbeat) { h.Servers, h.Running = MaxServers, MaxServers },
		func(h *Heartbeat) { h.Test = true },
		func(h *Heartbeat) { h.Reached = ReachedAccount },
		func(h *Heartbeat) { h.Reached = ReachedFriends },
		func(h *Heartbeat) { h.Reached = "first-backup" },
	} {
		c := h
		change(&c)
		if err := c.Check(); err != nil {
			t.Errorf("%+v was refused: %v", c, err)
		}
	}
	for _, e := range []Install{
		goodInstall(),
		{ID: goodInstall().ID, Event: EventFailed, Step: "services", System: goodSystem()},
		{ID: goodInstall().ID, Event: EventRefused, Step: "memory+port", System: goodSystem()},
		{ID: goodInstall().ID, Event: EventSucceeded, System: System{Version: "0.4.4", OS: "unknown", Arch: "arm64", Source: SourceTarball, Kind: KindJoined}},
	} {
		if err := e.Check(); err != nil {
			t.Errorf("%+v was refused: %v", e, err)
		}
	}
}

// Every field has a fixed form, so a report can't carry a host name, a
// path, an address or free text in any of them.
func TestReportsWithAFieldOutOfRangeAreRefusedWithoutShowingIt(t *testing.T) {
	secret := "alice.example.com"
	cases := map[string]func(*Heartbeat){
		"id":        func(h *Heartbeat) { h.ID = "0123456789ABCDEF0123456789abcdef" },
		"id ":       func(h *Heartbeat) { h.ID = secret },
		"version":   func(h *Heartbeat) { h.Version = "0.4.4 " + secret },
		"os":        func(h *Heartbeat) { h.OS = "Ubuntu" },
		"os ":       func(h *Heartbeat) { h.OS = secret + "/x" },
		"osVersion": func(h *Heartbeat) { h.OSVersion = "24.04 LTS (Noble)" },
		"arch":      func(h *Heartbeat) { h.Arch = "x86_64" },
		"source":    func(h *Heartbeat) { h.Source = secret },
		"channel":   func(h *Heartbeat) { h.Channel = "HN" },
		"channel ":  func(h *Heartbeat) { h.Channel = strings.Repeat("a", 33) },
		"kind":      func(h *Heartbeat) { h.Kind = "panel" },
		"address":   func(h *Heartbeat) { h.Address = secret },
		"servers":   func(h *Heartbeat) { h.Servers = MaxServers + 1 },
		"servers ":  func(h *Heartbeat) { h.Servers = -1 },
		"running":   func(h *Heartbeat) { h.Servers, h.Running = 1, 2 },
		"running ":  func(h *Heartbeat) { h.Running = -1 },
		"reached":   func(h *Heartbeat) { h.Reached = "Played" },
		"reached ":  func(h *Heartbeat) { h.Reached = secret },
		"reached  ": func(h *Heartbeat) { h.Reached = strings.Repeat("a", 33) },
	}
	for field, change := range cases {
		h := goodHeartbeat()
		change(&h)
		err := h.Check()
		if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": "+strings.TrimSpace(field)) {
			t.Errorf("%s: %v", field, err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error shows the value: %v", field, err)
		}
	}
	for field, e := range map[string]Install{
		"event":  {ID: goodInstall().ID, Event: "installed", System: goodSystem()},
		"step":   {ID: goodInstall().ID, Event: EventFailed, Step: "install Docker (docker.io)", System: goodSystem()},
		"step ":  {ID: goodInstall().ID, Event: EventRefused, Step: "a+b+c+d+e+f+g+h+i", System: goodSystem()},
		"source": {ID: goodInstall().ID, Event: EventStarted, System: System{Version: "0.4.4", OS: "ubuntu", Arch: "amd64", Kind: KindDashboard}},
	} {
		if err := e.Check(); !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": "+strings.TrimSpace(field)) {
			t.Errorf("%s: %v", field, err)
		}
	}
}

// jsonKeys are the keys v sends.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// What a report carries, field by field; README.md's "Usage stats" lists the
// same (TestTheReadmeListsEveryFieldSent).
var (
	heartbeatFields = []string{"address", "arch", "channel", "id", "kind", "os", "osVersion", "running", "servers", "source", "test", "version"}
	installFields   = []string{"arch", "channel", "event", "id", "kind", "os", "osVersion", "source", "step", "test", "version"}
)

func TestReportsCarryOnlyTheirFields(t *testing.T) {
	h := goodHeartbeat()
	h.Channel, h.Test = "hn", true
	if got := jsonKeys(t, h); !reflect.DeepEqual(got, heartbeatFields) {
		t.Errorf("a heartbeat sends %v, want %v", got, heartbeatFields)
	}
	e := goodInstall()
	e.Step, e.Channel, e.Test = "services", "hn", true
	if got := jsonKeys(t, e); !reflect.DeepEqual(got, installFields) {
		t.Errorf("an install event sends %v, want %v", got, installFields)
	}
}

func TestDoNotTrackWinsAndOnlyClearValuesCount(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want Choice
		why  string
	}{
		{nil, Unset, ""},
		{map[string]string{"DO_NOT_TRACK": "1"}, Off, EnvDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "true"}, Off, EnvDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "yes please"}, Off, EnvDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "1", "PLAYKEEPER_USAGE_STATS": "on"}, Off, EnvDoNotTrack},
		{map[string]string{"DO_NOT_TRACK": "0"}, Unset, ""},
		{map[string]string{"DO_NOT_TRACK": "false", "PLAYKEEPER_USAGE_STATS": "on"}, On, EnvSwitch},
		{map[string]string{"DO_NOT_TRACK": " "}, Unset, ""},
		{map[string]string{"PLAYKEEPER_USAGE_STATS": "OFF"}, Off, EnvSwitch},
		{map[string]string{"PLAYKEEPER_USAGE_STATS": "0"}, Off, EnvSwitch},
		{map[string]string{"PLAYKEEPER_USAGE_STATS": "on"}, On, EnvSwitch},
		{map[string]string{"PLAYKEEPER_USAGE_STATS": "maybe"}, Unset, ""},
	} {
		got, why := FromEnv(func(k string) string { return c.env[k] })
		if got != c.want || why != c.why {
			t.Errorf("%v: got %v (%q), want %v (%q)", c.env, got, why, c.want, c.why)
		}
	}
}

func TestSourceIsAReleasesOwnOrABuildFromSource(t *testing.T) {
	for _, c := range []struct{ said, version, want string }{
		{SourceSite, "0.4.4", SourceSite},
		{SourceGitHub, "0.4.4", SourceGitHub},
		{SourceMirror, "0.4.4", SourceMirror},
		{"", "0.4.4", SourceTarball},
		{"somewhere", "0.4.4", SourceTarball},
		{SourceBuild, "0.4.4", SourceTarball},
		{SourceSite, "0.6.0-dev+0123456789ab", SourceBuild},
		{SourceSite, "0.4.4-ci.1", SourceBuild},
		{"", "dev", SourceBuild},
	} {
		if got := SourceFor(c.said, c.version); got != c.want {
			t.Errorf("SourceFor(%q, %q) = %q, want %q", c.said, c.version, got, c.want)
		}
	}
}

func TestCleaningGivesValuesTheServiceTakes(t *testing.T) {
	for _, c := range []struct{ id, version, os, osVersion string }{
		{"ubuntu", "24.04", "ubuntu", "24.04"},
		{"Ubuntu ", " 24.04", "ubuntu", "24.04"},
		{"", "", "unknown", ""},
		{"my distro", "1", "unknown", ""},
		{"arch", "", "arch", ""},
		{"debian", "13 (trixie)", "debian", ""},
	} {
		os, v := CleanOS(c.id, c.version)
		if os != c.os || v != c.osVersion {
			t.Errorf("CleanOS(%q, %q) = %q, %q", c.id, c.version, os, v)
		}
		h := goodHeartbeat()
		h.OS, h.OSVersion = os, v
		if err := h.Check(); err != nil {
			t.Errorf("CleanOS(%q, %q): %v", c.id, c.version, err)
		}
	}
	if CleanChannel(" HN ") != "hn" || CleanChannel("no/way") != "" || CleanChannel("") != "" {
		t.Error("CleanChannel")
	}
	if CleanArch("arm64") != "arm64" || CleanArch("x86_64") != "other" {
		t.Error("CleanArch")
	}
	if CleanSource(SourceGitHub) != SourceGitHub || CleanSource("elsewhere") != "" {
		t.Error("CleanSource")
	}
}

func TestInstallIDsAreRandomAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewID()
		if !IsID(id) || seen[id] {
			t.Fatalf("NewID gave %q", id)
		}
		seen[id] = true
	}
}

func TestTheServiceIsReachedOverHTTPSOrOnThisMachine(t *testing.T) {
	for _, ok := range []string{DefaultURL, "https://stats.example.test/", "http://127.0.0.1:8766", "http://localhost:9", "http://[::1]:8766"} {
		if _, err := CheckURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "stats.playkeeper.io", "http://stats.playkeeper.io", "https://stats.playkeeper.io/v1", "https://user:pw@stats.playkeeper.io", "https://stats.playkeeper.io?x=1", "ftp://stats.playkeeper.io"} {
		if _, err := CheckURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTheClientPostsTheReportAndNothingElse(t *testing.T) {
	type got struct {
		method, path, contentType, agent, cookie string
		body                                     map[string]any
	}
	seen := make(chan got, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var g got
		g.method, g.path, g.contentType, g.agent, g.cookie = r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.Header.Get("Cookie")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &g.body)
		seen <- g
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, Version: "0.4.4"}
	if err := c.SendHeartbeat(context.Background(), goodHeartbeat()); err != nil {
		t.Fatal(err)
	}
	g := <-seen
	if g.method != "POST" || g.path != PathHeartbeat || g.contentType != "application/json" || g.agent != "playkeeper/0.4.4" || g.cookie != "" {
		t.Errorf("heartbeat request: %+v", g)
	}
	if g.body["id"] != goodHeartbeat().ID || g.body["servers"] != float64(2) || g.body["address"] != AddressFree {
		t.Errorf("heartbeat body: %v", g.body)
	}
	if err := c.SendInstall(context.Background(), goodInstall()); err != nil {
		t.Fatal(err)
	}
	if g := <-seen; g.path != PathInstall || g.body["event"] != EventStarted {
		t.Errorf("install request: %+v", g)
	}
}

// A reach check names a Minecraft server's port, and nothing else.
func TestAReachCheckNamesOnlyAMinecraftServersPort(t *testing.T) {
	for _, p := range []int{ReachPortMin, 25566, ReachPortMax} {
		if err := (ReachRequest{Port: p}).Check(); err != nil {
			t.Errorf("port %d was refused: %v", p, err)
		}
	}
	for _, p := range []int{0, 22, 443, 8443, ReachPortMin - 1, ReachPortMax + 1, 65535, 70000} {
		if err := (ReachRequest{Port: p}).Check(); !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), ": port") {
			t.Errorf("port %d: %v", p, err)
		}
	}
	if got := jsonKeys(t, ReachRequest{Port: ReachPortMin}); !reflect.DeepEqual(got, []string{"port"}) {
		t.Errorf("a reach check sends %v", got)
	}
}

// The client asks with the port alone, and reads what the service found, or
// why it refused, with how long it asked to wait.
func TestTheClientAsksForAReachCheckAndReadsTheAnswer(t *testing.T) {
	type got struct{ method, path, contentType, body string }
	seen := make(chan got, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- got{r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(b)}
		switch string(b) {
		case `{"port":25565}`:
			io.WriteString(w, `{"result":"refused"}`)
		case `{"port":25566}`:
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"Too many requests; try again later.","code":"rate_limited"}`)
		default:
			io.WriteString(w, `{"result":"maybe"}`)
		}
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, Version: "0.4.18"}
	a, err := c.Reach(context.Background(), 25565)
	if err != nil || a.Result != ReachRefused {
		t.Fatalf("a check: %+v, %v", a, err)
	}
	if g := <-seen; g.method != "POST" || g.path != PathReach || g.contentType != "application/json" || g.body != `{"port":25565}` {
		t.Errorf("the check's request: %+v", g)
	}
	var se *ServiceError
	if _, err := c.Reach(context.Background(), 25566); !errors.As(err, &se) || se.Status != http.StatusTooManyRequests || se.Code != "rate_limited" || se.RetryAfter != time.Minute {
		t.Errorf("a refused check: %v (%+v)", err, se)
	}
	<-seen
	if _, err := c.Reach(context.Background(), 25567); err == nil {
		t.Error("an answer the client doesn't know was taken")
	}
	<-seen
	if _, err := c.Reach(context.Background(), 22); !errors.Is(err, ErrInvalid) {
		t.Errorf("a check of port 22: %v", err)
	}
	select {
	case g := <-seen:
		t.Errorf("a check of port 22 was sent: %+v", g)
	default:
	}
}

func TestTheClientSendsNothingItsCheckRefuses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	h := goodHeartbeat()
	h.OS = "My Distro"
	if err := (Client{URL: srv.URL}).SendHeartbeat(context.Background(), h); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid heartbeat: %v", err)
	}
	if err := (Client{URL: srv.URL}).SendInstall(context.Background(), Install{ID: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid install event: %v", err)
	}
	if err := (Client{URL: "http://stats.example.test"}).SendHeartbeat(context.Background(), goodHeartbeat()); err == nil {
		t.Error("a plain http:// service that isn't this machine was accepted")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the service got %d requests", n)
	}
}

// A redirect could send the report somewhere else; it's an error instead.
func TestTheClientFollowsNoRedirectAndFailsOnARefusal(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer moved.Close()
	if err := (Client{URL: moved.URL}).SendHeartbeat(context.Background(), goodHeartbeat()); err == nil || !strings.Contains(err.Error(), "307") {
		t.Errorf("a redirect: %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the report followed the redirect")
	}
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer refusing.Close()
	if err := (Client{URL: refusing.URL}).SendInstall(context.Background(), goodInstall()); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("a refusal: %v", err)
	}
}

func TestTheClientGivesUpOnAServiceThatDoesNotAnswer(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang }))
	defer srv.Close()
	defer close(hang)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := (Client{URL: srv.URL}).SendHeartbeat(ctx, goodHeartbeat()); err == nil {
		t.Fatal("a service that never answered was taken for one that did")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up after %v", d)
	}
}

func TestTestInstallsAreThoseOfAnActionsJobOrMarked(t *testing.T) {
	if !ActionsJob([]string{"/sbin/init", "/home/runner/actions-runner/cached/bin/Runner.Worker spawnclient 118 121"}) {
		t.Error("a running Actions job wasn't noticed")
	}
	for _, procs := range [][]string{
		nil,
		{"/home/runner/actions-runner/bin/Runner.Listener run --startuptype service"},
		{"vim Runner.Worker"},
		{"/usr/bin/java -jar server.jar"},
	} {
		if ActionsJob(procs) {
			t.Errorf("%v was taken for an Actions job", procs)
		}
	}
	for v, want := range map[string]bool{"1": true, "true": true, "": false, "0": false, "no": false} {
		if got := TestFromEnv(func(string) string { return v }); got != want {
			t.Errorf("PLAYKEEPER_USAGE_TEST=%q: %v", v, got)
		}
	}
}

// README.md's "Usage stats" says what every report carries, field by field,
// and the installer and Settings link people to it.
func TestTheReadmeListsEveryFieldSent(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "\n## Usage stats\n")
	if !ok {
		t.Fatal("README.md has no \"## Usage stats\" section")
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	listed := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		for _, f := range regexp.MustCompile("`([a-zA-Z]+)`").FindAllStringSubmatch(first, -1) {
			listed[f[1]] = true
		}
	}
	sent := map[string]bool{}
	for _, f := range append(append([]string{}, heartbeatFields...), installFields...) {
		sent[f] = true
	}
	for f := range sent {
		if !listed[f] {
			t.Errorf("README.md's usage stats don't list %s, which reports carry", f)
		}
	}
	for f := range listed {
		if !sent[f] {
			t.Errorf("README.md's usage stats list %s, which no report carries", f)
		}
	}
	for _, want := range []string{"DO_NOT_TRACK=1", "PLAYKEEPER_USAGE_STATS=off", "Settings › Playkeeper › Usage stats", "stats.playkeeper.io", "never an IP address"} {
		if !strings.Contains(section, want) {
			t.Errorf("README.md's usage stats don't say %q", want)
		}
	}
}
