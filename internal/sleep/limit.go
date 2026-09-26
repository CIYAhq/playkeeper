package sleep

import "time"

const (
	// wakeWindow is how long after a wake a join attempt is told the server
	// is waking up, instead of waking it again.
	wakeWindow      = 2 * time.Minute
	maxWakesPerHour = 6
	firstBackoff    = 5 * time.Minute
	maxBackoff      = time.Hour
)

// answer is what a join attempt gets.
type answer int

const (
	answerWake answer = iota
	answerWaking
	answerTooMany
	answerFailed
	answerNameInvalid
)

// wakeLimit holds wakes back: one per wakeWindow, at most maxWakesPerHour
// in any hour, and after a failed start a pause that doubles with each
// failure in a row. The zero value is ready to use.
type wakeLimit struct {
	recent       []time.Time
	wakingUntil  time.Time
	failures     int
	blockedUntil time.Time
}

// check says what a join attempt at now would get, and how long until it
// could wake the server, without recording anything.
func (l *wakeLimit) check(now time.Time) (answer, time.Duration) {
	if now.Before(l.wakingUntil) {
		return answerWaking, 0
	}
	if now.Before(l.blockedUntil) {
		return answerFailed, l.blockedUntil.Sub(now)
	}
	l.forget(now)
	if len(l.recent) >= maxWakesPerHour {
		return answerTooMany, l.recent[0].Add(time.Hour).Sub(now)
	}
	return answerWake, 0
}

// try is check, recording the wake when the answer is to wake.
func (l *wakeLimit) try(now time.Time) (answer, time.Duration) {
	a, wait := l.check(now)
	if a == answerWake {
		l.recent = append(l.recent, now)
		l.wakingUntil = now.Add(wakeWindow)
	}
	return a, wait
}

func (l *wakeLimit) forget(now time.Time) {
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(l.recent) && !l.recent[i].After(cut) {
		i++
	}
	l.recent = l.recent[i:]
}

// failed records a start that failed.
func (l *wakeLimit) failed(now time.Time) {
	l.failures++
	d := firstBackoff
	for i := 1; i < l.failures && d < maxBackoff; i++ {
		d *= 2
	}
	l.blockedUntil = now.Add(min(d, maxBackoff))
	l.wakingUntil = time.Time{}
}

func (l *wakeLimit) succeeded() {
	l.failures = 0
	l.blockedUntil = time.Time{}
}

// slept starts a new sleep: the last wake is over.
func (l *wakeLimit) slept() {
	l.wakingUntil = time.Time{}
}
