// Package panel is Playkeeper's HTTPS web panel. It runs as an unprivileged
// service account, has no Docker access, and forwards validated, authenticated
// requests to the local agent socket.
package panel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/store"
	"github.com/CIYAhq/playkeeper/internal/version"
)

type Options struct {
	Config          config.Config
	Logger          *slog.Logger
	Now             func() time.Time
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// Static is the built UI (web/dist); nil serves a "UI not built" page.
	Static fs.FS
	Agent  *agentclient.Client
	// Heads are where player faces come from (default: Mojang).
	Heads *HeadSources
}

type Server struct {
	cfg     config.Config
	opts    Options
	db      *sql.DB
	log     *slog.Logger
	now     func() time.Time
	agent   *agentclient.Client
	static  fs.FS
	loginIP *limiter
	control *limiter
	locks   *lockout
	heads   *headFetcher
}

func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 12 * time.Hour
	}
	if opts.AbsoluteTimeout == 0 {
		opts.AbsoluteTimeout = 7 * 24 * time.Hour
	}
	if opts.Agent == nil {
		opts.Agent = agentclient.New(opts.Config.SocketPath)
	}
	if err := os.MkdirAll(opts.Config.PanelDir(), 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(opts.Config.PanelDir(), "panel.db"), panelMigrations)
	if err != nil {
		return nil, err
	}
	src := defaultHeadSources()
	if opts.Heads != nil {
		src = *opts.Heads
	}
	s := &Server{
		cfg: opts.Config, opts: opts, db: db, log: opts.Logger, now: opts.Now, agent: opts.Agent, static: opts.Static,
		loginIP: newLimiter(10, 15*time.Minute, opts.Now),
		control: newLimiter(30, time.Minute, opts.Now),
		locks:   newLockout(opts.Now),
		heads:   newHeadFetcher(src),
	}
	if err := s.ensureWorkspace(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) Close() error { return s.db.Close() }

type authLevel int

const (
	public         authLevel = iota
	publicMutation           // login/setup: same-origin + X-Requested-With, no session yet
	needSession
	needSessionCSRF
	// pendingSession is the second sign-in step: publicMutation's checks
	// plus a password-checked second-step cookie, which is not a session.
	pendingSession
)

// Route is one panel API route; tests iterate Routes() to check that every
// route enforces its authentication and CSRF level. Act is what the signed-in
// account must be permitted to do.
type Route struct {
	Method  string
	Pattern string
	Level   authLevel
	Act     action
	handler func(w http.ResponseWriter, r *http.Request, sess *session)
}

// Mutating reports whether the route changes state (and so needs CSRF).
func (rt Route) Mutating() bool {
	return rt.Level == needSessionCSRF || rt.Level == publicMutation || rt.Level == pendingSession
}

// NeedsSession reports whether the route requires a signed-in admin.
func (rt Route) NeedsSession() bool { return rt.Level == needSession || rt.Level == needSessionCSRF }

