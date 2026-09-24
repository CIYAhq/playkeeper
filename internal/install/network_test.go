package install

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// The host's own rules before Playkeeper: a VPN forward rule, a NAT rule for a
// lab network and a ufw-style chain. None of them may be touched.
const hostRulesV4 = `*raw
:PREROUTING ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
COMMIT
*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:ufw-user-input - [0:0]
-A INPUT -j ufw-user-input
-A FORWARD -i wg0 -j ACCEPT
-A ufw-user-input -p tcp --dport 22 -j ACCEPT
-A ufw-user-input -m comment --comment "their own rule" -j RETURN
COMMIT
*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
-A POSTROUTING -s 198.51.100.0/24 -o eth0 -j MASQUERADE
COMMIT
`

const hostRulesV6 = `*filter
:INPUT ACCEPT [0:0]
:FORWARD ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
COMMIT
*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
COMMIT
`

// What Docker 29 (Ubuntu 24.04's docker.io) adds when its daemon starts and
// a container publishes a port, as iptables-save prints it.
const dockerRulesV4 = `*raw
-A PREROUTING -d 172.17.0.2/32 ! -i docker0 -j DROP
COMMIT
*filter
:FORWARD DROP [0:0]
:DOCKER - [0:0]
:DOCKER-BRIDGE - [0:0]
:DOCKER-CT - [0:0]
:DOCKER-FORWARD - [0:0]
:DOCKER-INTERNAL - [0:0]
:DOCKER-USER - [0:0]
-A FORWARD -j DOCKER-USER
-A FORWARD -j DOCKER-FORWARD
-A DOCKER ! -i docker0 -o docker0 -j DROP
-A DOCKER-BRIDGE -o docker0 -j DOCKER
-A DOCKER-CT -o docker0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
-A DOCKER-FORWARD -j DOCKER-CT
-A DOCKER-FORWARD -j DOCKER-INTERNAL
-A DOCKER-FORWARD -j DOCKER-BRIDGE
-A DOCKER-FORWARD -i docker0 -j ACCEPT
COMMIT
*nat
:DOCKER - [0:0]
-A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER
-A OUTPUT ! -d 127.0.0.0/8 -m addrtype --dst-type LOCAL -j DOCKER
-A POSTROUTING -s 172.17.0.0/16 ! -o docker0 -j MASQUERADE
-A POSTROUTING -s 172.18.0.0/16 ! -o br-3f2a9c81d0e4 -j MASQUERADE
-A DOCKER -i docker0 -j RETURN
COMMIT
`

const dockerRulesV6 = `*filter
:DOCKER - [0:0]
:DOCKER-FORWARD - [0:0]
:DOCKER-USER - [0:0]
-A FORWARD -j DOCKER-USER
-A FORWARD -j DOCKER-FORWARD
COMMIT
*nat
:DOCKER - [0:0]
-A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER
-A OUTPUT ! -d ::1/128 -m addrtype --dst-type LOCAL -j DOCKER
COMMIT
`

// fakeFirewall models one address family's iptables: tables of chains in
// order, each with a policy ("-" for user chains) and rules.
type fakeFirewall struct{ tables []*fakeTable }

type fakeTable struct {
	name   string
	chains []*fakeChain
}

type fakeChain struct {
	name, policy string
	rules        []string
}

func parseSave(s string) *fakeFirewall {
	fw := &fakeFirewall{}
	fw.merge(s)
	return fw
}

// merge applies iptables-save text: new chains are added, a policy on an
// existing chain replaces its policy, and rules are appended.
func (fw *fakeFirewall) merge(s string) {
	var t *fakeTable
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "*"):
			t = fw.table(line[1:], true)
		case strings.HasPrefix(line, ":"):
			f := strings.Fields(line[1:])
			if c := t.chain(f[0]); c != nil {
				c.policy = f[1]
			} else {
				t.chains = append(t.chains, &fakeChain{name: f[0], policy: f[1]})
			}
		case strings.HasPrefix(line, "-A "):
			name, rule, _ := strings.Cut(line[3:], " ")
			c := t.chain(name)
			c.rules = append(c.rules, rule)
		}
	}
}

