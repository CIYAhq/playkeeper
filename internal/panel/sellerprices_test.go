package panel

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// asSeller is a call the seller's page makes through Whop's proxy: with
// the token, from the page itself.
func (e *env) asSeller(t *testing.T, method, path, body, token string, hdr map[string]string) resp {
	t.Helper()
	h := map[string]string{whop.UserTokenHeader: token, "X-Requested-With": "playkeeper", "Sec-Fetch-Site": "same-origin", "Origin": ""}
	for k, v := range hdr {
		h[k] = v
	}
	return e.do(t, method, whopSellerPrefix+path, body, h)
}

// openedAsSeller is sellerEnv once Other Hosting's owner opened its page,
// so its store is registered and not open yet, with the owner's token.
func openedAsSeller(t *testing.T) (*fakeWhop, *env, string) {
	t.Helper()
	f, e := sellerEnv(t)
	token := f.sellerToken(e, whopTestApp, "user_otherowner")
	if r := e.openAsSeller(t, "biz_other", token, nil); r.status != http.StatusOK {
		t.Fatalf("opening Other Hosting's page: %d %v", r.status, r.body)
	}
	return f, e, token
}

// sharesGoToSiya names the owner's own Whop account to receive
// Playkeeper's share, as the owner does in Settings › Sell on Whop.
func sharesGoToSiya(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET share_user = 'user_siya', share_username = 'siyabuilt' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
}

// addOtherPlan gives Other Hosting's store another plan, on its product.
func (f *fakeWhop) addOtherPlan(plan map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := map[string]any{"visibility": "visible", "plan_type": "renewal", "billing_period": 30, "currency": "usd",
		"product": map[string]any{"id": "prod_other", "title": "Minecraft server"}, "unlimited_stock": true}
	for k, v := range plan {
		p[k] = v
	}
	f.installed["biz_other"].plans = append(f.installed["biz_other"].plans, p)
}

// setOtherProduct gives one of Other Hosting's products this metadata on
// Whop, adding the product if it's new.
func (f *fakeWhop) setOtherProduct(id string, meta whop.Metadata) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed["biz_other"].products[id] = meta
}

// otherProduct is one of Other Hosting's products' metadata as Whop has it.
func (f *fakeWhop) otherProduct(id string) whop.Metadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.installed["biz_other"].products[id])
}

// setOtherPlan changes one of Other Hosting's plans on Whop, as its seller
// might there.
func (f *fakeWhop) setOtherPlan(id string, changes map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.installed["biz_other"].plan(id)
	for k, v := range changes {
		p[k] = v
	}
}

// otherCheckoutCharge is what Whop charges at checkout for one of Other
// Hosting's plans: its initial price, plus its first renewal price for a
// plan that renews.
func (f *fakeWhop) otherCheckoutCharge(id string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.installed["biz_other"].plan(id)
	charge, _ := p["initial_price"].(float64)
	if p["plan_type"] == "renewal" {
		renewal, _ := strconv.ParseFloat(fmt.Sprint(p["renewal_price"]), 64)
		charge += renewal
	}
	return charge
}

// otherPlanVisibility is how one of Other Hosting's plans shows on Whop.
func (f *fakeWhop) otherPlanVisibility(id string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed["biz_other"].plan(id)["visibility"]
}

