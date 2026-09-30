package panel

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
)

// hetznerFleetEnv is a dashboard watching Hetzner stock with the owner's
// read-only token, with a machine joined from 127.0.0.1 that takes no
// customers yet and doesn't keep servers away from itself.
func hetznerFleetEnv(t *testing.T) (*env, *fakeHetzner, member, string, *guardSwitch, *runningLink) {
	t.Helper()
	f := newFakeHetzner(t)
	e := newEnvConfig(t, func(c *config.Config) { withDomain(c); c.HetznerAPIURL = f.srv.URL }, nil)
	own := owner(t, e)
	rid, _, guard, link := joinForCustomers(t, e, own)
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watching Hetzner: %d %v", r.status, r.body)
	}
	return e, f, own, rid, guard, link
}

// inProject has the fake Hetzner project hold a server at ipv4.
func (f *fakeHetzner) inProject(name, ipv4 string) {
	f.change(func(f *fakeHetzner) {
		f.servers = append(f.servers, map[string]any{"id": 42 + len(f.servers), "name": name, "public_net": map[string]any{"ipv4": map[string]any{"ip": ipv4}, "ipv6": nil}})
	})
}

// listings is how many times the fake Hetzner project's servers were listed.
func (f *fakeHetzner) listings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listed
}

// takesCustomersBy is who confirmed the machine id takes customers, or ""
// while it takes none.
func (e *env) takesCustomersBy(t *testing.T, id string) string {
	t.Helper()
	m, err := e.srv.machineByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.customersAt.IsZero() {
		return ""
	}
	return m.customersBy
}

// A joined machine connected from a server in the owner's Hetzner project
// is confirmed by itself, as confirming by hand does it: servers are kept
// away from it first, and the audit log and its events say so. One from
// anywhere else waits for the owner.
func TestAJoinedMachineInTheOwnersHetznerProjectIsConfirmedByItself(t *testing.T) {
	e, f, _, rid, guard, _ := hetznerFleetEnv(t)
	ctx := context.Background()
	f.inProject("elsewhere", "203.0.113.7")
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "" {
		t.Fatalf("a machine outside the Hetzner project was confirmed by %q", by)
	}
	if asked := guard.requests(); len(asked) != 0 {
		t.Fatalf("a machine outside the Hetzner project was asked to keep servers away: %v", asked)
	}

	f.inProject("fleet-1", "127.0.0.1")
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "hetzner:fleet-1" {
		t.Fatalf("the machine found in the Hetzner project, confirmed by %q", by)
	}
	if asked := guard.requests(); len(asked) != 1 {
		t.Fatalf("keeping servers away from it first: %v", asked)
	}
	if !e.auditHas(t, "hetzner:fleet-1", "machine.customers", "home-server", "confirmed", "takes customers, with servers kept away from it") {
		t.Fatal("the audit log doesn't say it was confirmed")
	}
	if kind, actor := lastMachineEvent(t, e, rid); kind != "machine.customers_on" || actor != "hetzner:fleet-1" {
		t.Fatalf("the machine's newest event: %s by %s", kind, actor)
	}
	listed := f.listings()
	e.srv.checkStock(ctx)
	if f.listings() != listed {
		t.Fatal("the project's servers were listed again with no machine left to confirm")
	}
}

// A machine the owner stopped taking customers isn't confirmed again, even
// one confirmed meanwhile by the owner and stopped again.
func TestAMachineTheOwnerStoppedIsntConfirmedAgain(t *testing.T) {
	e, f, own, rid, _, _ := hetznerFleetEnv(t)
	ctx := context.Background()
	path := "/api/machines/" + rid + "/customers"
	for _, on := range []string{`{"on":true}`, `{"on":false}`} {
		if r := e.do(t, "PUT", path, on, own.auth()); r.status != http.StatusOK {
			t.Fatalf("the owner sends %s: %d %v", on, r.status, r.body)
		}
	}
	f.inProject("fleet-1", "127.0.0.1")
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "" || f.listings() != 0 {
		t.Fatalf("a machine the owner stopped: confirmed again by %q, the project listed %d times", by, f.listings())
	}
	if err := e.srv.confirmFound(ctx, rid, "hetzner:fleet-1"); err != nil || e.takesCustomersBy(t, rid) != "" {
		t.Fatalf("confirming a machine the owner stopped: %v, confirmed by %q", err, e.takesCustomersBy(t, rid))
	}
}

// A machine that won't keep servers away from itself isn't confirmed, and
// isn't asked again for a while.
func TestAMachineThatWontKeepServersAwayWaitsBeforeTheNextTry(t *testing.T) {
	e, f, _, rid, guard, _ := hetznerFleetEnv(t)
	ctx := context.Background()
	guard.mu.Lock()
	guard.refuse = true
	guard.mu.Unlock()
	f.inProject("fleet-1", "127.0.0.1")
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "" || len(guard.requests()) != 1 {
		t.Fatalf("a machine that won't keep servers away: confirmed by %q, asked %v", by, guard.requests())
	}
	e.clock.add(time.Minute)
	e.srv.checkStock(ctx)
	if n := len(guard.requests()); n != 1 {
		t.Fatalf("asked again a minute later: %d times in all", n)
	}
	guard.mu.Lock()
	guard.refuse = false
	guard.mu.Unlock()
	e.clock.add(confirmRetry)
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "hetzner:fleet-1" {
		t.Fatalf("once it keeps servers away, confirmed by %q", by)
	}
}

// Nothing is confirmed by itself without the owner's token, with a token
// Hetzner refuses, or for a machine that isn't connected.
func TestOnlyAConnectedMachineIsConfirmedAndOnlyWithTheOwnersToken(t *testing.T) {
	e, f, own, rid, _, link := hetznerFleetEnv(t)
	ctx := context.Background()
	f.inProject("fleet-1", "127.0.0.1")
	link.stop()
	eventually(t, "the machine is offline", func() bool { return linkState(e.machineView(t, own.cookie, rid)) == "offline" })
	e.srv.checkStock(ctx)
	if by := e.takesCustomersBy(t, rid); by != "" || f.listings() != 0 {
		t.Fatalf("a machine that isn't connected: confirmed by %q, the project listed %d times", by, f.listings())
	}

	e2, f2, own2, rid2, _, _ := hetznerFleetEnv(t)
	f2.inProject("fleet-1", "127.0.0.1")
	if r := e2.do(t, "DELETE", "/api/hetzner", "", own2.auth()); r.status != http.StatusOK {
		t.Fatalf("stopping the watch: %d %v", r.status, r.body)
	}
	e2.srv.checkStock(ctx)
	if by := e2.takesCustomersBy(t, rid2); by != "" || f2.listings() != 0 {
		t.Fatalf("without a token: confirmed by %q, the project listed %d times", by, f2.listings())
	}
	if r := e2.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own2.auth()); r.status != http.StatusOK {
		t.Fatalf("watching Hetzner again: %d %v", r.status, r.body)
	}
	f2.change(func(f *fakeHetzner) { f.refused = true })
	e2.srv.checkStock(ctx)
	if by := e2.takesCustomersBy(t, rid2); by != "" {
		t.Fatalf("with a token Hetzner refuses: confirmed by %q", by)
	}
}
