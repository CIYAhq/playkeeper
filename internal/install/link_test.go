package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// testDashboard is another Playkeeper's dashboard, accepting machines on
// loopback.
type testDashboard struct {
	*machinelink.Hub
	store  *machinelink.MemoryStore
	addr   string
	mu     sync.Mutex
	events []machinelink.EventKind
}

func startDashboard(t *testing.T) *testDashboard { return startDashboardWith(t, nil) }

// startDashboardWith is startDashboard with wrap, when set, between the
// dashboard and its store.
func startDashboardWith(t *testing.T, wrap func(*machinelink.MemoryStore) machinelink.Store) *testDashboard {
	t.Helper()
	id, err := machinelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	d := &testDashboard{store: machinelink.NewMemoryStore()}
	var store machinelink.Store = d.store
	if wrap != nil {
		store = wrap(d.store)
	}
	routes := []machinelink.Route{{Method: "GET", Pattern: "/v1/health"}}
	hub, err := machinelink.NewHub(machinelink.HubOptions{Identity: id, Store: store, Routes: routes, Version: "0.4.0", OnEvent: func(e machinelink.Event) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.events = append(d.events, e.Kind)
	}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go hub.Serve(ln)
	t.Cleanup(func() { hub.Close() })
	d.Hub, d.addr = hub, ln.Addr().String()
	return d
}

// command is what the dashboard's Connect a machine shows, with a new code.
func (d *testDashboard) command(t *testing.T) JoinOptions {
	t.Helper()
	code, _, err := d.NewJoinCode(context.Background(), "alex")
	if err != nil {
		t.Fatal(err)
	}
	return JoinOptions{Address: d.addr, Code: code, Fingerprint: d.Fingerprint(), Name: "home-server", Version: "0.4.0"}
}

func (d *testDashboard) saw(kind machinelink.EventKind) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.events, kind)
}

// machine is the one machine the dashboard knows, removed or not.
func (d *testDashboard) machine(t *testing.T) machinelink.Machine {
	t.Helper()
	ms, err := d.store.Machines(context.Background())
	if err != nil || len(ms) != 1 {
		t.Fatalf("the dashboard knows %d machines (%v)", len(ms), err)
	}
	return ms[0]
}

// losingStore is a dashboard's store whose first machine added never gets
// the dashboard's answer: lose runs before the dashboard can send it.
type losingStore struct {
	*machinelink.MemoryStore
	mu   sync.Mutex
	lose func()
}

func (s *losingStore) Pair(ctx context.Context, codeID string, m machinelink.Machine) error {
	err := s.MemoryStore.Pair(ctx, codeID, m)
	s.mu.Lock()
	lose := s.lose
	if err == nil {
		s.lose = nil
	}
	s.mu.Unlock()
	if err == nil && lose != nil {
		lose()
	}
	return err
}

// relay passes connections through to a dashboard until cut closes them.
type relay struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startRelay(t *testing.T, to string) *relay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", to)
			if err != nil {
				c.Close()
				continue
			}
			r.mu.Lock()
			r.conns = append(r.conns, c, u)
			r.mu.Unlock()
			go func() { io.Copy(u, c); u.Close() }()
			go func() { io.Copy(c, u); c.Close() }()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		r.cut()
	})
	return r
}

func (r *relay) cut() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		c.Close()
	}
	r.conns = nil
}

// noPanelAt is installedAt for a machine installed with --join: it has no
// panel, and the 'playkeeper' user runs its link.
func noPanelAt(t *testing.T, h *fakeHost, version string) config.Config {
	t.Helper()
	cfg := installedAt(t, h, version, true)
	cfg.NoPanel = true
	if err := cfg.Save(filepath.Join(h.root, ConfigDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(h.root, UnitDir, PanelUnit))
	os.RemoveAll(filepath.Join(h.root, cfg.PanelDir()))
	var m Manifest
	if err := json.Unmarshal([]byte(read(t, h, cfg.ManifestPath())), &m); err != nil {
		t.Fatal(err)
	}
	m.Units = slices.DeleteFunc(m.Units, func(u string) bool { return u == PanelUnit })
	m.FilesCreated = slices.DeleteFunc(m.FilesCreated, func(f string) bool { return f == UnitDir+"/"+PanelUnit })
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(h.root, cfg.ManifestPath()), b, 0o600); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h.root, cfg.LinkDir()), 0o700)
	h.users["playkeeper"], h.users["playkeeper-mc"] = true, true
	return cfg
}

