// Package schedule plans and runs one server's scheduled tasks: restarts with
// in-game warnings, backups, announcements and console commands from a short
// list of safe ones.
//
// Schedules are plain structs that map onto the agent's schedules table, keyed
// by server id; the caller stores them. The agent runs them, so they keep
// working while the panel is down. Times are wall-clock times in each
// schedule's own IANA time zone, and daylight-saving changes are handled on
// purpose: see Timing.
//
// Plan is the pure core. Given schedules, their last runs and the time, it
// says what is due, what was missed and when to look again. A Runner drives
// Plan with an injected clock and calls back into the agent to send console
// commands, start operations and read the player count. Scheduled work that
// is an operation (a restart or a backup) runs with the actor Actor(id).
package schedule

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is what a schedule does.
type Kind string

const (
	KindRestart      Kind = "restart"
	KindBackup       Kind = "backup"
	KindAnnouncement Kind = "announcement"
	KindCommand      Kind = "command"
)

// Schedule is one row of the agent's schedules table.
type Schedule struct {
	ID       string `json:"id"`
	ServerID string `json:"serverId"`
	// Name is an optional label; without one, the UI describes the schedule.
	Name      string    `json:"name,omitempty"`
	Kind      Kind      `json:"kind"`
	Timing    Timing    `json:"timing"`
	Payload   Payload   `json:"payload"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt changes whenever the schedule is edited, enabled or disabled.
	// Times at or before it never run, so a change never triggers a catch-up.
	UpdatedAt time.Time `json:"updatedAt"`
	// LastRun is the latest occurrence the runner handled, or nil. Times at or
	// before LastRun.Due never run again.
	LastRun *Run `json:"lastRun,omitempty"`
}

// IfEmpty is what a restart does when nobody is online.
type IfEmpty string

const (
	// IfEmptyAsPlanned restarts at the planned time, with warnings.
	IfEmptyAsPlanned IfEmpty = ""
	// IfEmptySkip skips the restart.
	IfEmptySkip IfEmpty = "skip"
	// IfEmptyNow restarts right away, without waiting for the warnings.
	IfEmptyNow IfEmpty = "now"
)

// Payload is what a schedule does, by kind. Normalize clears the fields its
// kind does not use.
type Payload struct {
	// WarnSeconds are a restart's in-game warnings, in seconds before it, from
	// 5 to 3600. Warnings of a minute or more are whole minutes. Empty means
	// no warnings.
	WarnSeconds []int `json:"warnSeconds,omitempty"`
	// Message is a restart's extra warning text, or an announcement.
	Message string  `json:"message,omitempty"`
	IfEmpty IfEmpty `json:"ifEmpty,omitempty"`
	// Command is a command schedule's console command; see CheckCommand.
	Command string `json:"command,omitempty"`
	// Note is a backup's note.
	Note string `json:"note,omitempty"`
	// SkipIfPlaying skips a restart or a backup while people are playing;
	// it tries again an hour later unless the next run comes first.
	SkipIfPlaying bool `json:"skipIfPlaying,omitempty"`
	// OnlyIfPlayed skips a backup when nobody played since the last backup.
	OnlyIfPlayed bool `json:"onlyIfPlayed,omitempty"`
}

// MinutesPlaceholder in a restart's message makes the message the whole
// warning, with the time left in its place: "Survival restarts in {minutes}
// minutes." becomes "Survival restarts in 5 minutes.", "… in 1 minute.".
const MinutesPlaceholder = "{minutes}"

// retryAfter is how long a run skipped because people were playing waits
// before it tries again.
const retryAfter = time.Hour

// DefaultWarnings are the warnings a new restart schedule starts with: 10
// minutes, 5 minutes, 1 minute, 30 seconds and 10 seconds before.
func DefaultWarnings() []int { return []int{600, 300, 60, 30, 10} }

// Result is how an occurrence ended.
type Result string

const (
	ResultRunning   Result = "running"
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
	ResultSkipped   Result = "skipped"
	ResultMissed    Result = "missed"
)

// Reason says why an occurrence was skipped, missed or failed.
type Reason string

const (
	ReasonServerStopped  Reason = "server_stopped"
	ReasonServerSleeping Reason = "server_sleeping"
	ReasonNobodyOnline   Reason = "nobody_online"
	ReasonAgentDown      Reason = "agent_down"
	ReasonLate           Reason = "late"
	ReasonBusy           Reason = "busy"
	ReasonRejected       Reason = "rejected"
	ReasonError          Reason = "error"
	ReasonInterrupted    Reason = "interrupted"
	ReasonInvalid        Reason = "invalid"
	// ReasonTurnedOff is a restart called off during its warnings because its
	// schedule was turned off or deleted.
	ReasonTurnedOff Reason = "turned_off"
	// ReasonPeoplePlaying is a restart or backup skipped because people were
	// playing and its schedule says to skip it then.
	ReasonPeoplePlaying Reason = "people_playing"
	// ReasonNobodyPlayed is a backup skipped because nobody played since the
	// last backup.
	ReasonNobodyPlayed Reason = "nobody_played"
)

// Run records one occurrence: the last_run and last_result columns.
type Run struct {
	// Due is the occurrence's planned time.
	Due      time.Time `json:"due"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Result   Result    `json:"result"`
	Reason   Reason    `json:"reason,omitempty"`
	// Detail is the error or the command's output, cleaned and shortened.
	Detail string `json:"detail,omitempty"`
	// OperationID is the agent operation the run started, if any.
	OperationID string `json:"operationId,omitempty"`
	// Players is how many players were online when the run started, if the
	// runner knew.
	Players *int `json:"players,omitempty"`
	// RetryAt is when a run skipped because people were playing tries again;
	// zero when it doesn't. Editing the schedule drops the retry.
	RetryAt time.Time `json:"retryAt,omitzero"`
}

