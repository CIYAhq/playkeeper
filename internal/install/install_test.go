package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/docker"
)

// fakeHost is an in-memory Ubuntu 24.04 host rooted in a temp directory.
type fakeHost struct {
	mu            sync.Mutex
	root          string
	cmds          []string
	users         map[string]bool
	packages      map[string]bool
	dockerPresent bool
	listening     map[int]bool
	procs         []string
	containers    []docker.ContainerSummary
	memMB         int
	freeBytes     int64
	healthErr     error
	failCmd       string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	h := &fakeHost{root: root, users: map[string]bool{}, packages: map[string]bool{"bash": true, "coreutils": true}, listening: map[int]bool{22: true}, memMB: 3900, freeBytes: 20 << 30}
	for _, d := range []string{"/etc/systemd/system", "/lib/systemd/system", "/run/systemd/system", "/usr/local/bin", "/usr/bin", "/var/lib", "/var/run"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "/etc/os-release"), []byte("NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "/usr/bin/apt-get"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(root, "/etc/group"), []byte("root:x:0:\n"), 0o644)
	os.WriteFile(filepath.Join(root, "/lib/systemd/system/ssh.service"), []byte("[Unit]\n"), 0o644)
	return h
}

func (h *fakeHost) system(t *testing.T) System {
	bin := filepath.Join(t.TempDir(), "playkeeper")
	os.WriteFile(bin, []byte("binary"), 0o755)
	return System{
		Root: h.root,
		Run: func(name string, args ...string) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			line := strings.TrimSpace(name + " " + strings.Join(args, " "))
			h.cmds = append(h.cmds, line)
			if h.failCmd != "" && strings.HasPrefix(line, h.failCmd) {
				return "", errors.New("simulated failure: " + line)
			}
			switch {
			case name == "ufw":
				return "Status: inactive\n", nil
			case name == "dpkg-query":
				var list []string
				for p := range h.packages {
					list = append(list, p)
				}
				return strings.Join(list, "\n") + "\n", nil
			case name == "apt-get" && len(args) > 0 && args[0] == "install":
				for _, p := range []string{"docker.io", "containerd", "runc", "pigz"} {
					h.packages[p] = true
				}
				h.dockerPresent = true
				appendLine(filepath.Join(h.root, "/etc/group"), "docker:x:999:")
			case name == "apt-get" && len(args) > 0 && args[0] == "purge":
				for _, p := range args[2:] {
					delete(h.packages, p)
				}
				h.dockerPresent = false
			case name == "groupadd":
				appendLine(filepath.Join(h.root, "/etc/group"), args[len(args)-1]+":x:990:")
			case name == "groupdel":
				removeLine(filepath.Join(h.root, "/etc/group"), args[0]+":")
			case name == "useradd":
				h.users[args[len(args)-1]] = true
			case name == "userdel":
				delete(h.users, args[0])
			}
			return "", nil
		},
		IsRoot:     func() bool { return true },
		Arch:       func() string { return "amd64" },
		MemTotalMB: func() int { return h.memMB },
		DiskFree:   func(string) int64 { return h.freeBytes },
		Listening:  func(p int) bool { return h.listening[p] },
		Processes:  func() []string { return h.procs },
		LookupUser: func(name string) (int, int, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return os.Getuid(), os.Getgid(), h.users[name]
		},
		Docker: func(context.Context) (DockerInfo, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.dockerPresent {
				return DockerInfo{}, errors.New("no docker")
			}
			return DockerInfo{Version: "27.5.1", Containers: h.containers}, nil
		},
		Executable:  func() (string, error) { return bin, nil },
		Chown:       func(string, int, int) error { return nil },
		Now:         time.Now,
		WaitHealthy: func(context.Context, string, string, int) error { return h.healthErr },
	}
}

func appendLine(path, line string) {
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, []byte(line+"\n")...), 0o644)
}

func removeLine(path, prefix string) {
	b, _ := os.ReadFile(path)
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if !strings.HasPrefix(l, prefix) {
			keep = append(keep, l)
		}
	}
	os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

// snapshot hashes every file and directory under root.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, _ := os.ReadFile(p)
		s := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(s[:])
		return nil
	})
	return out
}

