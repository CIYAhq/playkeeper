package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

const (
	whopTestKey    = "apik_pip_hosting_0123456789abcd"
	whopOtherKey   = "apik_other_business_0123456789"
	whopDashboard  = "https://beta.playkeeper.me:8443"
	whopTestSecret = "ws_0123456789abcdef0123456789abcdef"
	// whopTestOwner owns Pip Hosting, and is in each of its support chats.
	whopTestOwner = "user_pip"
	// whopTestAppKey is the Playkeeper Cloud app's key, which reaches each
	// business that installed the app by its id.
	whopTestAppKey = "apik_playkeeper_cloud_app_01234"
)

// fakeBusiness is a business that installed the Playkeeper Cloud app: its
// account, with its owner, and its store. revoked is a business that took
// the app's grant back, which Whop still answers with empty lists, and
// declined the permissions it didn't grant, as when it approved an older
// version of the app.
type fakeBusiness struct {
	account     map[string]any
	products    map[string]whop.Metadata
	plans       []map[string]any
	memberships map[string]map[string]any
	revoked     bool
	declined    []string
	// partner is the user Playkeeper's share goes to, once the app made
	// them the business's partner (aff_<business>), and shares their
	// revenue shares. payments are the business's payments, newest first,
	// fees each one's fee lines, and refunds what it gave back.
	partner  string
	shares   []map[string]any
	payments []map[string]any
	fees     map[string][]map[string]any
	refunds  []map[string]any
	// unlistArchived leaves archived plans out of the plan list, as Whop
	// may, though each is still read by its id. sharesDown makes listing
	// the partner's revenue shares fail.
	unlistArchived bool
	sharesDown     bool
}

// fakeChat is an installed business's support chat with one customer.
type fakeChat struct{ account, user string }

// fakeWhop answers like Whop's API for one seller, Pip Hosting, whose store
// sells one product with two plans: Starter, whose metadata says what it
// allows, and Big, whose metadata doesn't.
type fakeWhop struct {
	mu      sync.Mutex
	srv     *httptest.Server
	missing map[string]bool
	// permissionsDown makes Whop's permission check fail, membershipsDown
	// an installed business's list of memberships, patchDown its product
	// updates, refuseProduct those of one product, and revoked refuses the
	// test key. lostReply is a product whose next update Whop makes but
	// answers with an error, as when its answer is lost.
	permissionsDown, membershipsDown, patchDown, revoked bool
	refuseProduct, lostReply                             string
	products                                             map[string]whop.Metadata
	plans                                                []map[string]any
	patches                                              []string
	keysSeen                                             map[string]bool
	// webhooks are the endpoints the dashboard added, by id; hooksDown
	// makes adding one fail.
	webhooks  map[string]map[string]any
	hooksDown bool
	// memberships are the store's, by id; users the buyers' usernames.
	memberships map[string]map[string]any
	users       map[string]string
	// messages are what was sent to each support chat, and senders who sent
	// each; chatDown makes opening one fail.
	messages map[string][]string
	senders  map[string][]string
	chatDown bool
	// tokens are the user tokens Whop gave, by token; tokenDown makes
	// getting one fail.
	tokens    map[string]fakeToken
	tokenDown bool
	// stockSets are the stocks the dashboard set, as plan=n; stockDown
	// makes setting one fail, and listDown listing memberships.
	stockSets []string
	stockDown bool
	listDown  bool
	// priceSets are the monthly prices sellers set, as plan=price, and
	// priceDown makes Whop refuse setting one. marksDown makes an installed
	// business refuse a product's new metadata, and showDown a plan's
	// visibility.
	priceSets []string
	priceDown bool
	marksDown bool
	showDown  bool
	// shareWrites are the revenue shares the app added or set, as
	// "add <product> <percent>" or "set <share> <percent>".
	shareWrites []string
	// refuseFilter refuses listing memberships by plan, as a Whop that
	// doesn't know the filter might.
	refuseFilter bool
	// requests counts what the dashboard asked, and planReads its reads of
	// the store's plans, which only reading the store does. asked is each
	// request's method and path.
	requests, planReads int
	asked               []string
	// grants are the sign-ins Whop approved, by code, and revokedTokens
	// the refresh tokens ended. noTokenExchange is an app without the
	// oauth:token_exchange permission on Whop.
	grants          map[string]oauthGrant
	revokedTokens   []string
	noTokenExchange bool
	// installed are the businesses that installed the Playkeeper Cloud app,
	// by id, and chats their support chats, by id.
	installed map[string]*fakeBusiness
	chats     map[string]fakeChat
	// tokenKey signs the tokens Whop's proxy adds to a seller's page, and
	// team the users on each installed business's team besides its owner.
	tokenKey *ecdsa.PrivateKey
	team     map[string][]string
	// redirects are the redirect URLs the sign-in app lists; nil lists the
	// dashboard's address at the panel's port alone. unsure are those whose
	// check Whop answers with neither a sign-in page nor a refusal.
	redirects, unsure []string
}