// pricesOf is the seller's prices a page read.
func pricesOf(t *testing.T, r resp) sellerPrices {
	t.Helper()
	var v sellerPrices
	b, err := json.Marshal(r.body)
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// priceOf is one of the plans a seller's prices hold.
func priceOf(t *testing.T, v sellerPrices, plan string) sellerPrice {
	t.Helper()
	for _, p := range v.Plans {
		if p.ID == plan {
			return p
		}
	}
	t.Fatalf("no plan %s among %+v", plan, v.Plans)
	return sellerPrice{}
}

// A seller's page lists the store's hosting plans at their prices, with
// the floor, $12 a month for each 4 GB, and Playkeeper's share of each
// payment. The seller sets a price at or above the floor, which Whop then
// charges once at checkout and on each renewal, and the dashboard shows at
// once. A price under the floor, a plan of another store and a plan priced
// in another currency are refused.
func TestASellerPricesTheirPlansAtOrAboveTheFloor(t *testing.T) {
	f, e, token := openedAsSeller(t)
	f.addOtherPlan(map[string]any{"id": "plan_plus", "title": "Plus", "renewal_price": 20, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "8"},
		"product": map[string]any{"id": "prod_plus", "title": "Plus server"}})
	f.addOtherPlan(map[string]any{"id": "plan_euro", "title": "Euro", "currency": "eur", "renewal_price": 14, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"},
		"product": map[string]any{"id": "prod_euro", "title": "Euro server"}})
	f.addOtherPlan(map[string]any{"id": "plan_merch", "title": "Merch", "renewal_price": 5, "product": map[string]any{"id": "prod_merch", "title": "Merch"}})
	e.reconcile()
	r := e.asSeller(t, "GET", "biz_other/prices", "", token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("the prices: %d %v", r.status, r.body)
	}
	v := pricesOf(t, r)
	if len(v.Plans) != 3 || !v.CanOpen {
		t.Fatalf("the prices: %+v", v)
	}
	if p := priceOf(t, v, "plan_other"); p.Price != 1200 || p.Floor != 1200 || p.Share != 850 || p.MemoryMB != 4096 || !p.Settable || p.Problem != "" {
		t.Fatalf("Other: %+v", p)
	}
	if p := priceOf(t, v, "plan_plus"); p.Price != 2000 || p.Floor != 2400 || p.Share != 1700 || p.Problem != "It charges $20.00, under the $24.00 floor for 8 GB." {
		t.Fatalf("Plus: %+v", p)
	}
	if p := priceOf(t, v, "plan_euro"); p.Settable || !strings.Contains(p.Problem, "EUR") {
		t.Fatalf("Euro: %+v", p)
	}

	set := func(plan, price string) resp {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"plan": plan, "price": price})
		return e.asSeller(t, "POST", "biz_other/prices", string(body), token, nil)
	}
	r = set("plan_other", "15")
	if r.status != http.StatusOK || priceOf(t, pricesOf(t, r), "plan_other").Price != 1500 || !slices.Equal(f.priceSets, []string{"plan_other=15"}) {
		t.Fatalf("setting Other to $15: %d %v, prices set %v", r.status, r.body, f.priceSets)
	}
	if charge := f.otherCheckoutCharge("plan_other"); charge != 15 {
		t.Fatalf("Whop charges $%v at checkout for Other at $15 a month", charge)
	}
	var shown string
	e.srv.db.QueryRow(`SELECT price FROM whop_plans WHERE store_id = 'biz_other' AND plan_id = 'plan_other'`).Scan(&shown)
	if !strings.Contains(shown, "15.00") {
		t.Fatalf("the dashboard shows Other at %q", shown)
	}
	if rows := e.auditRows(t, "whop.plan_price"); len(rows) != 1 || !strings.HasPrefix(rows[0], "whop:user_otherowner biz_other succeeded plan_other charges $15.00 a month") {
		t.Fatalf("the audit log: %v", rows)
	}
	for name, c := range map[string]struct {
		plan, price string
		status      int
		says        string
	}{
		"under the floor":       {"plan_plus", "23.99", http.StatusBadRequest, "Plus allows 8 GB, so it charges at least $24.00 a month."},
		"not a price":           {"plan_other", "fifteen", http.StatusBadRequest, "Write the price in dollars"},
		"too many cents":        {"plan_other", "15.001", http.StatusBadRequest, "Write the price in dollars"},
		"another currency":      {"plan_euro", "15", http.StatusConflict, "US dollars"},
		"not a hosting plan":    {"plan_merch", "15", http.StatusNotFound, "isn't one of your store's hosting plans"},
		"another store's plan":  {"plan_starter", "15", http.StatusNotFound, "isn't one of your store's hosting plans"},
		"no plan":               {"", "15", http.StatusBadRequest, "Name one of your store's plans"},
		"a plan that isn't one": {"plan/other", "15", http.StatusBadRequest, "Name one of your store's plans"},
	} {
		if r := set(c.plan, c.price); r.status != c.status || !strings.Contains(r.body["error"].(string), c.says) {
			t.Errorf("%s: %d %v", name, r.status, r.body)
		}
	}
	if !slices.Equal(f.priceSets, []string{"plan_other=15"}) {
		t.Fatalf("a refused price reached Whop: %v", f.priceSets)
	}
	if r := set("plan_plus", "$24"); r.status != http.StatusOK || priceOf(t, pricesOf(t, r), "plan_plus").Problem != "" {
		t.Fatalf("setting Plus to the floor: %d %v", r.status, r.body)
	}
	f.priceDown = true
	if r := set("plan_other", "16"); r.status != http.StatusBadGateway {
		t.Fatalf("with Whop refusing the price: %d %v", r.status, r.body)
	}
}

