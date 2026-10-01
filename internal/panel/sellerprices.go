package panel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// A seller's prices and Open the store (the hosted blueprint's 2.3), on
// their page inside their Whop dashboard (see whop_sellerpage.go). The page
// lists the store's hosting plans at their prices, as Whop has them, and
// the seller sets any at or above the floor, $12 a month for each 4 GB.
// Open the store sets Playkeeper's share on each hosting product
// (syncWhopShares, with force) and only when that leaves nothing wrong
// opens the store for its seller's reason, "Not open yet".

// whopFloorPer4GB is the least a hosted plan may charge a month for each
// 4 GB it allows, in cents.
const whopFloorPer4GB = 1200

// reSellerPrice is a monthly price as a seller writes it, in dollars, with
// or without its cents.
var reSellerPrice = regexp.MustCompile(`^\$?([0-9]{1,5})(?:\.([0-9]{1,2}))?$`)

// whopFloorFor is the least a hosted plan that allows memoryMB may charge
// a month, in cents, rounded up.
func whopFloorFor(memoryMB int) int64 {
	return (int64(memoryMB)*whopFloorPer4GB + 4095) / 4096
}

// whopSuggestedPer4GB is the monthly price the seller's page suggests for
// each 4 GB a plan allows, in cents.
const whopSuggestedPer4GB = 1500

// whopSuggestedFor is the monthly price the seller's page suggests for a
// plan that allows memoryMB, in cents, rounded up.
func whopSuggestedFor(memoryMB int) int64 {
	return (int64(memoryMB)*whopSuggestedPer4GB + 4095) / 4096
}

// centsOf is an amount Whop writes in dollars, in cents.
func centsOf(dollars float64) int64 { return int64(math.Round(dollars * 100)) }

// gigabytes writes memory as a person reads it, such as 4 GB.
func gigabytes(memoryMB int) string {
	return strconv.FormatFloat(float64(memoryMB)/1024, 'f', -1, 64) + " GB"
}

// parseSellerPrice reads a monthly price as the seller wrote it, in cents.
func parseSellerPrice(s string) (int64, error) {
	m := reSellerPrice.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("Write the price in dollars, such as 15 or 14.99")
	}
	dollars, _ := strconv.ParseInt(m[1], 10, 64)
	cents, _ := strconv.ParseInt((m[2] + "00")[:2], 10, 64)
	return dollars*100 + cents, nil
}

// sellerPrices is how a seller prices their store: its hosting plans, and
// whether Open the store is theirs to press, as it is while the store is
// closed and neither suspended nor gone, or Update the store, the same
// call once it's open. Problem is what's wrong with Playkeeper's share once
// a price changed, if anything. Fixable says Fix my plans has a plan's
// renewal, currency or free trial to put right, and Blocked is what it
// can't, in a plain line each, for the seller to change in Whop.
type sellerPrices struct {
	Plans     []sellerPrice `json:"plans"`
	CanOpen   bool          `json:"canOpen"`
	CanUpdate bool          `json:"canUpdate"`
	Problem   string        `json:"problem,omitempty"`
	Fixable   bool          `json:"fixable"`
	Blocked   []string      `json:"blocked,omitempty"`
}

// sellerPrice is one hosting plan as its seller prices it: what it allows,
// its monthly price in its currency's smallest unit, and the floor and
// Playkeeper's share of each payment, in US cents. Settable says the seller
// can set its price here, since it renews monthly in US dollars, and
// Problem is what keeps it from selling.
type sellerPrice struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Servers  int    `json:"servers"`
	MemoryMB int    `json:"memoryMB"`
	Price    int64  `json:"price"`
	Currency string `json:"currency"`
	Floor    int64  `json:"floor"`
	Share    int64  `json:"share"`
	Settable bool   `json:"settable"`
	Problem  string `json:"problem,omitempty"`
	// Suggested is the monthly price the seller's page offers for it, in US
	// cents (whopSuggestedFor).
	Suggested int64 `json:"suggested"`
	// issues are what Problem says, each in a few words, for the plan's
	// line when Open the store refuses it (refusedPlans).
	issues []planIssue
}

// planRule is one of Open the store's rules for a hosting plan, in the
// order a refusal says how to meet them.
type planRule int

