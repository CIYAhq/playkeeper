package whop

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A delivery signed as Whop's backend signs it: HMAC-SHA256 keyed by the
// secret's literal bytes, prefix included, worked out apart from this code.
func TestVerifyWebhookMatchesWhopsSignature(t *testing.T) {
	body := []byte(`{"type":"membership.activated","data":{"id":"mem_1"}}`)
	h := http.Header{}
	h.Set("webhook-id", "msg_2kbXY1")
	h.Set("webhook-timestamp", "1790672400")
	h.Set("webhook-signature", "v1,fYmiq/kEISI4ajQQk0KuF0ccyAQUTBSVF2clhAdTDXQ=")
	ev, err := VerifyWebhook("ws_0123456789abcdef", h, body, time.Unix(1790672400, 0).Add(time.Minute))
	if err != nil || ev.ID != "msg_2kbXY1" || ev.Type != EventMembershipActivated || string(ev.Data) != `{"id":"mem_1"}` {
		t.Fatalf("VerifyWebhook = %+v, %v", ev, err)
	}
}

func TestVerifyWebhookRefuses(t *testing.T) {
	const secret = "ws_0123456789abcdef"
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"type":"membership.activated","data":{}}`)
	good := SignWebhook(secret, "msg_1", now, body)
	if _, err := VerifyWebhook(secret, good, body, now); err != nil {
		t.Fatalf("a good delivery: %v", err)
	}
	with := func(k, v string) http.Header {
		h := good.Clone()
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
		return h
	}
	for name, tc := range map[string]struct {
		secret string
		h      http.Header
		body   []byte
		now    time.Time
	}{
		"another secret":         {"ws_other", good, body, now},
		"a changed body":         {secret, good, []byte(`{"type":"membership.deactivated","data":{}}`), now},
		"too old":                {secret, good, body, now.Add(6 * time.Minute)},
		"from the future":        {secret, good, body, now.Add(-6 * time.Minute)},
		"no id":                  {secret, with("webhook-id", ""), body, now},
		"another id":             {secret, with("webhook-id", "msg_2"), body, now},
		"no signature":           {secret, with("webhook-signature", ""), body, now},
		"another scheme":         {secret, with("webhook-signature", "v2,"+good.Get("webhook-signature")[3:]), body, now},
		"a timestamp that isn't": {secret, with("webhook-timestamp", "soon"), body, now},
		"no secret":              {"", good, body, now},
	} {
		if _, err := VerifyWebhook(tc.secret, tc.h, tc.body, tc.now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// One good signature among others is enough, as the spec allows while
	// a secret rotates.
	if _, err := VerifyWebhook(secret, with("webhook-signature", "v1,AAAA "+good.Get("webhook-signature")), body, now); err != nil {
		t.Errorf("a good signature among others: %v", err)
	}
}

func TestMembershipsReadBothShapes(t *testing.T) {
	var current, nested Membership
	if err := json.Unmarshal([]byte(`{"id":"mem_1","status":"trialing","plan_id":"plan_a","product_id":"prod_mc","user_id":"user_x",
		"cancel_at_period_end":true,"current_period_end":"2026-10-02T09:00:00Z"}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.PlanID != "plan_a" || current.UserID != "user_x" || !current.CancelAtPeriodEnd || !current.PeriodEnd.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) || !current.HasAccess() {
		t.Fatalf("current shape: %+v", current)
	}
	if err := json.Unmarshal([]byte(`{"id":"mem_2","status":"canceled","plan":{"id":"plan_b"},"product":{"id":"prod_mc"},"user":{"id":"user_y","username":"alex"},"current_period_end":null}`), &nested); err != nil {
		t.Fatal(err)
	}
	if nested.PlanID != "plan_b" || nested.ProductID != "prod_mc" || nested.UserID != "user_y" || !nested.PeriodEnd.IsZero() || nested.HasAccess() {
		t.Fatalf("nested shape: %+v", nested)
	}
	for status, access := range map[string]bool{"active": true, "canceling": true, "past_due": true, "completed": true, "expired": false, "unresolved": false, "drafted": false, "": false} {
		if (Membership{Status: status}).HasAccess() != access {
			t.Errorf("%q: access %v", status, !access)
		}
	}
}

// Whop refuses api_version on a new webhook; the payloads are pinned by
// api_version_date alone.
func TestCreateWebhookPinsItsPayloadsByDateAlone(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /webhooks": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("body: %v", err)
			}
			if _, ok := body["api_version"]; ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"api_version is no longer supported. New webhooks always use the v1 events; pin payload shapes with api_version_date instead."}}`))
				return
			}
			events, _ := body["events"].([]any)
			if body["url"] != "https://beta.playkeeper.me:8443/api/public/whop/webhook" || body["api_version_date"] != APIVersion || body["resource_id"] != "biz_pip" ||
				body["enabled"] != true || len(events) != len(Events) {
				t.Errorf("body %v", body)
			}
			answer(map[string]any{"id": "hook_1", "url": body["url"], "api_version": "v1", "api_version_date": APIVersion, "webhook_secret": "ws_abc"})(w, r)
		},
	})
	hook, err := c.CreateWebhook(context.Background(), "biz_pip", "https://beta.playkeeper.me:8443/api/public/whop/webhook")
	if err != nil || hook != (Webhook{ID: "hook_1", URL: "https://beta.playkeeper.me:8443/api/public/whop/webhook", Secret: "ws_abc"}) {
		t.Fatalf("CreateWebhook = %+v, %v", hook, err)
	}
}

// The v1 envelope as Whop documents it for a webhook pinned since
// 2026-08-14, with a membership as the pinned version reads it.
func TestVerifyWebhookReadsTheV1Envelope(t *testing.T) {
	const secret = "ws_0123456789abcdef"
	now := time.Date(2026, 9, 30, 10, 25, 23, 0, time.UTC)
	body := []byte(`{"type":"membership.activated","api_version":"v1","api_version_date":"2026-09-29","timestamp":"2026-09-30T10:25:22.795Z",
		"account_id":"biz_pip","data":{"id":"mem_1","status":"completed","user_id":"user_x","product_id":"prod_mc","plan_id":"plan_a",
		"cancel_at_period_end":false,"current_period_start":null,"current_period_end":null,"metadata":{"playkeeper_servers":"1"}}}`)
	ev, err := VerifyWebhook(secret, SignWebhook(secret, "msg_1", now, body), body, now)
	if err != nil || ev.Type != EventMembershipActivated || ev.AccountID != "biz_pip" {
		t.Fatalf("VerifyWebhook = %+v, %v", ev, err)
	}
	var m Membership
	if err := json.Unmarshal(ev.Data, &m); err != nil || m.ID != "mem_1" || m.PlanID != "plan_a" || m.UserID != "user_x" || !m.HasAccess() || !m.PeriodEnd.IsZero() {
		t.Fatalf("its membership: %+v, %v", m, err)
	}
}
