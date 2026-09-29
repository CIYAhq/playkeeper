package netguard_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/netguard"
	"github.com/CIYAhq/playkeeper/internal/netguard/netguardtest"
)

const bridge = "br-0123456789ab"

var (
	hostTCP  = "-i " + bridge + " -p tcp -m tcp ! --dport 53 -m conntrack ! --ctstate RELATED,ESTABLISHED -m comment --comment playkeeper-guard -j REJECT --reject-with tcp-reset"
	hostUDP  = "-i " + bridge + " -p udp -m udp ! --dport 53 -m conntrack ! --ctstate RELATED,ESTABLISHED -m comment --comment playkeeper-guard -j REJECT --reject-with icmp-port-unreachable"
	metadata = "-d 169.254.0.0/16 -i " + bridge + " -p tcp -m tcp ! --dport 53 -m comment --comment playkeeper-guard -j REJECT --reject-with tcp-reset"
	// ufw's, as netguardtest.New has them.
	ufwInput   = []string{"-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", "-p tcp -m tcp --dport 8443 -j ACCEPT"}
	dockerJump = []string{"-j DOCKER-USER", "-j DOCKER-FORWARD"}
)

func apply(t *testing.T, fw *netguardtest.Tables, n netguard.Network, host bool) {
	t.Helper()
	if err := netguard.Apply(context.Background(), fw.Run, n, host); err != nil {
		t.Fatal(err)
	}
}

func want(t *testing.T, fw *netguardtest.Tables, fam, chain string, rules ...string) {
	t.Helper()
	if got := fw.Rules(fam, chain); !slices.Equal(got, rules) {
		t.Fatalf("%s %s:\n  got  %q\n  want %q", fam, chain, got, rules)
	}
}

func TestApplyPutsTheRulesFirstInTheirChains(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	// Before ufw's accept for the dashboard's port, which would let a
	// container through first.
	want(t, fw, "iptables", "INPUT", append([]string{hostTCP, hostUDP}, ufwInput...)...)
	want(t, fw, "iptables", "DOCKER-USER", metadata)
	want(t, fw, "iptables", "FORWARD", dockerJump...)
	if r := fw.Restores(); len(r) != 1 || strings.Contains(r[0], "\n:") {
		t.Fatalf("want one restore that declares no chain, got %q", r)
	}
	for _, c := range fw.Calls() {
		if strings.HasPrefix(c, "ip6tables") {
			t.Fatalf("an IPv4 network needs no ip6tables, but ran %q", c)
		}
	}
}

func TestApplyAgainChangesNothing(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	if r := fw.Restores(); len(r) != 1 {
		t.Fatalf("the second apply changed the rules again: %q", r)
	}
	want(t, fw, "iptables", "INPUT", append([]string{hostTCP, hostUDP}, ufwInput...)...)
}

func TestApplyMovesBuriedRulesBackToTheTop(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	// An admin's rule for the bridge, above the guard's, would let every
	// container in.
	fw.Insert("iptables", "INPUT", 1, "-i", bridge, "-j", "ACCEPT")
	fw.Insert("iptables", "DOCKER-USER", 1, "-j", "RETURN")
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "INPUT", append([]string{hostTCP, hostUDP, "-i " + bridge + " -j ACCEPT"}, ufwInput...)...)
	want(t, fw, "iptables", "DOCKER-USER", metadata, "-j RETURN")
}

func TestApplyPutsBackRulesAReloadRemoved(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	fw.Flush("iptables", "INPUT")
	fw.Append("iptables", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT")
	fw.Flush("iptables", "DOCKER-USER")
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "INPUT", hostTCP, hostUDP, "-p tcp -m tcp --dport 22 -j ACCEPT")
	want(t, fw, "iptables", "DOCKER-USER", metadata)
}

func TestApplyFollowsANewBridge(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: "br-aaaaaaaaaaaa"}, true)
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "INPUT", append([]string{hostTCP, hostUDP}, ufwInput...)...)
	want(t, fw, "iptables", "DOCKER-USER", metadata)
}

func TestServersMayReachTheMachine(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge, IPv6: true}, true)
	apply(t, fw, netguard.Network{Bridge: bridge, IPv6: true}, false)
	want(t, fw, "iptables", "INPUT", ufwInput...)
	want(t, fw, "ip6tables", "INPUT", ufwInput...)
	// The metadata service stays out of reach.
	want(t, fw, "iptables", "DOCKER-USER", metadata)
}

