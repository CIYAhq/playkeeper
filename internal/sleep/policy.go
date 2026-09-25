// Package sleep puts an empty server to sleep and wakes it when a player
// tries to join. It is off by default and works per server.
//
// A Tracker decides when to sleep: after a number of minutes in which every
// sample of the player count said nobody was online, never while an
// operation, backup or pre-generation job runs, and never soon after a start.
//
// While the server sleeps, a Manager answers on its game port in its place,
// speaking just enough of the Minecraft Java Edition protocol: the server
// list shows the server's name, "Asleep · join to wake it" and its icon,
// and a player who tries to join is told the server is waking up while the
// agent starts it. The Manager hands the port back and forth with the
// container: Wake closes the stand-in before the server starts, and Sleep
// takes the port back as soon as the stopped container lets go of it.
//
// The stand-in cannot check who a player is (that needs the encrypted
// Mojang login), so a name is only a claim. Wakes are rate-limited, and
// Config.Admit can keep names from waking the server, for example ones not
// on the whitelist. Every valid name gets the same reply, so the reply never
// tells whether a name is admitted; only admitted names wake the server,
// silently. Client addresses are never logged.
package sleep

import (
	"fmt"
	"time"
)

// Settings is a server's "Sleep when nobody's playing" setting. The zero
// value is off.
type Settings struct {
	Enabled bool `json:"enabled"`
	// IdleMinutes is how long the server must be empty before it sleeps.
	// Zero means DefaultIdleMinutes.
	IdleMinutes int `json:"idleMinutes,omitempty"`
}

const (
	DefaultIdleMinutes = 15
	MinIdleMinutes     = 5
	MaxIdleMinutes     = 24 * 60
	// DefaultStartGrace is how long after a start the server never sleeps,
	// so a player who woke it has time to join.
	DefaultStartGrace = 10 * time.Minute
	// MaxSampleGap is the longest gap between two samples that still counts
	// as watching the server. After a longer gap (a stalled sampler, a
	// suspended machine) nobody knows who was online, so counting starts
	// again.
	MaxSampleGap = 2 * time.Minute
)

// Idle is how long the server must be empty before it sleeps.
func (s Settings) Idle() time.Duration {
	if s.IdleMinutes == 0 {
		return DefaultIdleMinutes * time.Minute
	}
	return time.Duration(s.IdleMinutes) * time.Minute
}

// Validate checks the settings before they are saved.
func (s Settings) Validate() error {
	if s.IdleMinutes != 0 && (s.IdleMinutes < MinIdleMinutes || s.IdleMinutes > MaxIdleMinutes) {
		return &Error{Code: "idle_minutes_invalid",
			Msg:    fmt.Sprintf("Choose between %d minutes and 24 hours of nobody playing before the server sleeps.", MinIdleMinutes),
			Params: map[string]any{"min": MinIdleMinutes, "max": MaxIdleMinutes, "value": s.IdleMinutes}}
	}
	return nil
}

// Error is a problem the UI can show: Code and Params are stable for its
// translations, Msg and Hint are the English text.
type Error struct {
	Code   string
	Msg    string
	Hint   string
	Params map[string]any
	Err    error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Observation is one sample of the server, as the agent's sampler sees it.
type Observation struct {
	At time.Time
	// Running is true when the server is up and players can join.
	Running bool
	// Players is the number online, when PlayersKnown.
	Players      int
	PlayersKnown bool
	// Busy is true while an operation, a backup or a pre-generation job runs.
	Busy bool
	// StartedAt is when the server last started; zero if unknown.
	StartedAt time.Time
}

// Hold says why a server is not counting down to sleep.
type Hold string

const (
	HoldDisabled   Hold = "disabled"
	HoldNotRunning Hold = "not_running"
	HoldBusy       Hold = "busy"
	HoldUnknown    Hold = "players_unknown"
	HoldPlayers    Hold = "players_online"
)

// Decision is what a Tracker concluded from an observation.
type Decision struct {
	// Sleep is true when the server should go to sleep now. The agent checks
	// the player list once more right before it stops the server.
	Sleep bool
	// Hold is set while the server is not counting down.
	Hold Hold
	// EmptySince and SleepAt are set while counting down: the server has
	// been empty since EmptySince and sleeps at SleepAt if nobody joins.
	EmptySince time.Time
	SleepAt    time.Time
}

// Tracker decides when an empty server should sleep. Feed it every sample
// the agent takes. It is not safe for concurrent use.
type Tracker struct {
	settings   Settings
	startGrace time.Duration
	emptySince time.Time
	lastAt     time.Time
}

func NewTracker(s Settings) *Tracker {
	return &Tracker{settings: s, startGrace: DefaultStartGrace}
}

// SetSettings changes the settings and starts counting again.
func (t *Tracker) SetSettings(s Settings) {
	t.settings = s
	t.emptySince = time.Time{}
}

// Observe records a sample and says whether the server should sleep. Only
// an unbroken run of samples with a known player count of zero counts
// towards sleeping; anything else starts the count again, and so does a gap
// longer than MaxSampleGap or a clock that went backwards.
func (t *Tracker) Observe(o Observation) Decision {
	if !t.lastAt.IsZero() && (o.At.Before(t.lastAt) || o.At.Sub(t.lastAt) > MaxSampleGap) {
		t.emptySince = time.Time{}
	}
	t.lastAt = o.At
	hold := func(h Hold) Decision {
		t.emptySince = time.Time{}
		return Decision{Hold: h}
	}
	switch {
	case !t.settings.Enabled:
		return hold(HoldDisabled)
	case !o.Running:
		return hold(HoldNotRunning)
	case o.Busy:
		return hold(HoldBusy)
	case !o.PlayersKnown:
		return hold(HoldUnknown)
	case o.Players > 0:
		return hold(HoldPlayers)
	}
	if t.emptySince.IsZero() {
		t.emptySince = o.At
	}
	at := t.emptySince.Add(t.settings.Idle())
	if grace := o.StartedAt.Add(t.startGrace); !o.StartedAt.IsZero() && grace.After(at) {
		at = grace
	}
	return Decision{Sleep: !o.At.Before(at), EmptySince: t.emptySince, SleepAt: at}
}
