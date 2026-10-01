package panel

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

const (
	whopTestApp       = "app_pipcloud"
	whopTestAppSecret = "whop_secret_0123456789wxyz"
)

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
	target = strings.TrimPrefix(strings.TrimPrefix(target, whopDashboard), "https://beta.playkeeper.me")
	res, err := b.c.Get(b.e.ts.URL + target)
	if err != nil {
		b.t.Fatal(err)
	}
	res.Body.Close()
	checkHostOnly(b.t, res)
	return res, res.Header.Get("Location")
}

// plant puts cookie c in the browser for every path on the dashboard, as
// another name under its domain could, or as anyone can in their own
// browser.
func (b *browser) plant(c *http.Cookie) {
	b.t.Helper()
	b.c.Jar.SetCookies(mustURL(b.t, b.e.ts.URL+"/"), []*http.Cookie{{Name: c.Name, Value: c.Value, Path: "/", Secure: true}})
}

// signInWithWhop walks a sign-in as user through Whop, and answers with
// where the dashboard sends them at the end.
func (b *browser) signInWithWhop(f *fakeWhop, user string) string {
	b.t.Helper()
	return b.signInWithWhopFrom(f, whopSignInPath, user)
}

// signInWithWhopFrom is signInWithWhop from start, such as a store's
// sign-in.
func (b *browser) signInWithWhopFrom(f *fakeWhop, start, user string) string {
	b.t.Helper()
	res, authorize := b.visit(start)
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(authorize, f.srv.URL+"/oauth/authorize?") {
		b.t.Fatalf("leaving for Whop: %d to %q", res.StatusCode, authorize)
	}
	_, to := b.visit(f.approve(b.t, authorize, user))
	return to
}