// Open the store sets Playkeeper's share on each hosting product, from its
// price, marks each hosting product as this dashboard's for the store's
// business, which the store site needs to show its plans, makes the hosting
// plans visible, since a copy's plans arrive hidden and the store site
// lists only visible ones, and only then opens the store, which then
// sells. A product that isn't for hosting keeps its metadata, and its plan
// stays hidden. Once the store is open, a new price takes the share along
// at once.
func TestOpenTheStoreSetsPlaykeepersShareThenOpensIt(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	f.setOtherProduct("prod_other", whop.Metadata{"color": "blue"})
	f.setOtherProduct("prod_merch", whop.Metadata{"color": "green"})
	f.setOtherPlan("plan_other", map[string]any{"visibility": "hidden"})
	f.addOtherPlan(map[string]any{"id": "plan_merch", "title": "Merch", "visibility": "hidden", "renewal_price": 5, "product": map[string]any{"id": "prod_merch", "title": "Merch"}})
	r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil)
	if r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store: %d %v", r.status, r.body)
	}
	if share := f.share("biz_other", "prod_other"); share == nil || share["commission_value"] != 70.84 {
		t.Fatalf("Playkeeper's share on Other's product: %v", share)
	}
	if meta := f.otherProduct("prod_other"); meta[whop.MetaDashboard] != whopDashboard || meta[whop.MetaBusiness] != "biz_other" || meta["color"] != "blue" {
		t.Fatalf("Other's hosting product: %v", meta)
	}
	if meta := f.otherProduct("prod_merch"); len(meta) != 1 || meta["color"] != "green" {
		t.Fatalf("Other's merch: %v", meta)
	}
	if vis := f.otherPlanVisibility("plan_other"); vis != "visible" {
		t.Fatalf("Other's hosting plan shows as %v on Whop", vis)
	}
	if vis := f.otherPlanVisibility("plan_merch"); vis != "hidden" {
		t.Fatalf("Other's merch plan shows as %v on Whop", vis)
	}
	if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != "" {
		t.Fatalf("the store is still closed: %q", st.ClosedWhy)
	}
	if v := sellerViewOf(t, e.viewAsSeller(t, "biz_other", token, nil)); v.Store.State != "selling" {
		t.Fatalf("the seller's view: %+v", v.Store)
	}
	if v := pricesOf(t, e.asSeller(t, "GET", "biz_other/prices", "", token, nil)); v.CanOpen {
		t.Fatalf("Open the store is still offered: %+v", v)
	}
	if rows := e.auditRows(t, "whop.store_sell"); len(rows) != 1 || !strings.HasPrefix(rows[0], "whop:user_otherowner biz_other succeeded") {
		t.Fatalf("the audit log: %v", rows)
	}
	r = e.asSeller(t, "POST", "biz_other/prices", `{"plan":"plan_other","price":"15"}`, token, nil)
	if r.status != http.StatusOK || r.body["problem"] != nil {
		t.Fatalf("a new price: %d %v", r.status, r.body)
	}
	if share := f.share("biz_other", "prod_other"); share["commission_value"] != 56.67 {
		t.Fatalf("Playkeeper's share after the new price: %v", share)
	}
}

// Open the store puts right a share already on Whop that pays less than the
// price needs, such as one the seller lowered or one left from an earlier
// install, since it sets the shares with force.
func TestOpenTheStorePutsRightAShareThatPaysTooLittle(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	f.mu.Lock()
	f.installed["biz_other"].shares = []map[string]any{{"id": "ovr_old", "override_type": "rev_share", "product_id": "prod_other",
		"commission_type": "percentage", "commission_value": 50.0, "revenue_basis": "pre_fees"}}
	f.mu.Unlock()
	if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store: %d %v", r.status, r.body)
	}
	if share := f.share("biz_other", "prod_other"); share["id"] != "ovr_old" || share["commission_value"] != 70.84 {
		t.Fatalf("the share: %v", share)
	}
}

