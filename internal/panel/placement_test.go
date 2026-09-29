package panel

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// liveMachine is the dashboard's own machine as its agent reports it, with
// freeMB left for new servers' budgets and Keep servers away from this
// machine on or off.
func liveMachine(freeMB int, guarded bool) string {
	return fmt.Sprintf(`{"hostname":"siya","memoryTotalMB":32000,"systemReserveMB":768,"serversMemoryMB":%d,"memoryFreeMB":%d,"guard":{"on":true,"host":%t}}`, 31232-freeMB, freeMB, guarded)
}

// fourGB is a creator's allowance of one server with 4 GB, a Starter's.
var fourGB = invites.Allowance{Servers: 1, MemoryMB: 4096}

var starter = CustomerPlan{ID: "plan_starter", Name: "Starter", Servers: 1, MemoryMB: 4096}

// A standalone Playkeeper, such as a Whop blueprint seller's or
// siya.playkeeper.me as the Pip Hosting test seller, has no other machine
// and no fleet setup: its own machine takes its customers.
func TestAStandaloneDashboardPlacesCustomersOnItsOwnMachine(t *testing.T) {
	hetzner := newFakeHetzner(t)
	e := newHetznerEnv(t, hetzner)
	owner(t, e)
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.reply("GET", "/v1/servers", `[]`)
	alex := addCreator(t, e, "alexplays", fourGB)
	local := machineID(t, e)
	var machines, tokens int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM machines`).Scan(&machines)
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM hetzner_watch`).Scan(&tokens)
	if machines != 1 || tokens != 0 {
		t.Fatalf("%d machines and %d Hetzner tokens, want the dashboard's own machine alone and no token", machines, tokens)
	}
	defer func() {
		if n := hetzner.questions(); n != 0 {
			t.Errorf("placement asked Hetzner %d times", n)
		}
	}()

	got, err := e.srv.placeCustomer(t.Context(), alex.id, starter)
	if err != nil || got != local {
		t.Fatalf("placeCustomer = %q, %v; want the dashboard's own machine %q", got, err, local)
	}
	if again, err := e.srv.placeCustomer(t.Context(), alex.id, starter); err != nil || again != local {
		t.Fatalf("placing again = %q, %v", again, err)
	}
	if home, ok, err := e.srv.homeMachine(t.Context(), alex.id); err != nil || !ok || home != local {
		t.Fatalf("homeMachine = %q, %v, %v", home, ok, err)
	}
	rows := e.auditRows(t, "customer.place")
	if len(rows) != 1 || !strings.Contains(rows[0], fmt.Sprintf("playkeeper %d placed on", alex.id)) || !strings.Contains(rows[0], "4 GB set aside for Starter") {
		t.Fatalf("audit: %v", rows)
	}
	if slices.Contains(e.agentHits(), "POST /v1/network-guard") {
		t.Fatal("placement set the guard, which was already on")
	}
}

func TestPlacingKeepsServersAwayFromTheMachineFirst(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	e.reply("GET", "/v1/machine", liveMachine(30000, false))
	e.reply("GET", "/v1/servers", `[]`)
	alex := addCreator(t, e, "alexplays", fourGB)
	if got, err := e.srv.placeCustomer(t.Context(), alex.id, starter); err != nil || got != machineID(t, e) {
		t.Fatalf("placeCustomer = %q, %v", got, err)
	}
	if body := e.agent.lastBody["POST /v1/network-guard"]; !strings.Contains(body, `"host":true`) {
		t.Fatalf("the guard wasn't turned on: %q", body)
	}

	// An agent that won't keep servers away gets no customers.
	e2 := newEnv(t)
	owner(t, e2)
	e2.reply("GET", "/v1/machine", liveMachine(30000, false))
	e2.reply("GET", "/v1/servers", `[]`)
	e2.reply("POST", "/v1/network-guard", `{"on":true,"host":false}`)
	sam := addCreator(t, e2, "samcrafts", fourGB)
	if got, err := e2.srv.placeCustomer(t.Context(), sam.id, starter); !errors.Is(err, errNoRoom) || got != "" {
		t.Fatalf("placeCustomer with the guard off = %q, %v; want errNoRoom", got, err)
	}
	if home, ok, err := e2.srv.homeMachine(t.Context(), sam.id); err != nil || ok || home != "" {
		t.Fatalf("a waiting customer's home = %q, %v, %v", home, ok, err)
	}
	if rows := e2.auditRows(t, "customer.place"); len(rows) != 1 || !strings.Contains(rows[0], "waiting no machine has 4 GB free for Starter") {
		t.Fatalf("audit: %v", rows)
	}
}

