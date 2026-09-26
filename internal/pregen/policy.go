package pregen

import "time"

// Policy pauses pre-generation while players are online, so they get the
// whole server, and continues it once the server has been empty for a
// while. It only ever continues tasks it paused itself: a task a user
// paused stays paused.
type Policy struct {
	// Enabled turns the policy on.
	Enabled bool
	// ResumeAfter is how long the server must be empty before a task the
	// policy paused continues; 0 means DefaultResumeAfter.
	ResumeAfter time.Duration
}

// DefaultResumeAfter waits out players who briefly disconnect and rejoin.
const DefaultResumeAfter = 2 * time.Minute

// Observation is what the caller knows when it asks the policy.
type Observation struct {
	// Online is the number of players on the server.
	Online int
	// State is the world's pre-generation state (Controller.Status).
	State State
	// PausedByPolicy is set by the caller when it pauses a task because the
	// policy said so, and cleared when the task continues or a user pauses,
	// continues or cancels it. The caller keeps it across restarts.
	PausedByPolicy bool
	// EmptySince is when the last player left, or zero while players are
	// online.
	EmptySince time.Time
	Now        time.Time
}

// Action is what the policy wants done.
type Action string

const (
	ActionNone     Action = ""
	ActionPause    Action = "pause"
	ActionContinue Action = "continue"
)

// Decide says whether to pause or continue the task now.
func (p Policy) Decide(o Observation) Action {
	if !p.Enabled {
		return ActionNone
	}
	switch o.State {
	case StateRunning:
		if o.Online > 0 {
			return ActionPause
		}
	case StatePaused:
		wait := p.ResumeAfter
		if wait <= 0 {
			wait = DefaultResumeAfter
		}
		if o.PausedByPolicy && o.Online == 0 && !o.EmptySince.IsZero() && o.Now.Sub(o.EmptySince) >= wait {
			return ActionContinue
		}
	}
	return ActionNone
}