func (fw *fakeFirewall) table(name string, create bool) *fakeTable {
	for _, t := range fw.tables {
		if t.name == name {
			return t
		}
	}
	if !create {
		return nil
	}
	t := &fakeTable{name: name}
	fw.tables = append(fw.tables, t)
	return t
}

func (t *fakeTable) chain(name string) *fakeChain {
	for _, c := range t.chains {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (fw *fakeFirewall) save() string {
	var b strings.Builder
	for _, t := range fw.tables {
		fmt.Fprintf(&b, "*%s\n", t.name)
		for _, c := range t.chains {
			fmt.Fprintf(&b, ":%s %s [0:0]\n", c.name, c.policy)
		}
		for _, c := range t.chains {
			for _, r := range c.rules {
				fmt.Fprintf(&b, "-A %s %s\n", c.name, r)
			}
		}
		b.WriteString("COMMIT\n")
	}
	return b.String()
}

// run handles the iptables commands the installer uses: -S, -D, -F, -X, -P.
func (fw *fakeFirewall) run(args []string) (string, error) {
	table := "filter"
	if len(args) >= 2 && args[0] == "-t" {
		table, args = args[1], args[2:]
	}
	t := fw.table(table, false)
	if t == nil || len(args) < 2 || t.chain(args[1]) == nil {
		return "", fmt.Errorf("iptables: no chain/target/match by that name: %v", args)
	}
	c := t.chain(args[1])
	switch args[0] {
	case "-S":
		out := fmt.Sprintf("-P %s %s\n", c.name, c.policy)
		for _, r := range c.rules {
			out += "-A " + c.name + " " + r + "\n"
		}
		return out, nil
	case "-D":
		for i, r := range c.rules {
			if reflect.DeepEqual(splitRule(r), args[2:]) {
				c.rules = append(c.rules[:i], c.rules[i+1:]...)
				return "", nil
			}
		}
		return "", errors.New("iptables: Bad rule (does a matching rule exist in that chain?)")
	case "-F":
		c.rules = nil
	case "-X":
		for _, other := range t.chains {
			for _, r := range other.rules {
				if strings.Contains(r+" ", "-j "+c.name+" ") {
					return "", fmt.Errorf("iptables: Too many links: %s is used by %s", c.name, other.name)
				}
			}
		}
		for i := range t.chains {
			if t.chains[i] == c {
				t.chains = append(t.chains[:i], t.chains[i+1:]...)
				break
			}
		}
	case "-P":
		c.policy = args[2]
	}
	return "", nil
}

func forwarding(t *testing.T, h *fakeHost) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(h.root, "/proc/sys/net/ipv4/ip_forward"))
	return strings.TrimSpace(string(b))
}

func installed(t *testing.T, h *fakeHost, sys System) {
	t.Helper()
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatal(err)
	}
}

func readManifest(t *testing.T, h *fakeHost) (*Manifest, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, "/var/lib/playkeeper/install-manifest.json"))
	if err != nil {
		return nil, false
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return &m, true
}

