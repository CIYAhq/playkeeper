package panel

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// The Team page (Settings › Team): who can use the dashboard, with which
// role and servers, and team invite links that haven't been used yet. The
// rules for who may change whom live in internal/invites (CanGrant, CanEdit,
// CanRemove); these handlers store what they allow.

type teamMember struct {
	ID        int64         `json:"id"`
	Username  string        `json:"username"`
	Owner     bool          `json:"owner"`
	You       bool          `json:"you"`
	Role      string        `json:"role"`
	Servers   invites.Scope `json:"servers"`
	TwoFactor bool          `json:"twoFactor"`
	AddedAt   time.Time     `json:"addedAt"`
	// CanEdit says whether the signed-in account may change this member's
	// role and servers, or remove them.
	CanEdit bool `json:"canEdit"`
	// Waiting is set for an admin who turned on two-factor sign-in and
	// waits for their Admin rights to be confirmed; CanConfirm says whether
	// the signed-in account may confirm them.
	Waiting    bool `json:"waiting,omitempty"`
	CanConfirm bool `json:"canConfirm,omitempty"`
}

// memberRow is t as the Team page shows it to a.
func memberRow(a, t access, added time.Time) teamMember {
	return teamMember{ID: t.UserID, Username: t.Name, Owner: t.owner(), You: t.UserID == a.UserID,
		Role: t.ProjectRole, Servers: t.Servers, TwoFactor: t.FactorOn, AddedAt: added,
		CanEdit: invites.CanRemove(a.Account, t.Account) == nil, Waiting: t.awaitingConfirmation(),
		CanConfirm: t.awaitingConfirmation() && canConfirm(a, t) == nil}
}

type teamInvite struct {
	invites.Summary
	CanEdit bool `json:"canEdit"`
}

type teamBody struct {
	ProjectID string       `json:"projectId"`
	Project   string       `json:"project"`
	Members   []teamMember `json:"members"`
	Invites   []teamInvite `json:"invites"`
	// GrantableRoles are the roles the signed-in account may give, most
	// trusted first.
	GrantableRoles []string `json:"grantableRoles"`
	// Servers are the servers the signed-in account can use, with their
	// names, for the servers choice and to name members' servers.
	Servers []serverRef `json:"servers"`
}

