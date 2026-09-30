package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/config"
)

// hostFirewall is a firewall on this machine that the installer opens
// Playkeeper's ports in: ufw, or firewalld as the RHEL family and Oracle
// Cloud's images run it.
type hostFirewall interface {
	// kind is what the manifest records: "" for ufw, which manifests from
	// before firewalld don't name.
	kind() string
	// short names it: "ufw" or "firewalld".
	short() string
	// where says where its rules go, after a list of them: "", or " in its
	// public zone".
	where() string
	// planLine is the plan's line for allowing ports, a list of rules.
	planLine(ports string) string
	// record notes in m which firewall the rules go in, and what uninstall
	// needs to put it back as it was; the install calls it before its first
	// change.
	record(sys System, m *Manifest)
	// allow allows rule, like "8443/tcp", and reports whether that added
	// it: a rule that was already there is the admin's, so uninstall must
	// leave it.
	allow(sys System, rule string) (added bool, err error)
	remove(sys System, rule string) error
	// tidy, once the install's rules are removed, removes what the firewall
	// kept of them.
	tidy(sys System, m Manifest) error
}

// httpsRuleDue is the firewall an upgrade allows port 443 in, as a new
// install does: the one that filters this machine's traffic, while the
// install doesn't have the rule and runs a dashboard, and only the one its
// other rules are in. nil when there's nothing to do.
func httpsRuleDue(sys System, cfg config.Config, m Manifest) hostFirewall {
	if cfg.NoPanel || contains(m.FirewallRules, httpsRule) {
		return nil
	}
	fw := activeFirewall(sys)
	if fw == nil || len(m.FirewallRules) > 0 && fw.kind() != firewallOf(m).kind() {
		return nil
	}
	return fw
}

// allowHTTPSPort allows port 443 after an upgrade from a version that did
// not use it, in the firewall httpsRuleDue names, and records the rule for
// uninstall if that added it. The upgrade has succeeded either way, so a
// failure is reported, not returned.
func allowHTTPSPort(sys System, cfg config.Config, out io.Writer) {
	path := sys.P(cfg.ManifestPath())
	var m Manifest
	if err := readJSONFile(path, &m); err != nil {
		return
	}
	fw := httpsRuleDue(sys, cfg, m)
	if fw == nil {
		return
	}
	fmt.Fprintf(out, "  • allow port 443 in %s%s, for the dashboard without a port and the public server page\n", fw.short(), fw.where())
	if len(m.FirewallRules) == 0 {
		fw.record(sys, &m)
	}
	added, err := fw.allow(sys, httpsRule)
	if err != nil {
		fmt.Fprintf(out, "  ! could not allow port 443 in %s (%v). To serve the dashboard without a port, allow %s there\n", fw.short(), err, httpsRule)
		return
	}
	if !added {
		return
	}
	m.FirewallRules = append(m.FirewallRules, httpsRule)
	if err := writeJSONFile(path, m); err != nil {
		fmt.Fprintf(out, "  ! port 443 is allowed in %s, but the install manifest could not record it (%v), so uninstall will leave the rule\n", fw.short(), err)
	}
}

// activeFirewall is the firewall that filters this machine's traffic, if
// ufw or firewalld does.
func activeFirewall(sys System) hostFirewall {
	if ufwActive(sys) {
		return ufw{}
	}
	if firewalldActive(sys) {
		return firewalld{zone: firewalldZone(sys)}
	}
	return nil
}

// firewallOf is the firewall a manifest's rules are in.
func firewallOf(m Manifest) hostFirewall {
	if m.Firewall == (firewalld{}).kind() {
		return firewalld{zone: m.FirewallZone}
	}
	return ufw{}
}

type ufw struct{}

func (ufw) kind() string { return "" }

func (ufw) short() string { return "ufw" }

func (ufw) where() string { return "" }

func (ufw) planLine(ports string) string {
	return "Firewall:  ufw allow " + ports + " (rules that already exist stay yours)"
}

func (ufw) allow(sys System, rule string) (bool, error) { return ufwAllow(sys, rule) }

func (ufw) remove(sys System, rule string) error {
	_, err := sys.Run("ufw", "delete", "allow", rule)
	return err
}

func (ufw) record(System, *Manifest) {}

func (ufw) tidy(System, Manifest) error { return nil }

func ufwActive(sys System) bool {
	out, err := sys.Run("ufw", "status")
	return err == nil && strings.Contains(out, "Status: active")
}

// ufwAllow allows rule in ufw and reports whether that added it. A rule
// that was already there is the admin's, so uninstall must leave it.
func ufwAllow(sys System, rule string) (added bool, err error) {
	out, err := sys.Run("ufw", "allow", rule)
	if err != nil {
		return false, err
	}
	return !strings.Contains(out, "Skipping adding existing rule"), nil
}

// firewalld keeps its rules in zones; the installer's go in the zone of the
// interface that has the default route.
type firewalld struct{ zone string }

func (firewalld) kind() string { return "firewalld" }

func (firewalld) short() string { return "firewalld" }

func (f firewalld) where() string { return " in its " + f.zone + " zone" }

func (f firewalld) planLine(ports string) string {
	return "Firewall:  firewalld: allow " + ports + " in the " + f.zone + " zone, saved and running (rules that already exist stay yours)"
}

