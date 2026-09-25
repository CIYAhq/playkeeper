package panel

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Roles. The owner of the install may do everything; other accounts are
// members, and their project role and servers decide what they may do
// (decision 0004 and the Team page).
const (
	roleOwner  = invites.InstallOwner
	roleMember = invites.InstallMember
	localKind  = "local"

	defaultProjectName = "My servers"
)

// Actions a role is checked for. Every route names one; permit is the only
// place that decides.
type action string

const (
	actView           action = "view"
	actManageAccount  action = "account.manage"
	actRunServers     action = "servers.run"
	actConsole        action = "servers.console"
	actManagePlayers  action = "players.manage"
	actMakeBackups    action = "backups.make"
	actRestore        action = "backups.restore"
	actManageServers  action = "servers.manage"
	actCreateServers  action = "servers.create"
	actManageTeam     action = "team.manage"
	actManageMachine  action = "machine.manage"
	actViewAuditTrail action = "audit.view"
)

// actions lists every action, for the signed-in account's "can" list.
var actions = []action{actView, actManageAccount, actRunServers, actConsole, actManagePlayers, actMakeBackups,
	actRestore, actManageServers, actCreateServers, actManageTeam, actManageMachine, actViewAuditTrail}

// actNeeds is the least project role an action needs, as the Team page's
// table says: viewers look, moderators run the servers day to day, admins
// do everything else. An action missing here is refused.
var actNeeds = map[action]string{
	actView:           invites.RoleViewer,
	actRunServers:     invites.RoleModerator,
	actConsole:        invites.RoleModerator,
	actManagePlayers:  invites.RoleModerator,
	actMakeBackups:    invites.RoleModerator,
	actRestore:        invites.RoleAdmin,
	actManageServers:  invites.RoleAdmin,
	actCreateServers:  invites.RoleAdmin,
	actManageTeam:     invites.RoleAdmin,
	actManageMachine:  invites.RoleAdmin,
	actViewAuditTrail: invites.RoleAdmin,
}

// machineWide actions reach past single servers, so an admin needs all of
// them.
var machineWide = map[action]bool{actCreateServers: true, actManageMachine: true, actViewAuditTrail: true}

// access is the signed-in account as permit sees it. It is read for every
// request, so a changed role, scope or two-factor setting counts at once.
// Account.TwoFactor is what counts for rights. For an admin that is
// two-factor sign-in the owner or an admin confirmed their Admin rights
// with, so someone who learns an admin's password can't turn it on and
// become one.
type access struct {
	invites.Account
	ProjectID string
	// FactorOn says whether two-factor sign-in is on at all.
	FactorOn bool
}

func (a access) owner() bool { return a.InstallRole == roleOwner }

// awaitingConfirmation reports whether a is an admin who turned two-factor
// sign-in on and waits for the owner or an admin to confirm their Admin
// rights.
func (a access) awaitingConfirmation() bool {
	return invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole) && a.FactorOn && !a.TwoFactor
}

// covers reports whether a may use the server at all.
func (a access) covers(serverID string) bool { return a.owner() || a.Servers.Covers(serverID) }

var (
	errForbidden  = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden, Msg: "Your account is not allowed to do this."}
	errNoServer   = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden, Msg: "You don't have access to this server.", Hint: "Ask an admin to add it to your servers."}
	errAllServers = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden, Msg: "Only admins of all servers can do this.", Hint: "Ask the owner of this Playkeeper."}
	// errAdminUnconfirmed is the refusal for an admin whose two-factor
	// sign-in is on but whose Admin rights nobody has confirmed yet.
	errAdminUnconfirmed = &invites.Error{Code: api.CodeAdminUnconfirmed, Status: http.StatusForbidden,
		Msg: "The owner or an admin needs to confirm your Admin rights.", Hint: "Until then, you have Moderator rights."}
)