// Open the store leaves the store closed while anything is wrong: nobody
// named to receive Playkeeper's share, a plan under the floor, one that
// doesn't renew monthly or that has a free trial, no hosting plan at all,
// Whop refusing the products' marks or the plans' visibility, no address,
// or the store suspended or gone. A plan's problem leaves the shares alone,
// and the plans stay hidden until the shares and marks are done.
func TestOpenTheStoreKeepsItClosedWhileAnythingIsWrong(t *testing.T) {
	f, e, token := openedAsSeller(t)
	f.setOtherPlan("plan_other", map[string]any{"visibility": "hidden"})
	sell := func(name string, status int, says string) {
		t.Helper()
		r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil)
		if r.status != status || !strings.Contains(r.body["error"].(string), says) {
			t.Fatalf("%s: %d %v", name, r.status, r.body)
		}
		if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != whopNotOpenYetWhy {
			t.Fatalf("%s opened the store: %q", name, st.ClosedWhy)
		}
	}
	sell("nobody receiving the share", http.StatusConflict, "doesn't take its share yet")
	sharesGoToSiya(t, e)
	f.setOtherPlan("plan_other", map[string]any{"renewal_price": 10})
	sell("a plan under the floor", http.StatusConflict, "Other: charges $10.00, under the $12.00 floor for 4 GB")
	f.setOtherPlan("plan_other", map[string]any{"renewal_price": 12, "billing_period": 365})
	sell("a plan renewing yearly", http.StatusConflict, "doesn't renew every month")
	f.setOtherPlan("plan_other", map[string]any{"billing_period": 30, "trial_period_days": 7})
	sell("a plan with a free trial", http.StatusConflict, "free trial")
	if len(f.shareWrites) != 0 {
		t.Fatalf("a plan's problem set shares: %v", f.shareWrites)
	}
	f.mu.Lock()
	partner := f.installed["biz_other"].partner
	f.mu.Unlock()
	if partner != "" {
		t.Fatalf("a plan's problem made %s the business's partner on Whop", partner)
	}
	f.setOtherPlan("plan_other", map[string]any{"trial_period_days": 0, "metadata": map[string]any{}})
	sell("no hosting plan", http.StatusConflict, "no hosting plan yet")
	f.setOtherPlan("plan_other", map[string]any{"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	f.marksDown = true
	sell("Whop refusing the products' marks", http.StatusBadGateway, "access_pass:update")
	f.marksDown = false
	e.setAddress(t, "")
	sell("no address for the store site to send buyers to", http.StatusServiceUnavailable, "can't open stores just now")
	e.setAddress(t, "beta.playkeeper.me")
	if meta := f.otherProduct("prod_other"); meta[whop.MetaDashboard] != "" {
		t.Fatalf("a store that didn't open marked its product: %v", meta)
	}
	if vis := f.otherPlanVisibility("plan_other"); vis != "hidden" {
		t.Fatalf("a store that didn't open showed its plan: %v", vis)
	}
	f.showDown = true
	sell("Whop refusing to show the plans", http.StatusBadGateway, "plan:update")
	f.showDown = false
	writes := len(f.shareWrites)
	if _, err := e.srv.suspendWhopStore(t.Context(), "biz_other", "admin", "griefing"); err != nil {
		t.Fatal(err)
	}
	r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil)
	if r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "suspended") {
		t.Fatalf("a suspended store: %d %v", r.status, r.body)
	}
	if r := e.asSeller(t, "POST", "biz_other/prices", `{"plan":"plan_other","price":"15"}`, token, nil); r.status != http.StatusConflict || len(f.priceSets) != 0 {
		t.Fatalf("pricing a suspended store: %d %v, prices set %v", r.status, r.body, f.priceSets)
	}
	if v := pricesOf(t, e.asSeller(t, "GET", "biz_other/prices", "", token, nil)); v.CanOpen {
		t.Fatalf("a suspended store offers Open the store: %+v", v)
	}
	if _, err := e.srv.liftWhopStore(t.Context(), "biz_other", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.whopStoreLeft(t.Context(), "biz_other", "Playkeeper's share has been gone for 72 hours"); err != nil {
		t.Fatal(err)
	}
	r = e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil)
	if r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "left Playkeeper Cloud") {
		t.Fatalf("a store that left: %d %v", r.status, r.body)
	}
	if len(f.shareWrites) != writes {
		t.Fatalf("a suspended store, or one that left, set shares: %v", f.shareWrites)
	}
}

