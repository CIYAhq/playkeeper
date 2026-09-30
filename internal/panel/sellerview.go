package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// A seller's view of their store on Playkeeper Cloud (the hosted
// blueprint's 3.1), for their page inside their Whop dashboard (see
// whop_sellerpage.go): how the store stands, its plans, its customers and
// what it earned, and nothing of any other store. Earnings come from the
// payments 2.2 checked, which keepWhopPayment keeps.

// whopPayment is a payment 2.2 checked, as the seller's view counts it,
// with each amount in the currency's smallest unit, such as cents.
type whopPayment struct {
	ID         string
	Store      string
	WhopUserID string
	PlanID     string
	Currency   string
	Amount     int64
	Share      int64
	Refunded   int64
	PaidAt     time.Time
}

// reCurrency is a currency as Whop names it, such as "usd".
var reCurrency = regexp.MustCompile(`^[a-z]{3}$`)

// errNoSellerView refuses a seller's view of a store that isn't an app
// store of this dashboard.
var errNoSellerView = errors.New("this dashboard doesn't sell for that business through Playkeeper Cloud")

// keepWhopPayment keeps a payment 2.2 checked, for its store, once by its
// id; kept again, such as after a refund, it takes the newer amounts. A
// payment never moves to another store, and one for a store the dashboard
// doesn't sell for isn't kept.
func (s *Server) keepWhopPayment(ctx context.Context, p whopPayment) error {
	p.Currency = strings.ToLower(p.Currency)
	switch {
	case !reWhopID.MatchString(p.ID) || !reWhopID.MatchString(p.Store) || !reWhopID.MatchString(p.WhopUserID) || p.PlanID != "" && !reWhopID.MatchString(p.PlanID):
		return fmt.Errorf("a payment needs Whop's ids for itself, its store and its buyer: %q, %q, %q", p.ID, p.Store, p.WhopUserID)
	case !reCurrency.MatchString(p.Currency):
		return fmt.Errorf("%q isn't a currency", p.Currency)
	case p.Amount < 0 || p.Share < 0 || p.Refunded < 0 || p.Refunded > p.Amount || p.Share > p.Amount:
		return fmt.Errorf("the payment %s's amounts don't add up: %d paid, %d refunded, %d shared", p.ID, p.Amount, p.Refunded, p.Share)
	case p.PaidAt.IsZero():
		return fmt.Errorf("the payment %s needs when it was paid", p.ID)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO whop_payments(payment_id, store_id, whop_user_id, plan_id, currency, amount, share, refunded, paid_at, updated_at)
		SELECT ?, store_id, ?, ?, ?, ?, ?, ?, ?, ? FROM whop_stores WHERE store_id = ?
		ON CONFLICT(payment_id) DO UPDATE SET plan_id = excluded.plan_id, currency = excluded.currency, amount = excluded.amount, share = excluded.share,
		refunded = excluded.refunded, updated_at = excluded.updated_at WHERE whop_payments.store_id = excluded.store_id`,
		p.ID, p.WhopUserID, p.PlanID, p.Currency, p.Amount, p.Share, p.Refunded, p.PaidAt.UnixMilli(), s.now().UnixMilli(), p.Store)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("the payment %s isn't kept for %s: the dashboard doesn't sell for it, or the payment is another store's", p.ID, p.Store)
	}
	return nil
}

// sellerView is a seller's view of their store.
type sellerView struct {
	Store     sellerStore      `json:"store"`
	Plans     []sellerPlan     `json:"plans"`
	Customers []sellerCustomer `json:"customers"`
	Earnings  []sellerMonth    `json:"earnings"`
}

// sellerStore is how the store stands: selling, closed (such as not open
// yet), needing a look, suspended by Playkeeper, or left. Why is the words
// of a store that's closed, needs a look or left; a suspension's reason is
// the owner's own.
type sellerStore struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Route string `json:"route,omitempty"`
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
}

// sellerPlan is one of the store's plans that grants servers: its name and
// price as Whop shows them, what it allows, its stock, and how many
// customers have it now.
type sellerPlan struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Price          string `json:"price"`
	Servers        int    `json:"servers"`
	MemoryMB       int    `json:"memoryMB"`
	Stock          int    `json:"stock"`
	UnlimitedStock bool   `json:"unlimitedStock"`
	Customers      int    `json:"customers"`
}

// sellerCustomer is one of the store's customers as their seller sees them:
// their Whop username, their plan, since when they're a customer, and
// whether they're active, being set up, paused, suspended or ended. Their
// account and servers here are theirs alone.
type sellerCustomer struct {
	Handle string     `json:"handle"`
	Plan   string     `json:"plan,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
	Status string     `json:"status"`
}

// sellerMonth is what a store earned in one month, in UTC, in one currency:
// its sales less refunds, Playkeeper's share, and what's left for the
// seller before Whop's own fees, each in the currency's smallest unit.
type sellerMonth struct {
	Month    string `json:"month"`
	Currency string `json:"currency"`
	Sales    int64  `json:"sales"`
	Share    int64  `json:"share"`
	Kept     int64  `json:"kept"`
}

// sellerStoreView is the seller's view of the app store storeID. Every
// query names the store.
func (s *Server) sellerStoreView(ctx context.Context, storeID string) (sellerView, error) {
	st, ok, err := s.whopStoreByID(ctx, storeID)
	switch {
	case err != nil:
		return sellerView{}, err
	case !ok || st.Via != whopViaApp:
		return sellerView{}, errNoSellerView
	}
	v := sellerView{Store: sellerStore{ID: st.ID, Title: cmpOr(st.Title, st.ID), Route: st.Route, State: "selling"},
		Plans: []sellerPlan{}, Customers: []sellerCustomer{}, Earnings: []sellerMonth{}}
	switch {
	case !st.LeftAt.IsZero():
		v.Store.State, v.Store.Why = "left", st.LeftWhy
	case !st.SuspendedAt.IsZero():
		v.Store.State = "suspended"
	case st.ClosedWhy != "":
		v.Store.State, v.Store.Why = "closed", st.ClosedWhy
	case st.Problem != "":
		v.Store.State, v.Store.Why = "needsLook", st.Problem
	}
	if v.Plans, err = s.sellerPlans(ctx, st.ID); err != nil {
		return sellerView{}, err
	}
	if v.Customers, err = s.sellerCustomers(ctx, st.ID); err != nil {
		return sellerView{}, err
	}
	if v.Earnings, err = s.storeEarnings(ctx, st.ID); err != nil {
		return sellerView{}, err
	}
	return v, nil
}

// hWhopSellerView is a seller's page reading the seller's view of their
// store, as it does once the store is open: for the business's team alone,
// with Whop's token, from the page itself.
func (s *Server) hWhopSellerView(w http.ResponseWriter, r *http.Request, store string) {
	if !fromSellerPage(r) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Open this page inside your Whop dashboard.", "")
		return
	}
	if _, err := s.sellerAuth(r, store); err != nil {
		s.sellerRefusal(w, err)
		return
	}
	v, err := s.sellerStoreView(r.Context(), store)
	switch {
	case errors.Is(err, errNoSellerView):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Playkeeper Cloud doesn't sell for this business from this dashboard.", "")
	case err != nil:
		s.log.Error("could not read a seller's view of their store", "store", store, "err", err)
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

// sellerPlans lists the store's plans that grant servers, as the store
// orders them, each with how many customers have it now.
func (s *Server) sellerPlans(ctx context.Context, storeID string) ([]sellerPlan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.plan_id, p.title, p.price, p.allowance_servers, p.allowance_memory_mb, p.stock, p.unlimited_stock,
		(SELECT COUNT(DISTINCT m.whop_user_id) FROM whop_memberships m WHERE m.store_id = p.store_id AND m.plan_id = p.plan_id AND m.stale = 0 AND m.status IN `+whopAccess+`)
		FROM whop_plans p WHERE p.store_id = ? AND p.allowance_from != '' AND p.visibility != 'archived' ORDER BY p.position, p.plan_id`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sellerPlan{}
	for rows.Next() {
		var p sellerPlan
		if err := rows.Scan(&p.ID, &p.Title, &p.Price, &p.Servers, &p.MemoryMB, &p.Stock, &p.UnlimitedStock, &p.Customers); err != nil {
			return nil, err
		}
		p.Title = cmpOr(p.Title, p.ID)
		out = append(out, p)
	}
	return out, rows.Err()
}

// sellerCustomers lists the store's customers, the most recently changed
// first, at most 200.
func (s *Server) sellerCustomers(ctx context.Context, storeID string) ([]sellerCustomer, error) {
	custs, err := s.whopCustomers(ctx, storeID)
	if err != nil {
		return nil, err
	}
	out := []sellerCustomer{}
	for _, wc := range custs {
		if len(out) == 200 {
			break
		}
		c := sellerCustomer{Handle: cmpOr(wc.Handle, wc.WhopUserID), Plan: wc.Plan.Name}
		has := wc.Plan.Servers > 0 && wc.Plan.MemoryMB > 0
		switch {
		case has && (wc.Applied == "" || wc.Paused):
			c.Status = "starting"
		case has:
			c.Status = "active"
		case wc.Applied != "":
			c.Status = "paused"
		default:
			c.Status = "ended"
		}
		var state CustomerState
		var since int64
		err := s.db.QueryRowContext(ctx, `SELECT state, created_at FROM customers WHERE provider = ? AND store = ? AND subject = ?`, whopProvider, storeID, wc.WhopUserID).Scan(&state, &since)
		switch {
		case isNoRows(err):
		case err != nil:
			return nil, err
		default:
			if state == CustomerSuspended {
				c.Status = "suspended"
			}
			t := time.UnixMilli(since).UTC()
			c.Since = &t
		}
		out = append(out, c)
	}
	return out, nil
}

// storeEarnings adds up the store's payments by month and currency, the
// newest month first.
func (s *Server) storeEarnings(ctx context.Context, storeID string) ([]sellerMonth, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT paid_at, currency, amount, refunded, share FROM whop_payments WHERE store_id = ?`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	months := map[[2]string]*sellerMonth{}
	for rows.Next() {
		var at, amount, refunded, share int64
		var currency string
		if err := rows.Scan(&at, &currency, &amount, &refunded, &share); err != nil {
			return nil, err
		}
		key := [2]string{time.UnixMilli(at).UTC().Format("2006-01"), currency}
		m := months[key]
		if m == nil {
			m = &sellerMonth{Month: key[0], Currency: key[1]}
			months[key] = m
		}
		m.Sales += amount - refunded
		m.Share += share
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]sellerMonth, 0, len(months))
	for _, m := range months {
		m.Kept = m.Sales - m.Share
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Month != out[j].Month {
			return out[i].Month > out[j].Month
		}
		return out[i].Currency < out[j].Currency
	})
	return out, nil
}
