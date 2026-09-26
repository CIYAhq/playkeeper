package panel

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/totp"
	"github.com/CIYAhq/playkeeper/internal/twofactor"
)

// Two-factor sign-in: internal/twofactor holds the rules; this file stores
// each user's factor in user_factors and runs the second sign-in step.

const (
	pendingCookieName = "__Host-playkeeper-2fa"
	// pendingTTL is how long the second step waits after a correct password.
	pendingTTL = 5 * time.Minute
	// pendingAttempts is how many codes one correct password may try; then
	// the password is needed again.
	pendingAttempts = 10
)

var errFactorRace = errors.New("two-factor sign-in changed during this request")

// querier is the database, or the one connection that holds
// changeFactor's write transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func msTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func nullTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return msTime(v.Int64)
}

func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// loadFactor reads the user's authenticator app, and the id hash of the
// session that started its setup while that is not confirmed; ok is false
// when there is no row, which means two-factor sign-in is off.
func loadFactor(ctx context.Context, q querier, userID int64) (f twofactor.Factor, setupSession string, ok bool, err error) {
	var secret, hashes string
	var created int64
	var confirmed, lastUsed, recoveryCreated, lockedUntil sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT secret, created_at, setup_session, confirmed_at, last_step, last_used_at, recovery_hashes, recovery_created_at,
		failures, recovery_failures, locked_until, revision
		FROM user_factors WHERE user_id = ? AND kind = 'totp'`, userID).
		Scan(&secret, &created, &setupSession, &confirmed, &f.LastStep, &lastUsed, &hashes, &recoveryCreated,
			&f.Failures, &f.RecoveryFailures, &lockedUntil, &f.Revision)
	if isNoRows(err) {
		return twofactor.Factor{}, "", false, nil
	}
	if err != nil {
		return twofactor.Factor{}, "", false, err
	}
	if f.Secret, err = totp.ParseSecret(secret); err != nil {
		return twofactor.Factor{}, "", false, fmt.Errorf("stored two-factor secret: %w", err)
	}
	if err := json.Unmarshal([]byte(hashes), &f.Recovery.Hashes); err != nil {
		return twofactor.Factor{}, "", false, fmt.Errorf("stored recovery codes: %w", err)
	}
	f.CreatedAt, f.ConfirmedAt, f.LastUsedAt = msTime(created), nullTime(confirmed), nullTime(lastUsed)
	f.Recovery.CreatedAt, f.LockedUntil = nullTime(recoveryCreated), nullTime(lockedUntil)
	return f, setupSession, true, nil
}

// storeFactor saves next over the row loaded at revision old and returns
// errFactorRace if the row is no longer at old. A next with no secret is
// what Disable returns and deletes the row. A setup not yet confirmed is
// stored as setupSession's, the session that started it.
func storeFactor(ctx context.Context, q querier, userID, old int64, exists bool, next twofactor.Factor, setupSession string) error {
	var res sql.Result
	var err error
	switch {
	case next.Secret.IsZero():
		res, err = q.ExecContext(ctx, `DELETE FROM user_factors WHERE user_id = ? AND kind = 'totp' AND revision = ?`, userID, old)
	default:
		if next.On() {
			setupSession = ""
		}
		hashes := next.Recovery.Hashes
		if hashes == nil {
			hashes = []string{}
		}
		b, jerr := json.Marshal(hashes)
		if jerr != nil {
			return jerr
		}
		args := []any{next.Secret.Base32(), next.CreatedAt.UnixMilli(), setupSession, nullMS(next.ConfirmedAt), next.LastStep, nullMS(next.LastUsedAt),
			string(b), nullMS(next.Recovery.CreatedAt), next.Failures, next.RecoveryFailures, nullMS(next.LockedUntil), next.Revision}
		if exists {
			res, err = q.ExecContext(ctx, `UPDATE user_factors SET secret = ?, created_at = ?, setup_session = ?, confirmed_at = ?, last_step = ?, last_used_at = ?,
				recovery_hashes = ?, recovery_created_at = ?, failures = ?, recovery_failures = ?, locked_until = ?, revision = ?
				WHERE user_id = ? AND kind = 'totp' AND revision = ?`, append(args, userID, old)...)
		} else {
			res, err = q.ExecContext(ctx, `INSERT INTO user_factors(secret, created_at, setup_session, confirmed_at, last_step, last_used_at,
				recovery_hashes, recovery_created_at, failures, recovery_failures, locked_until, revision, user_id, kind)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'totp') ON CONFLICT DO NOTHING`, append(args, userID)...)
		}
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cmp.Or(err, errFactorRace)
	}
	return nil
}

// visibleTo is f as the session with id hash idHash may see it. Until a
// setup is confirmed nothing but the session protects its secret, so only
// the session that started it can show or confirm it; to others two-factor
// sign-in is off, and starting again, which needs the password, replaces it.
func visibleTo(f twofactor.Factor, setupSession, idHash string) twofactor.Factor {
	if f.On() || setupSession == idHash {
		return f
	}
	return twofactor.Factor{Revision: f.Revision}
}

// factorFor reads the user's factor as the session idHash may see it.
func (s *Server) factorFor(userID int64, idHash string) (twofactor.Factor, error) {
	f, setupSession, _, err := loadFactor(context.Background(), s.db, userID)
	if err != nil {
		return twofactor.Factor{}, err
	}
	return visibleTo(f, setupSession, idHash), nil
}

// changeFactor runs step on the user's factor as the session idHash may see
// it, and stores what step returns when that changed, in one write
// transaction on its own connection: concurrent requests take turns, each
// seeing what the one before stored, so a code works once and every code
// checked is counted. step's own error comes back after the store, since a
// wrong code counts even though the step failed.
func (s *Server) changeFactor(userID int64, idHash string, step func(f twofactor.Factor, exists bool) (twofactor.Factor, error)) (twofactor.Factor, error) {
	return s.changeFactorWith(userID, idHash, nil, step, nil)
}

// changeFactorWith is changeFactor with more work in its transaction: claim
// runs first, and its error ends the transaction before the factor is read;
// passed runs once step succeeded and what it returned is stored, and its
// error undoes all of it.
func (s *Server) changeFactorWith(userID int64, idHash string, claim func(ctx context.Context, q querier) error, step func(f twofactor.Factor, exists bool) (twofactor.Factor, error), passed func(ctx context.Context, q querier) error) (twofactor.Factor, error) {
	// Not the request's context: a client that goes away must not cut the
	// transaction short and hand the connection back inside it.
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return twofactor.Factor{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return twofactor.Factor{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	if claim != nil {
		if err := claim(ctx, conn); err != nil {
			return twofactor.Factor{}, err
		}
	}
	stored, setupSession, exists, err := loadFactor(ctx, conn, userID)
	if err != nil {
		return twofactor.Factor{}, err
	}
	f := visibleTo(stored, setupSession, idHash)
	next, stepErr := step(f, exists)
	if next.Revision != f.Revision || (exists && next.Secret.IsZero() && !f.Secret.IsZero()) {
		if err := storeFactor(ctx, conn, userID, stored.Revision, exists, next, idHash); err != nil {
			return f, err
		}
	}
	if stepErr == nil && passed != nil {
		if err := passed(ctx, conn); err != nil {
			return f, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return f, err
	}
	committed = true
	return next, stepErr
}

// accountName is how the authenticator app lists this dashboard:
// username@host with the host name or IPv4 address the page was opened at,
// or just the username at an IPv6 address, whose colons the label cannot
// hold.
func accountName(username, hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || len(host) > 96 {
		return username
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return username
		}
	}
	return username + "@" + host
}

// factorError writes a refused two-factor step. At the second sign-in step
// a wrong code is 401, like a wrong password; once signed in it is 403, so
// the page does not take it for an ended session.
func factorError(w http.ResponseWriter, err error, signingIn bool) {
	var fe *twofactor.Error
	if !errors.As(err, &fe) {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Two-factor sign-in did not work. Try again.", "")
		return
	}
	status := http.StatusForbidden
	switch fe.Kind {
	case twofactor.KindCodeMalformed, twofactor.KindAppCodeMalformed:
		status = http.StatusBadRequest
	case twofactor.KindCodeWrong, twofactor.KindCodeReused, twofactor.KindRecoveryCodeWrong:
		if signingIn {
			status = http.StatusUnauthorized
		}
	case twofactor.KindAppCodesLocked:
		w.Header().Set("Retry-After", strconv.Itoa(int((fe.RetryAfter+time.Second-1)/time.Second)))
		status = http.StatusTooManyRequests
	case twofactor.KindAppCodesBlocked, twofactor.KindPasswordWrong:
	case twofactor.KindNoSetup, twofactor.KindSetupExpired, twofactor.KindOff, twofactor.KindOn:
		status = http.StatusConflict
	}
	writeErr(w, status, string(fe.Kind), fe.Error(), fe.Hint())
}

// failureDetail is the audit detail for a refused code: the kind, and how
// many wrong codes are now in a row when this one was counted.
func failureDetail(err error, before, after twofactor.Factor) string {
	d := string(twofactor.KindOf(err))
	if d == "" {
		d = "error"
	}
	switch {
	case after.Failures > before.Failures:
		d += fmt.Sprintf(", %d in a row", after.Failures)
	case after.RecoveryFailures > before.RecoveryFailures:
		d += fmt.Sprintf(", %d in a row", after.RecoveryFailures)
	}
	return d
}

// --- pending second step ---

// newPendingLogin records a sign-in that passed the password. It has its own
// table, which only the second-step routes read, so its token is not a
// session in any cookie.
func (s *Server) newPendingLogin(u user) (token string, expires time.Time, err error) {
	token = randomToken(32)
	now := s.now()
	expires = now.Add(pendingTTL)
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE expires_at <= ?`, now.UnixMilli())
	_, err = s.db.Exec(`INSERT INTO pending_logins(id_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)`,
		tokenHash(token), u.ID, now.UnixMilli(), expires.UnixMilli())
	return token, expires, err
}

