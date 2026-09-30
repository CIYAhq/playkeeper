package panel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// Playkeeper's share of the app stores' sales (the hosted blueprint's 2.2).
// Each business that installed the Playkeeper Cloud app pays Playkeeper a
// revenue share on each of its hosting products: a percentage of every
// payment, before Whop's fees, that comes to $8.50 for each 4 GB its plans
// allow (whopShareFor). It goes to the Whop user the owner names in
// Settings › Sell on Whop, as the business's partner on Whop. A store's
// customers start only once their payment carried it (whopSharePaid).

const (
	// whopSharePer4GB is Playkeeper's share of a payment for each 4 GB its
	// plan allows, in cents.
	whopSharePer4GB = 850
	// whopShareOrigin is the fee line Whop records for a revenue share.
	whopShareOrigin = "revshare_percentage_fee"
)

// reWhopUserName is the shape of what the owner names a Whop user by: a
// username, with or without its @, or an id (user_…).
var reWhopUserName = regexp.MustCompile(`^@?[A-Za-z0-9_.-]{1,64}$`)

// whopShareFor is Playkeeper's share of a payment on a plan that allows
// memoryMB, in cents, rounded up.
func whopShareFor(memoryMB int) int64 {
	return (int64(memoryMB)*whopSharePer4GB + 4095) / 4096
}

// dollarsOf writes cents as dollars, such as $8.50.
func dollarsOf(cents int64) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// percentOf writes basis points as a percentage, such as 70.84%.
func percentOf(bp int64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%d.%02d", bp/100, bp%100), "0"), ".0") + "%"
}

// setWhopShareUser names the Whop user Playkeeper's share goes to, as the
// owner wrote them: Whop says who that is, by id, since a partner is made
// by id. It can't change while a store pays its share to someone else,
// since that store's share would then be paid twice.
func (s *Server) setWhopShareUser(w http.ResponseWriter, r *http.Request, sess *session, name string) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name != "" && !reWhopUserName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "That isn't a Whop username.", "Write the username of the Whop account that receives Playkeeper's share.")
		return
	}
	app, err := s.readWhopApp(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	var user whop.User
	if name != "" {
		if app.Key == "" {
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Paste the Playkeeper Cloud app's API key first.", "")
			return
		}
		c, err := s.whopClient(app.Key)
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
			user, err = c.User(ctx, name)
			cancel()
		}
		switch {
		case whop.NotFound(err):
			writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Whop has no user @"+name+".", "Check the username on their Whop profile.")
			return
		case err != nil:
			writeErr(w, http.StatusBadGateway, api.CodeRetryLater, whopProblem(err), "")
			return
		case !strings.HasPrefix(user.ID, "user_"):
			writeErr(w, http.StatusBadGateway, api.CodeRetryLater, "Whop's answer didn't say who @"+name+" is.", "")
			return
		}
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	var paying string
	err = s.db.QueryRowContext(r.Context(), `SELECT user_id FROM whop_partners WHERE user_id != ? LIMIT 1`, user.ID).Scan(&paying)
	if err == nil {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Stores already pay Playkeeper's share to "+cmpOr(app.ShareUsername, app.ShareUser)+".",
			"Changing who receives it isn't possible while they do.")
		return
	}
	if !isNoRows(err) {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO whop_app(id, share_user, share_username) VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET share_user = excluded.share_user, share_username = excluded.share_username`, user.ID, user.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	did := "Playkeeper's share goes to nobody"
	if user.ID != "" {
		did = "Playkeeper's share goes to @" + cmpOr(user.Username, name) + " (" + user.ID + ")"
	}
	s.audit(sess.User.Username, "whop.app", "panel", "succeeded", did)
	s.answerWhop(w, r)
}

// ensureWhopPartner makes the user Playkeeper's share goes to the store's
// partner on Whop, once, and returns the partner (aff_…).
func (s *Server) ensureWhopPartner(ctx context.Context, c *whop.Client, st whopStore, user string) (string, error) {
	var partner, was string
	err := s.db.QueryRowContext(ctx, `SELECT partner_id, user_id FROM whop_partners WHERE store_id = ?`, st.ID).Scan(&partner, &was)
	switch {
	case err == nil && was == user:
		return partner, nil
	case err == nil:
		return "", fmt.Errorf("the store pays Playkeeper's share to %s, not %s", was, user)
	case !isNoRows(err):
		return "", err
	}
	if partner, err = c.Partner(ctx, st.ID, user); err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO whop_partners(store_id, partner_id, user_id, made_at) VALUES(?,?,?,?)`, st.ID, partner, user, s.now().UnixMilli())
	return partner, err
}

