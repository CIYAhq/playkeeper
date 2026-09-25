package schedule

import (
	"cmp"
	"slices"
	"time"
)

// Job is one occurrence of a schedule.
type Job struct {
	Schedule Schedule
	// Due is the occurrence's time. Start is when to begin: Due, or earlier
	// by a restart's warnings.
	Due   time.Time
	Start time.Time
	// Reason says why a missed job was missed.
	Reason Reason
}

// Decision is what Plan decided.
type Decision struct {
	// Due are the occurrences to run now, in the order to run them.
	Due []Job
	// Missed are occurrences too late to run. Record them and move on.
	Missed []Job
	// Wake is when the next occurrence starts, for schedules without a due or
	// missed job. Zero means nothing else is scheduled.
	Wake time.Time
	// Invalid are enabled schedules whose timing cannot be worked out.
	Invalid []Invalid
}

// Invalid is an enabled schedule Plan had to leave out.
type Invalid struct {
	Schedule Schedule
	Err      error
}

// maxLookback bounds how far back Plan looks after a long downtime; anything
// older is missed whatever its grace.
const maxLookback = 400 * 24 * time.Hour

// Plan decides, at now, what a server's runner should do. since is when the
// runner started: occurrences before it were missed because Playkeeper was
// down, later ones because the runner was busy with other jobs.
//
// For each enabled schedule only the latest occurrence due by now counts.
// Earlier ones are superseded, so a long downtime never causes a burst. That
// occurrence runs if it is at most the schedule's Grace late, and is missed
// otherwise. A restart is due from its first warning.
func Plan(schedules []Schedule, now, since time.Time) Decision {
	var d Decision
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		r, err := s.Timing.compile()
		if err != nil {
			d.Invalid = append(d.Invalid, Invalid{Schedule: s, Err: err})
			continue
		}
		lead := s.lead()
		cursor := later(s.cursor(), now.Add(-maxLookback))
		horizon := now.Add(lead)
		var latest time.Time
		for t := r.next(cursor); !t.IsZero() && !t.After(horizon); t = r.next(t) {
			latest = t
		}
		if latest.IsZero() {
			if next := r.next(cursor); !next.IsZero() {
				d.Wake = earliest(d.Wake, next.Add(-lead))
			}
			continue
		}
		job := Job{Schedule: s, Due: latest, Start: latest.Add(-lead)}
		switch {
		case now.Sub(latest) <= s.Grace():
			d.Due = append(d.Due, job)
		case latest.Before(since):
			job.Reason = ReasonAgentDown
			d.Missed = append(d.Missed, job)
		default:
			job.Reason = ReasonLate
			d.Missed = append(d.Missed, job)
		}
	}
	slices.SortFunc(d.Due, func(a, b Job) int {
		return cmp.Or(a.Start.Compare(b.Start), a.Due.Compare(b.Due),
			cmp.Compare(a.Schedule.Kind.order(), b.Schedule.Kind.order()),
			cmp.Compare(a.Schedule.ID, b.Schedule.ID))
	})
	return d
}

// NextRun returns when s next runs after now, or the zero time if it is
// disabled, not valid or finished.
func NextRun(s Schedule, now time.Time) time.Time {
	if !s.Enabled {
		return time.Time{}
	}
	r, err := s.Timing.compile()
	if err != nil {
		return time.Time{}
	}
	return r.next(later(s.cursor(), now))
}

// cursor is the time at or before which s never runs.
func (s Schedule) cursor() time.Time {
	c := later(s.CreatedAt, s.UpdatedAt)
	if s.LastRun != nil {
		c = later(c, s.LastRun.Due)
	}
	return c
}

// order breaks ties between jobs due at the same moment: quick ones first,
// the restart last.
func (k Kind) order() int {
	switch k {
	case KindAnnouncement:
		return 0
	case KindCommand:
		return 1
	case KindBackup:
		return 2
	case KindRestart:
		return 3
	}
	return 4
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}