// pendingFrom resolves the second-step cookie to a password-checked user
// who has not passed the second step yet. The session it returns has no
// CSRF token and must never be treated as signed in.
func (s *Server) pendingFrom(r *http.Request) (session, error) {
	c, err := r.Cookie(pendingCookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return session{}, errNoSession
	}
	var sess session
	var expires int64
	err = s.db.QueryRow(`SELECT p.id_hash, p.expires_at, u.id, u.username, u.role
		FROM pending_logins p JOIN users u ON u.id = p.user_id WHERE p.id_hash = ?`, tokenHash(c.Value)).
		Scan(&sess.IDHash, &expires, &sess.User.ID, &sess.User.Username, &sess.User.Role)
	if err != nil {
		return session{}, errNoSession
	}
	sess.ExpiresAt = time.UnixMilli(expires)
	if !s.now().Before(sess.ExpiresAt) {
		s.endPendingLogin(sess.IDHash)
		return session{}, errNoSession
	}
	return sess, nil
}

var (
	errPendingGone   = errors.New("the sign-in no longer waits for its second step")
	errPendingUsedUp = errors.New("the sign-in tried all the codes it may")
)

// usePendingAttempt claims the pending sign-in for one code and counts the
// code against the ones it may try. errPendingGone means another request
// passed or ended it first, errPendingUsedUp that no codes are left.
func usePendingAttempt(ctx context.Context, q querier, idHash string) error {
	res, err := q.ExecContext(ctx, `UPDATE pending_logins SET attempts = attempts + 1 WHERE id_hash = ? AND attempts < ?`, idHash, pendingAttempts)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pending_logins WHERE id_hash = ?)`, idHash).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errPendingGone
	}
	return errPendingUsedUp
}

// passPendingLogin ends the pending sign-in p and starts the session it
// earned, in the transaction that checked its code.
func (s *Server) passPendingLogin(ctx context.Context, q querier, p *session) (string, session, error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM pending_logins WHERE id_hash = ?`, p.IDHash); err != nil {
		return "", session{}, err
	}
	return s.newSessionIn(ctx, q, p.User)
}

