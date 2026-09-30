package install

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/docker"
)

func TestTheAgentMayOpenPort80AndChangeTheFirewallAndNothingMore(t *testing.T) {
	u := agentUnit()
	// CAP_NET_BIND_SERVICE for Let's Encrypt's checks, and CAP_NET_ADMIN,
	// CAP_NET_RAW and netlink for the iptables the network guard runs.
	if !strings.Contains(u, "\nCapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW\n") {
		t.Fatalf("the agent's capabilities:\n%s", u)
	}
	if !strings.Contains(u, "\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n") {
		t.Fatalf("the agent's socket families:\n%s", u)
	}
	for _, never := range []string{"AmbientCapabilities", "CAP_SYS_ADMIN", "CAP_SYS_MODULE", "CAP_SYS_PTRACE", "CAP_SYS_RAWIO", "CAP_BPF", "CAP_SETUID"} {
		if strings.Contains(u, never) {
			t.Errorf("the agent unit grants %s", never)
		}
	}
	if p := panelUnit(8443); strings.Contains(p, "CAP_NET_BIND_SERVICE") {
		t.Fatalf("the panel on 8443 needs no capability:\n%s", p)
	}
}

func TestThePlanSaysServersAreKeptFromTheMachine(t *testing.T) {
	h := newFakeHost(t)
	o := opts("")
	plan := strings.Join(Plan(Preflight(context.Background(), h.system(t), o), o), "\n")
	if !strings.Contains(plan, "iptables rules that keep servers from this machine and the cloud's metadata service") {
		t.Fatalf("the plan doesn't say what the network guard adds:\n%s", plan)
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
	if c := check(f, "firewall"); c == nil || !strings.Contains(c.Detail, "allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp") ||
		!strings.Contains(c.Detail, "cloud firewall") || !strings.Contains(c.Detail, "Let's Encrypt") || !strings.Contains(c.Detail, "Port 443 carries the dashboard without a port") {
		t.Fatalf("firewall check: %+v", c)
	}
	plan := strings.Join(Plan(f, o), "\n")
	for _, want := range []string{"ufw allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp", "443/tcp and 80/tcp once the machine has an address, for the dashboard without a port", "80/tcp also while Let's Encrypt checks your own domain"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan misses %q:\n%s", want, plan)
		}
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"8443/tcp", "25565/tcp", "443/tcp", "80/tcp"} {
		if !h.ufwRules[rule] {
			t.Errorf("ufw does not allow %s", rule)
		}
	}
	if m := manifestOf(t, h); !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp", "443/tcp"}) {
		t.Fatalf("the manifest must record only the rules the installer added: %v", m.FirewallRules)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if !h.ufwRules["80/tcp"] || !h.ufwRules["22/tcp"] || h.ufwRules["8443/tcp"] || h.ufwRules["25565/tcp"] || h.ufwRules["443/tcp"] {
		t.Fatalf("after uninstall ufw allows %v; want only the admin's own rules", h.ufwRules)
	}
}

func TestWithoutUFWTheProviderFirewallIsExplained(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if c := check(f, "firewall"); c == nil || !strings.Contains(c.Detail, "allow 8443/tcp, 25565/tcp, 443/tcp and 80/tcp there") || !strings.Contains(c.Detail, "port 80 sends browsers there") {
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

// A new install turns the dashboard's standard port on when nothing uses or
// claims port 443, as the agent's hand-over would find it, and leaves it off
// otherwise, saying why; nothing else changes.
func TestANewInstallServesTheDashboardOnPort443OnlyWhenItsFree(t *testing.T) {
	install := func(t *testing.T, setup func(h *fakeHost)) (Facts, config.Config, *Result, string) {
		t.Helper()
		h := newFakeHost(t)
		if setup != nil {
			setup(h)
		}
		sys := h.system(t)
		o := opts("")
		o.Yes = true
		f := Preflight(context.Background(), sys, o)
		res, err := Run(context.Background(), sys, o, "test")
		if err != nil {
			t.Fatal(err)
		}
		var cfg config.Config
		if err := json.Unmarshal([]byte(read(t, h, ConfigDir+"/config.json")), &cfg); err != nil {
			t.Fatal(err)
		}
		return f, cfg, res, strings.Join(Plan(f, o), "\n")
	}
	f, cfg, res, plan := install(t, nil)
	if c := check(f, "port-443"); c == nil || c.Status != "info" || !strings.HasPrefix(c.Detail, "Free: once this machine has an address, the dashboard answers there without :8443.") {
		t.Fatalf("port 443 free: %+v", c)
	}
	if cfg.Dashboard443 != "on" || !res.Dashboard443 || !strings.Contains(plan, "playkeeper-panel (HTTPS on port 8443, and on 443 once the machine has an address)") {
		t.Fatalf("port 443 free: config %q, result %v, plan:\n%s", cfg.Dashboard443, res.Dashboard443, plan)
	}
	for name, tc := range map[string]struct {
		setup func(h *fakeHost)
		by    string
	}{
		"a program listening": {func(h *fakeHost) { h.listening[443] = true }, "another program"},
		"a web server set to start with the machine": {func(h *fakeHost) {
			wants := filepath.Join(h.root, UnitDir, "multi-user.target.wants")
			os.MkdirAll(wants, 0o755)
			os.Symlink("/lib/systemd/system/nginx.service", filepath.Join(wants, "nginx.service"))
		}, "nginx, set to start with the machine,"},
		"a Docker container": {func(h *fakeHost) {
			c := docker.ContainerSummary{Names: []string{"/proxy"}, Image: "traefik"}
			c.Ports = append(c.Ports, struct {
				PublicPort int    `json:"PublicPort"`
				Type       string `json:"Type"`
			}{443, "tcp"})
			h.dockerPresent, h.containers = true, []docker.ContainerSummary{c}
		}, "the Docker container proxy"},
	} {
		t.Run(name, func(t *testing.T) {
			f, cfg, res, plan := install(t, tc.setup)
			if c := check(f, "port-443"); c == nil || c.Status != "info" || c.Detail != "Used by "+tc.by+", so the dashboard stays on port 8443. Playkeeper leaves port 443 alone." {
				t.Fatalf("the check: %+v", c)
			}
			if cfg.Dashboard443 != "" || res.Dashboard443 || strings.Contains(plan, "and on 443") {
				t.Fatalf("config %q, result %v, plan:\n%s", cfg.Dashboard443, res.Dashboard443, plan)
			}
		})
	}
	// Data from an earlier install keeps its choice.
	f, cfg, _, _ = install(t, func(h *fakeHost) {
		os.MkdirAll(filepath.Join(h.root, config.DefaultDataDir, "servers", "abcdefghjk", "data"), 0o755)
	})
	if check(f, "port-443") != nil || cfg.Dashboard443 != "" {
		t.Fatalf("reusing data: %+v, %q", check(f, "port-443"), cfg.Dashboard443)
	}
}
