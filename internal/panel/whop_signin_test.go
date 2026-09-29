package panel

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

const whopTestApp = "app_pipcloud"

// browser is a browser on the dashboard: it keeps cookies and follows no
// redirect, so each step of a sign-in shows.
type browser struct {
	t *testing.T
	e *env
	c *http.Client
}

func newBrowser(t *testing.T, e *env) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, e: e, c: &http.Client{Transport: e.ts.Client().Transport, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// visit opens a path on the dashboard, or a link Whop sends back to it,
// and answers with where the dashboard sends the browser next.
func (b *browser) visit(target string) (*http.Response, string) {
	b.t.Helper()
	res, err := b.c.Get(b.e.ts.URL + strings.TrimPrefix(target, whopDashboard))
	if err != nil {
		b.t.Fatal(err)
	}
	res.Body.Close()
	return res, res.Header.Get("Location")
}

// signInWithWhop walks a sign-in as user through Whop, and answers with
// where the dashboard sends them at the end.
func (b *browser) signInWithWhop(f *fakeWhop, user string) string {
	b.t.Helper()
	res, authorize := b.visit(whopSignInPath)
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(authorize, f.srv.URL+"/oauth/authorize?") {
		b.t.Fatalf("leaving for Whop: %d to %q", res.StatusCode, authorize)
	}
	_, to := b.visit(f.approve(b.t, authorize, user))
	return to
}

// sellingWithSignIn is a dashboard selling for Pip Hosting that offers Sign
// in with Whop, with a fake hosting core in which alex's Whop account is
// the account alex.
func sellingWithSignIn(t *testing.T) (*fakeWhop, *env, member, *fakeCore, member) {
	t.Helper()
	f, e, own := connectedWhop(t)
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	core := useFakeCore(e)
	alex := addMember(t, e, "alex", invites.RoleViewer, "*")
	core.accounts["user_alex"] = CustomerAccountInfo{UserID: alex.id, Username: "alex", State: CustomerActive, SignIn: true}
	return f, e, own, core, alex
}

func TestSignInWithWhopOpensTheCustomersAccount(t *testing.T) {
	f, e, _, _, _ := sellingWithSignIn(t)
	var st api.SetupStatus
	if e.get(t, "/api/setup/status", "", &st); !st.WhopSignIn {
		t.Fatal("the sign-in page doesn't offer Sign in with Whop")
	}
	b := newBrowser(t, e)
	res, authorize := b.visit(whopSignInPath)
	var state *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == whopSignInCookie {
			state = c
		}
	}
	if state == nil || state.Path != whopSignInPrefix || !state.HttpOnly || !state.Secure || state.SameSite != http.SameSiteLaxMode || state.MaxAge != 600 ||
		res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the sign-in's cookie: %+v", state)
	}
	if !strings.Contains(authorize, "client_id="+whopTestApp) || !strings.Contains(authorize, "redirect_uri="+url.QueryEscape(whopDashboard+whopSignInCallback)) {
		t.Fatalf("sign-in link: %s", authorize)
	}
	res, to := b.visit(f.approve(t, authorize, "user_alex"))
	if res.StatusCode != http.StatusSeeOther || to != "/" {
		t.Fatalf("coming back: %d to %q", res.StatusCode, to)
	}
	sessionSet := false
	for _, c := range res.Cookies() {
		sessionSet = sessionSet || (c.Name == cookieName && c.Value != "" && c.SameSite == http.SameSiteStrictMode)
	}
	if !sessionSet {
		t.Fatal("no session")
	}
	me, err := b.c.Get(e.ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		User struct{ Username string } `json:"user"`
	}
	json.NewDecoder(me.Body).Decode(&body)
	me.Body.Close()
	if me.StatusCode != http.StatusOK || body.User.Username != "alex" {
		t.Fatalf("signed in as %q: %d", body.User.Username, me.StatusCode)
	}
	if len(f.revokedTokens) != 1 || f.revokedTokens[0] != "rt_user_alex" {
		t.Fatalf("Whop's refresh token wasn't ended: %q", f.revokedTokens)
	}
	if rows := e.auditRows(t, "login"); len(rows) == 0 || rows[len(rows)-1] != "alex panel succeeded signed in with Whop" {
		t.Fatalf("audit: %v", rows)
	}
}

