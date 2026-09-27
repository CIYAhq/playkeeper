package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/platform"
)

// newELHost is a fresh RHEL-family host: os-release says id and version,
// dnf and rpm manage its packages, and firewalld is off.
func newELHost(t *testing.T, id, version, name string) *fakeHost {
	t.Helper()
	h := newFakeHost(t)
	like := "rhel centos fedora"
	if id == "amzn" {
		like = "fedora"
	}
	os.WriteFile(filepath.Join(h.root, "/etc/os-release"), []byte("NAME=\""+name+"\"\nVERSION=\""+version+"\"\nID=\""+id+"\"\nID_LIKE=\""+like+"\"\nVERSION_ID=\""+version+"\"\n"), 0o644)
	os.Remove(filepath.Join(h.root, "/usr/bin/apt-get"))
	os.WriteFile(filepath.Join(h.root, "/usr/bin/dnf"), []byte("#!/bin/sh\n"), 0o755)
	for _, d := range []string{"/proc/net", "/etc/yum.repos.d", "/etc/pki/rpm-gpg"} {
		os.MkdirAll(filepath.Join(h.root, d), 0o755)
	}
	os.WriteFile(filepath.Join(h.root, "/proc/net/route"), []byte("Iface\tDestination\tGateway\tFlags\nlo\t0000007F\t00000000\t0001\neth0\t00000000\t0164A8C0\t0003\neth0\t0064A8C0\t00000000\t0001\n"), 0o644)
	h.packages = map[string]bool{"bash": true, "coreutils": true, "rpm": true, "gpg-pubkey-fd431d51-4ae0493b": true}
	h.rpmNeeds = map[string]string{}
	h.dnfDocker = []string{"docker-ce", "docker-ce-cli", "containerd.io", "container-selinux", "iptables-nft", "libnftnl"}
	// The sudoers files as each system ships them.
	path := "/sbin:/bin:/usr/sbin:/usr/bin"
	if id == "amzn" {
		h.dnfDocker = []string{"docker", "containerd", "runc", "pigz", "iptables-legacy"}
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/var/lib/snapd/snap/bin"
	}
	os.MkdirAll(filepath.Join(h.root, "/etc/sudoers.d"), 0o750)
	os.WriteFile(filepath.Join(h.root, "/etc/sudoers"), []byte("Defaults    !visiblepw\nDefaults    secure_path = "+path+"\nroot    ALL=(ALL)       ALL\n"), 0o440)
	return h
}

// withFirewalld turns firewalld on, with eth0 in zone and the admin's own
// rules in it.
// withFirewalld turns firewalld on, with eth0 in zone and the admin's own
// rules in it; saved says whether the image saved the zone in /etc, as
// Oracle Linux's does, or firewalld still reads it from its defaults.
func (h *fakeHost) withFirewalld(zone string, saved bool, rules ...string) {
	os.WriteFile(filepath.Join(h.root, "/usr/bin/firewall-cmd"), []byte("#!/bin/sh\n"), 0o755)
	for _, d := range []string{"/etc/firewalld/zones", "/etc/firewalld/policies"} {
		os.MkdirAll(filepath.Join(h.root, d), 0o755)
	}
	h.firewalld = &fakeFirewalld{root: h.root, zone: zone, perm: map[string]bool{}, run: map[string]bool{}, defaults: map[string]bool{}}
	for _, r := range rules {
		h.firewalld.perm[zone+" "+r], h.firewalld.run[zone+" "+r], h.firewalld.defaults[zone+" "+r] = true, true, true
	}
	if saved {
		h.firewalld.save(zone)
	}
}

// fakeFirewalld keeps "zone rule" pairs and Docker's objects ("zone docker",
// "policy docker-forwarding"), saved (perm, with each zone's and object's
// file under root's /etc/firewalld, and the one before as .old) and running
// (run); defaults are the zone's rules before anyone changed it.
type fakeFirewalld struct {
	root                string
	zone                string
	perm, run, defaults map[string]bool
	reloads             int
}

func (f *fakeFirewalld) file(object string) string {
	kind, name, _ := strings.Cut(object, " ")
	return filepath.Join(f.root, "/etc/firewalld", kind+"s", name+".xml")
}

// save writes zone's file as firewalld does after a saved change.
func (f *fakeFirewalld) save(zone string) {
	p := f.file("zone " + zone)
	if _, err := os.Stat(p); err == nil {
		os.Rename(p, p+".old")
	}
	os.WriteFile(p, []byte("<zone>"+strings.Join(f.ports(zone), " ")+"</zone>\n"), 0o644)
}