// joinThenLose joins a dashboard that then goes away for good.
func joinThenLose(t *testing.T, sys System, cfg config.Config) *testDashboard {
	t.Helper()
	d := startDashboard(t)
	if _, err := Join(context.Background(), sys, cfg, d.command(t)); err != nil {
		t.Fatal(err)
	}
	d.Close()
	return d
}

// storedNowhere fails if a file under root holds s.
func storedNowhere(t *testing.T, root, s string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(s)) {
			t.Errorf("%s holds %q", p, s)
		}
		return nil
	})
}

func TestInstallingToJoinRunsNoDashboardAndOpensOnlyTheGamePort(t *testing.T) {
	h := newFakeHost(t)
	h.ufwActive = true
	h.listening[8443] = true
	sys := h.system(t)
	o := opts("")
	o.Yes, o.Join = true, "203.0.113.10:8443"
	f := Preflight(context.Background(), sys, o)
	if !f.OK() {
		var buf bytes.Buffer
		PrintChecks(&buf, f)
		t.Fatalf("a program on the panel port must not stop a machine that runs no panel:\n%s", buf.String())
	}
	if c := check(f, "join"); c == nil || !strings.Contains(c.Detail, "203.0.113.10:8443") || check(f, "tls") != nil {
		t.Fatalf("checks: %+v", f.Checks)
	}
	if c := check(f, "firewall"); c == nil || !strings.Contains(c.Detail, "25565/tcp") || strings.Contains(c.Detail, "8443") {
		t.Fatalf("firewall check: %+v", c)
	}
	out := &bytes.Buffer{}
	o.Out = out
	res, err := Run(context.Background(), sys, o, "test")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !res.NoPanel || res.URL != "" || res.SetupCode != "" || res.Fingerprint != "" {
		t.Fatalf("result: %+v", res)
	}
	cfg, err := config.Load(filepath.Join(h.root, ConfigDir, "config.json"))
	if err != nil || !cfg.NoPanel {
		t.Fatalf("config: %+v, %v", cfg, err)
	}
	for _, u := range []string{AgentUnit, UpdatePathUnit, UpdateServiceUnit} {
		if read(t, h, UnitDir+"/"+u) == "<missing>" {
			t.Errorf("%s is missing", u)
		}
	}
	for _, u := range []string{PanelUnit, LinkUnit} {
		if read(t, h, UnitDir+"/"+u) != "<missing>" {
			t.Errorf("%s was installed", u)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, cfg.PanelDir())); err == nil {
		t.Error("a machine without a panel got the panel's directory")
	}
	if fi, err := os.Stat(filepath.Join(h.root, cfg.LinkDir())); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the link's directory: %v, %v", fi, err)
	}
	cmds := strings.Join(h.cmds, "\n")
	for _, want := range []string{"systemctl enable --now " + AgentUnit, "systemctl enable --now " + UpdatePathUnit, "ufw allow 25565/tcp"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("missing command %q in:\n%s", want, cmds)
		}
	}
	for _, never := range []string{PanelUnit, LinkUnit, "ufw allow 8443/tcp"} {
		if strings.Contains(cmds, never) {
			t.Errorf("the install ran %q:\n%s", never, cmds)
		}
	}
	if !slices.Equal(h.panelPorts, []int{0}) {
		t.Errorf("health checked a panel on %v", h.panelPorts)
	}
	var m Manifest
	json.Unmarshal([]byte(read(t, h, cfg.ManifestPath())), &m)
	if contains(m.Units, PanelUnit) || !slices.Equal(m.FirewallRules, []string{"25565/tcp"}) {
		t.Fatalf("manifest: %+v", m)
	}
	for _, want := range []string{"runs the link to your dashboard", "playkeeper-link (dials your dashboard at 203.0.113.10:8443; no port opens for it here)", "no dashboard runs here", "ufw allow 25565/tcp"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "8443/tcp") || strings.Contains(out.String(), "certificate") {
		t.Errorf("the plan mentions the panel:\n%s", out)
	}

	h.cmds = nil
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, c := range h.cmds {
		if strings.Contains(c, PanelUnit) || strings.Contains(c, LinkUnit) {
			t.Errorf("uninstalling a machine that never joined ran %q", c)
		}
	}
}

