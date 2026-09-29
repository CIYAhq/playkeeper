package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/netguard"
	"github.com/CIYAhq/playkeeper/internal/netguard/netguardtest"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// newGuardEnv is an agent whose network guard runs iptables in the fake it
// returns, looking at the rules every interval (never when negative).
func newGuardEnv(t *testing.T, interval time.Duration, setup func(e *agentEnv)) (*agentEnv, *netguardtest.Tables) {
	t.Helper()
	fw := netguardtest.New()
	e := newAgentEnvWith(t, func(e *agentEnv) {
		if setup != nil {
			setup(e)
		}
		prev := e.tweak
		e.tweak = func(o *Options) {
			if prev != nil {
				prev(o)
			}
			o.Firewall, o.GuardInterval = fw.Run, interval
		}
	})
	return e, fw
}

// keepAway sets the owner's switch, Keep servers away from this machine.
func (e *agentEnv) keepAway(on bool) api.NetworkGuard {
	e.t.Helper()
	var g api.NetworkGuard
	if code := e.callInto("POST", "/v1/network-guard", map[string]any{"host": on, "actor": "admin"}, &g); code != 200 {
		e.t.Fatalf("keeping servers away %v: %d %+v", on, code, g)
	}
	return g
}

// guardRules are the guard's rules in fam's chain.
func guardRules(fw *netguardtest.Tables, fam, chain string) []string {
	return slices.DeleteFunc(fw.Rules(fam, chain), func(r string) bool { return !strings.Contains(r, netguard.Tag) })
}

// metadataKept reports whether the guard's rule for br-0123456789ab is the
// first in DOCKER-USER.
func metadataKept(fw *netguardtest.Tables) bool {
	du := fw.Rules("iptables", "DOCKER-USER")
	return len(du) > 0 && strings.HasPrefix(du[0], "-d 169.254.0.0/16 -i br-0123456789ab ") && strings.Contains(du[0], netguard.Tag)
}

// kept reports whether the guard's rules for br-0123456789ab are the first
// in INPUT and DOCKER-USER.
func kept(fw *netguardtest.Tables) bool {
	in := fw.Rules("iptables", "INPUT")
	return len(in) >= 2 && metadataKept(fw) &&
		strings.HasPrefix(in[0], "-i br-0123456789ab -p tcp ") && strings.Contains(in[0], netguard.Tag) &&
		strings.HasPrefix(in[1], "-i br-0123456789ab -p udp ") && strings.Contains(in[1], netguard.Tag)
}

func (e *agentEnv) guard() *api.NetworkGuard { return e.a.Machine(context.Background()).Guard }

// On a machine its owner shares with no one, servers can reach it, as a
// plugin needs for a database there; the metadata service they never can.
func TestByDefaultServersKeepOnlyOutOfTheMetadataService(t *testing.T) {
	e, fw := newGuardEnv(t, -1, nil)
	if g := e.guard(); g == nil || g.On || g.Host || g.Problem != "" {
		t.Fatalf("with no network yet: %+v", g)
	}
	e.create()
	if r := guardRules(fw, "iptables", "INPUT"); len(r) != 0 {
		t.Fatalf("servers may reach the machine, but INPUT has %q", r)
	}
	if !metadataKept(fw) {
		t.Fatalf("the metadata service must stay out of reach: %q", fw.Rules("iptables", "DOCKER-USER"))
	}
	if g := e.guard(); g == nil || !g.On || g.Host || g.Problem != "" {
		t.Fatalf("machine guard: %+v", g)
	}
}

func TestTheGuardIsInPlaceBeforeAServersContainerStarts(t *testing.T) {
	var mu sync.Mutex
	var atStart []bool
	e, fw := newGuardEnv(t, -1, nil)
	e.fd.started = func(*fakeContainer) {
		mu.Lock()
		defer mu.Unlock()
		atStart = append(atStart, kept(fw))
	}
	if g := e.keepAway(true); !g.Host || g.On {
		t.Fatalf("keeping servers away with no network yet: %+v", g)
	}
	e.create()
	mu.Lock()
	defer mu.Unlock()
	if len(atStart) == 0 || slices.Contains(atStart, false) {
		t.Fatalf("the rules weren't all in place as the server's container started: %v\n%q\n%q", atStart, fw.Rules("iptables", "INPUT"), fw.Rules("iptables", "DOCKER-USER"))
	}
	if g := e.guard(); g == nil || !g.On || !g.Host || g.Problem != "" {
		t.Fatalf("machine guard: %+v", g)
	}
}

