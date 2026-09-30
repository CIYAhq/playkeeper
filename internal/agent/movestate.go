package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// moveState are the rows of its own a server's move carries beside its
// folder (hMoveOut) and what the dashboard gives its move-in, by table, and
// the columns of each: its sleep and public page settings, its own address
// and its pack page's link, its map's add-ons and link, its schedules, its
// copies somewhere else with their secrets and keys, what its template has
// yet to install, and the add-ons Playkeeper installed, whose files are in
// its folder. Its AI keys stay out, as they never leave the machine. The
// dashboard reads them where the server was and writes them where it goes
// before its requests go there. The servers row is the server's own; each
// other table's rows have its id as server_id.
var moveState = []struct {
	table string
	cols  []string
}{
	{"servers", []string{"sleep", "public_page", "public_page_players", "public_about", "public_stream", "public_board", "own_address", "packs_public", "packs_token"}},
	{"maps", []string{"addons", "installed_at", "public", "public_players", "first_render_at", "restart_when_empty", "share_token"}},
	{"schedules", []string{"id", "name", "kind", "timing", "payload", "enabled", "created_at", "updated_at", "created_by", "updated_by", "last_run"}},
	{"offsite", []string{"enabled", "config", "secret", "password", "private_key", "ssh_public", "keys", "key_saved_at", "key_saved_folder", "updated_at", "copies_made"}},
	{"offsite_copies", []string{"backup_id", "kind", "backup_created_at", "file_name", "size_bytes", "minecraft_version", "level_name", "copy", "copied_at", "removed_by"}},
	{"template_installs", []string{"planned", "remaining", "created_at", "packs", "skipped"}},
	{"addons", []string{"source", "project_id", "slug", "name", "summary", "icon_url", "version_id", "version_number", "channel", "published", "file_name", "hash_algo", "hash", "size_bytes", "dependency_of", "requires", "installed_at"}},
}

// hMoveStateGet gives the server's rows a move carries (api.MoveState).
func (s *server) hMoveStateGet(w http.ResponseWriter, r *http.Request) {
	state := api.MoveState{Rows: map[string][]map[string]any{}}
	for _, t := range moveState {
		key := "server_id"
		if t.table == "servers" {
			key = "id"
		}
		rows, err := s.db.QueryContext(r.Context(), `SELECT `+strings.Join(t.cols, ", ")+` FROM `+t.table+` WHERE `+key+` = ?`, s.id)
		if err != nil {
			writeError(w, err)
			return
		}
		for rows.Next() {
			vals := make([]any, len(t.cols))
			ptrs := make([]any, len(t.cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				writeError(w, err)
				return
			}
			row := make(map[string]any, len(t.cols))
			for i, c := range t.cols {
				if b, ok := vals[i].([]byte); ok {
					vals[i] = string(b)
				}
				row[c] = vals[i]
			}
			state.Rows[t.table] = append(state.Rows[t.table], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, state)
}

// hMoveStatePut gives a server moved here the rows it had where it was
// (api.MoveStateRequest), in place of its own: only the tables and columns
// moveState names, and an own address only if it fits this machine's.
func (s *server) hMoveStatePut(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	dec.UseNumber()
	var req api.MoveStateRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, errInvalid("Invalid request body."))
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	var res api.MoveStateResult
	if own := req.State.Rows["servers"]; len(own) == 1 {
		if name, _ := own[0]["own_address"].(string); name != "" {
			if fit, err := s.validOwnAddress(name); err != nil || fit != name {
				own[0]["own_address"] = ""
				res.Left = append(res.Left, "ownAddress")
			}
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	for _, t := range moveState {
		if err := s.putMoveRows(tx, t.table, t.cols, req.State.Rows[t.table]); err != nil {
			writeError(w, errInvalid("The %s the server had can't be kept: %v.", strings.ReplaceAll(t.table, "_", " "), err))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, err)
		return
	}
	s.reloadSchedules()
	s.serversChanged()
	s.audit(actor, "server.moved_in", s.id, "settings", fmt.Sprintf("kept what it had where it was%s", leftDetail(res.Left)))
	writeJSON(w, http.StatusOK, res)
}

func leftDetail(left []string) string {
	if len(left) == 0 {
		return ""
	}
	return "; left out: " + strings.Join(left, ", ")
}

// putMoveRows writes rows, of table's columns cols, as the server's own:
// its servers row takes their values, and each other table's rows replace
// the server's there.
func (s *server) putMoveRows(tx *sql.Tx, table string, cols []string, rows []map[string]any) error {
	if table == "servers" {
		if len(rows) != 1 {
			return fmt.Errorf("one row, not %d", len(rows))
		}
		set := make([]string, 0, len(cols))
		args := make([]any, 0, len(cols)+1)
		for _, c := range cols {
			v, ok := rows[0][c]
			if !ok {
				continue
			}
			sv, err := sqlValue(v)
			if err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
			set = append(set, c+" = ?")
			args = append(args, sv)
		}
		if len(set) == 0 {
			return nil
		}
		_, err := tx.Exec(`UPDATE servers SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, s.id)...)
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+table+` WHERE server_id = ?`, s.id); err != nil {
		return err
	}
	for _, row := range rows {
		names := []string{"server_id"}
		args := []any{s.id}
		for _, c := range cols {
			v, ok := row[c]
			if !ok {
				continue
			}
			sv, err := sqlValue(v)
			if err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
			names = append(names, c)
			args = append(args, sv)
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")
		if _, err := tx.Exec(`INSERT INTO `+table+`(`+strings.Join(names, ", ")+`) VALUES(`+marks+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// sqlValue is a value of a MoveState row, as JSON gave it, for SQLite: a
// string, a whole number, a number, or NULL.
func sqlValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, nil
		}
		return x.Float64()
	}
	return nil, fmt.Errorf("a value of type %T", v)
}
