package sleep

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func minute(m float64) time.Time { return t0.Add(time.Duration(m * float64(time.Minute))) }

// empty is a sample of a running server that nobody is on.
func empty(m float64) Observation {
	return Observation{At: minute(m), Running: true, PlayersKnown: true}
}

// feed observes empty samples every 15 seconds from one minute to another,
// excluding the last, and fails if any says to sleep.
func feed(t *testing.T, tr *Tracker, from, to float64) Decision {
	t.Helper()
	var d Decision
	for m := from; m < to; m += 0.25 {
		if d = tr.Observe(empty(m)); d.Sleep {
			t.Fatalf("slept at minute %v, want not before %v", m, to)
		}
	}
	return d
}

func TestTrackerSleepsAfterTheIdleMinutes(t *testing.T) {
	tr := NewTracker(Settings{Enabled: true, IdleMinutes: 15})
	d := feed(t, tr, 0, 15)
	if !d.EmptySince.Equal(t0) || !d.SleepAt.Equal(minute(15)) || d.Hold != "" {
		t.Fatalf("while counting: %+v", d)
	}
	if d := tr.Observe(empty(15)); !d.Sleep {
		t.Fatalf("minute 15: %+v, want sleep", d)
	}
}

func TestTrackerDefaultIdle(t *testing.T) {
	tr := NewTracker(Settings{Enabled: true})
	feed(t, tr, 0, DefaultIdleMinutes)
	if d := tr.Observe(empty(DefaultIdleMinutes)); !d.Sleep {
		t.Fatalf("after the default idle time: %+v", d)
	}
}

func TestTrackerHoldsAndStartsAgain(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
		want Hold
	}{
		{"players online", Observation{Running: true, PlayersKnown: true, Players: 1}, HoldPlayers},
		{"player count unknown", Observation{Running: true}, HoldUnknown},
		{"operation or backup running", Observation{Running: true, PlayersKnown: true, Busy: true}, HoldBusy},
		{"server not running", Observation{PlayersKnown: true}, HoldNotRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := NewTracker(Settings{Enabled: true, IdleMinutes: 10})
			feed(t, tr, 0, 9)
			c.obs.At = minute(9)
			if d := tr.Observe(c.obs); d.Sleep || d.Hold != c.want || !d.EmptySince.IsZero() {
				t.Fatalf("got %+v, want hold %q", d, c.want)
			}
			d := feed(t, tr, 9.25, 19.25)
			if !d.EmptySince.Equal(minute(9.25)) || !d.SleepAt.Equal(minute(19.25)) {
				t.Fatalf("after the hold: %+v, want counting from minute 9.25", d)
			}
			if d := tr.Observe(empty(19.25)); !d.Sleep {
				t.Fatalf("minute 19.25: %+v, want sleep", d)
			}
		})
	}
}

func TestTrackerOffByDefault(t *testing.T) {
	var s Settings
	tr := NewTracker(s)
	for m := 0.0; m < 120; m += 0.25 {
		if d := tr.Observe(empty(m)); d.Sleep || d.Hold != HoldDisabled {
			t.Fatalf("minute %v: %+v", m, d)
		}
	}
}

func TestTrackerWaitsAfterAStart(t *testing.T) {
	tr := NewTracker(Settings{Enabled: true, IdleMinutes: 5})
	obs := func(m float64) Observation {
		o := empty(m)
		o.StartedAt = t0
		return o
	}
	for m := 0.0; m < 10; m += 0.25 {
		d := tr.Observe(obs(m))
		if d.Sleep || !d.SleepAt.Equal(minute(10)) {
			t.Fatalf("minute %v: %+v, want no sleep before minute 10", m, d)
		}
	}
	if d := tr.Observe(obs(10)); !d.Sleep {
		t.Fatalf("minute 10: %+v", d)
	}

	tr = NewTracker(Settings{Enabled: true, IdleMinutes: 5})
	old := empty(0)
	old.StartedAt = minute(-60)
	tr.Observe(old)
	feed(t, tr, 0.25, 5)
	if d := tr.Observe(empty(5)); !d.Sleep {
		t.Fatalf("a start an hour ago held sleep back: %+v", d)
	}
}

func TestTrackerStartsAgainAfterAGapOrABackwardsClock(t *testing.T) {
	tr := NewTracker(Settings{Enabled: true, IdleMinutes: 5})
	feed(t, tr, 0, 4)
	if d := tr.Observe(empty(3.75 + 2.25)); !d.EmptySince.Equal(minute(3.75 + 2.25)) {
		t.Fatalf("after a gap just over %v: %+v, want counting again", MaxSampleGap, d)
	}

	tr = NewTracker(Settings{Enabled: true, IdleMinutes: 5})
	feed(t, tr, 0, 4)
	if d := tr.Observe(empty(3.75 + 1.75)); !d.EmptySince.Equal(t0) {
		t.Fatalf("a gap within %v broke the count: %+v", MaxSampleGap, d)
	}

	tr = NewTracker(Settings{Enabled: true, IdleMinutes: 5})
	feed(t, tr, 10, 14)
	if d := tr.Observe(empty(2)); !d.EmptySince.Equal(minute(2)) || d.Sleep {
		t.Fatalf("after the clock went back: %+v", d)
	}
}

func TestTrackerNewSettingsStartAgain(t *testing.T) {
	tr := NewTracker(Settings{Enabled: true, IdleMinutes: 30})
	feed(t, tr, 0, 20)
	tr.SetSettings(Settings{Enabled: true, IdleMinutes: 10})
	d := feed(t, tr, 20, 30)
	if !d.EmptySince.Equal(minute(20)) {
		t.Fatalf("got %+v, want counting from the change", d)
	}
	if d := tr.Observe(empty(30)); !d.Sleep {
		t.Fatalf("minute 30: %+v", d)
	}
}

func TestSettingsValidate(t *testing.T) {
	for _, n := range []int{0, MinIdleMinutes, 60, MaxIdleMinutes} {
		if err := (Settings{Enabled: true, IdleMinutes: n}).Validate(); err != nil {
			t.Errorf("%d minutes: %v", n, err)
		}
	}
	for _, n := range []int{-1, 1, MinIdleMinutes - 1, MaxIdleMinutes + 1} {
		err := (Settings{Enabled: true, IdleMinutes: n}).Validate()
		var e *Error
		if !errors.As(err, &e) || e.Code != "idle_minutes_invalid" || e.Params["value"] != n || e.Params["min"] != MinIdleMinutes {
			t.Errorf("%d minutes: %#v", n, err)
			continue
		}
		if e.Error() != "Choose between 5 minutes and 24 hours of nobody playing before the server sleeps." {
			t.Errorf("message: %q", e.Error())
		}
	}
	if got := (Settings{}).Idle(); got != 15*time.Minute {
		t.Errorf("default idle: %v", got)
	}
	if got := (Settings{IdleMinutes: 45}).Idle(); got != 45*time.Minute {
		t.Errorf("idle: %v", got)
	}
}
