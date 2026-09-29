package whop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Membership is a buyer's relationship with one plan: whether it grants
// access now, and when it ends.
type Membership struct {
	ID string `json:"id"`
	// Status is trialing, active, past_due, completed, canceled, expired or
	// unresolved (see HasAccess).
	Status    string `json:"status"`
	PlanID    string `json:"plan_id"`
	ProductID string `json:"product_id"`
	UserID    string `json:"user_id"`
	// CancelAtPeriodEnd is set once the buyer cancelled a plan that still
	// runs until PeriodEnd.
	CancelAtPeriodEnd bool      `json:"cancel_at_period_end"`
	PeriodEnd         time.Time `json:"-"`
}

// HasAccess reports whether the membership grants access now: a trial, a
// paid period, the grace period after a failed payment, or a one-time
// purchase. Cancelled, expired and unresolved ones don't.
func (m Membership) HasAccess() bool {
	switch m.Status {
	case "trialing", "active", "past_due", "completed":
		return true
	}
	return false
}

func (m *Membership) UnmarshalJSON(b []byte) error {
	// Whop's current shape names plan_id, product_id and user_id; payloads
	// from before API versions were pinned nest them as objects.
	var raw struct {
		ID                string     `json:"id"`
		Status            string     `json:"status"`
		PlanID            string     `json:"plan_id"`
		ProductID         string     `json:"product_id"`
		UserID            string     `json:"user_id"`
		CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
		CurrentPeriodEnd  *time.Time `json:"current_period_end"`
		Plan              *Ref       `json:"plan"`
		Product           *Ref       `json:"product"`
		User              *Ref       `json:"user"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Membership{ID: raw.ID, Status: raw.Status, PlanID: raw.PlanID, ProductID: raw.ProductID, UserID: raw.UserID, CancelAtPeriodEnd: raw.CancelAtPeriodEnd}
	if m.PlanID == "" && raw.Plan != nil {
		m.PlanID = raw.Plan.ID
	}
	if m.ProductID == "" && raw.Product != nil {
		m.ProductID = raw.Product.ID
	}
	if m.UserID == "" && raw.User != nil {
		m.UserID = raw.User.ID
	}
	if raw.CurrentPeriodEnd != nil {
		m.PeriodEnd = raw.CurrentPeriodEnd.UTC()
	}
	return nil
}

// Membership reads one membership as it stands.
func (c *Client) Membership(ctx context.Context, id string) (Membership, error) {
	var m Membership
	err := c.do(ctx, http.MethodGet, "/memberships/"+url.PathEscape(id), nil, nil, &m)
	return m, err
}

// Memberships lists the account's memberships, of every status.
func (c *Client) Memberships(ctx context.Context, accountID string) ([]Membership, error) {
	return list[Membership](ctx, c, "/memberships", url.Values{"account_id": {accountID}})
}

// User is a person on Whop, as others see them.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// User reads a user's public profile.
func (c *Client) User(ctx context.Context, id string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(id), nil, nil, &u)
	return u, err
}

// OpenSupportChat opens the account's support chat with a buyer, or finds
// the one there is, and returns its channel.
func (c *Client) OpenSupportChat(ctx context.Context, accountID, userID string) (string, error) {
	var ch struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/support_channels", nil, map[string]any{"account_id": accountID, "user_id": userID}, &ch)
	return ch.ID, err
}

// SendMessage posts text, in Markdown, to a channel.
func (c *Client) SendMessage(ctx context.Context, channelID, text string) error {
	return c.do(ctx, http.MethodPost, "/messages", nil, map[string]any{"channel_id": channelID, "content": text}, nil)
}
