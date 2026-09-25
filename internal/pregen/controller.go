package pregen

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Commander runs one command on the server console and returns its output.
type Commander interface {
	Command(ctx context.Context, cmd string) (string, error)
}

// Controller drives Chunky on one server through its console. Keep one
// Controller per server: Chunky has a single selection and one pending
// confirmation per console sender, so the Controller runs one operation at
// a time.
type Controller struct {
	Console  Commander
	Platform Platform
	// DataDir is the server's data directory, where Chunky keeps its config
	// and saved tasks.
	DataDir string
	// Owner owns the config files the Controller writes; nil keeps the
	// writing process's user.
	Owner *Owner

	mu sync.Mutex
}

// StartOptions are the choices besides the plan when starting.
type StartOptions struct {
	// ContinueOnRestart makes Chunky resume the task, and any other
	// unfinished task, when the server starts again.
	ContinueOnRestart bool
	// Replace discards a saved, unfinished task for the same world. Without
	// it Start fails with ErrSavedTask so the user can choose.
	Replace bool
}

// Started is the task Chunky started.
type Started struct {
	World   string `json:"world"`
	Shape   Shape  `json:"shape"`
	CenterX int    `json:"centerX"`
	CenterZ int    `json:"centerZ"`
	Radius  int    `json:"radius"`
}

