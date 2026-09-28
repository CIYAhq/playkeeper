package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/netguard"
	"github.com/CIYAhq/playkeeper/internal/netguard/netguardtest"
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

// guardRules are the guard's rules in fam's chain.
func guardRules(fw *netguardtest.Tables, fam, chain string) []string {
	return slices.DeleteFunc(fw.Rules(fam, chain), func(r string) bool { return !strings.Contains(r, netguard.Tag) })
}

// kept reports whether the guard's rules for br-0123456789ab are the first
// in INPUT and DOCKER-USER.
func kept(fw *netguardtest.Tables) bool {
	in, du := fw.Rules("iptables", "INPUT"), fw.Rules("iptables", "DOCKER-USER")
	return len(in) >= 2 && len(du) > 0 &&
		strings.HasPrefix(in[0], "-i br-0123456789ab -p tcp ") && strings.Contains(in[0], netguard.Tag) &&
		strings.HasPrefix(in[1], "-i br-0123456789ab -p udp ") && strings.Contains(in[1], netguard.Tag) &&
		strings.HasPrefix(du[0], "-d 169.254.0.0/16 -i br-0123456789ab ") && strings.Contains(du[0], netguard.Tag)
}

func (e *agentEnv) guard() *api.NetworkGuard { return e.a.Machine(context.Background()).Guard }

func TestTheGuardIsInPlaceBeforeAServersContainerStarts(t *testing.T) {
	var mu sync.Mutex
	var atStart []bool
	e, fw := newGuardEnv(t, -1, nil)
	e.fd.started = func(*fakeContainer) {
		mu.Lock()
		defer mu.Unlock()
		atStart = append(atStart, kept(fw))
	}
	if g := e.guard(); g != nil {
		t.Fatalf("with no network yet there is nothing to guard, but got %+v", g)
	}
	e.create()
	mu.Lock()
	defer mu.Unlock()
	if len(atStart) == 0 || slices.Contains(atStart, false) {
		t.Fatalf("the rules weren't all in place as the server's container started: %v\n%q\n%q", atStart, fw.Rules("iptables", "INPUT"), fw.Rules("iptables", "DOCKER-USER"))
	}
	if g := e.guard(); g == nil || !g.On || g.ServersReachHost || g.Problem != "" {
		t.Fatalf("machine guard: %+v", g)
	}
}

func TestTheGuardPutsBackRulesSomethingRemoved(t *testing.T) {
	e, fw := newGuardEnv(t, 20*time.Millisecond, nil)
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

func TestServersReachHostKeepsOnlyTheMetadataRule(t *testing.T) {
	e, fw := newGuardEnv(t, -1, func(e *agentEnv) { e.cfg.ServersReachHost = true })
	e.create()
	if r := guardRules(fw, "iptables", "INPUT"); len(r) != 0 {
		t.Fatalf("serversReachHost is on, but INPUT has %q", r)
	}
	if r := guardRules(fw, "iptables", "DOCKER-USER"); len(r) != 1 || !strings.HasPrefix(r[0], "-d 169.254.0.0/16 -i br-0123456789ab ") {
		t.Fatalf("the metadata service must stay out of reach: %q", r)
	}
	if g := e.guard(); g == nil || !g.On || !g.ServersReachHost {
		t.Fatalf("machine guard: %+v", g)
	}
}

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
	if n := strings.Count(e.warnings.String(), "servers are not kept from this machine"); n != 1 {
		t.Fatalf("want the problem logged once, got %d times:\n%s", n, e.warnings.String())
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	e.waitFor("the rules", func() bool { return kept(fw) })
	e.waitFor("the guard on", func() bool { g := e.guard(); return g != nil && g.On && g.Problem == "" })
}

func TestTheGuardUsesTheNetworksOwnInterfaceName(t *testing.T) {
	e, fw := newGuardEnv(t, -1, func(e *agentEnv) { e.fd.networkBridge = "pk0" })
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
