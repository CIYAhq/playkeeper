package panel

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// availability has e's fleet say, a second later, how many more of each
// plan fit, and the reconciler look.
func (e *env) availability(t *testing.T, left map[string]int) {
	t.Helper()
	e.clock.add(time.Second)
	if err := e.srv.sales.SetAvailability(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
}

func TestTheFleetSeesThePlansTheStoreSells(t *testing.T) {
	_, e, _ := connectedWhop(t)
	plans, err := e.srv.sales.SalePlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range plans {
		got = append(got, planText(p))
	}
	if want := []string{"plan_starter (Starter) 1/4096/0", "plan_big (Big) 2/8192/0"}; !slices.Equal(got, want) {
		t.Fatalf("plans on sale: %q", got)
	}
}

func TestTheFleetsNumbersBecomeEachPlansStockOnWhop(t *testing.T) {
	f, e, own := connectedWhop(t)
	if err := e.srv.sales.SetAvailability(context.Background(), map[string]int{"plan_starter": 2, "plan_big": 1, "plan_old": 5, "plan_nope": 3}); err != nil {
		t.Fatal(err)
	}
	if got := f.stockWrites(); len(got) != 0 {
		t.Fatalf("Whop was asked before the reconciler looked: %q", got)
	}
	e.reconcile()
	if got := f.stockWrites(); !slices.Equal(got, []string{"plan_starter=2", "plan_big=1"}) {
		t.Fatalf("stock set on Whop: %q", got)
	}
	// Nothing new, nothing written; a plan left out stays as it is, and a
	// number below zero is none.
	e.reconcile()
	e.availability(t, map[string]int{"plan_starter": -4})
	if got := f.stockWrites(); !slices.Equal(got, []string{"plan_starter=2", "plan_big=1", "plan_starter=0"}) || f.stockOf("plan_big") != 1 {
		t.Fatalf("stock set on Whop: %q, Big at %d", got, f.stockOf("plan_big"))
	}
	if r := e.do(t, "DELETE", "/api/whop", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnect: %d %v", r.status, r.body)
	}
	var n int
	if e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_stock`).Scan(&n); n != 0 {
		t.Fatalf("%d of the fleet's numbers kept after disconnecting", n)
	}
}

func TestAPurchaseCountsOnceUntilTheFleetCountsIt(t *testing.T) {
	f, e, _ := connectedWhop(t)
	core := useFakeCore(e)
	starter := map[string]int{"plan_starter": 2}
	e.availability(t, starter)

	// alex buys one of the two, and the core can't start them yet. Whop took
	// one off, and the fleet saying 2 again doesn't sell alex's room twice.
	core.refuse = errors.New("The machine is full.")
	e.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	e.reconcile()
	if n := f.stockOf("plan_starter"); n != 1 {
		t.Fatalf("after a purchase the core didn't start: Starter at %d", n)
	}
	e.availability(t, starter)
	if n := f.stockOf("plan_starter"); n != 1 {
		t.Fatalf("the fleet's number before alex, again: Starter at %d", n)
	}
	// Room frees up for one more while alex still waits.
	e.availability(t, map[string]int{"plan_starter": 3})
	if n := f.stockOf("plan_starter"); n != 2 {
		t.Fatalf("more room while alex waits: Starter at %d", n)
	}

	// Started, alex counts as a purchase until the fleet's number counts
	// them, and then once.
	core.refuse = nil
	e.clock.add(2 * time.Minute)
	e.reconcile()
	e.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("alex wasn't started: %q", got)
	}
	if n := f.stockOf("plan_starter"); n != 2 {
		t.Fatalf("alex started, not yet in the fleet's number: Starter at %d", n)
	}
	e.availability(t, map[string]int{"plan_starter": 2})
	if n := f.stockOf("plan_starter"); n != 2 {
		t.Fatalf("alex in the fleet's number: Starter at %d", n)
	}
	e.availability(t, map[string]int{"plan_starter": 4})
	if n := f.stockOf("plan_starter"); n != 4 {
		t.Fatalf("after more room freed up: Starter at %d", n)
	}
}

func TestABuyerWhosePlanEndedNeedsRoomAgainWhenTheyBuyAgain(t *testing.T) {
	f, e, _ := connectedWhop(t)
	core := useFakeCore(e)
	e.availability(t, map[string]int{"plan_starter": 2})
	e.deliver(t, "evt_1", whop.EventMembershipActivated, f.buy("mem_alex1", "user_alex", "plan_starter", "active"))
	e.reconcile()
	e.availability(t, map[string]int{"plan_starter": 1})
	e.deliver(t, "evt_2", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "expired"))
	e.reconcile()
	// Past the grace period alex's room is free again, and buying again
	// needs it back, even while the core can't start them.
	e.availability(t, map[string]int{"plan_starter": 2})
	core.refuse = errors.New("The machine is full.")
	e.deliver(t, "evt_3", whop.EventMembershipActivated, f.buy("mem_alex2", "user_alex", "plan_starter", "active"))
	e.reconcile()
	e.availability(t, map[string]int{"plan_starter": 3})
	if n := f.stockOf("plan_starter"); n != 2 {
		t.Fatalf("more room while alex waits to come back: Starter at %d", n)
	}
	if got := core.got(); len(got) != 2 || !strings.HasPrefix(got[1], "pause") {
		t.Fatalf("the core's calls: %q", got)
	}
}

func TestAStockChangedOnWhopIsRaisedOnlyWhenTheFleetHasMoreRoom(t *testing.T) {
	f, e, own := connectedWhop(t)
	readStore := func() {
		t.Helper()
		if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK {
			t.Fatalf("sync: %d %v", r.status, r.body)
		}
		e.reconcile()
	}
	e.availability(t, map[string]int{"plan_starter": 2})

	// Lowered, as a purchase the dashboard hasn't heard of yet does: raising
	// it could sell that room again.
	f.setStock("plan_starter", 0)
	readStore()
	e.availability(t, map[string]int{"plan_starter": 2})
	if n := f.stockOf("plan_starter"); n != 0 {
		t.Fatalf("a lowered stock was raised to the same number: Starter at %d", n)
	}
	e.availability(t, map[string]int{"plan_starter": 3})
	if n := f.stockOf("plan_starter"); n != 3 {
		t.Fatalf("more room didn't raise it: Starter at %d", n)
	}

	// Raised, or made unlimited: put back at once.
	f.setStock("plan_starter", 9)
	readStore()
	if n := f.stockOf("plan_starter"); n != 3 {
		t.Fatalf("a raised stock stayed: Starter at %d", n)
	}
	f.setStock("plan_starter", -1)
	readStore()
	if n := f.stockOf("plan_starter"); n != 3 {
		t.Fatalf("an unlimited stock stayed: Starter at %d", n)
	}
}

func TestAStockWhopDidntTakeIsSetAgain(t *testing.T) {
	f, e, _ := connectedWhop(t)
	down := func(v bool) {
		f.mu.Lock()
		f.stockDown = v
		f.mu.Unlock()
	}
	down(true)
	e.availability(t, map[string]int{"plan_starter": 2})
	if n := f.stockOf("plan_starter"); n != -1 {
		t.Fatalf("while Whop failed: Starter at %d", n)
	}
	down(false)
	e.reconcile()
	if n := f.stockOf("plan_starter"); n != 2 {
		t.Fatalf("setting it again: Starter at %d", n)
	}
	down(true)
	e.availability(t, map[string]int{"plan_starter": 1})
	down(false)
	e.reconcile()
	if n := f.stockOf("plan_starter"); n != 1 {
		t.Fatalf("lowering it again: Starter at %d", n)
	}
	down(true)
	e.availability(t, map[string]int{"plan_starter": 3})
	down(false)
	e.reconcile()
	if n := f.stockOf("plan_starter"); n != 3 {
		t.Fatalf("raising it again: Starter at %d", n)
	}
}