const (
	ruleOwnProduct planRule = iota
	ruleAllowance
	ruleOneTime
	ruleDollars
	ruleMonthly
	ruleNoTrial
	ruleFloor
)

// planRuleFixes says how to meet each rule, in a refusal's words.
var planRuleFixes = [...]string{
	ruleOwnProduct: "give each plan its own product",
	ruleAllowance:  fmt.Sprintf("allow 1 to %d servers and %d to %d GB", invites.MaxAllowanceServers, invites.MinAllowanceMemoryMB>>10, invites.MaxAllowanceMemoryMB>>10),
	ruleOneTime:    "replace one-time plans with monthly ones",
	ruleDollars:    "price in US dollars",
	ruleMonthly:    "renew every month",
	ruleNoTrial:    "take off free trials",
	ruleFloor:      "charge at least " + dollarsOf(whopFloorPer4GB) + " a month for each 4 GB",
}

// fixable says Fix my plans puts the rule right (MakePlanMonthly). A price
// under the floor is the seller's to set, at the price the page suggests.
func (r planRule) fixable() bool {
	return r == ruleDollars || r == ruleMonthly || r == ruleNoTrial
}

// planIssue is a rule a hosting plan breaks, with what the plan's line
// says of it in a few words, and, when Fix my plans can't put it right,
// what the seller changes in Whop, in a plain line.
type planIssue struct {
	rule    planRule
	says    string
	blocked string
}

// sellerPriceOf is a hosting plan as its seller prices it, and false for a
// plan that isn't one (hostedPlan) or that's archived, since an archived
// plan no longer sells.
func sellerPriceOf(p whop.Plan) (sellerPrice, bool) {
	if p.Visibility == "archived" {
		return sellerPrice{}, false
	}
	return hostedPlan(p)
}

// hostedPlan is a hosting plan, archived or not, as Open the store judges
// it, with what stops it selling hosting as its Problem; false for a plan
// that isn't one: one whose metadata allows no servers or that has no
// product, as for Playkeeper's share (whopShareWants).
func hostedPlan(p whop.Plan) (sellerPrice, bool) {
	servers, memoryMB, ok := whop.PlanAllowance(p.Metadata)
	if !ok || p.Product.ID == "" {
		return sellerPrice{}, false
	}
	sp := sellerPrice{ID: p.ID, Title: cmpOr(p.Title, p.ID), Servers: servers, MemoryMB: memoryMB, Price: centsOf(p.RenewalPrice),
		Currency: strings.ToLower(cmpOr(p.Currency, "usd")), Floor: whopFloorFor(memoryMB), Share: whopShareFor(memoryMB), Suggested: whopSuggestedFor(memoryMB)}
	monthly := p.PlanType == "renewal" && p.BillingPeriod == 30
	usd := strings.EqualFold(p.Currency, "usd")
	sp.Settable = monthly && usd
	switch least := centsOf(whopLeastCharge(p)); {
	case p.PlanType != "renewal":
		sp.Problem = "It doesn't renew every month, as hosted plans do."
		sp.issues = []planIssue{{rule: ruleOneTime, says: "is charged only once", blocked: sp.Title + " is charged only once. Replace it with a monthly plan in Whop."}}
	case !usd:
		sp.Problem = fmt.Sprintf("It's priced in %s, and hosted plans are priced in US dollars.", strings.ToUpper(p.Currency))
		sp.issues = []planIssue{{rule: ruleDollars, says: "priced in " + strings.ToUpper(p.Currency)}}
	case !monthly:
		sp.Problem = "It doesn't renew every month, as hosted plans do."
		sp.issues = []planIssue{{rule: ruleMonthly, says: "doesn't renew every month"}}
	case p.TrialDays > 0:
		sp.Problem = "It has a free trial, and hosted plans charge from the first day."
		sp.issues = []planIssue{{rule: ruleNoTrial, says: "has a free trial"}}
	case least < sp.Floor:
		under := fmt.Sprintf("charges %s, under the %s floor for %s", dollarsOf(least), dollarsOf(sp.Floor), gigabytes(memoryMB))
		sp.Problem = "It " + under + "."
		sp.issues = []planIssue{{rule: ruleFloor, says: under}}
	}
	return sp, true
}

