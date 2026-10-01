package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/usage"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// statsRecorder stands in for the stats service and keeps each heartbeat.
type statsRecorder struct {
	mu    sync.Mutex
	beats []map[string]any
	srv   *httptest.Server
}

func newStatsRecorder(t *testing.T) *statsRecorder {
	r := &statsRecorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != usage.PathHeartbeat {
			http.NotFound(w, req)
			return
		}
		b, _ := io.ReadAll(req.Body)
		m := map[string]any{}
		json.Unmarshal(b, &m)
		r.mu.Lock()
		r.beats = append(r.beats, m)
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *statsRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.beats)
}

func (r *statsRecorder) beat(i int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beats[i]
}

// heartbeat is the i'th heartbeat as the agent built it.
func (r *statsRecorder) heartbeat(i int) usage.Heartbeat {
	b, _ := json.Marshal(r.beat(i))
	var h usage.Heartbeat
	json.Unmarshal(b, &h)
	return h
}

// quiet reports whether no more heartbeats than n arrive in a moment, long
// enough for one the agent was kicked to send.
func (r *statsRecorder) quiet(n int) bool {
	time.Sleep(300 * time.Millisecond)
	return r.count() == n
}

// sendingEnv is an installed agent, not playkeeper dev, whose heartbeats go
// to a recorder soon after it starts and then every interval.
func sendingEnv(t *testing.T, interval time.Duration, setup func(e *agentEnv)) (*agentEnv, *statsRecorder) {
	t.Helper()
	rec := newStatsRecorder(t)
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.cfg.Dev = false
		e.cfg.StatsURL = rec.srv.URL
		e.cfg.UsageID = "00112233445566778899aabbccddeeff"
		e.cfg.UsageSource, e.cfg.UsageChannel = usage.SourceSite, "hn"
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = 20*time.Millisecond, interval }
		if setup != nil {
			setup(e)
		}
	})
	return e, rec
}

func (e *agentEnv) usageStats() api.UsageStats {
	e.t.Helper()
	code, out := e.call("GET", "/v1/usage-stats", nil)
	if code != 200 {
		e.t.Fatalf("GET /v1/usage-stats: %d %v", code, out)
	}
	return decodeAs[api.UsageStats](e.t, out)
}

// The heartbeat is exactly the report the dashboard shows, and says the
// machine's kind of address and servers, never their names. The first
// server coming online sends one at once, which counts it running.
func TestTheHeartbeatIsWhatTheDashboardShows(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, nil)
	e.waitFor("the first heartbeat", func() bool { return rec.count() == 1 })
	e.create()
	e.waitFor("the first server's heartbeat", func() bool { return rec.count() == 2 })
	st := e.usageStats()
	want := usage.Heartbeat{ID: "00112233445566778899aabbccddeeff", Address: usage.AddressIP, Servers: 1, Running: 1, System: usage.System{
		Version: version.Version, OS: "debian", OSVersion: "13", Arch: runtime.GOARCH, Source: usage.SourceSite, Channel: "hn", Kind: usage.KindDashboard}}
	if st.Report != want {
		t.Errorf("the report is %+v, want %+v", st.Report, want)
	}
	if !st.On || st.Reason != api.UsageDefault || !st.CanChange || st.Service != rec.srv.URL || st.LastSent == nil {
		t.Errorf("usage stats: %+v", st)
	}
	first := rec.beat(0)
	if first["id"] != want.ID || first["servers"] != float64(0) || first["address"] != usage.AddressIP || first["os"] != "debian" {
		t.Errorf("the first heartbeat: %v", first)
	}
	if online := rec.heartbeat(1); online != want {
		t.Errorf("the heartbeat as the first server came online is %+v, want %+v", online, want)
	}
	for i := range 2 {
		b, _ := json.Marshal(rec.beat(i))
		for _, secret := range []string{e.status().Name, e.sid, e.cfg.InstallID, "Debian GNU/Linux"} {
			if secret != "" && strings.Contains(string(b), secret) {
				t.Errorf("heartbeat %d carries %q: %s", i+1, secret, b)
			}
		}
	}
}

