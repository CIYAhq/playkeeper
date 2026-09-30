package panel

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// fleetPosts are the fleet alerts of kinds the panel gave its agent to post,
// in order, each as the fields it sent.
func (e *env) fleetPosts(kinds ...string) []map[string]any {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	var out []map[string]any
	for _, r := range e.agent.reqs {
		if r.method == http.MethodPost && r.path == "/v1/discord/notify" && slices.Contains(kinds, fmt.Sprint(r.body["kind"])) {
			out = append(out, r.body)
		}
	}
	return out
}

// posted checks that the fleet alerts of kinds posted so far say want, each
// by its kind and the fields that matter.
func (e *env) posted(t *testing.T, want []map[string]any, kinds ...string) {
	t.Helper()
	got := e.fleetPosts(kinds...)
	if len(got) != len(want) {
		t.Fatalf("%d alerts of %v posted, want %d: %v", len(got), kinds, len(want), got)
	}
	for i, w := range want {
		for k, v := range w {
			if fmt.Sprint(got[i][k]) != fmt.Sprint(v) {
				t.Fatalf("alert %d's %s is %v, want %v: %v", i, k, got[i][k], v, got[i])
			}
		}
		if got[i]["actor"] != placementActor {
			t.Fatalf("alert %d is posted as %v", i, got[i]["actor"])
		}
	}
}

// A joined machine off the dashboard for 5 minutes is posted once, and so is
// its coming back, with how long it was off.
func TestAMachineOffTheDashboardForFiveMinutesIsPostedAndSoIsItsReturn(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/machine", `{"hostname":"my-vps","agentVersion":"0.4.0"}`)
	e.reply("GET", "/v1/servers", `[]`)
	ra := newRemoteAgent()
	d, id, link := e.joinedAs(t, cookie, csrf, ra)
	ctx := context.Background()
	kinds := []string{"machine_off", "machine_back"}
	e.srv.watchFleet(ctx)
	// Off for a minute and back is neither.
	_ = link.stop()
	eventually(t, "home-server is away", func() bool { return !e.srv.hub.Connected(d.MachineID) })
	e.srv.watchFleet(ctx)
	e.clock.add(time.Minute)
	link = e.runLink(t, d, id, ra)
	eventually(t, "home-server is connected again", func() bool { return e.srv.hub.Connected(d.MachineID) })
	e.srv.watchFleet(ctx)
	e.posted(t, nil, kinds...)
	_ = link.stop()
	eventually(t, "home-server is away", func() bool { return !e.srv.hub.Connected(d.MachineID) })
	e.srv.watchFleet(ctx)
	e.clock.add(4 * time.Minute)
	e.srv.watchFleet(ctx)
	e.posted(t, nil, kinds...)
	e.clock.add(time.Minute)
	e.srv.watchFleet(ctx)
	e.clock.add(time.Minute)
	e.srv.watchFleet(ctx)
	e.posted(t, []map[string]any{{"kind": "machine_off", "machine": "home-server", "minutes": 5}}, kinds...)
	e.runLink(t, d, id, ra)
	eventually(t, "home-server is connected again", func() bool { return e.srv.hub.Connected(d.MachineID) })
	e.clock.add(time.Minute)
	e.srv.watchFleet(ctx)
	e.srv.watchFleet(ctx)
	e.posted(t, []map[string]any{{"kind": "machine_off", "minutes": 5}, {"kind": "machine_back", "machine": "home-server", "minutes": 7}}, kinds...)
}

// Room for fewer than 2 of the smallest paid plan's servers across every
// store is posted once, with the customers waiting for room, and again only
// after there was room for 2. A free plan, however small, isn't what sells.
func TestRoomForFewerThanTwoStartersIsPostedOnceUntilThereIsRoomAgain(t *testing.T) {
	f, e, own := twoStores(t)
	f.mu.Lock()
	f.plans = append(f.plans, map[string]any{"id": "plan_creator", "title": "Creator", "visibility": "hidden", "plan_type": "one_time", "initial_price": 0,
		"product": map[string]any{"id": "prod_mc", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "2"}})
	f.mu.Unlock()
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("reading the store: %d %v", r.status, r.body)
	}
	e.reconcile()
	plans, err := e.srv.sales.SalePlans(ctx)
	if err != nil || !slices.ContainsFunc(plans, func(p SalePlan) bool { return p.ID == "plan_creator" && p.Free && p.MemoryMB == 2048 }) {
		t.Fatalf("the free 2 GB plan isn't on sale: %+v %v", plans, err)
	}
	watch := func(freeMB int) {
		t.Helper()
		e.reply("GET", "/v1/machine", liveMachine(freeMB, true))
		e.srv.watchFleet(ctx)
	}
	watch(12288)
	e.posted(t, nil, "low_room")
	watch(6144)
	watch(6144)
	e.posted(t, []map[string]any{{"kind": "low_room", "room": 1, "memoryMB": 4096, "waiting": nil}}, "low_room")
	watch(8192)
	e.reply("GET", "/v1/machine", liveMachine(2048, true))
	if _, err := (customerCore{s: e.srv}).StartCustomer(ctx, Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	watch(2048)
	e.posted(t, []map[string]any{{"room": 1}, {"kind": "low_room", "room": nil, "memoryMB": 4096, "waiting": 1}}, "low_room")
}

