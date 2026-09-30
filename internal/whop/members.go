package whop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
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
// paid period, one cancelled that runs until its period ends, the grace
// period after a failed payment, or a one-time purchase. Cancelled, expired,
// unresolved and drafted ones don't.
func (m Membership) HasAccess() bool {
	switch m.Status {
	case "trialing", "active", "canceling", "past_due", "completed":
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

// PlanMemberships lists the account's memberships of one plan, of every
// status. Whop's two API references name the filter plan_id and plan_ids,
// so both are sent, and when Whop refuses them, or ignores them, the plan's
// are picked from every membership.
func (c *Client) PlanMemberships(ctx context.Context, accountID, planID string) ([]Membership, error) {
	ms, err := list[Membership](ctx, c, "/memberships", url.Values{"account_id": {accountID}, "plan_id": {planID}, "plan_ids": {planID}})
	var e *Error
	if errors.As(err, &e) && (e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity) {
		ms, err = c.Memberships(ctx, accountID)
	}
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ms, func(m Membership) bool { return m.PlanID != planID }), nil
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

// Owner is the one user who owns the account the key belongs to.
func (c *Client) Owner(ctx context.Context) (User, error) {
	var a struct {
		Owner User `json:"owner"`
	}
	err := c.do(ctx, http.MethodGet, "/accounts/me", nil, nil, &a)
	if err == nil && a.Owner.ID == "" {
		err = errors.New("Whop didn't say who owns the account")
	}
	return a.Owner, err
}

// MessageAction is what a token needs to send messages in support chats.
const MessageAction = "support_chat:message:create"

// UserToken gets a token that acts as userID inside the account and may do
// actions alone, for the hour Whop keeps it good. Whop gives a token asked
// for no actions every one the key has, so one is never asked for.
func (c *Client) UserToken(ctx context.Context, accountID, userID string, actions ...string) (string, error) {
	if len(actions) == 0 {
		return "", errors.New("a token needs the actions it may do")
	}
	var t struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/access_tokens", nil, map[string]any{"account_id": accountID, "user_id": userID, "scoped_actions": actions}, &t)
	if err == nil && t.Token == "" {
		err = errors.New("Whop's answer had no token")
	}
	return t.Token, err
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

// SendMessage posts text, in Markdown, to a channel as the user token acts
// as (see UserToken). Whop takes messages from people alone, so the key
// itself can't send one.
func (c *Client) SendMessage(ctx context.Context, token, channelID, text string) error {
	as := *c
	as.Key = token
	return as.do(ctx, http.MethodPost, "/messages", nil, map[string]any{"channel_id": channelID, "content": text}, nil)
}