func TestJoiningStartsTheLinkAndLeavingTellsTheDashboardFirst(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	cmd := d.command(t)
	h.cmds = nil
	joined, err := Join(ctx, sys, cfg, cmd)
	if err != nil {
		t.Fatal(err)
	}
	m := d.machine(t)
	if joined.Name != "home-server" || joined.MachineID != m.ID || m.CreatedBy != "alex" || !d.saw(machinelink.EventJoined) {
		t.Fatalf("joined as %+v; the dashboard has %+v", joined, m)
	}
	for _, p := range []string{cfg.LinkKeyPath(), cfg.LinkDashboardPath()} {
		if fi, err := os.Stat(filepath.Join(h.root, p)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", p, fi, err)
		}
	}
	if got := read(t, h, UnitDir+"/"+LinkUnit); got != LinkUnitFile(cfg) {
		t.Fatalf("the link's unit:\n%s", got)
	}
	if !slices.Equal(h.cmds, []string{"systemctl daemon-reload", "systemctl enable --now " + LinkUnit}) {
		t.Fatalf("joining ran %q", h.cmds)
	}
	if !Joined(cfg, h.root) {
		t.Fatal("the machine does not count as joined")
	}
	storedNowhere(t, h.root, cmd.Code)
	storedNowhere(t, h.root, strings.ReplaceAll(cmd.Code, "-", ""))

	key := read(t, h, cfg.LinkKeyPath())
	var already *machinelink.Error
	if _, err := Join(ctx, sys, cfg, d.command(t)); !errors.As(err, &already) || already.Code != machinelink.CodeMachineAlreadyJoined || !strings.Contains(already.Hint, "sudo playkeeper leave") {
		t.Fatalf("joining twice: %v", err)
	}
	if read(t, h, cfg.LinkKeyPath()) != key {
		t.Fatal("joining twice replaced the machine's key")
	}

	h.cmds = nil
	left, err := Leave(ctx, sys, cfg, false)
	if err != nil || left.Untold != nil || left.Dashboard.Name != "home-server" {
		t.Fatalf("leave: %+v, %v", left, err)
	}
	if m := d.machine(t); !m.Removed() || m.RevokedBy != "machine:"+m.ID || !d.saw(machinelink.EventLeft) {
		t.Fatalf("the dashboard kept the machine: %+v", m)
	}
	for _, p := range []string{cfg.LinkKeyPath(), cfg.LinkDashboardPath(), UnitDir + "/" + LinkUnit} {
		if read(t, h, p) != "<missing>" {
			t.Errorf("leaving left %s", p)
		}
	}
	if !slices.Equal(h.cmds, []string{"systemctl disable --now " + LinkUnit, "systemctl daemon-reload"}) {
		t.Fatalf("leaving ran %q", h.cmds)
	}
	if _, err := Leave(ctx, sys, cfg, false); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("leaving twice: %v", err)
	}
	if _, err := Join(ctx, sys, cfg, d.command(t)); err != nil {
		t.Fatalf("joining again with a new code: %v", err)
	}
	if read(t, h, cfg.LinkKeyPath()) == key {
		t.Fatal("joining again reused the old key")
	}
}