// The owner's switch changes the rules at once, and lasts.
func TestKeepingServersAwayIsTheOwnersSwitch(t *testing.T) {
	e, fw := newGuardEnv(t, -1, nil)
	e.create()
	if g := e.keepAway(true); !g.On || !g.Host || !kept(fw) {
		t.Fatalf("turning it on: %+v\n%q", g, fw.Rules("iptables", "INPUT"))
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'machine.network_guard'`); n != 1 {
		t.Fatalf("%d audit entries", n)
	}
	e.stop()
	e.start()
	if g := e.guard(); g == nil || !g.Host {
		t.Fatalf("after the agent restarted: %+v", g)
	}
	if g := e.keepAway(false); !g.On || g.Host || len(guardRules(fw, "iptables", "INPUT")) != 0 || !metadataKept(fw) {
		t.Fatalf("turning it off: %+v\n%q", g, fw.Rules("iptables", "INPUT"))
	}
}

func TestTheGuardPutsBackRulesSomethingRemoved(t *testing.T) {
	e, fw := newGuardEnv(t, 20*time.Millisecond, nil)
	e.keepAway(true)
	e.create()
	e.waitFor("the rules", func() bool { return kept(fw) })
	// ufw reloading, then an admin's rule for the bridge at the top.
	fw.Flush("iptables", "INPUT")
	fw.Flush("iptables", "DOCKER-USER")
	e.waitFor("the rules again", func() bool { return kept(fw) })
	fw.Insert("iptables", "INPUT", 1, "-i", "br-0123456789ab", "-j", "ACCEPT")
	e.waitFor("the rules back at the top", func() bool { return kept(fw) })
	if n := len(guardRules(fw, "iptables", "INPUT")); n != 2 {
		t.Fatalf("want the guard's 2 rules in INPUT, got %d: %q", n, fw.Rules("iptables", "INPUT"))
	}
}

// A database that can't be read or written for a while leaves servers kept
// away from the machine: the switch is in memory, and changes once stored.
func TestADatabaseErrorLeavesServersKeptAway(t *testing.T) {
	e, fw := newGuardEnv(t, 20*time.Millisecond, nil)
	e.keepAway(true)
	e.create()
	e.waitFor("the rules", func() bool { return kept(fw) })
	if _, err := e.a.db.Exec(`ALTER TABLE kv RENAME TO kv_unreadable`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.a.db.Exec(`ALTER TABLE kv_unreadable RENAME TO kv`) })
	looks := func() int {
		return len(slices.DeleteFunc(fw.Calls(), func(c string) bool { return c != "iptables -w 5 -S" }))
	}
	n := looks()
	e.waitFor("three more looks at the rules", func() bool { return looks() >= n+3 })
	if !kept(fw) {
		t.Fatalf("servers can reach the machine while the database can't be read: %q", fw.Rules("iptables", "INPUT"))
	}
	var out map[string]any
	if code := e.callInto("POST", "/v1/network-guard", map[string]any{"host": false, "actor": "admin"}, &out); code == 200 {
		t.Fatalf("the switch turned off without being stored: %v", out)
	}
	if g := e.guard(); g == nil || !g.Host || !g.On || !kept(fw) {
		t.Fatalf("after the switch couldn't be stored: %+v\n%q", g, fw.Rules("iptables", "INPUT"))
	}
}

// An agent that can't read the owner's switch doesn't start, rather than
// start with servers free to reach the machine.
func TestTheAgentWontStartWithoutReadingTheSwitch(t *testing.T) {
	e, _ := newGuardEnv(t, -1, nil)
	e.keepAway(true)
	e.stop()
	db, err := store.Open(filepath.Join(e.cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		t.Fatal(err)
	}
	// The switch's value can't be read, as on a disk error.
	_, err = db.Exec(`ALTER TABLE kv RENAME TO kv_saved;
		CREATE VIEW kv AS SELECT key, CASE key WHEN 'network_guard_host' THEN json(value || '{') ELSE value END AS value FROM kv_saved`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(e.options())
	if a != nil {
		a.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "network guard's switch") {
		t.Fatalf("the agent started without reading the switch: %v", err)
	}
}

// While servers may reach the machine, one starts even when the metadata
// rule can't be put in place, as on a machine without iptables.
func TestAServerStartsWhenTheGuardCant(t *testing.T) {
	var mu sync.Mutex
	broken := true
	e, fw := newGuardEnv(t, 20*time.Millisecond, nil)
	fw.Fail = func(name string, args []string) error {
		mu.Lock()
		defer mu.Unlock()
		if broken {
			return errors.New("the iptables command isn't installed")
		}
		return nil
	}
	e.create()
	g := e.guard()
	if g == nil || g.On || !strings.Contains(g.Problem, "iptables command isn't installed") {
		t.Fatalf("machine guard: %+v", g)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(e.warnings.String(), "the network guard's rules are not in place"); n != 1 {
		t.Fatalf("want the problem logged once, got %d times:\n%s", n, e.warnings.String())
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	e.waitFor("the rule", func() bool { return metadataKept(fw) })
	e.waitFor("the guard on", func() bool { g := e.guard(); return g != nil && g.On && g.Problem == "" })
}

// While servers are to be kept away from the machine, none starts without
// the rules that do it.
func TestAServerWontStartWhileItCantBeKeptAway(t *testing.T) {
	var mu sync.Mutex
	broken := true
	e, fw := newGuardEnv(t, -1, nil)
	fw.Fail = func(name string, args []string) error {
		mu.Lock()
		defer mu.Unlock()
		if broken {
			return errors.New("the iptables command isn't installed")
		}
		return nil
	}
	e.keepAway(true)
	code, out := e.startCreate(map[string]any{})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status == api.OpSucceeded && e.status().Phase == api.PhaseOnline {
		t.Fatal("the server started without the rules that keep it away from the machine")
	}
	e.waitFor("the start to fail", func() bool {
		st := e.status()
		return st.Phase != api.PhaseStarting && strings.Contains(st.LastError+op.Error, "couldn't keep this server away from the machine")
	})
	e.fd.mu.Lock()
	started := slices.ContainsFunc(e.fd.calls, func(c string) bool {
		return strings.HasPrefix(c, "POST /containers/") && strings.HasSuffix(c, "/start")
	})
	e.fd.mu.Unlock()
	if started {
		t.Fatal("the server's container started")
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	if code, out := e.callWhenFree("POST", e.sp("/start"), map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if !kept(fw) {
		t.Fatalf("online without the rules: %q", fw.Rules("iptables", "INPUT"))
	}
}

func TestTheGuardUsesTheNetworksOwnInterfaceName(t *testing.T) {
	e, fw := newGuardEnv(t, -1, func(e *agentEnv) { e.fd.networkBridge = "pk0" })
	e.keepAway(true)
	e.create()
	for _, r := range append(guardRules(fw, "iptables", "INPUT"), guardRules(fw, "iptables", "DOCKER-USER")...) {
		if !strings.Contains(r, "-i pk0 ") {
			t.Fatalf("a rule isn't for the network's interface pk0: %q", r)
		}
	}
	if n := len(guardRules(fw, "iptables", "INPUT")); n != 2 {
		t.Fatalf("want 2 rules in INPUT, got %d", n)
	}
}

func TestTheGuardKeepsAnIPv6NetworkFromTheMachine(t *testing.T) {
	e, fw := newGuardEnv(t, -1, func(e *agentEnv) { e.fd.networkIPv6 = true })
	e.keepAway(true)
	e.create()
	if r := guardRules(fw, "ip6tables", "INPUT"); len(r) != 2 {
		t.Fatalf("want the guard's 2 rules in ip6tables' INPUT, got %q", r)
	}
}

func TestTheGuardNeedsABridge(t *testing.T) {
	e, fw := newGuardEnv(t, -1, func(e *agentEnv) { e.fd.networkDriver = "macvlan" })
	e.create()
	if g := e.guard(); g == nil || g.On || !strings.Contains(g.Problem, `"macvlan" driver`) {
		t.Fatalf("machine guard: %+v", g)
	}
	if r := fw.Restores(); len(r) != 0 {
		t.Fatalf("rules for a network that isn't a bridge: %q", r)
	}
}

func TestNoGuardInDevMode(t *testing.T) {
	e := newAgentEnv(t)
	// The switch is kept, as a creator invite made there needs, but
	// nothing changes the firewall or holds a server back.
	if g := e.keepAway(true); !g.Host {
		t.Fatalf("keeping servers away in dev mode: %+v", g)
	}
	e.create()
	if e.a.opts.Firewall != nil {
		t.Fatal("dev mode must leave the machine's firewall alone")
	}
	if g := e.guard(); g != nil {
		t.Fatalf("machine guard: %+v", g)
	}
	cfg := config.Default()
	if defaultFirewall(cfg, 0) == nil {
		t.Fatal("an installed agent, which runs as root, runs iptables")
	}
	if defaultFirewall(cfg, 1000) != nil {
		t.Fatal("an agent that isn't root can't run iptables")
	}
	cfg.Dev = true
	if defaultFirewall(cfg, 0) != nil {
		t.Fatal("playkeeper dev run as root must leave the contributor's firewall alone")
	}
}
