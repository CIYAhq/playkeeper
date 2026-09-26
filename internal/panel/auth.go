package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

var panelMigrations = []string{
	`
CREATE TABLE users (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  username            TEXT NOT NULL UNIQUE,
  password_hash       TEXT NOT NULL,
  created_at          INTEGER NOT NULL,
  password_changed_at INTEGER NOT NULL
);
CREATE TABLE sessions (
  id_hash    TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE audit (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  ts     INTEGER NOT NULL,
  actor  TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
`,
	// 0.3.0: projects, machines and roles (decision 0004). Existing accounts
	// are owners of the install.
	`
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'owner';
CREATE TABLE projects (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, user_id)
);
CREATE TABLE machines (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,
  endpoint   TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE user_prefs (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL,
  PRIMARY KEY (user_id, key)
);
CREATE TABLE player_heads (
  name       TEXT PRIMARY KEY,
  status     TEXT NOT NULL,
  png        BLOB,
  fetched_at INTEGER NOT NULL
);
`,
	// 0.4.0, wave 2: two-factor sign-in. Each user's authenticator app, with
	// the session that started a setup not yet confirmed; and sign-ins that
	// passed the password but not yet the second step, kept apart from
	// sessions so they can never be taken for one.
	`
CREATE TABLE user_factors (
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind                TEXT NOT NULL DEFAULT 'totp',
  secret              TEXT NOT NULL,
  created_at          INTEGER NOT NULL,
  setup_session       TEXT NOT NULL DEFAULT '',
  confirmed_at        INTEGER,
  last_step           INTEGER NOT NULL DEFAULT 0,
  last_used_at        INTEGER,
  recovery_hashes     TEXT NOT NULL DEFAULT '[]',
  recovery_created_at INTEGER,
  failures            INTEGER NOT NULL DEFAULT 0,
  recovery_failures   INTEGER NOT NULL DEFAULT 0,
  locked_until        INTEGER,
  revision            INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind)
);
CREATE TABLE pending_logins (
  id_hash    TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0
);
`,
	// Wave 5: invite links for friends and team members, join requests, how
	// players got in, and the servers each team member can use. The servers
	// column fails closed: only the owner starts with all servers. One owner
	// per install, and usernames are unique in any capitalisation.
	// admin_factor is the confirmed_at of the two-factor setup an owner or
	// admin confirmed Admin rights with; factor_seen is the one the
	// dashboard last saw, to notice when it's turned on or off.
	`
CREATE TABLE invites (
  id          TEXT    PRIMARY KEY,
  kind        TEXT    NOT NULL CHECK (kind IN ('player', 'member')),
  code_hash   TEXT    NOT NULL UNIQUE,
  code        TEXT    NOT NULL DEFAULT '',
  project_id  TEXT    NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  server_id   TEXT    NOT NULL DEFAULT '',
  role        TEXT    NOT NULL DEFAULT '',
  servers     TEXT    NOT NULL DEFAULT '',
  approval    TEXT    NOT NULL DEFAULT '',
  label       TEXT    NOT NULL DEFAULT '',
  created_by  INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  max_uses    INTEGER NOT NULL CHECK (max_uses BETWEEN 0 AND 100),
  uses        INTEGER NOT NULL DEFAULT 0,
  revoked_at  INTEGER NOT NULL DEFAULT 0,
  CHECK (kind = 'player' OR (max_uses = 1 AND expires_at > 0))
);
CREATE INDEX invites_server ON invites(server_id, created_at);
CREATE INDEX invites_project ON invites(project_id, kind, created_at);
CREATE TABLE join_requests (
  id          TEXT    PRIMARY KEY,
  invite_id   TEXT    NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
  server_id   TEXT    NOT NULL,
  player_uuid TEXT    NOT NULL,
  player_name TEXT    NOT NULL,
  address     TEXT    NOT NULL DEFAULT '',
  state       TEXT    NOT NULL CHECK (state IN ('pending', 'approved', 'declined')),
  created_at  INTEGER NOT NULL,
  decided_at  INTEGER NOT NULL DEFAULT 0,
  decided_by  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX join_requests_one_pending ON join_requests(server_id, player_uuid) WHERE state = 'pending';
CREATE INDEX join_requests_server ON join_requests(server_id, state, created_at);
CREATE INDEX join_requests_invite ON join_requests(invite_id, state);
CREATE INDEX join_requests_address ON join_requests(address) WHERE state = 'pending';
CREATE TABLE player_origins (
  server_id   TEXT    NOT NULL,
  player_uuid TEXT    NOT NULL,
  player_name TEXT    NOT NULL,
  invite_id   TEXT    NOT NULL,
  request_id  TEXT    NOT NULL DEFAULT '',
  joined_at   INTEGER NOT NULL,
  PRIMARY KEY (server_id, player_uuid)
);
ALTER TABLE project_members ADD COLUMN servers TEXT NOT NULL DEFAULT '';
UPDATE project_members SET servers = '*' WHERE user_id IN (SELECT id FROM users WHERE role = 'owner');
ALTER TABLE project_members ADD COLUMN admin_factor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_members ADD COLUMN factor_seen INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX users_username_nocase ON users(username COLLATE NOCASE);
CREATE UNIQUE INDEX users_one_owner ON users(role) WHERE role = 'owner';
`,
	// Machines that joined over a machine link (kind 'remote'), their join
	// codes (a keyed hash only, never the code), what happened to each, and
	// which machine runs each server with its last known status and the
	// joined machine that also lists it, if any.
	`
ALTER TABLE machines ADD COLUMN public_key BLOB;
ALTER TABLE machines ADD COLUMN joined_from TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN version TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN last_addr TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN revoked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN revoked_by TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX machines_public_key ON machines(public_key) WHERE public_key IS NOT NULL;
CREATE TABLE machine_join_codes (
  id         TEXT PRIMARY KEY,
  hash       BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  used_at    INTEGER NOT NULL DEFAULT 0,
  machine_id TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  dials      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE machine_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id TEXT NOT NULL,
  ts         INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  address    TEXT NOT NULL DEFAULT '',
  code       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX machine_events_machine ON machine_events(machine_id, id);
CREATE TABLE server_machines (
  server_id   TEXT PRIMARY KEY,
  machine_id  TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT '',
  seen_at     INTEGER NOT NULL DEFAULT 0,
  disputed_by TEXT NOT NULL DEFAULT ''
);
`,
	// API tokens for AI agents (a hash only, never the token), with the
	// role and servers each was made for ('*' is every server), and what
	// agents did with them. Revoked tokens stay, so their names still show
	// in the activity log.
	`
CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  role         TEXT NOT NULL,
  servers      TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at   INTEGER NOT NULL DEFAULT 0,
  revoked_by   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);
CREATE TABLE agent_activity (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id    TEXT NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
  ts          INTEGER NOT NULL,
  tool        TEXT NOT NULL,
  server_id   TEXT NOT NULL DEFAULT '',
  server_name TEXT NOT NULL DEFAULT '',
  count       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX agent_activity_token ON agent_activity(token_id, id);
`,
	// What the dashboard remembers about itself, such as the version it last
	// ran.
	`
CREATE TABLE panel_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`,
	// Joins refused lately for their code, with the network each came from,
	// so that a pause after too many outlasts a restart.
	`
CREATE TABLE machine_join_failures (
  at      INTEGER NOT NULL,
  network TEXT NOT NULL
);
`,
	// The role each API token's account held when the token was made (owner,
	// or its project role): a token stops working once its account holds a
	// lower one or leaves the team.
	`
ALTER TABLE api_tokens ADD COLUMN account_role TEXT NOT NULL DEFAULT '';
UPDATE api_tokens SET account_role = COALESCE((
  SELECT CASE WHEN u.role = 'owner' THEN 'owner'
    ELSE (SELECT pm.role FROM project_members pm WHERE pm.user_id = u.id ORDER BY pm.created_at LIMIT 1) END
  FROM users u WHERE u.id = api_tokens.user_id), '');
`,
}

