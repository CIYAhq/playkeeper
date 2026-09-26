package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// migrations of names.db; see internal/store.
var migrations = []string{`
CREATE TABLE names (
	name          TEXT PRIMARY KEY,
	key           TEXT NOT NULL,
	state         TEXT NOT NULL,
	ipv4          TEXT NOT NULL DEFAULT '',
	ipv6          TEXT NOT NULL DEFAULT '',
	claimed_at    INTEGER NOT NULL,
	refreshed_at  INTEGER NOT NULL,
	lapsed_at     INTEGER NOT NULL DEFAULT 0,
	released_at   INTEGER NOT NULL DEFAULT 0,
	version       INTEGER NOT NULL DEFAULT 1,
	synced        INTEGER NOT NULL DEFAULT 0,
	sync_failures INTEGER NOT NULL DEFAULT 0,
	sync_after    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX names_by_key ON names (key);
CREATE TABLE challenges (
	name       TEXT NOT NULL REFERENCES names (name) ON DELETE CASCADE,
	value      TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	PRIMARY KEY (name, value)
) STRICT;
CREATE TABLE servers (
	name  TEXT NOT NULL REFERENCES names (name) ON DELETE CASCADE,
	label TEXT NOT NULL,
	port  INTEGER NOT NULL,
	PRIMARY KEY (name, label)
) STRICT;
CREATE TABLE nonces (
	key        TEXT NOT NULL,
	nonce      TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	PRIMARY KEY (key, nonce)
) STRICT;
CREATE INDEX nonces_by_expiry ON nonces (expires_at);
`, `
ALTER TABLE names ADD COLUMN network TEXT NOT NULL DEFAULT '';
CREATE INDEX names_by_network ON names (network);
`, `
ALTER TABLE names ADD COLUMN alive_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE names ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE names ADD COLUMN failed_checks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE names ADD COLUMN lapse_reason TEXT NOT NULL DEFAULT '';
CREATE INDEX names_by_check ON names (state, checked_at);
`, `
CREATE TABLE cert_sets (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	started_at INTEGER NOT NULL,
	first      INTEGER NOT NULL,
	challenges INTEGER NOT NULL
) STRICT;
CREATE INDEX cert_sets_by_name ON cert_sets (name, started_at);
CREATE INDEX cert_sets_by_start ON cert_sets (started_at);
`}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// writeTx runs f in a transaction that takes the write lock first (BEGIN
// IMMEDIATE). A transaction that reads before it writes cannot wait for
// the lock: SQLite fails it with "database is locked" as soon as another
// connection has written in between.
func (s *Service) writeTx(ctx context.Context, f func(q queryer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err = f(conn); err == nil {
		_, err = conn.ExecContext(ctx, `COMMIT`)
	}
	if err != nil {
		if _, rbErr := conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`); rbErr != nil {
			// A connection still inside a transaction must not go back to
			// the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	return err
}

type nameRow struct {
	Name, Key, State                             string
	IPv4, IPv6                                   string
	ClaimedAt, RefreshedAt, LapsedAt, ReleasedAt int64
	Version, Synced                              int64
	// Network is the network the name counts against (see nameNetwork).
	Network string
	// AliveAt is when the address last answered the liveness check since
	// the claim (0 if never), CheckedAt when it was last checked, and
	// FailedChecks how many checks in a row it did not answer.
	AliveAt, CheckedAt int64
	FailedChecks       int
	LapseReason        string
}

const nameColumns = `name, key, state, ipv4, ipv6, claimed_at, refreshed_at, lapsed_at, released_at, version, synced, network,
	alive_at, checked_at, failed_checks, lapse_reason`

func scanName(sc interface{ Scan(...any) error }) (*nameRow, error) {
	var n nameRow
	err := sc.Scan(&n.Name, &n.Key, &n.State, &n.IPv4, &n.IPv6, &n.ClaimedAt, &n.RefreshedAt, &n.LapsedAt, &n.ReleasedAt, &n.Version, &n.Synced, &n.Network,
		&n.AliveAt, &n.CheckedAt, &n.FailedChecks, &n.LapseReason)
	return &n, err
}

// getName returns the row of name, or nil if nobody holds it.
func (s *Service) getName(ctx context.Context, q queryer, name string) (*nameRow, error) {
	n, err := scanName(q.QueryRowContext(ctx, `SELECT `+nameColumns+` FROM names WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func (s *Service) namesOf(ctx context.Context, key string) ([]*nameRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nameColumns+` FROM names WHERE key = ? ORDER BY name`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nameRow
	for rows.Next() {
		n, err := scanName(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// heldBy counts the names key holds, other than except, that are not
// released.
func (s *Service) heldBy(ctx context.Context, q queryer, key, except string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM names WHERE key = ? AND name != ? AND state != ?`, key, except, names.StateReleased).Scan(&n)
	return n, err
}

type challengeRow struct {
	Value     string
	ExpiresAt int64
}

func (s *Service) challengeRows(ctx context.Context, name string) ([]challengeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value, expires_at FROM challenges WHERE name = ? ORDER BY value`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []challengeRow
	for rows.Next() {
		var c challengeRow
		if err := rows.Scan(&c.Value, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type serverRow struct {
	Label string
	Port  int
}

func (s *Service) serverRows(ctx context.Context, name string) ([]serverRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT label, port FROM servers WHERE name = ? ORDER BY label`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []serverRow
	for rows.Next() {
		var r serverRow
		if err := rows.Scan(&r.Label, &r.Port); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nameStrings runs a query that selects one column of names.
func (s *Service) nameStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// info is the client's view of a name.
func (s *Service) info(ctx context.Context, row *nameRow) (names.Name, error) {
	n := names.Name{
		Name: row.Name, Address: names.Address(row.Name, s.base), State: row.State,
		IPv4: row.IPv4, IPv6: row.IPv6,
		ClaimedAt: time.Unix(row.ClaimedAt, 0).UTC(), RefreshedAt: time.Unix(row.RefreshedAt, 0).UTC(),
		Servers: []names.Server{}, DNS: names.DNSOK,
	}
	if row.Synced < row.Version {
		n.DNS = names.DNSPending
	}
	if row.AliveAt > 0 {
		n.AnsweredAt = time.Unix(row.AliveAt, 0).UTC()
	}
	switch row.State {
	case names.StateActive:
		n.RefreshBy = n.RefreshedAt.Add(lapseAfter)
		n.FreedAt = n.RefreshBy.Add(freeAfter)
		n.AnswerBy = time.Unix(max(row.AliveAt, row.ClaimedAt), 0).UTC().Add(unansweredAfter)
	case names.StateLapsed:
		n.LapseReason = row.LapseReason
		n.FreedAt = time.Unix(row.LapsedAt, 0).UTC().Add(freeAfter)
		if row.LapseReason == names.LapseNoAnswer && row.AliveAt == 0 {
			n.FreedAt = time.Unix(row.LapsedAt, 0).UTC().Add(freeSilentAfter)
		}
	case names.StateReleased:
		n.FreedAt = time.Unix(row.ReleasedAt, 0).UTC().Add(releaseHold)
	}
	servers, err := s.serverRows(ctx, row.Name)
	if err != nil {
		return names.Name{}, err
	}
	for _, sv := range servers {
		n.Servers = append(n.Servers, names.Server{Label: sv.Label, Address: names.ServerAddress(sv.Label, row.Name, s.base), Port: sv.Port, DNS: n.DNS})
	}
	return n, nil
}
