package panel

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// API tokens let AI agents use Playkeeper's tools at /mcp (see mcp.go). A
// token is made for a role and for every server or some, and runs out after
// a number of days; the dashboard keeps only its hash. It never does more
// than its account may do now: its rights are looked up again, with its
// account's, on every request and every tool call, and a token made for
// more than its account now has is revoked the first time that shows.

const (
	tokenPrefix = "pk_mcp_"
	// tokenSecretBytes of randomness follow the prefix, base64url-encoded.
	tokenSecretBytes = 24
	tokenActorPrefix = "token:"
	// maxTokens is how many working tokens one account may have.
	maxTokens        = 20
	maxTokenServers  = 100
	maxTokenName     = 40
	defaultTokenDays = 60
	// tokenTouchEvery is how often a token's last use is written down.
	tokenTouchEvery = time.Minute
)

// Token roles, which the dashboard shows as "Same as a Viewer" and so on.
const (
	tokenViewer    = "viewer"
	tokenModerator = "moderator"
	tokenAdmin     = "admin"
)

// tokenDays are the lifetimes a token can be made with.
var tokenDays = []int{30, 60, 90, 365}

func roleRank(role string) int {
	switch role {
	case tokenViewer:
		return 1
	case tokenModerator:
		return 2
	case tokenAdmin:
		return 3
	}
	return 0
}

// scopeOf is the MCP scope a token role grants.
func scopeOf(role string) mcp.Scope {
	switch role {
	case tokenViewer:
		return mcp.ScopeRead
	case tokenModerator:
		return mcp.ScopeManage
	case tokenAdmin:
		return mcp.ScopeOwner
	}
	return ""
}

func tokenActor(id string) string { return tokenActorPrefix + id }

// rights are what a token may do, or the most an account's tokens may do:
// up to a role, on every server or on the servers listed.
type rights struct {
	role    string
	all     bool
	servers []string
}

// accountRights are the most an account's tokens may do. An owner's may do
// anything; other accounts' may only look.
func accountRights(accountRole string) rights {
	switch accountRole {
	case roleOwner:
		return rights{role: tokenAdmin, all: true}
	case roleMember:
		return rights{role: tokenViewer, all: true}
	}
	return rights{}
}

// within reports whether r is no more than max. A token for every server
// is for every server its account may use, so only its role can exceed.
func (r rights) within(max rights) bool {
	if roleRank(r.role) == 0 || roleRank(r.role) > roleRank(max.role) {
		return false
	}
	if r.all || max.all {
		return true
	}
	for _, id := range r.servers {
		if !slices.Contains(max.servers, id) {
			return false
		}
	}
	return true
}

// effective is what a token within max may do now.
func (r rights) effective(max rights) rights {
	if r.all {
		return rights{role: r.role, all: max.all, servers: max.servers}
	}
	return r
}

// The servers column is "*" for every server, or server ids joined by
// commas. Anything else grants nothing.
func encodeServers(r rights) string {
	if r.all {
		return "*"
	}
	return strings.Join(r.servers, ",")
}

func decodeServers(col string) (all bool, servers []string, ok bool) {
	if col == "*" {
		return true, nil, true
	}
	if col == "" {
		return false, nil, false
	}
	servers = strings.Split(col, ",")
	for _, id := range servers {
		if !reMachineID.MatchString(id) {
			return false, nil, false
		}
	}
	return false, servers, true
}

type apiToken struct {
	rights
	ID          string
	UserID      int64
	Username    string
	AccountRole string
	Name        string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastUsedAt  time.Time
	RevokedAt   time.Time
}

const tokenColumns = `t.id, t.user_id, u.username, u.role, t.name, t.role, t.servers, t.created_at, t.expires_at, t.last_used_at, t.revoked_at`

func scanToken(sc interface{ Scan(...any) error }) (apiToken, error) {
	var t apiToken
	var servers string
	var created, expires, used, revoked int64
	if err := sc.Scan(&t.ID, &t.UserID, &t.Username, &t.AccountRole, &t.Name, &t.role, &servers, &created, &expires, &used, &revoked); err != nil {
		return apiToken{}, err
	}
	all, list, ok := decodeServers(servers)
	if !ok {
		t.role = ""
	}
	t.all, t.servers = all, list
	t.CreatedAt, t.ExpiresAt, t.LastUsedAt, t.RevokedAt = fromMillis(created), fromMillis(expires), fromMillis(used), fromMillis(revoked)
	return t, nil
}