func (s *Server) Routes() []Route {
	view := func(p string, h func(http.ResponseWriter, *http.Request, *session)) Route {
		return Route{"GET", p, needSession, actView, h}
	}
	// Server routes are forwarded to the machine that runs the server.
	sg := func(p, agentPath string) Route {
		return Route{"GET", p, needSession, actView, s.serverProxy("GET", agentPath)}
	}
	sm := func(method, p, agentPath string) Route {
		return Route{method, p, needSessionCSRF, actManageServers, s.serverProxy(method, agentPath)}
	}
	// Machine routes are forwarded to the machine named in the path.
	mg := func(p, agentPath string) Route {
		return Route{"GET", p, needSession, actView, s.machineProxy("GET", agentPath)}
	}
	mm := func(method, p, agentPath string, act action) Route {
		return Route{method, p, needSessionCSRF, act, s.machineProxy(method, agentPath)}
	}
	return []Route{
		{"GET", "/api/health", public, "", s.hHealth},
		{"GET", "/api/setup/status", public, "", s.hSetupStatus},
		{"POST", "/api/setup", publicMutation, "", s.hSetup},
		{"POST", "/api/auth/login", publicMutation, "", s.hLogin},
		{"POST", "/api/auth/second-factor", pendingSession, "", s.hSecondFactor},
		{"POST", "/api/auth/second-factor/cancel", pendingSession, "", s.hSecondFactorCancel},
		view("/api/auth/me", s.hMe),
		{"POST", "/api/auth/logout", needSessionCSRF, actView, s.hLogout},
		{"POST", "/api/auth/logout-all", needSessionCSRF, actManageAccount, s.hLogoutAll},
		{"POST", "/api/auth/password", needSessionCSRF, actManageAccount, s.hPassword},
		view("/api/auth/2fa", s.h2FAStatus),
		{"POST", "/api/auth/2fa/setup", needSessionCSRF, actManageAccount, s.h2FASetupStart},
		view("/api/auth/2fa/setup", s.h2FASetupShow),
		{"DELETE", "/api/auth/2fa/setup", needSessionCSRF, actManageAccount, s.h2FASetupCancel},
		{"POST", "/api/auth/2fa/confirm", needSessionCSRF, actManageAccount, s.h2FAConfirm},
		{"POST", "/api/auth/2fa/disable", needSessionCSRF, actManageAccount, s.h2FADisable},
		{"POST", "/api/auth/2fa/recovery-codes", needSessionCSRF, actManageAccount, s.h2FARecoveryCodes},
		view("/api/me/prefs", s.hPrefs),
		{"POST", "/api/me/prefs", needSessionCSRF, actView, s.hPrefsSet},
		{"GET", "/api/audit", needSession, actViewAuditTrail, s.hAudit},
		view("/api/projects", s.hProjects),
		view("/api/machines", s.hMachines),
		view("/api/machines/{mid}", s.hMachine),
		mg("/api/machines/{mid}/preflight", "/v1/preflight"),
		mg("/api/machines/{mid}/catalog", "/v1/catalog"),
		mg("/api/machines/{mid}/activity", "/v1/activity"),
		mg("/api/machines/{mid}/update", "/v1/update"),
		mm("POST", "/api/machines/{mid}/update/check", "/v1/update/check", actManageMachine),
		mm("POST", "/api/machines/{mid}/update/apply", "/v1/update/apply", actManageMachine),
		mm("POST", "/api/machines/{mid}/servers", "/v1/servers", actManageServers),
		{"POST", "/api/machines/{mid}/restore/upload", needSessionCSRF, actManageServers, s.rawUpload("/v1/restore/upload", "application/gzip")},
		mg("/api/machines/{mid}/restore/{rid}", "/v1/restore/{rid}"),
		mm("POST", "/api/machines/{mid}/restore/{rid}/apply", "/v1/restore/{rid}/apply", actManageServers),
		mm("DELETE", "/api/machines/{mid}/restore/{rid}", "/v1/restore/{rid}", actManageServers),
		mg("/api/machines/{mid}/operations/{op}", "/v1/operations/{op}"),
		view("/api/servers", s.hServers),
		sg("/api/servers/{id}", "/v1/servers/{id}"),
		sm("POST", "/api/servers/{id}/start", "/v1/servers/{id}/start"),
		sm("POST", "/api/servers/{id}/stop", "/v1/servers/{id}/stop"),
		sm("POST", "/api/servers/{id}/restart", "/v1/servers/{id}/restart"),
		sm("POST", "/api/servers/{id}/settings", "/v1/servers/{id}/settings"),
		sm("POST", "/api/servers/{id}/version", "/v1/servers/{id}/version"),
		sm("POST", "/api/servers/{id}/delete", "/v1/servers/{id}/delete"),
		view("/api/servers/{id}/icon", s.rawGet("/v1/servers/{id}/icon", "image/png")),
		{"POST", "/api/servers/{id}/icon", needSessionCSRF, actManageServers, s.rawUpload("/v1/servers/{id}/icon", "image/png")},
		sg("/api/servers/{id}/logs", "/v1/servers/{id}/logs"),
		sm("POST", "/api/servers/{id}/command", "/v1/servers/{id}/command"),
		sg("/api/servers/{id}/whitelist", "/v1/servers/{id}/whitelist"),
		sm("POST", "/api/servers/{id}/whitelist", "/v1/servers/{id}/whitelist"),
		sm("DELETE", "/api/servers/{id}/whitelist/{name}", "/v1/servers/{id}/whitelist/{name}"),
		sg("/api/servers/{id}/operators", "/v1/servers/{id}/operators"),
		sm("POST", "/api/servers/{id}/operators", "/v1/servers/{id}/operators"),
		sm("DELETE", "/api/servers/{id}/operators/{name}", "/v1/servers/{id}/operators/{name}"),
		sm("POST", "/api/servers/{id}/kick", "/v1/servers/{id}/kick"),
		sg("/api/servers/{id}/metrics", "/v1/servers/{id}/metrics"),
		sg("/api/servers/{id}/players/sessions", "/v1/servers/{id}/players/sessions"),
		sg("/api/servers/{id}/players/summary", "/v1/servers/{id}/players/summary"),
		sg("/api/servers/{id}/events", "/v1/servers/{id}/events"),
		view("/api/servers/{id}/activity", s.hServerActivity),
		sg("/api/servers/{id}/backups", "/v1/servers/{id}/backups"),
		sm("POST", "/api/servers/{id}/backups", "/v1/servers/{id}/backups"),
		sm("POST", "/api/servers/{id}/backups/{bid}/verify", "/v1/servers/{id}/backups/{bid}/verify"),
		{"GET", "/api/servers/{id}/backups/{bid}/download", needSession, actView, s.hDownload},
		sm("DELETE", "/api/servers/{id}/backups/{bid}", "/v1/servers/{id}/backups/{bid}"),
		sm("POST", "/api/servers/{id}/backups/{bid}/restore", "/v1/servers/{id}/backups/{bid}/restore"),
		{"POST", "/api/servers/{id}/restore/upload", needSessionCSRF, actManageServers, s.rawUpload("/v1/servers/{id}/restore/upload", "application/gzip")},
		view("/api/players/{name}/head", s.hHead),
		view("/api/server", s.hLegacyStatus),
	}
}

