package panel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// Friend invite links, join requests and how players got in (wave 5). The
// rules live in internal/invites; these handlers store what it decides.

const inviteColumns = `id, kind, code_hash, code, project_id, server_id, role, servers, approval, label, created_by, created_at, expires_at, max_uses, uses, revoked_at`

// useInvite counts one use of an invite in one statement, so two people
// can't both take the last one (see invites.RecordUse).
const useInvite = `UPDATE invites SET uses = uses + 1 WHERE id = ? AND revoked_at = 0 AND (expires_at = 0 OR expires_at > ?) AND (max_uses = 0 OR uses < max_uses)`

// giveUseBack returns a use that didn't let anyone in.
const giveUseBack = `UPDATE invites SET uses = uses - 1 WHERE id = ? AND uses > 0`

type rowScanner interface{ Scan(dest ...any) error }

func scanInvite(row rowScanner) (invites.Invite, error) {
	var inv invites.Invite
	var kind, servers, approval string
	var created, expires, revoked int64
	if err := row.Scan(&inv.ID, &kind, &inv.CodeHash, &inv.Code, &inv.ProjectID, &inv.ServerID, &inv.Role, &servers, &approval,
		&inv.Label, &inv.CreatedBy, &created, &expires, &inv.MaxUses, &inv.Uses, &revoked); err != nil {
		return invites.Invite{}, err
	}
	inv.Kind, inv.Approval = invites.Kind(kind), invites.Approval(approval)
	if inv.Kind == invites.KindMember {
		inv.Servers, _ = invites.ParseScope(servers)
	}
	inv.CreatedAt, inv.ExpiresAt, inv.RevokedAt = invites.FromMillis(created), invites.FromMillis(expires), invites.FromMillis(revoked)
	return inv, nil
}

func (s *Server) insertInvite(inv invites.Invite) error {
	servers := ""
	if inv.Kind == invites.KindMember {
		servers = inv.Servers.String()
	}
	_, err := s.db.Exec(`INSERT INTO invites(`+inviteColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		inv.ID, string(inv.Kind), inv.CodeHash, inv.Code, inv.ProjectID, inv.ServerID, inv.Role, servers, string(inv.Approval), inv.Label,
		inv.CreatedBy, invites.Millis(inv.CreatedAt), invites.Millis(inv.ExpiresAt), inv.MaxUses, inv.Uses, invites.Millis(inv.RevokedAt))
	return err
}

func (s *Server) inviteByID(id string) (invites.Invite, error) {
	return scanInvite(s.db.QueryRow(`SELECT `+inviteColumns+` FROM invites WHERE id = ?`, id))
}