// sellerStore lets through a call the seller's page makes about the store
// it names: from the page itself, by the business's team with Whop's token
// (sellerAuth), for an app store of this dashboard. It answers a refusal
// itself, and returns the store, who's asking and a client acting on the
// store. A change then holds whopMu and looks at the store again
// (sellerStoreToChange).
func (s *Server) sellerStore(w http.ResponseWriter, r *http.Request, store string) (whopStore, string, *whop.Client, bool) {
	if !fromSellerPage(r) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Open this page inside your Whop dashboard.", "")
		return whopStore{}, "", nil, false
	}
	user, err := s.sellerAuth(r, store)
	if err != nil {
		s.sellerRefusal(w, err)
		return whopStore{}, "", nil, false
	}
	st, ok, err := s.whopStoreByID(r.Context(), store)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return whopStore{}, "", nil, false
	case !ok || st.Via != whopViaApp:
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Playkeeper Cloud doesn't sell for this business from this dashboard.", "")
		return whopStore{}, "", nil, false
	}
	c, err := s.whopClientFor(r.Context(), st)
	if err != nil {
		s.sellerRefusal(w, err)
		return whopStore{}, "", nil, false
	}
	return st, user, c, true
}

// sellerStoreToChange reads the store again for a change, once the change
// holds whopMu, and lets it through while it's neither suspended nor gone:
// the owner may have suspended it, or it may have left, while the change
// waited for the lock. It answers a refusal itself.
func (s *Server) sellerStoreToChange(ctx context.Context, w http.ResponseWriter, id string) (whopStore, bool) {
	st, ok, err := s.whopStoreByID(ctx, id)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return whopStore{}, false
	case !ok:
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Playkeeper Cloud doesn't sell for this business from this dashboard.", "")
		return whopStore{}, false
	case !st.SuspendedAt.IsZero():
		writeErr(w, http.StatusConflict, api.CodeConflict, "Playkeeper has suspended your store, so it can't change here. Ask Playkeeper why.", "")
		return whopStore{}, false
	case !st.LeftAt.IsZero():
		writeErr(w, http.StatusConflict, api.CodeConflict, "Your store left Playkeeper Cloud. Open this page again to connect it.", "")
		return whopStore{}, false
	}
	return st, true
}

// sellerPricesOf reads the store's hosting plans from Whop, as its seller
// prices them.
func (s *Server) sellerPricesOf(ctx context.Context, c *whop.Client, st whopStore) (sellerPrices, error) {
	plans, err := c.Plans(ctx, st.ID)
	if err != nil {
		return sellerPrices{}, err
	}
	return sellerPricesFrom(st, plans), nil
}

// sellerPricesFrom is the store's hosting plans among plans, as its seller
// prices them. A plan that allows more or less than the fleet runs has that
// as its problem, before any other: the store's pass would start none of
// its buyers, and the floor follows from what it allows. A plan whose
// product sells another plan that isn't archived has that on top of any
// other problem (sharedProductProblems).
func sellerPricesFrom(st whopStore, plans []whop.Plan) sellerPrices {
	v := sellerPrices{Plans: []sellerPrice{}, CanOpen: st.ClosedWhy != "" && st.SuspendedAt.IsZero() && st.LeftAt.IsZero()}
	shared := sharedProducts(slices.DeleteFunc(slices.Clone(plans), func(p whop.Plan) bool {
		return p.Visibility == "archived"
	}))
	seen := map[string]bool{}
	for _, p := range plans {
		if sp, ok := sellerPriceOf(p); ok {
			if problem, issue := allowanceIssue(sp.Title, sp.Servers, sp.MemoryMB); problem != "" {
				sp.Problem, sp.issues = problem, []planIssue{issue}
			} else if sh, ok := shared[p.ID]; ok {
				sp.Problem = strings.TrimSpace(sp.Problem + " " + sh.problem())
				sp.issues = append(sp.issues, sh.issue())
			}
			for _, is := range sp.issues {
				switch {
				case is.rule.fixable():
					v.Fixable = true
				case is.blocked != "" && !seen[is.blocked]:
					seen[is.blocked] = true
					v.Blocked = append(v.Blocked, is.blocked)
				}
			}
			v.Plans = append(v.Plans, sp)
		}
	}
	v.CanUpdate = st.ClosedWhy == "" && st.SuspendedAt.IsZero() && st.LeftAt.IsZero()
	return v
}

