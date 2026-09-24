package install

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Docker changes the host network when its daemon starts: it turns on IP
// forwarding, sets the iptables FORWARD policy to DROP, adds its DOCKER chains
// and NAT rules, and creates bridges (docker0, br-<id>). When Playkeeper
// removes a Docker it installed, it puts all of that back.

// NetSettings are the host settings Docker changes, recorded just before
// Playkeeper installs it.
type NetSettings struct {
	IPv4Forward    string `json:"ipv4Forward"`
	IPv6Forward    string `json:"ipv6Forward"`
	ForwardPolicy  string `json:"forwardPolicy"`
	Forward6Policy string `json:"forward6Policy"`
}

type ipFamily struct {
	name, iptables, save, sysctl string
}

var ipFamilies = []ipFamily{
	{"IPv4", "iptables", "iptables-save", "/proc/sys/net/ipv4/ip_forward"},
	{"IPv6", "ip6tables", "ip6tables-save", "/proc/sys/net/ipv6/conf/all/forwarding"},
}

func (n NetSettings) forward(f ipFamily) string {
	if f.name == "IPv4" {
		return n.IPv4Forward
	}
	return n.IPv6Forward
}

func (n NetSettings) policy(f ipFamily) string {
	if f.name == "IPv4" {
		return n.ForwardPolicy
	}
	return n.Forward6Policy
}

func readNetSettings(sys System) NetSettings {
	return NetSettings{
		IPv4Forward: readSysctl(sys, ipFamilies[0].sysctl), IPv6Forward: readSysctl(sys, ipFamilies[1].sysctl),
		ForwardPolicy: forwardPolicy(sys, "iptables"), Forward6Policy: forwardPolicy(sys, "ip6tables"),
	}
}

func readSysctl(sys System, path string) string {
	b, err := os.ReadFile(sys.P(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// forwardPolicy returns the filter table's FORWARD policy. Without iptables
// installed nothing is filtered, which is ACCEPT.
func forwardPolicy(sys System, bin string) string {
	out, err := sys.Run(bin, "-S", "FORWARD")
	if err != nil {
		return "ACCEPT"
	}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "-P" && f[1] == "FORWARD" {
			return f[2]
		}
	}
	return "ACCEPT"
}

// revertDockerNetwork removes what a stopped Docker left in the host network
// and puts forwarding and the FORWARD policy back as they were. It returns
// what it could not undo, each with the command to finish by hand.
func revertDockerNetwork(sys System, before NetSettings) []string {
	var left []string
	for _, f := range ipFamilies {
		// Forwarding goes back off before the FORWARD policy opens, so the
		// host never forwards traffic with nothing filtering it.
		if want, cur := before.forward(f), readSysctl(sys, f.sysctl); want != "" && cur != "" && cur != want {
			if err := os.WriteFile(sys.P(f.sysctl), []byte(want+"\n"), 0o644); err != nil {
				left = append(left, fmt.Sprintf("%s forwarding is still %s (it was %s before the install): sudo sh -c 'echo %s > %s'", f.name, cur, want, want, f.sysctl))
			}
		}
		save, err := sys.Run(f.save)
		if err != nil {
			continue
		}
		chains, rules := dockerRules(save)
		for _, r := range rules {
			_, _ = sys.Run(f.iptables, append([]string{"-t", r.table, "-D", r.chain}, r.args...)...)
		}
		for _, c := range chains {
			_, _ = sys.Run(f.iptables, "-t", c.table, "-F", c.chain)
		}
		for _, c := range chains {
			_, _ = sys.Run(f.iptables, "-t", c.table, "-X", c.chain)
		}
		if want := before.policy(f); want != "" && forwardPolicy(sys, f.iptables) != want {
			_, _ = sys.Run(f.iptables, "-P", "FORWARD", want)
		}
		if save, err := sys.Run(f.save); err == nil {
			chains, rules := dockerRules(save)
			for _, r := range rules {
				left = append(left, fmt.Sprintf("%s rule left in %s/%s: sudo %s -t %s -D %s %s", f.iptables, r.table, r.chain, f.iptables, r.table, r.chain, strings.Join(r.args, " ")))
			}
			for _, c := range chains {
				left = append(left, fmt.Sprintf("%s chain %s left in the %s table: sudo %s -t %s -F %s && sudo %s -t %s -X %s", f.iptables, c.chain, c.table, f.iptables, c.table, c.chain, f.iptables, c.table, c.chain))
			}
		}
		if want := before.policy(f); want != "" {
			if cur := forwardPolicy(sys, f.iptables); cur != want {
				left = append(left, fmt.Sprintf("%s FORWARD policy is still %s (it was %s before the install): sudo %s -P FORWARD %s", f.iptables, cur, want, f.iptables, want))
			}
		}
	}
	for _, name := range dockerBridges(sys) {
		if _, err := sys.Run("ip", "link", "delete", name); err != nil {
			left = append(left, fmt.Sprintf("Docker's %s network interface is still there: sudo ip link delete %s", name, name))
		}
	}
	return left
}

type ipRule struct {
	table, chain string
	args         []string
}

var builtinChains = map[string]bool{"PREROUTING": true, "INPUT": true, "FORWARD": true, "OUTPUT": true, "POSTROUTING": true}

var dockerIface = regexp.MustCompile(`^(docker0|br-[0-9a-f]{12})$`)

// dockerRules finds in iptables-save output Docker's own chains, and the
// rules it added to the built-in chains: jumps to its chains and rules for
// its bridges. Rules in other chains (ufw's, for example) are not Docker's.
func dockerRules(save string) (chains, rules []ipRule) {
	table := ""
	for _, line := range strings.Split(save, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*"):
			table = line[1:]
		case strings.HasPrefix(line, ":"):
			if f := strings.Fields(line[1:]); len(f) > 0 && strings.HasPrefix(f[0], "DOCKER") {
				chains = append(chains, ipRule{table: table, chain: f[0]})
			}
		case strings.HasPrefix(line, "-A "):
			args := splitRule(line[3:])
			if len(args) > 1 && builtinChains[args[0]] && isDockerRule(args[1:]) {
				rules = append(rules, ipRule{table: table, chain: args[0], args: args[1:]})
			}
		}
	}
	return chains, rules
}

func isDockerRule(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-j", "-g":
			if strings.HasPrefix(args[i+1], "DOCKER") {
				return true
			}
		case "-i", "-o":
			if dockerIface.MatchString(args[i+1]) {
				return true
			}
		}
	}
	return false
}

// splitRule splits an iptables-save rule into arguments; iptables-save puts
// double quotes around arguments that contain spaces.
func splitRule(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			quoted, have = !quoted, true
		case c == ' ' && !quoted:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func dockerBridges(sys System) []string {
	entries, _ := os.ReadDir(sys.P("/sys/class/net"))
	var out []string
	for _, e := range entries {
		if dockerIface.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
