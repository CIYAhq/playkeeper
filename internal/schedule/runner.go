package schedule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

var (
	// ErrBusy is Server.Run's error when another operation holds the server.
	// The runner tries again until Config.BusyGiveUp has passed.
	ErrBusy = errors.New("another operation is running on this server")
	// ErrNotRunning is Server.Run's error when a restart finds the server
	// stopped. The run is recorded as skipped.
	ErrNotRunning = errors.New("the server is not running")
)

// OperationKind is an agent operation a schedule starts.
type OperationKind string

const (
	OpRestart OperationKind = "restart"
	OpBackup  OperationKind = "backup"
)

// Operation asks the agent to run one operation for a schedule.
type Operation struct {
	Kind OperationKind
	// Actor is Actor(ScheduleID), for the operation and the audit trail.
	Actor      string
	ScheduleID string
	Due        time.Time
	// Note is a backup's note; empty for restarts.
	Note string
}

// ServerState is what the runner needs to know about its server.
type ServerState struct {
	// Running is true when the server is up and players can join.
	Running bool
	// Sleeping is true when Playkeeper stopped the server because nobody was
	// playing; Running is false then.
	Sleeping bool
	// Players is the number of players online, when PlayersKnown.
	Players      int
	PlayersKnown bool
}

// Server is what the runner needs from the agent for its server.
type Server interface {
	State(ctx context.Context) (ServerState, error)
	// Command sends one console command and returns the server's reply.
	Command(ctx context.Context, cmd string) (string, error)
	// Run runs op to the end and returns its operation id. A restart must not
	// warn players again; the runner already has. Run returns ErrBusy if
	// another operation holds the server, and ErrNotRunning if a restart finds
	// the server stopped.
	Run(ctx context.Context, op Operation) (string, error)
}

// Timer is the part of a *time.Timer the runner uses.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Config is one server's runner.
type Config struct {
	ServerID string
	// Load returns the server's schedules as stored. Schedules of other
	// servers are ignored.
	Load func(ctx context.Context) ([]Schedule, error)
	// Save stores a schedule's latest run: the last_run and last_result
	// columns. The runner saves a "running" record before it starts work, so
	// an occurrence runs at most once even if the agent stops halfway.
	Save   func(ctx context.Context, scheduleID string, run Run) error
	Server Server
	// Now and NewTimer are the clock. They default to the real one.
	Now      func() time.Time
	NewTimer func(d time.Duration) Timer
	// Logf defaults to discarding.
	Logf func(format string, args ...any)
	// BusyRetry and BusyGiveUp say how often, and for how long, a restart or
	// backup is tried again while another operation holds the server.
	BusyRetry  time.Duration
	BusyGiveUp time.Duration
}

// Activity is what a runner is doing now.
type Activity struct {
	Job Job
	// RestartAt is when a restart's warnings end and it happens.
	RestartAt time.Time
}

// Runner runs one server's schedules. Create it with NewRunner and call Run in
// its own goroutine; call Reload whenever the server's schedules change.
type Runner struct {
	cfg    Config
	reload chan struct{}

	mu      sync.Mutex
	runs    map[string]Run
	current *Activity
	logged  map[string]time.Time
}

const (
	maxSleep    = time.Minute
	retryDelay  = time.Minute
	saveTimeout = 10 * time.Second
	// minNotice is the least warning players get when a restart starts late.
	minNotice = time.Minute
)