// A join the dashboard accepted stays a join when the link's service then
// doesn't start: the machine keeps its key and the dashboard, and the error
// says it joined and how to start the link rather than to join again.
func TestAJoinWhoseLinkDoesNotStartStaysJoined(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	h.failCmd = "systemctl enable --now " + LinkUnit
	joined, err := Join(ctx, sys, cfg, d.command(t))
	var down *LinkNotStartedError
	if !errors.As(err, &down) || down.Dashboard.Name != "home-server" || joined.MachineID != d.machine(t).ID {
		t.Fatalf("join: %+v, %v", joined, err)
	}
	if !strings.Contains(err.Error(), "joined the dashboard as home-server") || !strings.Contains(err.Error(), "sudo systemctl enable --now "+LinkUnit) {
		t.Fatalf("the error doesn't say it joined and how to start the link: %v", err)
	}
	if !Joined(cfg, h.root) || read(t, h, cfg.LinkKeyPath()) == "<missing>" {
		t.Fatal("the machine forgot a dashboard that accepted it")
	}
}

// A join whose answer is lost after the dashboard added the machine keeps
// the key the dashboard has, and another attempt that fails keeps it too:
// the same command run again finishes that join, as the same machine. A
// new command can't add the machine twice, and says so.
func TestAJoinWhoseAnswerIsLostFinishesWhenRunAgain(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	ls := &losingStore{}
	d := startDashboardWith(t, func(s *machinelink.MemoryStore) machinelink.Store { ls.MemoryStore = s; return ls })
	r := startRelay(t, d.addr)
	ls.mu.Lock()
	ls.lose = r.cut
	ls.mu.Unlock()
	cmd := d.command(t)
	cmd.Address = r.ln.Addr().String()
	h.cmds = nil

	var unfinished *UnfinishedJoinError
	if _, err := Join(ctx, sys, cfg, cmd); !errors.As(err, &unfinished) || machinelink.CodeOf(err) != machinelink.CodeJoinUnanswered {
		t.Fatalf("a join whose answer was lost: %v", err)
	}
	m := d.machine(t)
	id, err := machinelink.LoadIdentity(sys.P(cfg.LinkKeyPath()))
	if err != nil || !m.PublicKey.Equal(id.PublicKey()) {
		t.Fatalf("the machine didn't keep the key the dashboard has: %v", err)
	}
	key := read(t, h, cfg.LinkKeyPath())
	if Joined(cfg, h.root) || len(h.cmds) != 0 {
		t.Fatalf("an unfinished join counts as joined, or ran %q", h.cmds)
	}

	stranger := d.command(t)
	stranger.Code = startDashboard(t).command(t).Code
	if _, err := Join(ctx, sys, cfg, stranger); !errors.As(err, &unfinished) || machinelink.CodeOf(err) != machinelink.CodeJoinCodeWrong {
		t.Fatalf("a wrong code after a lost answer: %v", err)
	}
	var already *machinelink.Error
	if _, err := Join(ctx, sys, cfg, d.command(t)); !errors.As(err, &already) || already.Code != machinelink.CodeMachineAlreadyJoined ||
		!strings.Contains(already.Msg, "as home-server, from an earlier join this machine didn't finish") || !strings.Contains(already.Hint, "Run the command from that join again") {
		t.Fatalf("a new command after a lost answer: %v", err)
	}
	if read(t, h, cfg.LinkKeyPath()) != key {
		t.Fatal("a failed attempt dropped the key the dashboard has")
	}

	joined, err := Join(ctx, sys, cfg, cmd)
	if err != nil || joined.MachineID != m.ID || joined.Name != "home-server" {
		t.Fatalf("the same command again: %+v, %v", joined, err)
	}
	if ms, _ := d.store.Machines(ctx); len(ms) != 1 || read(t, h, cfg.LinkKeyPath()) != key || !Joined(cfg, h.root) {
		t.Fatalf("finishing the join: %d machines on the dashboard, key kept %v, joined %v", len(ms), read(t, h, cfg.LinkKeyPath()) == key, Joined(cfg, h.root))
	}
	if !slices.Equal(h.cmds, []string{"systemctl daemon-reload", "systemctl enable --now " + LinkUnit}) {
		t.Fatalf("finishing the join ran %q", h.cmds)
	}
}