func (s *Server) hTeam(w http.ResponseWriter, r *http.Request, sess *session) {
	a := sess.Access
	project := s.projectID(a)
	out := teamBody{ProjectID: project, Members: []teamMember{}, Invites: []teamInvite{}, GrantableRoles: invites.GrantableRoles(a.Account), Servers: []serverRef{}}
	_ = s.db.QueryRow(`SELECT name FROM projects WHERE id = ?`, project).Scan(&out.Project)
	if out.GrantableRoles == nil {
		out.GrantableRoles = []string{}
	}
	rows, err := s.db.Query(`SELECT u.id, u.username, u.role, m.created_at FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ? ORDER BY u.role = 'owner' DESC, m.created_at, u.id`, project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	type row struct {
		u     user
		added int64
	}
	var list []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.u.ID, &x.u.Username, &x.u.Role, &x.added) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		t, err := s.access(x.u)
		if err != nil {
			continue
		}
		out.Members = append(out.Members, memberRow(a, t, invites.FromMillis(x.added)))
	}
	now := s.now()
	irows, err := s.db.Query(`SELECT `+inviteColumns+` FROM invites WHERE kind = 'member' AND project_id = ? AND revoked_at = 0 AND uses = 0
		AND expires_at > ? ORDER BY created_at DESC LIMIT 100`, project, now.UnixMilli())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	for irows.Next() {
		inv, err := scanInvite(irows)
		if err != nil {
			continue
		}
		out.Invites = append(out.Invites, teamInvite{Summary: inv.Summarize(now), CanEdit: invites.CanGrant(a.Account, inv.Role, inv.Servers) == nil})
	}
	irows.Close()
	if servers, err := s.listServers(r.Context()); err == nil {
		for _, sv := range servers {
			if a.covers(sv.ID) {
				out.Servers = append(out.Servers, sv)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// grantBody is a role and servers chosen on the Team page.
type grantBody struct {
	Role    string        `json:"role"`
	Servers invites.Scope `json:"servers"`
	Label   string        `json:"label,omitempty"`
}

// existingServers lists the servers that exist, for choices that must name
// only those.
func (s *Server) existingServers(w http.ResponseWriter, r *http.Request) ([]serverRef, bool) {
	servers, err := s.listServers(r.Context())
	if err != nil {
		s.agentFailure(w, err)
		return nil, false
	}
	return servers, true
}

// hTeamInviteCreate makes a team invite link. The link is shown once: only
// its code's hash is kept.
func (s *Server) hTeamInviteCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	var req grantBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	servers, ok := s.existingServers(w, r)
	if !ok {
		return
	}
	c, err := invites.NewMember(invites.MemberSpec{ProjectID: s.projectID(sess.Access), Role: req.Role, Servers: req.Servers, Label: req.Label},
		sess.Access.Account, serverIDs(servers), s.now())
	if err != nil {
		writeRefusal(w, err)
		return
	}
	if err := s.insertInvite(c.Invite); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "invite.create", c.Invite.Actor(), "succeeded",
		fmt.Sprintf("team invite; %s of %s; works 7 days", c.Invite.Role, scopeText(c.Invite.Servers, servers)))
	writeJSON(w, http.StatusCreated, map[string]any{"invite": c.Invite.Summarize(s.now()), "path": c.Path, "link": s.linkBase(r)})
}

// teamInvite loads an unused team invite of the signed-in account's project
// that the account could have made as it stands.
func (s *Server) teamInvite(w http.ResponseWriter, r *http.Request, sess *session) (invites.Invite, bool) {
	id := r.PathValue("invite")
	if invites.ValidID(id) {
		inv, err := s.inviteByID(id)
		switch {
		case err == nil && inv.Kind == invites.KindMember && inv.ProjectID == s.projectID(sess.Access):
			if err := invites.CanGrant(sess.Access.Account, inv.Role, inv.Servers); err != nil {
				writeRefusal(w, err)
				return invites.Invite{}, false
			}
			return inv, true
		case err != nil && !isNoRows(err):
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return invites.Invite{}, false
		}
	}
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such invite link.", "")
	return invites.Invite{}, false
}

// hTeamInviteEdit changes the role or servers an unused team invite gives.
func (s *Server) hTeamInviteEdit(w http.ResponseWriter, r *http.Request, sess *session) {
	var req grantBody
	if err := decodeJSON(r, &req); err != nil || req.Label != "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	inv, ok := s.teamInvite(w, r, sess)
	if !ok {
		return
	}
	servers, ok := s.existingServers(w, r)
	if !ok {
		return
	}
	if err := s.checkGrant(sess.Access, req, servers); err != nil {
		writeRefusal(w, err)
		return
	}
	res, err := s.db.Exec(`UPDATE invites SET role = ?, servers = ? WHERE id = ? AND uses = 0 AND revoked_at = 0`, req.Role, req.Servers.String(), inv.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "This invite link was used or turned off a moment ago.", "Reload the page.")
		return
	}
	s.audit(sess.User.Username, "invite.edit", inv.Actor(), "succeeded", fmt.Sprintf("%s of %s", req.Role, scopeText(req.Servers, servers)))
	inv.Role, inv.Servers = req.Role, req.Servers
	writeJSON(w, http.StatusOK, teamInvite{Summary: inv.Summarize(s.now()), CanEdit: true})
}

// checkGrant checks a role and servers chosen for someone else: the account
// may give them, and the servers exist.
func (s *Server) checkGrant(a access, req grantBody, servers []serverRef) error {
	if err := invites.CanGrant(a.Account, req.Role, req.Servers); err != nil {
		return err
	}
	for _, id := range req.Servers.Servers {
		found := false
		for _, sv := range servers {
			found = found || sv.ID == id
		}
		if !found {
			return &invites.Error{Code: invites.CodeBadOptions, Status: http.StatusBadRequest, Params: map[string]string{"field": "servers"},
				Msg: "Choose servers from this project, each once."}
		}
	}
	return nil
}

