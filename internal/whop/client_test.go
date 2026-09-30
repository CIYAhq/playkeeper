package whop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

const testKey = "apik_test_0123456789abcdef"

// fake answers like Whop's API for the routes a test gives it, and fails the
// test on a request without the key or the pinned version.
func fake(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("%s %s: Authorization %q", r.Method, r.URL.Path, got)
		}
		if got := r.Header.Get("Api-Version-Date"); got != APIVersion {
			t.Errorf("%s %s: Api-Version-Date %q", r.Method, r.URL.Path, got)
		}
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{APIURL: srv.URL, Key: testKey, UserAgent: "Playkeeper/test"}
}

func answer(v any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestMe(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /accounts/me": answer(map[string]any{"id": "biz_pip", "title": "Pip Hosting", "route": "pip-hosting", "status": "approved"}),
	})
	a, err := c.Me(context.Background())
	if err != nil || a != (Account{ID: "biz_pip", Title: "Pip Hosting", Route: "pip-hosting"}) {
		t.Fatalf("Me = %+v, %v", a, err)
	}
}

func TestErrorsKeepWhopsWordsAndNeverTheKey(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /accounts/me": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"Invalid API key"}}`)
		},
	})
	_, err := c.Me(context.Background())
	if !KeyRefused(err) || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("Me = %v, want a refused key with Whop's message", err)
	}
	var e *Error
	if !reflectAs(err, &e) || e.Type != "authentication_error" {
		t.Fatalf("error = %#v", err)
	}
	// An unreachable API quotes the URL, never the key.
	down := &Client{APIURL: "http://127.0.0.1:1", Key: testKey}
	_, err = down.Me(context.Background())
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("unreachable Whop: %v", err)
	}
}

func reflectAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestMissingListsUngrantedNeedsInOrder(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /permissions": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("resource_id") != "biz_pip" || r.URL.Query().Get("actions") != strings.Join(Needs, ",") {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			var data []map[string]any
			for _, n := range Needs {
				data = append(data, map[string]any{"action": n, "granted": n != "support_chat:create" && n != "access_pass:update"})
			}
			answer(map[string]any{"data": data})(w, r)
		},
	})
	missing, err := c.Missing(context.Background(), "biz_pip")
	if err != nil || !reflect.DeepEqual(missing, []string{"access_pass:update", "support_chat:create"}) {
		t.Fatalf("Missing = %v, %v", missing, err)
	}
}

func TestPlansFollowPagesAndReadNulls(t *testing.T) {
	var calls atomic.Int32
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /variants": func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Query().Get("account_id") != "biz_pip" || r.URL.Query().Get("first") != "100" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("after") == "" {
				_, _ = io.WriteString(w, `{"data":[{"id":"plan_a","title":"Starter","visibility":"hidden","plan_type":"renewal","billing_period":30,
					"trial_period_days":null,"formatted_price":"$8.00 / month","product":{"id":"prod_mc","title":"Minecraft server"},
					"metadata":{"playkeeper_servers":"1","playkeeper_memory_gb":4},"stock":2.0,"unlimited_stock":false}],"page_info":{"end_cursor":"c1","has_next_page":true}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"plan_b","title":null,"visibility":"visible","plan_type":"one_time","billing_period":null,
				"trial_period_days":3,"product":null,"metadata":null,"stock":null,"unlimited_stock":true}],"page_info":{"end_cursor":null,"has_next_page":false}}`)
		},
	})
	plans, err := c.Plans(context.Background(), "biz_pip")
	if err != nil {
		t.Fatal(err)
	}
	want := []Plan{
		{ID: "plan_a", Title: "Starter", Visibility: "hidden", PlanType: "renewal", BillingPeriod: 30, FormattedPrice: "$8.00 / month",
			Product: Ref{ID: "prod_mc", Title: "Minecraft server"}, Metadata: Metadata{MetaServers: "1", MetaMemoryGB: "4"}, Stock: 2},
		{ID: "plan_b", Visibility: "visible", PlanType: "one_time", TrialDays: 3, UnlimitedStock: true},
	}
	if !reflect.DeepEqual(plans, want) || calls.Load() != 2 {
		t.Fatalf("Plans = %+v after %d calls", plans, calls.Load())
	}
}