// whopShareWant is the share a hosting product must carry: the percentage,
// in basis points, that pays Playkeeper's share of each of its plans'
// payments.
type whopShareWant struct {
	Product, Title string
	BasisPoints    int64
}

// whopLeastCharge is the least a plan charges a buyer, 0 for a free plan: a
// plan that renews may charge its first payment apart.
func whopLeastCharge(p whop.Plan) float64 {
	least := 0.0
	for _, price := range []float64{p.InitialPrice, p.RenewalPrice} {
		if price > 0 && (least == 0 || price < least) {
			least = price
		}
	}
	return least
}

// whopShareWants works out each hosting product's share from its plans,
// each plan whose metadata allows servers and that isn't archived: the
// most any of its plans needs, from the plan's memory and the least it
// charges. A plan that can't carry Playkeeper's share, being free or
// charging less, is a problem naming it.
func whopShareWants(plans []whop.Plan) ([]whopShareWant, []string) {
	wants := map[string]*whopShareWant{}
	var problems []string
	for _, p := range plans {
		_, memoryMB, ok := whop.PlanAllowance(p.Metadata)
		if !ok || p.Visibility == "archived" || p.Product.ID == "" {
			continue
		}
		share := whopShareFor(memoryMB)
		pct, err := whop.SharePercent(float64(share)/100, whopLeastCharge(p))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s charges %s, which can't carry Playkeeper's share of %s", cmpOr(p.Title, p.ID), p.Price(), dollarsOf(share)))
			continue
		}
		w := wants[p.Product.ID]
		if w == nil {
			w = &whopShareWant{Product: p.Product.ID, Title: p.Product.Title}
			wants[p.Product.ID] = w
		}
		w.BasisPoints = max(w.BasisPoints, int64(math.Round(pct*100)))
	}
	out := make([]whopShareWant, 0, len(wants))
	for _, w := range wants {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	return out, problems
}

// whopShareSet is a product's share as the dashboard last set it.
type whopShareSet struct {
	ShareID     string
	BasisPoints int64
}

func (s *Server) whopSharesSet(ctx context.Context, storeID string) (map[string]whopShareSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_id, share_id, basis_points FROM whop_shares WHERE store_id = ?`, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]whopShareSet{}
	for rows.Next() {
		var product string
		var v whopShareSet
		if err := rows.Scan(&product, &v.ShareID, &v.BasisPoints); err != nil {
			return nil, err
		}
		set[product] = v
	}
	return set, rows.Err()
}

func (s *Server) keepWhopShare(ctx context.Context, storeID, product string, r whop.RevShare, bp int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO whop_shares(store_id, product_id, share_id, basis_points, set_at) VALUES(?,?,?,?,?)
		ON CONFLICT(store_id, product_id) DO UPDATE SET share_id = excluded.share_id, basis_points = excluded.basis_points, set_at = excluded.set_at`,
		storeID, product, r.ID, bp, s.now().UnixMilli())
	return err
}

