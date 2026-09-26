package api

import "time"

// Wave 7 (0.4.0): schedules, sleep when nobody's playing, backup rules with
// copies somewhere else, and disk space. The routes' own shapes come from the
// schedule, sleep, retention, offsite and diskusage packages; these are what
// the wave adds to a server's and a machine's status.

// PhaseAsleep is a server Playkeeper stopped because nobody was playing. A
// stand-in answers on its game port, and the first join wakes it.
const PhaseAsleep Phase = "asleep"

// DesiredSleeping is a sleeping server's desired state: stopped, with the
// stand-in on its port, and running again once someone joins.
const DesiredSleeping = "sleeping"

// SleepStatus is a server's "Sleep when nobody's playing" setting and what it
// is doing.
type SleepStatus struct {
	Enabled     bool `json:"enabled"`
	IdleMinutes int  `json:"idleMinutes"`
	// AsleepSince is when the server fell asleep, while it sleeps.
	AsleepSince *time.Time `json:"asleepSince,omitempty"`
	// Listening is true while the stand-in answers on the game port.
	Listening bool `json:"listening"`
	// SleepAt is when the empty server falls asleep if nobody joins.
	SleepAt *time.Time `json:"sleepAt,omitempty"`
}

// BackupRefusal is the scheduled backups refused since the last backup that
// succeeded, because world saving couldn't be paused. Scheduled backups
// never stop a running server; a backup with the server stopped needs no
// pause.
type BackupRefusal struct {
	// At is when the last refused run was, and Since the first of those in a
	// row; Count is how many there were.
	At    time.Time `json:"at"`
	Since time.Time `json:"since"`
	Count int       `json:"count"`
	// Kind is why the last one was refused: a backup error's kind, such as
	// "unexpected_reply", or "not_online" while the server was starting or
	// stopping. Error and Hint say it in plain English.
	Kind  string `json:"kind"`
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
	// ScheduleID and OperationID are the last refused run's schedule and
	// operation.
	ScheduleID  string `json:"scheduleId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
}
