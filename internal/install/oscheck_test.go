package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/platform"
)

func osRelease(t *testing.T, h *fakeHost, id, version string) {
	t.Helper()
	body := "ID=" + id + "\n"
	if version != "" {
		body += "VERSION_ID=\"" + version + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(h.root, "/etc/os-release"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightPassesEverySupportedRelease(t *testing.T) {
	for _, r := range platform.Supported {
		t.Run(r.Name, func(t *testing.T) {
			h := newFakeHost(t)
			osRelease(t, h, r.Distro, r.Version)
			f := Preflight(context.Background(), h.system(t), opts(""))
			c := check(f, "os")
			if !f.OK() || c == nil {
				t.Fatalf("preflight failed on %s: %+v", r.Name, f.Checks)
			}
			if r.SecurityEnded == "" {
				if c.Status != "pass" || !strings.Contains(c.Detail, r.Name) {
					t.Errorf("%s: %+v, want pass", r.Name, c)
				}
				return
			}
			// A release past its security support still installs, and the
			// preflight says what to move to.
			if c.Status != "warn" || !strings.Contains(c.Detail, r.SecurityEnded) || c.Fix == "" {
				t.Errorf("%s: %+v, want a warning that names when its security updates ended", r.Name, c)
			}
		})
	}
}

func TestPreflightWarnsButContinuesOnANewerRelease(t *testing.T) {
	for _, c := range []struct{ id, version, names string }{
		{"ubuntu", "28.04", "Ubuntu 26.04 LTS"},
		{"ubuntu", "25.10", "Ubuntu 24.04 LTS"},
		{"debian", "14", "Debian 13"},
		{"debian", "", "Debian 13"},
	} {
		h := newFakeHost(t)
		osRelease(t, h, c.id, c.version)
		f := Preflight(context.Background(), h.system(t), opts(""))
		ch := check(f, "os")
		if !f.OK() || ch.Status != "warn" || !strings.Contains(ch.Detail, c.names) || !strings.Contains(ch.Detail, "should work") {
			t.Errorf("%s %s: ok=%v %+v", c.id, c.version, f.OK(), ch)
		}
	}
}

func TestPreflightRefusesOlderReleasesAndOtherSystemsUnlessAllowed(t *testing.T) {
	for _, c := range []struct{ id, version, names string }{
		{"ubuntu", "18.04", "Ubuntu 20.04 LTS or later"},
		{"debian", "11", "Debian 12 or later"},
		{"alpine", "3.20", platform.Summary()},
	} {
		h := newFakeHost(t)
		osRelease(t, h, c.id, c.version)
		f := Preflight(context.Background(), h.system(t), opts(""))
		ch := check(f, "os")
		if f.OK() || ch.Status != "fail" || !strings.Contains(ch.Detail, c.names) || !strings.Contains(ch.Fix, "--allow-untested-os") {
			t.Errorf("%s %s: ok=%v %+v", c.id, c.version, f.OK(), ch)
		}
		o := opts("")
		o.AllowUntestedOS = true
		f = Preflight(context.Background(), h.system(t), o)
		if ch := check(f, "os"); !f.OK() || ch.Status != "warn" {
			t.Errorf("%s %s with --allow-untested-os: ok=%v %+v", c.id, c.version, f.OK(), ch)
		}
	}
}

func TestDebianGetsDockerFromItsOwnArchiveWithTheDockerCommand(t *testing.T) {
	h := newFakeHost(t)
	osRelease(t, h, "debian", "13")
	h.aptPolicy = map[string]string{"docker-cli": "docker-cli:\n  Installed: (none)\n  Candidate: 26.1.5+dfsg1-9+deb13u1\n"}
	// A new server's package lists are empty until apt-get update.
	h.aptListsEmpty = true
	out := &bytes.Buffer{}
	o := opts("")
	o.Yes, o.Out = true, out
	sys := h.system(t)
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "install Debian's docker.io package") || !strings.Contains(out.String(), "install docker.io and the docker command from Debian's archive") {
		t.Errorf("the preflight and the plan should name Debian's archive:\n%s", out.String())
	}
	if !ran(h, "apt-get -o DPkg::Lock::Timeout=60 install -y --no-install-recommends docker.io docker-cli") {
		t.Errorf("Debian 13's docker command (docker-cli) was not installed: %v", h.cmds)
	}
	m, _ := readManifest(t, h)
	if !contains(m.PackagesInstalled, "docker-cli") {
		t.Errorf("the manifest doesn't record docker-cli for uninstall: %v", m.PackagesInstalled)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if h.packages["docker-cli"] || h.packages["docker.io"] {
		t.Errorf("uninstall left Docker's packages: %v", h.packages)
	}
}

func TestUbuntuDockerIOAlreadyHasTheDockerCommand(t *testing.T) {
	h := newFakeHost(t)
	// Ubuntu's docker.io provides docker-cli, which is no package of its own.
	h.aptPolicy = map[string]string{"docker-cli": "docker-cli:\n  Installed: (none)\n  Candidate: (none)\n  Version table:\n"}
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), h.system(t), o, "test"); err != nil {
		t.Fatal(err)
	}
	if !ran(h, "apt-get -o DPkg::Lock::Timeout=60 install -y --no-install-recommends docker.io") {
		t.Errorf("want docker.io alone: %v", h.cmds)
	}
}

func TestTheInstallerFindsRootsCommandsWithAUsersPath(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	Real()
	if got, want := os.Getenv("PATH"), "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"; got != want {
		t.Errorf("PATH after su on Debian: %q, want %q", got, want)
	}
	t.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	Real()
	if got, want := os.Getenv("PATH"), "/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/sbin"; got != want {
		t.Errorf("a PATH with sbin already: %q, want %q", got, want)
	}
}

func ran(h *fakeHost, cmd string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.cmds {
		if c == cmd {
			return true
		}
	}
	return false
}

func TestPreflightNamesAFirewallOtherThanUFWThatDropsIncomingConnections(t *testing.T) {
	const nftDrop = `table inet filter {
	chain input {
		type filter hook input priority filter; policy drop;
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
	}
	chain output {
		type filter hook output priority filter; policy accept;
	}
}
table ip filter {
	chain INPUT {
		type filter hook input priority filter; policy accept;
	}
}
`
	h := newFakeHost(t)
	h.nftChains = nftDrop
	c := check(Preflight(context.Background(), h.system(t), opts("")), "firewall")
	if c.Status != "warn" || c.Label != "Firewall (nftables)" || !strings.Contains(c.Detail, "8443/tcp, 25565/tcp and 80/tcp") ||
		!strings.Contains(c.Fix, "sudo nft insert rule inet filter input tcp dport '{ 8443, 25565, 80 }' accept") {
		t.Errorf("nftables with a drop policy: %+v", c)
	}

	h = newFakeHost(t)
	h.nftChains = strings.ReplaceAll(nftDrop, "policy drop", "policy accept")
	h.fw4.table("filter", false).chain("INPUT").policy = "DROP"
	o := opts("")
	o.Join = "https://203.0.113.5:8443"
	c = check(Preflight(context.Background(), h.system(t), o), "firewall")
	if c.Status != "warn" || c.Label != "Firewall (iptables)" || !strings.Contains(c.Fix, "sudo iptables -I INPUT -p tcp -m multiport --dports 25565 -j ACCEPT") {
		t.Errorf("iptables with a DROP policy, on a machine that joins a dashboard: %+v", c)
	}

	// ufw's chains drop too; ufw is handled by opening the ports in it.
	h.ufwActive = true
	if c := check(Preflight(context.Background(), h.system(t), opts("")), "firewall"); c.Status != "info" || c.Label != "Firewall (ufw)" {
		t.Errorf("with ufw active: %+v", c)
	}

	h = newFakeHost(t)
	h.nftChains = strings.ReplaceAll(nftDrop, "policy drop", "policy accept")
	if c := check(Preflight(context.Background(), h.system(t), opts("")), "firewall"); c.Status != "info" || !strings.HasPrefix(c.Detail, "Found no firewall") {
		t.Errorf("a firewall that accepts: %+v", c)
	}
}