// hTeamInviteRevoke turns an unused team invite off.
func (s *Server) hTeamInviteRevoke(w http.ResponseWriter, r *http.Request, sess *session) {
	inv, ok := s.teamInvite(w, r, sess)
	if !ok {
		return
	}
	if _, err := s.db.Exec(`UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, s.now().UnixMilli(), inv.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "invite.revoke", inv.Actor(), "succeeded", "team invite")
	w.WriteHeader(http.StatusNoContent)
}

// teamMemberTarget loads the member in the path, in the signed-in account's
// project, as the invite rules see them.
func (s *Server) teamMemberTarget(w http.ResponseWriter, r *http.Request, sess *session) (access, bool) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err == nil && id > 0 {
		var u user
		err = s.db.QueryRow(`SELECT u.id, u.username, u.role FROM users u JOIN project_members m ON m.user_id = u.id
			WHERE u.id = ? AND m.project_id = ?`, id, s.projectID(sess.Access)).Scan(&u.ID, &u.Username, &u.Role)
		if err == nil {
			t, err := s.access(u)
			if err == nil {
				return t, true
			}
		}
		if err != nil && !isNoRows(err) {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return access{}, false
		}
	}
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such team member.", "")
	return access{}, false
}

// hTeamMemberEdit changes a member's role and servers. Links the member made
// that the new role no longer allows are turned off.
func (s *Server) hTeamMemberEdit(w http.ResponseWriter, r *http.Request, sess *session) {
	var req grantBody
	if err := decodeJSON(r, &req); err != nil || req.Label != "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	t, ok := s.teamMemberTarget(w, r, sess)
	if !ok {
		return
	}
	if err := invites.CanEdit(sess.Access.Account, t.Account, req.Role, req.Servers); err != nil {
		writeRefusal(w, err)
		return
	}
	servers, ok := s.existingServers(w, r)
	if !ok {
		return
	}
	if err := s.checkGrant(sess.Access, req, servers); err != nil {
		writeRefusal(w, err)
		return
	}
	// Only the owner makes admins, so making someone an admin confirms the
	// two-factor sign-in they have on now; any other role forgets it.
	confirmed := int64(0)
	if req.Role == invites.RoleAdmin {
		confirmed = s.factorAt(t.UserID)
	}
	if _, err := s.db.Exec(`UPDATE project_members SET role = ?, servers = ?, admin_factor = ? WHERE user_id = ? AND project_id = ?`,
		req.Role, req.Servers.String(), confirmed, t.UserID, t.ProjectID); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "team.edit", t.Name, "succeeded", fmt.Sprintf("%s of %s", req.Role, scopeText(req.Servers, servers)))
	after, err := s.access(user{ID: t.UserID, Username: t.Name, Role: t.InstallRole})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.turnOffLinks(r, sess.User, after.Account, "creator's role changed")
	s.answerMember(w, r, sess, t.UserID)
}

// answerMember answers with a member's row as it stands now.
func (s *Server) answerMember(w http.ResponseWriter, r *http.Request, sess *session, userID int64) {
	var u user
	var added int64
	err := s.db.QueryRow(`SELECT u.id, u.username, u.role, m.created_at FROM users u JOIN project_members m ON m.user_id = u.id WHERE u.id = ?`, userID).
		Scan(&u.ID, &u.Username, &u.Role, &added)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	t, err := s.access(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, memberRow(sess.Access, t, invites.FromMillis(added)))
}

// canConfirm reports whether a may confirm the Admin rights of t, an admin
// who turned on two-factor sign-in: the owner may, and so may an admin whose
// own Admin rights count and whose servers include all of t's.
func canConfirm(a, t access) error {
	switch {
	case t.owner() || t.UserID == a.UserID || !invites.RequiresTwoFactor(t.InstallRole, t.ProjectRole):
		return errForbidden
	case a.owner():
		return nil
	case permit(a, actManageTeam, "") != nil:
		return errForbidden
	case !t.Servers.Within(a.Servers):
		return errNoServer
	}
	return nil
}

// hTeamConfirmAdmin confirms an admin's rights with one click, once they
// have turned on two-factor sign-in. It confirms that very setup: turned
// off and on again, it needs confirming again. When an admin confirms, the
// owner hears about it on Discord.
func (s *Server) hTeamConfirmAdmin(w http.ResponseWriter, r *http.Request, sess *session) {
	t, ok := s.teamMemberTarget(w, r, sess)
	if !ok {
		return
	}
	if err := canConfirm(sess.Access, t); err != nil {
		writeRefusal(w, err)
		return
	}
	factor := s.factorAt(t.UserID)
	if !t.awaitingConfirmation() || factor == 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "There's nothing to confirm.", "Reload the page.")
		return
	}
	if _, err := s.db.Exec(`UPDATE project_members SET admin_factor = ? WHERE user_id = ? AND project_id = ? AND role = ?`,
		factor, t.UserID, t.ProjectID, invites.RoleAdmin); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "team.confirm_admin", t.Name, "succeeded", "")
	if !sess.Access.owner() {
		s.notifyTeam(api.DiscordNotifyRequest{Kind: api.DiscordAdminConfirmed, Member: t.Name, Actor: sess.User.Username})
	}
	s.answerMember(w, r, sess, t.UserID)
}

// hTeamMemberRemove takes someone off the team: their account goes, with its
// sessions, and so do the links they made.
func (s *Server) hTeamMemberRemove(w http.ResponseWriter, r *http.Request, sess *session) {
	t, ok := s.teamMemberTarget(w, r, sess)
	if !ok {
		return
	}
	if err := invites.CanRemove(sess.Access.Account, t.Account); err != nil {
		writeRefusal(w, err)
		return
	}
	s.turnOffLinks(r, sess.User, invites.Account{UserID: t.UserID}, "creator removed")
	if _, err := s.db.Exec(`DELETE FROM users WHERE id = ? AND role = ?`, t.UserID, roleMember); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.deleteUserSessions(t.UserID)
	s.audit(sess.User.Username, "team.remove", t.Name, "succeeded", "")
	w.WriteHeader(http.StatusNoContent)
}

// turnOffLinks turns off the open links creator made that they can no
// longer make as they stand now, and declines what those links let people
// ask. creator must be read after the change that prompts the call; a
// removed member is the zero Account, who can make none.
func (s *Server) turnOffLinks(r *http.Request, by user, creator invites.Account, why string) {
	rows, err := s.db.Query(`SELECT `+inviteColumns+` FROM invites WHERE created_by = ? AND revoked_at = 0`, creator.UserID)
	if err != nil {
		s.log.Warn("could not read a member's invite links", "err", err)
		return
	}
	var off []string
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			continue
		}
		allowed := invites.CanLetPlayersIn(creator, inv.ServerID) == nil
		if inv.Kind == invites.KindMember {
			allowed = invites.CanGrant(creator, inv.Role, inv.Servers) == nil
		}
		if creator.InstallRole == "" || !allowed {
			off = append(off, inv.ID)
		}
	}
	rows.Close()
	now := s.now().UnixMilli()
	for _, id := range off {
		err := s.immediate(r.Context(), func(c *sql.Conn) error {
			if _, err := c.ExecContext(r.Context(), `UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, now, id); err != nil {
				return err
			}
			_, err := c.ExecContext(r.Context(), `UPDATE join_requests SET state = 'declined', decided_at = ?, decided_by = ?, address = ''
				WHERE invite_id = ? AND state = 'pending'`, now, by.ID, id)
			return err
		})
		if err != nil {
			s.log.Warn("could not turn off an invite link", "err", err)
			continue
		}
		s.audit(by.Username, "invite.revoke", "invite:"+id, "succeeded", why)
	}
}

