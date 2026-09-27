package pregen

import (
	"testing"
	"time"
)

func TestPolicy(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	on := Policy{Enabled: true}
	cases := []struct {
		name string
		p    Policy
		o    Observation
		want Action
	}{
		{"disabled", Policy{}, Observation{Online: 3, State: StateRunning, Now: now}, ActionNone},
		{"player joins", on, Observation{Online: 1, State: StateRunning, Now: now}, ActionPause},
		{"running on an empty server", on, Observation{State: StateRunning, EmptySince: now.Add(-time.Hour), Now: now}, ActionNone},
		{"players still online", on, Observation{Online: 2, State: StatePaused, PausedByPolicy: true, Now: now}, ActionNone},
		{"just emptied", on, Observation{State: StatePaused, PausedByPolicy: true, EmptySince: now.Add(-time.Minute), Now: now}, ActionNone},
		{"empty long enough", on, Observation{State: StatePaused, PausedByPolicy: true, EmptySince: now.Add(-DefaultResumeAfter), Now: now}, ActionContinue},
		{"custom wait", Policy{Enabled: true, ResumeAfter: 10 * time.Minute}, Observation{State: StatePaused, PausedByPolicy: true, EmptySince: now.Add(-5 * time.Minute), Now: now}, ActionNone},
		{"custom wait over", Policy{Enabled: true, ResumeAfter: 10 * time.Minute}, Observation{State: StatePaused, PausedByPolicy: true, EmptySince: now.Add(-10 * time.Minute), Now: now}, ActionContinue},
		{"paused by a user", on, Observation{State: StatePaused, EmptySince: now.Add(-time.Hour), Now: now}, ActionNone},
		{"empty since unknown", on, Observation{State: StatePaused, PausedByPolicy: true, Now: now}, ActionNone},
		{"finished", on, Observation{Online: 1, State: StateFinished, Now: now}, ActionNone},
		{"cancelled", on, Observation{State: StateCancelled, PausedByPolicy: true, EmptySince: now.Add(-time.Hour), Now: now}, ActionNone},
		{"idle", on, Observation{Online: 1, State: StateIdle, Now: now}, ActionNone},
	}
	for _, c := range cases {
		if got := c.p.Decide(c.o); got != c.want {
			t.Errorf("%s: Decide = %q, want %q", c.name, got, c.want)
		}
	}
}