// permit decides whether a may take act, on the server serverID when the
// route names one ("" otherwise). Admin actions wait for two-factor sign-in
// (see invites.RequiresTwoFactor); until then an admin can do what a
// moderator can.
func permit(a access, act action, serverID string) error {
	switch {
	case a.UserID == 0:
		return errForbidden
	case serverID != "" && !a.covers(serverID):
		return errNoServer
	case a.owner():
		return nil
	case a.InstallRole != roleMember:
		return errForbidden
	case act == actManageAccount:
		return nil
	}
	need := actNeeds[act]
	switch {
	case !invites.AtLeast(a.ProjectRole, need):
		return errForbidden
	case need != invites.RoleAdmin:
		return nil
	case a.awaitingConfirmation():
		return errAdminUnconfirmed
	case invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole) && !a.TwoFactor:
		return invites.TwoFactorRequired()
	case machineWide[act] && !a.Servers.All:
		return errAllServers
	}
	return nil
}

// access reads what u may do: their project role and servers, and whether
// two-factor sign-in is on. A servers column that can't be read means no
// servers.
func (s *Server) access(u user) (access, error) {
	a := access{Account: invites.Account{UserID: u.ID, Name: u.Username, InstallRole: u.Role}}
	var servers string
	var adminFactor, seen int64
	err := s.db.QueryRow(`SELECT project_id, role, servers, admin_factor, factor_seen FROM project_members WHERE user_id = ? ORDER BY created_at LIMIT 1`, u.ID).
		Scan(&a.ProjectID, &a.ProjectRole, &servers, &adminFactor, &seen)
	if err != nil && !isNoRows(err) {
		return access{}, err
	}
	if sc, err := invites.ParseScope(servers); err == nil {
		a.Servers = sc
	}
	if a.owner() {
		a.ProjectRole, a.Servers = invites.RoleAdmin, invites.AllServers()
	}
	factor := s.factorAt(u.ID)
	a.FactorOn = factor > 0
	a.TwoFactor = a.FactorOn && (!invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole) || factor == adminFactor)
	if a.ProjectID != "" && !a.owner() && factor != seen {
		s.factorChanged(a, seen, factor)
	}
	return a, nil
}

