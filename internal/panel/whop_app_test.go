package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// whopTestAppHookSecret signs the deliveries of the webhook the dashboard
// added for the Playkeeper Cloud app.
const whopTestAppHookSecret = "ws_playkeepercloud0123456789abcdef"

// serveAppWebhooks answers the app's key adding the app's own webhook or
// moving it, which is kept with the key store's in webhooks; f.mu must be
// held.
func (f *fakeWhop) serveAppWebhooks(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	id, _ := strings.CutPrefix(r.URL.Path, "/webhooks/")
	switch {
	case f.appHooksRefused, r.Method == "POST" && body["resource_id"] != whopTestApp:
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"type":"forbidden","message":"You do not have permission to access this resource"}}`)
	case r.Method == "POST" && r.URL.Path == "/webhooks":
		hook := fmt.Sprintf("hook_app%d", f.requests)
		body["id"] = hook
		f.webhooks[hook] = body
		json.NewEncoder(w).Encode(map[string]any{"id": hook, "url": body["url"], "webhook_secret": whopTestAppHookSecret})
	case r.Method == "PATCH" && f.webhooks[id]["resource_id"] == whopTestApp:
		maps.Copy(f.webhooks[id], body)
		json.NewEncoder(w).Encode(f.webhooks[id])
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"not_found","message":"No such webhook"}}`)
	}
}

// appHook is the webhook the dashboard added for the app, nil for none.
func appHook(f *fakeWhop) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.webhooks {
		if h["resource_id"] == whopTestApp {
			return maps.Clone(h)
		}
	}
	return nil
}