// Open the store refuses a hosting plan that allows more servers, or more
// or less memory, than the fleet runs, since the store's pass would start
// none of its buyers. The seller's prices say why first, before a price
// under the floor, which follows from what the plan allows. Nothing
// reaches Whop until the plan is within the bounds, at whose edges it
// opens.
func TestOpenTheStoreRefusesAPlanTheFleetDoesntRun(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	f.setOtherPlan("plan_other", map[string]any{"visibility": "hidden"})
	for _, c := range []struct{ servers, gb, line, says string }{
		{"11", "4", "Other: allows 11 servers", "It allows 11 servers, and hosted plans allow 1 to 10."},
		{"1", "0.5", "Other: allows 0.5 GB", "It allows 0.5 GB, and hosted plans allow 1 GB to 64 GB."},
		{"1", "96", "Other: allows 96 GB", "It allows 96 GB, and hosted plans allow 1 GB to 64 GB."},
	} {
		f.setOtherPlan("plan_other", map[string]any{"metadata": map[string]any{whop.MetaServers: c.servers, whop.MetaMemoryGB: c.gb}})
		if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusConflict || r.body["error"] != c.line ||
			r.body["hint"] != "To open your store, allow 1 to 10 servers and 1 to 64 GB." {
			t.Fatalf("Open the store with %s servers and %s GB: %d %v", c.servers, c.gb, r.status, r.body)
		}
		if p := priceOf(t, pricesOf(t, e.asSeller(t, "GET", "biz_other/prices", "", token, nil)), "plan_other"); p.Problem != c.says {
			t.Fatalf("the prices with %s servers and %s GB: %+v", c.servers, c.gb, p)
		}
	}
	if vis := f.otherPlanVisibility("plan_other"); len(f.shareWrites) != 0 || vis != "hidden" {
		t.Fatalf("a plan the fleet doesn't run reached Whop: shares %v, the plan %v", f.shareWrites, vis)
	}
	if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != whopNotOpenYetWhy {
		t.Fatalf("a plan the fleet doesn't run opened the store: %q", st.ClosedWhy)
	}
	f.setOtherPlan("plan_other", map[string]any{"renewal_price": 192, "metadata": map[string]any{whop.MetaServers: "10", whop.MetaMemoryGB: "64"}})
	if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store with 10 servers and 64 GB: %d %v", r.status, r.body)
	}
}

// Open the store refuses a hosting product that sells another plan, for
// hosting or not, since Playkeeper's share is one percentage on the whole
// product: with a $12 and a $15 plan of 4 GB on one product, the $15 plan
// would pay $10.63 instead of $8.50, and a plan that isn't for hosting
// would pay a cut of its own. The seller's prices say so on each hosting
// plan, after any other problem it has, and nothing reaches Whop. The
// refusal gives each plan a line of its own with its problems in a few
// words, then says once how to meet the rules they break. An archived plan
// no longer sells, so it doesn't count, and once each plan has a product of
// its own, the store opens.
func TestOpenTheStoreRefusesAProductThatSellsAnotherPlan(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	f.setOtherPlan("plan_other", map[string]any{"visibility": "hidden", "renewal_price": 10})
	f.addOtherPlan(map[string]any{"id": "plan_dear", "title": "Dear", "renewal_price": 15, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	f.addOtherPlan(map[string]any{"id": "plan_merch", "title": "Merch", "renewal_price": 5})
	f.addOtherPlan(map[string]any{"id": "plan_old", "title": "Old", "visibility": "archived", "renewal_price": 20, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	refused := func(name string, says map[string]string, lines []string, hint string) {
		t.Helper()
		v := pricesOf(t, e.asSeller(t, "GET", "biz_other/prices", "", token, nil))
		for _, p := range v.Plans {
			if p.Problem != says[p.ID] {
				t.Fatalf("%s: %s's problem is %q", name, p.Title, p.Problem)
			}
		}
		if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusConflict || r.body["error"] != strings.Join(lines, "\n") || r.body["hint"] != hint {
			t.Fatalf("Open the store with %s: %d %v", name, r.status, r.body)
		}
	}
	refused("Dear and Merch on Other's product", map[string]string{
		"plan_other": "It charges $10.00, under the $12.00 floor for 4 GB. It shares its product, Minecraft server, with Dear and Merch, and a hosted plan needs a product of its own, since Playkeeper's share is set on the whole product.",
		"plan_dear":  "It shares its product, Minecraft server, with Other and Merch, and a hosted plan needs a product of its own, since Playkeeper's share is set on the whole product.",
	}, []string{
		"Other: charges $10.00, under the $12.00 floor for 4 GB; shares Minecraft server with Dear and Merch",
		"Dear: shares Minecraft server with Other and Merch",
	}, "To open your store, give each plan its own product and charge at least $12.00 a month for each 4 GB.")
	f.setOtherProduct("prod_dear", whop.Metadata{})
	f.setOtherPlan("plan_dear", map[string]any{"product": map[string]any{"id": "prod_dear", "title": "Dear server"}})
	f.setOtherPlan("plan_other", map[string]any{"renewal_price": 12})
	refused("Merch on Other's product", map[string]string{
		"plan_other": "It shares its product, Minecraft server, with Merch, and a hosted plan needs a product of its own, since Playkeeper's share is set on the whole product.",
	}, []string{"Other: shares Minecraft server with Merch"}, "To open your store, give each plan its own product.")
	if vis := f.otherPlanVisibility("plan_other"); len(f.shareWrites) != 0 || vis != "hidden" {
		t.Fatalf("a product that sells another plan reached Whop: shares %v, Other %v", f.shareWrites, vis)
	}
	f.setOtherProduct("prod_merch", whop.Metadata{})
	f.setOtherPlan("plan_merch", map[string]any{"product": map[string]any{"id": "prod_merch", "title": "Merch"}})
	if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store with each plan on its own product, and Old archived on Other's: %d %v", r.status, r.body)
	}
}

// waitForLock waits until a call in the function named fn waits for a
// mutex, as a seller's change does for whopMu while the store's pass holds
// it.
func waitForLock(t *testing.T, fn string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, "(*Mutex).lockSlow") && strings.Contains(g, fn) {
				return
			}
		}
	}
	t.Fatalf("no call in %s waited for a lock", fn)
}