// A join the dashboard accepted whose details this machine can't save
// keeps the key: once that's fixed, the same command finishes the join.
func TestAJoinThatCantSaveTheDashboardFinishesWhenRunAgain(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	cmd := d.command(t)
	dash := sys.P(cfg.LinkDashboardPath())
	sys.Chown = func(p string, _, _ int) error {
		if p == dash {
			return errors.New("operation not permitted")
		}
		return nil
	}
	_, err := Join(ctx, sys, cfg, cmd)
	var unfinished *UnfinishedJoinError
	var e *machinelink.Error
	if !errors.As(err, &unfinished) || !errors.As(err, &e) || e.Code != machinelink.CodeKeyFile ||
		!strings.Contains(e.Msg, "accepted this machine as home-server") || !strings.Contains(e.Hint, "run the same command again") {
		t.Fatalf("a join whose dashboard couldn't be saved: %v", err)
	}
	if Joined(cfg, h.root) || read(t, h, cfg.LinkKeyPath()) == "<missing>" {
		t.Fatal("the machine must keep its key and not count as joined")
	}

	sys.Chown = func(string, int, int) error { return nil }
	joined, err := Join(ctx, sys, cfg, cmd)
	if err != nil || joined.MachineID != d.machine(t).ID || !Joined(cfg, h.root) {
		t.Fatalf("the same command again: %+v, %v", joined, err)
	}
}

func TestAJoinThatFailsLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	other, err := machinelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, h.root)
	h.cmds = nil
	wrong := d.command(t)
	wrong.Fingerprint = other.Fingerprint()
	if _, err := Join(ctx, sys, cfg, wrong); machinelink.CodeOf(err) != machinelink.CodeDashboardKeyMismatch {
		t.Fatalf("joining a dashboard whose key doesn't match the fingerprint: %v", err)
	}
	refused := d.command(t)
	refused.Code = startDashboard(t).command(t).Code
	if _, err := Join(ctx, sys, cfg, refused); machinelink.CodeOf(err) != machinelink.CodeJoinCodeWrong {
		t.Fatalf("joining with a code the dashboard doesn't know: %v", err)
	}
	delete(h.users, "playkeeper")
	if _, err := Join(ctx, sys, cfg, d.command(t)); err == nil || !strings.Contains(err.Error(), "sudo playkeeper install") {
		t.Fatalf("joining without the user who runs the link: %v", err)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("failed joins changed the machine: %v", d)
	}
	if len(h.cmds) != 0 {
		t.Fatalf("failed joins ran %q", h.cmds)
	}
	if ms, _ := d.store.Machines(ctx); len(ms) != 0 {
		t.Fatalf("the dashboard accepted %+v", ms)
	}
}

