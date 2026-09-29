package panel

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/version"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Sign in with Whop: a customer signs in with their Whop account instead of
// a password of ours. The dashboard sends them to Whop with OAuth 2.1 and
// PKCE, reads who they are when Whop sends them back, asks the hosting core
// which account that is (CustomerAccount), and signs them in to it. It
// keeps none of Whop's tokens, and a sign-in that doesn't work goes back to
// the sign-in page with why, as ?whop=<code>.
const (
	whopSignInPrefix   = "/api/public/whop/signin/"
	whopSignInPath     = whopSignInPrefix + "start"
	whopSignInCallback = whopSignInPrefix + "callback"
	whopSignInCookie   = "pk_whop_signin"
	// whopSignInFor is how long someone may take on Whop's side.
	whopSignInFor = 10 * time.Minute
)

// whopSignInLimits bound sign-ins from one address like password sign-ins.
var whopSignInLimits = publicLimits{perMinute: 30, open: 8, read: 10 * time.Second, write: 10 * time.Second}

func (s *Server) whopSignIn() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case whopSignInPath:
			s.startWhopSignIn(w, r)
		case whopSignInCallback:
			s.finishWhopSignIn(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// whopOAuth is Sign in with Whop as the owner set it up, or ok false while
// it can't work: with no Whop app, or no address for Whop to send people
// back to.
func (s *Server) whopOAuth(ctx context.Context) (whop.OAuth, bool) {
	var id, secret string
	if err := s.db.QueryRowContext(ctx, `SELECT oauth_client_id, oauth_client_secret FROM whop_account WHERE id = 1`).Scan(&id, &secret); err != nil || id == "" {
		return whop.OAuth{}, false
	}
	dash, err := s.dashboardURL(ctx)
	if err != nil || dash == "" {
		return whop.OAuth{}, false
	}
	base, err := whop.OAuthURL(s.cfg.WhopAPIURL)
	if err != nil {
		return whop.OAuth{}, false
	}
	return whop.OAuth{URL: base, ClientID: id, ClientSecret: secret, RedirectURI: dash + whopSignInCallback, UserAgent: "Playkeeper/" + version.Version}, true
}

// backToSignIn sends a sign-in that didn't work back to the sign-in page.
func backToSignIn(w http.ResponseWriter, r *http.Request, why string) {
	http.Redirect(w, r, "/login?whop="+why, http.StatusSeeOther)
}

func (s *Server) startWhopSignIn(w http.ResponseWriter, r *http.Request) {
	o, ok := s.whopOAuth(r.Context())
	if !ok {
		backToSignIn(w, r, "off")
		return
	}
	verifier, err := whop.NewVerifier()
	if err != nil {
		backToSignIn(w, r, "failed")
		return
	}
	state := randomToken(32)
	now := s.now()
	if _, err := s.db.Exec(`DELETE FROM whop_signins WHERE created_at < ?`, now.Add(-whopSignInFor).UnixMilli()); err != nil {
		s.log.Error("could not forget old Whop sign-ins", "err", err)
	}
	if _, err := s.db.Exec(`INSERT INTO whop_signins(state_hash, verifier, created_at) VALUES(?,?,?)`, tokenHash(state), verifier, now.UnixMilli()); err != nil {
		backToSignIn(w, r, "failed")
		return
	}
	// Lax, since Whop sends the browser back from its own site; only the
	// callback beside this path ever gets it.
	http.SetCookie(w, &http.Cookie{Name: whopSignInCookie, Value: state, Path: whopSignInPrefix, MaxAge: int(whopSignInFor.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, o.AuthorizeURL(state, randomToken(16), verifier), http.StatusSeeOther)
}

func (s *Server) finishWhopSignIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	http.SetCookie(w, &http.Cookie{Name: whopSignInCookie, Value: "", Path: whopSignInPrefix, MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	// The state must be the one this browser left with, and is used once.
	state := q.Get("state")
	c, err := r.Cookie(whopSignInCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		backToSignIn(w, r, "expired")
		return
	}
	var verifier string
	var created int64
	err = s.db.QueryRowContext(ctx, `DELETE FROM whop_signins WHERE state_hash = ? RETURNING verifier, created_at`, tokenHash(state)).Scan(&verifier, &created)
	if err != nil || s.now().Sub(time.UnixMilli(created)) > whopSignInFor {
		backToSignIn(w, r, "expired")
		return
	}
	if q.Get("error") != "" {
		backToSignIn(w, r, "denied")
		return
	}
	o, ok := s.whopOAuth(ctx)
	if !ok {
		backToSignIn(w, r, "off")
		return
	}
	who, err := s.whoOnWhop(ctx, o, q.Get("code"), verifier)
	if err != nil {
		s.log.Warn("a sign-in with Whop failed", "err", err)
		backToSignIn(w, r, "failed")
		return
	}
	acct, ok, err := s.hosting.CustomerAccount(ctx, whopProvider, who.Subject)
	switch {
	case err != nil:
		s.log.Error("could not look up a Whop customer's account", "err", err)
		backToSignIn(w, r, "failed")
		return
	case !ok:
		s.audit("whop:"+who.Subject, "login", "panel", "refused", "signed in with Whop without an account here")
		backToSignIn(w, r, s.whopSignInWithoutAccount(ctx, who.Subject))
		return
	case !acct.SignIn:
		why := "paused"
		if acct.State == CustomerSuspended {
			why = "suspended"
		}
		s.audit(acct.Username, "login", "panel", "refused", "signed in with Whop while their account can't sign in ("+string(acct.State)+")")
		backToSignIn(w, r, why)
		return
	}
	var u user
	err = s.db.QueryRowContext(ctx, `SELECT id, username, role FROM users WHERE id = ?`, acct.UserID).Scan(&u.ID, &u.Username, &u.Role)
	if err != nil || u.Role == roleOwner {
		// The owner's account is never a customer's.
		backToSignIn(w, r, "no_account")
		return
	}
	token, _, err := s.newSession(u)
	if err != nil {
		backToSignIn(w, r, "failed")
		return
	}
	s.audit(u.Username, "login", "panel", "succeeded", "signed in with Whop")
	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// whoOnWhop trades the code Whop sent back for who signed in, then ends the
// refresh token, which the dashboard never uses.
func (s *Server) whoOnWhop(ctx context.Context, o whop.OAuth, code, verifier string) (whop.UserInfo, error) {
	if code == "" || len(code) > 2048 {
		return whop.UserInfo{}, errors.New("Whop sent back no code")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tokens, err := o.Exchange(ctx, code, verifier)
	if err != nil {
		return whop.UserInfo{}, err
	}
	who, err := o.UserInfo(ctx, tokens.AccessToken)
	if tokens.RefreshToken != "" {
		if err := o.Revoke(ctx, tokens.RefreshToken); err != nil {
			s.log.Warn("could not end a Whop refresh token", "err", err)
		}
	}
	if err != nil {
		return whop.UserInfo{}, err
	}
	if !strings.HasPrefix(who.Subject, "user_") || !reWhopID.MatchString(who.Subject) {
		return whop.UserInfo{}, errors.New("Whop's answer named no user")
	}
	return who, nil
}

// whopSignInWithoutAccount says why a Whop user with no account here can't
// sign in: their account is on its way ("starting") when Whop confirmed a
// plan of theirs, else they have none ("no_account").
func (s *Server) whopSignInWithoutAccount(ctx context.Context, whopUserID string) string {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM whop_memberships m JOIN whop_plans p ON p.plan_id = m.plan_id AND p.allowance_from != ''
		WHERE m.whop_user_id = ? AND m.stale = 0 AND m.status IN `+whopAccess, whopUserID).Scan(&n); err == nil && n > 0 {
		s.kickWhop()
		return "starting"
	}
	return "no_account"
}

// whopSignInView is Sign in with Whop's setup on Settings › Sell on Whop.
type whopSignInView struct {
	// ClientID is the Whop app customers sign in through, and SecretEnding
	// the end of its secret, when it has one.
	ClientID     string `json:"clientId,omitempty"`
	SecretEnding string `json:"secretEnding,omitempty"`
	// RedirectURI is the address the app must list, "" while the machine
	// has none.
	RedirectURI string `json:"redirectUri,omitempty"`
}

func (s *Server) readWhopSignIn(ctx context.Context, dash string) (whopSignInView, error) {
	var v whopSignInView
	var secret string
	err := s.db.QueryRowContext(ctx, `SELECT oauth_client_id, oauth_client_secret FROM whop_account WHERE id = 1`).Scan(&v.ClientID, &secret)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v.SecretEnding = whop.Ending(secret)
	if dash != "" {
		v.RedirectURI = dash + whopSignInCallback
	}
	return v, nil
}

// whopSignInOn is whether the sign-in page offers Sign in with Whop.
func (s *Server) whopSignInOn() bool {
	var id string
	return s.db.QueryRow(`SELECT oauth_client_id FROM whop_account WHERE id = 1`).Scan(&id) == nil && id != ""
}

// hWhopSignInSet keeps the Whop app customers sign in through.
func (s *Server) hWhopSignInSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	req.ClientID, req.ClientSecret = strings.TrimSpace(req.ClientID), strings.TrimSpace(req.ClientSecret)
	if !whop.ValidClientID(req.ClientID) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "That isn't a Whop app's ID.", "Copy it from the app on Whop: it starts with app_.")
		return
	}
	if req.ClientSecret != "" && !whop.ValidKey(req.ClientSecret) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "That isn't a Whop app's secret.", "Copy it again from the app on Whop, or leave it empty.")
		return
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	res, err := s.db.Exec(`UPDATE whop_account SET oauth_client_id = ?, oauth_client_secret = ? WHERE id = 1`, req.ClientID, req.ClientSecret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Connect a store on Whop first.", "")
		return
	}
	detail := "Whop app " + req.ClientID
	if req.ClientSecret != "" {
		detail += ", with a secret ending " + whop.Ending(req.ClientSecret)
	}
	s.audit(sess.User.Username, "whop.signin", "panel", "succeeded", detail)
	s.answerWhop(w, r)
}

// hWhopSignInOff stops offering Sign in with Whop.
func (s *Server) hWhopSignInOff(w http.ResponseWriter, r *http.Request, sess *session) {
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	if _, err := s.db.Exec(`UPDATE whop_account SET oauth_client_id = '', oauth_client_secret = '' WHERE id = 1`); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM whop_signins`); err != nil {
		s.log.Error("could not forget Whop sign-ins", "err", err)
	}
	s.audit(sess.User.Username, "whop.signin_off", "panel", "succeeded", "")
	s.answerWhop(w, r)
}
