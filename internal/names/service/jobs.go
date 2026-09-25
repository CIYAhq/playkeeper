package service

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// keepBackups is how many daily database snapshots stay in DataDir/backups.
const keepBackups = 7

// tick is one round of the periodic work.
func (s *Service) tick(ctx context.Context) {
	now := s.now()
	s.block.reload(s.log)
	s.expire(ctx, now)
	s.retry(ctx, now)
	s.backup(now)
}

func (s *Service) logErr(what string, err error) {
	if err != nil {
		s.log.Error(what, "error", err)
	}
}

// expire removes challenge records that were never cleared, takes the
// records away from names that stopped refreshing, releases blocklisted
// names, and frees names whose grace period or hold is over.
func (s *Service) expire(ctx context.Context, now time.Time) {
	t := now.Unix()
	_, err := s.db.ExecContext(ctx, `DELETE FROM nonces WHERE expires_at < ?`, t)
	s.logErr("Could not forget old nonces", err)

	expired, err := s.nameStrings(ctx, `SELECT DISTINCT name FROM challenges WHERE expires_at <= ?`, t)
	s.logErr("Could not list expired challenges", err)
	for _, name := range expired {
		err := s.bump(ctx, name, `DELETE FROM challenges WHERE name = ? AND expires_at <= ?`, name, t)
		s.logErr("Could not expire a challenge record", err)
		if err == nil {
			s.log.Info("Removed a challenge record that was not cleared", "name", name)
			_ = s.sync(ctx, name)
		}
	}

	stale, err := s.nameStrings(ctx, `SELECT name FROM names WHERE state = ? AND refreshed_at <= ?`, names.StateActive, now.Add(-lapseAfter).Unix())
	s.logErr("Could not list names to lapse", err)
	for _, name := range stale {
		err := s.bump(ctx, name, `UPDATE names SET state = ?, lapsed_at = ? WHERE name = ? AND state = ?`, names.StateLapsed, t, name, names.StateActive)
		if err == nil {
			_, err = s.db.ExecContext(ctx, `DELETE FROM challenges WHERE name = ?`, name)
		}
		s.logErr("Could not lapse a name", err)
		if err == nil {
			s.log.Info("A name was not refreshed in time; its records are removed", "name", name)
			_ = s.sync(ctx, name)
		}
	}

	held, err := s.nameStrings(ctx, `SELECT name FROM names WHERE state != ?`, names.StateReleased)
	s.logErr("Could not list names", err)
	for _, name := range held {
		if !s.block.has(name) {
			continue
		}
		if err := s.releaseName(ctx, name, "on the blocklist"); err != nil {
			s.logErr("Could not release a blocklisted name", err)
			continue
		}
		_ = s.sync(ctx, name)
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM names WHERE synced >= version AND
		((state = ? AND lapsed_at <= ?) OR (state = ? AND released_at <= ?))`,
		names.StateLapsed, now.Add(-freeAfter).Unix(), names.StateReleased, now.Add(-releaseHold).Unix())
	s.logErr("Could not free names", err)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.log.Info("Freed names nobody kept", "count", n)
		}
	}
}

// bump runs a change to a name and marks its records for syncing.
func (s *Service) bump(ctx context.Context, name, query string, args ...any) error {
	return s.writeTx(ctx, func(q queryer) error {
		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `UPDATE names SET version = version + 1 WHERE name = ?`, name)
		return err
	})
}

// retry syncs names whose last sync failed, once their back-off is over.
func (s *Service) retry(ctx context.Context, now time.Time) {
	due, err := s.nameStrings(ctx, `SELECT name FROM names WHERE version > synced AND sync_after <= ? ORDER BY sync_after LIMIT 50`, now.Unix())
	s.logErr("Could not list names to sync", err)
	for _, name := range due {
		if ctx.Err() != nil {
			return
		}
		_ = s.sync(ctx, name)
	}
}

// backup writes one consistent snapshot of the database a day into
// DataDir/backups and keeps the newest keepBackups, so file-level backups
// of the data directory always contain a usable copy.
func (s *Service) backup(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if s.lastBackup == day {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.logErr("Could not create the backups directory", err)
		return
	}
	s.lastBackup = day
	path := filepath.Join(dir, "names-"+day+".db")
	if _, err := os.Stat(path); err != nil {
		tmp := path + ".partial"
		os.Remove(tmp)
		_, err := s.db.Exec(`VACUUM INTO ?`, tmp)
		if err == nil {
			err = os.Chmod(tmp, 0o600)
		}
		if err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			s.logErr("Could not write the daily database snapshot", err)
			os.Remove(tmp)
			return
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var snaps []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "names-") && strings.HasSuffix(n, ".db") {
			snaps = append(snaps, n)
		}
	}
	slices.Sort(snaps)
	for len(snaps) > keepBackups {
		s.logErr("Could not remove an old snapshot", os.Remove(filepath.Join(dir, snaps[0])))
		snaps = snaps[1:]
	}
}
