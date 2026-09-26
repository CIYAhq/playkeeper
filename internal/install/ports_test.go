package install

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/config"
)

func TestTheAgentMayOpenPort80AndNothingMore(t *testing.T) {
	u := agentUnit()
	if !strings.Contains(u, "\nCapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE\n") {
		t.Fatalf("the agent needs CAP_NET_BIND_SERVICE for Let's Encrypt's checks:\n%s", u)
	}
	for _, never := range []string{"AmbientCapabilities", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_SYS_ADMIN"} {
		if strings.Contains(u, never) {
			t.Errorf("the agent unit grants %s", never)
		}
	}
	if p := panelUnit(8443); strings.Contains(p, "CAP_NET_BIND_SERVICE") {
		t.Fatalf("the panel on 8443 needs no capability:\n%s", p)
	}
}

func manifestOf(t *testing.T, h *fakeHost) Manifest {
	t.Helper()
	var m Manifest
	if err := json.Unmarshal([]byte(read(t, h, config.Default().ManifestPath())), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestActiveUFWAllowsPort80AndUninstallLeavesTheAdminsRules(t *testing.T) {
	h := newFakeHost(t)
	h.ufwActive = true
	h.ufwRules["80/tcp"] = true // the admin's own web server
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if c := check(f, "firewall"); c == nil || !strings.Contains(c.Detail, "allow 8443/tcp, 25565/tcp and 80/tcp") ||
		!strings.Contains(c.Detail, "cloud firewall") || !strings.Contains(c.Detail, "Let's Encrypt") {
		t.Fatalf("firewall check: %+v", c)
	}
	plan := strings.Join(Plan(f, o), "\n")
	for _, want := range []string{"ufw allow 8443/tcp, 25565/tcp and 80/tcp", "80/tcp only while Let's Encrypt checks your own domain"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan misses %q:\n%s", want, plan)
		}
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"8443/tcp", "25565/tcp", "80/tcp"} {
		if !h.ufwRules[rule] {
			t.Errorf("ufw does not allow %s", rule)
		}
	}
	if m := manifestOf(t, h); !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp"}) {
		t.Fatalf("the manifest must record only the rules the installer added: %v", m.FirewallRules)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if !h.ufwRules["80/tcp"] || !h.ufwRules["22/tcp"] || h.ufwRules["8443/tcp"] || h.ufwRules["25565/tcp"] {
		t.Fatalf("after uninstall ufw allows %v; want only the admin's own rules", h.ufwRules)
	}
}

func TestWithoutUFWTheProviderFirewallIsExplained(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if c := check(f, "firewall"); c == nil || !strings.Contains(c.Detail, "allow 8443/tcp, 25565/tcp and 80/tcp there") || !strings.Contains(c.Detail, "nothing answers on it otherwise") {
		t.Fatalf("firewall check: %+v", c)
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range h.cmds {
		if strings.HasPrefix(cmd, "ufw ") && cmd != "ufw status" {
			t.Fatalf("an inactive ufw was changed: %q", cmd)
		}
	}
	if m := manifestOf(t, h); len(m.FirewallRules) != 0 {
		t.Fatalf("firewall rules recorded without ufw: %v", m.FirewallRules)
	}
}

func TestUpgradeAllowsPort80InAnActiveUFWOnce(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.3.0", true)
	h.ufwActive = true
	h.ufwRules["8443/tcp"], h.ufwRules["25565/tcp"] = true, true
	path := filepath.Join(h.root, cfg.ManifestPath())
	var m Manifest
	if err := readJSONFile(path, &m); err != nil {
		t.Fatal(err)
	}
	m.FirewallRules = []string{"8443/tcp", "25565/tcp"}
	if err := writeJSONFile(path, m); err != nil {
		t.Fatal(err)
	}
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
	out := upgrade("0.4.0")
	if !strings.Contains(out, "Firewall:  ufw allow 80/tcp once 0.4.0 is running.") {
		t.Fatalf("the plan must say port 80 is allowed:\n%s", out)
	}
	started, allowed := slices.Index(h.cmds, "systemctl start playkeeper-agent.service playkeeper-panel.service"), slices.Index(h.cmds, "ufw allow 80/tcp")
	if allowed < 0 || allowed < started {
		t.Fatalf("port 80 must be allowed after the new version started: %v", h.cmds)
	}
	if m := manifestOf(t, h); !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp", "80/tcp"}) || m.Version != "0.4.0" {
		t.Fatalf("manifest after the upgrade: %+v", m)
	}
	h.cmds = nil
	if out := upgrade("0.4.1"); strings.Contains(out, "ufw") {
		t.Fatalf("the next upgrade mentions ufw again:\n%s", out)
	}
	for _, cmd := range h.cmds {
		if strings.HasPrefix(cmd, "ufw") {
			t.Fatalf("the next upgrade ran %q", cmd)
		}
	}
}
