package sleep

import (
	"testing"
	"time"
)

func expectAnswer(t *testing.T, got answer, gotWait time.Duration, want answer, wantWait time.Duration) {
	t.Helper()
	if got != want || gotWait != wantWait {
		t.Fatalf("got answer %d with wait %v, want %d with wait %v", got, gotWait, want, wantWait)
	}
}

func TestWakeLimitOneWakeAtATime(t *testing.T) {
	var l wakeLimit
	a, w := l.try(t0)
	expectAnswer(t, a, w, answerWake, 0)
	a, w = l.try(t0.Add(time.Minute))
	expectAnswer(t, a, w, answerWaking, 0)
	a, w = l.try(t0.Add(wakeWindow - time.Second))
	expectAnswer(t, a, w, answerWaking, 0)
	a, w = l.try(t0.Add(wakeWindow))
	expectAnswer(t, a, w, answerWake, 0)
}

func TestWakeLimitPerHour(t *testing.T) {
	var l wakeLimit
	for i := range maxWakesPerHour {
		a, w := l.try(t0.Add(time.Duration(i) * 3 * time.Minute))
		expectAnswer(t, a, w, answerWake, 0)
	}
	now := t0.Add(20 * time.Minute)
	a, w := l.check(now)
	expectAnswer(t, a, w, answerTooMany, 40*time.Minute)
	a, w = l.try(t0.Add(time.Hour))
	expectAnswer(t, a, w, answerWake, 0)
	a, w = l.try(t0.Add(time.Hour + wakeWindow))
	expectAnswer(t, a, w, answerTooMany, time.Minute)
}

func TestWakeLimitPausesAfterFailures(t *testing.T) {
	var l wakeLimit
	now := t0
	for i, pause := range []time.Duration{5, 10, 20, 40, 60, 60} {
		pause *= time.Minute
		l.failed(now)
		a, w := l.check(now.Add(time.Minute))
		expectAnswer(t, a, w, answerFailed, pause-time.Minute)
		if a, _ := l.check(now.Add(pause)); a != answerWake {
			t.Fatalf("failure %d: answer %d after the pause of %v", i+1, a, pause)
		}
		now = now.Add(pause)
	}
	l.succeeded()
	l.failed(now)
	a, w := l.check(now)
	expectAnswer(t, a, w, answerFailed, firstBackoff)
}

func TestWakeLimitFailureEndsTheWakingWindow(t *testing.T) {
	var l wakeLimit
	l.try(t0)
	l.failed(t0.Add(30 * time.Second))
	a, w := l.check(t0.Add(time.Minute))
	expectAnswer(t, a, w, answerFailed, 4*time.Minute+30*time.Second)
}

func TestWakeLimitNewSleepMayWakeAtOnce(t *testing.T) {
	var l wakeLimit
	l.try(t0)
	l.slept()
	a, w := l.try(t0.Add(10 * time.Second))
	expectAnswer(t, a, w, answerWake, 0)
}
