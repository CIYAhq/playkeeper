package whop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
)

// A revenue share pays a partner a percentage of every payment on a
// product. Whop splits it per transaction, with no referral link, and pays
// it into the partner's balance. A hosted store pays Playkeeper this way.
// Whop refuses a flat amount on a revenue share, so Playkeeper's flat fee is
// the percentage of the product's price that comes to it (SharePercent),
// taken before Whop's fees so it's the same whatever the buyer pays with,
// and set again when the price changes.

// RevShare is a partner's share of one product's payments, or of all the
// account's products when ProductID is empty.
type RevShare struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	// Kind is "percentage", of which Percent is from 1 to 100.
	Kind    string  `json:"commission_type"`
	Percent float64 `json:"commission_value"`
	// Basis is "pre_fees", of the full price, or "post_fees", of what's
	// left after Whop's fees.
	Basis string `json:"revenue_basis"`
	Type  string `json:"override_type"`
}

// SharePercent is the percentage of price that pays at least dollars:
// dollars ÷ price, rounded up to two decimals, as Whop takes a percentage.
// Whop caps a share at 100%, so a price under dollars can't carry it.
func SharePercent(dollars, price float64) (float64, error) {
	d, p := int64(math.Round(dollars*100)), int64(math.Round(price*100))
	if d <= 0 || p < d {
		return 0, fmt.Errorf("a price of %.2f can't carry a share of %.2f, since Whop caps a share at 100%%", price, dollars)
	}
	return float64((d*10000+p-1)/p) / 100, nil
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

// AddRevShare gives a partner percent of every payment on a product, of the
// full price before Whop's fees.
func (c *Client) AddRevShare(ctx context.Context, partnerID, productID string, percent float64) (RevShare, error) {
	if productID == "" {
		return RevShare{}, errors.New("a revenue share needs a product")
	}
	if err := checkSharePercent(percent); err != nil {
		return RevShare{}, err
	}
	var r RevShare
	err := c.do(ctx, http.MethodPost, "/affiliates/"+url.PathEscape(partnerID)+"/overrides", nil, map[string]any{
		"override_type": "rev_share", "product_id": productID, "commission_type": "percentage", "commission_value": percent, "revenue_basis": "pre_fees",
	}, &r)
	return r, err
}

// UpdateRevShare sets a partner's share to percent, as when its product's
// price changed.
func (c *Client) UpdateRevShare(ctx context.Context, partnerID, shareID string, percent float64) (RevShare, error) {
	if err := checkSharePercent(percent); err != nil {
		return RevShare{}, err
	}
	var r RevShare
	err := c.do(ctx, http.MethodPatch, "/affiliates/"+url.PathEscape(partnerID)+"/overrides/"+url.PathEscape(shareID), nil,
		map[string]any{"commission_value": percent}, &r)
	return r, err
}

// checkSharePercent refuses a percentage Whop doesn't take on a share.
func checkSharePercent(percent float64) error {
	if percent < 1 || percent > 100 {
		return fmt.Errorf("a revenue share is 1%% to 100%% of each payment, not %v%%", percent)
	}
	return nil
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
