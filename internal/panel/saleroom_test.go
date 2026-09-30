package panel

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// kicked reports whether the plans' room was asked to be worked out again
// since the last look.
func (e *env) kicked() bool {
	select {
	case <-e.srv.saleRoomKick:
		return true
	default:
		return false
	}
}

// Room goes out one server at a time, one of each plan in turn, paid plans
// first and the smallest first, then the free ones, each on the fullest
// machine that has room for it, after the customers waiting for room.
func TestRoomGoesToEachPlanInTurnPaidFirstAndSmallestFirst(t *testing.T) {
	plans := []SalePlan{
		{CustomerPlan: CustomerPlan{ID: "plan_creator", MemoryMB: 6144}, Free: true},
		{CustomerPlan: CustomerPlan{ID: "plan_big", MemoryMB: 8192}},
		{CustomerPlan: CustomerPlan{ID: "plan_starter", MemoryMB: 4096}},
		{CustomerPlan: CustomerPlan{ID: "plan_plus", MemoryMB: 6144}},
		{CustomerPlan: CustomerPlan{ID: "plan_none"}},
	}
	room := func(id string, freeMB int, takes bool) machineRoom {
		return machineRoom{machine: machine{ID: id, Kind: remoteKind}, FreeMB: freeMB, Takes: takes}
	}
	left := func(starter, plus, big, creator int) map[string]int {
		return map[string]int{"plan_starter": starter, "plan_plus": plus, "plan_big": big, "plan_creator": creator, "plan_none": 0}
	}
	for _, c := range []struct {
		name    string
		rooms   []machineRoom
		waiting []int
		want    map[string]int
	}{
		{"an empty machine's 28 GB", []machineRoom{room("a", 28672, true)}, nil, left(2, 1, 1, 1)},
		{"12 GB free", []machineRoom{room("a", 12288, true)}, nil, left(1, 1, 0, 0)},
		{"a machine that takes no customers", []machineRoom{room("a", 28672, false)}, nil, left(0, 0, 0, 0)},
		{"a customer waiting for room gets theirs first", []machineRoom{room("a", 12288, true)}, []int{8192}, left(1, 0, 0, 0)},
		{"each on the fullest machine it fits", []machineRoom{room("a", 10240, true), room("b", 6144, true)}, nil, left(2, 1, 0, 0)},
	} {
		if got := roomForPlans(c.rooms, c.waiting, plans); !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// With more than one store selling, each store's plans get room as if the
// store sold alone, so any mix of one store's numbers fits at once, until
// what they're offered together reaches the store's share: the room the
// customers waiting leave, divided evenly among the stores. Each plan is
// offered once while the room fits it, whatever its store's share.
func TestEachStoreGetsAnEvenShareOfTheRoom(t *testing.T) {
	plan := func(store, id string, mb int) SalePlan {
		return SalePlan{CustomerPlan: CustomerPlan{ID: id, MemoryMB: mb}, Store: store}
	}
	room := func(id string, freeMB int, takes bool) machineRoom {
		return machineRoom{machine: machine{ID: id, Kind: remoteKind}, FreeMB: freeMB, Takes: takes}
	}
	pip := []SalePlan{plan("biz_pip", "pip_starter", 4096), plan("biz_pip", "pip_big", 8192)}
	other := []SalePlan{plan("biz_other", "other_starter", 4096)}
	var many []SalePlan
	for _, s := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		many = append(many, plan("biz_"+s, s+"_starter", 4096))
	}
	for _, c := range []struct {
		name    string
		rooms   []machineRoom
		waiting []int
		plans   []SalePlan
		want    map[string]int
	}{
		{"one store has all the room, as before", []machineRoom{room("a", 28672, true)}, nil, pip,
			map[string]int{"pip_starter": 3, "pip_big": 2}},
		{"two stores share an empty machine's 28 GB", []machineRoom{room("a", 28672, true)}, nil, slices.Concat(pip, other),
			map[string]int{"pip_starter": 2, "pip_big": 1, "other_starter": 4}},
		{"each store's plan once while the room fits it, among many stores", []machineRoom{room("a", 28672, true)}, nil, many,
			map[string]int{"a_starter": 1, "b_starter": 1, "c_starter": 1, "d_starter": 1, "e_starter": 1, "f_starter": 1, "g_starter": 1, "h_starter": 1}},
		{"each of a store's plans once while the room fits it, however small its share", []machineRoom{room("a", 28672, true)}, nil, slices.Concat(pip, many[:7]),
			map[string]int{"pip_starter": 1, "pip_big": 1, "a_starter": 1, "b_starter": 1, "c_starter": 1, "d_starter": 1, "e_starter": 1, "f_starter": 1, "g_starter": 1}},
		{"any mix of one store's numbers fits at once", []machineRoom{room("a", 10240, true)}, nil, slices.Concat(pip, many[:3]),
			map[string]int{"pip_starter": 1, "pip_big": 0, "a_starter": 1, "b_starter": 1, "c_starter": 1}},
		{"the share is of what the customers waiting leave", []machineRoom{room("a", 28672, true)}, []int{12288}, slices.Concat(pip, other),
			map[string]int{"pip_starter": 1, "pip_big": 1, "other_starter": 2}},
		{"a machine that takes no customers is no one's room", []machineRoom{room("a", 28672, false), room("b", 8192, true)}, nil, slices.Concat(pip, other),
			map[string]int{"pip_starter": 1, "pip_big": 0, "other_starter": 1}},
	} {
		if got := roomForPlans(c.rooms, c.waiting, c.plans); !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Each plan's stock on Whop is how many more of it the machines can take,
// after the customers waiting for room, and follows the room as it changes.
func TestEachPlansStockFollowsTheMachinesRoom(t *testing.T) {
	f, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	sync := func(freeMB int) {
		t.Helper()
		e.reply("GET", "/v1/machine", liveMachine(freeMB, true))
		e.srv.syncSaleRoom(ctx)
		e.reconcile()
	}
	stock := func() [2]int { return [2]int{f.stockOf("plan_starter"), f.stockOf("plan_big")} }

	sync(20480)
	if got := stock(); got != [2]int{3, 1} {
		t.Fatalf("with 20 GB free, Starter and Big at %v", got)
	}

	// alex buys Starter with no room: they wait, and their room goes to them
	// before any plan's once there's some.
	e.kicked()
	e.reply("GET", "/v1/machine", liveMachine(2048, true))
	if _, err := (customerCore{s: e.srv}).StartCustomer(ctx, Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	if !e.kicked() {
		t.Fatal("a customer set waiting didn't have the room worked out again")
	}
	sync(12288)
	if got := stock(); got != [2]int{2, 0} {
		t.Fatalf("with 12 GB free and alex waiting, Starter and Big at %v", got)
	}

	// Placed, alex's room is set aside on the machine.
	if _, err := e.srv.placeCustomer(ctx, mustCustomer(t, e, "user_alex"), starter); err != nil {
		t.Fatal(err)
	}
	if !e.kicked() {
		t.Fatal("placing a customer didn't have the room worked out again")
	}
	sync(12288)
	if got := stock(); got != [2]int{2, 0} {
		t.Fatalf("with 12 GB free and alex placed, Starter and Big at %v", got)
	}

	// alex's plan grows to Big, whose memory is set aside instead.
	big := CustomerPlan{ID: "plan_big", Name: "Big", Servers: 2, MemoryMB: 8192}
	if _, err := (customerCore{s: e.srv}).StartCustomer(ctx, Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, big); err != nil {
		t.Fatal(err)
	}
	if !e.kicked() {
		t.Fatal("a customer's plan changing didn't have the room worked out again")
	}
	sync(12288)
	if got := stock(); got != [2]int{1, 0} {
		t.Fatalf("with 12 GB free and alex on Big, Starter and Big at %v", got)
	}
}

// Two stores selling on the same machines each get an even share of their
// room as their plans' stock on Whop, so neither's plans take the room the
// other's could sell.
func TestStoresShareTheMachinesRoomEvenly(t *testing.T) {
	f, e, _ := twoStores(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	e.reconcile()
	e.clock.add(2 * whopPollEvery)
	e.reply("GET", "/v1/machine", liveMachine(28672, true))
	e.srv.syncSaleRoom(ctx)
	e.reconcile()
	written := map[string]string{}
	for _, w := range f.stockWrites() {
		plan, n, _ := strings.Cut(w, "=")
		written[plan] = n
	}
	if want := map[string]string{"plan_starter": "2", "plan_big": "1", "plan_other": "4"}; !maps.Equal(written, want) {
		t.Fatalf("with 28 GB free, the stock written: %v, want %v", written, want)
	}
}

// mustCustomer is the account of the customer with Whop user subject.
func mustCustomer(t *testing.T, e *env, subject string) int64 {
	t.Helper()
	info, ok, err := (customerCore{s: e.srv}).CustomerAccount(context.Background(), whopProvider, testStore, subject)
	if err != nil || !ok {
		t.Fatalf("no account for %s: %v", subject, err)
	}
	return info.UserID
}

// The room is worked out again when it changes: a server made, resized or
// deleted, a plan's allowance changed, a machine confirmed or removed.
func TestTheRoomIsWorkedOutAgainWhenItChanges(t *testing.T) {
	_, e, own := connectedWhop(t)
	local := e.localMachine(t)
	kim := addCreator(t, e, "kim", fourGB)
	e.reply("POST", "/v1/servers", `{"id":"0123456789abcdef","serverId":"cafebabe23","kind":"create","status":"running"}`)
	e.reply("POST", "/v1/servers/"+sampleServer+"/settings", `{}`)
	e.reply("POST", "/v1/servers/"+sampleServer+"/delete", `{"id":"0123456789abcdef","kind":"delete","status":"running"}`)
	for _, c := range []struct {
		what, method, path, body string
	}{
		{"a server made", "POST", "/api/machines/" + local + "/servers", `{"name":"big","acceptEula":true,"memoryMB":8192}`},
		{"a server resized", "POST", "/api/servers/" + sampleServer + "/settings", `{"memoryMB":6144}`},
		{"a server deleted", "POST", "/api/servers/" + sampleServer + "/delete", `{}`},
		{"a plan's allowance changed", "PUT", "/api/whop/plans/plan_big", `{"servers":2,"memoryMB":6144}`},
		{"a creator removed", "DELETE", fmt.Sprintf("/api/team/members/%d", kim.id), ""},
	} {
		e.kicked()
		if r := e.do(t, c.method, c.path, c.body, own.auth()); r.status >= 300 {
			t.Fatalf("%s: %d %v", c.what, r.status, r.body)
		}
		if !e.kicked() {
			t.Errorf("%s didn't have the room worked out again", c.what)
		}
	}
}

// A joined machine connecting, confirmed or stopped taking customers, going
// away and being removed each change the room.
func TestAJoinedMachinesCustomersChangeTheRoom(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, _, _, link := joinForCustomers(t, e, own)
	eventually(t, "the machine connecting has the room worked out again", e.kicked)
	for _, c := range []struct {
		what, method, path, body string
	}{
		{"confirming it", "PUT", "/api/machines/" + rid + "/customers", `{"on":true}`},
		{"stopping it", "PUT", "/api/machines/" + rid + "/customers", `{"on":false}`},
	} {
		e.kicked()
		if r := e.do(t, c.method, c.path, c.body, own.auth()); r.status >= 300 {
			t.Fatalf("%s: %d %v", c.what, r.status, r.body)
		}
		if !e.kicked() {
			t.Errorf("%s didn't have the room worked out again", c.what)
		}
	}
	e.kicked()
	link.stop()
	eventually(t, "the machine going away has the room worked out again", e.kicked)
	if r := e.do(t, "DELETE", "/api/machines/"+rid, "", own.auth()); r.status >= 300 {
		t.Fatalf("removing it: %d %v", r.status, r.body)
	}
	eventually(t, "removing the machine has the room worked out again", e.kicked)
}

// A creator joining with an invite has their allowance set aside on the
// dashboard's machine, which changes the room.
func TestACreatorJoiningChangesTheRoom(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	r := e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth())
	code, _ := strings.CutPrefix(r.body["path"].(string), invites.JoinPath+"/")
	if r.status != http.StatusCreated {
		t.Fatalf("the owner's creator invite: %d %v", r.status, r.body)
	}
	e.kicked()
	if r := e.public(t, "accept", codeBody(code, "username", "alex", "password", "member password 1")); r.status != http.StatusOK {
		t.Fatalf("alex joins: %d %v", r.status, r.body)
	}
	if !e.kicked() {
		t.Fatal("a creator joining didn't have the room worked out again")
	}
}

// Settings › Machines shows the owner how many more of each plan fit and
// what each machine can still set aside for customers. Nobody else sees it.
// With two stores selling, the owner's room for customers says whose each
// plan is.
func TestTheRoomForCustomersNamesEachPlansStore(t *testing.T) {
	_, e, own := twoStores(t)
	e.reply("GET", "/v1/machine", liveMachine(28672, true))
	e.reply("GET", "/v1/servers", `[]`)
	e.reconcile()
	var v saleRoomView
	if st := e.get(t, "/api/machines/room", own.cookie, &v); st != http.StatusOK {
		t.Fatalf("the owner's room for customers: %d", st)
	}
	stores := map[string]string{}
	for _, p := range v.Plans {
		stores[p.ID] = p.Store + " " + p.StoreName
	}
	if want := map[string]string{"plan_starter": "biz_pip Pip Hosting", "plan_big": "biz_pip Pip Hosting", "plan_other": "biz_other Other Hosting"}; !maps.Equal(stores, want) {
		t.Fatalf("the plans' stores: %v, want %v", stores, want)
	}
}

func TestOnlyTheOwnerSeesTheRoomForCustomers(t *testing.T) {
	_, e, own := connectedWhop(t)
	e.reply("GET", "/v1/machine", liveMachine(20480, true))
	e.reply("GET", "/v1/servers", `[]`)
	var v saleRoomView
	if st := e.get(t, "/api/machines/room", own.cookie, &v); st != http.StatusOK {
		t.Fatalf("the owner's room for customers: %d", st)
	}
	left := map[string]int{}
	for _, p := range v.Plans {
		left[p.ID] = p.Left
	}
	if !maps.Equal(left, map[string]int{"plan_starter": 3, "plan_big": 1}) || len(v.Machines) != 1 || v.Machines[0].FreeMB != 20480 || !v.Machines[0].Takes {
		t.Fatalf("the owner's room for customers: %+v", v)
	}
	lena := addAdmin(t, e, "lena", "*")
	if r := e.do(t, "GET", "/api/machines/room", "", lena.auth()); r.status != http.StatusForbidden {
		t.Fatalf("an admin of every server looks at the room for customers: %d %v", r.status, r.body)
	}
	if _, err := (customerCore{s: e.srv}).StartCustomer(context.Background(), Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	alex := signIn(t, e, mustCustomer(t, e, "user_alex"))
	if r := e.do(t, "GET", "/api/machines/room", "", alex.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a customer looks at the room for customers: %d %v", r.status, r.body)
	}
}