func (f *fakeFirewalld) ports(zone string) []string {
	var out []string
	for k := range f.perm {
		if r, ok := strings.CutPrefix(k, zone+" "); ok {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeFirewalld) dockerStarted() {
	for _, o := range dockerFirewalld {
		f.perm[o], f.run[o] = true, true
		os.WriteFile(f.file(o), []byte("<"+o+"/>\n"), 0o644)
	}
}

// runEL answers rpm, dnf and firewall-cmd; the caller holds h.mu.
func (h *fakeHost) runEL(name string, args []string) (string, error) {
	switch {
	case name == "rpm" && len(args) > 0 && args[0] == "-qa":
		var lines []string
		for p := range h.packages {
			if key, ok := strings.CutPrefix(p, "gpg-pubkey-"); ok {
				lines = append(lines, "gpg-pubkey "+key)
			} else {
				lines = append(lines, p+" 1.0-1.el9")
			}
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n") + "\n", nil
	case name == "rpm" && len(args) > 1 && args[0] == "-e" && args[1] == "--test":
		var out []string
		for _, p := range args[2:] {
			if by := h.rpmNeeds[p]; by != "" && !slices.Contains(args[2:], by) {
				out = append(out, "\t"+p+" is needed by (installed) "+by+"-1.0-1.el9.x86_64")
			}
		}
		if len(out) > 0 {
			msg := "error: Failed dependencies:\n" + strings.Join(out, "\n") + "\n"
			return msg, errors.New("exit status 1: " + msg)
		}
		return "", nil
	case name == "rpm" && len(args) > 2 && args[0] == "-q" && args[1] == "--whatprovides":
		if h.packages[args[2]] {
			return args[2] + "\n", nil
		}
		return "no package provides " + args[2] + "\n", errors.New("exit status 1")
	case name == "rpm" && len(args) > 0 && args[0] == "-e":
		for _, p := range args[1:] {
			delete(h.packages, p)
		}
		return "", nil
	case name == "modinfo":
		if h.noNetfilterModules {
			return "modinfo: ERROR: Module " + args[len(args)-1] + " not found.\n", errors.New("exit status 1")
		}
		return "/lib/modules/6.12.0-211.47.1.el10_2.x86_64/kernel/net/netfilter/xt_addrtype.ko.xz\n", nil
	case name == "uname":
		return "6.12.0-211.47.1.el10_2.x86_64\n", nil
	case name == "dnf" && slices.Contains(args, "install"):
		for _, p := range h.dnfDocker {
			h.packages[p] = true
		}
		for _, a := range args {
			if strings.HasPrefix(a, "kernel-modules-extra-") {
				h.packages["kernel-modules-extra"] = true
			}
		}
		if _, err := os.Stat(filepath.Join(h.root, dockerKeyFile)); err == nil && slices.Contains(args, "docker-ce") {
			h.packages["gpg-pubkey-621e9f35-58adea78"] = true
		}
		h.dockerPresent = true
		appendLine(filepath.Join(h.root, "/etc/group"), "docker:x:989:")
		os.MkdirAll(filepath.Join(h.root, "/var/lib/docker/overlay2"), 0o710)
		os.MkdirAll(filepath.Join(h.root, "/var/lib/containerd"), 0o700)
		os.MkdirAll(filepath.Join(h.root, "/etc/docker"), 0o755)
		return "Complete!\n", nil
	case name == "dnf" && slices.Contains(args, "remove"):
		for _, p := range args[3:] {
			delete(h.packages, p)
		}
		h.dockerPresent = false
		return "Complete!\n", nil
	case name == "firewall-cmd":
		return h.firewalld.cmd(args)
	}
	return "", errors.New("unexpected command " + name)
}

func (f *fakeFirewalld) cmd(args []string) (string, error) {
	if f == nil {
		return "", errors.New("firewall-cmd: command not found")
	}
	set, zone := f.run, f.zone
	var op, val string
	for _, a := range args {
		switch k, v, _ := strings.Cut(a, "="); k {
		case "--permanent":
			set = f.perm
		case "--zone":
			zone = v
		default:
			op, val = k, v
		}
	}
	no := func() (string, error) { return "no\n", errors.New("exit status 1") }
	switch op {
	case "--state":
		return "running\n", nil
	case "--get-zone-of-interface":
		if val == "eth0" {
			return f.zone + "\n", nil
		}
		return "no zone\n", errors.New("exit status 2")
	case "--get-default-zone":
		return "public\n", nil
	case "--query-port":
		if set[zone+" "+val] {
			return "yes\n", nil
		}
		return no()
	case "--add-port", "--remove-port":
		if op == "--add-port" {
			set[zone+" "+val] = true
		} else {
			delete(set, zone+" "+val)
		}
		if slices.Contains(args, "--permanent") {
			f.save(zone)
		}
		return "success\n", nil
	case "--list-all":
		return zone + "\n  ports: " + strings.Join(f.ports(zone), " ") + "\n", nil
	case "--load-zone-defaults":
		p := f.file("zone " + val)
		if _, err := os.Stat(p); err != nil {
			return "Error: NO_DEFAULTS", errors.New("exit status 1")
		}
		os.Rename(p, p+".old")
		for k := range f.perm {
			if strings.HasPrefix(k, val+" ") {
				delete(f.perm, k)
			}
		}
		for k := range f.defaults {
			f.perm[k] = true
		}
		return "success\n", nil
	case "--get-zones", "--get-policies":
		kind := map[string]string{"--get-zones": "zone ", "--get-policies": "policy "}[op]
		names := map[string][]string{"zone ": {"block", "drop", "public", "trusted"}, "policy ": {"allow-host-ipv6"}}[kind]
		for o := range set {
			if n, ok := strings.CutPrefix(o, kind); ok {
				names = append(names, n)
			}
		}
		return strings.Join(names, " ") + "\n", nil
	case "--delete-zone", "--delete-policy":
		o := strings.TrimPrefix(op, "--delete-") + " " + val
		if !f.perm[o] {
			return "Error: INVALID_" + strings.ToUpper(strings.TrimPrefix(op, "--delete-")) + ": " + val, errors.New("exit status 112")
		}
		delete(f.perm, o)
		os.Rename(f.file(o), f.file(o)+".old")
		return "success\n", nil
	case "--reload":
		f.reloads++
		f.run = map[string]bool{}
		for k := range f.perm {
			f.run[k] = true
		}
		return "success\n", nil
	}
	return "", errors.New("firewall-cmd: unexpected " + strings.Join(args, " "))
}

func TestTheRHELFamilyAndAmazonLinuxAreSupported(t *testing.T) {
	for _, c := range []struct {
		id, version, name string
		status            string
		docker            string
	}{
		{"almalinux", "9.6", "AlmaLinux", "pass", "Docker Engine (docker-ce, docker-ce-cli, containerd.io) from Docker's repository for CentOS 9"},
		{"almalinux", "10.0", "AlmaLinux", "pass", "from Docker's repository for CentOS 10"},
		{"rocky", "9.6", "Rocky Linux", "pass", "from Docker's repository for CentOS 9"},
		{"rocky", "10.0", "Rocky Linux", "pass", "from Docker's repository for CentOS 10"},
		{"ol", "9.6", "Oracle Linux Server", "pass", "from Docker's repository for CentOS 9"},
		{"amzn", "2023", "Amazon Linux", "pass", "docker from Amazon Linux's repository"},
		{"rhel", "9.6", "Red Hat Enterprise Linux", "pass", "from Docker's repository for RHEL 9"},
		{"rhel", "10.0", "Red Hat Enterprise Linux", "pass", "from Docker's repository for RHEL 10"},
		{"centos", "9", "CentOS Stream", "pass", "from Docker's repository for CentOS 9"},
		{"centos", "10", "CentOS Stream", "pass", "from Docker's repository for CentOS 10"},
		{"ol", "10.1", "Oracle Linux Server", "warn", "from Docker's repository for CentOS 10"},
		{"almalinux", "8.10", "AlmaLinux", "fail", ""},
		{"amzn", "2", "Amazon Linux", "fail", ""},
		{"fedora", "42", "Fedora Linux", "fail", ""},
		{"opensuse-leap", "15.6", "openSUSE Leap", "fail", ""},
		{"arch", "", "Arch Linux", "fail", ""},
	} {
		t.Run(c.id+" "+c.version, func(t *testing.T) {
			h := newELHost(t, c.id, c.version, c.name)
			f := Preflight(context.Background(), h.system(t), opts(""))
			os := check(f, "os")
			if os == nil || os.Status != c.status {
				t.Fatalf("OS check: %+v", os)
			}
			if c.status == "fail" {
				// An older release of a supported distribution names its
				// oldest supported one; another system gets the whole list.
				if !strings.Contains(os.Detail, "is older than the releases Playkeeper supports") && !strings.Contains(os.Detail, platform.Summary()) || !strings.Contains(os.Fix, "--allow-untested-os") {
					t.Fatalf("a refused system must say what Playkeeper runs on and the way out: %+v", os)
				}
				return
			}
			if !f.OK() {
				var buf bytes.Buffer
				PrintChecks(&buf, f)
				t.Fatalf("a clean %s host failed preflight:\n%s", c.name, buf.String())
			}
			if d := check(f, "docker"); d == nil || d.Status != "info" || !strings.Contains(d.Detail, c.docker) {
				t.Fatalf("Docker check: %+v", d)
			}
			if plan := strings.Join(Plan(f, opts("")), "\n"); !strings.Contains(plan, "Packages:  install ") || !strings.Contains(plan, c.docker) {
				t.Fatalf("the plan must say where Docker comes from:\n%s", plan)
			}
		})
	}
}

func TestAnUntestedSystemGetsDockerOnlyWhereItsFamilyKnowsHow(t *testing.T) {
	for _, c := range []struct {
		id, version, like string
		want              string
	}{
		{"eurolinux", "9.4", "rhel fedora centos", "from Docker's repository for CentOS 9"},
		{"almalinux", "8.10", "rhel centos fedora", "from Docker's repository for RHEL 8"},
		{"fedora", "42", "", "doesn't install it on Fedora Linux 42"},
		{"opensuse-tumbleweed", "20260901", "opensuse suse", "doesn't install it on"},
	} {
		t.Run(c.id, func(t *testing.T) {
			h := newELHost(t, c.id, c.version, "Fedora Linux")
			os.WriteFile(filepath.Join(h.root, "/etc/os-release"), []byte("NAME=\"Fedora Linux\"\nID="+c.id+"\nID_LIKE=\""+c.like+"\"\nVERSION_ID="+c.version+"\n"), 0o644)
			o := opts("")
			o.AllowUntestedOS = true
			f := Preflight(context.Background(), h.system(t), o)
			if c := check(f, "os"); c.Status != "warn" {
				t.Fatalf("OS check with --allow-untested-os: %+v", c)
			}
			if d := check(f, "docker"); d == nil || !strings.Contains(d.Detail, c.want) {
				t.Fatalf("Docker check: %+v", d)
			}
		})
	}
}

func TestPreflightRefusesWhatInstallingDockerCEWouldBreak(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h *fakeHost)
		want  string
	}{
		{"podman-docker", func(h *fakeHost) { h.packages["podman"], h.packages["podman-docker"] = true, true }, "sudo dnf remove podman-docker"},
		{"runc", func(h *fakeHost) { h.packages["podman"], h.packages["runc"] = true, true }, "containerd.io package would replace it"},
		{"Podman on the Docker socket", func(h *fakeHost) { h.podmanSock = true }, "sudo systemctl disable --now podman.socket"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newELHost(t, "rocky", "9.6", "Rocky Linux")
			c.setup(h)
			before := snapshot(t, h.root)
			sys := h.system(t)
			f := Preflight(context.Background(), sys, opts(""))
			d := check(f, "docker")
			if f.OK() || d == nil || d.Status != "fail" || !strings.Contains(d.Detail+" "+d.Fix, c.want) {
				t.Fatalf("Docker check: %+v", d)
			}
			o := opts("")
			o.Yes = true
			if _, err := Run(context.Background(), sys, o, "test"); err == nil {
				t.Fatal("the install went ahead")
			}
			if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
				t.Fatalf("the refused install changed the host: %v", d)
			}
			for _, cmd := range h.cmds {
				if !readOnly(cmd) {
					t.Fatalf("the refused install ran %q", cmd)
				}
			}
		})
	}
	h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
	h.packages["podman"], h.packages["crun"] = true, true
	if f := Preflight(context.Background(), h.system(t), opts("")); !f.OK() {
		t.Fatalf("Podman with crun, its default runtime, can stay next to Docker Engine: %+v", check(f, "docker"))
	}
}