func diff(a, b map[string]string) []string {
	var d []string
	for k, v := range a {
		if b[k] != v {
			d = append(d, "changed/removed "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			d = append(d, "added "+k)
		}
	}
	sort.Strings(d)
	return d
}

func opts(in string) Options {
	return Options{PanelPort: 8443, GamePort: 25565, In: strings.NewReader(in), Out: &bytes.Buffer{}}
}

func check(f Facts, id string) *Check {
	for i := range f.Checks {
		if f.Checks[i].ID == id || strings.HasPrefix(f.Checks[i].ID, id) {
			return &f.Checks[i]
		}
	}
	return nil
}

func TestPreflightPassesOnCleanHost(t *testing.T) {
	h := newFakeHost(t)
	f := Preflight(context.Background(), h.system(t), opts(""))
	if !f.OK() {
		var buf bytes.Buffer
		PrintChecks(&buf, f)
		t.Fatalf("clean host failed preflight:\n%s", buf.String())
	}
	if c := check(f, "docker"); c == nil || c.Status != "info" {
		t.Fatalf("missing Docker should be planned, got %+v", c)
	}
}

func TestPreflightRefusesEachCollisionWithAFix(t *testing.T) {
	cases := map[string]struct {
		setup func(h *fakeHost)
		id    string
	}{
		"game port in use":  {func(h *fakeHost) { h.listening[25565] = true }, "port-25565"},
		"panel port in use": {func(h *fakeHost) { h.listening[8443] = true }, "port-8443"},
		"minecraft.service": {func(h *fakeHost) {
			os.WriteFile(filepath.Join(h.root, "/etc/systemd/system/minecraft.service"), []byte("[Service]\n"), 0o644)
		}, "existing"},
		"crafty install":      {func(h *fakeHost) { os.MkdirAll(filepath.Join(h.root, "/var/opt/minecraft/crafty"), 0o755) }, "existing"},
		"running java server": {func(h *fakeHost) { h.procs = []string{"java -Xmx2G -jar paper-1.21.11.jar nogui"} }, "existing"},
		"other mc container": {func(h *fakeHost) {
			h.dockerPresent = true
			h.containers = []docker.ContainerSummary{{Names: []string{"/mc"}, Image: "itzg/minecraft-server"}}
		}, "existing"},
		"low disk":   {func(h *fakeHost) { h.freeBytes = 1 << 30 }, "disk"},
		"low memory": {func(h *fakeHost) { h.memMB = 1900 }, "memory"},
		"unsupported distro": {func(h *fakeHost) {
			os.WriteFile(filepath.Join(h.root, "/etc/os-release"), []byte("ID=debian\nVERSION_ID=\"12\"\n"), 0o644)
		}, "os"},
		"no systemd": {func(h *fakeHost) { os.RemoveAll(filepath.Join(h.root, "/run/systemd/system")) }, "systemd"},
		"already installed": {func(h *fakeHost) {
			os.MkdirAll(filepath.Join(h.root, ConfigDir), 0o755)
			os.WriteFile(filepath.Join(h.root, ConfigDir, "config.json"), []byte("{}"), 0o644)
		}, "installed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost(t)
			c.setup(h)
			before := snapshot(t, h.root)
			sys := h.system(t)
			f := Preflight(context.Background(), sys, opts(""))
			if f.OK() {
				t.Fatal("preflight passed despite the collision")
			}
			ch := check(f, c.id)
			if ch == nil || ch.Status != "fail" || ch.Fix == "" {
				t.Fatalf("check %s: %+v", c.id, ch)
			}
			_, err := Run(context.Background(), sys, Options{PanelPort: 8443, GamePort: 25565, Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}, "test")
			if err == nil {
				t.Fatal("install proceeded despite failed preflight")
			}
			if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
				t.Fatalf("refused install changed the host: %v", d)
			}
			for _, cmd := range h.cmds {
				if !strings.HasPrefix(cmd, "ufw status") {
					t.Fatalf("refused install ran %q", cmd)
				}
			}
		})
	}
}

func TestExistingMinecraftCanBeAllowedButIsNeverTouched(t *testing.T) {
	h := newFakeHost(t)
	unit := filepath.Join(h.root, "/etc/systemd/system/minecraft.service")
	os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/bin/java -jar server.jar\n"), 0o644)
	os.MkdirAll(filepath.Join(h.root, "/srv/minecraft/world"), 0o755)
	os.WriteFile(filepath.Join(h.root, "/srv/minecraft/world/level.dat"), []byte("their world"), 0o644)
	h.listening[25565] = true
	o := Options{PanelPort: 8443, GamePort: 25566, Yes: true, AllowExistingMinecraft: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}
	theirs := func() string {
		a, _ := os.ReadFile(unit)
		b, _ := os.ReadFile(filepath.Join(h.root, "/srv/minecraft/world/level.dat"))
		s := sha256.Sum256(append(a, b...))
		return hex.EncodeToString(s[:])
	}
	before := theirs()
	sys := h.system(t)
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatalf("install with --allow-existing-minecraft on a free port: %v", err)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if theirs() != before {
		t.Fatal("the existing Minecraft service or world changed")
	}
	for _, cmd := range h.cmds {
		if strings.Contains(cmd, "minecraft.service") {
			t.Fatalf("installer touched the existing unit: %q", cmd)
		}
	}
}

