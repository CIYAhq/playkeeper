package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	added  chan struct{}
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, added: make(chan struct{}, 1)}
}

type fakeTimer struct {
	clock *fakeClock
	at    time.Time
	ch    chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	i := slices.Index(t.clock.timers, t)
	if i < 0 {
		return false
	}
	t.clock.timers = slices.Delete(t.clock.timers, i, i+1)
	return true
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t
	}
	c.timers = append(c.timers, t)
	select {
	case c.added <- struct{}{}:
	default:
	}
	return t
}

// advance lets the runner work, firing its timers in order, until it waits
// for a time after limit. The clock is then left at limit.
func (c *fakeClock) advance(t *testing.T, limit time.Time) {
	t.Helper()
	for {
		next := c.waitForTimer(t)
		c.mu.Lock()
		if next.After(limit) {
			c.now = limit
			c.mu.Unlock()
			return
		}
		c.now = next
		keep := c.timers[:0]
		for _, x := range c.timers {
			if x.at.After(next) {
				keep = append(keep, x)
			} else {
				x.ch <- next
			}
		}
		c.timers = keep
		c.mu.Unlock()
	}
}

func (c *fakeClock) waitForTimer(t *testing.T) time.Time {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		if len(c.timers) > 0 {
			first := c.timers[0].at
			for _, x := range c.timers {
				if x.at.Before(first) {
					first = x.at
				}
			}
			c.mu.Unlock()
			return first
		}
		c.mu.Unlock()
		select {
		case <-c.added:
		case <-timeout:
			t.Fatal("the runner never went back to waiting")
		}
	}
}

type fakeStore struct {
	mu        sync.Mutex
	schedules []Schedule
	saved     []savedRun
	failSaves bool
}

type savedRun struct {
	id  string
	run Run
}

func (f *fakeStore) load(context.Context) ([]Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.schedules), nil
}

func (f *fakeStore) save(_ context.Context, id string, run Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, savedRun{id, run})
	if f.failSaves {
		return errors.New("disk full")
	}
	for i := range f.schedules {
		if f.schedules[i].ID == id {
			r := run
			f.schedules[i].LastRun = &r
		}
	}
	return nil
}

func (f *fakeStore) runs(id string) []Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Run
	for _, s := range f.saved {
		if s.id == id {
			out = append(out, s.run)
		}
	}
	return out
}

func (f *fakeStore) update(id string, edit func(*Schedule)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.schedules {
		if f.schedules[i].ID == id {
			edit(&f.schedules[i])
		}
	}
}

type fakeServer struct {
	mu      sync.Mutex
	clock   *fakeClock
	loc     *time.Location
	state   ServerState
	replies map[string]string
	runErrs []error
	// busyUntil is when another operation lets go of the server.
	busyUntil time.Time
	timeline  []string
	commands  []string
	ops       []Operation
}

func (f *fakeServer) State(context.Context) (ServerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.state
	st.Busy = f.clock.Now().Before(f.busyUntil)
	return st, nil
}

func (f *fakeServer) setState(st ServerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = st
}

func (f *fakeServer) Command(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	f.timeline = append(f.timeline, f.clock.Now().In(f.loc).Format("15:04:05 ")+describe(cmd))
	return f.replies[cmd], nil
}

func (f *fakeServer) Run(_ context.Context, op Operation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	f.timeline = append(f.timeline, fmt.Sprintf("%s op %s %s", f.clock.Now().In(f.loc).Format("15:04:05"), op.Kind, op.Actor))
	if f.clock.Now().Before(f.busyUntil) {
		return "", fmt.Errorf("start %s: %w", op.Kind, ErrBusy)
	}
	if len(f.runErrs) > 0 {
		err := f.runErrs[0]
		f.runErrs = f.runErrs[1:]
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("op%d", len(f.ops)), nil
}

func (f *fakeServer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.timeline)
}

// describe shows a tellraw command as its text.
func describe(cmd string) string {
	body, ok := strings.CutPrefix(cmd, "tellraw @a ")
	if !ok {
		return cmd
	}
	var c struct{ Text string }
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return "INVALID " + cmd
	}
	return c.Text
}

