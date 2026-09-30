package whop

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
)

// A revenue share pays a partner part of every payment on a product. Whop
// splits it per transaction, after its own fees, with no referral link, and
// pays it into the partner's balance. A hosted store pays Playkeeper this
// way: a flat amount on each payment of each product.

// RevShare is a partner's share of one product's payments, or of all the
// account's products when ProductID is empty.
type RevShare struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	// Kind is "flat_fee", when Value is dollars per payment, or
	// "percentage".
	Kind  string  `json:"commission_type"`
	Value float64 `json:"commission_value"`
	// Basis is "post_fees" or "pre_fees".
	Basis string `json:"revenue_basis"`
	Type  string `json:"override_type"`
}

// Partner makes user, by id or username, a partner of the account, or finds
// the partner they are, and returns its id (aff_…).
func (c *Client) Partner(ctx context.Context, accountID, user string) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/affiliates", nil, map[string]any{"account_id": accountID, "user_identifier": user}, &a)
	if err == nil && a.ID == "" {
		err = errors.New("Whop's answer named no partner")
	}
	return a.ID, err
}

// AddRevShare gives a partner a flat amount, in dollars, of every payment
// on a product, after Whop's fees.
func (c *Client) AddRevShare(ctx context.Context, partnerID, productID string, dollars float64) (RevShare, error) {
	if productID == "" || dollars <= 0 {
		return RevShare{}, errors.New("a revenue share needs a product and an amount")
	}
	var r RevShare
	err := c.do(ctx, http.MethodPost, "/affiliates/"+url.PathEscape(partnerID)+"/overrides", nil, map[string]any{
		"override_type": "rev_share", "product_id": productID, "commission_type": "flat_fee", "commission_value": dollars, "revenue_basis": "post_fees",
	}, &r)
	return r, err
}

// RevShares lists a partner's revenue shares, leaving out their referral
// commissions.
func (c *Client) RevShares(ctx context.Context, partnerID string) ([]RevShare, error) {
	all, err := list[RevShare](ctx, c, "/affiliates/"+url.PathEscape(partnerID)+"/overrides", url.Values{})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r RevShare) bool { return r.Type != "rev_share" }), nil
}

// Money is an amount as Whop writes it, such as "8.50" in usd.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// PaymentFee is one line of what came out of a payment besides the seller's
// part: Whop's fee, processing, a partner's share and the like.
type PaymentFee struct {
	// Type is whop_fee, processing_fee, affiliate_program_fee or
	// other_fee, and Origin the exact fee, such as affiliate_fee.
	Type   string `json:"type"`
	Origin string `json:"origin"`
	Label  string `json:"label"`
	// Settled is the fee in the payment's settlement currency.
	Settled Money `json:"settlement_amount"`
}

// PaymentFees lists every fee line of a payment.
func (c *Client) PaymentFees(ctx context.Context, paymentID string) ([]PaymentFee, error) {
	var res struct {
		Data []PaymentFee `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/payments/"+url.PathEscape(paymentID)+"/fees", nil, nil, &res)
	return res.Data, err
}
