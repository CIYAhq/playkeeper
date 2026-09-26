package agent

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Keys of 0.1.0 and 0.2.0's single server; migrateSingleServer moves them to
// the servers table. The update keys (update.go) stay machine-wide.
const (
	kvServerConfig    = "server_config"
	kvDesired         = "desired_state"
	kvCollectingSince = "collecting_since"
	kvLogCursor       = "log_cursor"
)

func (a *Agent) kvGet(key string) (string, bool, error) {
	var v string
	err := a.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (a *Agent) kvSet(key, value string) error {
	_, err := a.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *server) serverConfig() (*api.ServerConfig, error) {
	var v string
	err := s.db.QueryRow(`SELECT config FROM servers WHERE id = ?`, s.id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sc api.ServerConfig
	if err := json.Unmarshal([]byte(v), &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *server) saveServerConfig(sc api.ServerConfig) error { return saveConfig(s.db, s.id, sc) }

// execer runs a statement: the database, or a transaction a change is part
// of.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// saveConfig saves the settings of the server with id through ex.
func saveConfig(ex execer, id string, sc api.ServerConfig) error {
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	// The type column follows the software, which a restore or a modpack
	// can change; configs made before 0.3.0 have no type and stay Paper.
	res, err := ex.Exec(`UPDATE servers SET config = ?, type = CASE WHEN ? = '' THEN type ELSE ? END WHERE id = ?`, string(b), sc.Type, sc.Type, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errNotFound("Server")
	}
	return nil
}

func (s *server) desired() string {
	var v string
	if err := s.db.QueryRow(`SELECT desired FROM servers WHERE id = ?`, s.id).Scan(&v); err != nil {
		return api.DesiredStopped
	}
	return v
}

func (s *server) setDesired(d string) error {
	_, err := s.db.Exec(`UPDATE servers SET desired = ? WHERE id = ?`, d, s.id)
	return err
}

func (s *server) collectingSince() *time.Time {
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT collecting_since FROM servers WHERE id = ?`, s.id).Scan(&v); err != nil || !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

func (s *server) setCollectingSince(t time.Time) {
	_, _ = s.db.Exec(`UPDATE servers SET collecting_since = ? WHERE id = ? AND collecting_since IS NULL`, t.UnixMilli(), s.id)
}

// audit records a machine-wide action; server.audit records a server's.
func (a *Agent) audit(actor, action, target, result, detail string) {
	a.auditFor("", actor, action, target, result, detail)
}

func (s *server) audit(actor, action, target, result, detail string) {
	s.auditFor(s.id, actor, action, target, result, detail)
}

func (a *Agent) auditFor(serverID, actor, action, target, result, detail string) {
	if actor == "" {
		actor = "unknown"
	}
	if _, err := a.db.Exec(`INSERT INTO audit(ts, actor, action, target, result, detail, server_id) VALUES(?,?,?,?,?,?,?)`,
		a.now().UnixMilli(), actor, action, target, result, detail, serverID); err != nil {
		a.log.Error("audit write failed", "err", err)
	}
}

func (a *Agent) listAudit(limit int) ([]api.AuditEntry, error) {
	rows, err := a.db.Query(`SELECT id, server_id, ts, actor, action, target, result, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AuditEntry{}
	for rows.Next() {
		var e api.AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &e.ServerID, &ts, &e.Actor, &e.Action, &e.Target, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (a *Agent) saveOperation(op *api.Operation) {
	detail, _ := json.Marshal(op.Detail)
	var finished any
	if op.FinishedAt != nil {
		finished = op.FinishedAt.UnixMilli()
	}
	_, err := a.db.Exec(`INSERT INTO operations(id, server_id, kind, status, phase, actor, started_at, finished_at, error, hint, detail)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, phase=excluded.phase, finished_at=excluded.finished_at,
		error=excluded.error, hint=excluded.hint, detail=excluded.detail`,
		op.ID, op.ServerID, op.Kind, op.Status, op.Phase, op.Actor, op.StartedAt.UnixMilli(), finished, op.Error, op.Hint, string(detail))
	if err != nil {
		a.log.Error("operation write failed", "err", err)
	}
}

const operationColumns = `id, server_id, kind, status, phase, actor, started_at, finished_at, error, hint, detail`

func (a *Agent) loadOperation(id string) (*api.Operation, error) {
	return a.scanOperation(a.db.QueryRow(`SELECT `+operationColumns+` FROM operations WHERE id = ?`, id))
}

func (s *server) lastFinishedOperation() *api.Operation {
	op, _ := s.scanOperation(s.db.QueryRow(`SELECT `+operationColumns+`
		FROM operations WHERE server_id = ? AND status != 'running' ORDER BY started_at DESC LIMIT 1`, s.id))
	return op
}

func (a *Agent) scanOperation(row *sql.Row) (*api.Operation, error) {
	var op api.Operation
	var started int64
	var finished sql.NullInt64
	var detail string
	if err := row.Scan(&op.ID, &op.ServerID, &op.Kind, &op.Status, &op.Phase, &op.Actor, &started, &finished, &op.Error, &op.Hint, &detail); err != nil {
		return nil, err
	}
	op.StartedAt = time.UnixMilli(started).UTC()
	if finished.Valid {
		t := time.UnixMilli(finished.Int64).UTC()
		op.FinishedAt = &t
	}
	_ = json.Unmarshal([]byte(detail), &op.Detail)
	return &op, nil
}

// markInterruptedOperations fails operations left "running" by a previous
// agent process (crash or reboot mid-operation) so the UI never shows a
// phantom in-progress task. An update the updater is still installing keeps
// running; its result is recorded when the updater reports. So do the
// restores in resumed, which this agent finishes.
func (a *Agent) markInterruptedOperations(resumed ...string) {
	a.upd.mu.Lock()
	keep := ""
	if a.upd.installing != "" {
		keep = a.upd.opID
	}
	a.upd.mu.Unlock()
	args := []any{a.now().UnixMilli(), keep}
	for _, id := range resumed {
		args = append(args, id)
	}
	_, err := a.db.Exec(`UPDATE operations SET status = 'failed', finished_at = ?, error = 'Interrupted: the Playkeeper agent restarted while this was running.'
		WHERE status = 'running' AND id NOT IN (?`+strings.Repeat(", ?", len(resumed))+`)`, args...)
	if err != nil {
		a.log.Error("mark interrupted operations", "err", err)
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
