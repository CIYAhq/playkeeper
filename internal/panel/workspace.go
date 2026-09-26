package panel

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
)

// Roles. The owner of the install may do everything; project roles decide
// what other accounts may do once there are co-admins (decision 0004).
const (
	roleOwner   = "owner"
	roleMember  = "member"
	projectRole = "admin"
	localKind   = "local"
)

// Actions a role is checked for. Every route that changes something names
// one; permit is the only place that decides.
type action string

const (
	actView           action = "view"
	actManageServers  action = "servers.manage"
	actManageMachine  action = "machine.manage"
	actManageAccount  action = "account.manage"
	actViewAuditTrail action = "audit.view"
)

// permit reports whether the signed-in account may take an action. Only
// owners exist in 0.3.0; members get what their project role allows once
// co-admins arrive.
func permit(sess *session, a action) bool {
	if sess == nil {
		return false
	}
	switch sess.User.Role {
	case roleOwner:
		return true
	case roleMember:
		return a == actView || a == actManageAccount
	}
	return false
}

// machine is a computer running a Playkeeper agent, and how to reach it.
type machine struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	agent     *agentclient.Client
}

var reMachineID = regexp.MustCompile(`^[a-z2-9]{10}$`)

func randomID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// ensureWorkspace makes sure the default project and this host's machine
// exist, and that every account belongs to the project.
func (s *Server) ensureWorkspace() error {
	var project string
	err := s.db.QueryRow(`SELECT id FROM projects ORDER BY created_at LIMIT 1`).Scan(&project)
	if isNoRows(err) {
		project = randomID()
		if _, err := s.db.Exec(`INSERT INTO projects(id, name, created_at) VALUES(?, 'My servers', ?)`, project, s.now().UnixMilli()); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM machines WHERE kind = ?`, localKind).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.db.Exec(`INSERT INTO machines(id, project_id, kind, created_at) VALUES(?, ?, ?, ?)`, randomID(), project, localKind, s.now().UnixMilli()); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO project_members(project_id, user_id, role, created_at) SELECT ?, id, ?, ? FROM users`, project, projectRole, s.now().UnixMilli())
	return err
}

// ensureMember adds an account to the default project.
func (s *Server) ensureMember(userID int64) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO project_members(project_id, user_id, role, created_at)
		SELECT id, ?, ?, ? FROM projects ORDER BY created_at LIMIT 1`, userID, projectRole, s.now().UnixMilli()); err != nil {
		s.log.Error("add project member", "err", err)
	}
}

// machines lists the machines the panel manages. The local machine is
// reached over the agent socket; remote machines come later.
func (s *Server) machines() ([]machine, error) {
	rows, err := s.db.Query(`SELECT id, project_id, name, kind FROM machines ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []machine
	for rows.Next() {
		var m machine
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Kind); err != nil {
			return nil, err
		}
		if m.Kind == localKind {
			m.agent = s.agent
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *Server) machineByID(id string) (machine, error) {
	list, err := s.machines()
	if err != nil {
		return machine{}, err
	}
	for _, m := range list {
		if m.ID == id {
			return m, nil
		}
	}
	return machine{}, errNotFound
}

var errNotFound = errors.New("not found")

// machineForServer finds the machine that runs a server. With one machine
// that is always the local one; with more, the panel asks each.
func (s *Server) machineForServer(r *http.Request, serverID string) (machine, error) {
	list, err := s.machines()
	if err != nil || len(list) == 0 {
		return machine{}, errNotFound
	}
	if len(list) == 1 {
		return list[0], nil
	}
	for _, m := range list {
		var st api.ServerStatus
		if _, err := m.agent.Do(r.Context(), "GET", "/v1/servers/"+serverID, nil, nil, &st); err == nil {
			return m, nil
		}
	}
	return machine{}, errNotFound
}