type harness struct {
	t      *testing.T
	loc    *time.Location
	clock  *fakeClock
	store  *fakeStore
	server *fakeServer
	runner *Runner
	stop   func()
}

const testDay = "2026-06-01"

func newHarness(t *testing.T, start string, list ...Schedule) *harness {
	t.Helper()
	loc := mustLoc(t, "Europe/Berlin")
	h := &harness{t: t, loc: loc}
	h.clock = newFakeClock(h.at(start))
	h.store = &fakeStore{schedules: list}
	h.server = &fakeServer{clock: h.clock, loc: loc, replies: map[string]string{},
		state: ServerState{Running: true, Players: 3, PlayersKnown: true}}
	return h
}

func (h *harness) at(clock string) time.Time {
	day, rest, ok := strings.Cut(clock, " ")
	if !ok {
		day, rest = testDay, clock
	}
	if len(rest) == 5 {
		rest += ":00"
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+rest, h.loc)
	if err != nil {
		h.t.Fatal(err)
	}
	return t
}

func (h *harness) start() {
	h.t.Helper()
	r, err := NewRunner(Config{ServerID: "srv1", Load: h.store.load, Save: h.store.save, Server: h.server,
		Now: h.clock.Now, NewTimer: h.clock.NewTimer, Logf: h.t.Logf})
	if err != nil {
		h.t.Fatal(err)
	}
	h.runner = r
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				h.t.Error("the runner did not stop")
			}
		})
	}
	h.t.Cleanup(h.stop)
}

func (h *harness) until(clock string) { h.t.Helper(); h.clock.advance(h.t, h.at(clock)) }

func (h *harness) expectTimeline(want ...string) {
	h.t.Helper()
	got := h.server.seen()
	if !slices.Equal(got, want) {
		h.t.Fatalf("timeline:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func (h *harness) lastRun(id string) Run {
	h.t.Helper()
	runs := h.store.runs(id)
	if len(runs) == 0 {
		h.t.Fatalf("schedule %s has no recorded run", id)
	}
	return runs[len(runs)-1]
}

func (h *harness) expectLast(id string, result Result, reason Reason) Run {
	h.t.Helper()
	r := h.lastRun(id)
	if r.Result != result || r.Reason != reason {
		h.t.Fatalf("last run of %s = %s/%s (%s), want %s/%s", id, r.Result, r.Reason, r.Detail, result, reason)
	}
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// sched is a daily schedule in Berlin created at the start of the test day.
func (h *harness) sched(id string, kind Kind, at string, p Payload) Schedule {
	created := h.at("00:00")
	return Schedule{ID: id, ServerID: "srv1", Kind: kind, Enabled: true, CreatedAt: created, UpdatedAt: created,
		Timing: Timing{Kind: Daily, TimeZone: "Europe/Berlin", At: at}, Payload: p}
}

func restartPayload() Payload {
	return Payload{WarnSeconds: DefaultWarnings(), Message: "Back in a minute!"}
}

func setup(t *testing.T, start string, build func(h *harness) []Schedule) *harness {
	t.Helper()
	h := newHarness(t, start)
	h.store.schedules = build(h)
	h.start()
	return h
}

func TestRunnerRestartWithWarnings(t *testing.T) {
	h := setup(t, "03:45", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())} })
	h.until("03:52")
	if a, ok := h.runner.Current(); !ok || a.Job.Schedule.ID != "r1" || !a.RestartAt.Equal(h.at("04:00")) {
		t.Fatalf("Current = %+v, %v", a, ok)
	}
	h.until("04:01")
	h.expectTimeline(
		"03:50:00 The server restarts in 10 minutes. Back in a minute!",
		"03:55:00 The server restarts in 5 minutes. Back in a minute!",
		"03:59:00 The server restarts in 1 minute. Back in a minute!",
		"03:59:30 The server restarts in 30 seconds. Back in a minute!",
		"03:59:50 The server restarts in 10 seconds. Back in a minute!",
		"04:00:00 The server is restarting now.",
		"04:00:00 op restart schedule:r1",
	)
	runs := h.store.runs("r1")
	if len(runs) != 2 || runs[0].Result != ResultRunning || !runs[0].Started.Equal(h.at("03:50")) {
		t.Fatalf("runs = %+v, want a running record first", runs)
	}
	r := h.expectLast("r1", ResultSucceeded, "")
	if !r.Due.Equal(h.at("04:00")) || !r.Finished.Equal(h.at("04:00")) || r.OperationID != "op1" {
		t.Fatalf("last run = %+v", r)
	}
	if _, ok := h.runner.Current(); ok {
		t.Fatal("Current reports work after the restart")
	}
	for _, cmd := range h.server.commands {
		if !strings.HasPrefix(cmd, "tellraw @a ") {
			t.Fatalf("sent %q; in-game text must only use tellraw", cmd)
		}
	}
}