// A machine's disk past 75% full is posted once, and again only after it
// was below 70%.
func TestADiskFillingPastThreeQuartersIsPostedOnce(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	watch := func(freeGB int) {
		t.Helper()
		e.reply("GET", "/v1/machine", fmt.Sprintf(`{"hostname":"siya","memoryTotalMB":32000,"memoryFreeMB":20480,"diskFreeBytes":%d,"diskTotalBytes":%d,"guard":{"on":true,"host":true}}`, int64(freeGB)<<30, int64(100)<<30))
		e.srv.watchFleet(ctx)
	}
	watch(30)
	watch(26)
	e.posted(t, nil, "disk_filling")
	watch(20)
	watch(21)
	watch(28)
	e.posted(t, []map[string]any{{"kind": "disk_filling", "machine": "the dashboard's machine", "percent": 80}}, "disk_filling")
	watch(31)
	watch(22)
	e.posted(t, []map[string]any{{"percent": 80}, {"kind": "disk_filling", "percent": 78}}, "disk_filling")
}

// A machine whose busiest hour used more than 70% of its CPU three days
// running is posted when the third such hour ends. A day it didn't breaks
// the run.
func TestAMachineBusyAtItsPeakThreeDaysRunningIsPosted(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	sample := func(cpu int) {
		t.Helper()
		e.reply("GET", "/v1/machine", fmt.Sprintf(`{"hostname":"siya","cpuPercent":%d,"memoryTotalMB":32000,"memoryFreeMB":20480,"guard":{"on":true,"host":true}}`, cpu))
		e.srv.watchFleet(ctx)
	}
	// The clock starts at noon: each day's evening, the busy hour then
	// the next, which ends it, back to the next noon.
	day := func(peak int) {
		t.Helper()
		e.clock.add(6 * time.Hour)
		sample(peak)
		sample(peak)
		e.clock.add(time.Hour)
		sample(20)
		e.clock.add(17 * time.Hour)
		sample(20)
	}
	day(85)
	day(60)
	day(80)
	day(75)
	e.posted(t, nil, "busy_cpu")
	day(90)
	e.posted(t, []map[string]any{{"kind": "busy_cpu", "machine": "the dashboard's machine", "percent": 90, "days": 3}}, "busy_cpu")
	day(90)
	e.posted(t, []map[string]any{{"percent": 90}}, "busy_cpu")
}

// A server whose ticks took over 50 ms for 10 minutes while players were on
// is posted once a day; one with nobody on isn't.
func TestAServerLaggingTenMinutesWithPlayersOnIsPosted(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/machine", liveMachine(20480, true))
	servers := func(players int, mspt float64) {
		t.Helper()
		e.reply("GET", "/v1/servers", fmt.Sprintf(`[{"id":"abcdefghjk","name":"Survival","phase":"online","players":{"online":%d,"max":20,"names":[]},"resources":{"mspt":%g}}]`, players, mspt))
	}
	minutes := func(n int) {
		t.Helper()
		for range n {
			e.srv.watchFleet(ctx)
			e.clock.add(time.Minute)
		}
	}
	servers(0, 80)
	minutes(12)
	servers(12, 40)
	minutes(3)
	servers(12, 63)
	minutes(10)
	e.posted(t, nil, "slow_ticks")
	minutes(1)
	e.posted(t, []map[string]any{{"kind": "slow_ticks", "serverName": "Survival", "machine": "the dashboard's machine", "mspt": 63, "players": 12, "minutes": 10}}, "slow_ticks")
	minutes(30)
	e.posted(t, []map[string]any{{"minutes": 10}}, "slow_ticks")
}

