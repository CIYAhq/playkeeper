package agent

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

const (
	kvServerConfig    = "server_config"
	kvDesired         = "desired_state"
	kvCollectingSince = "collecting_since"
	kvLogCursor       = "log_cursor"
	kvPendingRecreate = "pending_recreate"
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

func (a *Agent) kvDelete(key string) error {
	_, err := a.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
	return err
}

func (a *Agent) serverConfig() (*api.ServerConfig, error) {
	v, ok, err := a.kvGet(kvServerConfig)
	if err != nil || !ok {
		return nil, err
	}
	var sc api.ServerConfig
	if err := json.Unmarshal([]byte(v), &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

func (a *Agent) saveServerConfig(sc api.ServerConfig) error {
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return a.kvSet(kvServerConfig, string(b))
}

func (a *Agent) desired() string {
	v, ok, _ := a.kvGet(kvDesired)
	if !ok {
		return api.DesiredStopped
	}
	return v
}

func (a *Agent) setDesired(d string) error { return a.kvSet(kvDesired, d) }

func (a *Agent) collectingSince() *time.Time {
	v, ok, _ := a.kvGet(kvCollectingSince)
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil
	}
	return &t
}

func (a *Agent) audit(actor, action, target, result, detail string) {
	if actor == "" {
		actor = "unknown"
	}
	if _, err := a.db.Exec(`INSERT INTO audit(ts, actor, action, target, result, detail) VALUES(?,?,?,?,?,?)`,
		a.now().UnixMilli(), actor, action, target, result, detail); err != nil {
		a.log.Error("audit write failed", "err", err)
	}
}

func (a *Agent) listAudit(limit int) ([]api.AuditEntry, error) {
	rows, err := a.db.Query(`SELECT id, ts, actor, action, target, result, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AuditEntry{}
	for rows.Next() {
		var e api.AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Result, &e.Detail); err != nil {
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
	_, err := a.db.Exec(`INSERT INTO operations(id, kind, status, phase, actor, started_at, finished_at, error, hint, detail)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, phase=excluded.phase, finished_at=excluded.finished_at,
		error=excluded.error, hint=excluded.hint, detail=excluded.detail`,
		op.ID, op.Kind, op.Status, op.Phase, op.Actor, op.StartedAt.UnixMilli(), finished, op.Error, op.Hint, string(detail))
	if err != nil {
		a.log.Error("operation write failed", "err", err)
	}
}

func (a *Agent) loadOperation(id string) (*api.Operation, error) {
	return a.scanOperation(a.db.QueryRow(`SELECT id, kind, status, phase, actor, started_at, finished_at, error, hint, detail FROM operations WHERE id = ?`, id))
}

func (a *Agent) lastFinishedOperation() *api.Operation {
	op, _ := a.scanOperation(a.db.QueryRow(`SELECT id, kind, status, phase, actor, started_at, finished_at, error, hint, detail
		FROM operations WHERE status != 'running' ORDER BY started_at DESC LIMIT 1`))
	return op
}

func (a *Agent) scanOperation(row *sql.Row) (*api.Operation, error) {
	var op api.Operation
	var started int64
	var finished sql.NullInt64
	var detail string
	if err := row.Scan(&op.ID, &op.Kind, &op.Status, &op.Phase, &op.Actor, &started, &finished, &op.Error, &op.Hint, &detail); err != nil {
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
// phantom in-progress task.
func (a *Agent) markInterruptedOperations() {
	_, err := a.db.Exec(`UPDATE operations SET status = 'failed', finished_at = ?, error = 'Interrupted: the Playkeeper agent restarted while this was running.'
		WHERE status = 'running'`, a.now().UnixMilli())
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
