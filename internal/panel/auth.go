package panel

import (
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
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
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

func validUsername(u string) error {
	if l := utf8.RuneCountInString(u); l < 3 || l > 32 {
		return errors.New("Username must be 3–32 characters.")
	}
	for _, r := range u {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.') {
			return errors.New("Username may contain letters, numbers, dot, dash and underscore.")
		}
	}
	return nil
}

func validPassword(pw, username string) error {
	if utf8.RuneCountInString(pw) < 10 {
		return errors.New("Password must be at least 10 characters.")
	}
	if len(pw) > 256 {
		return errors.New("Password must be at most 256 bytes.")
	}
	if strings.EqualFold(pw, username) {
		return errors.New("Password must differ from the username.")
	}
	return nil
}

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
}

type session struct {
	IDHash    string
	User      user
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
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
	res, err := s.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, h, now, now)
	if err != nil {
		return user{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return user{}, errSetupDone
	}
	id, _ := res.LastInsertId()
	return user{ID: id, Username: username}, nil
}

// authenticate verifies credentials with constant work for unknown users.
func (s *Server) authenticate(username, password string) (user, bool) {
	var u user
	var h string
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`, username).Scan(&u.ID, &u.Username, &h)
	if err != nil {
		checkPassword(dummyHash, password)
		return user{}, false
	}
	return u, checkPassword(h, password)
}

func (s *Server) newSession(u user) (token string, sess session, err error) {
	token = randomToken(32)
	now := s.now()
	sess = session{IDHash: tokenHash(token), User: u, CSRF: randomToken(32), CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(s.opts.AbsoluteTimeout)}
	_, err = s.db.Exec(`INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen, expires_at) VALUES(?,?,?,?,?,?)`,
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
	err := s.db.QueryRow(`SELECT s.id_hash, s.csrf, s.created_at, s.last_seen, s.expires_at, u.id, u.username
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, tokenHash(token)).
		Scan(&sess.IDHash, &sess.CSRF, &created, &lastSeen, &expires, &sess.User.ID, &sess.User.Username)
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

func (s *Server) deleteUserSessions(userID int64) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
}

func (s *Server) audit(actor, action, target, result, detail string) {
	if _, err := s.db.Exec(`INSERT INTO audit(ts, actor, action, target, result, detail) VALUES(?,?,?,?,?,?)`,
		s.now().UnixMilli(), actor, action, target, result, detail); err != nil {
		s.log.Error("audit write failed", "err", err)
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