const (
	cookieName = "__Host-playkeeper"
	// argon2id parameters (RFC 9106 second recommended option, lower memory).
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// hashSem bounds concurrent password hashing so login floods cannot exhaust
// memory on a small VPS.
var hashSem = make(chan struct{}, 2)

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSem
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || m > 1<<20 || t > 10 || p == 0 {
		return false
	}
	hashSem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	<-hashSem
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash equalises timing for unknown usernames.
var dummyHash, _ = hashPassword("playkeeper-timing-equaliser")

// The account rules live in the invites package, so the join page and the
// panel can't drift apart.
func validUsername(u string) error { return invites.ValidUsername(u) }

func validPassword(pw, username string) error { return invites.ValidPassword(pw, username) }

// RandomPassword returns a readable 20-character password for CLI resets.
func RandomPassword() string {
	raw := make([]byte, 13)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return base32NoPad(raw)[:20]
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

type user struct {
	ID       int64
	Username string
	// Role is the account's role on the install: owner, or member (only what
	// its project memberships grant).
	Role string
}

type session struct {
	IDHash    string
	User      user
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
	// Access is what the account may do, read by guard for each request.
	Access access
}

func (s *Server) userCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

var errSetupDone = errors.New("an admin account already exists")

// createFirstAdmin inserts the account only if there is none, in a single
// statement, so setups racing with the same code cannot create two admins.
func (s *Server) createFirstAdmin(username, password string) (user, error) {
	h, err := hashPassword(password)
	if err != nil {
		return user{}, err
	}
	now := s.now().UnixMilli()
	res, err := s.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, h, now, now, roleOwner)
	if err != nil {
		return user{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return user{}, errSetupDone
	}
	id, _ := res.LastInsertId()
	s.ensureOwnerMember(id)
	return user{ID: id, Username: username, Role: roleOwner}, nil
}

// authenticate verifies credentials with constant work for unknown users.
func (s *Server) authenticate(username, password string) (user, bool) {
	var u user
	var h string
	err := s.db.QueryRow(`SELECT id, username, role, password_hash FROM users WHERE username = ?`, username).Scan(&u.ID, &u.Username, &u.Role, &h)
	if err != nil {
		checkPassword(dummyHash, password)
		return user{}, false
	}
	return u, checkPassword(h, password)
}

func (s *Server) newSession(u user) (token string, sess session, err error) {
	return s.newSessionIn(context.Background(), s.db, u)
}

// newSessionIn is newSession through q, which may hold a transaction.
func (s *Server) newSessionIn(ctx context.Context, q querier, u user) (token string, sess session, err error) {
	token = randomToken(32)
	now := s.now()
	sess = session{IDHash: tokenHash(token), User: u, CSRF: randomToken(32), CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(s.opts.AbsoluteTimeout)}
	_, err = q.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen, expires_at) VALUES(?,?,?,?,?,?)`,
		sess.IDHash, u.ID, sess.CSRF, now.UnixMilli(), now.UnixMilli(), sess.ExpiresAt.UnixMilli())
	return token, sess, err
}

var errNoSession = errors.New("no valid session")

// lookupSession resolves a cookie token. Expired (idle or absolute) sessions
// are deleted and rejected. Any database error rejects the request.
func (s *Server) lookupSession(token string) (session, error) {
	if token == "" || len(token) > 128 {
		return session{}, errNoSession
	}
	var sess session
	var created, lastSeen, expires int64
	err := s.db.QueryRow(`SELECT s.id_hash, s.csrf, s.created_at, s.last_seen, s.expires_at, u.id, u.username, u.role
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, tokenHash(token)).
		Scan(&sess.IDHash, &sess.CSRF, &created, &lastSeen, &expires, &sess.User.ID, &sess.User.Username, &sess.User.Role)
	if err != nil {
		return session{}, errNoSession
	}
	sess.CreatedAt, sess.LastSeen, sess.ExpiresAt = time.UnixMilli(created), time.UnixMilli(lastSeen), time.UnixMilli(expires)
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeen) >= s.opts.IdleTimeout {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, sess.IDHash)
		return session{}, errNoSession
	}
	if now.Sub(sess.LastSeen) > time.Minute {
		if _, err := s.db.Exec(`UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, now.UnixMilli(), sess.IDHash); err != nil {
			return session{}, errNoSession
		}
	}
	return sess, nil
}

func (s *Server) deleteSession(idHash string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, idHash)
}

// deleteUserSessions signs the user out everywhere, including sign-ins
// waiting for their second step.
func (s *Server) deleteUserSessions(userID int64) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE user_id = ?`, userID)
}