// signedInAs is the account the browser is signed in to, or "".
func (b *browser) signedInAs() string {
	b.t.Helper()
	res, err := b.c.Get(b.e.ts.URL + "/api/auth/me")
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	checkHostOnly(b.t, res)
	var body struct {
		User struct{ Username string } `json:"user"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&body) != nil {
		return ""
	}
	return body.User.Username
}

// sellingWithSignIn is a dashboard selling for Pip Hosting that offers Sign
// in with Whop, with a fake hosting core in which alex's Whop account is
// the account alex.
func sellingWithSignIn(t *testing.T) (*fakeWhop, *env, member, *fakeCore, member) {
	t.Helper()
	f, e, own := connectedWhop(t)
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	core := useFakeCore(e)
	alex := addMember(t, e, "alex", invites.RoleViewer, "*")
	core.accounts[testStore+"/user_alex"] = CustomerAccountInfo{UserID: alex.id, Username: "alex", State: CustomerActive, SignIn: true}
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
	// Lax, since Whop sends the browser back from its own site, and never
	// the state, which goes to Whop and back in the link.
	cookie := signInCookie(res)
	if cookie == nil || notHostOnly(cookie) != "" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 600 ||
		len(cookie.Value) < 43 || strings.Contains(authorize, cookie.Value) || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the sign-in's cookie: %+v, leaving for %s", cookie, authorize)
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

// Someone who bought from two stores signs in to the account of the store
// the sign-in is for, as the link in that store's messages names it.
// Without a store they get neither, since which one isn't guessed, and a
// store they have no account in finds none, whatever they have in another.
// A sign-in that didn't work goes back to the sign-in page for its store.
func TestSignInWithWhopOpensTheStoresAccount(t *testing.T) {
	f, e, _, core, _ := sellingWithSignIn(t)
	alex2 := addMember(t, e, "alex-2", invites.RoleViewer, "*")
	core.mu.Lock()
	core.accounts["biz_other/user_alex"] = CustomerAccountInfo{UserID: alex2.id, Username: "alex-2", State: CustomerActive, SignIn: true}
	core.mu.Unlock()
	for store, want := range map[string]string{testStore: "alex", "biz_other": "alex-2"} {
		b := newBrowser(t, e)
		if to := b.signInWithWhopFrom(f, whopSignInPath+"?store="+store, "user_alex"); to != "/" || b.signedInAs() != want {
			t.Fatalf("signing in for %s: to %q as %q, want %s", store, to, b.signedInAs(), want)
		}
	}
	b := newBrowser(t, e)
	if to := b.signInWithWhop(f, "user_alex"); to != "/login?whop=stores" || b.signedInAs() != "" {
		t.Fatalf("signing in for no store with accounts in two: to %q as %q", to, b.signedInAs())
	}
	b = newBrowser(t, e)
	if to := b.signInWithWhopFrom(f, whopSignInPath+"?store=biz_nobody", "user_alex"); to != "/login?whop=no_account&store=biz_nobody" || b.signedInAs() != "" {
		t.Fatalf("signing in for a store with no account there: to %q as %q", to, b.signedInAs())
	}
	if _, to := newBrowser(t, e).visit(whopSignInPath + "?store=" + url.QueryEscape("biz_pip&x=1")); to != "/login?whop=failed" {
		t.Fatalf("signing in for a store that isn't one: %q", to)
	}
	// An account on its way is on its way at the store it was bought from.
	f.mu.Lock()
	f.users["user_sam"] = "samcrafts"
	f.mu.Unlock()
	f.buy("mem_sam1", "user_sam", "plan_starter", "active")
	core.refuse = errNoHostingCore
	e.reconcile()
	for store, want := range map[string]string{testStore: "starting", "biz_nobody": "no_account"} {
		if to := newBrowser(t, e).signInWithWhopFrom(f, whopSignInPath+"?store="+store, "user_sam"); to != "/login?whop="+want+"&store="+store {
			t.Fatalf("sam signing in for %s, with a purchase on its way at %s: %q", store, testStore, to)
		}
	}
	if rows := e.auditRows(t, "login"); !slices.Contains(rows, "whop:user_alex panel refused signed in with Whop without saying which of their stores it's for") {
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
	// A sign-in is used once, and within ten minutes.
	b = newBrowser(t, e)
	left, authorize := b.visit(whopSignInPath)
	kept := signInCookie(left)
	cb = f.approve(t, authorize, "user_alex")
	if _, to := b.visit(cb); to != "/" {
		t.Fatalf("the first time: %q", to)
	}
	b.plant(kept)
	if _, to := b.visit(cb); to != back("expired") {
		t.Fatalf("the same sign-in again: %q", to)
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
	// The core decides: a paused account it lets in signs in, a suspended one
	// never, and the owner's account is never a customer's.
	alexID := core.accounts[testStore+"/user_alex"].UserID
	core.mu.Lock()
	core.accounts[testStore+"/user_alex"] = CustomerAccountInfo{UserID: alexID, Username: "alex", State: CustomerPaused, SignIn: true}
	core.accounts[testStore+"/user_own"] = CustomerAccountInfo{UserID: own.id, Username: "siya", State: CustomerActive, SignIn: true}
	core.mu.Unlock()
	if to := newBrowser(t, e).signInWithWhop(f, "user_alex"); to != "/" {
		t.Fatalf("a paused account the core lets in: %q", to)
	}
	core.mu.Lock()
	core.accounts[testStore+"/user_alex"] = CustomerAccountInfo{UserID: alexID, Username: "alex", State: CustomerSuspended}
	core.mu.Unlock()
	if to := newBrowser(t, e).signInWithWhop(f, "user_alex"); to != back("suspended") {
		t.Fatalf("a suspended account: %q", to)
	}
	core.mu.Lock()
	core.accounts[testStore+"/user_alex"] = CustomerAccountInfo{UserID: alexID, Username: "alex", State: CustomerPaused}
	core.mu.Unlock()
	if to := newBrowser(t, e).signInWithWhop(f, "user_alex"); to != back("paused") {
		t.Fatalf("an account the core keeps out: %q", to)
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

// A joined machine owns a name under the dashboard's domain, so a
// compromised one can set cookies for the dashboard's name too, all but
// __Host- ones. Planting its own sign-in's cookies in a visitor's browser,
// then sending the visitor back from Whop with its own code and state,
// would sign the visitor in to its customer account (login CSRF).
func TestASignInWithWhopCantBePlantedFromAnotherNameOfTheDomain(t *testing.T) {
	f, e, _, core, _ := sellingWithSignIn(t)
	mallory := addMember(t, e, "mallory", invites.RoleViewer, "*")
	f.mu.Lock()
	f.users["user_mallory"] = "mallorybuilds"
	f.mu.Unlock()
	core.mu.Lock()
	core.accounts[testStore+"/user_mallory"] = CustomerAccountInfo{UserID: mallory.id, Username: "mallory", State: CustomerActive, SignIn: true}
	core.mu.Unlock()
	// Mallory leaves for Whop and says yes there, but keeps the link back.
	left, authorize := newBrowser(t, e).visit(whopSignInPath)
	back := f.approve(t, authorize, "user_mallory")
	visitor := newBrowser(t, e)
	for _, c := range left.Cookies() {
		if !strings.HasPrefix(strings.ToLower(c.Name), "__host-") {
			visitor.plant(c)
		}
	}
	if _, to := visitor.visit(back); to != "/login?whop=expired" || visitor.signedInAs() != "" {
		t.Fatalf("a visitor sent back from Whop with mallory's sign-in planted: to %q, signed in as %q", to, visitor.signedInAs())
	}
}

// The state goes to Whop and comes back in the link, so it's no secret: a
// sign-in finishes only in the browser that started it, by a secret in that
// browser's cookie that never leaves it. A link back that leaks, opened in
// another browser with the state as the cookie, signs nobody in, and leaves
// the sign-in to its own browser.
func TestASignInWithWhopFinishesOnlyInTheBrowserThatStartedIt(t *testing.T) {
	f, e, _, _, _ := sellingWithSignIn(t)
	b := newBrowser(t, e)
	_, authorize := b.visit(whopSignInPath)
	back := f.approve(t, authorize, "user_alex")
	other := newBrowser(t, e)
	other.plant(&http.Cookie{Name: whopSignInCookie, Value: stateOf(t, back)})
	if _, to := other.visit(back); to != "/login?whop=expired" || other.signedInAs() != "" {
		t.Fatalf("the link back in another browser, with the state as its cookie: to %q, signed in as %q", to, other.signedInAs())
	}
	if _, to := b.visit(back); to != "/" || b.signedInAs() != "alex" {
		t.Fatalf("the link back in the browser that left for Whop: to %q, signed in as %q", to, b.signedInAs())
	}
}

// A browser keeps one sign-in cookie: that of the tab that left for Whop
// last. Coming back in an older tab, or through a link back from another
// browser's sign-in, as one made to end someone's sign-in would be, finds no
// sign-in and leaves the cookie, so the newest tab still finishes.
func TestASignInWithWhopThatMatchesNoneLeavesTheCookie(t *testing.T) {
	f, e, _, _, _ := sellingWithSignIn(t)
	b := newBrowser(t, e)
	_, older := b.visit(whopSignInPath)
	_, newer := b.visit(whopSignInPath)
	olderBack, newerBack := f.approve(t, older, "user_alex"), f.approve(t, newer, "user_alex")
	if res, to := b.visit(olderBack); to != "/login?whop=expired" || len(res.Cookies()) != 0 {
		t.Fatalf("coming back in the older tab: to %q, setting %q", to, res.Header.Values("Set-Cookie"))
	}
	_, theirs := newBrowser(t, e).visit(whopSignInPath)
	if res, to := b.visit(f.approve(t, theirs, "user_alex")); to != "/login?whop=expired" || len(res.Cookies()) != 0 {
		t.Fatalf("a link back from another browser's sign-in: to %q, setting %q", to, res.Header.Values("Set-Cookie"))
	}
	if _, to := b.visit(newerBack); to != "/" || b.signedInAs() != "alex" {
		t.Fatalf("coming back in the newer tab after both: to %q, signed in as %q", to, b.signedInAs())
	}
}

// signInCookie is the cookie a sign-in with Whop leaves with, or nil.
func signInCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == whopSignInCookie {
			return c
		}
	}
	return nil
}

func TestSignInWithWhopNeedsAnAppAndTheMachinesAddress(t *testing.T) {
	f, e, own := connectedWhop(t)
	if _, to := newBrowser(t, e).visit(whopSignInPath); to != "/login?whop=off" {
		t.Fatalf("without an app: %q", to)
	}
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	e.setAddress(t, "")
	if _, to := newBrowser(t, e).visit(whopSignInPath); to != "/login?whop=off" {
		t.Fatalf("without an address: %q", to)
	}
	if len(f.grants) != 0 {
		t.Fatal("Whop was asked")
	}
}

// Coming back waits on Whop, after the one-time state is used, and the
// public guard ends the request at its read or write deadline.
func TestSignInWithWhopWaitsOnWhopWithinItsDeadlines(t *testing.T) {
	if l := whopSignInLimits; l.read <= whopCallsFor || l.write <= whopCallsFor {
		t.Fatalf("the route's deadlines (read %v, write %v) end before its calls to Whop may (%v)", l.read, l.write, whopCallsFor)
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
	r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth())
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

// Whop checks an app's secret before anything else when someone comes back
// from signing in, so with one Whop refuses, every sign-in fails. Turning
// it on asks Whop first, and keeps nothing Whop refuses.
func TestSignInWithWhopTurnsOnOnlyWithAnAppWhopTakes(t *testing.T) {
	f, e, own := connectedWhop(t)
	for _, tc := range []struct{ body, why string }{
		{`{"clientId":"` + whopTestApp + `"}`, "Whop refused this app: client_secret is required."},
		{`{"clientId":"` + whopTestApp + `","clientSecret":"whop_secret_someone_elses"}`, "Whop refused this app: client_secret is invalid."},
		{`{"clientId":"app_nope","clientSecret":"` + whopTestAppSecret + `"}`, "Whop refused this app: Unknown client."},
	} {
		r := e.do(t, "PUT", "/api/whop/signin", tc.body, own.auth())
		if r.status != http.StatusBadRequest || r.body["error"] != tc.why {
			t.Fatalf("%s: %d %v", tc.body, r.status, r.body)
		}
	}
	// The app's own secret, while the app lacks the permission Whop wants
	// before it trades a code: the answer names it, and where to add it.
	f.mu.Lock()
	f.noTokenExchange = true
	f.mu.Unlock()
	right := `{"clientId":"` + whopTestApp + `","clientSecret":"` + whopTestAppSecret + `"}`
	r := e.do(t, "PUT", "/api/whop/signin", right, own.auth())
	hint, _ := r.body["hint"].(string)
	if r.status != http.StatusBadRequest || r.body["error"] != "Whop refused this app: client_secret lacks oauth:token_exchange permission." ||
		!strings.Contains(hint, "add oauth:token_exchange on the app's own Permissions tab, not on an API key") {
		t.Fatalf("an app without oauth:token_exchange: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); v.SignIn.ClientID != "" {
		t.Fatalf("an app Whop refused was kept: %+v", v.SignIn)
	}
	if rows := e.auditRows(t, "whop.signin"); len(rows) != 0 {
		t.Fatalf("audit: %q", rows)
	}
	if len(f.grants) != 0 {
		t.Fatal("the check signed someone in")
	}
	f.mu.Lock()
	f.noTokenExchange = false
	f.mu.Unlock()
	if r := e.do(t, "PUT", "/api/whop/signin", right, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the app's own secret: %d %v", r.status, r.body)
	}
	if to := newBrowser(t, e).signInWithWhop(f, "user_alex"); to != "/login?whop=no_account" {
		t.Fatalf("signing in once it's on: %q", to)
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
