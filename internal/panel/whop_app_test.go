package panel

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// whopTestAppHookSecret signs the deliveries of the webhook the owner made
// for the Playkeeper Cloud app.
const whopTestAppHookSecret = "ws_playkeepercloud0123456789abcdef"

// hookApp pastes the secret of the app's webhook, as the owner does once
// they've made it on Whop.
func hookApp(t *testing.T, e *env, own member) {
	t.Helper()
	if r := e.do(t, "PUT", "/api/whop/app", `{"webhookSecret":"`+whopTestAppHookSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("pasting the app's webhook's secret: %d %v", r.status, r.body)
	}
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

// The app's webhook keeps each event for the app store of the business it
// names, hurrying that store's pass alone. The key store's events come
// through its own webhook, and a business that isn't a store here has none,
// so the app's webhook keeps neither. It takes only Whop's deliveries, and
// each once.
func TestTheAppsWebhookKeepsEachEventForItsAppStore(t *testing.T) {
	f, e, own := twoStores(t)
	hookApp(t, e, own)
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

// Without the app's webhook an app store is read every minute. The pass
// after its secret is pasted reads every membership at once, since the
// webhook tells only of what happens from then, and after that the store is
// read every ten minutes, with the webhook telling of what happens in
// between.
func TestAnAppStoreIsReadAtOnceWhenTheAppsWebhookComesThenEveryTenMinutes(t *testing.T) {
	f, e, own := twoStores(t)
	e.reconcile()
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	e.clock.add(time.Minute)
	e.reconcile()
	if got := keptAt(t, e, "mem_other1"); got != "biz_other" {
		t.Fatalf("without the app's webhook, a purchase waited past a minute: %q", got)
	}
	f.buyAt("biz_other", "mem_other2", "user_alex", "plan_other", "active")
	e.clock.add(time.Second)
	hookApp(t, e, own)
	e.reconcile()
	if got := keptAt(t, e, "mem_other2"); got != "biz_other" {
		t.Fatalf("the pass after the webhook came didn't read what came before it: %q", got)
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

// The owner alone sets the app's key and its webhook's secret. The dashboard
// shows the key's ending and whether the secret is there, never either of
// them, and keeps them out of the audit log. An empty one clears it.
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
	r := e.do(t, "PUT", "/api/whop/app", `{"key":" `+whopTestAppKey+` ","webhookSecret":"`+whopTestAppHookSecret+`"}`, own.auth())
	raw, _ := json.Marshal(r.body)
	if r.status != http.StatusOK || strings.Contains(string(raw), whopTestAppKey) || strings.Contains(string(raw), whopTestAppHookSecret) {
		t.Fatalf("setting the app's key and secret: %d %s", r.status, raw)
	}
	var key, secret, audited string
	e.srv.db.QueryRow(`SELECT api_key, webhook_secret FROM whop_app WHERE id = 1`).Scan(&key, &secret)
	e.srv.db.QueryRow(`SELECT group_concat(detail) FROM audit WHERE action = 'whop.app'`).Scan(&audited)
	v := e.whopView(t, own).App
	if key != whopTestAppKey || secret != whopTestAppHookSecret || v.KeyEnding != whop.Ending(whopTestAppKey) || !v.Webhook ||
		v.WebhookURL != whopDashboard+whopAppWebhookPath || strings.Contains(audited, whopTestAppKey) || strings.Contains(audited, whopTestAppHookSecret) {
		t.Fatalf("kept %q and %q, shown %+v, audited %q", key, secret, v, audited)
	}
	if r := e.do(t, "PUT", "/api/whop/app", `{"key":"","webhookSecret":""}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("clearing them: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own).App; v.KeyEnding != "" || v.Webhook {
		t.Fatalf("after clearing them: %+v", v)
	}
}

// Without a secret, the app's webhook isn't there.
func TestWithoutASecretTheAppsWebhookIsNotThere(t *testing.T) {
	_, e, _ := twoStores(t)
	if r := e.do(t, "POST", whopAppWebhookPath, `{}`, map[string]string{"Origin": ""}); r.status != http.StatusNotFound {
		t.Fatalf("the app's webhook without a secret: %d", r.status)
	}
}