func TestSetProductMetadataSendsTheWholeMap(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"PATCH /products/prod_mc": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]map[string]string
			want := map[string]string{"color": "green", MetaDashboard: "https://beta.playkeeper.me:8443", MetaBusiness: "biz_pip"}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body["metadata"], want) {
				t.Errorf("body %v, %v", body, err)
			}
			answer(map[string]any{"id": "prod_mc"})(w, r)
		},
	})
	meta, changed := WithSeller(Metadata{"color": "green"}, "https://beta.playkeeper.me:8443", "biz_pip")
	if !changed {
		t.Fatal("adding the address changed nothing")
	}
	if err := c.SetProductMetadata(context.Background(), "prod_mc", meta); err != nil {
		t.Fatal(err)
	}
}

func TestSetPlanStockLimitsThePlan(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"PATCH /variants/plan_a": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]any{"stock": 2.0, "unlimited_stock": false}) {
				t.Errorf("body %v, %v", body, err)
			}
			answer(map[string]any{"id": "plan_a", "stock": 2, "unlimited_stock": false})(w, r)
		},
	})
	if err := c.SetPlanStock(context.Background(), "plan_a", 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(Needs, "plan:update") {
		t.Fatal("Sell on Whop doesn't ask for plan:update")
	}
}

func TestPlanMembershipsAreThePlansAlone(t *testing.T) {
	all := []map[string]any{
		{"id": "mem_1", "status": "active", "plan_id": "plan_a", "user_id": "user_alex"},
		{"id": "mem_2", "status": "active", "plan_id": "plan_b", "user_id": "user_sam"},
	}
	for _, how := range []string{"filters", "ignores", "refuses", "fails"} {
		unfiltered := 0
		c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
			"GET /memberships": func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				data := all
				switch {
				case q.Get("account_id") != "biz_pip":
					t.Errorf("Whop %s: query %s", how, r.URL.RawQuery)
				case !q.Has("plan_id") && !q.Has("plan_ids"):
					unfiltered++
				case q.Get("plan_id") != "plan_a" || q.Get("plan_ids") != "plan_a":
					t.Errorf("Whop %s: query %s", how, r.URL.RawQuery)
				case how == "refuses":
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Unknown parameter: plan_ids"}}`)
					return
				case how == "fails":
					w.WriteHeader(http.StatusInternalServerError)
					io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
					return
				case how == "filters":
					data = all[:1]
				}
				answer(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})(w, r)
			},
		})
		ms, err := c.PlanMemberships(context.Background(), "biz_pip", "plan_a")
		if how == "fails" {
			if err == nil || unfiltered != 0 {
				t.Fatalf("Whop fails: PlanMemberships = %+v, %v, after %d reads of every membership", ms, err, unfiltered)
			}
			continue
		}
		if err != nil || len(ms) != 1 || ms[0].ID != "mem_1" || unfiltered != map[string]int{"refuses": 1}[how] {
			t.Fatalf("Whop %s the filter: PlanMemberships = %+v, %v, after %d reads of every membership", how, ms, err, unfiltered)
		}
	}
}