// Handler returns the complete panel handler (API, health check and UI).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.Routes() {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, s.guard(rt))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Unknown API route.", "")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/", s.serveUI)
	return s.securityHeaders(s.logRequests(mux))
}

// guard enforces the route's authentication level before any handler code
// runs. Every failure path denies the request.
func (s *Server) guard(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch rt.Level {
		case public:
			rt.handler(w, r, nil)
		case publicMutation:
			if !s.sameOrigin(r) || r.Header.Get("X-Requested-With") != "playkeeper" {
				writeErr(w, http.StatusForbidden, api.CodeForbidden, "Cross-site request refused.", "")
				return
			}
			rt.handler(w, r, nil)
		case pendingSession:
			if !s.sameOrigin(r) || r.Header.Get("X-Requested-With") != "playkeeper" {
				writeErr(w, http.StatusForbidden, api.CodeForbidden, "Cross-site request refused.", "")
				return
			}
			p, err := s.pendingFrom(r)
			if err != nil {
				clearPendingCookie(w)
				writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in again.", "")
				return
			}
			rt.handler(w, r, &p)
		case needSession, needSessionCSRF:
			sess, err := s.sessionFrom(r)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Please sign in.", "")
				return
			}
			if rt.Level == needSessionCSRF {
				tok := r.Header.Get("X-CSRF-Token")
				if !s.sameOrigin(r) || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1 {
					writeErr(w, http.StatusForbidden, api.CodeForbidden, "Security token missing or invalid. Reload the page and try again.", "")
					return
				}
				if ok, wait := s.control.allow("session:" + sess.IDHash); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
					writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many actions in a short time. Wait a moment.", "")
					return
				}
			}
			if !permit(&sess, rt.Act) {
				writeErr(w, http.StatusForbidden, api.CodeForbidden, "Your account is not allowed to do this.", "")
				return
			}
			rt.handler(w, r, &sess)
		default:
			writeErr(w, http.StatusForbidden, api.CodeForbidden, "Forbidden.", "")
		}
	}
}

