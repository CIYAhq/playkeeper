package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/discord"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/schedule"
)

// sentAlert is an alert as Discord got it.
type sentAlert struct {
	Kind  discord.Kind
	Title string
}

// alertKinds names the kind of each alert title the agent posts.
var alertKinds = map[string]discord.Kind{
	"Server crashed":               discord.KindCrash,
	"Server crashed and stays off": discord.KindCrash,
	"Server didn't start":          discord.KindCrash,
	"Back online":                  discord.KindRecovered,
	"Low disk space":               discord.KindLowDisk,
	"Backup failed":                discord.KindBackupFailed,
	"Backup finished":              discord.KindBackupSucceeded,
	"Server started":               discord.KindStarted,
	"Server stopped":               discord.KindStopped,
	"Playkeeper update available":  discord.KindUpdateAvailable,
	"Minecraft update available":   discord.KindUpdateAvailable,
	"Player joined":                discord.KindPlayerJoined,
	"Player left":                  discord.KindPlayerLeft,
	"Join request":                 discord.KindJoinRequested,
}

// mark is how many requests the fake Discord has had so far.
func (f *fakeHook) mark() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// alertsSince lists the alerts posted after the first from requests, in
// order. The live status message and the connection check have titles no
// alert has, so they are left out.
func (f *fakeHook) alertsSince(t *testing.T, from int) []sentAlert {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentAlert
	for _, r := range f.got[from:] {
		if r.Method != http.MethodPost {
			continue
		}
		var msg struct{ Embeds []struct{ Title string } }
		if err := json.Unmarshal([]byte(r.Raw), &msg); err != nil {
			t.Fatalf("a message Discord could not read: %v: %s", err, r.Raw)
		}
		for _, em := range msg.Embeds {
			if k, ok := alertKinds[em.Title]; ok {
				out = append(out, sentAlert{k, em.Title})
			}
		}
	}
	return out
}

