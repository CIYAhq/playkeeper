package panel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
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

// Wave 7 (0.4.0): where copies of backups go, the key that opens them, and
// bringing a server back from its copies on a new machine. mayHoldBackupKeys
// alone decides who may use these; bringing one back makes a server, so it
// needs every server too.
const (
	actManageBackupCopies action = "backups.copies.manage"
	actRecoveryKey        action = "backups.recovery_key"
	actRecoverBackups     action = "backups.recover"
)

// actions lists every action, for the signed-in account's "can" list.
var actions = []action{actView, actManageAccount, actRunServers, actConsole, actManagePlayers, actMakeBackups,
	actRestore, actManageServers, actCreateServers, actManageTeam, actManageMachine, actViewAuditTrail,
	actManageBackupCopies, actRecoveryKey, actRecoverBackups}

// keyActions are decided by mayHoldBackupKeys rather than actNeeds.
var keyActions = map[action]bool{actManageBackupCopies: true, actRecoveryKey: true, actRecoverBackups: true}

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
	// errEveryServer refuses what shows every server to an account that may
	// use only some of them.
	errEveryServer = &invites.Error{Code: api.CodeForbidden, Status: http.StatusForbidden, Msg: "This shows every server, and your account can use only some of them.", Hint: "Ask an admin to add the other servers."}
	// errAdminUnconfirmed is the refusal for an admin whose two-factor
	// sign-in is on but whose Admin rights nobody has confirmed yet.
	errAdminUnconfirmed = &invites.Error{Code: api.CodeAdminUnconfirmed, Status: http.StatusForbidden,
		Msg: "The owner or an admin needs to confirm your Admin rights.", Hint: "Until then, you have Moderator rights."}
)

// mayHoldBackupKeys reports whether an account may change where backup
// copies go, and see or download the recovery key that opens them: the
// owner, or an admin with two-factor sign-in on. As for every Admin right,
// the owner or an admin must have confirmed it (see access). It is the only
// check for both.
func mayHoldBackupKeys(a access) bool {
	return a.owner() || (a.InstallRole == roleMember && a.ProjectRole == invites.RoleAdmin && a.FactorOn && a.TwoFactor)
}

// keysRefusal is permit for keyActions: why a may not take act, or nil.
func keysRefusal(a access, act action) error {
	if !mayHoldBackupKeys(a) {
		switch {
		case a.InstallRole != roleMember || a.ProjectRole != invites.RoleAdmin:
			return errForbidden
		case a.awaitingConfirmation():
			return errAdminUnconfirmed
		}
		return invites.TwoFactorRequired()
	}
	if act == actRecoverBackups && !a.owner() && !a.Servers.All {
		return errAllServers
	}
	return nil
}

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
	case keyActions[act]:
		return keysRefusal(a, act)
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

// everyServer serves h only to accounts that may use every server, for what
// shows them all.
func everyServer(h func(http.ResponseWriter, *http.Request, *session)) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !sess.Access.Servers.All {
			writeRefusal(w, errEveryServer)
			return
		}
		h(w, r, sess)
	}
}

// writeRefusal answers a refusal: an *invites.Error with its status, code,
// params and wait, or a plain server error.
func writeRefusal(w http.ResponseWriter, err error) {
	var e *invites.Error
	if !errors.As(err, &e) {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Something went wrong. Try again.", "")
		return
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(e.RetryAfter.Seconds()))))
	}
	body := api.Error{Error: e.Msg, Code: e.Code, Hint: e.Hint}
	if len(e.Params) > 0 {
		body.Params = make(map[string]any, len(e.Params))
		for k, v := range e.Params {
			body.Params[k] = v
		}
	}
	writeJSON(w, e.Status, body)
}

// machine is a computer running a Playkeeper agent, and how to reach it: the
// local one over the agent socket, joined ones over their machine links.
type machine struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	agent     *agentclient.Client
	// dials, joinedAt, joinedFrom and addedBy are about joined machines:
	// the address their command dialed, and when, from where and by whose
	// code they joined.
	dials      string
	joinedAt   time.Time
	joinedFrom string
	addedBy    string
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

