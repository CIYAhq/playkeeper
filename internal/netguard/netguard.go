// Package netguard keeps game servers from reaching the machine they run on
// and the cloud's metadata service. Docker already keeps one server's
// container from another's (Playkeeper's network has inter-container traffic
// off), but a container can still open connections to every port the machine
// listens on, through the bridge's gateway or the machine's own addresses,
// even ones a cloud firewall closes to the internet, since that traffic
// never leaves the machine. It can also reach the metadata service clouds
// answer at 169.254.169.254, which can hand out the machine's cloud-init
// data.
//
// So netguard puts firewall rules for Playkeeper's bridge in place, each
// tagged with Tag so it can be found again:
//
//   - at the top of INPUT, rejects for TCP and UDP from the bridge to the
//     machine that aren't replies to connections the machine opened (the
//     agent's RCON, status pings and map), in iptables and, when the network
//     has IPv6, in ip6tables;
//   - at the top of DOCKER-USER, or of FORWARD when Docker has no such chain,
//     a reject for TCP to link-local addresses (169.254.0.0/16).
//
// Both leave DNS alone: Google Cloud's and Oracle Cloud's resolver is the
// metadata address itself, and the resolver Docker gives each container asks
// the machine's resolvers, one of which may run on the machine. ICMP is left
// alone too, since IPv6 needs it to find its neighbours.
//
// The rules must come first in their chains: ufw's rules for open ports, and
// Docker's own, accept traffic before a rule further down sees it. Docker,
// ufw and firewalld can drop or bury the rules when they reload theirs, so
// the agent calls Apply again every minute; while the rules are in place,
// Apply changes nothing.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Tag is the comment on each of the guard's rules.
const Tag = "playkeeper-guard"

// Runner runs name (iptables, ip6tables or their -restore) with args, giving
// it input on its standard input, and returns what it printed.
type Runner func(ctx context.Context, input, name string, args ...string) (string, error)

