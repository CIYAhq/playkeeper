package whop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Needs are the permissions Sell on Whop's key must have, in the order the
// dashboard lists them: reading the store's products and plans, marking the
// products with the dashboard's address and setting how many more of each
// plan can sell, reading buyers' memberships and hearing about them, and
// sending buyers messages in a support chat. Whop calls products access
// passes in its permissions.
var Needs = []string{
	"access_pass:basic:read",
	"access_pass:update",
	"plan:basic:read",
	"plan:update",
	"member:basic:read",
	"member:email:read",
	"developer:manage_webhook",
	"webhook_receive:memberships",
	"support_chat:create",
	"support_chat:message:create",
}

// Account is the seller's business on Whop.
type Account struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Route string `json:"route"`
}

// Me is the account the key belongs to.
func (c *Client) Me(ctx context.Context) (Account, error) {
	var a Account
	err := c.do(ctx, http.MethodGet, "/accounts/me", nil, nil, &a)
	return a, err
}

// Missing lists the actions of Needs the key isn't granted on accountID.
func (c *Client) Missing(ctx context.Context, accountID string) ([]string, error) {
	var res struct {
		Data []struct {
			Action  string `json:"action"`
			Granted bool   `json:"granted"`
		} `json:"data"`
	}
	q := url.Values{"resource_id": {accountID}, "actions": {strings.Join(Needs, ",")}}
	if err := c.do(ctx, http.MethodGet, "/permissions", q, nil, &res); err != nil {
		return nil, err
	}
	granted := map[string]bool{}
	for _, p := range res.Data {
		granted[p.Action] = granted[p.Action] || p.Granted
	}
	var missing []string
	for _, n := range Needs {
		if !granted[n] {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

// Metadata is the key-value pairs Whop keeps on a product or plan. Whop
// keeps strings; anything else reads as its JSON text.
type Metadata map[string]string

func (m *Metadata) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(Metadata, len(raw))
	for k, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		if t := string(bytes.TrimSpace(v)); t != "null" {
			out[k] = t
		}
	}
	*m = out
	return nil
}

// Product is one thing the store sells, such as "Minecraft server".
type Product struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Route      string   `json:"route"`
	Visibility string   `json:"visibility"`
	Metadata   Metadata `json:"metadata"`
}

// Products lists the account's products.
func (c *Client) Products(ctx context.Context, accountID string) ([]Product, error) {
	return list[Product](ctx, c, "/products", url.Values{"account_id": {accountID}})
}

// SetProductMetadata replaces a product's metadata with meta. Whop keeps
// the whole map as sent, so callers send the product's other keys along.
func (c *Client) SetProductMetadata(ctx context.Context, productID string, meta Metadata) error {
	return c.do(ctx, http.MethodPatch, "/products/"+url.PathEscape(productID), nil, map[string]any{"metadata": meta}, nil)
}

// Plan is one way to buy a product: a price, how often it renews and, in its
// metadata, what a buyer may create (see PlanAllowance). Whop's API calls
// plans variants now; their ids still start with plan_.
type Plan struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Visibility is "visible", "hidden" (reachable only by its direct
	// link), "archived" or "quick_link".
	Visibility string `json:"visibility"`
	// PlanType is "renewal" or "one_time".
	PlanType       string  `json:"plan_type"`
	FormattedPrice string  `json:"formatted_price"`
	Currency       string  `json:"currency"`
	InitialPrice   float64 `json:"initial_price"`
	RenewalPrice   float64 `json:"renewal_price"`
	PurchaseURL    string  `json:"purchase_url"`
	// BillingPeriod is the days between renewals, 0 for a one-time plan.
	BillingPeriod int `json:"billing_period"`
	// TrialDays is the free days before the first charge, 0 for none.
	TrialDays int      `json:"trial_period_days"`
	Product   Ref      `json:"product"`
	Metadata  Metadata `json:"metadata"`
	// Stock is how many more of the plan can sell, unless UnlimitedStock.
	// Whop's schema calls it a number, not an integer.
	Stock          float64 `json:"stock"`
	UnlimitedStock bool    `json:"unlimited_stock"`
}

// Ref names another object, such as a plan's product.
type Ref struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (p *Plan) UnmarshalJSON(b []byte) error {
	// Whop sends null for a missing number or object; those stay zero.
	type plain Plan
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if string(bytes.TrimSpace(v)) == "null" {
			delete(raw, k)
		}
	}
	clean, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var out plain
	if err := json.Unmarshal(clean, &out); err != nil {
		return err
	}
	*p = Plan(out)
	return nil
}

// Price is how the dashboard shows a plan's price: Whop's own words when it
// sends them, else the amount and how often it renews.
func (p Plan) Price() string {
	if p.FormattedPrice != "" {
		return p.FormattedPrice
	}
	cur := strings.ToUpper(p.Currency)
	if p.PlanType == "renewal" && p.BillingPeriod > 0 {
		return strings.TrimSpace(fmt.Sprintf("%s %.2f every %d days", cur, p.RenewalPrice, p.BillingPeriod))
	}
	return strings.TrimSpace(fmt.Sprintf("%s %.2f", cur, p.InitialPrice))
}

// Free reports whether a plan charges nothing, first or on renewal.
func (p Plan) Free() bool { return p.InitialPrice == 0 && p.RenewalPrice == 0 }

// Plans lists the account's plans, visible and hidden.
func (c *Client) Plans(ctx context.Context, accountID string) ([]Plan, error) {
	return list[Plan](ctx, c, "/variants", url.Values{"account_id": {accountID}})
}

// SetPlanStock limits how many more of a plan can sell to n, which Whop
// enforces at checkout.
func (c *Client) SetPlanStock(ctx context.Context, planID string, n int) error {
	return c.do(ctx, http.MethodPatch, "/variants/"+url.PathEscape(planID), nil, map[string]any{"stock": n, "unlimited_stock": false}, nil)
}