// oauthGrant is one sign-in Whop approved: who, for which app and
// redirect, and the PKCE challenge the code must be traded with.
type oauthGrant struct {
	user, clientID, redirect, challenge string
}

// fakeToken is a user token Whop gave: the user it acts as, inside which
// account, and what it may do.
type fakeToken struct {
	user, account string
	actions       []string
}

// approve does what Whop does when someone signs in: it checks the link the
// dashboard sent the browser to and answers with where Whop sends them back.
func (f *fakeWhop) approve(t *testing.T, authorize, user string) string {
	t.Helper()
	u, err := url.Parse(authorize)
	if err != nil || u.Path != "/oauth/authorize" {
		t.Fatalf("not Whop's sign-in: %q", authorize)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != whop.SignInScope || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("sign-in link: %s", authorize)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("code_%d", len(f.grants)+1)
	if f.grants == nil {
		f.grants = map[string]oauthGrant{}
	}
	f.grants[code] = oauthGrant{user: user, clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
	return q.Get("redirect_uri") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
}

func (f *fakeWhop) serveOAuth(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	refuse := func(code, why string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": why})
	}
	refuseApp := func(why string) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client", "error_description": why})
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /oauth/authorize":
		// A browser leaving to sign in goes on to Whop's sign-in page, when
		// the app lists the redirect URL it names.
		q := r.URL.Query()
		listed := f.redirects
		if listed == nil {
			listed = []string{whopDashboard + whopSignInCallback}
		}
		switch {
		case q.Get("client_id") != whopTestApp:
			refuse("invalid_request", "client_id is invalid")
		case slices.Contains(f.unsure, q.Get("redirect_uri")):
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "<html>Bad gateway</html>")
		case !slices.Contains(listed, q.Get("redirect_uri")):
			refuse("invalid_request", "redirect_uri is invalid")
		default:
			http.Redirect(w, r, "https://whop.com/oauth/authorize?"+r.URL.RawQuery, http.StatusFound)
		}
	case "POST /oauth/token":
		// Whop checks the app before the code, and wants the app's secret.
		switch {
		case body["client_id"] != whopTestApp:
			refuseApp("Unknown client")
			return
		case body["client_secret"] == "":
			refuseApp("client_secret is required")
			return
		case body["client_secret"] != whopTestAppSecret:
			refuseApp("client_secret is invalid")
			return
		case f.noTokenExchange:
			refuseApp("client_secret lacks oauth:token_exchange permission")
			return
		}
		g, ok := f.grants[body["code"]]
		delete(f.grants, body["code"])
		switch {
		case !ok:
			refuse("invalid_grant", "Authorization code has expired")
		case body["grant_type"] != "authorization_code" || body["client_id"] != g.clientID || body["redirect_uri"] != g.redirect:
			refuse("invalid_request", "That code isn't for this app")
		case whop.Challenge(body["code_verifier"]) != g.challenge:
			refuse("invalid_grant", "PKCE verification failed")
		default:
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at_" + g.user, "refresh_token": "rt_" + g.user, "token_type": "bearer", "expires_in": 3600})
		}
	case "GET /oauth/userinfo":
		user, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer at_")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"sub": user, "preferred_username": f.users[user]})
	case "POST /oauth/revoke":
		f.revokedTokens = append(f.revokedTokens, body["token"])
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFakeWhop(t *testing.T) *fakeWhop {
	t.Helper()
	f := &fakeWhop{missing: map[string]bool{}, keysSeen: map[string]bool{}, webhooks: map[string]map[string]any{},
		memberships: map[string]map[string]any{}, users: map[string]string{"user_alex": "alexplays"}, messages: map[string][]string{},
		senders: map[string][]string{}, tokens: map[string]fakeToken{}, installed: map[string]*fakeBusiness{}, chats: map[string]fakeChat{}, team: map[string][]string{},
		products: map[string]whop.Metadata{"prod_mc": {"color": "green"}},
		plans: []map[string]any{
			{"id": "plan_starter", "title": "Starter", "visibility": "hidden", "plan_type": "renewal", "billing_period": 30, "formatted_price": "$8.00 / month",
				"renewal_price": 8, "trial_period_days": 3, "product": map[string]any{"id": "prod_mc", "title": "Minecraft server"},
				"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}, "unlimited_stock": true},
			{"id": "plan_big", "title": "Big", "visibility": "hidden", "plan_type": "renewal", "billing_period": 30, "currency": "usd", "renewal_price": 16,
				"product": map[string]any{"id": "prod_mc", "title": "Minecraft server"}, "metadata": map[string]any{}, "unlimited_stock": true},
			{"id": "plan_old", "title": "Old", "visibility": "archived", "product": map[string]any{"id": "prod_mc"}},
		}}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.tokenKey = key
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWhop) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.asked = append(f.asked, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/.well-known/jwks.json" {
		w.Write(whop.UserTokenKeys(whopTestTokenKid, &f.tokenKey.PublicKey))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/oauth/") {
		f.serveOAuth(w, r)
		return
	}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.keysSeen[key] = true
	w.Header().Set("Content-Type", "application/json")
	if tok, ok := f.tokens[key]; ok {
		f.serveAsUser(w, r, tok)
		return
	}
	if key == whopTestAppKey && strings.HasPrefix(r.URL.Path, "/users/") && strings.Contains(r.URL.Path, "/access/") {
		f.serveAccess(w, r)
		return
	}
	if key == whopTestAppKey {
		f.serveInstalled(w, r)
		return
	}
	account := map[string]any{"id": "biz_pip", "title": "Pip Hosting", "route": "pip-hosting",
		"owner": map[string]any{"id": whopTestOwner, "username": "pipowner", "name": "Pip"}}
	switch {
	case key == whopTestKey && !f.revoked:
	case key == whopOtherKey:
		account = map[string]any{"id": "biz_other", "title": "Other", "route": "other",
			"owner": map[string]any{"id": "user_otherowner", "username": "otherowner", "name": "Other"}}
	default:
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"type":"authentication_error","message":"Invalid API key"}}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /accounts/me":
		json.NewEncoder(w).Encode(account)
	case "GET /permissions":
		if f.permissionsDown {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
			return
		}
		var data []map[string]any
		for _, a := range strings.Split(r.URL.Query().Get("actions"), ",") {
			data = append(data, map[string]any{"action": a, "granted": !f.missing[a]})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "GET /products":
		var data []map[string]any
		for _, id := range slices.Sorted(maps.Keys(f.products)) {
			data = append(data, map[string]any{"id": id, "title": "Minecraft server", "metadata": f.products[id]})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})
	case "GET /variants":
		f.planReads++
		json.NewEncoder(w).Encode(map[string]any{"data": f.plans, "page_info": map[string]any{"has_next_page": false}})
	case "POST /webhooks":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["api_version"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"api_version is no longer supported. New webhooks always use the v1 events; pin payload shapes with api_version_date instead."}}`)
			return
		}
		if f.hooksDown {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
			return
		}
		id := "hook_" + strings.Repeat("x", len(f.webhooks)+1)
		body["id"] = id
		f.webhooks[id] = body
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": body["url"], "webhook_secret": whopTestSecret})
	case "GET /memberships":
		if f.listDown {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
			return
		}
		plan := r.URL.Query().Get("plan_id")
		if plan != "" && f.refuseFilter {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Unknown parameter: plan_id"}}`)
			return
		}
		var data []map[string]any
		for _, m := range f.memberships {
			if plan == "" || m["plan_id"] == plan {
				data = append(data, m)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})
	case "POST /support_channels":
		if f.chatDown {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{"id": "chan_" + body["user_id"]})
	case "POST /access_tokens":
		var body struct {
			AccountID string   `json:"account_id"`
			UserID    string   `json:"user_id"`
			Actions   []string `json:"scoped_actions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		owner, _ := account["owner"].(map[string]any)
		var lacking []string
		for _, a := range body.Actions {
			if f.missing[a] {
				lacking = append(lacking, a)
			}
		}
		switch {
		case f.tokenDown:
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
		case len(lacking) > 0:
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "forbidden",
				"message": "Actor is missing all required permissions: " + strings.Join(lacking, ", ")}})
		case body.AccountID != account["id"] || body.UserID != owner["id"] || len(body.Actions) == 0:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"type":"forbidden","message":"You do not have permission to access this resource"}}`)
		default:
			token := fmt.Sprintf("ut_%d", len(f.tokens)+1)
			f.tokens[token] = fakeToken{user: body.UserID, account: body.AccountID, actions: body.Actions}
			json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": "2026-09-30T13:00:00Z"})
		}
	case "POST /messages":
		// Messages come from people: Whop refuses one sent with an API key,
		// whatever the key may do, in these words.
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Unauthorized: Actor is missing all required permissions: support_chat:message:create"}}`)
	default:
		if id, ok := strings.CutPrefix(r.URL.Path, "/products/"); ok && r.Method == "PATCH" && f.products[id] != nil {
			if f.patchDown || id == f.refuseProduct {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, `{"error":{"type":"server_error","message":"Try again"}}`)
				return
			}
			var body struct {
				Metadata whop.Metadata `json:"metadata"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			f.products[id] = body.Metadata
			f.patches = append(f.patches, body.Metadata[whop.MetaDashboard])
			if id == f.lostReply {
				f.lostReply = ""
				w.WriteHeader(http.StatusGatewayTimeout)
				io.WriteString(w, `{"error":{"type":"server_error","message":"Try again"}}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id})
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/webhooks/"); ok {
			hook, found := f.webhooks[id]
			switch {
			case !found:
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":{"type":"not_found","message":"No such webhook"}}`)
			case r.Method == "PATCH":
				json.NewDecoder(r.Body).Decode(&hook)
				json.NewEncoder(w).Encode(hook)
			case r.Method == "DELETE":
				delete(f.webhooks, id)
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/memberships/"); ok && r.Method == "GET" {
			if m, found := f.memberships[id]; found {
				json.NewEncoder(w).Encode(m)
				return
			}
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/users/"); ok && r.Method == "GET" {
			if name, found := f.users[id]; found {
				json.NewEncoder(w).Encode(map[string]any{"id": id, "username": name})
				return
			}
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/variants/"); ok && r.Method == "PATCH" {
			if f.stockDown {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, `{"error":{"type":"server_error","message":"Try again"}}`)
				return
			}
			var body struct {
				Stock          *int  `json:"stock"`
				UnlimitedStock *bool `json:"unlimited_stock"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if p := f.plan(id); p != nil && body.Stock != nil && body.UnlimitedStock != nil {
				p["stock"], p["unlimited_stock"] = *body.Stock, *body.UnlimitedStock
				f.stockSets = append(f.stockSets, fmt.Sprintf("%s=%d", id, *body.Stock))
				json.NewEncoder(w).Encode(p)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"not_found","message":"No such route"}}`)
	}
}