// Exec runs the commands themselves.
func Exec(ctx context.Context, input, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdin = strings.NewReader(input)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("the %s command isn't installed", name)
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// Network is Playkeeper's Docker network, as the rules need it.
type Network struct {
	// Bridge is the machine's interface for the network: br- and the start
	// of the network's ID, unless it was given a name.
	Bridge string
	// IPv6 is whether its containers have IPv6 addresses too.
	IPv6 bool
}

// ifname is an interface name (at most 15 bytes) made only of characters
// that stay one word in a rule.
var ifname = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

type family struct{ cmd, restore string }

var (
	ipv4 = family{"iptables", "iptables-restore"}
	ipv6 = family{"ip6tables", "ip6tables-restore"}
)

// hostRules keep the bridge's containers from opening TCP and UDP
// connections to the machine, except for DNS.
func hostRules(bridge string) [][]string {
	var out [][]string
	for _, proto := range []string{"tcp", "udp"} {
		r := []string{"-i", bridge, "-p", proto, "!", "--dport", "53", "-m", "conntrack", "!", "--ctstate", "ESTABLISHED,RELATED", "-m", "comment", "--comment", Tag, "-j", "REJECT"}
		if proto == "tcp" {
			r = append(r, "--reject-with", "tcp-reset")
		}
		out = append(out, r)
	}
	return out
}

// metadataRule keeps them from TCP to link-local addresses, except for DNS.
func metadataRule(bridge string) []string {
	return []string{"-i", bridge, "-d", "169.254.0.0/16", "-p", "tcp", "!", "--dport", "53", "-m", "comment", "--comment", Tag, "-j", "REJECT", "--reject-with", "tcp-reset"}
}

// Apply puts the rules for n in place, or back at the top of their chains,
// and takes out the guard's other rules: ones for an earlier bridge, and the
// ones for the machine itself when host is false, which lets servers reach
// it. The metadata rule is there either way.
func Apply(ctx context.Context, run Runner, n Network, host bool) error {
	if !ifname.MatchString(n.Bridge) {
		return fmt.Errorf("Docker gave no usable interface name for Playkeeper's network (%q)", n.Bridge)
	}
	var in [][]string
	if host {
		in = hostRules(n.Bridge)
	}
	t, err := list(ctx, run, ipv4)
	if err != nil {
		return err
	}
	want := map[string][][]string{"INPUT": in}
	if t.chains["DOCKER-USER"] {
		want["DOCKER-USER"] = [][]string{metadataRule(n.Bridge)}
	} else {
		want["FORWARD"] = [][]string{metadataRule(n.Bridge)}
	}
	if err := t.fix(ctx, run, ipv4, want); err != nil {
		return err
	}
	if !n.IPv6 {
		return nil
	}
	if t, err = list(ctx, run, ipv6); err != nil {
		return err
	}
	return t.fix(ctx, run, ipv6, map[string][][]string{"INPUT": in})
}

// Remove takes out each of the guard's rules, whichever bridge it is for, as
// Playkeeper's uninstall does. A family whose command is missing or doesn't
// work, as ip6tables where IPv6 is off, has none.
func Remove(ctx context.Context, run Runner) error {
	var errs []error
	for _, f := range []family{ipv4, ipv6} {
		t, err := list(ctx, run, f)
		if err != nil {
			continue
		}
		if err := t.fix(ctx, run, f, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// table is the filter table: its chains, and each chain's rules in order, as
// iptables -S prints them.
type table struct {
	chains map[string]bool
	rules  map[string][]string
}

func list(ctx context.Context, run Runner, f family) (table, error) {
	out, err := run(ctx, "", f.cmd, "-w", "5", "-S")
	if err != nil {
		return table{}, err
	}
	t := table{chains: map[string]bool{}, rules: map[string][]string{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch w := strings.Fields(line); {
		case len(w) < 2:
		case w[0] == "-P" || w[0] == "-N":
			t.chains[w[1]] = true
		case w[0] == "-A":
			t.rules[w[1]] = append(t.rules[w[1]], line)
		}
	}
	return t, nil
}

// chains are the chains the guard's rules go in.
var chains = []string{"INPUT", "FORWARD", "DOCKER-USER"}

// fix makes want[chain] the first rules of each chain and the only ones of
// the guard's in it, leaving alone a chain that already is so. It changes
// the others in one iptables-restore, so there is no moment without the
// rules, and never declares a chain there, which would empty it.
func (t table) fix(ctx context.Context, run Runner, f family, want map[string][][]string) error {
	var script []string
	for _, chain := range chains {
		w := want[chain]
		var ours []string
		top := true
		for i, r := range t.rules[chain] {
			if tagged(r) {
				ours = append(ours, r)
				top = top && i < len(w)
			}
		}
		if len(ours) == len(w) && top && present(ctx, run, f, chain, w) {
			continue
		}
		for _, r := range ours {
			script = append(script, "-D"+strings.TrimPrefix(r, "-A"))
		}
		for i := len(w) - 1; i >= 0; i-- {
			script = append(script, "-I "+chain+" 1 "+strings.Join(w[i], " "))
		}
	}
	if len(script) == 0 {
		return nil
	}
	_, err := run(ctx, "*filter\n"+strings.Join(script, "\n")+"\nCOMMIT\n", f.restore, "-w", "5", "--noflush")
	return err
}

// present reports whether each of rules is in chain.
func present(ctx context.Context, run Runner, f family, chain string, rules [][]string) bool {
	for _, r := range rules {
		if _, err := run(ctx, "", f.cmd, append([]string{"-w", "5", "-C", chain}, r...)...); err != nil {
			return false
		}
	}
	return true
}

// tagged reports whether a rule iptables -S printed is one of the guard's.
func tagged(rule string) bool {
	w := strings.Fields(rule)
	for i := 0; i+1 < len(w); i++ {
		if w[i] == "--comment" && strings.Trim(w[i+1], `"`) == Tag {
			return true
		}
	}
	return false
}