// --- routes scoped to the servers an account can use ---

// hMachineActivity is a machine's recent activity, for the servers the
// account can use. Lines about the whole machine need all servers.
func (s *Server) hMachineActivity(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	ask := limit
	if !sess.Access.Servers.All {
		ask = 200
	}
	var list []api.Activity
	status, err := m.agent.Do(r.Context(), "GET", "/v1/activity", url.Values{"limit": {strconv.Itoa(ask)}}, nil, &list)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	out := []api.Activity{}
	for _, a := range list {
		if len(out) == limit {
			break
		}
		if a.ServerID == "" && sess.Access.Servers.All || a.ServerID != "" && sess.Access.covers(a.ServerID) {
			out = append(out, a)
		}
	}
	out = append(out, s.teamJoins(sess.Access, limit)...)
	slices.SortStableFunc(out, func(a, b api.Activity) int { return b.TS.Compare(a.TS) })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, status, out)
}

// teamJoins are members joining the team, as activity with their role.
// Who is on the team is for those who manage it, so everyone else sees only
// their own join.
func (s *Server) teamJoins(a access, limit int) []api.Activity {
	everyone := permit(a, actManageTeam, "") == nil
	rows, err := s.db.Query(`SELECT u.username, m.role, m.created_at FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ? AND u.role = ? AND (? OR u.id = ?) ORDER BY m.created_at DESC LIMIT ?`,
		s.projectID(a), roleMember, everyone, a.UserID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []api.Activity
	for rows.Next() {
		var name, role string
		var joined int64
		if rows.Scan(&name, &role, &joined) == nil {
			out = append(out, api.Activity{TS: invites.FromMillis(joined), Kind: api.ActivityTeamJoined, Actor: name, Detail: role})
		}
	}
	return out
}

// hOperation is an operation's progress, for the servers the account can
// use. Machine-wide operations need all servers.
func (s *Server) hOperation(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	var op api.Operation
	status, err := m.agent.Do(r.Context(), "GET", agentPath("/v1/operations/{op}", r), nil, nil, &op)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	if op.ServerID == "" && !sess.Access.Servers.All || op.ServerID != "" && !sess.Access.covers(op.ServerID) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Operation not found.", "")
		return
	}
	writeJSON(w, status, op)
}