func TestDecliningChangesNothing(t *testing.T) {
	h := newFakeHost(t)
	before := snapshot(t, h.root)
	o := opts("n\n")
	out := &bytes.Buffer{}
	o.Out = out
	_, err := Run(context.Background(), h.system(t), o, "test")
	if !errors.Is(err, errDeclined) {
		t.Fatalf("got %v", err)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("declining changed the host: %v", d)
	}
	text := out.String()
	for _, want := range []string{"docker.io", "playkeeper-mc", BinPath, AgentUnit, "8443/tcp", "sudo playkeeper uninstall"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan does not mention %q:\n%s", want, text)
		}
	}
}

func TestInjectedFailureRollsBackCompletely(t *testing.T) {
	for _, step := range []string{"install and start systemd services", "write install manifest", "create directories"} {
		t.Run(step, func(t *testing.T) {
			h := newFakeHost(t)
			before := snapshot(t, h.root)
			pkgs := len(h.packages)
			t.Setenv(FailStepEnv, step)
			o := opts("")
			o.Yes = true
			_, err := Run(context.Background(), h.system(t), o, "test")
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("expected injected failure, got %v", err)
			}
			if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
				t.Fatalf("rollback left changes: %v", d)
			}
			if len(h.users) != 0 {
				t.Fatalf("rollback left users: %v", h.users)
			}
			if len(h.packages) != pkgs || h.dockerPresent {
				t.Fatalf("rollback left packages: %v", h.packages)
			}
		})
	}
}

func TestHealthFailureRollsBack(t *testing.T) {
	h := newFakeHost(t)
	h.healthErr = errors.New("panel did not answer")
	before := snapshot(t, h.root)
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil {
		t.Fatal("install must fail when services are unhealthy")
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("rollback left changes: %v", d)
	}
}

func TestInstallManifestUninstallKeepsWorldsAndPurgeNeedsConfirmation(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	o := opts("")
	o.Yes = true
	res, err := Run(context.Background(), sys, o, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.SetupCode == "" || res.Fingerprint == "" || !strings.HasPrefix(res.URL, "https://") {
		t.Fatalf("result: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(h.root, "/var/lib/playkeeper/install-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	json.Unmarshal(b, &m)
	for _, want := range []string{BinPath, ConfigDir + "/config.json", UnitDir + "/" + AgentUnit, UnitDir + "/" + PanelUnit} {
		if !contains(m.FilesCreated, want) {
			t.Errorf("manifest misses %s: %+v", want, m.FilesCreated)
		}
	}
	if !contains(m.PackagesInstalled, "docker.io") || !contains(m.UsersCreated, "playkeeper") || !contains(m.UsersCreated, "playkeeper-mc") {
		t.Fatalf("manifest: %+v", m)
	}
	token, _ := os.ReadFile(filepath.Join(h.root, "/var/lib/playkeeper/panel/setup-token.sha256"))
	if strings.Contains(string(token), res.SetupCode) {
		t.Fatal("the setup code must only be stored hashed")
	}
	unit, _ := os.ReadFile(filepath.Join(h.root, UnitDir, PanelUnit))
	if !strings.Contains(string(unit), "User=playkeeper") || strings.Contains(string(unit), "docker") {
		t.Fatalf("panel unit must run unprivileged without Docker access:\n%s", unit)
	}
	world := filepath.Join(h.root, "/var/lib/playkeeper/server/data/world/level.dat")
	os.MkdirAll(filepath.Dir(world), 0o755)
	os.WriteFile(world, []byte("precious world"), 0o644)
	bk := filepath.Join(h.root, "/var/lib/playkeeper/backups/playkeeper-world-1.tar.gz")
	os.WriteFile(bk, []byte("backup bytes"), 0o600)
	keep := func() string {
		a, _ := os.ReadFile(world)
		b, _ := os.ReadFile(bk)
		return string(a) + "|" + string(b)
	}
	before := keep()
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, Purge: true, In: strings.NewReader("nope\n"), Out: &bytes.Buffer{}}); err == nil {
		t.Fatal("purge without the typed phrase must be refused")
	}
	if keep() != before {
		t.Fatal("a refused purge changed data")
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range m.FilesCreated {
		if _, err := os.Stat(filepath.Join(h.root, f)); err == nil {
			t.Errorf("uninstall left %s", f)
		}
	}
	if len(h.users) != 0 || h.dockerPresent {
		t.Fatalf("uninstall left users %v / docker %v", h.users, h.dockerPresent)
	}
	if keep() != before {
		t.Fatal("uninstall changed the world or backups")
	}
	f := Preflight(context.Background(), sys, opts(""))
	if c := check(f, "reuse"); c == nil || !f.OK() {
		t.Fatalf("reinstall should reuse data: %+v", f.Checks)
	}
	if _, err := Run(context.Background(), sys, o, "test"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if keep() != before {
		t.Fatal("reinstall changed the world or backups")
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, Purge: true, In: strings.NewReader(PurgePhrase + "\n"), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "/var/lib/playkeeper")); err == nil {
		t.Fatal("confirmed purge must delete /var/lib/playkeeper")
	}
	_ = fmt.Sprint()
}