// The dashboard's first account, which the panel tells the agent about, and
// the machine's first server online each send a heartbeat at once, the
// first time only. No machine link carries the first account.
func TestEachSetupStepSendsAHeartbeatOnce(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	firstAccount := func() {
		t.Helper()
		if code, out := e.call("POST", usageFirstAccountPath, nil); code != http.StatusNoContent {
			t.Fatalf("the first account: %d %v", code, out)
		}
	}
	firstAccount()
	e.waitFor("the first account's heartbeat", func() bool { return rec.count() == 1 })
	if h := rec.heartbeat(0); h.Servers != 0 || h.Running != 0 || h.Address != usage.AddressIP {
		t.Errorf("the first account's heartbeat: %+v", h)
	}
	firstAccount()
	if !rec.quiet(1) {
		t.Fatalf("the first account told again sent %d heartbeats in all", rec.count())
	}
	e.create()
	e.waitFor("the first server's heartbeat", func() bool { return rec.count() == 2 })
	if h := rec.heartbeat(1); h.Servers != 1 || h.Running != 1 {
		t.Errorf("the first server's heartbeat: %+v", h)
	}
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.stop()
	e.start()
	firstAccount()
	if !rec.quiet(2) {
		t.Errorf("a restarted server and agent sent %d heartbeats in all, want one for each step", rec.count())
	}
	for _, r := range LinkRoutes() {
		if r.Pattern == usageFirstAccountPath {
			t.Errorf("a machine link offers %s %s, which only the dashboard's own panel sends", r.Method, r.Pattern)
		}
	}
}

// While usage stats are off, by root's choice or the switch's, the setup
// steps send nothing.
func TestSetupStepsSendNothingWhileUsageStatsAreOff(t *testing.T) {
	for _, c := range []struct {
		name      string
		setup     func(e *agentEnv)
		switchOff bool
	}{
		{name: "DO_NOT_TRACK for the agent", setup: func(e *agentEnv) {
			e.tweak = func(o *Options) {
				o.UsageFirst, o.UsageInterval = time.Hour, time.Hour
				o.Getenv = func(k string) string { return map[string]string{"DO_NOT_TRACK": "1"}[k] }
			}
		}},
		{name: "off when installed", setup: func(e *agentEnv) {
			e.cfg.UsageStats = "off"
			e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
		}},
		{name: "the switch", switchOff: true, setup: func(e *agentEnv) {
			e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, rec := sendingEnv(t, time.Hour, c.setup)
			if c.switchOff {
				if code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": false, "actor": "admin"}); code != 200 {
					t.Fatalf("the switch: %d %v", code, out)
				}
			}
			if code, out := e.call("POST", usageFirstAccountPath, nil); code != http.StatusNoContent {
				t.Fatalf("the first account: %d %v", code, out)
			}
			e.create()
			if !rec.quiet(0) {
				t.Errorf("%d heartbeats were sent while usage stats were off", rec.count())
			}
		})
	}
}

// A machine whose servers came online before Playkeeper noted the first, as
// one updated from an earlier version, sends no heartbeat for it.
func TestAnUpdatedMachineSendsNoHeartbeatForAnOldFirstServer(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageInterval = -1 }
	})
	e.create()
	if _, err := e.a.db.Exec(`DELETE FROM kv WHERE key = ?`, kvUsageOnline); err != nil {
		t.Fatal(err)
	}
	e.stop()
	e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	e.start()
	if _, noted, err := e.a.kvGet(kvUsageOnline); err != nil || !noted {
		t.Fatalf("the first server online isn't noted after the update: %v %v", noted, err)
	}
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	if !rec.quiet(0) {
		t.Errorf("%d heartbeats for a first server that came online before the update", rec.count())
	}
}

// Root's choices come first, then the switch: playkeeper dev never sends,
// DO_NOT_TRACK in the agent's environment decides, and so does an off
// recorded when Playkeeper was installed.
func TestTheSwitchChangesUsageStatsUnlessRootChose(t *testing.T) {
	for _, c := range []struct {
		name     string
		setup    func(e *agentEnv)
		on       bool
		reason   string
		variable string
	}{
		{"playkeeper dev", func(e *agentEnv) { e.cfg.Dev = true }, false, api.UsageDev, ""},
		{"DO_NOT_TRACK for the agent", func(e *agentEnv) {
			e.tweak = func(o *Options) {
				o.UsageInterval = -1
				o.Getenv = func(k string) string {
					return map[string]string{"DO_NOT_TRACK": "1", "PLAYKEEPER_USAGE_STATS": "on"}[k]
				}
			}
		}, false, api.UsageEnv, usage.EnvDoNotTrack},
		{"off when installed", func(e *agentEnv) { e.cfg.UsageStats = "off" }, false, api.UsageInstall, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				e.cfg.Dev = false
				c.setup(e)
			})
			st := e.usageStats()
			if st.On != c.on || st.Reason != c.reason || st.Variable != c.variable || st.CanChange {
				t.Fatalf("usage stats: %+v", st)
			}
			code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": true, "actor": "admin"})
			if code != 409 || (c.variable != "" && !strings.Contains(out["error"].(string), c.variable)) {
				t.Errorf("the switch: %d %v", code, out)
			}
			if st := e.usageStats(); st.On != c.on || st.Reason != c.reason {
				t.Errorf("the refused switch changed them: %+v", st)
			}
		})
	}

	e := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.Dev, e.cfg.UsageStats = false, "on" })
	if st := e.usageStats(); !st.On || st.Reason != api.UsageInstall || !st.CanChange {
		t.Fatalf("on when installed: %+v", st)
	}
	code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": false, "actor": "admin"})
	if code != 200 || out["on"] != false || out["reason"] != api.UsageSettings {
		t.Fatalf("turning them off: %d %v", code, out)
	}
	if code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": true}); code != 400 {
		t.Errorf("a switch with no actor: %d %v", code, out)
	}
	if code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": true, "actor": "admin", "url": "https://elsewhere.example"}); code != 400 {
		t.Errorf("a switch that also names a service: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'usage_stats.off' AND actor = 'admin' AND result = 'succeeded'`); n != 1 {
		t.Errorf("the audit log records %d switches off by admin, want 1: %v", n, e.auditActions())
	}
	e.stop()
	e.start()
	if st := e.usageStats(); st.On || st.Reason != api.UsageSettings {
		t.Errorf("after a restart: %+v", st)
	}
}