// allow adds rule to the zone's permanent configuration, which survives a
// reboot, and to the running one.
func (f firewalld) allow(sys System, rule string) (bool, error) {
	zone := "--zone=" + f.zone
	out, _ := sys.Run("firewall-cmd", "--permanent", zone, "--query-port="+rule)
	added := strings.TrimSpace(out) != "yes"
	if added {
		if _, err := sys.Run("firewall-cmd", "--permanent", zone, "--add-port="+rule); err != nil {
			return false, err
		}
	}
	if out, _ := sys.Run("firewall-cmd", zone, "--query-port="+rule); strings.TrimSpace(out) != "yes" {
		if _, err := sys.Run("firewall-cmd", zone, "--add-port="+rule); err != nil {
			return added, err
		}
	}
	return added, nil
}

func (f firewalld) remove(sys System, rule string) error {
	zone := "--zone=" + f.zone
	_, perr := sys.Run("firewall-cmd", "--permanent", zone, "--remove-port="+rule)
	_, rerr := sys.Run("firewall-cmd", zone, "--remove-port="+rule)
	return errors.Join(perr, rerr)
}

// file is where firewalld saves the zone once it is changed, and keeps
// the version before each change as .old.
func (f firewalld) file() string { return "/etc/firewalld/zones/" + f.zone + ".xml" }

// record notes which of the zone's two files firewalld doesn't have yet, and
// its saved settings.
func (f firewalld) record(sys System, m *Manifest) {
	m.Firewall, m.FirewallZone = f.kind(), f.zone
	for _, p := range []string{f.file(), f.file() + ".old"} {
		if _, err := os.Stat(sys.P(p)); errors.Is(err, os.ErrNotExist) {
			m.FirewallZoneFiles = append(m.FirewallZoneFiles, p)
		}
	}
	m.FirewallZoneBefore, _ = sys.Run("firewall-cmd", "--permanent", "--zone="+f.zone, "--list-all")
}

// tidy removes the zone's backup firewalld made, and its own copy of the
// zone when there wasn't one and the zone is back to what it was: then
// firewalld reads the zone from its defaults again, as before the install.
func (f firewalld) tidy(sys System, m Manifest) error {
	var errs []error
	if _, err := os.Stat(sys.P(f.file())); err == nil && slices.Contains(m.FirewallZoneFiles, f.file()) {
		if now, err := sys.Run("firewall-cmd", "--permanent", "--zone="+f.zone, "--list-all"); err == nil && now == m.FirewallZoneBefore {
			_, err := sys.Run("firewall-cmd", "--permanent", "--load-zone-defaults="+f.zone)
			errs = append(errs, err)
		}
	}
	if slices.Contains(m.FirewallZoneFiles, f.file()+".old") {
		errs = append(errs, removeIfExists(sys.P(f.file()+".old")))
	}
	return errors.Join(errs...)
}

func firewalldActive(sys System) bool {
	if _, err := os.Stat(sys.P("/usr/bin/firewall-cmd")); err != nil {
		return false
	}
	out, err := sys.Run("firewall-cmd", "--state")
	return err == nil && strings.TrimSpace(out) == "running"
}

// firewalldZone is the zone of the interface with the default route, or
// firewalld's default zone, which takes interfaces no zone names.
func firewalldZone(sys System) string {
	if iface := defaultRouteInterface(sys); iface != "" {
		if out, err := sys.Run("firewall-cmd", "--get-zone-of-interface="+iface); err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out)
		}
	}
	if out, err := sys.Run("firewall-cmd", "--get-default-zone"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return "public"
}

// defaultRouteInterface reads the IPv4 routing table for the interface of
// the default route.
func defaultRouteInterface(sys System) string {
	b, err := os.ReadFile(sys.P("/proc/net/route"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		if f := strings.Fields(line); len(f) > 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// dockerFirewalld are what Docker adds to firewalld when it starts while
// firewalld runs: a zone for the bridges it makes, and a policy that lets
// traffic in to them.
var dockerFirewalld = []string{"zone docker", "policy docker-forwarding"}

// firewalldHas reports which of Docker's zone and policy firewalld has,
// running or saved.
func firewalldHas(sys System) map[string]bool {
	have := map[string]bool{}
	for _, scope := range [][]string{nil, {"--permanent"}} {
		for _, kind := range []string{"zone", "policy"} {
			plural := map[string]string{"zone": "--get-zones", "policy": "--get-policies"}[kind]
			out, err := sys.Run("firewall-cmd", append(slices.Clone(scope), plural)...)
			if err != nil {
				continue
			}
			for _, name := range strings.Fields(out) {
				have[kind+" "+name] = true
			}
		}
	}
	return have
}

// removeFirewalld deletes what Docker added to firewalld, once Docker is
// gone, and reloads firewalld so the running configuration drops it too.
// firewalld keeps a deleted zone's or policy's file as .xml.old; it goes too.
func removeFirewalld(sys System, added []string) error {
	if len(added) == 0 {
		return nil
	}
	var errs []error
	for _, o := range added {
		kind, name, _ := strings.Cut(o, " ")
		out, err := sys.Run("firewall-cmd", "--permanent", "--delete-"+kind+"="+name)
		// Docker may have added it to the running configuration only.
		if err != nil && !strings.Contains(out+err.Error(), "INVALID_"+strings.ToUpper(kind)) {
			errs = append(errs, err)
		}
		dir := map[string]string{"zone": "/etc/firewalld/zones", "policy": "/etc/firewalld/policies"}[kind]
		errs = append(errs, removeIfExists(sys.P(dir+"/"+name+".xml.old")))
	}
	if _, err := sys.Run("firewall-cmd", "--reload"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// dropFirewall finds a firewall other than ufw that drops incoming
// connections unless a rule allows them: an nftables input chain, Debian's
// own firewall, or an iptables INPUT chain with a DROP policy. The
// installer opens ports only in ufw and firewalld, so it names the firewall
// and a command that allows ports in it; name is "" when there is none.
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
