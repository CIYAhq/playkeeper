package agent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/usage"
)

// internetCheck asks for an internet check of the env's server, as the
// dashboard does by itself (auto) or when someone asks.
func (e *agentEnv) internetCheck(auto bool) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", "/v1/servers/"+e.sid+"/internet-check", map[string]any{"actor": "admin", "auto": auto})
}

// answering waits for the env's server to answer on the machine.
func (e *agentEnv) answering() {
	e.t.Helper()
	e.waitFor("the server answering on the machine", func() bool { return e.status().Reachable })
}

// checkDone waits for the server's internet check to be done, with what
// done says, and returns it.
func (e *agentEnv) checkDone(done func(api.InternetCheck) bool) api.InternetCheck {
	e.t.Helper()
	var c api.InternetCheck
	e.waitFor("the internet check", func() bool {
		st := e.status().InternetCheck
		if st == nil || st.Checking {
			return false
		}
		c = *st
		return done(c)
	})
	return c
}

func checkSays(result string) func(api.InternetCheck) bool {
	return func(c api.InternetCheck) bool { return c.Result == result }
}

// While usage stats are on, the dashboard's own checks ask the stats
// service, with the port alone, at most hourly; someone can ask again a few
// seconds after a check.
func TestWhileUsageStatsAreOnTheDashboardChecksFromTheInternet(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	e.create()
	e.answering()
	if c := e.status().InternetCheck; c == nil || !c.Auto || c.Checking || c.Result != "" || c.StepsURL != "https://playkeeper.io/ports" || c.Provider != "" {
		t.Fatalf("before a check: %+v", c)
	}
	if code, out := e.internetCheck(true); code != http.StatusAccepted || out["internetCheck"] == nil {
		t.Fatalf("the dashboard's check: %d %v", code, out)
	}
	if c := e.checkDone(checkSays(api.InternetCheckReachable)); c.CheckedAt == nil || c.Problem != "" || c.RetryAt != nil {
		t.Errorf("the check: %+v", c)
	}
	want := fmt.Sprintf(`{"port":%d}`, e.status().GamePort)
	if got := rec.checked(); len(got) != 1 || got[0] != want {
		t.Fatalf("the stats service was asked %q, want only %s", got, want)
	}
	e.internetCheck(true)
	e.internetCheck(false)
	time.Sleep(200 * time.Millisecond)
	if got := rec.checked(); len(got) != 1 {
		t.Fatalf("checks within the hour and a moment after asked %d times in all", len(got))
	}
	e.later(internetCheckAgain + time.Second)
	e.answering()
	e.internetCheck(true)
	time.Sleep(200 * time.Millisecond)
	if got := rec.checked(); len(got) != 1 {
		t.Fatalf("the dashboard's own check within the hour asked %d times in all", len(got))
	}
	e.internetCheck(false)
	e.waitFor("the check someone asked for", func() bool { return len(rec.checked()) == 2 })
	e.later(internetCheckEvery)
	e.answering()
	e.internetCheck(true)
	e.waitFor("the dashboard's check an hour on", func() bool { return len(rec.checked()) == 3 })
	if rec.count() != 1 {
		t.Errorf("checks sent heartbeats: %d in all", rec.count())
	}
}

// With usage stats off the dashboard doesn't check by itself, but someone
// can ask, and nothing else is sent.
func TestWithUsageStatsOffOnlyAskingChecksFromTheInternet(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) {
			o.UsageFirst, o.UsageInterval = 20*time.Millisecond, time.Hour
			o.Getenv = func(k string) string { return map[string]string{"DO_NOT_TRACK": "1"}[k] }
		}
	})
	e.create()
	e.answering()
	if c := e.status().InternetCheck; c == nil || c.Auto {
		t.Fatalf("before a check: %+v", c)
	}
	if code, out := e.internetCheck(true); code != http.StatusConflict || !strings.Contains(out["error"].(string), "only when someone asks") {
		t.Fatalf("the dashboard's own check: %d %v", code, out)
	}
	if got := rec.checked(); len(got) != 0 {
		t.Fatalf("the stats service was asked %q", got)
	}
	if code, out := e.internetCheck(false); code != http.StatusAccepted {
		t.Fatalf("a check someone asked for: %d %v", code, out)
	}
	e.checkDone(checkSays(api.InternetCheckReachable))
	if rec.count() != 0 {
		t.Errorf("%d heartbeats were sent while usage stats were off", rec.count())
	}
}