// A seller's change that waits while the owner suspends the store, or while
// it leaves, doesn't change it: the change looks at the store once it holds
// the lock the suspension and the store's pass hold.
func TestASellersChangeThatWaitsSeesASuspensionOrLeaving(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	waiting := func(path, body, fn string, meanwhile string) resp {
		t.Helper()
		e.srv.whopMu.Lock()
		done := make(chan resp, 1)
		go func() { done <- e.asSeller(t, "POST", path, body, token, nil) }()
		waitForLock(t, fn)
		if _, err := e.srv.db.Exec(meanwhile, e.clock.now().UnixMilli()); err != nil {
			t.Error(err)
		}
		e.srv.whopMu.Unlock()
		select {
		case r := <-done:
			return r
		case <-time.After(30 * time.Second):
			t.Fatal("the seller's change didn't answer")
			return resp{}
		}
	}
	r := waiting("biz_other/sell", `{}`, "hWhopSellerSell", `UPDATE whop_stores SET suspended_at = ?, suspend_reason = 'griefing' WHERE store_id = 'biz_other'`)
	if r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "suspended") {
		t.Fatalf("Open the store for a store suspended while it waited: %d %v", r.status, r.body)
	}
	if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != whopNotOpenYetWhy || len(f.shareWrites) != 0 {
		t.Fatalf("a store suspended while Open the store waited: closed %q, shares set %v", st.ClosedWhy, f.shareWrites)
	}
	if _, err := e.srv.db.Exec(`UPDATE whop_stores SET suspended_at = 0, suspend_reason = '' WHERE store_id = 'biz_other'`); err != nil {
		t.Fatal(err)
	}
	r = waiting("biz_other/prices", `{"plan":"plan_other","price":"15"}`, "hWhopSellerSetPrice", `UPDATE whop_stores SET left_at = ?, left_why = 'uninstalled' WHERE store_id = 'biz_other'`)
	if r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "left") || len(f.priceSets) != 0 {
		t.Fatalf("pricing a store that left while it waited: %d %v, prices set %v", r.status, r.body, f.priceSets)
	}
}