// serveAsUser answers a request made with a user token, which may only send
// a message in a support chat its user is in, the store's owner or its
// customer, and only if the token may send messages; f.mu must be held.
func (f *fakeWhop) serveAsUser(w http.ResponseWriter, r *http.Request, tok fakeToken) {
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	channel := body["channel_id"]
	customer, _ := strings.CutPrefix(channel, "chan_")
	inChat := tok.account == "biz_pip" && (tok.user == whopTestOwner || tok.user == customer)
	if chat, ok := f.chats[channel]; ok {
		owner, _ := f.installed[chat.account].account["owner"].(map[string]any)
		inChat = tok.account == chat.account && (tok.user == owner["id"] || tok.user == chat.user)
	}
	if r.Method+" "+r.URL.Path != "POST /messages" || !slices.Contains(tok.actions, whop.MessageAction) || !inChat {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"type":"forbidden","message":"You do not have permission to access this resource"}}`)
		return
	}
	f.messages[channel] = append(f.messages[channel], body["content"])
	f.senders[channel] = append(f.senders[channel], tok.user)
	json.NewEncoder(w).Encode(map[string]any{"id": "msg_sent"})
}

func (f *fakeWhop) dashboardMeta() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.products["prod_mc"][whop.MetaDashboard]
}

// plan is the fake's plan id, nil when there's none; f.mu must be held.
func (f *fakeWhop) plan(id string) map[string]any {
	for _, p := range f.plans {
		if p["id"] == id {
			return p
		}
	}
	return nil
}

// setStock changes a plan's stock on Whop as its seller could by hand, or
// makes it unlimited for n < 0.
func (f *fakeWhop) setStock(id string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.plan(id)
	p["stock"], p["unlimited_stock"] = max(n, 0), n < 0
}

// stockOf is a plan's stock on Whop, -1 when it's unlimited.
func (f *fakeWhop) stockOf(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.plan(id)
	if p["unlimited_stock"] != false {
		return -1
	}
	n, _ := p["stock"].(int)
	return n
}

func (f *fakeWhop) stockWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stockSets...)
}

// newWhopEnv is a dashboard whose Sell on Whop talks to f, on a machine
// whose address is beta.playkeeper.me with a certificate good for a while.
func newWhopEnv(t *testing.T, f *fakeWhop) *env {
	t.Helper()
	e := newEnvConfig(t, func(c *config.Config) { c.WhopAPIURL = f.srv.URL }, nil)
	e.setAddress(t, "beta.playkeeper.me")
	e.reply("GET", "/v1/servers", bothServers)
	return e
}

// setAddress makes the agent answer that the machine's address is host,
// with a certificate for it good for 60 days, or no address for "".
func (e *env) setAddress(t *testing.T, host string) {
	t.Helper()
	if host == "" {
		e.replyStatus("GET", "/v1/address", 200, `{"kind":"","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{}}`)
		return
	}
	after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	e.replyStatus("GET", "/v1/address", 200, `{"kind":"playkeeper","host":"`+host+`","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
		"certificate":{"names":["`+host+`"],"challenge":"dns-01","notAfter":"`+after+`"}}`)
}

