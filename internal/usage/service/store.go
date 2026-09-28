package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// migrations are append-only (internal/store). The tables hold what the
// reports say and when, to the hour; never an address or anything else about
// the request.
var migrations = []string{`
CREATE TABLE installs (
	id         TEXT PRIMARY KEY,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL DEFAULT 0,
	outcome    TEXT NOT NULL DEFAULT '',
	outcome_at INTEGER NOT NULL DEFAULT 0,
	step       TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL DEFAULT '',
	channel    TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT '',
	version    TEXT NOT NULL DEFAULT '',
	os         TEXT NOT NULL DEFAULT '',
	os_version TEXT NOT NULL DEFAULT '',
	arch       TEXT NOT NULL DEFAULT '',
	address    TEXT NOT NULL DEFAULT '',
	servers    INTEGER NOT NULL DEFAULT 0,
	running    INTEGER NOT NULL DEFAULT 0,
	test       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX installs_last_seen ON installs(last_seen);
CREATE INDEX installs_started_at ON installs(started_at);
CREATE INDEX installs_outcome_at ON installs(outcome_at);
CREATE TABLE active_days (
	day INTEGER NOT NULL,
	id  TEXT NOT NULL,
	PRIMARY KEY (day, id)
) WITHOUT ROWID;
`}

// keepFor is how long an install the service hears nothing more from, and
// each day's list of active installs, are kept.
const keepFor = 400 * 24 * time.Hour

const day = 24 * time.Hour

// hour is t to the hour, as the tables keep times.
func hour(t time.Time) int64 { return t.Unix() - t.Unix()%3600 }

// dayOf is t's UTC day, as a count of days since 1970.
func dayOf(t time.Time) int64 { return t.Unix() / 86400 }

var errTooManyNew = errors.New("too many new installs today")

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isNew reports whether the service has not heard of id before.
func (s *Service) isNew(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM installs WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// admit refuses an install ID the service hasn't heard of once the day's
// new ones are used up.
func (s *Service) admit(ctx context.Context, id string) error {
	fresh, err := s.isNew(ctx, id)
	if err != nil {
		return err
	}
	if fresh {
		if ok, _ := s.newIDs.allow(""); !ok {
			return errTooManyNew
		}
	}
	return nil
}

// recordHeartbeat keeps what h says about its install, and that it ran
// today.
func (s *Service) recordHeartbeat(ctx context.Context, h usage.Heartbeat) error {
	if err := s.admit(ctx, h.ID); err != nil {
		return err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO installs(id, first_seen, last_seen, source, channel, kind, version, os, os_version, arch, address, servers, running, test)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET last_seen = excluded.last_seen,
			source = CASE WHEN excluded.source != '' THEN excluded.source ELSE installs.source END,
			channel = CASE WHEN excluded.channel != '' THEN excluded.channel ELSE installs.channel END,
			kind = excluded.kind, version = excluded.version, os = excluded.os, os_version = excluded.os_version,
			arch = excluded.arch, address = excluded.address, servers = excluded.servers, running = excluded.running,
			test = MAX(installs.test, excluded.test)`,
		h.ID, hour(now), hour(now), h.Source, h.Channel, h.Kind, h.Version, h.OS, h.OSVersion, h.Arch, h.Address, h.Servers, h.Running, boolInt(h.Test))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO active_days(day, id) VALUES(?, ?)`, dayOf(now), h.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// recordInstall keeps an installer event: when the plan was accepted, or how
// the install ended.
func (s *Service) recordInstall(ctx context.Context, e usage.Install) error {
	if err := s.admit(ctx, e.ID); err != nil {
		return err
	}
	now := hour(s.now())
	var started, outcomeAt int64
	outcome, step := "", ""
	if e.Event == usage.EventStarted {
		started = now
	} else {
		outcome, outcomeAt, step = e.Event, now, e.Step
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO installs(id, first_seen, started_at, outcome, outcome_at, step, source, channel, kind, version, os, os_version, arch, test)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			started_at = CASE WHEN excluded.started_at != 0 THEN excluded.started_at ELSE installs.started_at END,
			outcome = CASE WHEN excluded.outcome != '' THEN excluded.outcome ELSE installs.outcome END,
			outcome_at = CASE WHEN excluded.outcome != '' THEN excluded.outcome_at ELSE installs.outcome_at END,
			step = CASE WHEN excluded.outcome != '' THEN excluded.step ELSE installs.step END,
			source = excluded.source,
			channel = CASE WHEN excluded.channel != '' THEN excluded.channel ELSE installs.channel END,
			kind = excluded.kind, version = excluded.version, os = excluded.os, os_version = excluded.os_version, arch = excluded.arch,
			test = MAX(installs.test, excluded.test)`,
		e.ID, now, started, outcome, outcomeAt, step, e.Source, e.Channel, e.Kind, e.Version, e.OS, e.OSVersion, e.Arch, boolInt(e.Test))
	return err
}

// prune forgets installs the service has heard nothing from for keepFor,
// and older days' lists of active installs.
func (s *Service) prune(ctx context.Context, now time.Time) {
	cut := now.Add(-keepFor)
	_, err := s.db.ExecContext(ctx, `DELETE FROM installs WHERE MAX(first_seen, last_seen, started_at, outcome_at) < ?`, cut.Unix())
	s.logErr("Could not forget old installs", err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM active_days WHERE day < ?`, dayOf(cut))
	s.logErr("Could not forget old days", err)
}