func NewRunner(cfg Config) (*Runner, error) {
	if cfg.ServerID == "" || cfg.Load == nil || cfg.Save == nil || cfg.Server == nil {
		return nil, errors.New("schedule: a runner needs a server id, Load, Save and Server")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewTimer == nil {
		cfg.NewTimer = func(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.BusyRetry <= 0 {
		cfg.BusyRetry = 30 * time.Second
	}
	if cfg.BusyGiveUp <= 0 {
		cfg.BusyGiveUp = 15 * time.Minute
	}
	return &Runner{cfg: cfg, reload: make(chan struct{}, 1), runs: map[string]Run{}, logged: map[string]time.Time{}}, nil
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// Reload makes the runner read the schedules again. Turning a restart's
// schedule off or deleting it during the warnings calls the restart off.
func (r *Runner) Reload() {
	select {
	case r.reload <- struct{}{}:
	default:
	}
}

// Current returns what the runner is doing, if anything.
func (r *Runner) Current() (Activity, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return Activity{}, false
	}
	return *r.current, true
}

// Run runs the schedules until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	since := r.cfg.Now()
	first := true
	for {
		list, err := r.load(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.cfg.Logf("schedules for server %s could not be read: %v", r.cfg.ServerID, err)
			if r.sleep(ctx, retryDelay) == wokeDone {
				return ctx.Err()
			}
			continue
		}
		if first {
			first = false
			r.markInterrupted(ctx, list)
		}
		now := r.cfg.Now()
		d := Plan(list, now, since)
		r.logInvalid(d.Invalid)
		for _, j := range d.Missed {
			r.record(ctx, j, Run{Due: j.Due, Finished: now, Result: ResultMissed, Reason: j.Reason})
		}
		if len(d.Due) > 0 {
			r.run(ctx, d.Due[0])
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		wait := maxSleep
		if !d.Wake.IsZero() {
			wait = min(wait, d.Wake.Sub(now))
		}
		if r.sleep(ctx, wait) == wokeDone {
			return ctx.Err()
		}
	}
}

// load reads the server's schedules, with the runs this runner saved laid
// over them in case a save did not reach the database.
func (r *Runner) load(ctx context.Context) ([]Schedule, error) {
	list, err := r.cfg.Load(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Schedule
	ids := map[string]bool{}
	for _, s := range list {
		if s.ServerID != r.cfg.ServerID {
			continue
		}
		ids[s.ID] = true
		if run, ok := r.runs[s.ID]; ok && (s.LastRun == nil || !run.Due.Before(s.LastRun.Due)) {
			s.LastRun = &run
		}
		out = append(out, s)
	}
	for id := range r.runs {
		if !ids[id] {
			delete(r.runs, id)
		}
	}
	return out, nil
}

func (r *Runner) save(ctx context.Context, id string, run Run) error {
	r.mu.Lock()
	r.runs[id] = run
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := r.cfg.Save(ctx, id, run); err != nil {
		r.cfg.Logf("schedule %s: the run could not be recorded: %v", id, err)
		return err
	}
	return nil
}

func (r *Runner) record(ctx context.Context, j Job, run Run) {
	r.cfg.Logf("schedule %s (%s, due %s): %s", j.Schedule.ID, j.Schedule.Kind, j.Due.UTC().Format(time.RFC3339), run.Text())
	_ = r.save(ctx, j.Schedule.ID, run)
}

// markInterrupted closes runs left "running" by an agent that stopped
// halfway. They are not run again.
func (r *Runner) markInterrupted(ctx context.Context, list []Schedule) {
	for _, s := range list {
		if s.LastRun == nil || s.LastRun.Result != ResultRunning {
			continue
		}
		run := *s.LastRun
		run.Finished, run.Result, run.Reason = r.cfg.Now(), ResultFailed, ReasonInterrupted
		r.record(ctx, Job{Schedule: s, Due: run.Due}, run)
	}
}

func (r *Runner) logInvalid(list []Invalid) {
	for _, inv := range list {
		if seen, ok := r.logged[inv.Schedule.ID]; ok && seen.Equal(inv.Schedule.UpdatedAt) {
			continue
		}
		r.logged[inv.Schedule.ID] = inv.Schedule.UpdatedAt
		r.cfg.Logf("schedule %s does not run because it is not valid: %v", inv.Schedule.ID, inv.Err)
	}
}

func (r *Runner) run(ctx context.Context, j Job) {
	id := j.Schedule.ID
	started := r.cfg.Now()
	if err := r.save(ctx, id, Run{Due: j.Due, Started: started, Result: ResultRunning}); err != nil {
		r.mu.Lock()
		r.runs[id] = Run{Due: j.Due, Started: started, Finished: started, Result: ResultFailed, Reason: ReasonError}
		r.mu.Unlock()
		r.cfg.Logf("schedule %s was skipped because its run could not be recorded", id)
		return
	}
	r.setCurrent(&Activity{Job: j})
	out := r.execute(ctx, j)
	r.setCurrent(nil)
	out.Due, out.Started, out.Finished = j.Due, started, r.cfg.Now()
	r.record(ctx, j, out)
}

func (r *Runner) setCurrent(a *Activity) {
	r.mu.Lock()
	r.current = a
	r.mu.Unlock()
}

func (r *Runner) execute(ctx context.Context, j Job) Run {
	if err := j.Schedule.Payload.validate(j.Schedule.Kind); err != nil {
		return Run{Result: ResultFailed, Reason: ReasonInvalid, Detail: err.Error()}
	}
	switch j.Schedule.Kind {
	case KindRestart:
		return r.restart(ctx, j)
	case KindBackup:
		return r.operation(ctx, j, Operation{Kind: OpBackup, Note: j.Schedule.Payload.Note})
	case KindAnnouncement:
		return r.announce(ctx, j)
	case KindCommand:
		return r.command(ctx, j)
	}
	return Run{Result: ResultFailed, Reason: ReasonInvalid, Detail: fmt.Sprintf("unknown schedule kind %q", j.Schedule.Kind)}
}

// offline is the skipped run for a server that is not up.
func offline(st ServerState) (Run, bool) {
	switch {
	case st.Sleeping:
		return Run{Result: ResultSkipped, Reason: ReasonServerSleeping}, true
	case !st.Running:
		return Run{Result: ResultSkipped, Reason: ReasonServerStopped}, true
	}
	return Run{}, false
}

func stateFailed(err error) Run {
	return Run{Result: ResultFailed, Reason: ReasonError, Detail: cleanOutput("Could not check the server: " + err.Error())}
}

func (r *Runner) restart(ctx context.Context, j Job) Run {
	p := j.Schedule.Payload
	st, err := r.cfg.Server.State(ctx)
	if err != nil {
		return stateFailed(err)
	}
	if skip, ok := offline(st); ok {
		return skip
	}
	if st.PlayersKnown && st.Players == 0 {
		switch p.IfEmpty {
		case IfEmptySkip:
			return Run{Result: ResultSkipped, Reason: ReasonNobodyOnline}
		case IfEmptyNow:
			return r.operation(ctx, j, Operation{Kind: OpRestart})
		}
	}
	now := r.cfg.Now()
	at := later(j.Due, now.Add(min(p.lead(), minNotice)))
	r.setCurrent(&Activity{Job: j, RestartAt: at})
	warned, emptied := false, false
	for _, w := range warningSteps(p.WarnSeconds, at, now) {
		if res, stop := r.countdownWait(ctx, j, w.at, warned); stop {
			return res
		}
		if st, err := r.cfg.Server.State(ctx); err == nil {
			if skip, ok := offline(st); ok {
				return skip
			}
			if p.IfEmpty == IfEmptyNow && st.PlayersKnown && st.Players == 0 {
				emptied = true
				break
			}
		}
		cmd, err := restartWarning(w.remaining, p.Message)
		if err != nil {
			return Run{Result: ResultFailed, Reason: ReasonInvalid, Detail: err.Error()}
		}
		if _, err := r.cfg.Server.Command(ctx, cmd); err != nil {
			r.cfg.Logf("schedule %s: a restart warning could not be sent: %v", j.Schedule.ID, err)
		} else {
			warned = true
		}
	}
	if !emptied {
		if res, stop := r.countdownWait(ctx, j, at, warned); stop {
			return res
		}
		if st, err := r.cfg.Server.State(ctx); err == nil {
			if skip, ok := offline(st); ok {
				return skip
			}
		}
		if warned {
			_, _ = r.cfg.Server.Command(ctx, restartNow())
		}
	}
	res := r.operation(ctx, j, Operation{Kind: OpRestart})
	if warned && res.Result == ResultFailed && res.Reason != ReasonInterrupted {
		_, _ = r.cfg.Server.Command(ctx, restartCalledOff())
	}
	return res
}

type warningStep struct {
	at        time.Time
	remaining time.Duration
}

// warningSteps lists a restart's warnings for a restart at at, from now. When
// the countdown starts late, the warnings already due are replaced by one
// right away that says how long is left.
func warningSteps(warnSeconds []int, at, now time.Time) []warningStep {
	ws := slices.Sorted(slices.Values(warnSeconds))
	slices.Reverse(ws)
	var steps []warningStep
	late := false
	for _, w := range ws {
		d := time.Duration(w) * time.Second
		if t := at.Add(-d); t.Before(now) {
			late = true
		} else {
			steps = append(steps, warningStep{at: t, remaining: d})
		}
	}
	if late && (len(steps) == 0 || steps[0].at.Sub(now) > 5*time.Second) {
		steps = append([]warningStep{{at: now, remaining: at.Sub(now)}}, steps...)
	}
	return steps
}

// countdownWait waits until t during a restart's warnings. It stops the
// restart when ctx ends or the schedule is turned off or deleted.
func (r *Runner) countdownWait(ctx context.Context, j Job, t time.Time, warned bool) (Run, bool) {
	for {
		if r.turnedOff(ctx, j.Schedule.ID) {
			if warned {
				_, _ = r.cfg.Server.Command(ctx, restartCalledOff())
			}
			return Run{Result: ResultSkipped, Reason: ReasonTurnedOff}, true
		}
		d := t.Sub(r.cfg.Now())
		if d <= 0 {
			return Run{}, false
		}
		if r.sleep(ctx, d) == wokeDone {
			return Run{Result: ResultFailed, Reason: ReasonInterrupted}, true
		}
	}
}

// turnedOff reports whether the schedule was disabled or deleted. When the
// schedules cannot be read, the restart goes ahead as planned.
func (r *Runner) turnedOff(ctx context.Context, id string) bool {
	list, err := r.cfg.Load(ctx)
	if err != nil {
		return false
	}
	for _, s := range list {
		if s.ID == id && s.ServerID == r.cfg.ServerID {
			return !s.Enabled
		}
	}
	return true
}

func (r *Runner) operation(ctx context.Context, j Job, op Operation) Run {
	op.Actor, op.ScheduleID, op.Due = Actor(j.Schedule.ID), j.Schedule.ID, j.Due
	giveUp := r.cfg.Now().Add(r.cfg.BusyGiveUp)
	for {
		id, err := r.cfg.Server.Run(ctx, op)
		switch {
		case err == nil:
			return Run{Result: ResultSucceeded, OperationID: id}
		case errors.Is(err, ErrNotRunning):
			return Run{Result: ResultSkipped, Reason: ReasonServerStopped, OperationID: id}
		case ctx.Err() != nil:
			return Run{Result: ResultFailed, Reason: ReasonInterrupted, OperationID: id}
		case !errors.Is(err, ErrBusy):
			return Run{Result: ResultFailed, Reason: ReasonError, Detail: cleanOutput(err.Error()), OperationID: id}
		case !r.cfg.Now().Before(giveUp):
			return Run{Result: ResultFailed, Reason: ReasonBusy}
		}
		if !r.sleepUntil(ctx, r.cfg.Now().Add(r.cfg.BusyRetry)) {
			return Run{Result: ResultFailed, Reason: ReasonInterrupted}
		}
	}
}

func (r *Runner) announce(ctx context.Context, j Job) Run {
	st, err := r.cfg.Server.State(ctx)
	if err != nil {
		return stateFailed(err)
	}
	if skip, ok := offline(st); ok {
		return skip
	}
	if st.PlayersKnown && st.Players == 0 {
		return Run{Result: ResultSkipped, Reason: ReasonNobodyOnline}
	}
	cmd, err := tellraw(j.Schedule.Payload.Message, colorAnnouncement)
	if err != nil {
		return Run{Result: ResultFailed, Reason: ReasonInvalid, Detail: err.Error()}
	}
	if _, err := r.cfg.Server.Command(ctx, cmd); err != nil {
		return Run{Result: ResultFailed, Reason: ReasonError, Detail: cleanOutput(err.Error())}
	}
	return Run{Result: ResultSucceeded}
}

func (r *Runner) command(ctx context.Context, j Job) Run {
	st, err := r.cfg.Server.State(ctx)
	if err != nil {
		return stateFailed(err)
	}
	if skip, ok := offline(st); ok {
		return skip
	}
	out, err := r.cfg.Server.Command(ctx, j.Schedule.Payload.Command)
	if err != nil {
		return Run{Result: ResultFailed, Reason: ReasonError, Detail: cleanOutput(err.Error())}
	}
	if rejected(out) {
		return Run{Result: ResultFailed, Reason: ReasonRejected, Detail: cleanOutput(out)}
	}
	return Run{Result: ResultSucceeded, Detail: cleanOutput(out)}
}

type woke int

const (
	wokeTimer woke = iota
	wokeReload
	wokeDone
)

func (r *Runner) sleep(ctx context.Context, d time.Duration) woke {
	t := r.cfg.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return wokeDone
	case <-r.reload:
		return wokeReload
	case <-t.C():
		return wokeTimer
	}
}

// sleepUntil waits until t, whatever Reload says, and reports whether ctx is
// still live.
func (r *Runner) sleepUntil(ctx context.Context, t time.Time) bool {
	for {
		d := t.Sub(r.cfg.Now())
		if d <= 0 {
			return true
		}
		if r.sleep(ctx, d) == wokeDone {
			return false
		}
	}
}
