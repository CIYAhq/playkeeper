package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/config"
)

// Oracle Cloud's Ubuntu images: SSH gets in, and the last rule rejects
// everything else, saved for iptables-persistent.
const oracleRulesV4 = `*raw
:PREROUTING ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
COMMIT
*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:InstanceServices - [0:0]
-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
-A INPUT -p icmp -j ACCEPT
-A INPUT -i lo -j ACCEPT
-A INPUT -p udp -m udp --sport 123 -j ACCEPT
-A INPUT -p tcp -m state --state NEW -m tcp --dport 22 -j ACCEPT
-A INPUT -j REJECT --reject-with icmp-host-prohibited
-A FORWARD -j REJECT --reject-with icmp-host-prohibited
-A OUTPUT -d 169.254.0.0/16 -j InstanceServices
COMMIT
*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
COMMIT
`

const oracleSavedV4 = `# CLOUD_IMG: This file was created/modified by the Cloud Image build process
# iptables configuration for Oracle Cloud Infrastructure

*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [463:49013]
:InstanceServices - [0:0]
-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
-A INPUT -p icmp -j ACCEPT
-A INPUT -i lo -j ACCEPT
-A INPUT -p udp --sport 123 -j ACCEPT
-A INPUT -p tcp -m state --state NEW -m tcp --dport 22 -j ACCEPT
-A INPUT -j REJECT --reject-with icmp-host-prohibited
-A FORWARD -j REJECT --reject-with icmp-host-prohibited
-A OUTPUT -d 169.254.0.0/16 -j InstanceServices
COMMIT
`

func newOracleHost(t *testing.T) *fakeHost {
	t.Helper()
	h := newFakeHost(t)
	h.fw4 = parseSave(oracleRulesV4)
	p := filepath.Join(h.root, "/etc/iptables/rules.v4")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(oracleSavedV4), 0o640); err != nil {
		t.Fatal(err)
	}
	return h
}

func inputRules(h *fakeHost) []string {
	return h.fw4.table("filter", false).chain("INPUT").rules
}

const ourRules = `-p tcp -m tcp --dport 8443 -m comment --comment playkeeper -j ACCEPT
-p tcp -m tcp --dport 25565 -m comment --comment playkeeper -j ACCEPT
-p tcp -m tcp --dport 443 -m comment --comment playkeeper -j ACCEPT
-p tcp -m tcp --dport 80 -m comment --comment playkeeper -j ACCEPT`