func TestRunnerLateStartCatchesUp(t *testing.T) {
	h := setup(t, "03:57:20", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())} })
	h.until("04:01")
	h.expectTimeline(
		"03:57:20 The server restarts in about 2 minutes. Back in a minute!",
		"03:59:00 The server restarts in 1 minute. Back in a minute!",
		"03:59:30 The server restarts in 30 seconds. Back in a minute!",
		"03:59:50 The server restarts in 10 seconds. Back in a minute!",
		"04:00:00 The server is restarting now.",
		"04:00:00 op restart schedule:r1",
	)
}

func TestRunnerLateAfterDueGivesAMinute(t *testing.T) {
	h := setup(t, "04:05", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())} })
	h.until("04:10")
	h.expectTimeline(
		"04:05:00 The server restarts in 1 minute. Back in a minute!",
		"04:05:30 The server restarts in 30 seconds. Back in a minute!",
		"04:05:50 The server restarts in 10 seconds. Back in a minute!",
		"04:06:00 The server is restarting now.",
		"04:06:00 op restart schedule:r1",
	)
	if r := h.lastRun("r1"); !r.Due.Equal(h.at("04:00")) {
		t.Fatalf("the late run is recorded for %v, want 04:00", r.Due)
	}
}

func TestRunnerSkipsRestartWhenServerIsDown(t *testing.T) {
	for _, c := range []struct {
		state  ServerState
		reason Reason
	}{
		{ServerState{}, ReasonServerStopped},
		{ServerState{Sleeping: true}, ReasonServerSleeping},
	} {
		h := newHarness(t, "03:45")
		h.store.schedules = []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())}
		h.server.state = c.state
		h.start()
		h.until("04:10")
		h.expectTimeline()
		h.expectLast("r1", ResultSkipped, c.reason)
		h.stop()
	}
}

func TestRunnerStopsCountdownWhenServerStops(t *testing.T) {
	h := setup(t, "03:45", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())} })
	h.until("03:51")
	h.server.setState(ServerState{})
	h.until("04:10")
	h.expectTimeline("03:50:00 The server restarts in 10 minutes. Back in a minute!")
	h.expectLast("r1", ResultSkipped, ReasonServerStopped)
}

func TestRunnerIfEmpty(t *testing.T) {
	skip := restartPayload()
	skip.IfEmpty = IfEmptySkip
	now := restartPayload()
	now.IfEmpty = IfEmptyNow
	h := setup(t, "03:45", func(h *harness) []Schedule {
		return []Schedule{h.sched("r1", KindRestart, "04:00", skip), h.sched("r2", KindRestart, "05:00", now)}
	})
	h.server.setState(ServerState{Running: true, Players: 0, PlayersKnown: true})
	h.until("05:10")
	h.expectTimeline("04:50:00 op restart schedule:r2")
	h.expectLast("r1", ResultSkipped, ReasonNobodyOnline)
	h.expectLast("r2", ResultSucceeded, "")
}