func TestLeavingADashboardThatIsGoneNeedsForce(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	joinThenLose(t, sys, cfg)
	before := snapshot(t, h.root)
	h.cmds = nil
	if _, err := Leave(ctx, sys, cfg, false); machinelink.CodeOf(err) != machinelink.CodeDashboardUnreachable {
		t.Fatalf("leaving a dashboard that is gone: %v", err)
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 || len(h.cmds) != 0 {
		t.Fatalf("a leave the dashboard never heard of changed the machine: %v %q", d, h.cmds)
	}
	left, err := Leave(ctx, sys, cfg, true)
	if err != nil || machinelink.CodeOf(left.Untold) != machinelink.CodeDashboardUnreachable || left.Dashboard.Name != "home-server" {
		t.Fatalf("leave --force: %+v, %v", left, err)
	}
	for _, p := range []string{cfg.LinkKeyPath(), cfg.LinkDashboardPath(), UnitDir + "/" + LinkUnit} {
		if read(t, h, p) != "<missing>" {
			t.Errorf("leave --force left %s", p)
		}
	}
	if !slices.Contains(h.cmds, "systemctl disable --now "+LinkUnit) {
		t.Fatalf("leave --force ran %q", h.cmds)
	}
}

func TestUninstallingAJoinedMachineLeavesItsDashboardFirst(t *testing.T) {
	ctx := context.Background()
	disabled := func(h *fakeHost) []string {
		var units []string
		for _, c := range h.cmds {
			if u, ok := strings.CutPrefix(c, "systemctl disable --now "); ok {
				units = append(units, u)
			}
		}
		return units
	}
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	if _, err := Join(ctx, sys, cfg, d.command(t)); err != nil {
		t.Fatal(err)
	}
	h.cmds = nil
	out := &bytes.Buffer{}
	if err := Uninstall(ctx, sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: out}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if want := "this machine from the dashboard at " + d.addr + ", where it is home-server (it leaves first)"; !strings.Contains(out.String(), want) {
		t.Errorf("the plan does not say %q:\n%s", want, out)
	}
	if m := d.machine(t); !m.Removed() || !d.saw(machinelink.EventLeft) {
		t.Fatalf("the dashboard kept the machine: %+v", m)
	}
	if units := disabled(h); len(units) == 0 || units[0] != LinkUnit || slices.Contains(units[1:], LinkUnit) || !slices.Contains(units, AgentUnit) || slices.Contains(units, PanelUnit) {
		t.Fatalf("services disabled: %q", units)
	}
	for _, p := range []string{cfg.LinkKeyPath(), cfg.LinkDashboardPath(), UnitDir + "/" + LinkUnit, UnitDir + "/" + AgentUnit} {
		if read(t, h, p) != "<missing>" {
			t.Errorf("uninstall left %s", p)
		}
	}

	h = newFakeHost(t)
	cfg = noPanelAt(t, h, "0.4.0")
	sys = h.system(t)
	joinThenLose(t, sys, cfg)
	h.cmds = nil
	err := Uninstall(ctx, sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "was not told that this machine left") || !strings.Contains(err.Error(), "remove home-server there, in Settings › Machines") {
		t.Fatalf("uninstalling with the dashboard gone: %v", err)
	}
	if units := disabled(h); len(units) == 0 || units[0] != LinkUnit || !slices.Contains(units, AgentUnit) {
		t.Fatalf("services disabled with the dashboard gone: %q", units)
	}
	for _, p := range []string{cfg.LinkKeyPath(), cfg.LinkDashboardPath(), UnitDir + "/" + LinkUnit} {
		if read(t, h, p) != "<missing>" {
			t.Errorf("uninstall left %s with the dashboard gone", p)
		}
	}
}

func TestUpgradingAJoinedMachineRestartsItsAgentAndLink(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	if _, err := Join(ctx, sys, cfg, d.command(t)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h.root, UnitDir, LinkUnit), []byte("# "+LinkUnit+" from 0.4.0\n"), 0o644)
	bin := newBinary(t, "0.5.0")
	sys.Executable = func() (string, error) { return bin, nil }
	h.cmds, h.panelPorts = nil, nil
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	res, err := Run(ctx, sys, o, "0.5.0")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	if !res.Upgraded || !res.NoPanel || res.URL != "" || res.Fingerprint != "" {
		t.Fatalf("result: %+v", res)
	}
	if got := read(t, h, UnitDir+"/"+LinkUnit); got != LinkUnitFile(cfg) {
		t.Fatalf("the link's unit is not the new version's:\n%s", got)
	}
	if read(t, h, UnitDir+"/"+PanelUnit) != "<missing>" {
		t.Fatal("the upgrade installed a panel")
	}
	start, restart := slices.Index(h.cmds, "systemctl start "+AgentUnit), slices.Index(h.cmds, "systemctl try-restart "+LinkUnit)
	if !slices.Contains(h.cmds, "systemctl stop "+AgentUnit) || start < 0 || restart < start {
		t.Fatalf("the agent must restart, then the link: %q", h.cmds)
	}
	for _, c := range h.cmds {
		if strings.Contains(c, PanelUnit) {
			t.Errorf("the upgrade ran %q", c)
		}
	}
	if len(h.panelPorts) == 0 || slices.ContainsFunc(h.panelPorts, func(p int) bool { return p != 0 }) {
		t.Fatalf("health checked a panel on %v", h.panelPorts)
	}
	for _, want := range []string{"only the Playkeeper agent restarts", "Restarts:  the link to your dashboard, so that it runs 0.5.0 too", "restart the link to the dashboard"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the upgrade does not say %q:\n%s", want, out)
		}
	}
	var m Manifest
	json.Unmarshal([]byte(read(t, h, cfg.ManifestPath())), &m)
	if m.Version != "0.5.0" || contains(m.Units, LinkUnit) || contains(m.FilesCreated, UnitDir+"/"+LinkUnit) || contains(m.Units, PanelUnit) {
		t.Fatalf("the manifest must leave the link's unit to joining and leaving: %+v", m)
	}
}