func (e *env) whopView(t *testing.T, m member) whopView {
	t.Helper()
	var v whopView
	if st := e.get(t, "/api/whop", m.cookie, &v); st != http.StatusOK {
		t.Fatalf("GET /api/whop: %d", st)
	}
	return v
}

func TestSellOnWhopIsTheOwnersAlone(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	owner(t, e)
	lena := addAdmin(t, e, "lena", "*")
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/whop", ""}, {"POST", "/api/whop/connect", `{"key":"` + whopTestKey + `"}`}, {"POST", "/api/whop/sync", ""},
		{"PUT", "/api/whop/plans/plan_big", `{"servers":1,"memoryMB":4096}`}, {"DELETE", "/api/whop", ""},
	} {
		if r := e.do(t, rt.method, rt.path, rt.body, lena.auth()); r.status != http.StatusForbidden {
			t.Errorf("an admin of every server: %s %s = %d %v", rt.method, rt.path, r.status, r.body)
		}
	}
	if len(f.keysSeen) != 0 {
		t.Fatalf("Whop was asked for a non-owner: %v", f.keysSeen)
	}
}

func TestConnectingReadsTheStoreAndMarksItsProducts(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	v := e.whopView(t, own)
	if v.Connected || v.Dashboard != whopDashboard || len(v.Needs) != len(whop.Needs) || len(v.Plans) != 0 {
		t.Fatalf("before connecting: %+v", v)
	}

	r := e.do(t, "POST", "/api/whop/connect", `{"key":"  `+whopTestKey+`  "}`, own.auth())
	if r.status != http.StatusOK || r.body["connected"] != true || r.body["keyEnding"] != "abcd" {
		t.Fatalf("connect: %d %v", r.status, r.body)
	}
	if strings.Contains(mustJSON(t, r.body), whopTestKey) {
		t.Fatal("the answer names the key")
	}
	v = e.whopView(t, own)
	if v.Account == nil || v.Account.ID != "biz_pip" || v.Account.Title != "Pip Hosting" || v.ConnectedBy != "admin" || v.Problem != "" || v.SyncedAt == nil {
		t.Fatalf("connected: %+v", v)
	}
	want := []whopPlanView{
		{ID: "plan_starter", ProductID: "prod_mc", ProductTitle: "Minecraft server", Title: "Starter", Price: "$8.00 / month", Visibility: "hidden", TrialDays: 3,
			Allowance: invites.Allowance{Servers: 1, MemoryMB: 4096}, AllowanceFrom: "store"},
		{ID: "plan_big", ProductID: "prod_mc", ProductTitle: "Minecraft server", Title: "Big", Price: "USD 16.00 every 30 days", Visibility: "hidden"},
	}
	if mustJSON(t, v.Plans) != mustJSON(t, want) {
		t.Fatalf("plans:\n%+v\nwant\n%+v", v.Plans, want)
	}
	if got := f.dashboardMeta(); got != whopDashboard || f.products["prod_mc"]["color"] != "green" {
		t.Fatalf("the product's metadata: %v", f.products["prod_mc"])
	}
	var stored string
	e.srv.db.QueryRow(`SELECT api_key FROM whop_stores`).Scan(&stored)
	if stored != whopTestKey {
		t.Fatalf("stored key %q", stored)
	}
	rows := e.auditRows(t, "whop.connect")
	if len(rows) != 1 || !strings.Contains(rows[0], "succeeded key ending abcd") || strings.Contains(rows[0], whopTestKey) {
		t.Fatalf("audit: %v", rows)
	}

	// Reading again changes nothing on Whop when nothing changed.
	patches := len(f.patches)
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK || len(f.patches) != patches {
		t.Fatalf("sync: %d %v, %d new patches", r.status, r.body, len(f.patches)-patches)
	}
	// A key of another business needs a disconnect first.
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopOtherKey+`"}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("another business's key: %d %v", r.status, r.body)
	}
}

func TestKeysWhopRefusesOrThatLackPermissionsAreNotKept(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	for _, key := range []string{"short", "has a space in it 0123456789"} {
		if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+key+`"}`, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != "whop_key_refused" {
			t.Fatalf("key %q: %d %v", key, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"apik_wrong_0123456789abcdef"}`, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != "whop_key_refused" {
		t.Fatalf("a key Whop refuses: %d %v", r.status, r.body)
	}
	f.missing["support_chat:create"], f.missing["developer:manage_webhook"] = true, true
	r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	params, _ := r.body["params"].(map[string]any)
	if r.status != http.StatusBadRequest || r.body["code"] != "whop_permissions" || params["missing"] != "developer:manage_webhook,support_chat:create" {
		t.Fatalf("a key without every permission: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); v.Connected {
		t.Fatalf("a refused key was kept: %+v", v)
	}
	if f.dashboardMeta() != "" {
		t.Fatal("a refused key marked the store")
	}
	// When Whop won't say what a key may do, the key is kept: the work
	// itself shows what's missing.
	f.mu.Lock()
	f.missing = map[string]bool{}
	f.permissionsDown = true
	f.mu.Unlock()
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth()); r.status != http.StatusOK || r.body["connected"] != true {
		t.Fatalf("connect while Whop's permission check fails: %d %v", r.status, r.body)
	}
}

func TestAPlanWithoutMetadataGetsItsAllowanceHere(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	for _, tc := range []struct {
		plan, body string
		status     int
	}{
		{"plan_starter", `{"servers":2,"memoryMB":8192}`, http.StatusConflict},
		{"plan_nope", `{"servers":2,"memoryMB":8192}`, http.StatusNotFound},
		{"plan_big", `{"servers":11,"memoryMB":8192}`, http.StatusBadRequest},
		{"plan_big", `{"servers":1,"memoryMB":1000}`, http.StatusBadRequest},
		{"plan_big", `{"servers":2,"memoryMB":8192}`, http.StatusOK},
	} {
		if r := e.do(t, "PUT", "/api/whop/plans/"+tc.plan, tc.body, own.auth()); r.status != tc.status {
			t.Fatalf("PUT %s %s: %d %v", tc.plan, tc.body, r.status, r.body)
		}
	}
	v := e.whopView(t, own)
	if v.Plans[1].Allowance != (invites.Allowance{Servers: 2, MemoryMB: 8192}) || v.Plans[1].AllowanceFrom != "owner" {
		t.Fatalf("Big: %+v", v.Plans[1])
	}
	// What the owner set outlasts reading the store again.
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	if v := e.whopView(t, own); v.Plans[1].AllowanceFrom != "owner" || v.Plans[1].Allowance.Servers != 2 {
		t.Fatalf("Big after a sync: %+v", v.Plans[1])
	}
	if rows := e.auditRows(t, "whop.plan"); len(rows) != 1 || !strings.Contains(rows[0], "Big: creator: up to 2 servers with 8192 MB") {
		t.Fatalf("audit: %v", rows)
	}
	if r := e.do(t, "PUT", "/api/whop/plans/plan_big", `{"servers":0,"memoryMB":0}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("clearing Big: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); v.Plans[1].AllowanceFrom != "" || !v.Plans[1].Allowance.IsZero() {
		t.Fatalf("Big cleared: %+v", v.Plans[1])
	}
}