// Each sequence of events in a server's life runs once with every alert
// switched on and once with the defaults, and Discord must get exactly the
// alerts the sequence calls for that are switched on, in order, and show
// the server in the live status as the sequence leaves it. The quiet period
// holds back a second alert about the same thing within five minutes, as it
// does for real.
func TestDiscordAlertSequences(t *testing.T) {
	var (
		started      = sentAlert{discord.KindStarted, "Server started"}
		stopped      = sentAlert{discord.KindStopped, "Server stopped"}
		crashed      = sentAlert{discord.KindCrash, "Server crashed"}
		gaveUp       = sentAlert{discord.KindCrash, "Server crashed and stays off"}
		didntStart   = sentAlert{discord.KindCrash, "Server didn't start"}
		back         = sentAlert{discord.KindRecovered, "Back online"}
		lowDisk      = sentAlert{discord.KindLowDisk, "Low disk space"}
		backedUp     = sentAlert{discord.KindBackupSucceeded, "Backup finished"}
		backupFailed = sentAlert{discord.KindBackupFailed, "Backup failed"}
	)
	op := func(e *agentEnv, verb string, body map[string]any) {
		e.t.Helper()
		if body == nil {
			body = map[string]any{}
		}
		body["actor"] = "admin"
		code, out := e.call("POST", e.sp("/"+verb), body)
		if code != 202 {
			e.t.Fatalf("%s: %d %v", verb, code, out)
		}
		if o := e.waitOp(out["id"].(string)); o.Status != api.OpSucceeded {
			e.t.Fatalf("%s: %+v", verb, o)
		}
	}
	portTaken := func(e *agentEnv, taken bool) {
		e.fd.mu.Lock()
		defer e.fd.mu.Unlock()
		e.fd.startErr = ""
		if taken {
			e.fd.startErr = "driver failed programming external connectivity: Bind for 0.0.0.0:25565 failed: port is already allocated"
		}
	}
	failedStarts := func(e *agentEnv) int {
		return e.countRows(`SELECT COUNT(*) FROM operations WHERE kind IN ('recover', 'auto-restart') AND status = 'failed'`)
	}
	// crash kills the server once it has been online; a line after "Done"
	// keeps the follower from reading that line again as a new start.
	crash := func(e *agentEnv) {
		e.t.Helper()
		n := e.crashEvents()
		e.fd.addLog("[12:00:05 INFO]: Timings Reset")
		e.fd.crash(137)
		e.waitFor("the crash counted", func() bool { return e.crashEvents() == n+1 })
	}
	gone := func(e *agentEnv) {
		e.t.Helper()
		if err := e.a.docker.ContainerRemove(context.Background(), e.cname(), true); err != nil {
			e.t.Fatal(err)
		}
	}
	lastError := func(e *agentEnv, want string) {
		e.t.Helper()
		e.waitFor(want, func() bool { return strings.Contains(e.status().LastError, want) && !e.a.busy() })
	}
	// crashLogged has the server log a crash, then its shutdown, as Paper
	// does after many unexpected exits, and exit.
	crashLogged := func(e *agentEnv) {
		e.fd.addLog("[12:00:30 ERROR]: Encountered an unexpected exception")
		e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
		e.fd.addLog("[12:00:31 INFO]: Stopping server")
		e.fd.crash(1)
	}
	// outOfMemory has Java run out of memory and the server log its shutdown
	// with no crash line of its own, and exit.
	outOfMemory := func(e *agentEnv) {
		e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
		e.fd.addLog("[12:00:31 INFO]: Stopping server")
		e.fd.crash(1)
	}
	// redeliverDone hands the collector the crashed run's "Done" line again,
	// as a log stream that attached while the run was going would.
	redeliverDone := func(e *agentEnv) {
		e.t.Helper()
		s := e.srv()
		c, err := s.docker.ContainerInspect(context.Background(), s.containerName())
		if err != nil {
			e.t.Fatal(err)
		}
		var done *fakeLine
		e.fd.mu.Lock()
		for _, l := range e.fd.byID[c.ID].logs {
			if strings.Contains(l.text, "Done (") {
				done = &l
			}
		}
		e.fd.mu.Unlock()
		if done == nil {
			e.t.Fatal("the run logged no Done line")
		}
		s.mu.Lock()
		run := s.runStartedAt
		s.mu.Unlock()
		raw := done.ts.Format(time.RFC3339Nano) + " " + done.text
		s.ingest(c.ID, docker.LogLine{TS: done.ts, Stream: 1, Text: done.text, Raw: raw}, run, true, time.Time{})
	}
	// logRead waits until the follower has read the stopped server's log to
	// its end, when the live status can tell how it stopped.
	logRead := func(e *agentEnv) {
		e.t.Helper()
		e.waitFor("the log read to its end", func() bool {
			s := e.srv()
			c, err := s.docker.ContainerInspect(context.Background(), s.containerName())
			fin, _ := c.State.Finished()
			s.mu.Lock()
			defer s.mu.Unlock()
			return err == nil && !c.State.Running && !s.followEnded[c.ID].Before(fin)
		})
	}

	cases := []struct {
		name string
		// backoff is how long Playkeeper waits before an automatic start;
		// the tests' default is none.
		backoff time.Duration
		// stopFirst stops the server before Discord is connected.
		stopFirst bool
		// reconcile, when set, is how often the reconcile loop looks: an
		// hour keeps it from handling an exit, as in the seconds before it
		// does.
		reconcile time.Duration
		steps     func(e *agentEnv)
		want      []sentAlert
		// status is how the live status shows the server afterwards, as the
		// dashboard does: a server whose starts failed has no container
		// left, so it shows as offline.
		status discord.State
	}{
		{name: "a start", stopFirst: true, steps: func(e *agentEnv) { op(e, "start", nil) }, want: []sentAlert{started}, status: discord.StateOnline},
		{name: "starts fail until Playkeeper gives up", steps: func(e *agentEnv) {
			portTaken(e, true)
			gone(e)
			lastError(e, "stopped trying to start")
		}, want: []sentAlert{didntStart}, status: discord.StateOffline},
		{name: "a start fails, then one works", backoff: 2 * time.Second, steps: func(e *agentEnv) {
			portTaken(e, true)
			gone(e)
			e.waitFor("a failed start", func() bool { return failedStarts(e) == 1 })
			portTaken(e, false)
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{started}, status: discord.StateOnline},
		{name: "a crash, then a restart that works", steps: func(e *agentEnv) {
			crash(e)
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{crashed, back}, status: discord.StateOnline},
		{name: "crashes until Playkeeper gives up", steps: func(e *agentEnv) {
			for i := 1; i <= maxCrashes; i++ {
				e.waitFor("online before the crash", e.onlineIdle)
				crash(e)
			}
			lastError(e, "stopped restarting")
		}, want: []sentAlert{crashed, back, gaveUp}, status: discord.StateCrashed},
		{name: "a crash, then restarts fail until Playkeeper gives up", steps: func(e *agentEnv) {
			portTaken(e, true)
			crash(e)
			lastError(e, "stopped trying to start")
		}, want: []sentAlert{crashed, didntStart}, status: discord.StateOffline},
		{name: "a crash, its Done line delivered again, then a restart", backoff: 2 * time.Second, steps: func(e *agentEnv) {
			crash(e)
			ready := func() int { return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_ready'`) }
			n := ready()
			redeliverDone(e)
			if ready() != n {
				e.t.Fatal("the line delivered again was stored as a new one, so it wasn't the same line")
			}
			if st := e.srv().discordStatus(context.Background()).State; st != discord.StateCrashed {
				e.t.Fatalf("after its Done line came again, the live status shows the server %s, want crashed", st)
			}
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{crashed, back}, status: discord.StateOnline},
		{name: "a crash, a restart that fails, then one that works", backoff: 2 * time.Second, steps: func(e *agentEnv) {
			portTaken(e, true)
			crash(e)
			e.waitFor("a failed restart", func() bool { return failedStarts(e) == 1 })
			portTaken(e, false)
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{crashed, back}, status: discord.StateOnline},
		{name: "a crash of a server meant to be off", steps: func(e *agentEnv) {
			if err := e.srv().setDesired(api.DesiredStopped); err != nil {
				e.t.Fatal(err)
			}
			crash(e)
		}, want: []sentAlert{crashed}, status: discord.StateCrashed},
		{name: "a stop", steps: func(e *agentEnv) { op(e, "stop", nil) }, want: []sentAlert{stopped}, status: discord.StateOffline},
		{name: "a restart", steps: func(e *agentEnv) {
			op(e, "restart", nil)
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{stopped, started}, status: discord.StateOnline},
		{name: "a scheduled restart", steps: func(e *agentEnv) {
			restart := schedule.Operation{Kind: schedule.OpRestart, Actor: schedule.Actor("qrstuvwxyz"), ScheduleID: "qrstuvwxyz"}
			if _, err := (scheduleServer{e.srv()}).Run(context.Background(), restart); err != nil {
				e.t.Fatalf("scheduled restart: %v", err)
			}
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{stopped, started}, status: discord.StateOnline},
		// A server falls asleep whenever it's empty, so that isn't news; a
		// player waking it starts it as usual.
		{name: "falling asleep", steps: func(e *agentEnv) { e.putToSleep() }, status: discord.StateOffline},
		{name: "falling asleep, then a player wakes it", steps: func(e *agentEnv) {
			e.putToSleep()
			e.srv().wakeFor("Alex")
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{started}, status: discord.StateOnline},
		{name: "a clean stop outside Playkeeper", steps: func(e *agentEnv) {
			e.fd.externalStop()
			e.waitFor("the stop seen", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'server_stopped_externally'`) == 1
			})
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{stopped, started}, status: discord.StateOnline},
		{name: "a backup", steps: func(e *agentEnv) {
			if o := e.backupNow(nil); o.Status != api.OpSucceeded {
				e.t.Fatalf("backup: %+v", o)
			}
		}, want: []sentAlert{backedUp}, status: discord.StateOnline},
		{name: "a backup with the server stopped", steps: func(e *agentEnv) {
			if o := e.backupNow(map[string]any{"stopped": true}); o.Status != api.OpSucceeded {
				e.t.Fatalf("backup: %+v", o)
			}
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{stopped, started, backedUp}, status: discord.StateOnline},
		{name: "a backup without room", steps: func(e *agentEnv) {
			e.diskFree.Store(1 << 20)
			e.waitFor("the disk seen as low", func() bool {
				e.a.disc.mu.Lock()
				defer e.a.disc.mu.Unlock()
				return e.a.disc.lowDisk
			})
			if o := e.backupNow(nil); o.Status != api.OpFailed {
				e.t.Fatalf("backup without room: %+v", o)
			}
		}, want: []sentAlert{lowDisk, backupFailed}, status: discord.StateOnline},
		{name: "a crash that logged a shutdown", steps: func(e *agentEnv) {
			n := e.crashEvents()
			crashLogged(e)
			e.waitFor("the crash counted", func() bool { return e.crashEvents() == n+1 })
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{crashed, back}, status: discord.StateOnline},
		{name: "out of memory, then Stopping server", backoff: 2 * time.Second, steps: func(e *agentEnv) {
			n := e.crashEvents()
			outOfMemory(e)
			e.waitFor("the crash counted", func() bool { return e.crashEvents() == n+1 })
			if st := e.srv().discordStatus(context.Background()).State; st != discord.StateCrashed {
				e.t.Fatalf("after running out of memory, the live status shows the server %s, want crashed", st)
			}
			e.waitFor("online again", e.onlineIdle)
		}, want: []sentAlert{crashed, back}, status: discord.StateOnline},
		{name: "out of memory, then Stopping server, before the reconcile loop sees it", reconcile: time.Hour, steps: func(e *agentEnv) {
			outOfMemory(e)
			logRead(e)
		}, status: discord.StateCrashed},
		{name: "a crash, before the reconcile loop sees it", reconcile: time.Hour, steps: func(e *agentEnv) {
			e.fd.addLog("[12:00:05 INFO]: Timings Reset")
			e.fd.crash(137)
			logRead(e)
		}, status: discord.StateCrashed},
		{name: "a crash that logged a shutdown, before the reconcile loop sees it", reconcile: time.Hour, steps: func(e *agentEnv) {
			crashLogged(e)
			logRead(e)
		}, status: discord.StateCrashed},
		{name: "a clean stop outside Playkeeper, before the reconcile loop sees it", reconcile: time.Hour, steps: func(e *agentEnv) {
			e.fd.externalStop()
			logRead(e)
		}, status: discord.StateOffline},
	}
	passes := []struct {
		name   string
		alerts discord.Alerts
	}{
		{"every alert", discord.Kinds()},
		{"defaults", discord.DefaultAlerts()},
	}
	for _, c := range cases {
		for _, p := range passes {
			t.Run(c.name+"/"+p.name, func(t *testing.T) {
				f := startFakeHook(t)
				e := newAgentEnv(t)
				e.stop()
				e.discordClient = f.client()
				if c.backoff > 0 {
					e.crashBackoff = []time.Duration{c.backoff}
				}
				if c.reconcile > 0 {
					e.reconcileInterval = c.reconcile
				}
				e.start()
				e.create()
				if c.stopFirst {
					op(e, "stop", nil)
				}
				e.connectDiscord()
				var on []string
				for _, k := range p.alerts {
					on = append(on, string(k))
				}
				if code, out := e.call("PUT", "/v1/discord", map[string]any{"alerts": on, "liveStatus": false, "actor": "admin"}); code != 200 {
					t.Fatalf("alert settings: %d %v", code, out)
				}
				from := f.mark()
				c.steps(e)
				var want []sentAlert
				for _, a := range c.want {
					if slices.Contains(p.alerts, a.Kind) {
						want = append(want, a)
					}
				}
				e.waitFor(fmt.Sprintf("%d alerts", len(want)), func() bool { return len(f.alertsSince(t, from)) >= len(want) })
				time.Sleep(700 * time.Millisecond)
				if got := f.alertsSince(t, from); !slices.Equal(got, want) {
					t.Fatalf("Discord got\n%v\nwant\n%v", got, want)
				}
				if got := e.srv().discordStatus(context.Background()).State; got != c.status {
					t.Fatalf("the live status shows the server %s, want %s", got, c.status)
				}
			})
		}
	}
}