func (s *Server) tokenWhere(cond string, args ...any) (apiToken, error) {
	return scanToken(s.db.QueryRow(`SELECT `+tokenColumns+` FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE `+cond, args...))
}

func (s *Server) tokensWhere(cond string, args ...any) ([]apiToken, error) {
	rows, err := s.db.Query(`SELECT `+tokenColumns+` FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE `+cond+` ORDER BY t.created_at DESC, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apiToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

var errTokenGone = errors.New("the token was revoked or ran out")

// tokenRights is what the token with this id may do now.
func (s *Server) tokenRights(id string) (rights, error) {
	t, err := s.tokenWhere(`t.id = ?`, id)
	if isNoRows(err) {
		return rights{}, errTokenGone
	}
	if err != nil {
		return rights{}, errDB
	}
	return s.checkToken(t)
}

// checkToken is what a token may do now: errTokenGone once it is revoked or
// has run out, and for a token made for more than its account may now let
// it do, which it revokes. It leaves the token's MCP sessions to the
// caller, which may be serving one of them.
func (s *Server) checkToken(t apiToken) (rights, error) {
	if !t.RevokedAt.IsZero() || !s.now().Before(t.ExpiresAt) {
		return rights{}, errTokenGone
	}
	max := accountRights(t.AccountRole)
	if !t.within(max) {
		s.revokeToken(t, "playkeeper", "its account can no longer do everything the token was made for")
		return rights{}, errTokenGone
	}
	return t.effective(max), nil
}

// revokeToken stops a token working and reports whether it was working.
func (s *Server) revokeToken(t apiToken, by, why string) bool {
	res, err := s.db.Exec(`UPDATE api_tokens SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at = 0`, s.now().UnixMilli(), by, t.ID)
	if err != nil {
		s.log.Error("revoke an API token", "err", err)
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false
	}
	s.audit(by, "token.revoke", t.Name, "succeeded", why)
	return true
}

// closeTokenSessions ends a token's MCP sessions and the calls they run.
func (s *Server) closeTokenSessions(id string) {
	if s.mcpHTTP != nil {
		s.mcpHTTP.CloseSessions(tokenActor(id))
	}
}

// sweepTokens revokes every working token made for more than its account
// may now let it do, so none waits for its next use.
func (s *Server) sweepTokens() {
	list, err := s.tokensWhere(`t.revoked_at = 0 AND t.expires_at > ?`, s.now().UnixMilli())
	if err != nil {
		s.log.Error("check API tokens", "err", err)
		return
	}
	for _, t := range list {
		if _, err := s.checkToken(t); errors.Is(err, errTokenGone) {
			s.closeTokenSessions(t.ID)
		}
	}
}

// revokeAccountTokens revokes every token of an account.
func (s *Server) revokeAccountTokens(userID int64, by, why string) {
	list, err := s.tokensWhere(`t.user_id = ? AND t.revoked_at = 0`, userID)
	if err != nil {
		s.log.Error("revoke an account's API tokens", "err", err)
		return
	}
	for _, t := range list {
		s.revokeToken(t, by, why)
		s.closeTokenSessions(t.ID)
	}
}

// authenticateToken checks a bearer token at /mcp. The principal's scope is
// what the token may do at this moment.
func (s *Server) authenticateToken(_ *http.Request, token string) (mcp.Principal, error) {
	secret, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || len(secret) != base64.RawURLEncoding.EncodedLen(tokenSecretBytes) {
		return mcp.Principal{}, mcp.ErrInvalidToken
	}
	t, err := s.tokenWhere(`t.token_hash = ?`, tokenHash(token))
	if isNoRows(err) {
		return mcp.Principal{}, mcp.ErrInvalidToken
	}
	if err != nil {
		return mcp.Principal{}, errDB
	}
	r, err := s.checkToken(t)
	if errors.Is(err, errTokenGone) {
		s.closeTokenSessions(t.ID)
		return mcp.Principal{}, mcp.ErrInvalidToken
	}
	if err != nil {
		return mcp.Principal{}, err
	}
	if now := s.now(); now.Sub(t.LastUsedAt) >= tokenTouchEvery {
		if _, err := s.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.UnixMilli(), t.ID); err != nil {
			s.log.Error("note an API token's use", "err", err)
		}
	}
	return mcp.Principal{ID: tokenActor(t.ID), Name: t.Name, Scopes: []mcp.Scope{scopeOf(r.role)}}, nil
}

// --- routes ---

type tokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	AllServers bool       `json:"allServers"`
	Servers    []string   `json:"servers"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	// Account is the account the token belongs to; owners see everyone's.
	Account string `json:"account"`
	Mine    bool   `json:"mine"`
}

func viewToken(t apiToken, sess *session) tokenView {
	v := tokenView{ID: t.ID, Name: t.Name, Role: t.role, AllServers: t.all, Servers: t.servers, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		Account: t.Username, Mine: t.UserID == sess.User.ID}
	if v.Servers == nil {
		v.Servers = []string{}
	}
	if !t.LastUsedAt.IsZero() {
		v.LastUsedAt = &t.LastUsedAt
	}
	return v
}

// hTokens lists the tokens that haven't been revoked, those that ran out
// too: an owner's view has every account's, anyone else's their own.
func (s *Server) hTokens(w http.ResponseWriter, r *http.Request, sess *session) {
	s.sweepTokens()
	cond, args := `t.revoked_at = 0`, []any{}
	if sess.User.Role != roleOwner {
		cond, args = cond+` AND t.user_id = ?`, append(args, sess.User.ID)
	}
	list, err := s.tokensWhere(cond, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	out := make([]tokenView, 0, len(list))
	for _, t := range list {
		out = append(out, viewToken(t, sess))
	}
	writeJSON(w, http.StatusOK, out)
}

func validTokenName(name string) error {
	if n := utf8.RuneCountInString(name); n == 0 || n > maxTokenName {
		return fmt.Errorf("Give the token a name of up to %d characters, such as \"Claude on my laptop\".", maxTokenName)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return errors.New("The token's name can't contain line breaks or other control characters.")
		}
	}
	return nil
}

func validTokenServers(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("Choose at least one server, or all servers.")
	}
	if len(ids) > maxTokenServers {
		return nil, fmt.Errorf("A token can be for at most %d servers; choose all servers instead.", maxTokenServers)
	}
	var out []string
	for _, id := range ids {
		if !reMachineID.MatchString(id) {
			return nil, errors.New("Invalid server id.")
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) hTokenCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Name       string   `json:"name"`
		Role       string   `json:"role"`
		AllServers bool     `json:"allServers"`
		Servers    []string `json:"servers"`
		Days       int      `json:"days"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validTokenName(name); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	if req.Days == 0 {
		req.Days = defaultTokenDays
	}
	if !slices.Contains(tokenDays, req.Days) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "A token can last 30, 60, 90 or 365 days.", "")
		return
	}
	if roleRank(req.Role) == 0 {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Choose what the token can do: viewer, moderator or admin.", "")
		return
	}
	want := rights{role: req.Role, all: req.AllServers}
	switch {
	case req.AllServers && len(req.Servers) > 0:
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Send either all servers or a list of servers, not both.", "")
		return
	case !req.AllServers:
		servers, err := validTokenServers(req.Servers)
		if err != nil {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
			return
		}
		want.servers = servers
	}
	if !want.within(accountRights(sess.User.Role)) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "A token can't do more than your account can.", "")
		return
	}
	now := s.now()
	t := apiToken{rights: want, ID: randomID(), UserID: sess.User.ID, Username: sess.User.Username, AccountRole: sess.User.Role, Name: name,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(req.Days) * 24 * time.Hour)}
	secret := tokenPrefix + randomToken(tokenSecretBytes)
	res, err := s.db.Exec(`INSERT INTO api_tokens(id, user_id, name, token_hash, role, servers, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked_at = 0 AND expires_at > ?) < ?
		AND NOT EXISTS (SELECT 1 FROM api_tokens WHERE user_id = ? AND revoked_at = 0 AND name = ? COLLATE NOCASE)`,
		t.ID, t.UserID, t.Name, tokenHash(secret), t.role, encodeServers(t.rights), now.UnixMilli(), t.ExpiresAt.UnixMilli(),
		t.UserID, now.UnixMilli(), maxTokens, t.UserID, t.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not create the token.", "")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.tokenWhere(`t.user_id = ? AND t.revoked_at = 0 AND t.name = ? COLLATE NOCASE`, t.UserID, t.Name); err == nil {
			writeErr(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("You already have a token called %q.", t.Name),
				"Give each tool its own token and name, such as \"Claude on my laptop\".")
			return
		}
		writeErr(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("You have %d working tokens, the most one account can have.", maxTokens),
			"Revoke one you no longer use first.")
		return
	}
	servers := "all servers"
	if !t.all {
		servers = strings.Join(t.servers, ", ")
	}
	s.audit(sess.User.Username, "token.create", t.Name, "succeeded", t.role+" · "+servers+" · runs out "+t.ExpiresAt.UTC().Format("2006-01-02"))
	writeJSON(w, http.StatusCreated, map[string]any{"token": viewToken(t, sess), "secret": secret})
}

// hTokenRevoke revokes a token of one's own, or anyone's for an owner, and
// ends its MCP sessions at once.
func (s *Server) hTokenRevoke(w http.ResponseWriter, r *http.Request, sess *session) {
	id := r.PathValue("tid")
	if !reMachineID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid token id.", "")
		return
	}
	t, err := s.tokenWhere(`t.id = ? AND t.revoked_at = 0`, id)
	if err == nil && sess.User.Role != roleOwner && t.UserID != sess.User.ID {
		err = sql.ErrNoRows
	}
	switch {
	case isNoRows(err):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Token not found.", "")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if !s.revokeToken(t, sess.User.Username, "") {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Token not found.", "")
		return
	}
	s.closeTokenSessions(t.ID)
	w.WriteHeader(http.StatusNoContent)
}

type agentActivityView struct {
	TokenID    string    `json:"tokenId"`
	TokenName  string    `json:"tokenName"`
	Tool       string    `json:"tool"`
	ServerID   string    `json:"serverId,omitempty"`
	ServerName string    `json:"serverName,omitempty"`
	Count      int       `json:"count"`
	At         time.Time `json:"at"`
}

// hTokenActivity is "What agents did lately": the newest things tokens did,
// every account's for an owner.
func (s *Server) hTokenActivity(w http.ResponseWriter, r *http.Request, sess *session) {
	q, args := `SELECT a.token_id, t.name, a.tool, a.server_id, a.server_name, a.count, a.ts FROM agent_activity a JOIN api_tokens t ON t.id = a.token_id`, []any{}
	if sess.User.Role != roleOwner {
		q, args = q+` WHERE t.user_id = ?`, append(args, sess.User.ID)
	}
	rows, err := s.db.Query(q+` ORDER BY a.ts DESC, a.id DESC LIMIT 50`, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	out := []agentActivityView{}
	for rows.Next() {
		var a agentActivityView
		var ts int64
		if rows.Scan(&a.TokenID, &a.TokenName, &a.Tool, &a.ServerID, &a.ServerName, &a.Count, &ts) == nil {
			a.At = fromMillis(ts)
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// actorNames are the names of the actors tokens act as, revoked ones too,
// so the activity and audit logs can show them.
func (s *Server) actorNames() map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT id, name FROM api_tokens`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[tokenActor(id)] = name
		}
	}
	return out
}

// actorInfo says who an actor is when it isn't an account: an API token
// (kind "token", with its name) or root on the command line running
// "playkeeper mcp" (kind "cli", with the account that ran sudo).
func actorInfo(actor string, tokens map[string]string) (kind, name string) {
	if n, ok := tokens[actor]; ok {
		return "token", n
	}
	if u, ok := strings.CutPrefix(actor, "cli:"); ok && u != "" {
		return "cli", u
	}
	return "", ""
}