// A joined machine that's connected but doesn't answer holds up no other
// part of the look: each question the watch asks a machine has its time.
func TestAMachineThatDoesntAnswerHoldsUpNoLook(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/machine", fmt.Sprintf(`{"hostname":"my-vps","agentVersion":"0.4.0","diskFreeBytes":%d,"diskTotalBytes":%d}`, int64(20)<<30, int64(100)<<30))
	e.reply("GET", "/v1/servers", `[]`)
	ra := newRemoteAgent()
	e.joinedAs(t, cookie, csrf, ra)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ra.handle("GET /v1/machine", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	was := fleetAskTimeout
	fleetAskTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fleetAskTimeout = was })
	done := make(chan struct{})
	go func() {
		e.srv.watchFleet(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a joined machine that doesn't answer held up the look")
	}
	e.posted(t, []map[string]any{{"kind": "disk_filling", "machine": "the dashboard's machine", "percent": 80}}, "disk_filling")
}

// A machine whose customers' plans set aside more memory than it has, as
// when a plan grows past what it can hold, is posted once, with how much
// more, and again only after it had room for them.
func TestAMachineWhoseCustomersPlansOutgrowItIsPostedOnce(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	e.reply("GET", "/v1/machine", liveMachine(6144, true))
	alex := Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}
	if _, err := (customerCore{s: e.srv}).StartCustomer(ctx, alex, starter); err != nil {
		t.Fatal(err)
	}
	e.srv.watchFleet(ctx)
	e.posted(t, nil, "overbooked")
	big := CustomerPlan{ID: "plan_big", Name: "Big", Servers: 2, MemoryMB: 8192}
	grow := func(p CustomerPlan) {
		t.Helper()
		if err := (customerCore{s: e.srv}).ChangeCustomerPlan(ctx, alex, p); err != nil {
			t.Fatal(err)
		}
		e.srv.watchFleet(ctx)
		e.srv.watchFleet(ctx)
	}
	grow(big)
	e.posted(t, []map[string]any{{"kind": "overbooked", "machine": "the dashboard's machine", "memoryMB": 2048}}, "overbooked")
	// A machine that doesn't answer for a while says nothing either way.
	e.agent.mu.Lock()
	e.agent.statuses["GET /v1/machine"] = http.StatusServiceUnavailable
	e.agent.mu.Unlock()
	e.srv.watchFleet(ctx)
	e.agent.mu.Lock()
	delete(e.agent.statuses, "GET /v1/machine")
	e.agent.mu.Unlock()
	e.srv.watchFleet(ctx)
	e.posted(t, []map[string]any{{"memoryMB": 2048}}, "overbooked")
	grow(starter)
	grow(big)
	e.posted(t, []map[string]any{{"memoryMB": 2048}, {"kind": "overbooked", "memoryMB": 2048}}, "overbooked")
}

// A look that can't read the plans on sale is skipped, not taken for a
// dashboard without a fleet, so what the watch keeps stays: nothing is
// posted again once the plans read.
func TestALookThatCantReadThePlansKeepsWhatTheWatchKept(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	e.reply("GET", "/v1/machine", fmt.Sprintf(`{"hostname":"siya","memoryTotalMB":32000,"memoryFreeMB":20480,"diskFreeBytes":%d,"diskTotalBytes":%d,"guard":{"on":true,"host":true}}`, int64(20)<<30, int64(100)<<30))
	e.srv.watchFleet(ctx)
	if _, err := e.srv.db.Exec(`ALTER TABLE whop_plans RENAME TO whop_plans_away`); err != nil {
		t.Fatal(err)
	}
	e.srv.watchFleet(ctx)
	if _, err := e.srv.db.Exec(`ALTER TABLE whop_plans_away RENAME TO whop_plans`); err != nil {
		t.Fatal(err)
	}
	e.srv.watchFleet(ctx)
	e.posted(t, []map[string]any{{"kind": "disk_filling", "percent": 80}}, "disk_filling")
}

// A dashboard with neither a store nor a joined machine has no fleet, so
// nothing about machines is posted.
func TestADashboardWithoutAFleetPostsNothingAboutIt(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	ctx := context.Background()
	e.reply("GET", "/v1/machine", fmt.Sprintf(`{"hostname":"siya","cpuPercent":95,"memoryTotalMB":32000,"memoryFreeMB":1024,"diskFreeBytes":%d,"diskTotalBytes":%d}`, int64(5)<<30, int64(100)<<30))
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online","players":{"online":5,"max":20,"names":[]},"resources":{"mspt":90}}]`)
	for range 20 {
		e.srv.watchFleet(ctx)
		e.clock.add(time.Hour)
	}
	e.posted(t, nil, "machine_off", "machine_back", "low_room", "disk_filling", "busy_cpu", "slow_ticks")
}