// Text describes the run in English, for logs and the audit trail. The UI
// builds its own text from Result and Reason.
func (r Run) Text() string {
	switch r.Result {
	case ResultRunning:
		return "Running now."
	case ResultSucceeded:
		return "Done."
	case ResultSkipped:
		return "Skipped: " + r.Reason.Text()
	case ResultMissed:
		return "Missed: " + r.Reason.Text()
	case ResultFailed:
		if r.Detail != "" && (r.Reason == ReasonError || r.Reason == ReasonRejected) {
			return "Failed: " + r.Reason.Text() + " " + r.Detail
		}
		return "Failed: " + r.Reason.Text()
	}
	return string(r.Result)
}

// Text is the reason as an English sentence.
func (r Reason) Text() string {
	switch r {
	case ReasonServerStopped:
		return "the server was stopped."
	case ReasonServerSleeping:
		return "the server was asleep because nobody was playing."
	case ReasonNobodyOnline:
		return "nobody was online."
	case ReasonAgentDown:
		return "Playkeeper was not running at that time."
	case ReasonLate:
		return "it could not start close enough to its time."
	case ReasonBusy:
		return "another task kept the server busy for too long."
	case ReasonRejected:
		return "the server did not accept the command."
	case ReasonError:
		return "something went wrong."
	case ReasonInterrupted:
		return "Playkeeper stopped while it was running."
	case ReasonInvalid:
		return "the schedule is not valid any more."
	case ReasonTurnedOff:
		return "the schedule was turned off before the restart."
	case ReasonPeoplePlaying:
		return "people were playing."
	case ReasonNobodyPlayed:
		return "nobody played since the last backup."
	}
	return string(r)
}