// endPendingLogin deletes the pending sign-in.
func (s *Server) endPendingLogin(idHash string) {
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE id_hash = ?`, idHash)
}

func setPendingCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(pendingTTL.Seconds())})
}

func clearPendingCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// secondFactorNeeded starts the second step after a correct password, or
// reports false when the user has two-factor sign-in off.
func (s *Server) secondFactorNeeded(w http.ResponseWriter, u user) (bool, error) {
	f, _, ok, err := loadFactor(context.Background(), s.db, u.ID)
	if err != nil || !ok || !f.On() {
		return false, err
	}
	token, expires, err := s.newPendingLogin(u)
	if err != nil {
		return false, err
	}
	s.audit(u.Username, "login.password", "panel", "succeeded", "second factor needed")
	setPendingCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"secondFactor": f.Challenge(s.now()),
		"user":         map[string]string{"username": u.Username},
		"expiresAt":    expires.UTC(),
	})
	return true, nil
}

func (s *Server) hSecondFactor(w http.ResponseWriter, r *http.Request, p *session) {
	if !s.rateLimitIP(w, r) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	// One transaction claims the pending sign-in, checks the code and
	// starts the session: of two requests with right codes, the one that
	// comes second finds the sign-in gone before its code is spent.
	var before twofactor.Factor
	var res twofactor.Result
	var token string
	var sess session
	sessionFailed := false
	now := s.now()
	if s.beforeCodeCheck != nil {
		s.beforeCodeCheck()
	}
	after, err := s.changeFactorWith(p.User.ID, p.IDHash, func(ctx context.Context, q querier) error {
		return usePendingAttempt(ctx, q, p.IDHash)
	}, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		before = f
		next, result, err := twofactor.SignIn(f, req.Code, now)
		res = result
		return next, err
	}, func(ctx context.Context, q querier) error {
		var err error
		token, sess, err = s.passPendingLogin(ctx, q, p)
		sessionFailed = err != nil
		return err
	})
	switch {
	case errors.Is(err, errPendingUsedUp):
		s.endPendingLogin(p.IDHash)
		s.audit(p.User.Username, "login.second_factor", "panel", "refused", fmt.Sprintf("%d codes tried, password needed again", pendingAttempts))
		clearPendingCookie(w)
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Too many codes for one sign-in. Enter your password again.", "")
		return
	case errors.Is(err, errPendingGone):
		clearPendingCookie(w)
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in again.", "")
		return
	case twofactor.KindOf(err) == twofactor.KindOff:
		s.endPendingLogin(p.IDHash)
		clearPendingCookie(w)
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in again.", "")
		return
	case sessionFailed:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	case err != nil:
		if twofactor.KindOf(err) != "" {
			s.audit(p.User.Username, "login.second_factor", "panel", "failed", failureDetail(err, before, after))
		}
		factorError(w, err, true)
		return
	}
	detail := string(res.Method)
	if res.Method == twofactor.MethodRecoveryCode {
		detail = fmt.Sprintf("%s, %d left", res.Method, after.Recovery.Remaining())
	}
	s.audit(p.User.Username, "login", "panel", "succeeded", detail)
	s.setSessionCookie(w, token)
	clearPendingCookie(w)
	body := s.meBody(sess)
	body["notices"] = res.Notices
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) hSecondFactorCancel(w http.ResponseWriter, r *http.Request, p *session) {
	s.endPendingLogin(p.IDHash)
	clearPendingCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// --- signed-in routes ---

func (s *Server) h2FAStatus(w http.ResponseWriter, r *http.Request, sess *session) {
	f, err := s.factorFor(sess.User.ID, sess.IDHash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, f.Status(s.now()))
}

func (s *Server) h2FASetupStart(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	_, passwordOK := s.authenticate(sess.User.Username, req.Password)
	account := accountName(sess.User.Username, r.Host)
	var setup twofactor.Setup
	_, err := s.changeFactor(sess.User.ID, sess.IDHash, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		next, st, err := twofactor.Begin(f, passwordOK, account, s.now(), rand.Reader)
		setup = st
		return next, err
	})
	if err != nil {
		if k := twofactor.KindOf(err); k != "" {
			s.audit(sess.User.Username, "2fa.setup", "panel", "failed", string(k))
		}
		factorError(w, err, false)
		return
	}
	s.audit(sess.User.Username, "2fa.setup", "panel", "succeeded", "")
	writeJSON(w, http.StatusOK, setup)
}

func (s *Server) h2FASetupShow(w http.ResponseWriter, r *http.Request, sess *session) {
	f, err := s.factorFor(sess.User.ID, sess.IDHash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	setup, err := f.Setup(accountName(sess.User.Username, r.Host), s.now())
	if err != nil {
		factorError(w, err, false)
		return
	}
	writeJSON(w, http.StatusOK, setup)
}

func (s *Server) h2FASetupCancel(w http.ResponseWriter, r *http.Request, sess *session) {
	res, err := s.db.Exec(`DELETE FROM user_factors WHERE user_id = ? AND kind = 'totp' AND confirmed_at IS NULL AND setup_session = ?`, sess.User.ID, sess.IDHash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit(sess.User.Username, "2fa.setup.cancel", "panel", "succeeded", "")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) h2FAConfirm(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	var codes []string
	after, err := s.changeFactor(sess.User.ID, sess.IDHash, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		next, c, err := twofactor.Confirm(f, req.Code, s.now(), rand.Reader)
		codes = c
		return next, err
	})
	if err != nil {
		factorError(w, err, false)
		return
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, sess.User.ID, sess.IDHash)
	s.audit(sess.User.Username, "2fa.enable", "panel", "succeeded", "other sessions signed out")
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes, "status": after.Status(s.now())})
}

type passwordAndCode struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) h2FADisable(w http.ResponseWriter, r *http.Request, sess *session) {
	var req passwordAndCode
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	_, passwordOK := s.authenticate(sess.User.Username, req.Password)
	var before twofactor.Factor
	after, err := s.changeFactor(sess.User.ID, sess.IDHash, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		before = f
		return twofactor.Disable(f, passwordOK, req.Code, s.now())
	})
	if err != nil {
		if twofactor.KindOf(err) != "" {
			s.audit(sess.User.Username, "2fa.disable", "panel", "failed", failureDetail(err, before, after))
		}
		factorError(w, err, false)
		return
	}
	s.audit(sess.User.Username, "2fa.disable", "panel", "succeeded", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) h2FARecoveryCodes(w http.ResponseWriter, r *http.Request, sess *session) {
	var req passwordAndCode
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	_, passwordOK := s.authenticate(sess.User.Username, req.Password)
	var before twofactor.Factor
	var codes []string
	after, err := s.changeFactor(sess.User.ID, sess.IDHash, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		before = f
		next, c, err := twofactor.RenewRecoveryCodes(f, passwordOK, req.Code, s.now(), rand.Reader)
		codes = c
		return next, err
	})
	if err != nil {
		if twofactor.KindOf(err) != "" {
			s.audit(sess.User.Username, "2fa.recovery_codes.renew", "panel", "failed", failureDetail(err, before, after))
		}
		factorError(w, err, false)
		return
	}
	s.audit(sess.User.Username, "2fa.recovery_codes.renew", "panel", "succeeded", "")
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// ResetTwoFactor turns two-factor sign-in off for username from the host
// (root CLI recovery, for a lost phone with no recovery codes left) and
// signs out all of the user's sessions. wasOn is false if it was off.
func (s *Server) ResetTwoFactor(username string) (wasOn bool, err error) {
	u, err := s.userByName(username)
	if isNoRows(err) {
		return false, errors.New("no such user: " + username)
	}
	if err != nil {
		return false, err
	}
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_factors WHERE user_id = ? AND confirmed_at IS NOT NULL)`, u.ID).Scan(&wasOn); err != nil {
		return false, err
	}
	if _, err := s.db.Exec(`DELETE FROM user_factors WHERE user_id = ?`, u.ID); err != nil {
		return false, err
	}
	if !wasOn {
		return false, nil
	}
	s.deleteUserSessions(u.ID)
	s.audit("root@host", "2fa.reset", username, "succeeded", "reset from the server command line")
	return true, nil
}