// factorAt is when u's two-factor sign-in was turned on, in Unix
// milliseconds, or 0 while it's off. It reads wave 2's user_factors as
// Factor.On does: a setup that was only started doesn't count. Until that
// table exists, nobody has it on.
func (s *Server) factorAt(userID int64) int64 {
	var at int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(confirmed_at), 0) FROM user_factors WHERE user_id = ? AND confirmed_at IS NOT NULL AND confirmed_at > 0`, userID).Scan(&at); err != nil {
		return 0
	}
	return at
}

// factorChanged records, once, that a team member turned two-factor sign-in
// on or off since the dashboard last looked: in the audit trail, and on
// Discord when it's connected.
func (s *Server) factorChanged(a access, seen, factor int64) {
	res, err := s.db.Exec(`UPDATE project_members SET factor_seen = ? WHERE user_id = ? AND project_id = ? AND factor_seen = ?`, factor, a.UserID, a.ProjectID, seen)
	if err != nil {
		s.log.Warn("could not note a two-factor change", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	detail := "turned on"
	if factor == 0 {
		detail = "turned off"
	}
	if a.awaitingConfirmation() {
		detail += "; Admin rights wait for the owner or an admin to confirm them"
	}
	s.audit(a.Name, "team.two_factor", a.Name, "succeeded", detail)
	s.notifyTeam(api.DiscordNotifyRequest{Kind: api.DiscordTwoFactorChanged, Member: a.Name, On: factor != 0,
		Admin: invites.RequiresTwoFactor(a.InstallRole, a.ProjectRole), Actor: a.Name})
}

// can lists the actions a may take on the servers they can use, for the
// UI to show only what works.
func (a access) can() []action {
	out := []action{}
	for _, act := range actions {
		if permit(a, act, "") == nil {
			out = append(out, act)
		}
	}
	return out
}

// refuse answers a refusal: an *invites.Error with its status, code, params
// and wait, or a plain server error.
func refuse(w http.ResponseWriter, err error) {
	var e *invites.Error
	if !errors.As(err, &e) {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Something went wrong. Try again.", "")
		return
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(e.RetryAfter.Seconds()))))
	}
	writeJSON(w, e.Status, api.Error{Error: e.Msg, Code: e.Code, Hint: e.Hint, Params: e.Params})
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
// exist, and that the owner belongs to the project. Only the owner: a
// member without a membership was removed, and must stay out.
func (s *Server) ensureWorkspace() error {
	var project string
	err := s.db.QueryRow(`SELECT id FROM projects ORDER BY created_at LIMIT 1`).Scan(&project)
	if isNoRows(err) {
		project = randomID()
		if _, err := s.db.Exec(`INSERT INTO projects(id, name, created_at) VALUES(?, ?, ?)`, project, defaultProjectName, s.now().UnixMilli()); err != nil {
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
	_, err = s.db.Exec(`INSERT OR IGNORE INTO project_members(project_id, user_id, role, servers, created_at) SELECT ?, id, ?, '*', ? FROM users WHERE role = ?`,
		project, invites.RoleAdmin, s.now().UnixMilli(), roleOwner)
	return err
}

// ensureOwnerMember adds the owner's account to the default project.
func (s *Server) ensureOwnerMember(userID int64) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO project_members(project_id, user_id, role, servers, created_at)
		SELECT p.id, u.id, ?, '*', ? FROM projects p, users u WHERE u.id = ? AND u.role = ? ORDER BY p.created_at LIMIT 1`,
		invites.RoleAdmin, s.now().UnixMilli(), userID, roleOwner); err != nil {
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
	rows, err := s.db.Query(`SELECT p.id, p.name, m.role, m.servers FROM projects p JOIN project_members m ON m.project_id = p.id WHERE m.user_id = ? ORDER BY p.created_at`, sess.User.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	type project struct {
		ID      string        `json:"id"`
		Name    string        `json:"name"`
		Role    string        `json:"role"`
		Servers invites.Scope `json:"servers"`
	}
	out := []project{}
	for rows.Next() {
		var p project
		var servers string
		if rows.Scan(&p.ID, &p.Name, &p.Role, &servers) == nil {
			p.Servers, _ = invites.ParseScope(servers)
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

// hServers lists the servers the account can use on every machine, each
// with its machine's id.
func (s *Server) hServers(w http.ResponseWriter, r *http.Request, sess *session) {
	list, err := s.machines()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	out := []map[string]any{}
	everyMachine := true
	var ids []string
	for _, m := range list {
		var servers []map[string]any
		if _, err := m.agent.Do(r.Context(), "GET", "/v1/servers", nil, nil, &servers); err != nil {
			if len(list) == 1 {
				s.agentFailure(w, err)
				return
			}
			everyMachine = false
			continue
		}
		for _, sv := range servers {
			id, _ := sv["id"].(string)
			ids = append(ids, id)
			if !sess.Access.covers(id) {
				continue
			}
			sv["machineId"] = m.ID
			out = append(out, sv)
		}
	}
	if everyMachine && len(ids) > 0 {
		s.forgetDeletedServers(ids)
	}
	writeJSON(w, http.StatusOK, out)
}

// forgetDeletedServers drops the friend invites, join requests and origins
// of servers that no longer exist. ids must list every server on every
// machine.
func (s *Server) forgetDeletedServers(ids []string) {
	list, _ := json.Marshal(ids)
	for _, q := range []string{
		`DELETE FROM join_requests WHERE server_id NOT IN (SELECT value FROM json_each(?))`,
		`DELETE FROM invites WHERE kind = 'player' AND server_id NOT IN (SELECT value FROM json_each(?))`,
		`DELETE FROM player_origins WHERE server_id NOT IN (SELECT value FROM json_each(?))`,
	} {
		if _, err := s.db.Exec(q, string(list)); err != nil {
			s.log.Warn("could not forget a deleted server's invites", "err", err)
			return
		}
	}
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
	for _, sv := range servers {
		if id, _ := sv["id"].(string); sess.Access.covers(id) {
			out = sv
			break
		}
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