// machines lists the machines the panel manages, the local one first and
// removed ones left out.
func (s *Server) machines() ([]machine, error) {
	rows, err := s.db.Query(`SELECT id, project_id, name, kind, endpoint, created_at, joined_from, created_by FROM machines
		WHERE revoked_at = 0 ORDER BY kind != ?, created_at, id`, localKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []machine
	for rows.Next() {
		var m machine
		var created int64
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Kind, &m.dials, &created, &m.joinedFrom, &m.addedBy); err != nil {
			return nil, err
		}
		switch m.Kind {
		case localKind:
			m.agent = s.agent
		case remoteKind:
			m.agent = agentclient.Via(s.machineTransport(m.ID))
			m.joinedAt = fromMillis(created)
		default:
			continue
		}
		out = append(out, m)
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

// errServerMachine is a lookup of the machine that runs a server that
// failed. The request goes to no machine: the dashboard's own may not be
// the one.
var errServerMachine = errors.New("could not look up the machine that runs the server")

// machineForServer finds the machine that runs a server in server_machines
// (see claimServers), or the dashboard's own machine for a server no
// machine has. A disputed server has none. It never asks the machines.
func (s *Server) machineForServer(serverID string) (machine, error) {
	list, err := s.machines()
	if err != nil {
		s.log.Error("look up the machine that runs a server", "server", serverID, "err", err)
		return machine{}, errServerMachine
	}
	if len(list) == 0 {
		return machine{}, errNotFound
	}
	var owner, disputedBy string
	err = s.db.QueryRow(`SELECT machine_id, disputed_by FROM server_machines WHERE server_id = ?`, serverID).Scan(&owner, &disputedBy)
	if err != nil && !isNoRows(err) {
		s.log.Error("look up the machine that runs a server", "server", serverID, "err", err)
		return machine{}, errServerMachine
	}
	if err == nil {
		for _, m := range list {
			if m.ID != owner {
				continue
			}
			if disputedBy != "" && m.Kind != localKind {
				return machine{}, errDisputed
			}
			return m, nil
		}
	}
	for _, m := range list {
		if m.Kind == localKind {
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
// A joined machine also has its link's status and how it joined.
type machineView struct {
	machine
	Live       *api.Machine        `json:"live,omitempty"`
	Error      *api.Error          `json:"error,omitempty"`
	Link       *machinelink.Status `json:"link,omitempty"`
	Dials      string              `json:"dials,omitempty"`
	JoinedAt   *time.Time          `json:"joinedAt,omitempty"`
	JoinedFrom string              `json:"joinedFrom,omitempty"`
	AddedBy    string              `json:"addedBy,omitempty"`
}

// machineTimeout bounds how long one machine may take to answer a list, so
// a slow link can't hold up the others.
const machineTimeout = 8 * time.Second

func (s *Server) machineView(ctx context.Context, m machine, link *machinelink.Status) machineView {
	v := machineView{machine: m, Link: link}
	if m.Kind == remoteKind {
		v.Dials, v.JoinedFrom, v.AddedBy = m.dials, m.joinedFrom, m.addedBy
		v.JoinedAt = &m.joinedAt
	}
	ctx, cancel := context.WithTimeout(ctx, machineTimeout)
	defer cancel()
	var live api.Machine
	if _, err := m.agent.Do(ctx, "GET", "/v1/machine", nil, nil, &live); err != nil {
		v.Error = agentErrorBody(err)
	} else {
		v.Live = &live
		if v.Name == "" {
			v.Name = live.Hostname
		}
	}
	return v
}

// linkStatuses are the joined machines' link statuses by machine id.
func (s *Server) linkStatuses(ctx context.Context) map[string]*machinelink.Status {
	out := map[string]*machinelink.Status{}
	if s.hub == nil {
		return out
	}
	list, err := s.hub.Status(ctx)
	if err != nil {
		s.log.Error("machine link status", "err", err)
		return out
	}
	for i := range list {
		out[list[i].MachineID] = &list[i]
	}
	return out
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
	links := s.linkStatuses(r.Context())
	out := make([]machineView, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Go(func() { out[i] = s.machineView(r.Context(), m, links[m.ID]) })
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hMachine(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.machineView(r.Context(), m, s.linkStatuses(r.Context())[m.ID]))
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
	all, _, err := s.allServers(r.Context())
	switch {
	case errors.Is(err, errDB):
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	case err != nil:
		s.agentFailure(w, err)
		return
	}
	out := []map[string]any{}
	for _, sv := range all {
		id, _ := sv["id"].(string)
		if !sess.Access.covers(id) {
			continue
		}
		out = append(out, sv)
	}
	writeJSON(w, http.StatusOK, out)
}

// allServers lists every server on every machine, each with its machine's
// id, and the machines. A machine that can't be reached shows its servers
// as it last listed them, with lastKnownAt. When the dashboard's own
// machine is the only one, its error is the list's. When every machine
// answered, servers that no longer exist lose their invites.
func (s *Server) allServers(ctx context.Context) ([]map[string]any, []machine, error) {
	list, err := s.machines()
	if err != nil {
		return nil, nil, errDB
	}
	type listing struct {
		servers []map[string]any
		err     error
	}
	got := make([]listing, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, machineTimeout)
			defer cancel()
			_, got[i].err = m.agent.Do(ctx, "GET", "/v1/servers", nil, nil, &got[i].servers)
		})
	}
	wg.Wait()
	if len(list) == 1 && got[0].err != nil {
		return nil, nil, got[0].err
	}
	for i, m := range list {
		if m.Kind == localKind && got[i].err == nil {
			s.claimLocal(m, got[i].servers)
		}
	}
	out := []map[string]any{}
	everyMachine := true
	var ids []string
	for i, m := range list {
		servers := got[i].servers
		for _, sv := range servers {
			id, _ := sv["id"].(string)
			ids = append(ids, id)
		}
		switch {
		case got[i].err != nil:
			everyMachine = false
			servers = s.lastKnownServers(m)
		case m.Kind == localKind:
		default:
			servers = s.claimServers(m, servers)
		}
		for _, sv := range servers {
			sv["machineId"] = m.ID
			out = append(out, sv)
		}
	}
	if everyMachine && len(ids) > 0 {
		s.forgetDeletedServers(ids)
	}
	return out, list, nil
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
	_, body := failureOf(err)
	return &body
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