func TestThePodmanSocketFixUsesTheHostsPackageManager(t *testing.T) {
	unknown := func(t *testing.T) *fakeHost {
		h := newELHost(t, "opensuse-tumbleweed", "20260901", "openSUSE Tumbleweed")
		os.WriteFile(filepath.Join(h.root, "/etc/os-release"), []byte("NAME=\"openSUSE Tumbleweed\"\nID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\nVERSION_ID=\"20260901\"\n"), 0o644)
		return h
	}
	for _, c := range []struct {
		name string
		host func(t *testing.T) *fakeHost
		want string
	}{
		{"Ubuntu", newFakeHost, "and remove podman-docker (sudo apt-get remove podman-docker); "},
		{"AlmaLinux", func(t *testing.T) *fakeHost { return newELHost(t, "almalinux", "9.6", "AlmaLinux") }, "and remove podman-docker (sudo dnf remove podman-docker); "},
		{"a family Playkeeper doesn't know", unknown, "and remove podman-docker; "},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := c.host(t)
			h.podmanSock = true
			o := opts("")
			o.AllowUntestedOS = true
			d := check(Preflight(context.Background(), h.system(t), o), "docker")
			if d == nil || d.Status != "fail" || !strings.Contains(d.Fix, "(sudo systemctl disable --now podman.socket)") || !strings.Contains(d.Fix, c.want) {
				t.Fatalf("Docker check: %+v", d)
			}
		})
	}
}