// sharedProductProblems is, by plan id, what's wrong with each of plans
// whose product has another of them, for hosting or not: Playkeeper's share
// is one percentage on the whole product (Whop's single_product), set for
// its neediest hosting plan, so it would take more than its share of another
// hosting plan's payments, and a cut of a plan that isn't for hosting. Each
// hosting product sells one plan. Which plans count is the caller's: those
// that sell, for Open the store.
func sharedProductProblems(plans []whop.Plan) map[string]string {
	out := map[string]string{}
	for id, sh := range sharedProducts(plans) {
		out[id] = sh.problem()
	}
	return out
}

// sharedProduct is the product a plan shares with other plans, their
// titles, and every plan's on it, the plan's own included, in Whop's order.
type sharedProduct struct {
	product string
	others  []string
	all     []string
}

// sharedProducts is, by plan id, the product of each of plans whose product
// has another of them, as for sharedProductProblems.
func sharedProducts(plans []whop.Plan) map[string]sharedProduct {
	byProduct := map[string][]whop.Plan{}
	for _, p := range plans {
		if p.Product.ID != "" {
			byProduct[p.Product.ID] = append(byProduct[p.Product.ID], p)
		}
	}
	out := map[string]sharedProduct{}
	for _, same := range byProduct {
		if len(same) < 2 {
			continue
		}
		for _, p := range same {
			sh := sharedProduct{product: cmpOr(p.Product.Title, p.Product.ID)}
			for _, o := range same {
				if o.ID != p.ID {
					sh.others = append(sh.others, cmpOr(o.Title, o.ID))
				}
				sh.all = append(sh.all, cmpOr(o.Title, o.ID))
			}
			out[p.ID] = sh
		}
	}
	return out
}

func (sh sharedProduct) problem() string {
	return fmt.Sprintf("It shares its product, %s, with %s, and a hosted plan needs a product of its own, since Playkeeper's share is set on the whole product.",
		sh.product, andList(sh.others))
}

// issue is the shared product as one of a plan's issues, whose blocked line
// is the same for every plan on the product, so the seller reads it once.
func (sh sharedProduct) issue() planIssue {
	return planIssue{rule: ruleOwnProduct, says: "shares " + sh.product + " with " + andList(sh.others),
		blocked: andList(sh.all) + " are on the same product. Give each its own product in Whop."}
}

// andList writes names as a person lists them, such as "Plus, Max and
// Merch".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// allowanceProblem is what keeps a hosting plan that allows servers with
// memoryMB between them from selling, when that's outside the bounds the
// store's pass runs (invites.Allowance.Check), or "".
func allowanceProblem(servers, memoryMB int) string {
	problem, _ := allowanceIssue("", servers, memoryMB)
	return problem
}

// allowanceIssue is allowanceProblem, with the issue it is for the plan
// titled title.
func allowanceIssue(title string, servers, memoryMB int) (string, planIssue) {
	switch {
	case (invites.Allowance{Servers: servers, MemoryMB: memoryMB}).Check() == nil:
		return "", planIssue{}
	case servers < 1 || servers > invites.MaxAllowanceServers:
		return fmt.Sprintf("It allows %d servers, and hosted plans allow 1 to %d.", servers, invites.MaxAllowanceServers),
			planIssue{rule: ruleAllowance, says: fmt.Sprintf("allows %d servers", servers),
				blocked: fmt.Sprintf("%s allows %d servers. Make it %d or fewer in Whop.", title, servers, invites.MaxAllowanceServers)}
	}
	fix := "Make it " + gigabytes(invites.MaxAllowanceMemoryMB) + " or less in Whop."
	if memoryMB < invites.MinAllowanceMemoryMB {
		fix = "Make it " + gigabytes(invites.MinAllowanceMemoryMB) + " or more in Whop."
	}
	return fmt.Sprintf("It allows %s, and hosted plans allow %s to %s.", gigabytes(memoryMB), gigabytes(invites.MinAllowanceMemoryMB), gigabytes(invites.MaxAllowanceMemoryMB)),
		planIssue{rule: ruleAllowance, says: "allows " + gigabytes(memoryMB), blocked: fmt.Sprintf("%s allows %s. %s", title, gigabytes(memoryMB), fix)}
}