// sameOrigin rejects browser requests whose Origin is not this panel.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(o)
	return err == nil && u.Scheme == "https" && u.Host == r.Host
}

func (s *Server) sessionFrom(r *http.Request) (session, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, errNoSession
	}
	return s.lookupSession(c.Value)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(s.opts.AbsoluteTimeout.Seconds())})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests records method, path, status and duration. It never logs query
// strings, headers or bodies (which may carry codes, cookies or passwords).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || sw.status >= 400 {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSON(w, status, api.Error{Error: msg, Code: code, Hint: hint})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- public routes ---

func (s *Server) hHealth(w http.ResponseWriter, r *http.Request, _ *session) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version})
}

func (s *Server) hSetupStatus(w http.ResponseWriter, r *http.Request, _ *session) {
	n, err := s.userCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"needsSetup": n == 0})
}

type credentials struct {
	Token    string `json:"token,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) rateLimitIP(w http.ResponseWriter, r *http.Request) bool {
	if ok, wait := s.loginIP.allow("ip:" + clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many attempts. Try again in a few minutes.", "")
		return false
	}
	return true
}

func (s *Server) hSetup(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.rateLimitIP(w, r) {
		return
	}
	n, err := s.userCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Setup is already complete. Sign in instead.", "")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	if err := s.checkSetupToken(c.Token); err != nil {
		s.audit("(setup)", "setup", "admin", "refused", "invalid or expired setup code")
		writeErr(w, http.StatusForbidden, api.CodeForbidden, err.Error(), "")
		return
	}
	if err := validUsername(c.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	if err := validPassword(c.Password, c.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	u, err := s.createFirstAdmin(c.Username, c.Password)
	if err != nil {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Setup is already complete. Sign in instead.", "")
		return
	}
	s.consumeSetupToken()
	token, sess, err := s.newSession(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	}
	s.audit(u.Username, "setup", "admin", "succeeded", "first admin account created")
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.meBody(sess))
}

func (s *Server) hLogin(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.rateLimitIP(w, r) {
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil || c.Token != "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	key := "user:" + strings.ToLower(c.Username)
	if locked, wait := s.locks.locked(key); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, api.CodeRateLimited, "Too many failed sign-ins for this account. Try again later.", "")
		return
	}
	u, ok := s.authenticate(c.Username, c.Password)
	if !ok {
		s.locks.fail(key)
		actor := "(unknown user)"
		if u.Username != "" {
			actor = u.Username
		} else if _, err := s.userByName(c.Username); err == nil {
			actor = c.Username
		}
		s.audit(actor, "login", "panel", "failed", "")
		writeErr(w, http.StatusUnauthorized, api.CodeUnauthorized, "Wrong username or password.", "")
		return
	}
	s.locks.succeed(key)
	if started, err := s.secondFactorNeeded(w, u); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start the second sign-in step.", "")
		return
	} else if started {
		return
	}
	token, sess, err := s.newSession(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not start a session.", "")
		return
	}
	s.audit(u.Username, "login", "panel", "succeeded", "")
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.meBody(sess))
}

func (s *Server) userByName(name string) (user, error) {
	var u user
	err := s.db.QueryRow(`SELECT id, username FROM users WHERE username = ?`, name).Scan(&u.ID, &u.Username)
	return u, err
}

func (s *Server) meBody(sess session) map[string]any {
	body := map[string]any{
		"user":               map[string]string{"username": sess.User.Username, "role": sess.User.Role},
		"csrfToken":          sess.CSRF,
		"expiresAt":          sess.ExpiresAt.UTC(),
		"idleTimeoutSeconds": int(s.opts.IdleTimeout.Seconds()),
		"version":            version.Version,
	}
	var changed int64
	if s.db.QueryRow(`SELECT password_changed_at FROM users WHERE id = ?`, sess.User.ID).Scan(&changed) == nil {
		body["passwordChangedAt"] = msTime(changed)
	}
	return body
}

func (s *Server) hMe(w http.ResponseWriter, r *http.Request, sess *session) {
	writeJSON(w, http.StatusOK, s.meBody(*sess))
}

func (s *Server) hLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.deleteSession(sess.IDHash)
	s.audit(sess.User.Username, "logout", "panel", "succeeded", "")
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hLogoutAll(w http.ResponseWriter, r *http.Request, sess *session) {
	s.deleteUserSessions(sess.User.ID)
	s.audit(sess.User.Username, "logout-all", "panel", "succeeded", "")
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	if _, ok := s.authenticate(sess.User.Username, req.CurrentPassword); !ok {
		s.audit(sess.User.Username, "password.change", "panel", "failed", "wrong current password")
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Your current password is not correct.", "")
		return
	}
	if err := validPassword(req.NewPassword, sess.User.Username); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, err.Error(), "")
		return
	}
	h, err := hashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not change the password.", "")
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`, h, s.now().UnixMilli(), sess.User.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not change the password.", "")
		return
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, sess.User.ID, sess.IDHash)
	s.audit(sess.User.Username, "password.change", "panel", "succeeded", "other sessions signed out")
	w.WriteHeader(http.StatusNoContent)
}

