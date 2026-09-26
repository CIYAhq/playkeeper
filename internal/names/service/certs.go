package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// certSet is what counting a challenge value started: nothing when it
// joined an open set, else a set that is the name's first certificate or a
// renewal.
type certSet struct{ started, first bool }

// countChallenge counts a new challenge value of row's name towards its
// certificate sets (see certSetWindow), refusing a new set when the name
// or, for a first certificate, everyone used up the week's.
func (s *Service) countChallenge(ctx context.Context, q queryer, row *nameRow, now time.Time) (certSet, error) {
	res, err := q.ExecContext(ctx, `UPDATE cert_sets SET challenges = challenges + 1 WHERE id = (
		SELECT id FROM cert_sets WHERE name = ? AND started_at > ? AND challenges < ? ORDER BY started_at DESC, id DESC LIMIT 1)`,
		row.Name, now.Add(-certSetWindow).Unix(), challengesPerSet)
	if err != nil {
		return certSet{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return certSet{}, err
	}

	weekAgo := now.Add(-week).Unix()
	var sets int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM cert_sets WHERE name = ? AND started_at > ?`, row.Name, weekAgo).Scan(&sets); err != nil {
		return certSet{}, err
	}
	if sets >= certSetsPerName {
		var oldest int64
		if err := q.QueryRowContext(ctx, `SELECT started_at FROM cert_sets WHERE name = ? AND started_at > ? ORDER BY started_at LIMIT 1 OFFSET ?`,
			row.Name, weekAgo, sets-certSetsPerName).Scan(&oldest); err != nil {
			return certSet{}, err
		}
		return certSet{}, certificateLimit(now, oldest, "name", certSetsPerName,
			fmt.Sprintf("%s has asked for %d certificates in the last 7 days, the most one name can.", names.Address(row.Name, s.base), certSetsPerName),
			"Every attempt counts, even one that failed.")
	}

	var earlier int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM cert_sets WHERE name = ? AND started_at >= ? AND started_at > ?`,
		row.Name, row.ClaimedAt, now.Add(-renewalLookback).Unix()).Scan(&earlier); err != nil {
		return certSet{}, err
	}
	first := earlier == 0
	if first {
		var used int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM cert_sets WHERE first = 1 AND started_at > ?`, weekAgo).Scan(&used); err != nil {
			return certSet{}, err
		}
		if budget := s.cfg.NewCertificates; used >= budget {
			var oldest int64
			if err := q.QueryRowContext(ctx, `SELECT started_at FROM cert_sets WHERE first = 1 AND started_at > ? ORDER BY started_at LIMIT 1 OFFSET ?`,
				weekAgo, used-budget).Scan(&oldest); err != nil {
				return certSet{}, err
			}
			s.alerts.send(alertCertificates, fmt.Sprintf("New certificates are refused for now: the %d a week that %s allows are used up. See \"Limits\" in services/names/README.md.",
				budget, EnvNewCertificates))
			return certSet{}, certificateLimit(now, oldest, "all", budget,
				fmt.Sprintf("New certificates for %s names are paused: this week's %d are used up, which keeps %s within Let's Encrypt's limits.", s.base, budget, s.base),
				"Players can still join, and names that have a certificate can renew it.")
		}
	}
	_, err = q.ExecContext(ctx, `INSERT INTO cert_sets (name, started_at, first, challenges) VALUES (?, ?, ?, 1)`, row.Name, now.Unix(), first)
	return certSet{started: true, first: first}, err
}

// certificateLimit refuses a certificate set until a week after the set
// started at oldest (a Unix time) that holds the place.
func certificateLimit(now time.Time, oldest int64, scope string, limit int, msg, hint string) error {
	at := time.Unix(oldest, 0).UTC().Add(week)
	shown := at.Add(time.Minute - time.Second).Truncate(time.Minute)
	return &names.Error{Status: http.StatusTooManyRequests, Code: names.CodeCertificateLimit, RetryAfter: at.Sub(now),
		Params:  map[string]any{"scope": scope, "limit": limit, "retryAt": at.Format(time.RFC3339)},
		Message: msg + " Try again after " + shown.Format("2 January 2006 15:04 UTC") + ".", Hint: hint}
}

// logCertSet logs a set countChallenge started, with the week's counts of
// new certificates and renewals.
func (s *Service) logCertSet(ctx context.Context, name string, set certSet, now time.Time) {
	if !set.started {
		return
	}
	var fresh, renewals int
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(first), 0), coalesce(sum(1 - first), 0) FROM cert_sets WHERE started_at > ?`,
		now.Add(-week).Unix()).Scan(&fresh, &renewals)
	s.logErr("Could not count this week's certificates", err)
	kind := "renewal"
	if set.first {
		kind = "new"
	}
	s.log.Info("A name asked for a certificate", "name", name, "kind", kind,
		"new_this_week", fresh, "new_budget", s.cfg.NewCertificates, "renewals_this_week", renewals)
}