// ValidationError is a problem with one field of a schedule. Code and Params
// are stable for the UI's translations; Msg and Hint are the English text.
type ValidationError struct {
	Field  string         `json:"field"`
	Code   string         `json:"code"`
	Msg    string         `json:"message"`
	Hint   string         `json:"hint,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

func (e *ValidationError) Error() string { return e.Msg }

const (
	actorPrefix   = "schedule:"
	maxNameRunes  = 64
	maxMessage    = 256
	maxNote       = 200
	maxWarnings   = 8
	minWarnSecond = 5
	maxWarnSecond = 3600
)

var (
	idRe       = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	serverIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Actor is the operation actor for work a schedule starts.
func Actor(id string) string { return actorPrefix + id }

// ParseActor returns the schedule id in an actor made by Actor.
func ParseActor(actor string) (string, bool) {
	id, ok := strings.CutPrefix(actor, actorPrefix)
	if !ok || !idRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// Normalize trims text, puts warnings and days in order and clears the fields
// the schedule's kinds do not use. Store the normalized schedule.
func (s Schedule) Normalize() Schedule {
	s.Name = strings.TrimSpace(s.Name)
	s.Timing = s.Timing.normalize()
	s.Payload = s.Payload.normalize(s.Kind)
	return s
}

func (p Payload) normalize(k Kind) Payload {
	switch k {
	case KindRestart:
		w := slices.Clone(p.WarnSeconds)
		slices.Sort(w)
		slices.Reverse(w)
		return Payload{WarnSeconds: slices.Compact(w), Message: strings.TrimSpace(p.Message), IfEmpty: p.IfEmpty, SkipIfPlaying: p.SkipIfPlaying}
	case KindAnnouncement:
		return Payload{Message: strings.TrimSpace(p.Message)}
	case KindCommand:
		return Payload{Command: normCommand(p.Command)}
	case KindBackup:
		return Payload{Note: strings.TrimSpace(p.Note), SkipIfPlaying: p.SkipIfPlaying, OnlyIfPlayed: p.OnlyIfPlayed}
	}
	return p
}

// Validate checks a schedule before it is saved. now is used to reject a
// one-off time in the past. The error is a *ValidationError.
func (s Schedule) Validate(now time.Time) error {
	if !idRe.MatchString(s.ID) {
		return &ValidationError{Field: "id", Code: "id_invalid", Msg: "The schedule id must be 1 to 32 lower-case letters or digits."}
	}
	if !serverIDRe.MatchString(s.ServerID) {
		return &ValidationError{Field: "serverId", Code: "server_invalid", Msg: "The schedule needs a valid server id."}
	}
	if utf8.RuneCountInString(s.Name) > maxNameRunes {
		return &ValidationError{Field: "name", Code: "name_too_long",
			Msg: fmt.Sprintf("Names can be at most %d characters.", maxNameRunes), Params: map[string]any{"max": maxNameRunes}}
	}
	if !plainText(s.Name) {
		return &ValidationError{Field: "name", Code: "name_invalid", Msg: "Names can't contain line breaks or control characters."}
	}
	switch s.Kind {
	case KindRestart, KindBackup, KindAnnouncement, KindCommand:
	default:
		return &ValidationError{Field: "kind", Code: "kind_invalid",
			Msg: "Choose what the schedule does: restart, back up, announce or run a command."}
	}
	if err := s.Timing.validate(now); err != nil {
		return err
	}
	if err := s.Payload.validate(s.Kind); err != nil {
		return err
	}
	return s.checkSpacing()
}

func (p Payload) validate(k Kind) error {
	switch k {
	case KindRestart:
		if err := validWarnings(p.WarnSeconds); err != nil {
			return err
		}
		switch p.IfEmpty {
		case IfEmptyAsPlanned, IfEmptySkip, IfEmptyNow:
		default:
			return &ValidationError{Field: "payload.ifEmpty", Code: "if_empty_invalid",
				Msg: "Choose what happens when nobody is online: restart as planned, skip, or restart right away."}
		}
		if p.Message != "" {
			return validMessage(p.Message)
		}
	case KindAnnouncement:
		if p.Message == "" {
			return &ValidationError{Field: "payload.message", Code: "message_required", Msg: "Write the message to announce."}
		}
		return validMessage(p.Message)
	case KindCommand:
		return CheckCommand(p.Command)
	case KindBackup:
		if len(p.Note) > maxNote {
			return &ValidationError{Field: "payload.note", Code: "note_too_long",
				Msg: fmt.Sprintf("Backup notes can be at most %d characters.", maxNote), Params: map[string]any{"max": maxNote}}
		}
		if !plainText(p.Note) {
			return &ValidationError{Field: "payload.note", Code: "note_invalid", Msg: "Backup notes can't contain line breaks or control characters."}
		}
	}
	return nil
}

func validWarnings(ws []int) error {
	if len(ws) > maxWarnings {
		return &ValidationError{Field: "payload.warnSeconds", Code: "warnings_too_many",
			Msg: fmt.Sprintf("Use at most %d warnings.", maxWarnings), Params: map[string]any{"max": maxWarnings}}
	}
	seen := map[int]bool{}
	for _, w := range ws {
		if w < minWarnSecond || w > maxWarnSecond {
			return &ValidationError{Field: "payload.warnSeconds", Code: "warning_range",
				Msg:    "Warnings can be from 5 seconds to 1 hour before the restart.",
				Params: map[string]any{"value": w, "min": minWarnSecond, "max": maxWarnSecond}}
		}
		if w >= 60 && w%60 != 0 {
			return &ValidationError{Field: "payload.warnSeconds", Code: "warning_whole_minutes",
				Msg:    "Warnings of a minute or more must be whole minutes.",
				Params: map[string]any{"value": w}}
		}
		if seen[w] {
			return &ValidationError{Field: "payload.warnSeconds", Code: "warning_duplicate",
				Msg: "Each warning time can only be used once.", Params: map[string]any{"value": w}}
		}
		seen[w] = true
	}
	return nil
}

func validMessage(m string) error {
	if utf8.RuneCountInString(m) > maxMessage {
		return &ValidationError{Field: "payload.message", Code: "message_too_long",
			Msg: fmt.Sprintf("Messages can be at most %d characters.", maxMessage), Params: map[string]any{"max": maxMessage}}
	}
	if !chatSafe(m) {
		return &ValidationError{Field: "payload.message", Code: "message_invalid",
			Msg: "Messages can't contain line breaks or invisible control characters."}
	}
	return nil
}

// checkSpacing refuses schedules that would run too close together: a restart
// needs its warnings plus 15 minutes, and at least an hour.
func (s Schedule) checkSpacing() error {
	r, err := s.Timing.compile()
	if err != nil {
		return err
	}
	gap := r.minGap()
	if gap == 0 {
		return nil
	}
	need := s.minSpacing()
	if gap >= need {
		return nil
	}
	field := "timing.cron"
	if s.Timing.Kind == Interval {
		field = "timing.everyHours"
	}
	return &ValidationError{Field: field, Code: "too_often",
		Msg:    fmt.Sprintf("This would run every %s, but %s need at least %s between runs.", minutesPhrase(gap), s.Kind.plural(), minutesPhrase(need)),
		Params: map[string]any{"gapMinutes": int(gap / time.Minute), "minMinutes": int(need / time.Minute)}}
}

func (s Schedule) minSpacing() time.Duration {
	switch s.Kind {
	case KindRestart:
		return max(time.Hour, s.Payload.lead()+15*time.Minute)
	case KindBackup:
		return time.Hour
	}
	return 5 * time.Minute
}

func (k Kind) plural() string {
	switch k {
	case KindRestart:
		return "restarts"
	case KindBackup:
		return "backups"
	case KindAnnouncement:
		return "announcements"
	case KindCommand:
		return "commands"
	}
	return "schedules"
}

func minutesPhrase(d time.Duration) string {
	m := int(d / time.Minute)
	h, rest := m/60, m%60
	switch {
	case h == 0:
		return plural(m, "minute")
	case rest == 0:
		return plural(h, "hour")
	}
	return plural(h, "hour") + " " + plural(rest, "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// lead is how long before the restart the first warning goes out.
func (p Payload) lead() time.Duration {
	if len(p.WarnSeconds) == 0 {
		return 0
	}
	return time.Duration(slices.Max(p.WarnSeconds)) * time.Second
}

// lead is how long before its due time an occurrence starts.
func (s Schedule) lead() time.Duration {
	if s.Kind == KindRestart {
		return s.Payload.lead()
	}
	return 0
}

// Grace is how late an occurrence may still start.
func (s Schedule) Grace() time.Duration {
	if g := s.Timing.GraceMinutes; g > 0 && g <= maxGraceMinutes {
		return time.Duration(g) * time.Minute
	}
	switch s.Kind {
	case KindRestart:
		return 15 * time.Minute
	case KindBackup:
		return 6 * time.Hour
	case KindAnnouncement:
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

// plainText reports whether s is valid UTF-8 without control characters.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}
