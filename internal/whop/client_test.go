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
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body["metadata"], map[string]string{"color": "green", MetaDashboard: "https://beta.playkeeper.me:8443"}) {
				t.Errorf("body %v, %v", body, err)
			}
			answer(map[string]any{"id": "prod_mc"})(w, r)
		},
	})
	meta, changed := WithDashboard(Metadata{"color": "green"}, "https://beta.playkeeper.me:8443")
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

func TestWithDashboard(t *testing.T) {
	if _, changed := WithDashboard(Metadata{MetaDashboard: "https://a"}, "https://a"); changed {
		t.Error("the same address counted as a change")
	}
	out, changed := WithDashboard(Metadata{MetaDashboard: "https://a", "x": "1"}, "")
	if !changed || !reflect.DeepEqual(out, Metadata{"x": "1"}) {
		t.Errorf("removing the address: %v, %v", out, changed)
	}
	if _, changed := WithDashboard(Metadata{"x": "1"}, ""); changed {
		t.Error("removing an address that wasn't there counted as a change")
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