// ResetAdmin replaces the admin password from the host (root CLI recovery).
func (s *Server) ResetAdmin(username, password string) error {
	if err := validPassword(password, username); err != nil {
		return err
	}
	h, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, password_changed_at = ? WHERE username = ?`, h, s.now().UnixMilli(), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such user: " + username)
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	s.audit("root@host", "password.reset", username, "succeeded", "reset from the server command line")
	return nil
}

// Usernames lists admin accounts (root CLI).
func (s *Server) Usernames() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

func (s *Server) hAudit(w http.ResponseWriter, r *http.Request, sess *session) {
	rows, err := s.db.Query(`SELECT id, ts, actor, action, target, result, detail FROM audit ORDER BY id DESC LIMIT 200`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	type entry struct {
		api.AuditEntry
		Source string `json:"source"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.Result, &e.Detail); err == nil {
			e.TS = time.UnixMilli(ts).UTC()
			e.Source = "panel"
			out = append(out, e)
		}
	}
	var agentAudit []api.AuditEntry
	if _, err := s.agent.Do(r.Context(), "GET", "/v1/audit", url.Values{"limit": {"200"}}, nil, &agentAudit); err == nil {
		for _, a := range agentAudit {
			out = append(out, entry{AuditEntry: a, Source: "agent"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > 300 {
		out = out[:300]
	}
	writeJSON(w, http.StatusOK, out)
}

// --- agent proxy ---

var pathKeys = []string{"id", "name", "bid", "rid", "op"}

func agentPath(pattern string, r *http.Request) string {
	out := pattern
	for _, key := range pathKeys {
		out = strings.ReplaceAll(out, "{"+key+"}", url.PathEscape(r.PathValue(key)))
	}
	return out
}

func (s *Server) agentFailure(w http.ResponseWriter, err error) {
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, ae.Body)
		return
	}
	writeErr(w, http.StatusServiceUnavailable, api.CodeAgentUnavailable, "The Playkeeper agent is not running, so the server cannot be seen or controlled right now.", "On the server, check: sudo systemctl status playkeeper-agent")
}

// target is the machine a request goes to: the one named by {mid}, or the
// one that runs the server named by {id}.
func (s *Server) target(w http.ResponseWriter, r *http.Request) (machine, bool) {
	if r.PathValue("mid") != "" {
		return s.machineFromPath(w, r)
	}
	id := r.PathValue("id")
	if !reMachineID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid server id.", "")
		return machine{}, false
	}
	m, err := s.machineForServer(r, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Server not found.", "")
		return machine{}, false
	}
	return m, true
}