// pruneAuditEvery is how many audit rows are written between prunes.
const pruneAuditEvery = 1000

func (s *Server) audit(actor, action, target, result, detail string) {
	s.writeAudit(auditRow{at: s.now(), actor: actor, action: action, target: target, result: result, detail: detail})
}

func (s *Server) writeAudit(r auditRow) {
	if _, err := s.db.Exec(`INSERT INTO audit(ts, actor, action, target, result, detail) VALUES(?,?,?,?,?,?)`,
		r.at.UnixMilli(), r.actor, r.action, r.target, r.result, r.detail); err != nil {
		s.log.Error("audit write failed", "err", err)
		return
	}
	if s.audits.Add(1)%pruneAuditEvery == 0 {
		s.pruneAudit()
	}
}

// pruneAudit drops audit rows older than auditMaxAge and all but the newest
// maxAudit, and API tokens that stopped working longer ago than that, whose
// names the log no longer needs.
func (s *Server) pruneAudit() {
	cutoff := s.now().Add(-s.auditMaxAge).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM audit WHERE ts < ?`, cutoff); err != nil {
		s.log.Error("audit prune failed", "err", err)
	}
	if _, err := s.db.Exec(`DELETE FROM audit WHERE id <= (SELECT id FROM audit ORDER BY id DESC LIMIT 1 OFFSET ?)`, s.maxAudit); err != nil {
		s.log.Error("audit prune failed", "err", err)
	}
	if _, err := s.db.Exec(`DELETE FROM api_tokens WHERE (revoked_at != 0 AND revoked_at < ?) OR expires_at < ?`, cutoff, cutoff); err != nil {
		s.log.Error("prune API tokens", "err", err)
	}
}

// SetupToken is the one-time first-run secret written by the installer. Only
// its SHA-256 is stored on disk.
type SetupToken struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// NewSetupToken writes a fresh token hash to path and returns the token.
func NewSetupToken(path string, ttl time.Duration, now time.Time) (string, error) {
	raw := make([]byte, 15)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := strings.ToLower(base32NoPad(raw))
	grouped := tok[0:6] + "-" + tok[6:12] + "-" + tok[12:18] + "-" + tok[18:24]
	b, _ := json.Marshal(SetupToken{Hash: tokenHash(grouped), ExpiresAt: now.Add(ttl).UTC()})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return grouped, os.Rename(tmp, path)
}

func base32NoPad(b []byte) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	var out strings.Builder
	var buf uint64
	bits := 0
	for _, c := range b {
		buf = buf<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			out.WriteByte(alphabet[(buf>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		out.WriteByte(alphabet[(buf<<(5-bits))&31])
	}
	return out.String()
}

// checkSetupToken validates a presented token without consuming it.
func (s *Server) checkSetupToken(presented string) error {
	b, err := os.ReadFile(s.cfg.SetupTokenPath())
	if err != nil {
		return errors.New("No setup code is active. Run `sudo playkeeper setup-code` on the server to create one.")
	}
	var t SetupToken
	if err := json.Unmarshal(b, &t); err != nil {
		return errors.New("The setup code file is damaged. Run `sudo playkeeper setup-code` to create a new one.")
	}
	if !s.now().Before(t.ExpiresAt) {
		return errors.New("This setup code has expired. Run `sudo playkeeper setup-code` on the server to create a new one.")
	}
	got := tokenHash(strings.ToLower(strings.TrimSpace(presented)))
	if subtle.ConstantTimeCompare([]byte(got), []byte(t.Hash)) != 1 {
		return errors.New("That setup code is not correct.")
	}
	return nil
}

func (s *Server) consumeSetupToken() {
	os.Remove(s.cfg.SetupTokenPath())
}

var errDB = errors.New("database error")

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