func TestAFailedUpgradeOfAJoinedMachinePutsItsLinkBack(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost(t)
	cfg := noPanelAt(t, h, "0.4.0")
	sys := h.system(t)
	d := startDashboard(t)
	if _, err := Join(ctx, sys, cfg, d.command(t)); err != nil {
		t.Fatal(err)
	}
	old := "# " + LinkUnit + " from 0.4.0\n"
	os.WriteFile(filepath.Join(h.root, UnitDir, LinkUnit), []byte(old), 0o644)
	h.unhealthy = map[string]error{"0.5.0": errors.New("the agent did not answer")}
	bin := newBinary(t, "0.5.0")
	sys.Executable = func() (string, error) { return bin, nil }
	h.cmds = nil
	o := opts("")
	o.Yes = true
	var rolledBack *RolledBackError
	if _, err := Run(ctx, sys, o, "0.5.0"); !errors.As(err, &rolledBack) {
		t.Fatalf("a new version that never answers: %v", err)
	}
	if got := read(t, h, UnitDir+"/"+LinkUnit); got != old {
		t.Fatalf("the link's unit was not put back:\n%s", got)
	}
	lastStart, restart := -1, -1
	for i, c := range h.cmds {
		switch c {
		case "systemctl start " + AgentUnit:
			lastStart = i
		case "systemctl try-restart " + LinkUnit:
			restart = i
		}
	}
	if lastStart < 0 || restart < lastStart {
		t.Fatalf("the link must restart once the version put back runs: %q", h.cmds)
	}
	if !Joined(cfg, h.root) {
		t.Fatal("a rolled back upgrade forgot the dashboard")
	}
}

func TestUnitsHaveTheLinkOnlyWhenJoinedAndThePanelOnlyWithADashboard(t *testing.T) {
	cfg := config.Default()
	if u := Units(cfg, false); u[PanelUnit] == "" || u[AgentUnit] == "" || u[LinkUnit] != "" {
		t.Fatalf("a dashboard's units: %v", keys(u))
	}
	if u := Units(cfg, true); u[PanelUnit] == "" || u[LinkUnit] != LinkUnitFile(cfg) {
		t.Fatalf("a dashboard that joined another one: %v", keys(u))
	}
	cfg.NoPanel = true
	u := Units(cfg, true)
	if _, ok := u[PanelUnit]; ok || u[LinkUnit] == "" {
		t.Fatalf("a machine installed to join: %v", keys(u))
	}
	if err := checkUnits(u, true); err != nil {
		t.Fatalf("a machine without a panel refused its own units: %v", err)
	}
	if err := checkUnits(u, false); err == nil {
		t.Fatal("a machine with a panel must refuse units without one")
	}
	link := LinkUnitFile(cfg)
	for _, want := range []string{"User=playkeeper\n", "ConditionPathExists=" + cfg.LinkDashboardPath() + "\n", "ReadWritePaths=" + cfg.LinkDir() + "\n",
		"ExecStart=/usr/local/bin/playkeeper link --config /etc/playkeeper/config.json\n", "CapabilityBoundingSet=\n", "NoNewPrivileges=yes\n", "ProtectSystem=strict\n"} {
		if !strings.Contains(link, want) {
			t.Errorf("the link's unit lacks %q:\n%s", strings.TrimSpace(want), link)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