func TestRunnerUnknownPlayerCountRestartsAsPlanned(t *testing.T) {
	p := restartPayload()
	p.IfEmpty = IfEmptySkip
	p.WarnSeconds = []int{10}
	h := setup(t, "03:59", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", p)} })
	h.server.setState(ServerState{Running: true})
	h.until("04:01")
	h.expectTimeline(
		"03:59:50 The server restarts in 10 seconds. Back in a minute!",
		"04:00:00 The server is restarting now.",
		"04:00:00 op restart schedule:r1",
	)
}

func TestRunnerRestartsRightAwayWhenEveryoneLeaves(t *testing.T) {
	p := restartPayload()
	p.IfEmpty = IfEmptyNow
	h := setup(t, "03:45", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", p)} })
	h.until("03:56")
	h.server.setState(ServerState{Running: true, Players: 0, PlayersKnown: true})
	h.until("04:10")
	h.expectTimeline(
		"03:50:00 The server restarts in 10 minutes. Back in a minute!",
		"03:55:00 The server restarts in 5 minutes. Back in a minute!",
		"03:59:00 op restart schedule:r1",
	)
	h.expectLast("r1", ResultSucceeded, "")
}

func TestRunnerTurningScheduleOffCallsRestartOff(t *testing.T) {
	h := setup(t, "03:45", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", restartPayload())} })
	h.until("03:52")
	h.store.update("r1", func(s *Schedule) { s.Enabled = false })
	h.runner.Reload()
	waitFor(t, "the restart to be called off", func() bool {
		runs := h.store.runs("r1")
		return len(runs) > 0 && runs[len(runs)-1].Result == ResultSkipped
	})
	h.until("04:30")
	h.expectTimeline(
		"03:50:00 The server restarts in 10 minutes. Back in a minute!",
		"03:52:00 The planned restart was called off.",
	)
	h.expectLast("r1", ResultSkipped, ReasonTurnedOff)
}

func TestRunnerRetriesWhileBusy(t *testing.T) {
	h := setup(t, "03:59", func(h *harness) []Schedule {
		return []Schedule{h.sched("b1", KindBackup, "04:00", Payload{Note: "Nightly"})}
	})
	h.server.mu.Lock()
	h.server.runErrs = []error{ErrBusy, fmt.Errorf("wrapped: %w", ErrBusy)}
	h.server.mu.Unlock()
	h.until("04:05")
	h.expectTimeline("04:00:00 op backup schedule:b1", "04:00:30 op backup schedule:b1", "04:01:00 op backup schedule:b1")
	r := h.expectLast("b1", ResultSucceeded, "")
	if r.OperationID != "op3" || h.server.ops[2].Note != "Nightly" || h.server.ops[2].ScheduleID != "b1" || !h.server.ops[2].Due.Equal(h.at("04:00")) {
		t.Fatalf("run = %+v, op = %+v", r, h.server.ops[2])
	}
}

// While another operation holds the server, such as a restore of a copy that
// takes hours, a scheduled backup is tried again until its grace ends, as a
// backup delayed by downtime may still start that late. A restart waits once
// its countdown is over, without telling players it restarts now, and gives
// up after BusyGiveUp.
func TestRunnerWaitsForABusyServer(t *testing.T) {
	restart := restartPayload()
	restart.WarnSeconds = []int{10}
	cases := []struct {
		name      string
		kind      Kind
		payload   Payload
		busyUntil string
		until     string
		// timeline is what players see and the operations started, from the
		// first that isn't an op the server refused as busy; attempts
		// counts every op.
		timeline []string
		attempts int
		result   Result
		reason   Reason
	}{
		{name: "a backup, busy for 40 minutes", kind: KindBackup, busyUntil: "04:40", until: "05:00",
			timeline: []string{"04:40:00 op backup schedule:j1"}, attempts: 81, result: ResultSucceeded},
		{name: "a backup, busy past its grace", kind: KindBackup, busyUntil: "10:30", until: "11:00",
			timeline: []string{"10:00:00 op backup schedule:j1"}, attempts: 721, result: ResultFailed, reason: ReasonBusy},
		{name: "a restart, busy for 5 minutes", kind: KindRestart, payload: restart, busyUntil: "04:05", until: "04:30",
			timeline: []string{"03:59:50 The server restarts in 10 seconds. Back in a minute!", "04:05:00 The server is restarting now.", "04:05:00 op restart schedule:j1"},
			attempts: 1, result: ResultSucceeded},
		{name: "a restart, busy for 40 minutes", kind: KindRestart, payload: restart, busyUntil: "04:40", until: "05:00",
			timeline: []string{"03:59:50 The server restarts in 10 seconds. Back in a minute!", "04:15:00 The planned restart was called off."},
			result: ResultFailed, reason: ReasonBusy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "03:59")
			h.store.schedules = []Schedule{h.sched("j1", c.kind, "04:00", c.payload)}
			h.server.busyUntil = h.at(c.busyUntil)
			h.start()
			h.until(c.until)
			var shown []string
			ops := 0
			for _, line := range h.server.seen() {
				if strings.Contains(line, " op ") {
					ops++
					if ops < c.attempts {
						continue
					}
				}
				shown = append(shown, line)
			}
			if !slices.Equal(shown, c.timeline) || ops != c.attempts {
				t.Fatalf("%d ops; timeline:\n  %s\nwant %d ops and:\n  %s", ops, strings.Join(shown, "\n  "), c.attempts, strings.Join(c.timeline, "\n  "))
			}
			h.expectLast("j1", c.result, c.reason)
		})
	}
}

func TestRunnerRestartFailureIsAnnounced(t *testing.T) {
	p := restartPayload()
	p.WarnSeconds = []int{10}
	h := setup(t, "03:59", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", p)} })
	h.server.mu.Lock()
	h.server.runErrs = []error{errors.New("docker: \x1b[31mcontainer gone\x1b[0m")}
	h.server.mu.Unlock()
	h.until("04:01")
	h.expectTimeline(
		"03:59:50 The server restarts in 10 seconds. Back in a minute!",
		"04:00:00 The server is restarting now.",
		"04:00:00 op restart schedule:r1",
		"04:00:00 The planned restart was called off.",
	)
	if r := h.expectLast("r1", ResultFailed, ReasonError); r.Detail != "docker: container gone" {
		t.Fatalf("detail = %q", r.Detail)
	}
}

func TestRunnerRunsSimultaneousJobsOneAfterAnother(t *testing.T) {
	h := setup(t, "03:45", func(h *harness) []Schedule {
		return []Schedule{
			h.sched("b1", KindBackup, "04:00", Payload{}),
			h.sched("r1", KindRestart, "04:00", restartPayload()),
			h.sched("c1", KindCommand, "04:00", Payload{Command: "save-all"}),
		}
	})
	h.server.replies["save-all"] = "Saving the game (this may take a moment!)\nSaved the game"
	h.until("04:10")
	h.expectTimeline(
		"03:50:00 The server restarts in 10 minutes. Back in a minute!",
		"03:55:00 The server restarts in 5 minutes. Back in a minute!",
		"03:59:00 The server restarts in 1 minute. Back in a minute!",
		"03:59:30 The server restarts in 30 seconds. Back in a minute!",
		"03:59:50 The server restarts in 10 seconds. Back in a minute!",
		"04:00:00 The server is restarting now.",
		"04:00:00 op restart schedule:r1",
		"04:00:00 save-all",
		"04:00:00 op backup schedule:b1",
	)
	if r := h.expectLast("c1", ResultSucceeded, ""); r.Detail != "Saving the game (this may take a moment!) Saved the game" {
		t.Fatalf("detail = %q", r.Detail)
	}
	h.expectLast("b1", ResultSucceeded, "")
}

func TestRunnerRecordsMissedRunsAfterDowntime(t *testing.T) {
	h := setup(t, "05:00", func(h *harness) []Schedule {
		s := h.sched("r1", KindRestart, "04:00", restartPayload())
		s.LastRun = &Run{Due: h.at("2026-05-31 04:00"), Result: ResultSucceeded}
		return []Schedule{s}
	})
	h.until("2026-06-02 03:51")
	h.expectTimeline("03:50:00 The server restarts in 10 minutes. Back in a minute!")
	runs := h.store.runs("r1")
	if runs[0].Result != ResultMissed || runs[0].Reason != ReasonAgentDown || !runs[0].Due.Equal(h.at("04:00")) {
		t.Fatalf("first record = %+v, want today's 04:00 missed", runs[0])
	}
}

func TestRunnerDoesNotRepeatInterruptedRun(t *testing.T) {
	h := setup(t, "04:02", func(h *harness) []Schedule {
		s := h.sched("r1", KindRestart, "04:00", restartPayload())
		s.LastRun = &Run{Due: h.at("04:00"), Started: h.at("03:50"), Result: ResultRunning}
		return []Schedule{s}
	})
	h.until("05:00")
	h.expectTimeline()
	r := h.expectLast("r1", ResultFailed, ReasonInterrupted)
	if !r.Due.Equal(h.at("04:00")) || !r.Started.Equal(h.at("03:50")) {
		t.Fatalf("interrupted record = %+v", r)
	}
}

func TestRunnerAnnouncements(t *testing.T) {
	h := setup(t, "11:59", func(h *harness) []Schedule {
		return []Schedule{
			h.sched("a1", KindAnnouncement, "12:00", Payload{Message: `Say "hi" to @a \o/`}),
			h.sched("a2", KindAnnouncement, "13:00", Payload{Message: "Nobody hears this"}),
		}
	})
	h.until("12:30")
	h.server.setState(ServerState{Running: true, Players: 0, PlayersKnown: true})
	h.until("13:30")
	h.expectTimeline(`12:00:00 Say "hi" to @a \o/`)
	if want := `tellraw @a {"text":"Say \"hi\" to @a \\o/","color":"yellow"}`; h.server.commands[0] != want {
		t.Fatalf("sent %s, want %s", h.server.commands[0], want)
	}
	h.expectLast("a1", ResultSucceeded, "")
	h.expectLast("a2", ResultSkipped, ReasonNobodyOnline)
}

func TestRunnerCommands(t *testing.T) {
	h := setup(t, "11:59", func(h *harness) []Schedule {
		return []Schedule{
			h.sched("c1", KindCommand, "12:00", Payload{Command: "weather clear"}),
			h.sched("c2", KindCommand, "13:00", Payload{Command: "op Steve"}),
		}
	})
	h.server.replies["weather clear"] = "Unknown or incomplete command, see below for error"
	h.until("13:30")
	h.expectTimeline("12:00:00 weather clear")
	h.expectLast("c1", ResultFailed, ReasonRejected)
	h.expectLast("c2", ResultFailed, ReasonInvalid)
}

func TestRunnerSkipsJobItCannotRecord(t *testing.T) {
	h := setup(t, "11:59", func(h *harness) []Schedule {
		return []Schedule{h.sched("a1", KindAnnouncement, "12:00", Payload{Message: "hello"})}
	})
	h.store.mu.Lock()
	h.store.failSaves = true
	h.store.mu.Unlock()
	h.until("13:00")
	h.expectTimeline()
	if n := len(h.store.runs("a1")); n != 1 {
		t.Fatalf("%d save attempts, want 1", n)
	}
}

func TestRunnerIgnoresOtherServers(t *testing.T) {
	h := setup(t, "11:59", func(h *harness) []Schedule {
		s := h.sched("a1", KindAnnouncement, "12:00", Payload{Message: "hello"})
		s.ServerID = "srv2"
		return []Schedule{s}
	})
	h.until("13:00")
	h.expectTimeline()
}

func TestNewRunnerNeedsCallbacks(t *testing.T) {
	if _, err := NewRunner(Config{ServerID: "srv1"}); err == nil {
		t.Fatal("NewRunner accepted a config without callbacks")
	}
}

func TestRunnerSkipsRestartWhilePeoplePlayAndRetries(t *testing.T) {
	p := Payload{WarnSeconds: []int{600}, Message: "Survival restarts in {minutes} minutes.", SkipIfPlaying: true}
	h := setup(t, "03:45", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", p)} })
	h.until("04:30")
	h.expectTimeline()
	r := h.expectLast("r1", ResultSkipped, ReasonPeoplePlaying)
	if !r.RetryAt.Equal(h.at("05:00")) || r.Players == nil || *r.Players != 3 {
		t.Fatalf("skipped run = %+v, want a retry at 05:00 with 3 players", r)
	}
	if next := NextRun(h.store.schedules[0], h.clock.Now()); !next.Equal(h.at("05:00")) {
		t.Fatalf("NextRun = %v, want the retry at 05:00", next)
	}
	h.server.setState(ServerState{Running: true, Players: 0, PlayersKnown: true})
	h.until("05:01")
	h.expectTimeline(
		"04:50:00 Survival restarts in 10 minutes.",
		"05:00:00 The server is restarting now.",
		"05:00:00 op restart schedule:r1",
	)
	r = h.expectLast("r1", ResultSucceeded, "")
	if !r.Due.Equal(h.at("05:00")) || r.Players == nil || *r.Players != 0 || !r.RetryAt.IsZero() {
		t.Fatalf("retried run = %+v", r)
	}
}

func TestRunnerKeepsRetryingHourlyWhilePeoplePlay(t *testing.T) {
	p := Payload{WarnSeconds: []int{60}, SkipIfPlaying: true}
	h := setup(t, "03:58", func(h *harness) []Schedule { return []Schedule{h.sched("r1", KindRestart, "04:00", p)} })
	h.until("06:30")
	h.expectTimeline()
	var retries []string
	for _, r := range h.store.runs("r1") {
		if r.Result == ResultSkipped {
			retries = append(retries, r.Due.In(h.loc).Format("15:04")+"→"+r.RetryAt.In(h.loc).Format("15:04"))
		}
	}
	if want := []string{"04:00→05:00", "05:00→06:00", "06:00→07:00"}; !slices.Equal(retries, want) {
		t.Fatalf("skips %v, want %v", retries, want)
	}
}

func TestRunnerBackupSkipsWhilePeoplePlay(t *testing.T) {
	h := setup(t, "03:59", func(h *harness) []Schedule {
		return []Schedule{h.sched("b1", KindBackup, "04:00", Payload{SkipIfPlaying: true})}
	})
	h.until("04:30")
	h.expectTimeline()
	if r := h.expectLast("b1", ResultSkipped, ReasonPeoplePlaying); !r.RetryAt.Equal(h.at("05:00")) {
		t.Fatalf("skipped backup = %+v", r)
	}
	h.server.setState(ServerState{})
	h.until("05:10")
	h.expectTimeline("05:00:00 op backup schedule:b1")
	if r := h.expectLast("b1", ResultSucceeded, ""); r.Players != nil {
		t.Fatalf("a stopped server's backup records players %d", *r.Players)
	}
}

func TestRunnerBackupOnlyIfPlayed(t *testing.T) {
	h := setup(t, "03:59", func(h *harness) []Schedule {
		return []Schedule{h.sched("b1", KindBackup, "04:00", Payload{OnlyIfPlayed: true, Note: "Automatic"})}
	})
	h.server.mu.Lock()
	h.server.runErrs = []error{fmt.Errorf("backup: %w", ErrNobodyPlayed)}
	h.server.mu.Unlock()
	h.until("04:10")
	h.expectTimeline("04:00:00 op backup schedule:b1")
	if op := h.server.ops[0]; !op.OnlyIfPlayed || op.Note != "Automatic" {
		t.Fatalf("op = %+v, want OnlyIfPlayed with the note", op)
	}
	if r := h.expectLast("b1", ResultSkipped, ReasonNobodyPlayed); !r.RetryAt.IsZero() || r.Players == nil || *r.Players != 3 {
		t.Fatalf("run = %+v", r)
	}
}

func TestRunnerSayGoesOutAsTellraw(t *testing.T) {
	h := setup(t, "19:59", func(h *harness) []Schedule {
		return []Schedule{
			h.sched("c1", KindCommand, "20:00", Payload{Command: "say Weekend build contest starts now! @a"}),
			h.sched("c2", KindCommand, "21:00", Payload{Command: "say Nobody hears this"}),
		}
	})
	h.until("20:30")
	h.server.setState(ServerState{Running: true, Players: 0, PlayersKnown: true})
	h.until("21:30")
	h.expectTimeline("20:00:00 [Server] Weekend build contest starts now! @a")
	if want := `tellraw @a {"text":"[Server] Weekend build contest starts now! @a","color":"white"}`; h.server.commands[0] != want {
		t.Fatalf("sent %s, want %s", h.server.commands[0], want)
	}
	if r := h.expectLast("c1", ResultSucceeded, ""); r.Players == nil || *r.Players != 3 {
		t.Fatalf("run = %+v, want 3 players", r)
	}
	h.expectLast("c2", ResultSkipped, ReasonNobodyOnline)
}