// withApp is twoStores with Sign in with Whop's app as the Playkeeper Cloud
// app, so the dashboard knows which app's webhook to add.
func withApp(t *testing.T) (*fakeWhop, *env, member) {
	t.Helper()
	f, e, own := twoStores(t)
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET client_id = ?`, whopTestApp); err != nil {
		t.Fatal(err)
	}
	return f, e, own
}

// deliverApp posts an event of the business account to the app's webhook as
// Whop would, signed with secret at the dashboard's time.
func (e *env) deliverApp(t *testing.T, secret, account, id, event string, m map[string]any) resp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"type": event, "api_version": "v1", "api_version_date": whop.APIVersion,
		"timestamp": e.clock.now().Format(time.RFC3339Nano), "account_id": account, "data": m})
	hdr := map[string]string{"Origin": ""}
	for k, v := range whop.SignWebhook(secret, id, e.clock.now(), body) {
		hdr[k] = v[0]
	}
	return e.do(t, "POST", whopAppWebhookPath, string(body), hdr)
}

// takeKicks takes the stores kicked so far, and the wake they sent.
func takeKicks(e *env) map[string]bool {
	only := e.srv.takeWhopKicks()
	select {
	case <-e.srv.whopKick:
	default:
	}
	return only
}

// keptAt is the store the dashboard keeps a membership for, "" for none.
func keptAt(t *testing.T, e *env, membership string) string {
	t.Helper()
	var store string
	if err := e.srv.db.QueryRow(`SELECT store_id FROM whop_memberships WHERE membership_id = ?`, membership).Scan(&store); err != nil && !isNoRows(err) {
		t.Fatal(err)
	}
	return store
}

// The dashboard adds the app's webhook for the businesses that installed
// the app, and keeps each event for the app store of the business it
// names, hurrying that store's pass alone. The key store's events come
// through its own webhook, and a business that isn't a store here has none,
// so the app's webhook keeps neither. It takes only Whop's deliveries, and
// each once.
func TestTheAppsWebhookKeepsEachEventForItsAppStore(t *testing.T) {
	f, e, _ := withApp(t)
	e.reconcile()
	hook := appHook(f)
	events, _ := hook["events"].([]any)
	if hook == nil || hook["url"] != whopDashboard+whopAppWebhookPath || hook["api_version_date"] != whop.APIVersion || len(events) != len(whop.Events) {
		t.Fatalf("the app's webhook: %v", hook)
	}
	takeKicks(e)
	m := f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	if r := e.deliverApp(t, whopTestAppHookSecret, "biz_other", "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("delivery: %d %v", r.status, r.body)
	}
	if got := keptAt(t, e, "mem_other1"); got != "biz_other" {
		t.Fatalf("Other's membership kept for %q", got)
	}
	if only := takeKicks(e); !maps.Equal(only, map[string]bool{"biz_other": true}) {
		t.Fatalf("Other's delivery hurried %v", only)
	}
	pip := f.buy("mem_pip1", "user_alex", "plan_starter", "active")
	e.deliverApp(t, whopTestAppHookSecret, testStore, "msg_2", whop.EventMembershipActivated, pip)
	e.deliverApp(t, whopTestAppHookSecret, "biz_nobody", "msg_3", whop.EventMembershipActivated,
		map[string]any{"id": "mem_nobody1", "status": "active", "plan_id": "plan_x", "user_id": "user_alex"})
	if got := keptAt(t, e, "mem_pip1") + keptAt(t, e, "mem_nobody1"); got != "" || len(e.srv.whopKick) != 0 {
		t.Fatalf("the app's webhook kept the key store's or a stranger's membership (%q), or hurried a pass (%d)", got, len(e.srv.whopKick))
	}
	m2 := f.buyAt("biz_other", "mem_other2", "user_alex", "plan_other", "active")
	if r := e.deliverApp(t, whopTestSecret, "biz_other", "msg_4", whop.EventMembershipActivated, m2); r.status != http.StatusUnauthorized || keptAt(t, e, "mem_other2") != "" {
		t.Fatalf("a delivery signed with another secret: %d", r.status)
	}
	e.deliverApp(t, whopTestAppHookSecret, "biz_other", "msg_1", whop.EventMembershipActivated, m)
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_deliveries WHERE id = 'msg_1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d deliveries kept for one id", n)
	}
}

// Without the app's webhook an app store is read every minute. The pass that
// adds it reads every membership at once, since the webhook tells only of
// what happens from then, and after that the store is read every ten
// minutes, with the webhook telling of what happens in between.
func TestAnAppStoreIsReadAtOnceWhenTheAppsWebhookComesThenEveryTenMinutes(t *testing.T) {
	f, e, _ := withApp(t)
	f.appHooksRefused = true
	e.reconcile()
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	e.clock.add(time.Minute)
	e.reconcile()
	if got := keptAt(t, e, "mem_other1"); got != "biz_other" {
		t.Fatalf("without the app's webhook, a purchase waited past a minute: %q", got)
	}
	f.appHooksRefused = false
	f.buyAt("biz_other", "mem_other2", "user_alex", "plan_other", "active")
	e.clock.add(time.Second)
	e.reconcile()
	if appHook(f) == nil || keptAt(t, e, "mem_other2") != "biz_other" {
		t.Fatalf("the pass that added the app's webhook didn't read what came before it: webhook %v", appHook(f))
	}
	f.buyAt("biz_other", "mem_other3", "user_alex", "plan_other", "active")
	e.clock.add(time.Minute)
	e.reconcile()
	if got := keptAt(t, e, "mem_other3"); got != "" {
		t.Fatal("with the app's webhook, the store was still read every minute")
	}
	e.clock.add(10 * time.Minute)
	e.reconcile()
	if got := keptAt(t, e, "mem_other3"); got != "biz_other" {
		t.Fatalf("the ten-minute read missed a purchase: %q", got)
	}
}

// When Whop doesn't let the app's key add the app's webhook, Settings › Sell
// on Whop says what to do, and the owner pastes the secret of one they made.
// The dashboard leaves that one alone, even once Whop would let it add its
// own, until the owner clears it.
func TestWhenWhopWontLetTheAppsKeyAddItsWebhookTheOwnerPastesTheSecretOfOneTheyMade(t *testing.T) {
	f, e, own := withApp(t)
	f.appHooksRefused = true
	e.reconcile()
	want := whopDashboard + whopAppWebhookPath
	if v := e.whopView(t, own).App; v.Webhook || v.WebhookURL != want || !strings.Contains(v.Problem, want) || !strings.Contains(v.Problem, "paste its secret here") {
		t.Fatalf("with Whop refusing: %+v", v)
	}
	const secret = "ws_madeonwhop0123456789abcdef"
	r := e.do(t, "PUT", "/api/whop/app", `{"webhookSecret":"`+secret+`"}`, own.auth())
	app, _ := r.body["app"].(map[string]any)
	if r.status != http.StatusOK || app["webhook"] != true || app["webhookBy"] != "owner" || app["problem"] != nil {
		t.Fatalf("pasting its secret: %d %v", r.status, r.body)
	}
	m := f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	if r := e.deliverApp(t, secret, "biz_other", "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK || keptAt(t, e, "mem_other1") != "biz_other" {
		t.Fatalf("a delivery of the owner's webhook: %d", r.status)
	}
	f.appHooksRefused = false
	e.reconcile()
	if hook := appHook(f); hook != nil {
		t.Fatalf("the dashboard added a webhook beside the owner's: %v", hook)
	}
	if r := e.do(t, "PUT", "/api/whop/app", `{"webhookSecret":""}`, own.auth()); r.status != http.StatusOK || e.whopView(t, own).App.Webhook {
		t.Fatalf("clearing it: %d %v", r.status, r.body)
	}
	e.reconcile()
	if v := e.whopView(t, own).App; appHook(f) == nil || !v.Webhook || v.WebhookBy != "dashboard" {
		t.Fatalf("once cleared, the dashboard's own: %+v", v)
	}
}

// The webhook the dashboard added follows the dashboard's address, and one
// gone from Whop is added again.
func TestTheAppsWebhookFollowsTheDashboardsAddress(t *testing.T) {
	f, e, own := withApp(t)
	e.reconcile()
	first := appHook(f)
	e.setAddress(t, "play.pip.gg")
	e.reconcile()
	if hook := appHook(f); hook == nil || hook["id"] != first["id"] || hook["url"] != "https://play.pip.gg:8443"+whopAppWebhookPath || !e.whopView(t, own).App.Webhook {
		t.Fatalf("after the address changed: %v", hook)
	}
	f.mu.Lock()
	delete(f.webhooks, first["id"].(string))
	f.mu.Unlock()
	e.setAddress(t, "beta.playkeeper.me")
	e.reconcile()
	if hook := appHook(f); hook == nil || hook["id"] == first["id"] || hook["url"] != whopDashboard+whopAppWebhookPath {
		t.Fatalf("after Whop lost it: %v", hook)
	}
}

// The owner alone sets the app's key. The dashboard shows its ending, never
// the key, and keeps the key out of the audit log. An empty key clears it.
func TestOnlyTheOwnerSetsTheAppsKeyAndOnlyItsEndingShows(t *testing.T) {
	_, e, own := connectedWhop(t)
	viewer := addMember(t, e, "vic", invites.RoleViewer, "*")
	if r := e.do(t, "PUT", "/api/whop/app", `{"key":"`+whopTestAppKey+`"}`, viewer.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a viewer setting the app's key: %d", r.status)
	}
	for _, body := range []string{`{}`, `{"key":"not a key"}`, `{"webhookSecret":"short"}`} {
		if r := e.do(t, "PUT", "/api/whop/app", body, own.auth()); r.status != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, r.status)
		}
	}
	r := e.do(t, "PUT", "/api/whop/app", `{"key":" `+whopTestAppKey+` "}`, own.auth())
	raw, _ := json.Marshal(r.body)
	if r.status != http.StatusOK || strings.Contains(string(raw), whopTestAppKey) {
		t.Fatalf("setting the app's key: %d %s", r.status, raw)
	}
	var key, audited string
	e.srv.db.QueryRow(`SELECT api_key FROM whop_app WHERE id = 1`).Scan(&key)
	e.srv.db.QueryRow(`SELECT group_concat(detail) FROM audit WHERE action = 'whop.app'`).Scan(&audited)
	if v := e.whopView(t, own).App; key != whopTestAppKey || v.KeyEnding != whop.Ending(whopTestAppKey) || strings.Contains(audited, whopTestAppKey) {
		t.Fatalf("kept %q, shown %+v, audited %q", key, v, audited)
	}
	if r := e.do(t, "PUT", "/api/whop/app", `{"key":""}`, own.auth()); r.status != http.StatusOK || e.whopView(t, own).App.KeyEnding != "" {
		t.Fatalf("clearing the app's key: %d %v", r.status, r.body)
	}
}

// Without a secret, the app's webhook isn't there.
func TestWithoutASecretTheAppsWebhookIsNotThere(t *testing.T) {
	_, e, _ := twoStores(t)
	if r := e.do(t, "POST", whopAppWebhookPath, `{}`, map[string]string{"Origin": ""}); r.status != http.StatusNotFound {
		t.Fatalf("the app's webhook without a secret: %d", r.status)
	}
}