// On Oracle Cloud's Ubuntu the check said no firewall blocked anything, and
// the dashboard was out of reach. The installer now puts its ports right
// before the rule that rejects everything else, running and saved, and the
// uninstall takes them out again.
func TestOracleCloudsIptablesLetsThePortsInBeforeItsLastRule(t *testing.T) {
	h := newOracleHost(t)
	before := slices.Clone(inputRules(h))
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if c := check(f, "firewall"); c == nil || c.Label != "Firewall (iptables)" || c.Status != "info" ||
		!strings.HasPrefix(c.Detail, "iptables is active; the installer will allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp before its last rule, which rejects everything else.") {
		t.Fatalf("firewall check: %+v", c)
	}
	if plan := strings.Join(Plan(f, o), "\n"); !strings.Contains(plan, "Firewall:  iptables: allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp before its last rule, which rejects everything else, running and in /etc/iptables/rules.v4") {
		t.Errorf("the plan:\n%s", plan)
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	want := slices.Concat(before[:len(before)-1], strings.Split(ourRules, "\n"), before[len(before)-1:])
	if got := inputRules(h); !slices.Equal(got, want) {
		t.Errorf("INPUT after the install:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	saved := read(t, h, "/etc/iptables/rules.v4")
	var lines []string
	for _, r := range strings.Split(ourRules, "\n") {
		lines = append(lines, "-A INPUT "+r)
	}
	wantSaved := strings.Replace(oracleSavedV4, "-A INPUT -j REJECT", strings.Join(lines, "\n")+"\n-A INPUT -j REJECT", 1)
	if saved != wantSaved {
		t.Errorf("the saved rules after the install:\n%s", saved)
	}
	if m := manifestOf(t, h); m.Firewall != "iptables" || !slices.Equal(m.FirewallFamilies, []string{"iptables"}) || !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp", "443/tcp", "80/tcp"}) {
		t.Errorf("manifest: firewall %q, families %v, rules %v", m.Firewall, m.FirewallFamilies, m.FirewallRules)
	}

	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if got := inputRules(h); !slices.Equal(got, before) {
		t.Errorf("INPUT after the uninstall:\n%s", strings.Join(got, "\n"))
	}
	if saved := read(t, h, "/etc/iptables/rules.v4"); saved != oracleSavedV4 {
		t.Errorf("the saved rules after the uninstall:\n%s", saved)
	}
}

func TestAFailedInstallTakesItsIptablesRulesBack(t *testing.T) {
	h := newOracleHost(t)
	before := slices.Clone(inputRules(h))
	o := opts("")
	o.Yes = true
	t.Setenv(FailStepEnv, "write install manifest")
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil {
		t.Fatal("the injected failure didn't stop the install")
	}
	if got := inputRules(h); !slices.Equal(got, before) {
		t.Errorf("INPUT after the rollback:\n%s", strings.Join(got, "\n"))
	}
	if saved := read(t, h, "/etc/iptables/rules.v4"); saved != oracleSavedV4 {
		t.Errorf("the saved rules after the rollback:\n%s", saved)
	}
}

// A rule the admin already saved is theirs: the install doesn't add it twice,
// and the uninstall leaves it.
func TestTheAdminsOwnSavedRuleStays(t *testing.T) {
	h := newOracleHost(t)
	spec := "-p tcp -m tcp --dport 8443 -m comment --comment playkeeper -j ACCEPT"
	rules := h.fw4.table("filter", false).chain("INPUT")
	rules.rules = slices.Insert(rules.rules, len(rules.rules)-1, spec)
	theirs := strings.Replace(oracleSavedV4, "-A INPUT -j REJECT", "-A INPUT "+spec+"\n-A INPUT -j REJECT", 1)
	if err := os.WriteFile(filepath.Join(h.root, "/etc/iptables/rules.v4"), []byte(theirs), 0o640); err != nil {
		t.Fatal(err)
	}
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	if m := manifestOf(t, h); slices.Contains(m.FirewallRules, "8443/tcp") {
		t.Errorf("the admin's rule went in the manifest: %v", m.FirewallRules)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if saved := read(t, h, "/etc/iptables/rules.v4"); saved != theirs {
		t.Errorf("the uninstall changed the admin's saved rules:\n%s", saved)
	}
	if !slices.Contains(inputRules(h), spec) {
		t.Error("the uninstall took the admin's running rule out")
	}
}

// An install from a version that didn't know Oracle Cloud's iptables has no
// rules there, so its dashboard stayed out of reach. Running the installer
// again, or an update from the dashboard, lets every one of the install's
// ports in once the new version runs, as a new install does, and once only;
// uninstall takes them out.
func TestAnUpgradeLetsThePortsInWhereAnEarlierInstallDidnt(t *testing.T) {
	theRulesAreIn := func(t *testing.T, h *fakeHost, before []string) {
		t.Helper()
		want := slices.Concat(before[:len(before)-1], strings.Split(ourRules, "\n"), before[len(before)-1:])
		if got := inputRules(h); !slices.Equal(got, want) {
			t.Errorf("INPUT:\n%s", strings.Join(got, "\n"))
		}
		if saved := read(t, h, "/etc/iptables/rules.v4"); !strings.Contains(saved, "-A INPUT -p tcp -m tcp --dport 80 -m comment --comment playkeeper -j ACCEPT\n-A INPUT -j REJECT") {
			t.Errorf("the saved rules:\n%s", saved)
		}
		if m := manifestOf(t, h); m.Firewall != "iptables" || !slices.Equal(m.FirewallFamilies, []string{"iptables"}) || !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp", "443/tcp", "80/tcp"}) {
			t.Errorf("manifest: firewall %q, families %v, rules %v", m.Firewall, m.FirewallFamilies, m.FirewallRules)
		}
	}

	t.Run("running the installer again", func(t *testing.T) {
		h := newOracleHost(t)
		before := slices.Clone(inputRules(h))
		installedAt(t, h, "0.4.15", true)
		sys := h.system(t)
		upgrade := func(version string) string {
			t.Helper()
			bin := newBinary(t, version)
			sys.Executable = func() (string, error) { return bin, nil }
			o := opts("")
			o.Yes = true
			if _, err := Run(context.Background(), sys, o, version); err != nil {
				t.Fatalf("upgrade to %s: %v\n%s", version, err, o.Out)
			}
			return o.Out.(*bytes.Buffer).String()
		}
		out := upgrade("0.4.16")
		if !strings.Contains(out, "Firewall:  allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp in iptables before its last rule, which rejects everything else, once 0.4.16 is running.") {
			t.Errorf("the plan must say the ports are allowed:\n%s", out)
		}
		started := slices.Index(h.cmds, "systemctl start playkeeper-agent.service playkeeper-panel.service")
		if allowed := slices.IndexFunc(h.cmds, func(c string) bool { return strings.HasPrefix(c, "iptables -I INPUT") }); allowed < 0 || allowed < started {
			t.Errorf("the ports must be allowed once the new version started: %v", h.cmds)
		}
		theRulesAreIn(t, h, before)

		h.cmds = nil
		if out := upgrade("0.4.17"); strings.Contains(out, "Firewall:") || strings.Contains(out, "• allow") {
			t.Errorf("the next upgrade allows ports again:\n%s", out)
		}
		for _, cmd := range h.cmds {
			if strings.HasPrefix(cmd, "iptables -I") {
				t.Errorf("the next upgrade ran %q", cmd)
			}
		}

		if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
			t.Fatal(err)
		}
		if got := inputRules(h); !slices.Equal(got, before) {
			t.Errorf("INPUT after the uninstall:\n%s", strings.Join(got, "\n"))
		}
		if saved := read(t, h, "/etc/iptables/rules.v4"); saved != oracleSavedV4 {
			t.Errorf("the saved rules after the uninstall:\n%s", saved)
		}
	})
	update := func(t *testing.T, h *fakeHost, cfg config.Config) {
		t.Helper()
		s := stage(t, h, cfg, "0.4.16", "0.4.17")
		var out bytes.Buffer
		if err := SelfUpdate(context.Background(), h.system(t), cfg, "0.4.16", s.keys, &out); err != nil {
			t.Fatalf("update failed: %v\n%s", err, out.String())
		}
	}
	t.Run("an update from the dashboard", func(t *testing.T) {
		h := newOracleHost(t)
		before := slices.Clone(inputRules(h))
		update(t, h, installedAt(t, h, "0.4.16", true))
		theRulesAreIn(t, h, before)
	})
	t.Run("a joined machine", func(t *testing.T) {
		h := newOracleHost(t)
		cfg := installedAt(t, h, "0.4.16", true)
		cfg.NoPanel = true
		update(t, h, cfg)
		if m := manifestOf(t, h); m.Firewall != "iptables" || !slices.Equal(m.FirewallRules, []string{"25565/tcp"}) {
			t.Errorf("a joined machine: firewall %q, rules %v", m.Firewall, m.FirewallRules)
		}
	})
}

// Rules that reject everything but aren't saved for iptables-persistent
// come back as they were at the next boot, so the installer only says how
// to allow the ports, as for a DROP policy; it never says no firewall
// blocks anything.
func TestARejectAllRuleNobodySavedIsNamed(t *testing.T) {
	h := newFakeHost(t)
	h.fw4 = parseSave(oracleRulesV4)
	f := Preflight(context.Background(), h.system(t), opts(""))
	if f.Firewall != nil {
		t.Fatalf("an unsaved chain is a firewall the installer opens: %#v", f.Firewall)
	}
	if c := check(f, "firewall"); c == nil || c.Status != "warn" || c.Label != "Firewall (iptables)" || !strings.Contains(c.Fix, "sudo iptables -I INPUT -p tcp -m multiport --dports 8443,25565,443,80 -j ACCEPT") {
		t.Errorf("firewall check: %+v", c)
	}
}

func TestCatchAllAndSavedCatchAll(t *testing.T) {
	for _, c := range []struct {
		out   string
		place int
		ok    bool
	}{
		{"-P INPUT ACCEPT\n-A INPUT -i lo -j ACCEPT\n-A INPUT -j REJECT --reject-with icmp-host-prohibited\n", 2, true},
		{"-P INPUT ACCEPT\n-A INPUT -j DROP\n", 1, true},
		{"-P INPUT ACCEPT\n-A INPUT -j REJECT\n", 1, true},
		{"-P INPUT DROP\n-A INPUT -j REJECT\n", 0, false},
		{"-P INPUT ACCEPT\n-A INPUT -s 203.0.113.0/24 -j DROP\n", 0, false},
		{"-P INPUT ACCEPT\n-A INPUT -j ufw-user-input\n", 0, false},
		{"-P INPUT ACCEPT\n", 0, false},
	} {
		if place, ok := catchAll(c.out); place != c.place || ok != c.ok {
			t.Errorf("%q: got %d %v, want %d %v", c.out, place, ok, c.place, c.ok)
		}
	}
	if at := savedCatchAll(oracleSavedV4); at != 13 {
		t.Errorf("the saved rules' catch-all is on line %d", at)
	}
	if at := savedCatchAll("*nat\n-A INPUT -j DROP\nCOMMIT\n"); at != -1 {
		t.Errorf("a nat table's rule counts: line %d", at)
	}
}