// Only the business's team prices its store and opens it, through Whop's
// proxy with the app's token, from the page itself, each call by its own
// method, and nothing else under the store's address answers.
func TestOnlyTheBusinesssTeamPricesAndOpensItsStore(t *testing.T) {
	f, e, owner := openedAsSeller(t)
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	buyer := f.sellerToken(e, whopTestApp, "user_alex")
	for _, call := range []struct{ method, path, body string }{
		{"GET", "biz_other/prices", ""},
		{"POST", "biz_other/prices", `{"plan":"plan_other","price":"15"}`},
		{"POST", "biz_other/sell", `{}`},
	} {
		for name, c := range map[string]struct {
			path, token string
			hdr         map[string]string
			status      int
			code        string
		}{
			"no token":          {call.path, "", nil, http.StatusUnauthorized, "whop_token"},
			"a buyer":           {call.path, buyer, nil, http.StatusForbidden, "whop_not_team"},
			"another business":  {strings.Replace(call.path, "biz_other", "biz_pip", 1), owner, nil, http.StatusForbidden, "whop_not_team"},
			"not from the page": {call.path, owner, map[string]string{"X-Requested-With": ""}, http.StatusForbidden, "forbidden"},
			"from another site": {call.path, owner, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, "forbidden"},
		} {
			if r := e.asSeller(t, call.method, c.path, call.body, c.token, c.hdr); r.status != c.status || r.body["code"] != c.code {
				t.Errorf("%s %s, %s: %d %v", call.method, call.path, name, r.status, r.body)
			}
		}
	}
	if len(f.priceSets) != 0 || len(f.shareWrites) != 0 {
		t.Fatalf("a refused call reached Whop: prices %v, shares %v", f.priceSets, f.shareWrites)
	}
	for _, c := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{"GET", "biz_other/sell", http.StatusMethodNotAllowed, "POST"},
		{"PUT", "biz_other/prices", http.StatusMethodNotAllowed, "GET, POST"},
		{"GET", "biz_other/more", http.StatusNotFound, ""},
		{"GET", "biz_other/prices/plan_other", http.StatusNotFound, ""},
	} {
		if r := e.asSeller(t, c.method, c.path, "", owner, nil); r.status != c.status || r.header.Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q", c.method, c.path, r.status, r.header.Get("Allow"))
		}
	}
}

