package panel

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

const remoteKind = "remote"

// linkStore keeps machine links' join codes and machines in panel.db. Joined
// machines are rows of the machines table with kind 'remote'.
type linkStore struct{ db *sql.DB }

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (st *linkStore) JoinCodes(ctx context.Context) ([]machinelink.JoinCode, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT id, hash, created_at, expires_at, created_by, used_at, machine_id FROM machine_join_codes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []machinelink.JoinCode
	for rows.Next() {
		var c machinelink.JoinCode
		var created, expires, used int64
		if err := rows.Scan(&c.ID, &c.Hash, &created, &expires, &c.CreatedBy, &used, &c.MachineID); err != nil {
			return nil, err
		}
		c.CreatedAt, c.ExpiresAt, c.UsedAt = fromMillis(created), fromMillis(expires), fromMillis(used)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (st *linkStore) AddJoinCode(ctx context.Context, c machinelink.JoinCode) error {
	_, err := st.db.ExecContext(ctx, `INSERT INTO machine_join_codes(id, hash, created_at, expires_at, created_by, used_at, machine_id) VALUES(?,?,?,?,?,?,?)`,
		c.ID, slices.Clone(c.Hash), millis(c.CreatedAt), millis(c.ExpiresAt), c.CreatedBy, millis(c.UsedAt), c.MachineID)
	return err
}

func (st *linkStore) DeleteJoinCode(ctx context.Context, id string) error {
	res, err := st.db.ExecContext(ctx, `DELETE FROM machine_join_codes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return machinelink.ErrNotFound
	}
	return nil
}

// Pair marks the code used and adds the machine to the first project in one
// transaction. The address the code's command dialed becomes the machine's
// endpoint.
func (st *linkStore) Pair(ctx context.Context, codeID string, m machinelink.Machine) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE machine_join_codes SET used_at = ?, machine_id = ? WHERE id = ? AND used_at = 0`, millis(m.JoinedAt), m.ID, codeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var used int64
		switch err := tx.QueryRowContext(ctx, `SELECT used_at FROM machine_join_codes WHERE id = ?`, codeID).Scan(&used); {
		case isNoRows(err):
			return machinelink.ErrNotFound
		case err != nil:
			return err
		}
		return machinelink.ErrCodeUsed
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO machines(id, project_id, name, kind, endpoint, created_at, public_key, joined_from, created_by, version, last_seen, last_addr)
		SELECT ?, id, ?, ?, (SELECT dials FROM machine_join_codes WHERE id = ?), ?, ?, ?, ?, ?, ?, ? FROM projects ORDER BY created_at LIMIT 1`,
		m.ID, m.Name, remoteKind, codeID, millis(m.JoinedAt), []byte(m.PublicKey), m.JoinedFrom, m.CreatedBy, m.Version, millis(m.LastSeen), m.LastAddr)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("there is no project to add the machine to")
	}
	return tx.Commit()
}

func (st *linkStore) JoinFailures(ctx context.Context) ([]machinelink.JoinFailure, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT at, network FROM machine_join_failures ORDER BY at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []machinelink.JoinFailure
	for rows.Next() {
		var at int64
		var network string
		if err := rows.Scan(&at, &network); err != nil {
			return nil, err
		}
		// A network that doesn't parse counts with the addresses that
		// couldn't be read.
		from, _ := netip.ParsePrefix(network)
		out = append(out, machinelink.JoinFailure{At: fromMillis(at), From: from})
	}
	return out, rows.Err()
}

func (st *linkStore) SetJoinFailures(ctx context.Context, fails []machinelink.JoinFailure) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM machine_join_failures`); err != nil {
		return err
	}
	for _, f := range fails {
		network := ""
		if f.From.IsValid() {
			network = f.From.String()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO machine_join_failures(at, network) VALUES(?, ?)`, millis(f.At), network); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const machineColumns = `id, name, public_key, created_at, joined_from, created_by, version, last_seen, last_addr, revoked_at, revoked_by`

func scanMachine(sc interface{ Scan(...any) error }) (machinelink.Machine, error) {
	var m machinelink.Machine
	var key []byte
	var joined, seen, revoked int64
	if err := sc.Scan(&m.ID, &m.Name, &key, &joined, &m.JoinedFrom, &m.CreatedBy, &m.Version, &seen, &m.LastAddr, &revoked, &m.RevokedBy); err != nil {
		return machinelink.Machine{}, err
	}
	m.PublicKey = ed25519.PublicKey(key)
	m.JoinedAt, m.LastSeen, m.RevokedAt = fromMillis(joined), fromMillis(seen), fromMillis(revoked)
	return m, nil
}

func (st *linkStore) Machines(ctx context.Context) ([]machinelink.Machine, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+machineColumns+` FROM machines WHERE kind = ? ORDER BY created_at, id`, remoteKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []machinelink.Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (st *linkStore) Machine(ctx context.Context, id string) (machinelink.Machine, bool, error) {
	m, err := scanMachine(st.db.QueryRowContext(ctx, `SELECT `+machineColumns+` FROM machines WHERE id = ? AND kind = ?`, id, remoteKind))
	if isNoRows(err) {
		return machinelink.Machine{}, false, nil
	}
	return m, err == nil, err
}

func (st *linkStore) MachineByKey(ctx context.Context, key ed25519.PublicKey) (machinelink.Machine, bool, error) {
	m, err := scanMachine(st.db.QueryRowContext(ctx, `SELECT `+machineColumns+` FROM machines WHERE public_key = ? AND kind = ?`, []byte(key), remoteKind))
	if isNoRows(err) {
		return machinelink.Machine{}, false, nil
	}
	return m, err == nil, err
}

func (st *linkStore) Seen(ctx context.Context, id string, at time.Time, version, addr string) error {
	res, err := st.db.ExecContext(ctx, `UPDATE machines SET last_seen = MAX(last_seen, ?),
		version = CASE WHEN ? = '' THEN version ELSE ? END,
		last_addr = CASE WHEN ? = '' THEN last_addr ELSE ? END
		WHERE id = ? AND kind = ?`, millis(at), version, version, addr, addr, id, remoteKind)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return machinelink.ErrNotFound
	}
	return nil
}

func (st *linkStore) Revoke(ctx context.Context, id string, at time.Time, by string) error {
	res, err := st.db.ExecContext(ctx, `UPDATE machines SET revoked_at = ?, revoked_by = ? WHERE id = ? AND kind = ? AND revoked_at = 0`, millis(at), by, id, remoteKind)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, found, err := st.Machine(ctx, id); err != nil {
		return err
	} else if !found {
		return machinelink.ErrNotFound
	}
	return nil
}