func TestDockerCEInstallsFromDockersRepositoryAndLeavesWithIt(t *testing.T) {
	h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
	os.MkdirAll(filepath.Join(h.root, "/etc/yum.repos.d"), 0o755)
	os.WriteFile(filepath.Join(h.root, "/etc/yum.repos.d/almalinux-baseos.repo"), []byte("[baseos]\n"), 0o644)
	before, pkgs := snapshot(t, h.root), clonePackages(h)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	repo := read(t, h, dockerRepoFile)
	for _, want := range []string{"baseurl=https://download.docker.com/linux/centos/9/$basearch/stable", "gpgcheck=1", "gpgkey=file://" + dockerKeyFile, "playkeeper uninstall"} {
		if !strings.Contains(repo, want) {
			t.Errorf("the repository file misses %q:\n%s", want, repo)
		}
	}
	if key := read(t, h, dockerKeyFile); key != string(dockerRPMKey) || !strings.HasPrefix(key, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatal("the signing key must be the one Playkeeper carries")
	}
	if !slices.Contains(h.cmds, "dnf -y --setopt=install_weak_deps=False install docker-ce docker-ce-cli containerd.io") {
		t.Fatalf("dnf must install Docker Engine without the packages it only recommends: %v", h.cmds)
	}
	m := manifestOf(t, h)
	if m.PackageManager != "dnf" || !slices.Equal(m.DockerRepoFiles, []string{dockerKeyFile, dockerRepoFile}) ||
		!contains(m.PackagesInstalled, "docker-ce") || !contains(m.PackagesInstalled, "gpg-pubkey-621e9f35-58adea78") || contains(m.PackagesInstalled, "gpg-pubkey-fd431d51-4ae0493b") {
		t.Fatalf("manifest: %+v", m)
	}
	if !strings.Contains(out.String(), "install Docker (docker-ce, docker-ce-cli, containerd.io)") ||
		!strings.Contains(out.String(), "Files:     /usr/local/bin/playkeeper\n             /usr/bin/playkeeper, a link to it, since sudo here leaves /usr/local/bin out of its path") {
		t.Fatalf("install output:\n%s", out)
	}
	if link, err := os.Readlink(filepath.Join(h.root, SudoLink)); err != nil || link != BinPath || !contains(m.FilesCreated, SudoLink) {
		t.Fatalf("sudo must find playkeeper: %q %v, manifest %v", link, err, m.FilesCreated)
	}
	h.cmds = nil
	uout := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: uout}); err != nil {
		t.Fatalf("%v\n%s", err, uout)
	}
	if !strings.Contains(uout.String(), "the repository and signing key Docker came from: "+dockerKeyFile+", "+dockerRepoFile) {
		t.Fatalf("the uninstall must say the repository goes:\n%s", uout)
	}
	test, remove, key := slices.IndexFunc(h.cmds, func(c string) bool { return strings.HasPrefix(c, "rpm -e --test ") }),
		slices.IndexFunc(h.cmds, func(c string) bool { return strings.HasPrefix(c, "dnf -y remove --noautoremove ") }),
		slices.Index(h.cmds, "rpm -e gpg-pubkey-621e9f35-58adea78")
	if test < 0 || remove < test || key < remove || strings.Contains(h.cmds[remove], "gpg-pubkey") {
		t.Fatalf("rpm must check first that nothing else needs the packages, dnf remove them and rpm the signing key: %v", h.cmds)
	}
	if !sameSet(pkgs, h.packages) {
		t.Fatalf("packages after uninstall: %v, before: %v", trueKeys(h.packages), trueKeys(pkgs))
	}
	if d := diff(before, snapshotWithout(t, h.root, "var/lib/playkeeper")); len(d) != 0 {
		t.Fatalf("uninstall left changes: %v", d)
	}
}