func TestMessagesGoOutAsTheOwnerWithATokenThatMaySendThemAlone(t *testing.T) {
	var seen []string
	answers := map[string]string{
		"GET /accounts/me":    `{"id":"biz_pip","owner":{"id":"user_pip","username":"pipowner","name":"Pip"}}`,
		"POST /access_tokens": `{"token":"ut_1","expires_at":"2026-09-30T13:00:00Z"}`,
		"POST /messages":      `{"id":"msg_1"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answers[r.Method+" "+r.URL.Path])
	}))
	defer srv.Close()
	c := &Client{APIURL: srv.URL, Key: testKey}
	ctx := context.Background()
	owner, err := c.Owner(ctx)
	if err != nil || owner != (User{ID: "user_pip", Username: "pipowner", Name: "Pip"}) {
		t.Fatalf("Owner = %+v, %v", owner, err)
	}
	token, err := c.UserToken(ctx, "biz_pip", owner.ID, MessageAction)
	if err != nil || token != "ut_1" {
		t.Fatalf("UserToken = %q, %v", token, err)
	}
	if err := c.SendMessage(ctx, token, "feed_1", "Your server is ready"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /accounts/me Bearer " + testKey + " ",
		"POST /access_tokens Bearer " + testKey + ` {"account_id":"biz_pip","scoped_actions":["support_chat:message:create"],"user_id":"user_pip"}`,
		`POST /messages Bearer ut_1 {"channel_id":"feed_1","content":"Your server is ready"}`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
	// A token asked for no actions would get every one the key has.
	if _, err := c.UserToken(ctx, "biz_pip", owner.ID); err == nil || len(seen) != 3 {
		t.Fatalf("a token for every action: %v, after %d requests", err, len(seen))
	}
	answers["GET /accounts/me"], answers["POST /access_tokens"] = `{"id":"biz_pip"}`, `{}`
	if _, err := c.Owner(ctx); err == nil {
		t.Fatal("an account Whop names no owner of")
	}
	if _, err := c.UserToken(ctx, "biz_pip", owner.ID, MessageAction); err == nil {
		t.Fatal("an answer without a token")
	}
}

// An app's key can't read an installed business's account, which Whop
// keeps behind company:balance:read, so the owner comes from one of its
// products, read by id since the list leaves the owner out.
func TestOwnerOfReadsTheOwnerOnOneOfTheAccountsProducts(t *testing.T) {
	balance := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"type":"forbidden","message":"App API key is not authorized for the company:balance:read scope."}}`)
	}
	list := func(ids ...string) func(http.ResponseWriter, *http.Request) {
		var data []map[string]any
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "title": "Minecraft server"})
		}
		return answer(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})
	}
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /accounts/biz_seller": balance,
		"GET /products": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("account_id") {
			case "biz_seller":
				list("prod_4gb", "prod_8gb")(w, r)
			case "biz_noowner":
				list("prod_x")(w, r)
			default:
				list()(w, r)
			}
		},
		"GET /products/prod_4gb": answer(map[string]any{"id": "prod_4gb", "owner_user": map[string]any{"id": "user_seller", "username": "sellerjoe", "name": "Joe"},
			"account": map[string]any{"id": "biz_seller", "title": "Joe's Hosting", "route": "biz_seller"}}),
		"GET /products/prod_x": answer(map[string]any{"id": "prod_x"}),
	})
	u, err := c.OwnerOf(context.Background(), "biz_seller")
	if err != nil || u != (User{ID: "user_seller", Username: "sellerjoe", Name: "Joe"}) {
		t.Fatalf("OwnerOf = %+v, %v", u, err)
	}
	for _, biz := range []string{"biz_noowner", "biz_empty"} {
		if _, err := c.OwnerOf(context.Background(), biz); err == nil {
			t.Fatalf("%s: no owner, but no error", biz)
		}
	}
}

// Which of the actions an account grants the key: an app's key holds only
// what each business that installed the app approved.
func TestLacksListsTheActionsTheAccountDoesntGrant(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /permissions": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("resource_id") != "biz_seller" {
				t.Errorf("asked about %q", r.URL.Query().Get("resource_id"))
			}
			var data []map[string]any
			for _, a := range strings.Split(r.URL.Query().Get("actions"), ",") {
				data = append(data, map[string]any{"action": a, "granted": a != "member:email:read"})
			}
			answer(map[string]any{"data": data})(w, r)
		},
	})
	lacking, err := c.Lacks(context.Background(), "biz_seller", AppNeeds)
	if err != nil || !slices.Equal(lacking, []string{"member:email:read"}) {
		t.Fatalf("Lacks = %v, %v", lacking, err)
	}
	if len(AppNeeds) != 16 || slices.Contains(AppNeeds, "developer:manage_webhook") {
		t.Fatalf("an app store's needs: %v", AppNeeds)
	}
}