// Off, the agent sends nothing; turned on with the switch, it sends at once.
func TestTurnedOffNothingIsSentAndTurningThemOnSendsAtOnce(t *testing.T) {
	e, rec := sendingEnv(t, 50*time.Millisecond, func(e *agentEnv) { e.cfg.UsageStats = "on" })
	e.waitFor("a heartbeat", func() bool { return rec.count() >= 1 })
	if code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": false, "actor": "admin"}); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	time.Sleep(100 * time.Millisecond)
	n := rec.count()
	time.Sleep(300 * time.Millisecond)
	if got := rec.count(); got != n {
		t.Fatalf("%d heartbeats were sent while usage stats were off", got-n)
	}
	e.stop()
	e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	e.start()
	if code, out := e.call("PUT", "/v1/usage-stats", map[string]any{"on": true, "actor": "admin"}); code != 200 || out["on"] != true {
		t.Fatalf("%d %v", code, out)
	}
	e.waitFor("the heartbeat the switch sends", func() bool { return rec.count() > n })
}

// An install from before 0.4.4 has no ID from the installer: the agent makes
// one of its own, once, unrelated to the install ID backups carry.
func TestAnInstallWithoutAUsageIDGetsOneOfItsOwnOnce(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.Dev = false })
	id := e.usageStats().Report.ID
	if !usage.IsID(id) || id == e.cfg.InstallID || strings.Contains(e.cfg.InstallID, id) {
		t.Fatalf("the agent's own ID is %q", id)
	}
	e.stop()
	e.start()
	if again := e.usageStats().Report.ID; again != id {
		t.Errorf("the ID changed from %s to %s after a restart", id, again)
	}
}

// The project's own tests mark their reports, and a joined machine says it
// is one.
func TestTestInstallsAndJoinedMachinesSayWhatTheyAre(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.Dev = false })
	if st := e.usageStats(); st.Report.Test || st.Report.Kind != usage.KindDashboard {
		t.Fatalf("a plain install: %+v", st.Report)
	}
	for name, setup := range map[string]func(e *agentEnv){
		"marked when installed": func(e *agentEnv) { e.cfg.UsageTest = true },
		"the offline harness":   func(e *agentEnv) { e.tweak = func(o *Options) { o.UsageInterval, o.OfflineModeTest = -1, true } },
		"an Actions job": func(e *agentEnv) {
			e.tweak = func(o *Options) {
				o.UsageInterval = -1
				o.Processes = func() []string {
					return []string{"/home/runner/actions-runner/cached/bin/Runner.Worker spawnclient 1 2"}
				}
			}
		},
		"PLAYKEEPER_USAGE_TEST": func(e *agentEnv) {
			e.tweak = func(o *Options) {
				o.UsageInterval = -1
				o.Getenv = func(k string) string { return map[string]string{"PLAYKEEPER_USAGE_TEST": "1"}[k] }
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				e.cfg.Dev = false
				setup(e)
			})
			if !e.usageStats().Report.Test {
				t.Error("not marked as a test")
			}
		})
	}
	joined := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.Dev, e.cfg.NoPanel = false, true })
	if st := joined.usageStats(); st.Report.Kind != usage.KindJoined {
		t.Errorf("a machine installed to join a dashboard: %+v", st.Report)
	}
}