func TestAmazonLinuxGetsItsOwnDockerPackage(t *testing.T) {
	h := newELHost(t, "amzn", "2023", "Amazon Linux")
	pkgs := clonePackages(h)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.cmds, "dnf -y --setopt=install_weak_deps=False install docker") {
		t.Fatalf("Amazon Linux installs its docker package: %v", h.cmds)
	}
	if _, err := os.Stat(filepath.Join(h.root, dockerRepoFile)); err == nil {
		t.Fatal("Amazon Linux needs no repository added")
	}
	if m := manifestOf(t, h); len(m.DockerRepoFiles) != 0 || !contains(m.PackagesInstalled, "docker") {
		t.Fatalf("manifest: %+v", m)
	}
	if _, err := os.Lstat(filepath.Join(h.root, SudoLink)); err == nil {
		t.Fatal("Amazon Linux's sudo finds /usr/local/bin, so it needs no link")
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if !sameSet(pkgs, h.packages) {
		t.Fatalf("packages after uninstall: %v", trueKeys(h.packages))
	}
}

func TestAnInjectedFailureOnTheRHELFamilyRollsBackDockerAndItsRepository(t *testing.T) {
	h := newELHost(t, "ol", "9.6", "Oracle Linux Server")
	h.withFirewalld("public", true, "22/tcp")
	before, pkgs := snapshot(t, h.root), clonePackages(h)
	perm, run := clone(h.firewalld.perm), clone(h.firewalld.run)
	t.Setenv(FailStepEnv, "install and start systemd services")
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("expected the injected failure, got %v", err)
	}
	if !strings.Contains(out.String(), "Rollback complete") {
		t.Fatalf("rollback output:\n%s", out)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("rollback left changes: %v", d)
	}
	if !sameSet(pkgs, h.packages) || !sameSet(perm, h.firewalld.perm) || !sameSet(run, h.firewalld.run) {
		t.Fatalf("rollback left packages %v or firewalld %v / %v", trueKeys(h.packages), trueKeys(h.firewalld.perm), trueKeys(h.firewalld.run))
	}
}

