package install

import (
	"fmt"
	"strings"
)

// dropFirewall finds a firewall other than ufw that drops incoming
// connections unless a rule allows them: an nftables input chain, Debian's
// own firewall, or an iptables INPUT chain with a DROP policy. The
// installer opens ports only in ufw, so it names the firewall and a command
// that allows ports in it; name is "" when there is none.
func dropFirewall(sys System, ports []string) (name, allow string) {
	var nums []string
	for _, p := range ports {
		nums = append(nums, strings.TrimSuffix(p, "/tcp"))
	}
	if out, err := sys.Run("nft", "list", "chains"); err == nil {
		if family, table, chain := nftInputDrop(out); chain != "" {
			return "nftables", fmt.Sprintf("sudo nft insert rule %s %s %s tcp dport '{ %s }' accept", family, table, chain, strings.Join(nums, ", "))
		}
	}
	for _, bin := range []string{"iptables", "ip6tables"} {
		out, err := sys.Run(bin, "-S", "INPUT")
		if err != nil {
			continue
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) == "-P INPUT DROP" {
				return "iptables", fmt.Sprintf("sudo %s -I INPUT -p tcp -m multiport --dports %s -j ACCEPT", bin, strings.Join(nums, ","))
			}
		}
	}
	return "", ""
}

// nftInputDrop finds, in `nft list chains` output, a chain hooked into
// input whose policy is drop. Chains named INPUT are iptables' own, made
// through its nftables backend; dropFirewall asks iptables about those.
func nftInputDrop(out string) (family, table, chain string) {
	var curFamily, curTable, curChain string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 3 && f[0] == "table":
			curFamily, curTable, curChain = f[1], f[2], ""
		case len(f) >= 2 && f[0] == "chain":
			curChain = f[1]
		case curChain != "" && curChain != "INPUT" && strings.Contains(line, "hook input") && strings.Contains(line, "policy drop"):
			return curFamily, curTable, curChain
		}
	}
	return "", "", ""
}