// Start writes Chunky's config, then starts pre-generating the plan's area.
func (c *Controller) Start(ctx context.Context, pl Plan, opt StartOptions) (Started, error) {
	if err := pl.Check(c.Platform); err != nil {
		return Started{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The first command only checks that Chunky is there: until the config
	// is rewritten, it may answer in another language.
	if _, err := c.run(ctx, "chunky progress"); err != nil {
		return Started{}, err
	}
	if err := c.configure(ctx, Config{ContinueOnRestart: opt.ContinueOnRestart}); err != nil {
		return Started{}, err
	}
	evs, err := c.run(ctx, "chunky pattern region")
	if err != nil {
		return Started{}, err
	}
	if _, ok := find(evs, EventPatternSet); !ok {
		return Started{}, unexpected("chunky pattern region", evs)
	}
	s := Started{World: pl.World, Shape: pl.Shape, CenterX: pl.CenterX, CenterZ: pl.CenterZ, Radius: pl.Radius}
	if pl.CenterOnSpawn {
		if s.CenterX, s.CenterZ, err = c.spawn(ctx, pl.World); err != nil {
			return Started{}, err
		}
		if abs(s.CenterX)+s.Radius > WorldLimit || abs(s.CenterZ)+s.Radius > WorldLimit {
			return Started{}, &Error{Code: CodeOutsideWorld, Params: map[string]any{"centerX": s.CenterX, "centerZ": s.CenterZ, "radius": s.Radius, "limit": WorldLimit}, Msg: "This area around the world spawn reaches past the edge of the Minecraft world.", Hint: "Use a smaller radius."}
		}
	}
	cmd := fmt.Sprintf("chunky start %s %s %d %d %d", s.World, s.Shape, s.CenterX, s.CenterZ, s.Radius)
	if evs, err = c.run(ctx, cmd); err != nil {
		return Started{}, err
	}
	if _, ok := find(evs, EventConfirmStart); ok {
		if !opt.Replace {
			return Started{}, &Error{Code: CodeSavedTask, Params: map[string]any{"world": s.World}, Msg: fmt.Sprintf("%s has an unfinished pre-generation task.", shortQuote(s.World)), Hint: "Continue that task, or start again and choose to replace it."}
		}
		cmd = "chunky confirm"
		if evs, err = c.run(ctx, cmd); err != nil {
			return Started{}, err
		}
	}
	if e, ok := find(evs, EventStarted, EventAlreadyRunning, EventRadiusLimit, EventUsage); ok {
		switch e.Kind {
		case EventStarted:
			if e.World == s.World {
				return s, nil
			}
		case EventAlreadyRunning:
			return Started{}, alreadyRunning(s.World)
		case EventRadiusLimit:
			limit := strconv.FormatFloat(e.Limit, 'f', -1, 64)
			return Started{}, &Error{Code: CodeRadiusLimit, Params: map[string]any{"world": s.World, "radius": s.Radius, "limit": e.Limit}, Msg: fmt.Sprintf("Chunky on this server is limited to a radius of %s blocks.", limit), Hint: fmt.Sprintf("Choose a radius of at most %s blocks, or remove radius-limit from Chunky's .chunky.properties file and restart the server.", limit)}
		case EventUsage:
			return Started{}, unknownWorld(s.World)
		}
	}
	return Started{}, unexpected(cmd, evs)
}

// spawn asks Chunky for world's spawn point, the block to center on.
func (c *Controller) spawn(ctx context.Context, world string) (int, int, error) {
	evs, err := c.run(ctx, "chunky world "+world)
	if err != nil {
		return 0, 0, err
	}
	e, ok := find(evs, EventWorldSet, EventUsage)
	switch {
	case ok && e.Kind == EventUsage:
		return 0, 0, unknownWorld(world)
	case !ok || e.World != world:
		return 0, 0, unexpected("chunky world", evs)
	}
	if evs, err = c.run(ctx, "chunky spawn"); err != nil {
		return 0, 0, err
	}
	e, ok = find(evs, EventCenterSet)
	if !ok || math.Abs(e.CenterX) > WorldLimit || math.Abs(e.CenterZ) > WorldLimit {
		return 0, 0, unexpected("chunky spawn", evs)
	}
	return int(math.Floor(e.CenterX)), int(math.Floor(e.CenterZ)), nil
}

// Pause stops the task in world and saves it so Continue can resume it.
// It reports ErrNotRunning when no task runs there.
func (c *Controller) Pause(ctx context.Context, world string) error {
	if err := c.Platform.CheckWorld(world); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	evs, err := c.run(ctx, "chunky pause "+world)
	if err != nil {
		return err
	}
	if e, ok := find(evs, EventPaused, EventNoTasks, EventUsage); ok {
		if e.Kind == EventPaused && e.World == world {
			return nil
		}
		if e.Kind != EventPaused {
			return &Error{Code: CodeNotRunning, Params: map[string]any{"world": world}, Msg: fmt.Sprintf("Chunky is not pre-generating %s right now.", shortQuote(world))}
		}
	}
	return unexpected("chunky pause", evs)
}

// Continue resumes the saved task in world. A task that is already running
// is left alone; ErrNothingToContinue means there is no unfinished task.
func (c *Controller) Continue(ctx context.Context, world string) error {
	if err := c.Platform.CheckWorld(world); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	evs, err := c.run(ctx, "chunky continue "+world)
	if err != nil {
		return err
	}
	if e, ok := find(evs, EventContinued, EventAlreadyRunning, EventNoTasks, EventUsage); ok {
		switch e.Kind {
		case EventContinued, EventAlreadyRunning:
			if e.World == world {
				return nil
			}
		case EventNoTasks:
			return &Error{Code: CodeNothingToContinue, Params: map[string]any{"world": world}, Msg: fmt.Sprintf("%s has no paused pre-generation task to continue.", shortQuote(world)), Hint: "Start a new one instead."}
		case EventUsage:
			return unknownWorld(world)
		}
	}
	return unexpected("chunky continue", evs)
}

// Cancel stops the task in world for good, answering Chunky's confirmation
// question. Cancelling when there is no task succeeds.
func (c *Controller) Cancel(ctx context.Context, world string) error {
	if err := c.Platform.CheckWorld(world); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	evs, err := c.run(ctx, "chunky cancel "+world)
	if err != nil {
		return err
	}
	e, ok := find(evs, EventConfirmCancel, EventNoTasks, EventUsage)
	switch {
	case !ok:
		return unexpected("chunky cancel", evs)
	case e.Kind == EventNoTasks:
		return nil
	case e.Kind == EventUsage:
		return unknownWorld(world)
	}
	if evs, err = c.run(ctx, "chunky confirm"); err != nil {
		return err
	}
	if e, ok := find(evs, EventCancelled); ok && e.World == world {
		return nil
	}
	return unexpected("chunky confirm", evs)
}

// Configure writes Chunky's config and makes Chunky reread it, which it
// does even while tasks run.
func (c *Controller) Configure(ctx context.Context, cfg Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.configure(ctx, cfg)
}

func (c *Controller) configure(ctx context.Context, cfg Config) error {
	if err := WriteConfig(c.DataDir, c.Platform, cfg, c.Owner); err != nil {
		return err
	}
	evs, err := c.run(ctx, "chunky reload")
	if err != nil {
		return err
	}
	if _, ok := find(evs, EventReloaded); !ok {
		return unexpected("chunky reload", evs)
	}
	return nil
}

// Progress is Chunky's latest report for every running task.
func (c *Controller) Progress(ctx context.Context) ([]Progress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.progress(ctx)
}

func (c *Controller) progress(ctx context.Context) ([]Progress, error) {
	evs, err := c.run(ctx, "chunky progress")
	if err != nil {
		return nil, err
	}
	out := []Progress{}
	none := false
	for _, e := range evs {
		switch {
		case e.Progress != nil:
			out = append(out, *e.Progress)
		case e.Kind == EventNoTasks && e.Text == "progress":
			none = true
		}
	}
	if len(out) == 0 && !none {
		return nil, unexpected("chunky progress", evs)
	}
	return out, nil
}

// State is where pre-generation of a world stands.
type State string

const (
	// StateIdle means the world has no task.
	StateIdle    State = "idle"
	StateRunning State = "running"
	// StatePaused means an unfinished task is saved: paused by a user or
	// the policy, or stopped with the server. Continue resumes it.
	StatePaused    State = "paused"
	StateFinished  State = "finished"
	StateCancelled State = "cancelled"
)

// Status is where pre-generation of one world stands.
type Status struct {
	World string `json:"world"`
	State State  `json:"state"`
	// Progress is Chunky's latest report while the task runs.
	Progress *Progress `json:"progress,omitempty"`
	// Task is the task Chunky saved last, if any: the paused, cancelled or
	// finished task, or an older save of the running one.
	Task *Task `json:"task,omitempty"`
}

// Status combines Chunky's progress report with the task it saved for
// world.
func (c *Controller) Status(ctx context.Context, world string) (Status, error) {
	if err := c.Platform.CheckWorld(world); err != nil {
		return Status{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	running, err := c.progress(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{World: world, State: StateIdle}
	task, found, err := ReadTask(c.DataDir, c.Platform, world)
	if err != nil {
		return Status{}, err
	}
	if found {
		st.Task = &task
		switch {
		case !task.Cancelled:
			st.State = StatePaused
		case task.Finished():
			st.State = StateFinished
		default:
			st.State = StateCancelled
		}
	}
	for i := range running {
		if running[i].World == world {
			st.Progress = &running[i]
			st.State = StateRunning
			if running[i].Finished {
				st.State = StateFinished
			}
		}
	}
	return st, nil
}

// run sends one command and reads its reply. A server without Chunky
// answers with the game's unknown-command error.
func (c *Controller) run(ctx context.Context, cmd string) ([]Event, error) {
	out, err := c.Console.Command(ctx, cmd)
	if err != nil {
		return nil, &Error{Code: CodeConsole, Msg: "Playkeeper could not send a command to the server console.", Hint: "Check that the server is running, then try again.", Err: err}
	}
	evs := ParseReply(out)
	if _, ok := find(evs, EventUnknownCommand); ok {
		return evs, &Error{Code: CodeNotInstalled, Msg: "Chunky is not installed on this server, or the server has not loaded it yet.", Hint: "Install Chunky from the add-ons page and restart the server."}
	}
	return evs, nil
}

// find returns the first event of one of kinds.
func find(evs []Event, kinds ...EventKind) (Event, bool) {
	for _, e := range evs {
		for _, k := range kinds {
			if e.Kind == k {
				return e, true
			}
		}
	}
	return Event{}, false
}

func alreadyRunning(world string) error {
	return &Error{Code: CodeAlreadyRunning, Params: map[string]any{"world": world}, Msg: fmt.Sprintf("Chunky is already pre-generating %s.", shortQuote(world)), Hint: "Wait for it to finish, or pause or cancel it first."}
}

func unknownWorld(world string) error {
	return &Error{Code: CodeUnknownWorld, Params: map[string]any{"world": world}, Msg: fmt.Sprintf("Chunky doesn't know a world named %s.", shortQuote(world)), Hint: "The Nether or the End may be turned off, or the world was never created. Pick another world."}
}

// unexpected describes a reply Playkeeper can't interpret, with a short,
// printable excerpt of it.
func unexpected(cmd string, evs []Event) error {
	var texts []string
	for _, e := range evs {
		texts = append(texts, e.Text)
	}
	reply := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.Join(texts, " / "))
	if r := []rune(reply); len(r) > 200 {
		reply = string(r[:200]) + "…"
	}
	return &Error{
		Code:   CodeUnexpectedReply,
		Params: map[string]any{"command": cmd, "reply": reply},
		Msg:    fmt.Sprintf("Chunky gave an answer Playkeeper doesn't understand to %q: %q.", cmd, reply),
		Hint:   "Check the server console. A newer Chunky may word its messages differently.",
	}
}
