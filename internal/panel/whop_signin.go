package panel

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
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
	// whopCallsFor bounds the calls to Whop when someone comes back: the
	// exchange, reading who they are, and ending the refresh token.
	whopCallsFor = 30 * time.Second
)

// whopSignInLimits bound sign-ins from one address like password sign-ins.
// Coming back waits on Whop, and the route's read deadline ends the
// request's context, so both deadlines outlast whopCallsFor.
var whopSignInLimits = publicLimits{perMinute: 30, open: 8, read: whopCallsFor + 10*time.Second, write: whopCallsFor + 10*time.Second}

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
	return s.whopOAuthFor(ctx, id, secret)
}

// whopOAuthFor is sign-in through the Whop app id with its secret, sending
// people back to the dashboard's address, or ok false without one.
func (s *Server) whopOAuthFor(ctx context.Context, id, secret string) (whop.OAuth, bool) {
	dash, err := s.dashboardURL(ctx)
	if err != nil || dash == "" {
		return whop.OAuth{}, false
	}
	base, err := whop.OAuthURL(s.cfg.WhopAPIURL)
	if err != nil {
		return whop.OAuth{}, false
	}
	return s.whopOAuthAt(base, id, secret, dash+whopSignInCallback), true
}

// whopOAuthAt is sign-in through the Whop app id, at Whop's OAuth address
// base, sending people back to redirect.
func (s *Server) whopOAuthAt(base, id, secret, redirect string) whop.OAuth {
	return whop.OAuth{URL: base, ClientID: id, ClientSecret: secret, RedirectURI: redirect, UserAgent: "Playkeeper/" + version.Version}
}

// signInRedirect is the redirect URI a sign-in sends Whop: the dashboard's
// address, unless Whop refuses that and takes the dashboard's address at
// the panel's port, which keeps reaching it. So a dashboard that has just
// lost its port (see dashboard443.go) keeps signing customers in through
// the redirect URL the app listed before, until its owner adds the new one.
// fresh asks Whop again instead of trusting its last answers.
func (s *Server) signInRedirect(ctx context.Context, o whop.OAuth, fresh bool) string {
	_, old, err := s.dashboardURLs(ctx)
	alt := old + whopSignInCallback
	if err != nil || old == "" || alt == o.RedirectURI {
		return o.RedirectURI
	}
	if accepted, known := s.whopAccepts(ctx, o, fresh); accepted || !known {
		return o.RedirectURI
	}
	at := o
	at.RedirectURI = alt
	if accepted, known := s.whopAccepts(ctx, at, fresh); accepted || !known {
		return alt
	}
	return o.RedirectURI
}

// redirectChecks are Whop's last answers on whether an app lists a
// redirect URI (see whopAccepts).
type redirectChecks struct {
	mu sync.Mutex
	m  map[string]redirectCheck
}

type redirectCheck struct {
	accepted, known bool
	until           time.Time
}

// How long whopAccepts keeps Whop's answer: one that took the redirect URI,
// one that refused it, which the owner may put right any moment, and none.
const (
	redirectAcceptedFor = 10 * time.Minute
	redirectRefusedFor  = time.Minute
	redirectUnknownFor  = 30 * time.Second
)

// whopAccepts is Whop's answer on whether the app lists o's redirect URI
// (whop.OAuth.Accepts), kept for a while unless fresh asks again.
func (s *Server) whopAccepts(ctx context.Context, o whop.OAuth, fresh bool) (accepted, known bool) {
	key := o.URL + " " + o.ClientID + " " + o.RedirectURI
	c := &s.redirects
	now := s.now()
	c.mu.Lock()
	if got, ok := c.m[key]; ok && !fresh && now.Before(got.until) {
		c.mu.Unlock()
		return got.accepted, got.known
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	accepted, known = o.Accepts(ctx)
	keep := redirectUnknownFor
	switch {
	case accepted:
		keep = redirectAcceptedFor
	case known:
		keep = redirectRefusedFor
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= 32 {
		c.m = map[string]redirectCheck{}
	}
	c.m[key] = redirectCheck{accepted: accepted, known: known, until: now.Add(keep)}
	c.mu.Unlock()
	return accepted, known
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
	o.RedirectURI = s.signInRedirect(r.Context(), o, false)
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
	if _, err := s.db.Exec(`INSERT INTO whop_signins(state_hash, verifier, created_at, redirect_uri) VALUES(?,?,?,?)`, tokenHash(state), verifier, now.UnixMilli(), o.RedirectURI); err != nil {
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
	var verifier, redirect string
	var created int64
	err = s.db.QueryRowContext(ctx, `DELETE FROM whop_signins WHERE state_hash = ? RETURNING verifier, created_at, redirect_uri`, tokenHash(state)).Scan(&verifier, &created, &redirect)
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
	// Trading the code names the redirect URI the sign-in left with.
	if redirect != "" {
		o.RedirectURI = redirect
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
	ctx, cancel := context.WithTimeout(ctx, whopCallsFor)
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
	// has none. Using is the one sign-ins send Whop instead while Whop lists
	// only that one: the dashboard's address at the panel's port, from
	// before it answered without a port (see signInRedirect).
	RedirectURI string `json:"redirectUri,omitempty"`
	Using       string `json:"using,omitempty"`
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
		if v.ClientID != "" {
			if o, ok := s.whopOAuthFor(ctx, v.ClientID, secret); ok {
				if using := s.signInRedirect(ctx, o, false); using != v.RedirectURI {
					v.Using = using
				}
			}
		}
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
	var stores int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM whop_account WHERE id = 1`).Scan(&stores); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if stores == 0 {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Connect a store on Whop first.", "")
		return
	}
	// Every sign-in would fail with an app Whop refuses, as with a secret
	// that isn't the app's, or none when Whop wants one, and with an app
	// that doesn't list the dashboard's redirect URL.
	if o, ok := s.whopOAuthFor(r.Context(), req.ClientID, req.ClientSecret); ok {
		ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
		defer cancel()
		err := o.CheckClient(ctx)
		var oe *whop.OAuthError
		if errors.As(err, &oe) {
			hint := "Copy the app's ID and its client secret again from Whop's developer dashboard."
			if strings.Contains(oe.Description, whop.TokenExchange) {
				hint = "On Whop's developer dashboard, add " + whop.TokenExchange + " on the app's own Permissions tab, not on an API key, and save. Then turn this on again."
			}
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Whop refused this app: "+cmpOr(oe.Description, oe.Code)+".", hint)
			return
		}
		want := o.RedirectURI
		o.RedirectURI = s.signInRedirect(ctx, o, true)
		if accepted, known := s.whopAccepts(ctx, o, false); known && !accepted {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Whop doesn't list this dashboard's redirect URL on that app.",
				"On the app's OAuth tab on Whop's developer dashboard, add "+want+" as a redirect URL and save. Then turn this on again.")
			return
		}
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
