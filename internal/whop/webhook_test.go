package whop

import (
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
	for status, access := range map[string]bool{"active": true, "past_due": true, "completed": true, "expired": false, "unresolved": false, "": false} {
		if (Membership{Status: status}).HasAccess() != access {
			t.Errorf("%q: access %v", status, !access)
		}
	}
}