// Once the store is open, the seller's prices offer Update the store
// instead of Open the store: the same call, by the same rules, which sets
// Playkeeper's share on a hosting product the seller added since, marks it
// for the store site and shows its plan, as a copy's plans need. The audit
// log says it was an update. One refused for a plan that breaks the rules
// writes nothing to Whop and leaves the store open, since only the store's
// pass closes an open store. A suspended store is offered neither.
func TestUpdateTheStorePutsAPlanAddedAfterOpeningOnTheStoreSite(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	offered := func(name string, open, update bool) {
		t.Helper()
		r := e.asSeller(t, "GET", "biz_other/prices", "", token, nil)
		if r.body["canOpen"] != open || r.body["canUpdate"] != update {
			t.Fatalf("%s offers Open the store %v and Update the store %v", name, r.body["canOpen"], r.body["canUpdate"])
		}
	}
	sell := func() resp {
		t.Helper()
		return e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil)
	}
	offered("a store not open yet", true, false)
	if r := sell(); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store: %d %v", r.status, r.body)
	}
	offered("an open store", false, true)
	f.setOtherProduct("prod_big", whop.Metadata{})
	f.addOtherPlan(map[string]any{"id": "plan_big", "title": "Big", "visibility": "hidden", "renewal_price": 24,
		"product": map[string]any{"id": "prod_big", "title": "Big server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "8"}})
	f.addOtherPlan(map[string]any{"id": "plan_cheap", "title": "Cheap", "visibility": "hidden", "renewal_price": 9,
		"product": map[string]any{"id": "prod_cheap", "title": "Cheap server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	if r := sell(); r.status != http.StatusConflict || r.body["error"] != "Cheap: charges $9.00, under the $12.00 floor for 4 GB" ||
		r.body["hint"] != "To update your store, charge at least $12.00 a month for each 4 GB." {
		t.Fatalf("Update the store with a plan under the floor: %d %v", r.status, r.body)
	}
	if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != "" {
		t.Fatalf("a refused update closed the store: %q", st.ClosedWhy)
	}
	if vis := f.otherPlanVisibility("plan_big"); vis != "hidden" || f.share("biz_other", "prod_big") != nil {
		t.Fatalf("a refused update reached Whop: Big %v, its share %v", vis, f.share("biz_other", "prod_big"))
	}
	f.setOtherPlan("plan_cheap", map[string]any{"visibility": "archived"})
	if r := sell(); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Update the store: %d %v", r.status, r.body)
	}
	if share := f.share("biz_other", "prod_big"); share == nil || share["commission_value"] != 70.84 {
		t.Fatalf("Playkeeper's share on Big's product: %v", share)
	}
	if meta := f.otherProduct("prod_big"); meta[whop.MetaDashboard] != whopDashboard || meta[whop.MetaBusiness] != "biz_other" {
		t.Fatalf("Big's product: %v", meta)
	}
	if vis := f.otherPlanVisibility("plan_big"); vis != "visible" {
		t.Fatalf("Big shows as %v on Whop", vis)
	}
	if rows := e.auditRows(t, "whop.store_sell"); len(rows) != 2 || !strings.HasPrefix(rows[0], "whop:user_otherowner biz_other succeeded Open the store: ") ||
		!strings.HasPrefix(rows[1], "whop:user_otherowner biz_other succeeded Update the store: ") {
		t.Fatalf("the audit log: %v", rows)
	}
	if _, err := e.srv.suspendWhopStore(t.Context(), "biz_other", "admin", "griefing"); err != nil {
		t.Fatal(err)
	}
	offered("a suspended store", false, false)
}

// Lowering a price on an open store sets the share the new price needs
// first, with force, and only then the price, so no payment at the lower
// price pays less than Playkeeper's share: with Whop refusing the price,
// the share is raised already, until the store's next read, which comes at
// once, finds the old price and puts it back. A share Whop won't set, or a
// problem with the shares, refuses the change, and the price stays.
// Raising a price sets it first, and the share follows after, since a
// share above what a price needs still pays Playkeeper's.
func TestLoweringAPriceSetsTheShareFirst(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	if r := e.asSeller(t, "POST", "biz_other/sell", `{}`, token, nil); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store: %d %v", r.status, r.body)
	}
	set := func(price string) resp {
		t.Helper()
		return e.asSeller(t, "POST", "biz_other/prices", `{"plan":"plan_other","price":"`+price+`"}`, token, nil)
	}
	share := func() any {
		t.Helper()
		return f.share("biz_other", "prod_other")["commission_value"]
	}
	f.priceDown = true
	if r := set("20"); r.status != http.StatusBadGateway || share() != 70.84 {
		t.Fatalf("a higher price Whop refuses: %d %v, the share %v", r.status, r.body, share())
	}
	f.priceDown = false
	if r := set("20"); r.status != http.StatusOK || share() != 42.5 {
		t.Fatalf("raising Other to $20: %d %v, the share %v", r.status, r.body, share())
	}
	f.priceDown = true
	if r := set("15"); r.status != http.StatusBadGateway || share() != 56.67 {
		t.Fatalf("a lower price Whop refuses: %d %v, the share %v", r.status, r.body, share())
	}
	f.priceDown = false
	e.reconcile()
	if share() != 42.5 {
		t.Fatalf("the share once the store's next read finds Other at $20 still: %v", share())
	}
	f.shareDown = true
	if r := set("15"); r.status != http.StatusBadGateway || share() != 42.5 || len(f.priceSets) != 1 {
		t.Fatalf("a lower price whose share Whop won't set: %d %v, the share %v, prices set %v", r.status, r.body, share(), f.priceSets)
	}
	f.shareDown = false
	f.addOtherPlan(map[string]any{"id": "plan_year", "title": "Yearly", "billing_period": 365, "renewal_price": 120,
		"product": map[string]any{"id": "prod_year", "title": "Yearly server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	if r := set("15"); r.status != http.StatusConflict || !strings.Contains(fmt.Sprint(r.body["error"]), "Yearly: It doesn't renew every month") || len(f.priceSets) != 1 {
		t.Fatalf("a lower price while a plan breaks the rules: %d %v, prices set %v", r.status, r.body, f.priceSets)
	}
	e.reconcile()
	if share() != 42.5 {
		t.Fatalf("the share once the store's next read finds Other at $20 still, after a refused lower price: %v", share())
	}
	f.setOtherPlan("plan_year", map[string]any{"visibility": "archived"})
	if r := set("15"); r.status != http.StatusOK || share() != 56.67 || !slices.Equal(f.priceSets, []string{"plan_other=20", "plan_other=15"}) {
		t.Fatalf("lowering Other to $15: %d %v, the share %v, prices set %v", r.status, r.body, share(), f.priceSets)
	}
}
