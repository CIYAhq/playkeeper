package panel

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// whopTestTokenKid names the key the fake Whop signs a seller page's tokens
// with.
const whopTestTokenKid = "fake-kid-es256"

// serveAccess answers whether a user may manage an installed business: its
// owner and its team may, a buyer is a customer; f.mu must be held.
func (f *fakeWhop) serveAccess(w http.ResponseWriter, r *http.Request) {
	user, biz, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/access/")
	level := "no_access"
	if b := f.installed[biz]; b != nil {
		owner, _ := b.account["owner"].(map[string]any)
		switch {
		case owner["id"] == user || slices.Contains(f.team[biz], user):
			level = "admin"
		default:
			for _, m := range b.memberships {
				if m["user_id"] == user {
					level = "customer"
				}
			}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"has_access": level != "no_access", "access_level": level})
}

// sellerEnv is a dashboard selling for Pip Hosting with its own key, with the
// Playkeeper Cloud app set up, and Other Hosting, which installed the app
// and hasn't opened its page yet.
func sellerEnv(t *testing.T) (*fakeWhop, *env) {
	t.Helper()
	f, e, _ := connectedWhop(t)
	f.installOther()
	if _, err := e.srv.db.Exec(`INSERT INTO whop_app(id, client_id, api_key) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET client_id = excluded.client_id, api_key = excluded.api_key`,
		whopTestApp, whopTestAppKey); err != nil {
		t.Fatal(err)
	}
	return f, e
}

// sellerToken is the token Whop's proxy adds for user on the app's page.
func (f *fakeWhop) sellerToken(e *env, app, user string) string {
	return whop.SignUserToken(f.tokenKey, whopTestTokenKid, app, user, e.clock.now().Add(time.Hour))
}

// openAsSeller is the seller's page opening the store, as it does through
// Whop's proxy: with the token, from the page itself.
func (e *env) openAsSeller(t *testing.T, biz, token string, hdr map[string]string) resp {
	t.Helper()
	h := map[string]string{whop.UserTokenHeader: token, "X-Requested-With": "playkeeper", "Sec-Fetch-Site": "same-origin", "Origin": ""}
	for k, v := range hdr {
		h[k] = v
	}
	return e.do(t, "POST", whopSellerPrefix+biz+"/open", `{}`, h)
}

// A seller opens the Playkeeper Cloud app's page in their Whop dashboard:
// their business becomes an app store, named as its products say, at the
// address its memberships give once there are some, and closed until its
// seller opens it. Opening the page again changes nothing.
func TestASellerOpensTheirStoreFromTheirWhopDashboard(t *testing.T) {
	f, e := sellerEnv(t)
	token := f.sellerToken(e, whopTestApp, "user_otherowner")
	r := e.openAsSeller(t, "biz_other", token, nil)
	store, _ := r.body["store"].(map[string]any)
	if r.status != http.StatusOK || r.body["new"] != true || store["id"] != "biz_other" || store["title"] != "Other Hosting" {
		t.Fatalf("first open: %d %v", r.status, r.body)
	}
	st, ok, err := e.srv.whopStoreByID(t.Context(), "biz_other")
	if err != nil || !ok || st.Via != whopViaApp || st.Title != "Other Hosting" || st.Route != "" || st.ClosedWhy != whopNotOpenYetWhy {
		t.Fatalf("the store: %+v, %v, %v", st, ok, err)
	}
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	if r := e.openAsSeller(t, "biz_other", token, nil); r.status != http.StatusOK || r.body["new"] != false {
		t.Fatalf("opening it again: %d %v", r.status, r.body)
	}
	if acc, err := (&whop.Client{APIURL: f.srv.URL, Key: whopTestAppKey}).Business(t.Context(), "biz_other"); err != nil || acc.Route != "other-hosting" {
		t.Fatalf("once it has a membership, its address: %+v, %v", acc, err)
	}
}

