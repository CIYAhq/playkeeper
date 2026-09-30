package whop

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Payment is one charge on a business: a first purchase, a renewal, or a
// one-time payment.
type Payment struct {
	ID string `json:"id"`
	// Status is "paid" once the money moved; open, pending, void and
	// the like before or instead.
	Status       string `json:"status"`
	MembershipID string `json:"membership_id"`
	PlanID       string `json:"plan_id"`
	ProductID    string `json:"product_id"`
	// Total is the price after discounts, plus any tax added on top, and
	// Refunded how much of it went back, nil for none.
	Total    *Money `json:"total"`
	Refunded *Money `json:"refunded_amount"`
	// PaidAt is when the money was collected (ISO 8601), "" before.
	PaidAt        string `json:"paid_at"`
	BillingReason string `json:"billing_reason"`
	User          *User  `json:"user"`
}

// paidAt is when the payment's money was collected, zero when Whop didn't
// say.
func (p Payment) paidAt() time.Time {
	t, err := time.Parse(time.RFC3339Nano, p.PaidAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// paymentsRead is how many of a membership's payments PaidPayments reads,
// newest first: a membership's latest charge is what starts its buyer.
const paymentsRead = 10

// PaidPayments lists the paid payments of a membership on an account,
// newest first, up to paymentsRead of them. Whop is asked for them in that
// order, and they're put in it again, so the first is the latest charge
// whatever order Whop's answer comes in.
func (c *Client) PaidPayments(ctx context.Context, accountID, membershipID string) ([]Payment, error) {
	var res page[Payment]
	q := url.Values{"account_id": {accountID}, "membership_id": {membershipID}, "status": {"paid"},
		"order": {"paid_at"}, "direction": {"desc"}, "first": {strconv.Itoa(paymentsRead)}}
	if err := c.do(ctx, http.MethodGet, "/payments", q, nil, &res); err != nil {
		return nil, err
	}
	slices.SortStableFunc(res.Data, func(a, b Payment) int { return b.paidAt().Compare(a.paidAt()) })
	return res.Data, nil
}