// A hosted store's fee is Playkeeper's revenue share: a flat amount of every
// payment on each product, after Whop's fees, with no referral link.
func TestRevenueSharesAreAFlatAmountPerPaymentOnAProduct(t *testing.T) {
	var sent []string
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /affiliates": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			sent = append(sent, string(b))
			answer(map[string]any{"id": "aff_pk", "status": "active"})(w, r)
		},
		"POST /affiliates/aff_pk/overrides": func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			sent = append(sent, string(b))
			answer(map[string]any{"id": "affov_1", "override_type": "rev_share", "product_id": "prod_4gb", "commission_type": "flat_fee",
				"commission_value": 8.5, "revenue_basis": "post_fees", "applies_to_payments": nil, "plan_id": nil})(w, r)
		},
		"GET /affiliates/aff_pk/overrides": answer(map[string]any{"data": []map[string]any{
			{"id": "affov_1", "override_type": "rev_share", "product_id": "prod_4gb", "commission_type": "flat_fee", "commission_value": 8.5, "revenue_basis": "post_fees"},
			{"id": "affov_2", "override_type": "standard", "plan_id": "plan_x", "commission_type": "percentage", "commission_value": 30},
		}, "page_info": map[string]any{"has_next_page": false}}),
	})
	ctx := context.Background()
	partner, err := c.Partner(ctx, "biz_seller", "playkeeper")
	if err != nil || partner != "aff_pk" {
		t.Fatalf("Partner = %q, %v", partner, err)
	}
	share, err := c.AddRevShare(ctx, partner, "prod_4gb", 8.5)
	want := RevShare{ID: "affov_1", ProductID: "prod_4gb", Kind: "flat_fee", Value: 8.5, Basis: "post_fees", Type: "rev_share"}
	if err != nil || share != want {
		t.Fatalf("AddRevShare = %+v, %v", share, err)
	}
	if got := strings.Join(sent, "\n"); got != `{"account_id":"biz_seller","user_identifier":"playkeeper"}`+"\n"+
		`{"commission_type":"flat_fee","commission_value":8.5,"override_type":"rev_share","product_id":"prod_4gb","revenue_basis":"post_fees"}` {
		t.Fatalf("requests:\n%s", got)
	}
	shares, err := c.RevShares(ctx, partner)
	if err != nil || len(shares) != 1 || shares[0] != want {
		t.Fatalf("RevShares = %+v, %v", shares, err)
	}
	for _, bad := range []struct {
		product string
		dollars float64
	}{{"", 8.5}, {"prod_4gb", 0}} {
		if _, err := c.AddRevShare(ctx, partner, bad.product, bad.dollars); err == nil {
			t.Fatalf("a share of %v on %q", bad.dollars, bad.product)
		}
	}
}

func TestPaymentFeesListEveryLine(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /payments/pay_1/fees": answer(map[string]any{"data": []map[string]any{
			{"type": "whop_fee", "origin": "whop_processing_fee", "label": "Whop fee", "amount": map[string]any{"amount": "0.45", "currency": "usd"},
				"settlement_amount": map[string]any{"amount": "0.45", "currency": "usd"}},
			{"type": "affiliate_program_fee", "origin": "affiliate_fee", "label": "Revenue share", "amount": map[string]any{"amount": "8.50", "currency": "usd"},
				"settlement_amount": map[string]any{"amount": "8.50", "currency": "usd"}},
		}}),
	})
	fees, err := c.PaymentFees(context.Background(), "pay_1")
	if err != nil || len(fees) != 2 || fees[1] != (PaymentFee{Type: "affiliate_program_fee", Origin: "affiliate_fee", Label: "Revenue share", Settled: Money{Amount: "8.50", Currency: "usd"}}) {
		t.Fatalf("PaymentFees = %+v, %v", fees, err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer elsewhere.Close()
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /accounts/me": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, elsewhere.URL, http.StatusFound) },
	})
	if _, err := c.Me(context.Background()); err == nil || leaked.Load() {
		t.Fatalf("a redirect was followed (err %v, reached elsewhere %v)", err, leaked.Load())
	}
}