func (s *Server) serverProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forward(method, pattern)
}

func (s *Server) machineProxy(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return s.forward(method, pattern)
}

// forward sends the request to its machine's agent. GETs pass the query on;
// JSON bodies get the signed-in account stamped as actor (the agent checks
// every field and rejects unknown ones); DELETEs pass the actor in the query.
func (s *Server) forward(method, pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		path := agentPath(pattern, r)
		var raw json.RawMessage
		var status int
		var err error
		switch method {
		case "GET":
			status, err = m.agent.Do(r.Context(), "GET", path, r.URL.Query(), nil, &raw)
		case "DELETE":
			status, err = m.agent.Do(r.Context(), "DELETE", path, url.Values{"actor": {sess.User.Username}}, nil, &raw)
		default:
			body := map[string]any{}
			b, rerr := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if rerr != nil {
				writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
				return
			}
			if len(bytes.TrimSpace(b)) > 0 {
				if err := json.Unmarshal(b, &body); err != nil {
					writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
					return
				}
			}
			body["actor"] = sess.User.Username
			status, err = m.agent.Do(r.Context(), method, path, nil, body, &raw)
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

func (s *Server) hServerActivity(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	q := url.Values{"server": {r.PathValue("id")}}
	if l := r.URL.Query().Get("limit"); l != "" {
		q.Set("limit", l)
	}
	var raw json.RawMessage
	status, err := m.agent.Do(r.Context(), "GET", "/v1/activity", q, nil, &raw)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	writeJSON(w, status, raw)
}

func (s *Server) hDownload(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	resp, err := m.agent.Raw(r.Context(), "GET", agentPath("/v1/servers/{id}/backups/{bid}/download", r), nil, nil, map[string]string{"X-Playkeeper-Actor": sess.User.Username}, true)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
		return
	}
	for _, h := range []string{"Content-Type", "Content-Disposition", "Content-Length", "X-Playkeeper-SHA256"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}

// rawGet streams a non-JSON agent response of the given type (an image).
func (s *Server) rawGet(pattern, contentType string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, _ *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		resp, err := m.agent.Raw(r.Context(), "GET", agentPath(pattern, r), nil, nil, nil, false)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, io.LimitReader(resp.Body, 1<<20))
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	}
}

// rawUpload streams an uploaded file (a backup archive, a server icon) to
// the agent, which checks it.
func (s *Server) rawUpload(pattern, contentType string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		m, ok := s.target(w, r)
		if !ok {
			return
		}
		resp, err := m.agent.Raw(r.Context(), "POST", agentPath(pattern, r), nil, r.Body,
			map[string]string{"X-Playkeeper-Actor": sess.User.Username, "Content-Type": contentType}, true)
		if err != nil {
			s.agentFailure(w, err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	}
}

// --- UI ---

const uiMissing = `<!doctype html><meta charset="utf-8"><title>Playkeeper</title><p>The Playkeeper web UI is not built into this binary. Run <code>make web</code> and rebuild.</p>`

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.static == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, uiMissing)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p != "" && p != "index.html" {
		if st, err := fs.Stat(s.static, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.static, p)
			return
		}
		if strings.HasPrefix(p, "assets/") || path.Ext(p) != "" {
			http.NotFound(w, r)
			return
		}
	}
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, uiMissing)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// ListenAndServeTLS serves the panel over HTTPS until ctx ends.
func (s *Server) ListenAndServeTLS(ctx context.Context) error {
	certFile, keyFile := filepath.Join(s.cfg.TLSDir(), "cert.pem"), filepath.Join(s.cfg.TLSDir(), "key.pem")
	if _, err := EnsureSelfSignedCert(s.cfg.TLSDir(), s.now()); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.cfg.PanelBind, strconv.Itoa(s.cfg.PanelPort))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	s.log.Info("panel listening", "addr", "https://"+addr)
	err = srv.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
