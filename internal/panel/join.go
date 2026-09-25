package panel

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// The page an invite link opens (/join/<code>) and the calls it makes. Anyone
// can reach these without signing in, so every call counts against the
// caller's address first, the code travels only in POST bodies, and a
// refusal is logged by its reason, never with the code. The page never shows
// the inviter's username either: it is the name they sign in with.

// joinCall is the body of every public invite call; each call reads the
// fields it needs.
type joinCall struct {
	Code     string `json:"code"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Format keeps the code and password out of anything printed with fmt.
func (c joinCall) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "{Name:%q Username:%q Code:[hidden] Password:[hidden]}", c.Name, c.Username)
}

// LogValue keeps the code and password out of slog output.
func (c joinCall) LogValue() slog.Value {
	return slog.GroupValue(slog.String("name", c.Name), slog.String("username", c.Username))
}

var errJoinUnavailable = &invites.Error{Code: api.CodeAgentUnavailable, Status: http.StatusServiceUnavailable,
	Msg: "Playkeeper can't reach this server right now.", Hint: "Try again in a few minutes.", Reason: "the agent did not answer"}

// hJoinPage is the dashboard's page for an invite link. It learns the kind
// of invite from the preview call. Its address holds the code, so browsers
// and proxies mustn't keep it.
func (s *Server) hJoinPage(w http.ResponseWriter, _ *http.Request, _ *session) {
	s.writeIndex(w, "no-store")
}

// openInvite runs the checks every public invite call starts with: the
// caller's address, the body, the code's shape, the invite it opens, and that
// invite's recent failures.
func (s *Server) openInvite(w http.ResponseWriter, r *http.Request) (joinCall, invites.Invite, bool) {
	ip, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return joinCall{}, invites.Invite{}, false
	}
	if err := s.joinGuard.Address(ip); err != nil {
		s.refuseJoin(w, err)
		return joinCall{}, invites.Invite{}, false
	}
	var c joinCall
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return joinCall{}, invites.Invite{}, false
	}
	if !invites.WellFormed(c.Code) {
		s.refuseJoin(w, invites.NotFound())
		return joinCall{}, invites.Invite{}, false
	}
	inv, err := scanInvite(s.db.QueryRow(`SELECT `+inviteColumns+` FROM invites WHERE code_hash = ?`, invites.HashCode(c.Code)))
	switch {
	case isNoRows(err):
		s.refuseJoin(w, invites.NotFound())
		return joinCall{}, invites.Invite{}, false
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return joinCall{}, invites.Invite{}, false
	}
	if err := s.joinGuard.Invite(inv.ID); err != nil {
		s.refuseJoin(w, err)
		return joinCall{}, invites.Invite{}, false
	}
	return c, inv, true
}

// refuseJoin answers a refusal on the public page and logs why, by the
// refusal's code and reason only.
func (s *Server) refuseJoin(w http.ResponseWriter, err error) {
	var e *invites.Error
	if errors.As(err, &e) {
		s.log.Info("invite link refused", "refusal", e.Code, "reason", e.Reason)
	}
	refuse(w, err)
}

// creator is an invite's creator as the invite rules see them now, without
// their username, which public pages never show.
func (s *Server) creator(inv invites.Invite) invites.Account {
	a := s.accountByID(inv.CreatedBy)
	a.Name = ""
	return a
}

// joinServer is what the public page may say about a friend invite's server,
// and the machine that runs it. A server that is gone reads like a link that
// doesn't work.
func (s *Server) joinServer(r *http.Request, serverID string) (invites.Server, machine, error) {
	m, st, err := s.serverStatus(r, serverID)
	var ae *agentclient.Error
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound, errors.Is(err, errNotFound):
		return invites.Server{}, machine{}, invites.NotFound()
	case err != nil:
		return invites.Server{}, machine{}, errJoinUnavailable
	}
	return s.inviteServer(r, st), m, nil
}

// serverRef is a server's id and name.
type serverRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// listServers lists every server on every machine. It fails when a machine
// doesn't answer, since team invites narrow to the servers that exist.
func (s *Server) listServers(ctx context.Context) ([]serverRef, error) {
	list, err := s.machines()
	if err != nil {
		return nil, err
	}
	var out []serverRef
	for _, m := range list {
		var servers []serverRef
		if _, err := m.agent.Do(ctx, "GET", "/v1/servers", nil, nil, &servers); err != nil {
			return nil, err
		}
		out = append(out, servers...)
	}
	return out, nil
}

func serverIDs(list []serverRef) []string {
	ids := make([]string, 0, len(list))
	for _, sv := range list {
		ids = append(ids, sv.ID)
	}
	return ids
}

// scopeNames names the servers of a scope, in the machines' order.
func scopeNames(sc invites.Scope, list []serverRef) []string {
	names := []string{}
	for _, sv := range list {
		if sc.Covers(sv.ID) {
			names = append(names, sv.Name)
		}
	}
	return names
}

// teamName is the project's name, or "" while it has the default name,
// which means nothing to someone new.
func (s *Server) teamName(projectID string) string {
	var name string
	_ = s.db.QueryRow(`SELECT name FROM projects WHERE id = ?`, projectID).Scan(&name)
	if name == defaultProjectName {
		return ""
	}
	return name
}

// memberPreview is what the page shows for a team invite: the invite as the
// rules allow it now, the team's name and the servers' names.
type memberPreview struct {
	invites.MemberPage
	Team        string   `json:"team,omitempty"`
	ServerNames []string `json:"serverNames"`
}

func (s *Server) hJoinPreview(w http.ResponseWriter, r *http.Request, _ *session) {
	c, inv, ok := s.openInvite(w, r)
	if !ok {
		return
	}
	creator := s.creator(inv)
	switch inv.Kind {
	case invites.KindPlayer:
		// Check the link before asking the machine anything.
		if _, err := invites.PreviewPlayer(inv, c.Code, creator, invites.Server{}, s.now()); err != nil {
			s.refuseJoin(w, err)
			return
		}
		srv, _, err := s.joinServer(r, inv.ServerID)
		if err != nil {
			s.refuseJoin(w, err)
			return
		}
		page, err := invites.PreviewPlayer(inv, c.Code, creator, srv, s.now())
		if err != nil {
			s.refuseJoin(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	case invites.KindMember:
		servers, err := s.listServers(r.Context())
		if err != nil {
			s.refuseJoin(w, errJoinUnavailable)
			return
		}
		page, err := invites.PreviewMember(inv, c.Code, creator, serverIDs(servers), s.now())
		if err != nil {
			s.refuseJoin(w, err)
			return
		}
		writeJSON(w, http.StatusOK, memberPreview{MemberPage: page, Team: s.teamName(inv.ProjectID), ServerNames: scopeNames(page.Servers, servers)})
	default:
		s.refuseJoin(w, invites.NotFound())
	}
}

// candidate is the account a typed name belongs to, with its face when the
// cache has one or can fetch it quickly.
type candidate struct {
	invites.Candidate
	Face string `json:"face,omitempty"`
}

// hJoinLookup checks a typed name for "Is this you?". Names half typed
// don't count against the link, only against the address.
func (s *Server) hJoinLookup(w http.ResponseWriter, r *http.Request, _ *session) {
	c, inv, ok := s.openInvite(w, r)
	if !ok {
		return
	}
	found, err := invites.LookupPlayer(r.Context(), s.mojang, inv, c.Code, s.creator(inv), c.Name, s.now())
	if err != nil {
		s.refuseJoin(w, err)
		return
	}
	out := candidate{Candidate: found}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if status, png := s.head(ctx, found.Name, strings.ReplaceAll(found.UUID, "-", "")); status == headOK {
		out.Face = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, http.StatusOK, out)
}

// hJoinRedeem puts a friend on the allowlist, or asks whoever made the link
// to let them in.
func (s *Server) hJoinRedeem(w http.ResponseWriter, r *http.Request, _ *session) {
	c, inv, ok := s.openInvite(w, r)
	if !ok {
		return
	}
	red, err := invites.RedeemPlayer(r.Context(), s.mojang, inv, c.Code, s.creator(inv), c.Name, s.now())
	s.joinGuard.Record(inv.ID, err)
	if err != nil {
		s.refuseJoin(w, err)
		return
	}
	srv, m, err := s.joinServer(r, inv.ServerID)
	if err != nil {
		s.refuseJoin(w, err)
		return
	}
	if red.Wait {
		s.askToJoin(w, r, c, inv, red, srv, m)
		return
	}
	s.letIn(w, r, c, inv, red, srv, m)
}

// ranOut explains why an invite couldn't take one more use: it ran out or
// was turned off a moment ago.
func (s *Server) ranOut(c joinCall, inv invites.Invite) error {
	now, err := s.inviteByID(inv.ID)
	if err != nil {
		return invites.NotFound()
	}
	if _, err := invites.PreviewPlayer(now, c.Code, s.creator(now), invites.Server{}, s.now()); err != nil {
		return err
	}
	return invites.NotFound()
}

func (s *Server) answerJoin(w http.ResponseWriter, info invites.JoinInfo, err error) {
	if err != nil {
		s.log.Warn("could not describe how to join", "err", err)
		refuse(w, errJoinUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// letIn adds a friend right away. The use is counted before the agent adds
// them, so two friends can't both take a link's last use, and given back
// when nobody new got in.
func (s *Server) letIn(w http.ResponseWriter, r *http.Request, c joinCall, inv invites.Invite, red invites.Redemption, srv invites.Server, m machine) {
	grant, _ := red.Grant()
	name := grant.Profile.Name
	res, err := s.db.Exec(useInvite, inv.ID, s.now().UnixMilli())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.refuseJoin(w, s.ranOut(c, inv))
		return
	}
	change, err := s.addToWhitelist(r.Context(), m, inv.ServerID, grant.Profile, inv.Actor())
	if err != nil || !change.Added {
		if _, dbErr := s.db.Exec(giveUseBack, inv.ID); dbErr != nil {
			s.log.Warn("could not give an invite its use back", "err", dbErr)
		}
	}
	if err != nil {
		s.audit(inv.Actor(), "invite.redeem", name, "failed", "the agent could not add the player")
		refuse(w, errJoinUnavailable)
		return
	}
	if change.Added {
		s.keepOrigin(grant.Origin(s.now()), name)
		s.audit(inv.Actor(), "invite.redeem", name, "succeeded", "added")
	} else {
		s.audit(inv.Actor(), "invite.redeem", name, "succeeded", "already on the allowlist")
	}
	info, err := invites.Join(srv, grant.Profile)
	s.answerJoin(w, info, err)
}

// askToJoin makes a join request for a link that lets people in after a yes.
// The request holds one of the link's uses until someone says no.
func (s *Server) askToJoin(w http.ResponseWriter, r *http.Request, c joinCall, inv invites.Invite, red invites.Redemption, srv invites.Server, m machine) {
	p := red.Profile
	if s.onAllowlist(r.Context(), m, inv.ServerID, p) {
		info, err := invites.Join(srv, p)
		s.answerJoin(w, info, err)
		return
	}
	address := s.addressKey(r)
	var jr invites.JoinRequest
	asked := false
	err := s.immediate(r.Context(), func(conn *sql.Conn) error {
		var waiting int
		if err := conn.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM join_requests WHERE server_id = ? AND player_uuid = ? AND state = 'pending'`,
			inv.ServerID, p.ID).Scan(&waiting); err != nil {
			return err
		}
		if waiting > 0 {
			return nil
		}
		var pending invites.Pending
		if err := conn.QueryRowContext(r.Context(), `SELECT
			(SELECT COUNT(*) FROM join_requests WHERE invite_id = ? AND state = 'pending'),
			(SELECT COUNT(*) FROM join_requests WHERE address = ? AND state = 'pending')`, inv.ID, address).
			Scan(&pending.Invite, &pending.Address); err != nil {
			return err
		}
		var err error
		if jr, err = invites.NewJoinRequest(red, address, pending, s.now()); err != nil {
			return err
		}
		res, err := conn.ExecContext(r.Context(), useInvite, inv.ID, s.now().UnixMilli())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errRanOut
		}
		if _, err := conn.ExecContext(r.Context(), `INSERT INTO join_requests(`+requestColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			jr.ID, jr.InviteID, jr.ServerID, jr.PlayerUUID, jr.PlayerName, jr.Address, string(jr.State),
			invites.Millis(jr.CreatedAt), 0, 0); err != nil {
			return err
		}
		asked = true
		return nil
	})
	switch {
	case errors.Is(err, errRanOut):
		s.refuseJoin(w, s.ranOut(c, inv))
		return
	case invites.CodeOf(err) != "":
		s.refuseJoin(w, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if asked {
		s.audit(inv.Actor(), "invite.redeem", p.Name, "succeeded", "asked to join")
		s.notifyJoinRequest(r.Context(), m, jr, inv.Actor())
	}
	info, err := invites.Wait(srv, p, "")
	s.answerJoin(w, info, err)
}

var errRanOut = errors.New("the invite ran out")

// onAllowlist reports whether a player is on a server's allowlist already,
// by UUID or, for offline-mode servers, by name. When the machine doesn't
// answer, they are asked to wait for a yes like anyone else.
func (s *Server) onAllowlist(ctx context.Context, m machine, serverID string, p mojang.Profile) bool {
	var list []api.WhitelistEntry
	if _, err := m.agent.Do(ctx, "GET", "/v1/servers/"+url.PathEscape(serverID)+"/whitelist", nil, nil, &list); err != nil {
		return false
	}
	for _, e := range list {
		if id, ok := mojang.NormalizeUUID(e.UUID); ok && id == p.ID || strings.EqualFold(e.Name, p.Name) {
			return true
		}
	}
	return false
}

// hJoinAccept makes the account a team invite is for, signs it in, and says
// what it must do before it can use its role.
func (s *Server) hJoinAccept(w http.ResponseWriter, r *http.Request, _ *session) {
	c, inv, ok := s.openInvite(w, r)
	if !ok {
		return
	}
	if inv.Kind != invites.KindMember {
		s.refuseJoin(w, invites.NotFound())
		return
	}
	servers, err := s.listServers(r.Context())
	if err != nil {
		s.refuseJoin(w, errJoinUnavailable)
		return
	}
	inviter := s.creator(inv)
	grant, err := invites.AcceptMember(inv, invites.MemberRequest{Code: c.Code, Username: c.Username, Password: c.Password}, inviter, serverIDs(servers), s.now())
	if err != nil {
		s.refuseJoin(w, err)
		return
	}
	hash, err := hashPassword(c.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not make the account.", "")
		return
	}
	var id int64
	now := s.now().UnixMilli()
	err = s.immediate(r.Context(), func(conn *sql.Conn) error {
		var taken int
		if err := conn.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`, grant.Username).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return invites.UsernameTaken()
		}
		res, err := conn.ExecContext(r.Context(), useInvite, inv.ID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errRanOut
		}
		res, err = conn.ExecContext(r.Context(), `INSERT INTO users(username, password_hash, role, created_at, password_changed_at) VALUES(?,?,?,?,?)`,
			grant.Username, hash, grant.InstallRole, now, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = conn.ExecContext(r.Context(), `INSERT INTO project_members(project_id, user_id, role, servers, created_at) VALUES(?,?,?,?,?)`,
			grant.ProjectID, id, grant.Role, grant.Servers.String(), now)
		return err
	})
	switch {
	case errors.Is(err, errRanOut):
		fresh, ferr := s.inviteByID(inv.ID)
		if ferr != nil {
			s.refuseJoin(w, invites.NotFound())
			return
		}
		_, cerr := invites.PreviewMember(fresh, c.Code, inviter, serverIDs(servers), s.now())
		if cerr == nil {
			cerr = invites.NotFound()
		}
		s.refuseJoin(w, cerr)
		return
	case invites.CodeOf(err) == invites.CodeUsernameTaken:
		s.joinGuard.Record(inv.ID, err)
		s.refuseJoin(w, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not make the account.", "")
		return
	}
	u := user{ID: id, Username: grant.Username, Role: grant.InstallRole}
	token, sess, err := s.newSession(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	}
	s.audit(inv.Actor(), "invite.accept", grant.Username, "succeeded", fmt.Sprintf("%s of %s", grant.Role, scopeText(grant.Servers, servers)))
	s.setSessionCookie(w, token)
	body := s.meBody(sess)
	body["requires"] = grant.Requires
	writeJSON(w, http.StatusOK, body)
}

// scopeText names a scope's servers for the audit trail.
func scopeText(sc invites.Scope, list []serverRef) string {
	if sc.All {
		return "all servers"
	}
	return strings.Join(scopeNames(sc, list), ", ")
}