func TestFirewalldOpensPortsInTheZoneAndUninstallLeavesTheAdminsRules(t *testing.T) {
	h := newELHost(t, "ol", "9.6", "Oracle Linux Server")
	h.withFirewalld("internal", true, "22/tcp", "80/tcp")
	before := snapshot(t, h.root)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if c := check(f, "firewall"); c == nil || c.Label != "Firewall (firewalld)" || !strings.Contains(c.Detail, "firewalld is active; the installer will allow 8443/tcp, 25565/tcp and 80/tcp in its internal zone") {
		t.Fatalf("firewall check: %+v", c)
	}
	plan := strings.Join(Plan(f, o), "\n")
	for _, want := range []string{"Firewall:  firewalld: allow 8443/tcp, 25565/tcp and 80/tcp in the internal zone", "adds a 'docker' zone and a 'docker-forwarding' policy to firewalld"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan misses %q:\n%s", want, plan)
		}
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	fw := h.firewalld
	for _, rule := range []string{"8443/tcp", "25565/tcp", "80/tcp"} {
		if !fw.perm["internal "+rule] || !fw.run["internal "+rule] {
			t.Errorf("firewalld does not allow %s both saved and running", rule)
		}
	}
	m := manifestOf(t, h)
	if m.Firewall != "firewalld" || m.FirewallZone != "internal" || !slices.Equal(m.FirewallRules, []string{"8443/tcp", "25565/tcp"}) || !slices.Equal(m.DockerFirewalld, dockerFirewalld) {
		t.Fatalf("the manifest must record only what the install added: %+v", m)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"internal 22/tcp": true, "internal 80/tcp": true}
	if !sameSet(want, fw.perm) || !sameSet(want, fw.run) || fw.reloads != 1 {
		t.Fatalf("after uninstall firewalld has %v saved, %v running (%d reloads); want only the admin's rules", trueKeys(fw.perm), trueKeys(fw.run), fw.reloads)
	}
	if d := diff(before, snapshotWithout(t, h.root, "var/lib/playkeeper")); len(d) != 0 {
		t.Fatalf("uninstall left files, firewalld's backups of Docker's zone and policy included: %v", d)
	}
}

func TestAZoneFirewalldReadFromItsDefaultsIsLeftAsItWas(t *testing.T) {
	for _, adminChanged := range []bool{false, true} {
		h := newELHost(t, "rocky", "10.0", "Rocky Linux")
		h.withFirewalld("public", false, "22/tcp")
		before := snapshot(t, h.root)
		sys := h.system(t)
		o := opts("")
		o.Yes = true
		if _, err := Run(context.Background(), sys, o, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(h.root, "/etc/firewalld/zones/public.xml")); err != nil {
			t.Fatal("firewalld saves the zone it changed in /etc")
		}
		if adminChanged {
			h.firewalld.perm["public 9090/tcp"] = true
			h.firewalld.save("public")
		}
		if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
			t.Fatal(err)
		}
		after := snapshotWithout(t, h.root, "var/lib/playkeeper")
		switch d := diff(before, after); {
		case !adminChanged && len(d) != 0:
			t.Fatalf("uninstall left firewalld's copy of the zone or its backup: %v", d)
		case adminChanged && !slices.Equal(d, []string{"added etc/firewalld/zones/public.xml"}):
			t.Fatalf("a zone the admin changed since keeps its settings, and loses only the backup: %v", d)
		}
		want := map[string]bool{"public 22/tcp": true}
		if adminChanged {
			want["public 9090/tcp"] = true
		}
		if !sameSet(want, h.firewalld.perm) {
			t.Fatalf("firewalld's saved rules after uninstall: %v", trueKeys(h.firewalld.perm))
		}
	}
}

func TestUninstallKeepsWhatOtherSoftwareNeedsOfDocker(t *testing.T) {
	h := newELHost(t, "rocky", "10.0", "Rocky Linux")
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	// Podman, installed later, shares container-selinux with Docker.
	h.packages["podman"], h.rpmNeeds["container-selinux"] = true, "podman"
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Keeping container-selinux, which Docker came with: podman-1.0-1.el9.x86_64 needs container-selinux.") {
		t.Fatalf("the uninstall must say what stays and why:\n%s", out)
	}
	if !h.packages["podman"] || !h.packages["container-selinux"] || h.packages["docker-ce"] || h.packages["containerd.io"] || h.packages["gpg-pubkey-621e9f35-58adea78"] {
		t.Fatalf("Docker must go and what Podman needs stay: %v", trueKeys(h.packages))
	}
	if _, err := os.Stat(filepath.Join(h.root, dockerRepoFile)); err == nil {
		t.Fatal("Docker's repository goes with Docker")
	}
	for _, c := range h.cmds {
		if strings.HasPrefix(c, "dnf -y remove") && strings.Contains(c, "container-selinux") {
			t.Fatalf("dnf must not remove what Podman needs: %q", c)
		}
	}
}

