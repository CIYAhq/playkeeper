package panel

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// termsAccepted is each acceptance of the seller terms the dashboard keeps,
// as "store version user when", in the order they came.
func (e *env) termsAccepted(t *testing.T) []string {
	t.Helper()
	rows, err := e.srv.db.Query(`SELECT store_id, version, whop_user, accepted_at FROM whop_terms_accepted ORDER BY accepted_at, whop_user`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var store, version, user string
		var at int64
		if err := rows.Scan(&store, &version, &user, &at); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %s %s %d", store, version, user, at))
	}
	return out
}

// Open the store needs the box "I accept the seller terms" ticked: without
// it, nothing reaches Whop and the store stays closed. With it, the
// dashboard keeps who accepted, when and which version, with an audit
// entry, and a person's first acceptance of a version is the one kept.
// Update the store, on an open store, doesn't ask again; opening it again
// once it's closed does.
func TestOpenTheStoreNeedsTheSellerTermsAccepted(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	sell := func(body string) resp {
		t.Helper()
		return e.asSeller(t, "POST", "biz_other/sell", body, token, nil)
	}
	for _, body := range []string{`{}`, `{"acceptTerms":false}`} {
		if r := sell(body); r.status != http.StatusBadRequest || r.body["error"] != "Tick the box to accept the seller terms first." {
			t.Fatalf("Open the store with %s: %d %v", body, r.status, r.body)
		}
	}
	if st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other"); st.ClosedWhy != whopNotOpenYetWhy || len(f.shareWrites) != 0 || len(e.termsAccepted(t)) != 0 {
		t.Fatalf("Open the store without the terms: closed %q, shares set %v, accepted %v", st.ClosedWhy, f.shareWrites, e.termsAccepted(t))
	}
	first := e.clock.now().UnixMilli()
	if r := sell(acceptingTerms); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store with the terms accepted: %d %v", r.status, r.body)
	}
	want := []string{fmt.Sprintf("biz_other %s user_otherowner %d", sellerTermsVersion, first)}
	if got := e.termsAccepted(t); !slices.Equal(got, want) {
		t.Fatalf("the terms accepted: %v, want %v", got, want)
	}
	if rows := e.auditRows(t, "whop.terms_accept"); !slices.Equal(rows, []string{"whop:user_otherowner biz_other succeeded the seller terms of " + sellerTermsVersion}) {
		t.Fatalf("the audit log: %v", rows)
	}
	if r := sell(`{}`); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Update the store: %d %v", r.status, r.body)
	}
	if err := e.srv.closeWhopStore(t.Context(), "biz_other", whopNotOpenYet, whopNotOpenYetWhy); err != nil {
		t.Fatal(err)
	}
	e.clock.add(time.Hour)
	token = f.sellerToken(e, whopTestApp, "user_otherowner")
	if r := sell(`{}`); r.status != http.StatusBadRequest {
		t.Fatalf("Open the store again without the terms: %d %v", r.status, r.body)
	}
	if r := sell(acceptingTerms); r.status != http.StatusOK || r.body["open"] != true {
		t.Fatalf("Open the store again: %d %v", r.status, r.body)
	}
	if got, rows := e.termsAccepted(t), e.auditRows(t, "whop.terms_accept"); !slices.Equal(got, want) || len(rows) != 1 {
		t.Fatalf("the terms accepted again: %v, audit %v", got, rows)
	}
}

// The version Open the store keeps is the one the seller terms' page on
// playkeeper.io gives, so new terms change both.
func TestTheSellerTermsVersionIsThePages(t *testing.T) {
	page, err := os.ReadFile("../../site/pages/cloud/seller-terms.html")
	if err != nil {
		t.Fatal(err)
	}
	day, err := time.Parse(time.DateOnly, sellerTermsVersion)
	if err != nil {
		t.Fatalf("the seller terms' version %q isn't a date: %v", sellerTermsVersion, err)
	}
	if want := "Version of " + day.Format("2 January 2006"); !strings.Contains(string(page), want) {
		t.Fatalf("the seller terms' page doesn't say %q", want)
	}
}