// Only the business's team opens its page, only through Whop's proxy, whose
// token must be for the Playkeeper Cloud app and not expired, and only from
// the page itself.
func TestOnlyTheBusinesssTeamOpensItsPageFromWhop(t *testing.T) {
	f, e := sellerEnv(t)
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	owner := f.sellerToken(e, whopTestApp, "user_otherowner")
	for name, c := range map[string]struct {
		biz, token string
		hdr        map[string]string
		status     int
		code       string
	}{
		"no token":             {"biz_other", "", nil, http.StatusUnauthorized, "whop_token"},
		"another app's token":  {"biz_other", f.sellerToken(e, "app_someoneelse", "user_otherowner"), nil, http.StatusUnauthorized, "whop_token"},
		"an expired token":     {"biz_other", whop.SignUserToken(f.tokenKey, whopTestTokenKid, whopTestApp, "user_otherowner", e.clock.now()), nil, http.StatusUnauthorized, "whop_token"},
		"a buyer":              {"biz_other", f.sellerToken(e, whopTestApp, "user_alex"), nil, http.StatusForbidden, "whop_not_team"},
		"another business":     {"biz_pip", owner, nil, http.StatusForbidden, "whop_not_team"},
		"not from the page":    {"biz_other", owner, map[string]string{"X-Requested-With": ""}, http.StatusForbidden, "forbidden"},
		"from another site":    {"biz_other", owner, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, "forbidden"},
		"not a business":       {"user_otherowner", owner, nil, http.StatusNotFound, ""},
		"not a business's id":  {"biz_x%2Fy", owner, nil, http.StatusNotFound, ""},
		"an unknown key's one": {"biz_other", whop.SignUserToken(f.tokenKey, "k_unknown", whopTestApp, "user_otherowner", e.clock.now().Add(time.Hour)), nil, http.StatusUnauthorized, "whop_token"},
	} {
		r := e.openAsSeller(t, c.biz, c.token, c.hdr)
		if r.status != c.status || (c.code != "" && r.body["code"] != c.code) {
			t.Errorf("%s: %d %v", name, r.status, r.body)
		}
	}
	f.mu.Lock()
	f.team["biz_other"] = []string{"user_mod"}
	f.mu.Unlock()
	if r := e.openAsSeller(t, "biz_other", f.sellerToken(e, whopTestApp, "user_mod"), nil); r.status != http.StatusOK {
		t.Fatalf("someone on the team: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", whopSellerPrefix+"biz_other/open", "", map[string]string{whop.UserTokenHeader: owner, "Origin": ""}); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r.status)
	}
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET api_key = ''`); err != nil {
		t.Fatal(err)
	}
	if r := e.openAsSeller(t, "biz_other", owner, nil); r.status != http.StatusServiceUnavailable {
		t.Fatalf("without the app's key: %d %v", r.status, r.body)
	}
}

// A business that hasn't approved everything the app asks for, as when it
// approved an older version of the app, or its owner approved the app for
// another of their businesses, is told to approve it for this one, with the
// install link, and isn't registered.
func TestABusinessThatHasntApprovedTheAppIsToldToApproveIt(t *testing.T) {
	f, e := sellerEnv(t)
	token := f.sellerToken(e, whopTestApp, "user_otherowner")
	unapproved := func(name string, lacks int) {
		t.Helper()
		r := e.openAsSeller(t, "biz_other", token, nil)
		params, _ := r.body["params"].(map[string]any)
		lacking, _ := params["lacking"].([]any)
		if r.status != http.StatusConflict || r.body["code"] != "whop_not_approved" || params["installUrl"] != "https://whop.com/apps/"+whopTestApp+"/install" || len(lacking) != lacks {
			t.Fatalf("%s: %d %v", name, r.status, r.body)
		}
		if _, ok, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); ok {
			t.Fatalf("%s was registered", name)
		}
	}
	f.mu.Lock()
	f.installed["biz_other"].declined = []string{"member:email:read"}
	f.mu.Unlock()
	unapproved("a business that approved all but one permission", 1)
	f.mu.Lock()
	f.installed["biz_other"].declined, f.installed["biz_other"].revoked = nil, true
	f.mu.Unlock()
	unapproved("a business that approved none", len(whop.AppNeeds))
}

// Whop shows the seller's page inside its own frames, and nothing else of
// the dashboard may be framed by any site.
func TestOnlyTheSellersPageMayBeFramedAndOnlyByWhop(t *testing.T) {
	_, e := sellerEnv(t)
	page := e.do(t, "GET", "/whop/seller/biz_other", "", nil)
	if csp := page.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors https://whop.com https://*.whop.com;") || page.header.Get("X-Frame-Options") != "" {
		t.Fatalf("the seller's page: CSP %q, X-Frame-Options %q", csp, page.header.Get("X-Frame-Options"))
	}
	for _, path := range []string{"/", "/settings/whop", "/whop", "/login"} {
		r := e.do(t, "GET", path, "", nil)
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || r.header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: CSP %q, X-Frame-Options %q", path, csp, r.header.Get("X-Frame-Options"))
		}
	}
}