// markHostedProducts marks each of the store's hosting products, the ones
// with a hosting plan among plans, as sold by this dashboard for the
// store's business (whop.WithSeller): the store site shows a product's
// plans only once it's marked so. A product already marked so is left
// alone.
func (s *Server) markHostedProducts(ctx context.Context, c *whop.Client, st whopStore, plans []whop.Plan) error {
	dash, err := s.dashboardURL(ctx)
	if err != nil {
		return err
	}
	if dash == "" {
		return errNoDashboardAddress
	}
	hosting := map[string]bool{}
	for _, p := range plans {
		if _, ok := sellerPriceOf(p); ok {
			hosting[p.Product.ID] = true
		}
	}
	products, err := c.Products(ctx, st.ID)
	if err != nil {
		return err
	}
	for _, p := range products {
		if !hosting[p.ID] {
			continue
		}
		meta, differs := whop.WithSeller(p.Metadata, dash, st.ID)
		if !differs {
			continue
		}
		if err := c.SetProductMetadata(ctx, p.ID, meta); err != nil {
			return err
		}
	}
	return nil
}

// showHostedPlans makes each of the store's hosting plans among plans
// visible on Whop, where a copy's plans arrive hidden: the store site lists
// only visible plans. A plan already visible is left alone.
func showHostedPlans(ctx context.Context, c *whop.Client, plans []whop.Plan) error {
	for _, p := range plans {
		if _, ok := sellerPriceOf(p); !ok || p.Visibility == "visible" {
			continue
		}
		if err := c.ShowPlan(ctx, p.ID); err != nil {
			return err
		}
	}
	return nil
}

// readWhopStoreSoon has the store's next pass read it from Whop, and has
// that pass come now.
func (s *Server) readWhopStoreSoon(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `UPDATE whop_stores SET synced_at = 0 WHERE store_id = ?`, id); err != nil {
		s.log.Error("could not have a store read again", "store", id, "err", err)
	}
	s.kickWhopStore(id)
}

// hWhopSellerPrices is a seller's page reading the store's hosting plans as
// the seller prices them, from Whop.
func (s *Server) hWhopSellerPrices(w http.ResponseWriter, r *http.Request, store string) {
	st, _, c, ok := s.sellerStore(w, r, store)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopTimeout)
	defer cancel()
	v, err := s.sellerPricesOf(ctx, c, st)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hWhopSellerSetPrice is a seller setting a hosting plan's monthly price, at