// accountByID is an account as it stands now, for the invite rules; a
// deleted account is the zero Account.
func (s *Server) accountByID(id int64) invites.Account {
	var u user
	if err := s.db.QueryRow(`SELECT id, username, role FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Username, &u.Role); err != nil {
		return invites.Account{}
	}
	a, err := s.access(u)
	if err != nil {
		return invites.Account{}
	}
	return a.Account
}

// projectID is the signed-in account's project, or the default one.
func (s *Server) projectID(a access) string {
	if a.ProjectID != "" {
		return a.ProjectID
	}
	var id string
	_ = s.db.QueryRow(`SELECT id FROM projects ORDER BY created_at LIMIT 1`).Scan(&id)
	return id
}

// immediate runs fn in a write transaction that takes SQLite's write lock
// first, so nothing fn reads can change before it writes.
func (s *Server) immediate(ctx context.Context, fn func(c *sql.Conn) error) error {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	end := func(stmt string) error {
		_, err := c.ExecContext(context.WithoutCancel(ctx), stmt)
		if err != nil && stmt == "ROLLBACK" {
			// A connection left inside a transaction must not go back to
			// the pool.
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
		return err
	}
	if err := fn(c); err != nil {
		end("ROLLBACK")
		return err
	}
	if err := end("COMMIT"); err != nil {
		end("ROLLBACK")
		return err
	}
	return nil
}

// linkBase is where invite links start: the dashboard's own address name
// when it has one, otherwise the host the signed-in admin used, with the
// panel's port (so a link copied over an SSH tunnel still names the port
// friends use).
type linkBase struct {
	Base string `json:"base"`
	// Friendly is false until the dashboard has an address name, when the
	// UI suggests setting one up before sharing.
	Friendly bool `json:"friendly"`
}

func (s *Server) linkBase(r *http.Request) linkBase {
	host := s.joinHost(r)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if s.cfg.PanelPort != 443 && s.cfg.PanelPort != 0 {
		host += ":" + strconv.Itoa(s.cfg.PanelPort)
	}
	return linkBase{Base: "https://" + host, Friendly: s.cfg.Domain != ""}
}

// joinHost is the host name friends reach this machine at: the dashboard's
// address name, or the host of the request.
func (s *Server) joinHost(r *http.Request) string {
	if s.cfg.Domain != "" {
		return s.cfg.Domain
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// serverStatus asks the machine that runs a server how it is.
func (s *Server) serverStatus(r *http.Request, serverID string) (machine, api.ServerStatus, error) {
	m, err := s.machineForServer(r, serverID)
	if err != nil {
		return machine{}, api.ServerStatus{}, err
	}
	var st api.ServerStatus
	if _, err := m.agent.Do(r.Context(), "GET", "/v1/servers/"+url.PathEscape(serverID), nil, nil, &st); err != nil {
		return machine{}, api.ServerStatus{}, err
	}
	return m, st, nil
}

// inviteServer is what the join page may say about a server: its name,
// the address to join, its version and how many are playing (never who).
func (s *Server) inviteServer(r *http.Request, st api.ServerStatus) invites.Server {
	port := st.GamePort
	if port == 0 {
		port = s.cfg.GamePort
	}
	addr, err := invites.JoinAddress(s.joinHost(r), port)
	if err != nil {
		addr = ""
	}
	srv := invites.Server{Name: st.Name, Address: addr, Online: st.Phase == api.PhaseOnline}
	if st.Config != nil {
		srv.Version = st.Config.MinecraftVersion
	}
	if p := st.Players; p != nil && s.now().Sub(p.At) < 2*time.Minute {
		srv.Playing = p.Online
	}
	return srv
}

// addToWhitelist asks the agent to add a player, with the UUID so a
// stopped server's list can be written directly.
func (s *Server) addToWhitelist(ctx context.Context, m machine, serverID string, p mojang.Profile, actor string) (api.WhitelistChange, error) {
	var change api.WhitelistChange
	_, err := m.agent.Do(ctx, "POST", "/v1/servers/"+url.PathEscape(serverID)+"/whitelist", nil,
		api.WhitelistRequest{Name: p.Name, UUID: p.ID, Actor: actor}, &change)
	return change, err
}

func (s *Server) keepOrigin(o invites.Origin, name string) {
	if _, err := s.db.Exec(`INSERT INTO player_origins(server_id, player_uuid, player_name, invite_id, request_id, joined_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(server_id, player_uuid) DO UPDATE SET player_name = excluded.player_name, invite_id = excluded.invite_id,
		request_id = excluded.request_id, joined_at = excluded.joined_at`,
		o.ServerID, o.PlayerUUID, name, o.InviteID, o.RequestID, invites.Millis(o.JoinedAt)); err != nil {
		s.log.Warn("could not keep how a player got in", "err", err)
	}
}

// origins says how each player who came with an invite got onto a server,
// by UUID and by lower-case name (offline-mode servers list other UUIDs).
func (s *Server) origins(serverID string) (map[string]*api.Phrase, error) {
	rows, err := s.db.Query(`SELECT o.player_uuid, o.player_name, o.invite_id, o.request_id, o.joined_at FROM player_origins o WHERE o.server_id = ?`, serverID)
	if err != nil {
		return nil, err
	}
	type row struct {
		o    invites.Origin
		name string
	}
	var list []row
	for rows.Next() {
		var x row
		var joined int64
		if err := rows.Scan(&x.o.PlayerUUID, &x.name, &x.o.InviteID, &x.o.RequestID, &joined); err != nil {
			rows.Close()
			return nil, err
		}
		x.o.ServerID, x.o.JoinedAt = serverID, invites.FromMillis(joined)
		list = append(list, x)
	}
	rows.Close()
	out := map[string]*api.Phrase{}
	for _, x := range list {
		inv, _ := s.inviteByID(x.o.InviteID)
		step := x.o.Note(inv)
		at := x.o.JoinedAt
		note := &api.Phrase{Key: step.Key, Params: step.Params, Text: step.Text, At: &at}
		out[x.o.PlayerUUID] = note
		out["name:"+strings.ToLower(x.name)] = note
	}
	return out, nil
}

func noteFor(origins map[string]*api.Phrase, name, uuid string) *api.Phrase {
	if id, ok := mojang.NormalizeUUID(uuid); ok {
		if n := origins[id]; n != nil {
			return n
		}
	}
	return origins["name:"+strings.ToLower(name)]
}

// --- the Players tab ---

type invitesBody struct {
	Invites  []invites.Summary `json:"invites"`
	Expiries []invites.Expiry  `json:"expiries"`
	Link     linkBase          `json:"link"`
}

// hInvites lists a server's friend invite links that aren't turned off,
// newest first.
func (s *Server) hInvites(w http.ResponseWriter, r *http.Request, sess *session) {
	id := r.PathValue("id")
	if err := invites.CanLetPlayersIn(sess.Access.Account, id); err != nil {
		writeRefusal(w, err)
		return
	}
	rows, err := s.db.Query(`SELECT `+inviteColumns+` FROM invites WHERE kind = 'player' AND server_id = ? AND revoked_at = 0 ORDER BY created_at DESC LIMIT 100`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	out := invitesBody{Invites: []invites.Summary{}, Expiries: invites.Expiries(), Link: s.linkBase(r)}
	now := s.now()
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			continue
		}
		out.Invites = append(out.Invites, inv.Summarize(now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hInviteCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Label     string           `json:"label"`
		Expiry    invites.Expiry   `json:"expiry"`
		MaxUses   int              `json:"maxUses"`
		Unlimited bool             `json:"unlimited"`
		Approval  invites.Approval `json:"approval"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	id := r.PathValue("id")
	_, st, err := s.serverStatus(r, id)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	c, err := invites.NewPlayer(invites.PlayerSpec{ServerID: id, ProjectID: s.projectID(sess.Access), Label: req.Label, Expiry: req.Expiry,
		MaxUses: req.MaxUses, Unlimited: req.Unlimited, Approval: req.Approval}, sess.Access.Account, s.now())
	if err != nil {
		writeRefusal(w, err)
		return
	}
	if err := s.insertInvite(c.Invite); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	uses := "no limit"
	if c.Invite.MaxUses > 0 {
		uses = fmt.Sprintf("%d friends", c.Invite.MaxUses)
	}
	s.audit(sess.User.Username, "invite.create", c.Invite.Actor(), "succeeded",
		fmt.Sprintf("friend invite for %s; %s; %s; %s", st.Name, expiryText(req.Expiry), uses, c.Invite.Approval))
	writeJSON(w, http.StatusCreated, c.Invite.Summarize(s.now()))
}

func expiryText(e invites.Expiry) string {
	if e == "" {
		e = invites.DefaultExpiry
	}
	if e == invites.ExpiryUntilTurnedOff {
		return "until turned off"
	}
	return "works " + string(e)
}

// hInviteRevoke turns a friend invite off and declines what it let people
// ask. Turning off a link that is off already is fine.
func (s *Server) hInviteRevoke(w http.ResponseWriter, r *http.Request, sess *session) {
	id, serverID := r.PathValue("invite"), r.PathValue("id")
	if !invites.ValidID(id) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such invite link.", "")
		return
	}
	if err := invites.CanLetPlayersIn(sess.Access.Account, serverID); err != nil {
		writeRefusal(w, err)
		return
	}
	var declined int64
	now := s.now().UnixMilli()
	err := s.immediate(r.Context(), func(c *sql.Conn) error {
		res, err := c.ExecContext(r.Context(), `UPDATE invites SET revoked_at = CASE WHEN revoked_at = 0 THEN ? ELSE revoked_at END
			WHERE id = ? AND kind = 'player' AND server_id = ?`, now, id, serverID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound
		}
		res, err = c.ExecContext(r.Context(), `UPDATE join_requests SET state = 'declined', decided_at = ?, decided_by = ?, address = ''
			WHERE invite_id = ? AND state = 'pending'`, now, sess.User.ID, id)
		if err == nil {
			declined, _ = res.RowsAffected()
		}
		return err
	})
	switch {
	case errors.Is(err, errNotFound):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such invite link.", "")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "invite.revoke", "invite:"+id, "succeeded", fmt.Sprintf("%d pending requests declined", declined))
	w.WriteHeader(http.StatusNoContent)
}

const requestColumns = `id, invite_id, server_id, player_uuid, player_name, address, state, created_at, decided_at, decided_by`

func scanRequest(row rowScanner) (invites.JoinRequest, error) {
	var jr invites.JoinRequest
	var state string
	var created, decided int64
	err := row.Scan(&jr.ID, &jr.InviteID, &jr.ServerID, &jr.PlayerUUID, &jr.PlayerName, &jr.Address, &state, &created, &decided, &jr.DecidedBy)
	jr.State, jr.CreatedAt, jr.DecidedAt = invites.RequestState(state), invites.FromMillis(created), invites.FromMillis(decided)
	return jr, err
}

type requestView struct {
	Request invites.JoinRequest `json:"request"`
	Notice  invites.Notice      `json:"notice"`
}

// hJoinRequests lists a server's pending join requests, oldest first.
func (s *Server) hJoinRequests(w http.ResponseWriter, r *http.Request, sess *session) {
	rows, err := s.db.Query(`SELECT `+requestColumns+` FROM join_requests WHERE server_id = ? AND state = 'pending' ORDER BY created_at LIMIT 100`, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	var list []invites.JoinRequest
	for rows.Next() {
		if jr, err := scanRequest(rows); err == nil {
			list = append(list, jr)
		}
	}
	rows.Close()
	out := []requestView{}
	for _, jr := range list {
		inv, _ := s.inviteByID(jr.InviteID)
		out = append(out, requestView{Request: jr, Notice: jr.Notice(inv)})
	}
	writeJSON(w, http.StatusOK, out)
}

// joinRequest loads a request of the server in the path.
func (s *Server) joinRequest(w http.ResponseWriter, r *http.Request) (invites.JoinRequest, bool) {
	id := r.PathValue("request")
	if invites.ValidID(id) {
		jr, err := scanRequest(s.db.QueryRow(`SELECT `+requestColumns+` FROM join_requests WHERE id = ? AND server_id = ?`, id, r.PathValue("id")))
		if err == nil {
			return jr, true
		}
		if !isNoRows(err) {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return invites.JoinRequest{}, false
		}
	}
	writeErr(w, http.StatusNotFound, api.CodeNotFound, "No such join request.", "")
	return invites.JoinRequest{}, false
}

// hJoinRequestApprove lets a player in: the request is decided first, so two
// people answering at once can't both add them, and put back if the agent
// can't add the player.
func (s *Server) hJoinRequestApprove(w http.ResponseWriter, r *http.Request, sess *session) {
	jr, ok := s.joinRequest(w, r)
	if !ok {
		return
	}
	decided, grant, err := invites.Approve(jr, sess.Access.Account, s.now())
	if err != nil {
		writeRefusal(w, err)
		return
	}
	res, err := s.db.Exec(`UPDATE join_requests SET state = 'approved', decided_at = ?, decided_by = ?, address = '' WHERE id = ? AND state = 'pending'`,
		invites.Millis(decided.DecidedAt), decided.DecidedBy, jr.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeRefusal(w, invites.RequestDecided())
		return
	}
	m, err := s.machineForServer(r, jr.ServerID)
	var change api.WhitelistChange
	if err == nil {
		change, err = s.addToWhitelist(r.Context(), m, jr.ServerID, grant.Profile, sess.User.Username)
	}
	if err != nil {
		if dbErr := s.putBack(context.WithoutCancel(r.Context()), jr, decided); dbErr != nil {
			s.log.Error("could not put a join request back", "err", dbErr)
		}
		s.audit(sess.User.Username, "join_request.approve", jr.PlayerName, "failed", fmt.Sprintf("request %s, invite %s", jr.ID, jr.InviteID))
		s.agentFailure(w, err)
		return
	}
	var origin *invites.Origin
	if change.Added {
		o := grant.Origin(s.now())
		s.keepOrigin(o, grant.Profile.Name)
		origin = &o
	} else if _, err := s.db.Exec(giveUseBack, jr.InviteID); err != nil {
		s.log.Warn("could not give an invite its use back", "err", err)
	}
	s.audit(sess.User.Username, "join_request.approve", jr.PlayerName, "succeeded", fmt.Sprintf("request %s, invite %s", jr.ID, jr.InviteID))
	writeJSON(w, http.StatusOK, map[string]any{"request": decided, "origin": origin})
}

// putBack undoes an approve the agent couldn't carry out, for a request the
// approve left as it was. It waits again, unless its link was turned off
// meanwhile: a revoke, or removing the member who made the link, declines
// only the requests still waiting, so it would miss this one and a friend
// could still be let in through the dead link. Then it's declined.
func (s *Server) putBack(ctx context.Context, jr, decided invites.JoinRequest) error {
	at := invites.Millis(decided.DecidedAt)
	return s.immediate(ctx, func(c *sql.Conn) error {
		var live bool
		if err := c.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM invites WHERE id = ? AND revoked_at = 0)`, jr.InviteID).Scan(&live); err != nil {
			return err
		}
		if live {
			_, err := c.ExecContext(ctx, `UPDATE join_requests SET state = 'pending', decided_at = 0, decided_by = 0, address = ?
				WHERE id = ? AND state = 'approved' AND decided_at = ? AND decided_by = ?`, jr.Address, jr.ID, at, decided.DecidedBy)
			return err
		}
		_, err := c.ExecContext(ctx, `UPDATE join_requests SET state = 'declined', address = ''
			WHERE id = ? AND state = 'approved' AND decided_at = ? AND decided_by = ?`, jr.ID, at, decided.DecidedBy)
		return err
	})
}

// hJoinRequestDecline says no and gives the invite its use back.
func (s *Server) hJoinRequestDecline(w http.ResponseWriter, r *http.Request, sess *session) {
	jr, ok := s.joinRequest(w, r)
	if !ok {
		return
	}
	decided, err := invites.Decline(jr, sess.Access.Account, s.now())
	if err != nil {
		writeRefusal(w, err)
		return
	}
	err = s.immediate(r.Context(), func(c *sql.Conn) error {
		res, err := c.ExecContext(r.Context(), `UPDATE join_requests SET state = 'declined', decided_at = ?, decided_by = ?, address = '' WHERE id = ? AND state = 'pending'`,
			invites.Millis(decided.DecidedAt), decided.DecidedBy, jr.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return invites.RequestDecided()
		}
		_, err = c.ExecContext(r.Context(), giveUseBack, jr.InviteID)
		return err
	})
	if err != nil {
		writeRefusal(w, err)
		return
	}
	s.audit(sess.User.Username, "join_request.decline", jr.PlayerName, "succeeded", fmt.Sprintf("request %s, invite %s", jr.ID, jr.InviteID))
	writeJSON(w, http.StatusOK, map[string]any{"request": decided})
}

// hWhitelist lists the allowlist, saying how each player who came with an
// invite got in.
func (s *Server) hWhitelist(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var list []api.WhitelistEntry
	status, err := m.agent.Do(r.Context(), "GET", agentPath("/v1/servers/{id}/whitelist", r), nil, nil, &list)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	origins, err := s.origins(r.PathValue("id"))
	if err != nil {
		s.log.Warn("could not read how players got in", "err", err)
	}
	for i := range list {
		list[i].Joined = noteFor(origins, list[i].Name, list[i].UUID)
	}
	if list == nil {
		list = []api.WhitelistEntry{}
	}
	writeJSON(w, status, list)
}

// hWhitelistRemove takes a player off the allowlist and forgets how they got
// in. A link that still works can add them again.
func (s *Server) hWhitelistRemove(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var change api.WhitelistChange
	status, err := m.agent.Do(r.Context(), "DELETE", agentPath("/v1/servers/{id}/whitelist/{name}", r), url.Values{"actor": {sess.User.Username}}, nil, &change)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	if _, err := s.db.Exec(`DELETE FROM player_origins WHERE server_id = ? AND player_name = ? COLLATE NOCASE`, r.PathValue("id"), r.PathValue("name")); err != nil {
		s.log.Warn("could not forget how a player got in", "err", err)
	}
	origins, _ := s.origins(r.PathValue("id"))
	for i := range change.Whitelist {
		change.Whitelist[i].Joined = noteFor(origins, change.Whitelist[i].Name, change.Whitelist[i].UUID)
	}
	writeJSON(w, status, change)
}

// hProfile is a player's page, with how they got in.
func (s *Server) hProfile(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	q := url.Values{"name": {r.URL.Query().Get("name")}, "tz": {r.URL.Query().Get("tz")}}
	var p api.PlayerProfile
	status, err := m.agent.Do(r.Context(), "GET", agentPath("/v1/servers/{id}/players/profile", r), q, nil, &p)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	if origins, err := s.origins(r.PathValue("id")); err == nil {
		p.Joined = noteFor(origins, p.Name, p.UUID)
	}
	writeJSON(w, status, p)
}

// notifyJoinRequest tells the agent's Discord notifier about a join
// request. Discord is optional, so a failure only shows in the log.
func (s *Server) notifyJoinRequest(ctx context.Context, m machine, jr invites.JoinRequest, actor string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := m.agent.Do(ctx, "POST", "/v1/discord/notify", nil,
		api.DiscordNotifyRequest{Kind: api.DiscordJoinRequested, ServerID: jr.ServerID, Player: jr.PlayerName, Actor: actor}, nil); err != nil {
		s.log.Info("could not report a join request to Discord", "err", err)
	}
}

// notifyTeam tells this machine's Discord notifier about a change to the
// team, in the background: it is noticed while answering a request, and
// Discord is optional.
func (s *Server) notifyTeam(req api.DiscordNotifyRequest) {
	m, err := s.localMachine()
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := m.agent.Do(ctx, "POST", "/v1/discord/notify", nil, req, nil); err != nil {
			s.log.Info("could not report a team change to Discord", "err", err)
		}
	}()
}