func TestUninstallKeepsDockerWhenOtherSoftwareNeedsIt(t *testing.T) {
	h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	h.packages["my-ci-runner"], h.rpmNeeds["docker-ce"] = true, "my-ci-runner"
	h.rpmNeeds["docker-ce-cli"], h.rpmNeeds["containerd.io"] = "docker-ce", "docker-ce"
	h.cmds = nil
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Keeping Docker: other software needs it (my-ci-runner-1.0-1.el9.x86_64 needs docker-ce") ||
		!strings.Contains(out.String(), "Docker keeps getting updates from "+dockerRepoFile) {
		t.Fatalf("the uninstall must keep Docker and say why:\n%s", out)
	}
	for _, c := range h.cmds {
		if strings.HasPrefix(c, "dnf -y remove") || strings.HasPrefix(c, "systemctl stop docker") {
			t.Fatalf("a Docker that stays must keep running: %q", c)
		}
	}
	if !h.packages["docker-ce"] || !h.packages["gpg-pubkey-621e9f35-58adea78"] {
		t.Fatalf("Docker must stay: %v", trueKeys(h.packages))
	}
	if _, err := os.Stat(filepath.Join(h.root, dockerRepoFile)); err != nil {
		t.Fatal("Docker keeps its repository")
	}
}

func TestEL10GetsTheRunningKernelsNetfilterModulesForDocker(t *testing.T) {
	h := newELHost(t, "almalinux", "10.1", "AlmaLinux")
	h.noNetfilterModules = true
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	f := Preflight(context.Background(), sys, o)
	if d := check(f, "docker"); d == nil || !strings.Contains(d.Detail, "from Docker's repository for CentOS 10, and kernel-modules-extra-6.12.0-211.47.1.el10_2.x86_64, the running kernel's netfilter modules Docker's rules need") {
		t.Fatalf("Docker check: %+v", d)
	}
	pkgs := clonePackages(h)
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.cmds, "dnf -y --setopt=install_weak_deps=False install docker-ce docker-ce-cli containerd.io kernel-modules-extra-6.12.0-211.47.1.el10_2.x86_64") {
		t.Fatalf("the running kernel's modules must come with Docker: %v", h.cmds)
	}
	if m := manifestOf(t, h); !contains(m.PackagesInstalled, "kernel-modules-extra") {
		t.Fatalf("manifest: %+v", m)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if !sameSet(pkgs, h.packages) {
		t.Fatalf("packages after uninstall: %v", trueKeys(h.packages))
	}

	for _, c := range []struct {
		id, version string
		missing     bool
	}{{"rocky", "10.0", false}, {"rocky", "9.6", true}, {"amzn", "2023", true}} {
		h := newELHost(t, c.id, c.version, c.id)
		h.noNetfilterModules = c.missing
		if d := check(Preflight(context.Background(), h.system(t), opts("")), "docker"); d == nil || strings.Contains(d.Detail, "kernel-modules-extra") {
			t.Fatalf("%s %s needs no kernel-modules-extra: %+v", c.id, c.version, d)
		}
	}
}

func TestADockerThatFailsToStartLeavesFirewalldAsItWas(t *testing.T) {
	h := newELHost(t, "rocky", "10.0", "Rocky Linux")
	h.withFirewalld("public", false, "22/tcp")
	h.dockerFailsToStart = true
	before, pkgs := snapshot(t, h.root), clonePackages(h)
	perm, run := clone(h.firewalld.perm), clone(h.firewalld.run)
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil || !strings.Contains(err.Error(), "docker.service") {
		t.Fatalf("expected Docker's start to fail, got %v", err)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("the rollback left files: %v\n%s", d, out)
	}
	if !sameSet(pkgs, h.packages) || !sameSet(perm, h.firewalld.perm) || !sameSet(run, h.firewalld.run) {
		t.Fatalf("the rollback left packages %v or firewalld %v / %v", trueKeys(h.packages), trueKeys(h.firewalld.perm), trueKeys(h.firewalld.run))
	}
}

func TestAFailedDockerInstallRemovesTheRepositoryItAdded(t *testing.T) {
	h := newELHost(t, "centos", "10", "CentOS Stream")
	before := snapshot(t, h.root)
	h.failCmd = "dnf -y --setopt=install_weak_deps=False install"
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil || !strings.Contains(err.Error(), "install Docker") {
		t.Fatalf("expected the Docker install to fail, got %v", err)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("the rollback left Docker's repository or key: %v\n%s", d, out)
	}
}

func TestDNFRemoveRefusesWhatOtherSoftwareNeeds(t *testing.T) {
	h := newELHost(t, "rocky", "9.6", "Rocky Linux")
	h.packages["container-selinux"], h.packages["podman"], h.rpmNeeds["container-selinux"] = true, true, "podman"
	err := dnf{}.remove(h.system(t), &bytes.Buffer{}, []string{"container-selinux"})
	if err == nil || !strings.Contains(err.Error(), "podman-1.0-1.el9.x86_64 needs container-selinux") {
		t.Fatalf("got %v", err)
	}
	if !h.packages["container-selinux"] || slices.ContainsFunc(h.cmds, func(c string) bool { return strings.HasPrefix(c, "dnf ") }) {
		t.Fatalf("dnf must not remove a package another needs: %v", h.cmds)
	}
}

func TestTheSudoLinkStaysForASecondUninstallAndLeavesAnotherPlaykeeperAlone(t *testing.T) {
	h := newELHost(t, "rocky", "9.6", "Rocky Linux")
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
	h.failCmd = "dnf -y remove"
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "run `sudo playkeeper uninstall` again") {
		t.Fatalf("a failed Docker removal must say how to finish: %v", err)
	}
	if m, ok := readManifest(t, h); !ok || !slices.Equal(m.FilesCreated, []string{BinPath, SudoLink}) {
		t.Fatalf("what the second run needs must stay: %+v", m)
	}
	if _, err := os.Lstat(filepath.Join(h.root, SudoLink)); err != nil {
		t.Fatal("`sudo playkeeper uninstall` must still find the binary")
	}
	h.failCmd = ""
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(h.root, SudoLink)); err == nil {
		t.Fatal("the second run must remove the link")
	}

	h = newELHost(t, "rocky", "9.6", "Rocky Linux")
	os.WriteFile(filepath.Join(h.root, SudoLink), []byte("someone else's"), 0o755)
	if f := Preflight(context.Background(), h.system(t), opts("")); f.SudoLink {
		t.Fatal("a playkeeper already in /usr/bin is left alone")
	}
}