func TestSignInWithWhopGoesBackWithWhyWhenItCant(t *testing.T) {
	f, e, own, core, _ := sellingWithSignIn(t)
	back := func(why string) string { return "/login?whop=" + why }

	// Whop's answer without the browser's own cookie, or with another state.
	b := newBrowser(t, e)
	_, authorize := b.visit(whopSignInPath)
	if _, to := newBrowser(t, e).visit(f.approve(t, authorize, "user_alex")); to != back("expired") {
		t.Fatalf("another browser: %q", to)
	}
	cb := f.approve(t, authorize, "user_alex")
	if _, to := b.visit(strings.Replace(cb, "state=", "state=x", 1)); to != back("expired") {
		t.Fatalf("another state: %q", to)
	}
	// A state is used once, and within ten minutes.
	b = newBrowser(t, e)
	_, authorize = b.visit(whopSignInPath)
	cb = f.approve(t, authorize, "user_alex")
	if _, to := b.visit(cb); to != "/" {
		t.Fatalf("the first time: %q", to)
	}
	b.c.Jar.SetCookies(mustURL(t, e.ts.URL+whopSignInPath), []*http.Cookie{{Name: whopSignInCookie, Value: stateOf(t, cb), Path: whopSignInPrefix}})
	if _, to := b.visit(cb); to != back("expired") {
		t.Fatalf("the same state again: %q", to)
	}
	b = newBrowser(t, e)
	_, authorize = b.visit(whopSignInPath)
	e.clock.add(whopSignInFor + time.Second)
	if _, to := b.visit(f.approve(t, authorize, "user_alex")); to != back("expired") {
		t.Fatalf("after eleven minutes: %q", to)
	}
	// They said no on Whop, or Whop refuses the code.
	b = newBrowser(t, e)
	_, authorize = b.visit(whopSignInPath)
	if _, to := b.visit(whopSignInCallback + "?error=access_denied&state=" + stateOf(t, authorize)); to != back("denied") {
		t.Fatalf("refused on Whop: %q", to)
	}
	b = newBrowser(t, e)
	_, authorize = b.visit(whopSignInPath)
	if _, to := b.visit(whopSignInCallback + "?code=code_nope&state=" + stateOf(t, authorize)); to != back("failed") {
		t.Fatalf("a code Whop refuses: %q", to)
	}
	// No account yet: on its way once Whop confirmed their plan.
	f.mu.Lock()
	f.users["user_sam"] = "samcrafts"
	f.mu.Unlock()
	if to := newBrowser(t, e).signInWithWhop(f, "user_sam"); to != back("no_account") {
		t.Fatalf("no plan: %q", to)
	}
	f.buy("mem_sam1", "user_sam", "plan_starter", "active")
	core.refuse = errNoHostingCore
	e.reconcile()
	if to := newBrowser(t, e).signInWithWhop(f, "user_sam"); to != back("starting") {
		t.Fatalf("a plan whose account is on its way: %q", to)
	}
	// An account that can't sign in now, and the owner's, which is never a customer's.
	core.mu.Lock()
	core.accounts["user_alex"] = CustomerAccountInfo{UserID: core.accounts["user_alex"].UserID, Username: "alex", State: CustomerPaused}
	core.accounts["user_own"] = CustomerAccountInfo{UserID: own.id, Username: "siya", State: CustomerActive, SignIn: true}
	core.mu.Unlock()
	if to := newBrowser(t, e).signInWithWhop(f, "user_alex"); to != back("paused") {
		t.Fatalf("a paused account: %q", to)
	}
	if to := newBrowser(t, e).signInWithWhop(f, "user_own"); to != back("no_account") {
		t.Fatalf("the owner's account: %q", to)
	}
	// Only GET.
	res, err := newBrowser(t, e).c.Post(e.ts.URL+whopSignInPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", res.StatusCode)
	}
}

func TestSignInWithWhopNeedsAnAppAndTheMachinesAddress(t *testing.T) {
	f, e, own := connectedWhop(t)
	if _, to := newBrowser(t, e).visit(whopSignInPath); to != "/login?whop=off" {
		t.Fatalf("without an app: %q", to)
	}
	e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`"}`, own.auth())
	e.setAddress(t, "")
	if _, to := newBrowser(t, e).visit(whopSignInPath); to != "/login?whop=off" {
		t.Fatalf("without an address: %q", to)
	}
	if len(f.grants) != 0 {
		t.Fatal("Whop was asked")
	}
}

func TestSignInWithWhopIsSetUpByTheOwnerAlone(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	lena := addAdmin(t, e, "lena", "*")
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`"}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("before a store is connected: %d %v", r.status, r.body)
	}
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	for _, rt := range []struct{ method, body string }{{"PUT", `{"clientId":"` + whopTestApp + `"}`}, {"DELETE", ""}} {
		if r := e.do(t, rt.method, "/api/whop/signin", rt.body, lena.auth()); r.status != http.StatusForbidden {
			t.Errorf("an admin of every server: %s = %d", rt.method, r.status)
		}
	}
	for _, bad := range []string{`{"clientId":"biz_pip"}`, `{"clientId":"app_pip cloud"}`, `{"clientId":"` + whopTestApp + `","clientSecret":"short"}`} {
		if r := e.do(t, "PUT", "/api/whop/signin", bad, own.auth()); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
	r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"whop_secret_0123456789wxyz"}`, own.auth())
	if r.status != http.StatusOK {
		t.Fatalf("setting it up: %d %v", r.status, r.body)
	}
	v := e.whopView(t, own)
	if v.SignIn == nil || v.SignIn.ClientID != whopTestApp || v.SignIn.SecretEnding != "wxyz" || v.SignIn.RedirectURI != whopDashboard+whopSignInCallback {
		t.Fatalf("the view: %+v", v.SignIn)
	}
	if rows := e.auditRows(t, "whop.signin"); len(rows) != 1 || !strings.HasSuffix(rows[0], " panel succeeded Whop app app_pipcloud, with a secret ending wxyz") {
		t.Fatalf("audit: %q", rows)
	}
	if b, _ := json.Marshal(r.body); strings.Contains(string(b), "whop_secret_0123456789") {
		t.Fatal("the secret was sent back")
	}
	if r := e.do(t, "DELETE", "/api/whop/signin", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("turning it off: %d", r.status)
	}
	var st api.SetupStatus
	if e.get(t, "/api/setup/status", "", &st); st.WhopSignIn {
		t.Fatal("still offered once turned off")
	}
}

func stateOf(t *testing.T, link string) string {
	t.Helper()
	return mustURL(t, link).Query().Get("state")
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