func TestUninstallPutsDockersNetworkChangesBack(t *testing.T) {
	h := newFakeHost(t)
	fw4, fw6 := h.fw4.save(), h.fw6.save()
	sys := h.system(t)
	installed(t, h, sys)
	if h.fw4.save() == fw4 || forwarding(t, h) != "1" {
		t.Fatal("the fake Docker did not change the network; the test proves nothing")
	}
	m, _ := readManifest(t, h)
	if m.NetBeforeDocker == nil || *m.NetBeforeDocker != (NetSettings{IPv4Forward: "0", IPv6Forward: "0", ForwardPolicy: "ACCEPT", Forward6Policy: "ACCEPT"}) {
		t.Fatalf("the manifest must record the network as it was before Docker: %+v", m.NetBeforeDocker)
	}
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := h.fw4.save(); got != fw4 {
		t.Fatalf("iptables after uninstall:\n%s\nwant (as before the install):\n%s", got, fw4)
	}
	if got := h.fw6.save(); got != fw6 {
		t.Fatalf("ip6tables after uninstall:\n%s\nwant:\n%s", got, fw6)
	}
	if forwarding(t, h) != "0" {
		t.Fatal("IP forwarding must be off again, as before the install")
	}
	if _, err := os.Stat(filepath.Join(h.root, "/sys/class/net/docker0")); err == nil {
		t.Fatal("the docker0 bridge must be deleted")
	}
	if !strings.Contains(out.String(), "Docker's firewall rules and bridges") {
		t.Fatalf("the uninstall plan must say it undoes Docker's network changes:\n%s", out)
	}
}

func TestKeptDockerKeepsItsNetwork(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	installed(t, h, sys)
	withDocker := h.fw4.save()
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, KeepDocker: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatal(err)
	}
	if h.fw4.save() != withDocker || forwarding(t, h) != "1" {
		t.Fatal("a Docker that stays installed must keep its rules and forwarding")
	}
	if !strings.Contains(out.String(), "firewall rules and docker0 bridge") {
		t.Fatalf("the output must say Docker's network changes stay:\n%s", out)
	}
}

func TestUninstallWaitsForThePackageLockBeforeChangingAnything(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	installed(t, h, sys)
	h.cmds = nil
	h.lockPolls = 4
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "waiting for another package manager") {
		t.Fatalf("the wait must be visible:\n%s", out)
	}
	if h.lockFreedAt != 0 {
		t.Fatalf("uninstall ran %v while apt's lock was held", h.cmds[:h.lockFreedAt])
	}
	if _, ok := readManifest(t, h); ok || h.dockerPresent {
		t.Fatal("after the wait the uninstall must finish, Docker included")
	}

	h = newFakeHost(t)
	sys = h.system(t)
	installed(t, h, sys)
	h.cmds = nil
	h.lockForever = true
	err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("a lock that is never released must stop the uninstall before any change: %v", err)
	}
	if len(h.cmds) != 0 {
		t.Fatalf("uninstall changed things while waiting: %v", h.cmds)
	}
	if _, ok := readManifest(t, h); !ok {
		t.Fatal("the manifest must stay")
	}
}

func TestUninstallKeepsItsManifestUntilDockerIsRemoved(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	installed(t, h, sys)
	fw4 := parseSave(hostRulesV4).save()
	h.failCmd = "apt-get -o DPkg::Lock::Timeout=60 purge"
	err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "run `sudo playkeeper uninstall` again") || !strings.Contains(err.Error(), "sudo apt-get purge -y containerd docker.io pigz runc") {
		t.Fatalf("a failed Docker purge must say how to finish: %v", err)
	}
	m, ok := readManifest(t, h)
	if !ok || !contains(m.PackagesInstalled, "docker.io") || !reflect.DeepEqual(m.FilesCreated, []string{BinPath}) || len(m.Units) != 0 || len(m.UsersCreated) != 0 {
		t.Fatalf("the manifest must remain, listing only what is left: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(h.root, BinPath)); err != nil {
		t.Fatal("the playkeeper binary must stay so the uninstall can be run again")
	}
	if _, err := os.Stat(filepath.Join(h.root, UnitDir, AgentUnit)); err == nil {
		t.Fatal("everything except Docker should already be removed")
	}

	h.failCmd = ""
	out := &bytes.Buffer{}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("second uninstall: %v\n%s", err, out)
	}
	if _, ok := readManifest(t, h); ok || h.dockerPresent {
		t.Fatal("the second run must remove Docker and then the manifest")
	}
	if _, err := os.Stat(filepath.Join(h.root, BinPath)); err == nil {
		t.Fatal("the second run must remove the binary")
	}
	if h.fw4.save() != fw4 || forwarding(t, h) != "0" {
		t.Fatal("Docker's network changes must be undone")
	}
}