// Each creator's plan is set aside on their machine until their servers use
// it, and never counted twice once they do.
func TestPlacementSetsAsideEachPlanUntilItsUsed(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	local := machineID(t, e)
	e.reply("GET", "/v1/machine", liveMachine(8192, true))
	e.reply("GET", "/v1/servers", `[]`)
	// An invited creator has no row: their 4 GB is set aside on the
	// dashboard's own machine, and Alex takes the other 4 GB.
	invited := addCreator(t, e, "invited", fourGB)
	alex := addCreator(t, e, "alexplays", fourGB)
	if got, err := e.srv.placeCustomer(t.Context(), alex.id, starter); err != nil || got != local {
		t.Fatalf("Alex: %q, %v", got, err)
	}
	sam := addCreator(t, e, "samcrafts", fourGB)
	if _, err := e.srv.placeCustomer(t.Context(), sam.id, starter); !errors.Is(err, errNoRoom) {
		t.Fatalf("Sam with all 8 GB set aside: %v, want errNoRoom", err)
	}

	// Alex's 4 GB server comes out of the machine's free memory and out of
	// Alex's share at once: the machine is still full.
	e.reply("GET", "/v1/machine", liveMachine(4096, true))
	e.reply("GET", "/v1/servers", `[{"id":"alexsrv001","name":"Alex","phase":"stopped","config":{"memoryMB":4096}}]`)
	if _, err := e.srv.db.Exec(`INSERT INTO creator_servers(server_id, user_id, created_at) VALUES('alexsrv001', ?, 0)`, alex.id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.placeCustomer(t.Context(), sam.id, starter); !errors.Is(err, errNoRoom) {
		t.Fatalf("Sam once Alex uses their share: %v, want errNoRoom", err)
	}
	// Without the invited creator, the 4 GB they had is Sam's.
	if _, err := e.srv.db.Exec(`UPDATE project_members SET allowance_servers = 0, allowance_memory_mb = 0 WHERE user_id = ?`, invited.id); err != nil {
		t.Fatal(err)
	}
	if got, err := e.srv.placeCustomer(t.Context(), sam.id, starter); err != nil || got != local {
		t.Fatalf("Sam once the invited creator is gone: %q, %v", got, err)
	}
	rows := e.auditRows(t, "customer.place")
	if len(rows) != 3 || !strings.Contains(rows[0], "placed") || !strings.Contains(rows[1], "waiting") || !strings.Contains(rows[2], "placed") {
		t.Fatalf("audit, with Sam's wait once: %v", rows)
	}
	if home, ok, _ := e.srv.homeMachine(t.Context(), alex.id); !ok || home != local {
		t.Fatalf("Alex's home = %q, %v", home, ok)
	}
	if _, ok, _ := e.srv.homeMachine(t.Context(), 999); ok {
		t.Fatal("an account that isn't a creator has a home")
	}
}

func TestJoinedMachinesTakeNoCustomersUntilConfirmed(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.reply("GET", "/v1/servers", `[]`)
	joined := machine{ID: "j2345abcde", Name: "fsn1-2", Kind: remoteKind, agent: e.srv.agent}
	r := e.srv.machineRoom(t.Context(), joined, machineID(t, e), 0, nil, nil, nil)
	if r.Takes || r.Why == "" || r.FreeMB != 30000 || !r.Guarded {
		t.Fatalf("a joined machine: %+v", r)
	}
}

func TestTheFullestMachineThatTakesCustomersWins(t *testing.T) {
	room := func(id, kind string, takes bool, freeMB int) machineRoom {
		return machineRoom{machine: machine{ID: id, Kind: kind}, Takes: takes, FreeMB: freeMB}
	}
	rooms := []machineRoom{
		room("localaaaaa", localKind, true, 20000),
		room("unconfirmd", remoteKind, false, 5000),
		room("fsn1bbbbbb", remoteKind, true, 6000),
		room("nbg1cccccc", remoteKind, true, 4096),
	}
	for _, c := range []struct {
		need int
		want string
	}{{4096, "nbg1cccccc"}, {5000, "fsn1bbbbbb"}, {8192, "localaaaaa"}, {32768, ""}} {
		got, ok := chooseMachine(rooms, c.need)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%d MB: %q, %v; want %q", c.need, got.ID, ok, c.want)
		}
	}
	tie := []machineRoom{room("hel1dddddd", remoteKind, true, 6000), room("localaaaaa", localKind, true, 6000), room("fsn1bbbbbb", remoteKind, true, 6000)}
	if got, _ := chooseMachine(tie, 4096); got.ID != "localaaaaa" {
		t.Errorf("a tie goes to %q, want the dashboard's own machine", got.ID)
	}
	if got, _ := chooseMachine(tie[:1:1], 4096); got.ID != "hel1dddddd" {
		t.Errorf("one machine: %q", got.ID)
	}
}
