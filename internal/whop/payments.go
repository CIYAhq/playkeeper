package whop

import (
	"context"
	"fmt"
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

// PaidTime is when the payment's money was collected, zero when Whop didn't
// say.
func (p Payment) PaidTime() time.Time { return p.paidAt() }

// paymentsRead is how many of a membership's payments PaidPayments reads,
// newest first: a membership's latest charge is what starts its buyer.
const paymentsRead = 10

// PaidSince lists an account's payments paid since a time. Whop filters
// payments only by when they were created, and a charge can be paid long
// after, as a renewal paid on a retry is, so they're listed newest paid
// first and read page by page until a page reaches back past since, up to
// maxPages of them.
func (c *Client) PaidSince(ctx context.Context, accountID string, since time.Time) ([]Payment, error) {
	q := url.Values{"account_id": {accountID}, "status": {"paid"}, "order": {"paid_at"}, "direction": {"desc"}, "first": {strconv.Itoa(100)}}
	var paid []Payment
	for range maxPages {
		var p page[Payment]
		if err := c.do(ctx, http.MethodGet, "/payments", q, nil, &p); err != nil {
			return nil, err
		}
		past := false
		for _, pay := range p.Data {
			switch t := pay.paidAt(); {
			case t.IsZero():
			case t.Before(since):
				past = true
			default:
				paid = append(paid, pay)
			}
		}
		if past || !p.PageInfo.HasNextPage || p.PageInfo.EndCursor == nil || *p.PageInfo.EndCursor == "" {
			return paid, nil
		}
		q.Set("after", *p.PageInfo.EndCursor)
	}
	return nil, fmt.Errorf("Whop's payments paid since %s are more than %d pages", since.UTC().Format(time.RFC3339), maxPages)
}

// Payment reads one payment.
func (c *Client) Payment(ctx context.Context, id string) (Payment, error) {
	var p Payment
	err := c.do(ctx, http.MethodGet, "/payments/"+url.PathEscape(id), nil, nil, &p)
	return p, err
}

// Refund is money an account gave back on one of its payments.
type Refund struct {
	ID        string `json:"id"`
	PaymentID string `json:"payment_id"`
	// Status is pending or requires_action until the refund is settled as
	// succeeded, failed or canceled.
	Status string `json:"status"`
	// CreatedAt is when the refund was asked for (ISO 8601).
	CreatedAt string `json:"created_at"`
}

// Unsettled says whether a refund may still change what its payment
// refunded.
func (r Refund) Unsettled() bool { return r.Status == "pending" || r.Status == "requires_action" }

// Created is when the refund was asked for, zero when Whop didn't say.
func (r Refund) Created() time.Time {
	t, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// RefundsSince lists the refunds an account issued since.
func (c *Client) RefundsSince(ctx context.Context, accountID string, since time.Time) ([]Refund, error) {
	return list[Refund](ctx, c, "/refunds", url.Values{"account_id": {accountID}, "created_after": {since.UTC().Format(time.RFC3339)}})
}

// PaidPayments lists the paid payments of a membership on an account,
// newest first, up to paymentsRead of them. Whop is asked for them in that
// order, and they're put in it again, so the first is the latest charge
// whatever order Whop's answer comes in. Only those Whop names the
// membership on and calls paid are kept, so a filter Whop ignores, or a
// field it nests, finds none rather than another membership's.
func (c *Client) PaidPayments(ctx context.Context, accountID, membershipID string) ([]Payment, error) {
	var res page[Payment]
	q := url.Values{"account_id": {accountID}, "membership_id": {membershipID}, "status": {"paid"},
		"order": {"paid_at"}, "direction": {"desc"}, "first": {strconv.Itoa(paymentsRead)}}
	if err := c.do(ctx, http.MethodGet, "/payments", q, nil, &res); err != nil {
		return nil, err
	}
	paid := slices.DeleteFunc(res.Data, func(p Payment) bool { return p.MembershipID != membershipID || p.Status != "paid" })
	slices.SortStableFunc(paid, func(a, b Payment) int { return b.paidAt().Compare(a.paidAt()) })
	return paid, nil
}