func TestSELinuxIsReportedAndChangesNothing(t *testing.T) {
	h := newELHost(t, "almalinux", "10.0", "AlmaLinux")
	os.MkdirAll(filepath.Join(h.root, "/sys/fs/selinux"), 0o755)
	os.WriteFile(filepath.Join(h.root, "/sys/fs/selinux/enforce"), []byte("1"), 0o644)
	f := Preflight(context.Background(), h.system(t), opts(""))
	if c := check(f, "selinux"); c == nil || c.Status != "pass" || !strings.Contains(c.Detail, "enforcing") {
		t.Fatalf("SELinux check: %+v", c)
	}
	if c := check(newFakeHostPreflight(t), "selinux"); c != nil {
		t.Fatalf("no SELinux check where it is off: %+v", c)
	}
}

func TestAUnitUnderBothLibAndUsrLibIsListedOnce(t *testing.T) {
	h := newELHost(t, "rocky", "9.6", "Rocky Linux")
	os.MkdirAll(filepath.Join(h.root, "/usr/lib/systemd/system"), 0o755)
	for _, d := range []string{"/lib/systemd/system", "/usr/lib/systemd/system"} {
		os.WriteFile(filepath.Join(h.root, d, "minecraft.service"), []byte("[Service]\n"), 0o644)
	}
	f := Preflight(context.Background(), h.system(t), opts(""))
	if c := check(f, "existing"); c == nil || strings.Count(c.Detail, "minecraft.service") != 1 {
		t.Fatalf("existing check: %+v", c)
	}
}

func TestDNFWaitsForAnotherPackageManager(t *testing.T) {
	h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
	h.lockPolls = 2
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "waiting for another package manager to finish (on a new server this is usually dnf-makecache or cloud-init)") {
		t.Fatalf("the wait must be visible:\n%s", out)
	}
	for i, c := range h.cmds {
		if strings.HasPrefix(c, "dnf ") && i < h.lockFreedAt {
			t.Fatalf("dnf ran while the lock was held: %v", h.cmds)
		}
	}
}

func TestPIDLockHeldOnlyByARunningProcess(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "/var/cache/dnf"), 0o755)
	lock := "/var/cache/dnf/metadata_lock.pid"
	if pidLockHeld("/", []string{filepath.Join(root, lock)}) {
		t.Fatal("no lock file, no lock")
	}
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleep.Process.Kill()
	os.WriteFile(filepath.Join(root, lock), []byte(strconv.Itoa(sleep.Process.Pid)+"\n"), 0o644)
	if !pidLockHeld("/", []string{filepath.Join(root, lock)}) {
		t.Fatal("a running process's lock was not seen")
	}
	sleep.Process.Kill()
	sleep.Wait()
	if pidLockHeld("/", []string{filepath.Join(root, lock)}) {
		t.Fatal("a lock left by a process that has exited is not held")
	}
	os.WriteFile(filepath.Join(root, lock), []byte(strconv.Itoa(os.Getpid())), 0o644)
	if pidLockHeld("/", []string{filepath.Join(root, lock)}) {
		t.Fatal("this process never waits for itself")
	}
}

func TestNeedsReadsRPMsFailedDependencies(t *testing.T) {
	out := "error: Failed dependencies:\n\tcontainer-selinux is needed by (installed) podman-5:5.8.2-7.el9_8.x86_64\n\tcontainerd.io >= 1.7 is needed by (installed) k3s-1.33-1.x86_64\n"
	if got := describeNeeds(needs(out)); got != "podman-5:5.8.2-7.el9_8.x86_64 needs container-selinux; k3s-1.33-1.x86_64 needs containerd.io >= 1.7" {
		t.Fatalf("got %q", got)
	}
}

func newFakeHostPreflight(t *testing.T) Facts {
	h := newFakeHost(t)
	return Preflight(context.Background(), h.system(t), opts(""))
}

func clonePackages(h *fakeHost) map[string]bool { return clone(h.packages) }

func clone(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	return slices.Equal(trueKeys(a), trueKeys(b))
}

func trueKeys(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// snapshotWithout is snapshot without dir, relative to root.
func snapshotWithout(t *testing.T, root, dir string) map[string]string {
	s := snapshot(t, root)
	for k := range s {
		if k == dir || strings.HasPrefix(k, dir+"/") {
			delete(s, k)
		}
	}
	return s
}