func (s *Server) hProjects(w http.ResponseWriter, r *http.Request, sess *session) {
	rows, err := s.db.Query(`SELECT p.id, p.name, m.role FROM projects p JOIN project_members m ON m.project_id = p.id WHERE m.user_id = ? ORDER BY p.created_at`, sess.User.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	type project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	out := []project{}
	for rows.Next() {
		var p project
		if rows.Scan(&p.ID, &p.Name, &p.Role) == nil {
			out = append(out, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// machineView is a machine with what its agent reports now, or why it can't.
type machineView struct {
	machine
	Live  *api.Machine `json:"live,omitempty"`
	Error *api.Error   `json:"error,omitempty"`
}

func (s *Server) machineView(r *http.Request, m machine) machineView {
	v := machineView{machine: m}
	var live api.Machine
	if _, err := m.agent.Do(r.Context(), "GET", "/v1/machine", nil, nil, &live); err != nil {
		v.Error = agentErrorBody(err)
	} else {
		v.Live = &live
		if v.Name == "" {
			v.Name = live.Hostname
		}
	}
	return v
}

// localMachineName is the name machineView gives this machine, for the
// sign-in page. It reads the hostname here, as the self-signed certificate
// does, so a page anyone can load costs the agent nothing.
func (s *Server) localMachineName() string {
	var name string
	if err := s.db.QueryRow(`SELECT name FROM machines WHERE kind = ? ORDER BY created_at LIMIT 1`, localKind).Scan(&name); err == nil && name != "" {
		return name
	}
	host, _ := os.Hostname()
	return host
}

func (s *Server) hMachines(w http.ResponseWriter, r *http.Request, sess *session) {
	list, err := s.machines()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	out := []machineView{}
	for _, m := range list {
		out = append(out, s.machineView(r, m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hMachine(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.machineView(r, m))
}

func (s *Server) machineFromPath(w http.ResponseWriter, r *http.Request) (machine, bool) {
	id := r.PathValue("mid")
	if !reMachineID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid machine id.", "")
		return machine{}, false
	}
	m, err := s.machineByID(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Machine not found.", "")
		return machine{}, false
	}
	return m, true
}

// hServers lists every server on every machine, each with its machine's id.
func (s *Server) hServers(w http.ResponseWriter, r *http.Request, sess *session) {
	list, err := s.machines()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	out := []map[string]any{}
	for _, m := range list {
		var servers []map[string]any
		if _, err := m.agent.Do(r.Context(), "GET", "/v1/servers", nil, nil, &servers); err != nil {
			if len(list) == 1 {
				s.agentFailure(w, err)
				return
			}
			continue
		}
		for _, sv := range servers {
			sv["machineId"] = m.ID
			out = append(out, sv)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Preferences are small per-account settings, such as a dismissed checklist.

var rePrefKey = regexp.MustCompile(`^[a-z][a-z0-9.:_-]{0,63}$`)

const maxPrefs = 200

func (s *Server) hPrefs(w http.ResponseWriter, r *http.Request, sess *session) {
	rows, err := s.db.Query(`SELECT key, value FROM user_prefs WHERE user_id = ? ORDER BY key`, sess.User.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// hPrefsSet stores the given keys; an empty value removes a key.
func (s *Server) hPrefsSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req map[string]string
	if err := decodeJSON(r, &req); err != nil || len(req) == 0 || len(req) > 20 {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Send between 1 and 20 preferences as a JSON object of strings.", "")
		return
	}
	keys := make([]string, 0, len(req))
	for k, v := range req {
		if !rePrefKey.MatchString(k) || len(v) > 512 {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid preference "+k+".", "")
			return
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM user_prefs WHERE user_id = ?`, sess.User.ID).Scan(&n)
	for _, k := range keys {
		var err error
		if req[k] == "" {
			_, err = s.db.Exec(`DELETE FROM user_prefs WHERE user_id = ? AND key = ?`, sess.User.ID, k)
		} else {
			if n >= maxPrefs {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Too many preferences.", "")
				return
			}
			_, err = s.db.Exec(`INSERT INTO user_prefs(user_id, key, value) VALUES(?,?,?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, sess.User.ID, k, req[k])
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return
		}
	}
	s.hPrefs(w, r, sess)
}

func agentErrorBody(err error) *api.Error {
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		b := ae.Body
		return &b
	}
	return &api.Error{Error: "The Playkeeper agent is not running, so the server cannot be seen or controlled right now.", Code: api.CodeAgentUnavailable, Hint: "On the server, check: sudo systemctl status playkeeper-agent"}
}

// hLegacyStatus answers GET /api/server in the single-server shape of 0.1.0
// and 0.2.0, for a dashboard still open from before an update (it polls this
// to notice the new version and reload) and for scripts. It describes the
// first server, with the machine's version and update state.
func (s *Server) hLegacyStatus(w http.ResponseWriter, r *http.Request, sess *session) {
	var m api.Machine
	if _, err := s.agent.Do(r.Context(), "GET", "/v1/machine", nil, nil, &m); err != nil {
		s.agentFailure(w, err)
		return
	}
	var servers []map[string]any
	if _, err := s.agent.Do(r.Context(), "GET", "/v1/servers", nil, nil, &servers); err != nil {
		s.agentFailure(w, err)
		return
	}
	out := map[string]any{"exists": false, "desired": api.DesiredStopped, "phase": api.PhaseNotCreated, "reachable": false,
		"gamePort": m.DefaultGamePort, "offlineModeTest": m.OfflineModeTest, "crashCount": 0, "pendingRestart": false}
	if len(servers) > 0 {
		out = servers[0]
	}
	out["agentVersion"] = m.AgentVersion
	if m.UpdateAvailable != "" {
		out["updateAvailable"] = m.UpdateAvailable
	}
	if m.UpdateInstalling != "" {
		out["updateInstalling"] = m.UpdateInstalling
	}
	if m.DiskWarning != nil {
		out["diskWarning"] = m.DiskWarning
	}
	b, _ := json.Marshal(out)
	writeJSON(w, http.StatusOK, json.RawMessage(b))
}
