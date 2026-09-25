package panel

import (
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
	// stageSecondFactor marks a sessions row that passed the password only;
	// lookupSession accepts only stage 'full'.
	stageSecondFactor = "second_factor"
)

var errFactorRace = errors.New("two-factor sign-in changed during this request")

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

// loadFactor reads the user's authenticator app; ok is false when there is
// no row, which means two-factor sign-in is off.
func (s *Server) loadFactor(userID int64) (f twofactor.Factor, ok bool, err error) {
	var secret, hashes string
	var created int64
	var confirmed, lastUsed, recoveryCreated, lockedUntil sql.NullInt64
	err = s.db.QueryRow(`SELECT secret, created_at, confirmed_at, last_step, last_used_at, recovery_hashes, recovery_created_at, failures, locked_until, revision
		FROM user_factors WHERE user_id = ? AND kind = 'totp'`, userID).
		Scan(&secret, &created, &confirmed, &f.LastStep, &lastUsed, &hashes, &recoveryCreated, &f.Failures, &lockedUntil, &f.Revision)
	if isNoRows(err) {
		return twofactor.Factor{}, false, nil
	}
	if err != nil {
		return twofactor.Factor{}, false, err
	}
	if f.Secret, err = totp.ParseSecret(secret); err != nil {
		return twofactor.Factor{}, false, fmt.Errorf("stored two-factor secret: %w", err)
	}
	if err := json.Unmarshal([]byte(hashes), &f.Recovery.Hashes); err != nil {
		return twofactor.Factor{}, false, fmt.Errorf("stored recovery codes: %w", err)
	}
	f.CreatedAt, f.ConfirmedAt, f.LastUsedAt = msTime(created), nullTime(confirmed), nullTime(lastUsed)
	f.Recovery.CreatedAt, f.LockedUntil = nullTime(recoveryCreated), nullTime(lockedUntil)
	return f, true, nil
}

// storeFactor saves next in place of old only if the stored revision is
// still old's, so two requests cannot both use one code; stored is false
// when another request changed it first. A next with no secret is what
// Disable returns and deletes the row.
func (s *Server) storeFactor(userID int64, old twofactor.Factor, exists bool, next twofactor.Factor) (stored bool, err error) {
	var res sql.Result
	switch {
	case next.Secret.IsZero():
		res, err = s.db.Exec(`DELETE FROM user_factors WHERE user_id = ? AND kind = 'totp' AND revision = ?`, userID, old.Revision)
	default:
		hashes := next.Recovery.Hashes
		if hashes == nil {
			hashes = []string{}
		}
		b, jerr := json.Marshal(hashes)
		if jerr != nil {
			return false, jerr
		}
		args := []any{next.Secret.Base32(), next.CreatedAt.UnixMilli(), nullMS(next.ConfirmedAt), next.LastStep, nullMS(next.LastUsedAt),
			string(b), nullMS(next.Recovery.CreatedAt), next.Failures, nullMS(next.LockedUntil), next.Revision}
		if exists {
			res, err = s.db.Exec(`UPDATE user_factors SET secret = ?, created_at = ?, confirmed_at = ?, last_step = ?, last_used_at = ?,
				recovery_hashes = ?, recovery_created_at = ?, failures = ?, locked_until = ?, revision = ?
				WHERE user_id = ? AND kind = 'totp' AND revision = ?`, append(args, userID, old.Revision)...)
		} else {
			res, err = s.db.Exec(`INSERT INTO user_factors(secret, created_at, confirmed_at, last_step, last_used_at,
				recovery_hashes, recovery_created_at, failures, locked_until, revision, user_id, kind)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,'totp') ON CONFLICT DO NOTHING`, append(args, userID)...)
		}
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// changeFactor runs step on the stored factor and stores what it returns
// when that changed. If another request changed the factor meanwhile, it
// reloads and runs step once more. step's own error comes back after the
// store, since a wrong code is counted even though the step failed.
func (s *Server) changeFactor(userID int64, step func(f twofactor.Factor, exists bool) (twofactor.Factor, error)) (twofactor.Factor, error) {
	for try := 0; ; try++ {
		f, exists, err := s.loadFactor(userID)
		if err != nil {
			return f, err
		}
		next, stepErr := step(f, exists)
		if next.Revision != f.Revision || (exists && next.Secret.IsZero()) {
			stored, err := s.storeFactor(userID, f, exists, next)
			if err != nil {
				return f, err
			}
			if !stored {
				if try == 0 {
					continue
				}
				return f, errFactorRace
			}
		}
		return next, stepErr
	}
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
	if after.Failures > before.Failures {
		d += fmt.Sprintf(", %d in a row", after.Failures)
	}
	return d
}

// --- pending second step ---

func (s *Server) newPendingSession(u user) (token string, expires time.Time, err error) {
	token = randomToken(32)
	now := s.now()
	expires = now.Add(pendingTTL)
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE stage = ? AND expires_at <= ?`, stageSecondFactor, now.UnixMilli())
	_, err = s.db.Exec(`INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen, expires_at, stage) VALUES(?,?,'',?,?,?,?)`,
		tokenHash(token), u.ID, now.UnixMilli(), now.UnixMilli(), expires.UnixMilli(), stageSecondFactor)
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
	err = s.db.QueryRow(`SELECT s.id_hash, s.expires_at, u.id, u.username, u.role
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ? AND s.stage = ?`, tokenHash(c.Value), stageSecondFactor).
		Scan(&sess.IDHash, &expires, &sess.User.ID, &sess.User.Username, &sess.User.Role)
	if err != nil {
		return session{}, errNoSession
	}
	sess.ExpiresAt = time.UnixMilli(expires)
	if !s.now().Before(sess.ExpiresAt) {
		s.deleteSession(sess.IDHash)
		return session{}, errNoSession
	}
	return sess, nil
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
	f, ok, err := s.loadFactor(u.ID)
	if err != nil || !ok || !f.On() {
		return false, err
	}
	token, expires, err := s.newPendingSession(u)
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
	var before twofactor.Factor
	var res twofactor.Result
	now := s.now()
	after, err := s.changeFactor(p.User.ID, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
		before = f
		next, result, err := twofactor.SignIn(f, req.Code, now)
		res = result
		return next, err
	})
	switch {
	case twofactor.KindOf(err) == twofactor.KindOff:
		s.deleteSession(p.IDHash)
		clearPendingCookie(w)
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in again.", "")
		return
	case err != nil:
		if twofactor.KindOf(err) != "" {
			s.audit(p.User.Username, "login.second_factor", "panel", "failed", failureDetail(err, before, after))
		}
		factorError(w, err, true)
		return
	}
	s.deleteSession(p.IDHash)
	token, sess, err := s.newSession(p.User)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
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
	s.deleteSession(p.IDHash)
	clearPendingCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// --- signed-in routes ---

func (s *Server) h2FAStatus(w http.ResponseWriter, r *http.Request, sess *session) {
	f, _, err := s.loadFactor(sess.User.ID)
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
	_, err := s.changeFactor(sess.User.ID, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
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
	f, _, err := s.loadFactor(sess.User.ID)
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
	res, err := s.db.Exec(`DELETE FROM user_factors WHERE user_id = ? AND kind = 'totp' AND confirmed_at IS NULL`, sess.User.ID)
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
	after, err := s.changeFactor(sess.User.ID, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
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
	after, err := s.changeFactor(sess.User.ID, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
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
	after, err := s.changeFactor(sess.User.ID, func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
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