func TestTheMetadataRuleGoesInForwardWithoutDockerUser(t *testing.T) {
	fw := netguardtest.New()
	fw.DeleteChain("iptables", "DOCKER-USER")
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "FORWARD", metadata, "-j DOCKER-FORWARD")
	// Docker starts managing the firewall again: DOCKER-USER comes first.
	fw.NewChain("iptables", "DOCKER-USER")
	fw.Insert("iptables", "FORWARD", 1, "-j", "DOCKER-USER")
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "FORWARD", dockerJump...)
	want(t, fw, "iptables", "DOCKER-USER", metadata)
}

func TestAnIPv6NetworkIsKeptFromTheMachineToo(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge, IPv6: true}, true)
	udp6 := strings.Replace(hostUDP, "icmp-port-unreachable", "icmp6-port-unreachable", 1)
	want(t, fw, "ip6tables", "INPUT", append([]string{hostTCP, udp6}, ufwInput...)...)
	want(t, fw, "ip6tables", "DOCKER-USER")
	want(t, fw, "iptables", "DOCKER-USER", metadata)
}

func TestApplyRefusesAnUnusableInterfaceName(t *testing.T) {
	for _, name := range []string{"", "br-0123456789abc", "br x", "br\n-F INPUT", "br-a/b"} {
		fw := netguardtest.New()
		if err := netguard.Apply(context.Background(), fw.Run, netguard.Network{Bridge: name}, true); err == nil {
			t.Errorf("%q: want an error", name)
		}
		if c := fw.Calls(); len(c) != 0 {
			t.Errorf("%q: ran %q", name, c)
		}
	}
}

func TestApplySaysWhatFailed(t *testing.T) {
	fw := netguardtest.New()
	fw.Fail = func(name string, args []string) error {
		if name == "iptables-restore" {
			return errors.New("iptables-restore: line 2 failed")
		}
		return nil
	}
	err := netguard.Apply(context.Background(), fw.Run, netguard.Network{Bridge: bridge}, true)
	if err == nil || !strings.Contains(err.Error(), "line 2 failed") {
		t.Fatalf("got %v", err)
	}
	want(t, fw, "iptables", "INPUT", ufwInput...)
}

func TestQuotedCommentsAreTheGuards(t *testing.T) {
	fw := netguardtest.New()
	fw.QuoteComments("iptables")
	apply(t, fw, netguard.Network{Bridge: "br-aaaaaaaaaaaa"}, true)
	apply(t, fw, netguard.Network{Bridge: "br-aaaaaaaaaaaa"}, true)
	if r := fw.Restores(); len(r) != 1 {
		t.Fatalf("the second apply changed the rules again: %q", r)
	}
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	want(t, fw, "iptables", "INPUT", append([]string{hostTCP, hostUDP}, ufwInput...)...)
}

func TestRemoveTakesOutEveryRuleOfTheGuards(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge, IPv6: true}, true)
	fw.Insert("iptables", "FORWARD", 3, "-i", "br-aaaaaaaaaaaa", "-d", "169.254.0.0/16", "-m", "comment", "--comment", netguard.Tag, "-j", "DROP")
	if err := netguard.Remove(context.Background(), fw.Run); err != nil {
		t.Fatal(err)
	}
	for _, fam := range []string{"iptables", "ip6tables"} {
		want(t, fw, fam, "INPUT", ufwInput...)
		want(t, fw, fam, "FORWARD", dockerJump...)
		want(t, fw, fam, "DOCKER-USER")
	}
}

func TestRemoveDoesWithoutIP6tables(t *testing.T) {
	fw := netguardtest.New()
	apply(t, fw, netguard.Network{Bridge: bridge}, true)
	fw.RemoveFamily("ip6tables")
	if err := netguard.Remove(context.Background(), fw.Run); err != nil {
		t.Fatal(err)
	}
	want(t, fw, "iptables", "INPUT", ufwInput...)
	want(t, fw, "iptables", "DOCKER-USER")
}

func TestExecSaysACommandIsMissing(t *testing.T) {
	_, err := netguard.Exec(context.Background(), "", "playkeeper-test-no-such-iptables", "-S")
	if err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("got %v", err)
	}
}