// restoreProxy forwards a restore step once the account may restore into its
// target: an existing server it can use, or a new server, which needs all
// servers.
func (s *Server) restoreProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	fwd := s.forward(method, pattern)
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.machineFromPath(w, r)
		if !ok {
			return
		}
		var p api.RestorePreview
		if _, err := m.agent.Do(r.Context(), "GET", agentPath("/v1/restore/{rid}", r), nil, nil, &p); err != nil {
			s.agentFailure(w, err)
			return
		}
		act := actRestore
		if p.ServerID == "" {
			act = actCreateServers
		}
		if err := permit(sess.Access, act, p.ServerID); err != nil {
			writeRefusal(w, err)
			return
		}
		fwd(w, r, sess)
	}
}

// localMachine is the machine this panel runs on, whose agent keeps the
// Discord connection.
func (s *Server) localMachine() (machine, error) {
	list, err := s.machines()
	if err != nil {
		return machine{}, err
	}
	for _, m := range list {
		if m.Kind == localKind {
			return m, nil
		}
	}
	return machine{}, errNotFound
}

// discordProxy forwards Settings › Discord to the agent, which keeps the
// webhook URL: it is a secret, so the agent never sends it back, and the
// panel passes it on without keeping or logging it. Changes carry the
// signed-in account as actor, and the dashboard's host name for the links
// and addresses in Discord messages.
func (s *Server) discordProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, err := s.localMachine()
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, api.CodeAgentUnavailable, "This machine's agent isn't set up.", "")
			return
		}
		var raw json.RawMessage
		var status int
		switch method {
		case "GET":
			status, err = m.agent.Do(r.Context(), "GET", pattern, nil, nil, &raw)
		case "DELETE":
			status, err = m.agent.Do(r.Context(), "DELETE", pattern, url.Values{"actor": {sess.User.Username}}, nil, &raw)
		default:
			body := map[string]any{}
			b, rerr := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if rerr != nil || len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &body) != nil {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
				return
			}
			body["actor"] = sess.User.Username
			delete(body, "host")
			if method == "PUT" || pattern == "/v1/discord/connect" {
				body["host"] = s.joinHost(r)
			}
			status, err = m.agent.Do(r.Context(), method, pattern, nil, body, &raw)
		}
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		if status == http.StatusNoContent || len(raw) == 0 {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, raw)
	}
}