// A check says what the stats service found, keeps it while a later check
// can't be made and says why, and doesn't ask again before the service said
// to.
func TestACheckSaysWhatItFoundOrWhyItCouldntBeMade(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	e.create()
	e.answering()
	again := func() {
		t.Helper()
		e.later(internetCheckAgain + time.Second)
		e.answering()
		if code, out := e.internetCheck(false); code != http.StatusAccepted {
			t.Fatalf("a check: %d %v", code, out)
		}
	}
	rec.answer(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"result":"timeout"}`) })
	again()
	e.checkDone(checkSays(api.InternetCheckTimeout))
	rec.answer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"Too many requests; try again later.","code":"rate_limited"}`)
	})
	again()
	c := e.checkDone(func(c api.InternetCheck) bool { return c.Problem != "" })
	if c.Result != api.InternetCheckTimeout || c.RetryAt == nil || !strings.Contains(c.Problem, "as often as it will") {
		t.Errorf("a check the service refused: %+v", c)
	}
	again()
	time.Sleep(200 * time.Millisecond)
	if got := rec.checked(); len(got) != 2 {
		t.Fatalf("a check before the service said to try again asked it: %d checks in all", len(got))
	}
	e.later(10 * time.Minute)
	rec.answer(func(w http.ResponseWriter, _ *http.Request) {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close()
		}
	})
	again()
	e.waitFor("the check that couldn't reach the service", func() bool { return len(rec.checked()) == 3 })
	c = e.checkDone(func(c api.InternetCheck) bool { return strings.Contains(c.Problem, "couldn't ask the stats service") })
	if c.Result != api.InternetCheckTimeout {
		t.Errorf("the check that couldn't reach the service: %+v", c)
	}
	rec.answer(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"result":"refused"}`) })
	again()
	if c := e.checkDone(checkSays(api.InternetCheckRefused)); c.Problem != "" || c.RetryAt != nil {
		t.Errorf("a check that worked again: %+v", c)
	}
}

// refusingTransport fails every request, counting them.
type refusingTransport struct{ tries atomic.Int32 }

func (rt *refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.tries.Add(1)
	return nil, errors.New("no requests leave these tests")
}

// A stats service without the check, as the live one is until the owner
// redeploys it, answers 404; its proxy answers 502 while it's down; and it
// can't be reached at all. None of them is a check that found the port
// closed: the status says only why there's no answer.
func TestAServiceThatCantCheckNeverSaysThePortIsClosed(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	e.create()
	e.answering()
	for _, c := range []struct {
		name, problem string
		answer        http.HandlerFunc
	}{
		{"a service from before the check", "answered HTTP 404", http.NotFound},
		{"its proxy while it's down", "answered HTTP 502", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "<html><body>Bad Gateway</body></html>")
		}},
		{"a service that can't be reached", "couldn't ask the stats service", func(w http.ResponseWriter, _ *http.Request) {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				conn.Close()
			}
		}},
	} {
		rec.answer(c.answer)
		e.later(internetCheckAgain + time.Second)
		e.answering()
		if code, out := e.internetCheck(false); code != http.StatusAccepted {
			t.Fatalf("%s: %d %v", c.name, code, out)
		}
		got := e.checkDone(func(ic api.InternetCheck) bool { return strings.Contains(ic.Problem, c.problem) })
		if got.Result != "" || got.CheckedAt != nil {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if n := len(rec.checked()); n != 3 {
		t.Errorf("the stats service was asked %d times, want 3", n)
	}
}

// A test install and playkeeper dev never ask the project's stats service
// for a check, so the dashboard offers none.
func TestTestInstallsAndDevNeverAskTheProjectsStatsService(t *testing.T) {
	for name, setup := range map[string]func(e *agentEnv){
		"playkeeper dev": func(e *agentEnv) { e.cfg.Dev = true },
		"a test install": func(e *agentEnv) { e.cfg.Dev, e.cfg.UsageTest = false, true },
		"an Actions job": func(e *agentEnv) {
			e.cfg.Dev = false
			e.tweak = func(o *Options) {
				o.Processes = func() []string {
					return []string{"/home/runner/actions-runner/cached/bin/Runner.Worker spawnclient 1 2"}
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			refuse := &refusingTransport{}
			e := newAgentEnvWith(t, func(e *agentEnv) {
				setup(e)
				e.cfg.StatsURL = usage.DefaultURL
				tweak := e.tweak
				e.tweak = func(o *Options) {
					if tweak != nil {
						tweak(o)
					}
					o.UsageClient = &http.Client{Transport: refuse}
				}
			})
			e.create()
			e.answering()
			if c := e.status().InternetCheck; c != nil {
				t.Errorf("the dashboard is offered a check: %+v", c)
			}
			if code, out := e.internetCheck(false); code != http.StatusConflict {
				t.Errorf("a check: %d %v", code, out)
			}
			time.Sleep(100 * time.Millisecond)
			if n := refuse.tries.Load(); n != 0 {
				t.Errorf("%d requests to %s", n, usage.DefaultURL)
			}
		})
	}
}

// Only a server that answers on the machine, on a port the stats service
// checks, is checked.
func TestOnlyAServerAnsweringOnAPortTheServiceChecksIsChecked(t *testing.T) {
	e, rec := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.cfg.GamePort = 30000
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	e.create()
	e.answering()
	if c := e.status().InternetCheck; c != nil {
		t.Errorf("a server on port 30000 is offered a check: %+v", c)
	}
	if code, out := e.internetCheck(false); code != http.StatusConflict || !strings.Contains(out["error"].(string), "30000") {
		t.Errorf("a check of port 30000: %d %v", code, out)
	}

	stopped, rec2 := sendingEnv(t, time.Hour, func(e *agentEnv) {
		e.tweak = func(o *Options) { o.UsageFirst, o.UsageInterval = time.Hour, time.Hour }
	})
	stopped.create()
	if op := stopped.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	stopped.waitFor("the server not answering", func() bool { return !stopped.status().Reachable })
	if code, out := stopped.internetCheck(false); code != http.StatusConflict || !strings.Contains(out["error"].(string), "isn't answering") {
		t.Errorf("a check of a stopped server: %d %v", code, out)
	}
	if n := len(rec.checked()) + len(rec2.checked()); n != 0 {
		t.Errorf("the stats service was asked %d times", n)
	}
}

// The check names the provider the machine runs at, with its steps for
// opening the port.
func TestTheCheckNamesTheMachinesProvider(t *testing.T) {
	e, _ := sendingEnv(t, time.Hour, func(e *agentEnv) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "run/cloud-init"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "run/cloud-init/cloud-id"), []byte("oracle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.tweak = func(o *Options) {
			o.UsageFirst, o.UsageInterval = time.Hour, time.Hour
			o.ProviderRoot = root
		}
	})
	e.create()
	if c := e.status().InternetCheck; c == nil || c.Provider != "Oracle Cloud" || c.StepsURL != "https://playkeeper.io/ports#oracle-cloud" {
		t.Errorf("the check: %+v", c)
	}
}

// The dashboard reads the stats service's own results.
func TestInternetCheckResultsAreTheStatsServices(t *testing.T) {
	for got, want := range map[string]string{
		api.InternetCheckReachable:    usage.ReachReachable,
		api.InternetCheckRefused:      usage.ReachRefused,
		api.InternetCheckTimeout:      usage.ReachTimeout,
		api.InternetCheckUnreachable:  usage.ReachUnreachable,
		api.InternetCheckNotMinecraft: usage.ReachNotMinecraft,
	} {
		if got != want {
			t.Errorf("the dashboard reads %q for the service's %q", got, want)
		}
	}
}