// syncWhopShares brings an app store's shares in line with its prices, and
// says what's wrong with them, "" when nothing is. Each hosting product
// needs Playkeeper's share (whopShareWants) from the store's partner.
//   - A product with no share gets one, as when the seller added it.
//   - A share the dashboard set follows its product's price, up or down.
//   - A share the seller removed, lowered below what the price needs, or
//     made other than a percentage of the full price is a problem. Only
//     force, as Open the store does, sets it right.
//   - A share the seller raised stays, even with force, since it pays at
//     least Playkeeper's.
//
// An error is Whop or the dashboard failing, which says nothing about the
// shares.
func (s *Server) syncWhopShares(ctx context.Context, c *whop.Client, st whopStore, force bool) (string, error) {
	app, err := s.readWhopApp(ctx)
	if err != nil {
		return "", err
	}
	if app.ShareUser == "" {
		return "Playkeeper Cloud doesn't take its share yet: the owner hasn't said who receives it in Settings › Sell on Whop.", nil
	}
	partner, err := s.ensureWhopPartner(ctx, c, st, app.ShareUser)
	if err != nil {
		return "", err
	}
	plans, err := c.Plans(ctx, st.ID)
	if err != nil {
		return "", err
	}
	wants, problems := whopShareWants(plans)
	shares, err := c.RevShares(ctx, partner)
	if err != nil {
		return "", err
	}
	onWhop := map[string]whop.RevShare{}
	for _, r := range shares {
		if _, seen := onWhop[r.ProductID]; r.ProductID != "" && !seen {
			onWhop[r.ProductID] = r
		}
	}
	set, err := s.whopSharesSet(ctx, st.ID)
	if err != nil {
		return "", err
	}
	for _, w := range wants {
		name := cmpOr(w.Title, w.Product)
		r, there := onWhop[w.Product]
		was, ours := set[w.Product]
		bp := int64(math.Round(r.Percent * 100))
		// full is a share taken as the dashboard sets it, and untouched one
		// the seller left as the dashboard last set it.
		full := r.Kind == "percentage" && r.Basis == "pre_fees"
		untouched := ours && was.ShareID == r.ID && was.BasisPoints == bp
		switch {
		case !there && ours && !force:
			problems = append(problems, "Playkeeper's share on "+name+" was removed")
		case !there:
			added, err := c.AddRevShare(ctx, partner, w.Product, float64(w.BasisPoints)/100)
			if err != nil {
				return "", err
			}
			err = s.keepWhopShare(ctx, st.ID, w.Product, added, w.BasisPoints)
			if err != nil {
				return "", err
			}
		case full && bp == w.BasisPoints:
			if !untouched {
				if err := s.keepWhopShare(ctx, st.ID, w.Product, r, bp); err != nil {
					return "", err
				}
			}
		case full && untouched, force && (!full || bp < w.BasisPoints):
			updated, err := c.UpdateRevShare(ctx, partner, r.ID, float64(w.BasisPoints)/100)
			if err != nil {
				return "", err
			}
			updated.ID = cmpOr(updated.ID, r.ID)
			if err := s.keepWhopShare(ctx, st.ID, w.Product, updated, w.BasisPoints); err != nil {
				return "", err
			}
		case !full:
			problems = append(problems, "Playkeeper's share on "+name+" isn't a percentage of the full price")
		case bp < w.BasisPoints:
			problems = append(problems, fmt.Sprintf("Playkeeper's share on %s is %s, under the %s its price needs", name, percentOf(bp), percentOf(w.BasisPoints)))
		}
	}
	if len(problems) == 0 {
		return "", nil
	}
	return strings.Join(problems, "; ") + ".", nil
}

// whopSharePaid checks that a membership's latest paid payment carried
// Playkeeper's share for a plan that allows memoryMB, before its buyer
// starts. Every fee line it reads is kept. The error says why not: no paid
// payment yet, or no line of Playkeeper's share (whopSharePaidIn).
func (s *Server) whopSharePaid(ctx context.Context, c *whop.Client, st whopStore, membershipID string, memoryMB int) error {
	pays, err := c.PaidPayments(ctx, st.ID, membershipID)
	if err != nil {
		return err
	}
	if len(pays) == 0 {
		return errors.New("Whop has no paid payment for this membership yet, so its server waits")
	}
	pay := pays[0]
	if whopRefundedInFull(pay) {
		return fmt.Errorf("its payment on Whop (%s) was refunded, so its server waits", pay.ID)
	}
	lines, err := c.PaymentFees(ctx, pay.ID)
	if err != nil {
		return err
	}
	if err := s.keepWhopFeeLines(ctx, st.ID, pay.ID, lines); err != nil {
		return err
	}
	if want := whopShareFor(memoryMB); !whopSharePaidIn(lines, want) {
		return fmt.Errorf("its payment on Whop (%s) didn't carry Playkeeper's share of %s, so its server waits", pay.ID, dollarsOf(want))
	}
	return nil
}

// whopRefundedInFull says whether all of a payment went back to its buyer.
func whopRefundedInFull(p whop.Payment) bool {
	if p.Refunded == nil || p.Total == nil {
		return false
	}
	back, err1 := p.Refunded.Minor()
	total, err2 := p.Total.Minor()
	return err1 == nil && err2 == nil && back > 0 && back >= total
}

// whopSharePaidIn says whether a payment's fee lines hold Playkeeper's
// share of at least want cents: a revenue share line of that much in US
// dollars, whichever sign Whop writes a fee with.
func whopSharePaidIn(lines []whop.PaymentFee, want int64) bool {
	for _, l := range lines {
		if l.Origin != whopShareOrigin || l.Settled.Currency != "usd" {
			continue
		}
		if n, err := l.Settled.Minor(); err == nil && max(n, -n) >= want {
			return true
		}
	}
	return false
}

// keepWhopFeeLines keeps the fee lines a payment was checked with, once, as
// Whop wrote them.
func (s *Server) keepWhopFeeLines(ctx context.Context, storeID, paymentID string, lines []whop.PaymentFee) error {
	at := s.now().UnixMilli()
	for i, l := range lines {
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO whop_fee_lines(payment_id, n, store_id, type, origin, label, amount, currency, read_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, paymentID, i, storeID, l.Type, l.Origin, l.Label, l.Settled.Amount, l.Settled.Currency, at); err != nil {
			return err
		}
	}
	return nil
}
