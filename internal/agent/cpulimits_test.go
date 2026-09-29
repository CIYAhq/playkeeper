package agent

import (
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// containerCPUs is the env's server container's processor cap, in
// billionths of a core, or 0 for none.
func (e *agentEnv) containerCPUs() int64 {
	e.t.Helper()
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[e.cname()]
	if c == nil {
		e.t.Fatal("no server container")
	}
	return c.cfg.HostConfig.NanoCPUs
}

// shareCPUs gives the env's server a limit with a processor share of milli
// thousandths of a core for each GB of its memory.
func (e *agentEnv) shareCPUs(milli int) (int, map[string]any) {
	e.t.Helper()
	return e.call("PUT", "/v1/disk-limits", map[string]any{"limits": []any{map[string]any{
		"id": "customer-6", "limitBytes": 1 << 40, "servers": []string{e.sid}, "cpuMilliPerGB": milli}}, "actor": "admin"})
}

// A customer's servers get their limit's processor share for each GB of
// memory. A running server is capped at once when its share is set or
// changes, and asks for no restart. Lifting the limit gives back every
// core. A stopped container with another cap is made again with its own
// when the server starts, and no cap is more than the machine's cores.
func TestCustomersServersGetTheirShareOfTheProcessor(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	sc, err := e.srv().serverConfig()
	if err != nil || sc == nil {
		t.Fatal(sc, err)
	}
	all := int64(numCPU()) * 1_000_000_000
	share := func(milli int) int64 { return min(int64(sc.MemoryMB)*int64(milli)*1_000_000/1024, all) }
	set := func(milli int) {
		t.Helper()
		if code, out := e.shareCPUs(milli); code != 200 {
			t.Fatalf("a share of %d: %d %v", milli, code, out)
		}
	}

	if got := e.containerCPUs(); got != 0 {
		t.Fatalf("a server outside every limit: %d", got)
	}
	set(500)
	if got := e.containerCPUs(); got != share(500) || got <= 0 {
		t.Fatalf("a running server given half a core per GB: %d, want %d", got, share(500))
	}
	set(1000)
	if got := e.containerCPUs(); got != share(1000) {
		t.Fatalf("a running server once its share changed: %d, want %d", got, share(1000))
	}
	if e.status().PendingRestart {
		t.Fatal("a new share asks for a restart")
	}
	var none []api.DiskLimit
	if code := e.callInto("PUT", "/v1/disk-limits", map[string]any{"limits": []any{}, "actor": "admin"}, &none); code != 200 {
		t.Fatalf("lifting the limit: %d", code)
	}
	if got := e.containerCPUs(); got != all {
		t.Fatalf("a running server once its limit went: %d, want every core, %d", got, all)
	}
	e.runOp("POST", "/stop")
	set(500)
	e.runOp("POST", "/start")
	if got := e.containerCPUs(); got != share(500) {
		t.Fatalf("a stopped server once it started again: %d, want %d", got, share(500))
	}
	set(maxCPUMilliPerGB)
	if got := e.containerCPUs(); got != share(maxCPUMilliPerGB) || got > all {
		t.Fatalf("a share past the machine's cores: %d, want %d", got, share(maxCPUMilliPerGB))
	}
	for _, milli := range []int{-1, minCPUMilliPerGB - 1, maxCPUMilliPerGB + 1} {
		if code, _ := e.shareCPUs(milli); code != 400 {
			t.Errorf("a share of %d: %d", milli, code)
		}
	}
}

// A server in a limit with no processor share isn't capped, and its
// container isn't made again for it.
func TestServersWithoutAShareKeepEveryCore(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.limitTo(1 << 40)
	e.fd.mu.Lock()
	id := e.fd.byName[e.cname()].id
	e.fd.mu.Unlock()
	e.runOp("POST", "/stop")
	e.runOp("POST", "/start")
	e.fd.mu.Lock()
	again := e.fd.byName[e.cname()].id
	e.fd.mu.Unlock()
	if got := e.containerCPUs(); got != 0 || again != id {
		t.Fatalf("a server whose limit has no processor share: cap %d, container %s then %s", got, id, again)
	}
}