func TestInstallWaitsForThePackageLock(t *testing.T) {
	h := newFakeHost(t)
	h.lockPolls = 3
	h.aptLockErrors = 1 // someone else takes the lock between the check and apt-get
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "waiting for another package manager") {
		t.Fatalf("the wait must be visible:\n%s", out)
	}
	var updates []int
	for i, c := range h.cmds {
		if strings.HasPrefix(c, "apt-get") {
			if !strings.Contains(c, "-o DPkg::Lock::Timeout=") {
				t.Fatalf("apt-get must also wait for dpkg's lock itself: %q", c)
			}
			if strings.HasSuffix(c, " update") {
				updates = append(updates, i)
			}
		}
	}
	if len(updates) != 2 || updates[0] < h.lockFreedAt {
		t.Fatalf("apt-get update must run after the lock is free and be retried after a lock error: %v (lock freed at %d)", h.cmds, h.lockFreedAt)
	}
}

func TestDockerRulesFindsOnlyDockersRules(t *testing.T) {
	// iptables-save from a host running Docker 29 next to a KVM lab bridge.
	save := strings.Join([]string{"*filter", ":INPUT ACCEPT [0:0]", ":FORWARD ACCEPT [0:0]", ":DOCKER - [0:0]", ":DOCKER-USER - [0:0]",
		"-A FORWARD -o pkbr0 -j ACCEPT", "-A FORWARD -j DOCKER-USER", "-A DOCKER ! -i docker0 -o docker0 -j DROP", "COMMIT",
		"*nat", ":POSTROUTING ACCEPT [0:0]", ":DOCKER - [0:0]", "-A POSTROUTING -s 172.17.0.0/16 ! -o docker0 -j MASQUERADE",
		"-A POSTROUTING -s 198.51.100.0/24 -o enp0s5 -j MASQUERADE", `-A POSTROUTING -m comment --comment "a b" -o br-0123456789ab -j MASQUERADE`, "COMMIT"}, "\n")
	chains, rules := dockerRules(save)
	var got []string
	for _, c := range chains {
		got = append(got, c.table+"/"+c.chain)
	}
	for _, r := range rules {
		got = append(got, r.table+"/"+r.chain+" "+strings.Join(r.args, "|"))
	}
	want := []string{"filter/DOCKER", "filter/DOCKER-USER", "nat/DOCKER",
		"filter/FORWARD -j|DOCKER-USER", "nat/POSTROUTING -s|172.17.0.0/16|!|-o|docker0|-j|MASQUERADE",
		"nat/POSTROUTING -m|comment|--comment|a b|-o|br-0123456789ab|-j|MASQUERADE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestLockHeldSeesAnotherProcessesLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock-frontend")
	if err := os.WriteFile(p, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if lockHeld([]string{p, p + ".missing"}) {
		t.Fatal("nobody holds the lock yet")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsLock$")
	cmd.Env = append(os.Environ(), "PK_HOLD_LOCK="+p)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper did not take the lock: %q", line)
	}
	if !lockHeld([]string{p}) {
		t.Fatal("a lock held by another process was not seen")
	}
	stdin.Close()
	cmd.Wait()
	if lockHeld([]string{p}) {
		t.Fatal("the lock is released when that process exits")
	}
}

// TestHelperHoldsLock is run as a separate process by
// TestLockHeldSeesAnotherProcessesLock; it holds an fcntl lock like apt does.
func TestHelperHoldsLock(t *testing.T) {
	p := os.Getenv("PK_HOLD_LOCK")
	if p == "" {
		t.Skip("helper process")
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		os.Exit(2)
	}
	lk := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLKW, &lk); err != nil {
		os.Exit(3)
	}
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