func TestPlanAllowance(t *testing.T) {
	for _, tc := range []struct {
		meta           Metadata
		servers, memMB int
		ok             bool
	}{
		{Metadata{MetaServers: "1", MetaMemoryGB: "4"}, 1, 4096, true},
		{Metadata{MetaServers: " 2 ", MetaMemoryGB: "8.5"}, 2, 8704, true},
		{Metadata{MetaServers: "1"}, 0, 0, false},
		{Metadata{MetaServers: "0", MetaMemoryGB: "4"}, 0, 0, false},
		{Metadata{MetaServers: "one", MetaMemoryGB: "4"}, 0, 0, false},
		{Metadata{MetaServers: "1", MetaMemoryGB: "4.3"}, 0, 0, false},
		{Metadata{MetaServers: "1", MetaMemoryGB: "-4"}, 0, 0, false},
		{Metadata{MetaServers: "1", MetaMemoryGB: "NaN"}, 0, 0, false},
		{nil, 0, 0, false},
	} {
		s, m, ok := PlanAllowance(tc.meta)
		if s != tc.servers || m != tc.memMB || ok != tc.ok {
			t.Errorf("PlanAllowance(%v) = %d, %d, %v", tc.meta, s, m, ok)
		}
	}
}

func TestPlanDiskGB(t *testing.T) {
	for meta, want := range map[string]int{"30": 30, " 60 ": 60, "": 0, "0": 0, "-5": 0, "7.5": 0, "lots": 0, "100001": 0} {
		if got := PlanDiskGB(Metadata{MetaDiskGB: meta}); got != want {
			t.Errorf("PlanDiskGB(%q) = %d, want %d", meta, got, want)
		}
	}
}

func TestWithSeller(t *testing.T) {
	if _, changed := WithSeller(Metadata{MetaDashboard: "https://a", MetaBusiness: "biz_pip"}, "https://a", "biz_pip"); changed {
		t.Error("the same marking counted as a change")
	}
	if out, changed := WithSeller(Metadata{MetaDashboard: "https://a"}, "https://a", "biz_pip"); !changed || out[MetaBusiness] != "biz_pip" {
		t.Errorf("a marking without its business: %v, %v", out, changed)
	}
	out, changed := WithSeller(Metadata{MetaDashboard: "https://a", MetaBusiness: "biz_pip", "x": "1"}, "", "biz_pip")
	if !changed || !reflect.DeepEqual(out, Metadata{"x": "1"}) {
		t.Errorf("removing the marking: %v, %v", out, changed)
	}
	if _, changed := WithSeller(Metadata{"x": "1"}, "", "biz_pip"); changed {
		t.Error("removing a marking that wasn't there counted as a change")
	}
}

func TestSellerIsTheDashboardMarkingAProductOfItsBusiness(t *testing.T) {
	for _, tc := range []struct {
		meta Metadata
		want string
	}{
		{Metadata{}, ""},
		{Metadata{MetaDashboard: "https://a", MetaBusiness: "biz_pip"}, "https://a"},
		{Metadata{MetaDashboard: "https://a"}, "https://a"},
		{Metadata{MetaDashboard: "https://publisher", MetaBusiness: "biz_publisher"}, ""},
		{Metadata{MetaBusiness: "biz_pip"}, ""},
	} {
		if got := Seller(tc.meta, "biz_pip"); got != tc.want {
			t.Errorf("Seller(%v) = %q, want %q", tc.meta, got, tc.want)
		}
	}
}

func TestValidKeyAndEnding(t *testing.T) {
	if !ValidKey(testKey) || ValidKey("short") || ValidKey("has a space in it 0123456789") || ValidKey(strings.Repeat("k", 257)) || ValidKey("tab\tinside0123456789") {
		t.Error("ValidKey")
	}
	if Ending(testKey) != "cdef" || Ending("abc") != "" {
		t.Error("Ending")
	}
}