// or above the floor, which Whop then charges first and at each renewal.
// Once the store has Playkeeper's share, the share follows the new price at
// once, as the store's pass would have it follow. A lower price, which
// needs a higher share, has that share set first, with force, and is
// refused when it can't be (syncWhopSharesAt), so no payment at it pays
// less than Playkeeper's share; a higher one is set first, as the share it
// had pays at least Playkeeper's meanwhile.
func (s *Server) hWhopSellerSetPrice(w http.ResponseWriter, r *http.Request, store string) {
	st, user, c, ok := s.sellerStore(w, r, store)
	if !ok {
		return
	}
	var req struct {
		Plan  string `json:"plan"`
		Price string `json:"price"`
	}
	if err := decodeJSON(r, &req); err != nil || !reWhopID.MatchString(req.Plan) {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Name one of your store's plans and its price.", "")
		return
	}
	price, err := parseSellerPrice(req.Price)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Error{Error: err.Error() + ".", Code: api.CodeInvalid, Field: "price"})
		return
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	if st, ok = s.sellerStoreToChange(r.Context(), w, st.ID); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopCallsFor)
	defer cancel()
	plans, err := c.Plans(ctx, st.ID)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	at := -1
	var sp sellerPrice
	for i, p := range plans {
		if p.ID == req.Plan {
			if priced, hosting := sellerPriceOf(p); hosting {
				at, sp = i, priced
			}
		}
	}
	switch {
	case at < 0:
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "That isn't one of your store's hosting plans.", "")
		return
	case !sp.Settable:
		writeErr(w, http.StatusConflict, api.CodeConflict, sp.Title+": "+sp.Problem+" Change it on Whop.", "")
		return
	case price < sp.Floor:
		msg := fmt.Sprintf("%s allows %s, so it charges at least %s a month.", sp.Title, gigabytes(sp.MemoryMB), dollarsOf(sp.Floor))
		writeJSON(w, http.StatusBadRequest, api.Error{Error: msg, Code: api.CodeInvalid, Field: "price"})
		return
	}
	set, err := s.whopSharesSet(ctx, st.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	// Whatever this changes on Whop, the store's next read comes now: it
	// takes up the new price, or puts back a share raised for one that
	// didn't come.
	defer s.readWhopStoreSoon(ctx, st.ID)
	shareFirst := len(set) > 0 && price < sp.Price
	if shareFirst {
		problem, err := s.syncWhopSharesAt(ctx, c, st, true, map[string]float64{sp.ID: float64(price) / 100})
		switch {
		case err != nil:
			s.sellerRefusal(w, err)
			return
		case problem != "":
			writeErr(w, http.StatusConflict, api.CodeConflict, "The price stays as it was, since Playkeeper's share couldn't be set for it first: "+problem, "")
			return
		}
	}
	updated, err := c.SetPlanPrice(ctx, sp.ID, float64(price)/100)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	if updated.ID == sp.ID {
		plans[at] = updated
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE whop_plans SET price = ?, free = ? WHERE store_id = ? AND plan_id = ?`, plans[at].Price(), plans[at].Free(), st.ID, sp.ID); err != nil {
		s.log.Error("could not keep a plan's new price", "store", st.ID, "plan", sp.ID, "err", err)
	}
	s.audit("whop:"+user, "whop.plan_price", st.ID, "succeeded", fmt.Sprintf("%s charges %s a month", sp.ID, dollarsOf(price)))
	v := sellerPricesFrom(st, plans)
	if len(set) > 0 && !shareFirst {
		problem, err := s.syncWhopShares(ctx, c, st, false)
		if err != nil {
			s.log.Warn("could not have Playkeeper's share follow a new price", "store", st.ID, "err", err)
			problem = "Playkeeper's share on Whop hasn't followed the new price yet (" + whopProblem(err) + "). The store's next read tries again."
		}
		v.Problem = problem
	}
	writeJSON(w, http.StatusOK, v)
}

// sellerFixed is what Fix my plans did: what it changed, in a line, "" for
// nothing, and the store's prices then.
type sellerFixed struct {
	Changed string       `json:"changed,omitempty"`
	Prices  sellerPrices `json:"prices"`
}

// hWhopSellerFix is a seller pressing Fix my plans, before their store is
// open: each hosting plan that renews then does so every month, in US
// dollars, with no free trial, keeping its price (MakePlanMonthly), with the
// app's permission to update plans. It says in a line what it changed. A
// plan charged only once, one allowing more than the fleet runs and plans
// sharing a product stay the seller's to change in Whop, as the prices'
// Blocked lines say, and so does a price under the floor, which the page
// suggests one for. An open store's plans stay as they are.
func (s *Server) hWhopSellerFix(w http.ResponseWriter, r *http.Request, store string) {
	st, user, c, ok := s.sellerStore(w, r, store)
	if !ok {
		return
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	if st, ok = s.sellerStoreToChange(r.Context(), w, st.ID); !ok {
		return
	}
	if st.ClosedWhy == "" {
		writeErr(w, http.StatusConflict, api.CodeConflict, "Your store is open, so its plans stay as they are.", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopCallsFor)
	defer cancel()
	plans, err := c.Plans(ctx, st.ID)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	var changed []string
	for i, p := range plans {
		if _, hosting := sellerPriceOf(p); !hosting || p.PlanType != "renewal" {
			continue
		}
		var now []string
		if p.BillingPeriod != 30 {
			now = append(now, "renews every month")
		}
		if !strings.EqualFold(p.Currency, "usd") {
			now = append(now, "is in US dollars")
		}
		if p.TrialDays > 0 {
			now = append(now, "has no free trial")
		}
		if len(now) == 0 {
			continue
		}
		updated, err := c.MakePlanMonthly(ctx, p.ID)
		if err != nil {
			s.sellerRefusal(w, err)
			return
		}
		if updated.ID == p.ID {
			plans[i] = updated
		}
		changed = append(changed, cmpOr(p.Title, p.ID)+" now "+andList(now)+".")
	}
	line := strings.Join(changed, " ")
	if line != "" {
		s.audit("whop:"+user, "whop.plan_fix", st.ID, "succeeded", line)
		s.readWhopStoreSoon(ctx, st.ID)
	}
	writeJSON(w, http.StatusOK, sellerFixed{Changed: line, Prices: sellerPricesFrom(st, plans)})
}

// refusedPlans is what Open the store says when it refuses plans: a line
// for each plan with what's wrong with it, in a few words, and how to meet
// the rules they break, each once.
func refusedPlans(plans []sellerPrice) (lines []string, fix string) {
	broken := map[planRule]bool{}
	for _, p := range plans {
		if len(p.issues) == 0 {
			continue
		}
		says := make([]string, len(p.issues))
		for i, is := range p.issues {
			says[i], broken[is.rule] = is.says, true
		}
		lines = append(lines, p.Title+": "+strings.Join(says, "; "))
	}
	var fixes []string
	for rule, f := range planRuleFixes {
		if broken[planRule(rule)] {
			fixes = append(fixes, f)
		}
	}
	return lines, andList(fixes)
}

// sellerOpened is what pressing Open the store did: whether the store is
// open now, and if not, why it's still closed.
type sellerOpened struct {
	Open bool   `json:"open"`
	Why  string `json:"why,omitempty"`
}

// hWhopSellerSell is a seller pressing Open the store, or Update the store
// once it's open, which puts a hosting plan added since on the store site.
// Every hosting plan must renew monthly in US dollars, without a trial, at
// or above the floor, and allow what the fleet runs (sellerPricesFrom).
// Then Playkeeper's share is set on each hosting product, putting right any
// share the seller changed (syncWhopShares, with force), and only when that
// leaves nothing wrong are the hosting products marked for the store site
// and the hosting plans made visible there, and the store opens, for its
// seller's reason alone. A refusal leaves an open store open: only its
// pass closes it. One for plans gives each a line with its problems, and
// says once how to meet the rules they break (refusedPlans).
func (s *Server) hWhopSellerSell(w http.ResponseWriter, r *http.Request, store string) {
	st, user, c, ok := s.sellerStore(w, r, store)
	if !ok {
		return
	}
	s.whopMu.Lock()
	defer s.whopMu.Unlock()
	if st, ok = s.sellerStoreToChange(r.Context(), w, store); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), whopCallsFor)
	defer cancel()
	plans, err := c.Plans(ctx, st.ID)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	v := sellerPricesFrom(st, plans)
	lines, fix := refusedPlans(v.Plans)
	switch {
	case len(v.Plans) == 0:
		writeErr(w, http.StatusConflict, api.CodeConflict, "Your store has no hosting plan yet. A hosting plan's metadata says how many servers and how much memory it allows.", "")
		return
	case len(lines) > 0:
		to := "open"
		if st.ClosedWhy == "" {
			to = "update"
		}
		writeErr(w, http.StatusConflict, api.CodeConflict, strings.Join(lines, "\n"), "To "+to+" your store, "+fix+".")
		return
	}
	problem, err := s.syncWhopShares(ctx, c, st, true)
	if err != nil {
		s.sellerRefusal(w, err)
		return
	}
	if problem != "" {
		writeErr(w, http.StatusConflict, api.CodeConflict, problem, "")
		return
	}
	if err := s.markHostedProducts(ctx, c, st, plans); err != nil {
		if errors.Is(err, errNoDashboardAddress) || errors.Is(err, errAddressUnknown) {
			s.log.Warn("a store couldn't open without the dashboard's address", "store", st.ID, "err", err)
			writeErr(w, http.StatusServiceUnavailable, api.CodeRetryLater, "Playkeeper Cloud can't open stores just now. Try again in a few minutes.", "")
			return
		}
		s.sellerRefusal(w, err)
		return
	}
	if err := showHostedPlans(ctx, c, plans); err != nil {
		s.sellerRefusal(w, err)
		return
	}
	open, err := s.openWhopStore(ctx, st.ID, whopNotOpenYet)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	pressed := "Open the store"
	if st.ClosedWhy == "" {
		pressed = "Update the store"
	}
	s.audit("whop:"+user, "whop.store_sell", st.ID, "succeeded", pressed+": Playkeeper's share is set on each hosting product, the products are marked for the store site, and their plans are visible")
	s.readWhopStoreSoon(ctx, st.ID)
	out := sellerOpened{Open: open}
	if !open {
		if now, found, err := s.whopStoreByID(ctx, st.ID); err == nil && found {
			out.Why = now.ClosedWhy
		}
	}
	writeJSON(w, http.StatusOK, out)
}