func TestWithoutAnAddressTheStoreStaysClosed(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	e.setAddress(t, "")
	own := owner(t, e)
	r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	if r.status != http.StatusOK || r.body["dashboard"] != "" || !strings.Contains(r.body["problem"].(string), "no address") {
		t.Fatalf("connect without an address: %d %v", r.status, r.body)
	}
	if f.dashboardMeta() != "" {
		t.Fatal("the store was marked without an address")
	}
	// A certificate that lapsed counts as none.
	e.setAddress(t, "beta.playkeeper.me")
	e.clock.add(61 * 24 * time.Hour)
	if v := e.whopView(t, signIn(t, e, own.id)); v.Dashboard != "" {
		t.Fatalf("a lapsed certificate: %+v", v.Dashboard)
	}
}

func TestDisconnectingClosesTheStoreAndForgetsTheKey(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	if f.dashboardMeta() != whopDashboard {
		t.Fatal("not marked after connecting")
	}
	r := e.do(t, "DELETE", "/api/whop", "", own.auth())
	if r.status != http.StatusOK || r.body["connected"] != false {
		t.Fatalf("disconnect: %d %v", r.status, r.body)
	}
	if _, ok := f.products["prod_mc"][whop.MetaDashboard]; ok || f.products["prod_mc"]["color"] != "green" {
		t.Fatalf("the product after disconnecting: %v", f.products["prod_mc"])
	}
	var n int
	e.srv.db.QueryRow(`SELECT (SELECT COUNT(*) FROM whop_stores) + (SELECT COUNT(*) FROM whop_plans)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after disconnecting", n)
	}
	if rows := e.auditRows(t, "whop.disconnect"); len(rows) != 1 {
		t.Fatalf("audit: %v", rows)
	}
}

// A store that still names this dashboard keeps taking orders, so a
// disconnect that can't close it keeps the key for another try, unless
// Whop no longer takes the key, which then can't close it either.
func TestADisconnectThatCantCloseTheStoreKeepsTheKey(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	f.mu.Lock()
	f.patchDown = true
	f.mu.Unlock()
	if r := e.do(t, "DELETE", "/api/whop", "", own.auth()); r.status != http.StatusBadGateway {
		t.Fatalf("disconnect while Whop fails: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); !v.Connected || f.dashboardMeta() != whopDashboard {
		t.Fatalf("after a disconnect that failed: connected %v, product %q", v.Connected, f.dashboardMeta())
	}
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	r := e.do(t, "DELETE", "/api/whop", "", own.auth())
	if r.status != http.StatusOK || r.body["connected"] != false || !strings.Contains(fmt.Sprint(r.body["notice"]), whop.MetaDashboard) {
		t.Fatalf("disconnect with a key Whop refuses: %d %v", r.status, r.body)
	}
}

// An agent that can't be asked for the address leaves the store as it was,
// rather than closing it as if the machine had none.
func TestAnAgentThatCantBeAskedLeavesTheStoreOpen(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	e.replyStatus("GET", "/v1/address", http.StatusServiceUnavailable, `{"error":"Busy.","code":"busy"}`)
	r := e.do(t, "POST", "/api/whop/sync", "", own.auth())
	if r.status != http.StatusOK || f.dashboardMeta() != whopDashboard || !strings.Contains(fmt.Sprint(r.body["problem"]), "couldn't ask this machine for its address") {
		t.Fatalf("sync while the agent can't be asked: %d %v, product %q", r.status, r.body, f.dashboardMeta())
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
