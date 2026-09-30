package whop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Events Sell on Whop hears about.
const (
	EventMembershipActivated   = "membership.activated"
	EventMembershipDeactivated = "membership.deactivated"
	EventMembershipCancelling  = "membership.cancel_at_period_end_changed"
)

// Events lists them, for the webhook the dashboard adds.
var Events = []string{EventMembershipActivated, EventMembershipDeactivated, EventMembershipCancelling}

// Webhook is an endpoint Whop sends events to. Secret is only in the answer
// that made it.
type Webhook struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Secret string `json:"webhook_secret"`
}

// CreateWebhook adds an endpoint at u for membership events, its payloads
// pinned to APIVersion. resource is an account, for its own events, or an
// app, for those of every business that installed it. Whop refuses
// api_version on a new webhook, since every new one gets its v1 events.
func (c *Client) CreateWebhook(ctx context.Context, resource, u string) (Webhook, error) {
	var w Webhook
	err := c.do(ctx, http.MethodPost, "/webhooks", nil, map[string]any{
		"url": u, "events": Events, "api_version_date": APIVersion, "resource_id": resource, "enabled": true,
	}, &w)
	return w, err
}

// UpdateWebhookURL moves an endpoint to u.
func (c *Client) UpdateWebhookURL(ctx context.Context, id, u string) error {
	return c.do(ctx, http.MethodPatch, "/webhooks/"+url.PathEscape(id), nil, map[string]any{"url": u, "enabled": true}, nil)
}

// DeleteWebhook removes an endpoint. One that's gone already counts as
// removed.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/webhooks/"+url.PathEscape(id), nil, nil, nil)
	if NotFound(err) {
		return nil
	}
	return err
}

// Event is one event a webhook delivered.
type Event struct {
	// ID is the delivery's webhook-id, the same on every retry.
	ID   string `json:"-"`
	Type string `json:"type"`
	// AccountID is the business the event happened in. An app's webhook
	// hears from every business that installed the app.
	AccountID string          `json:"account_id"`
	Data      json.RawMessage `json:"data"`
}

func (e *Event) UnmarshalJSON(b []byte) error {
	// A webhook pinned before 2026-08-14, or not pinned at all, as one made
	// on Whop's dashboard can be, names the business company_id.
	var raw struct {
		Type      string          `json:"type"`
		AccountID string          `json:"account_id"`
		CompanyID string          `json:"company_id"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*e = Event{Type: raw.Type, AccountID: raw.AccountID, Data: raw.Data}
	if e.AccountID == "" {
		e.AccountID = raw.CompanyID
	}
	return nil
}

// WebhookTolerance is how far a delivery's timestamp may be from now, as
// Standard Webhooks allows: an older one is a replay.
const WebhookTolerance = 5 * time.Minute

// ErrWebhook refuses a delivery that isn't Whop's or is too old.
var ErrWebhook = errors.New("the delivery isn't signed by Whop, or is too old")

// VerifyWebhook checks a delivery's Standard Webhooks signature with the
// endpoint's secret and its timestamp against now, and reads its event.
// Whop signs "{webhook-id}.{webhook-timestamp}.{body}" with HMAC-SHA256
// keyed by the secret's own bytes, prefix and all, and sends the base64
// result as "v1,<signature>", possibly among others separated by spaces.
func VerifyWebhook(secret string, h http.Header, body []byte, now time.Time) (Event, error) {
	id, ts, sigs := h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature")
	if secret == "" || id == "" || ts == "" || sigs == "" {
		return Event{}, ErrWebhook
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return Event{}, ErrWebhook
	}
	if d := now.Sub(time.Unix(sec, 0)); d > WebhookTolerance || d < -WebhookTolerance {
		return Event{}, ErrWebhook
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	ok := false
	for _, s := range strings.Fields(sigs) {
		v, sig, found := strings.Cut(s, ",")
		if !found || v != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(sig)
		if err == nil && hmac.Equal(got, want) {
			ok = true
		}
	}
	if !ok {
		return Event{}, ErrWebhook
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return Event{}, err
	}
	ev.ID = id
	return ev, nil
}

// SignWebhook signs a delivery as Whop does, for tests and fakes.
func SignWebhook(secret, id string, at time.Time, body []byte) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", ts)
	h.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	h.Set("Content-Type", "application/json")
	return h
}
