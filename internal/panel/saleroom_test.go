package panel

import (
	"context"
	"fmt"
	"maps"
	"net/http"
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
