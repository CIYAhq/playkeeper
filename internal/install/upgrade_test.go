package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/panel"
	"github.com/CIYAhq/playkeeper/internal/update"
)

// installedAt puts an existing Playkeeper install on the fake host, the way
// the given version's installer left it: the binary, config, manifest,
// services, databases, a world and a backup.
func installedAt(t *testing.T, h *fakeHost, version string, withUpdater bool) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.InstallID = "install-0001"
	w := func(p, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(h.root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	w(BinPath, "playkeeper "+version+" (installed)\n", 0o755)
	os.MkdirAll(filepath.Join(h.root, ConfigDir), 0o755)
	if err := cfg.Save(filepath.Join(h.root, ConfigDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	units := []string{AgentUnit, PanelUnit}
	if withUpdater {
		units = unitNames
	}
	files := []string{BinPath, ConfigDir + "/config.json"}
	for _, u := range units {
		w(UnitDir+"/"+u, "# "+u+" from "+version+"\n", 0o644)
		files = append(files, UnitDir+"/"+u)
	}
	m := Manifest{Version: version, InstallID: cfg.InstallID, PanelPort: 8443, GamePort: 25565, Units: units, FilesCreated: files, UsersCreated: []string{"playkeeper", "playkeeper-mc"}}
	b, _ := json.MarshalIndent(m, "", "  ")
	w(cfg.ManifestPath(), string(b), 0o600)
	w(cfg.DataDir+"/server/data/world/level.dat", "my world", 0o640)
	w(cfg.DataDir+"/backups/playkeeper-world-1.tar.gz", "a backup", 0o600)
	w(cfg.DataDir+"/agent/agent.db", "agent db", 0o600)
	w(cfg.DataDir+"/agent/agent.db-wal", "agent wal", 0o600)
	w(cfg.DataDir+"/panel/panel.db", "panel db with the admin account", 0o600)
	if _, err := panel.EnsureSelfSignedCert(filepath.Join(h.root, cfg.TLSDir()), time.Now()); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newBinary(t *testing.T, version string) string {
	p := filepath.Join(t.TempDir(), "playkeeper")
	os.WriteFile(p, []byte("playkeeper "+version+" (new)\n"), 0o755)
	return p
}

func read(t *testing.T, h *fakeHost, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// userData hashes what an upgrade must never change.
func userData(t *testing.T, h *fakeHost, cfg config.Config) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range snapshot(t, filepath.Join(h.root, cfg.DataDir)) {
		if !strings.HasPrefix(k, "agent/update") && !strings.HasPrefix(k, "install-manifest") {
			out[k] = v
		}
	}
	return out
}

func TestOneLinerUpgradesA010InstallInPlace(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.1.0", false)
	before := userData(t, h, cfg)
	sys := h.system(t)
	bin := newBinary(t, "0.2.0")
	sys.Executable = func() (string, error) { return bin, nil }
	o := opts("")
	o.Yes, o.ReleaseURL = true, "http://127.0.0.1:8765"
	res, err := Run(context.Background(), sys, o, "0.2.0")
	if err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, o.Out)
	}
	if !res.Upgraded || res.FromVersion != "0.1.0" || res.Fingerprint == "" {
		t.Fatalf("result: %+v", res)
	}
	if got := read(t, h, BinPath); got != "playkeeper 0.2.0 (new)\n" {
		t.Fatalf("binary not replaced: %q", got)
	}
	for name, content := range Units(cfg) {
		if got := read(t, h, UnitDir+"/"+name); got != content {
			t.Errorf("%s is not the new version's unit:\n%s", name, got)
		}
	}
	after := userData(t, h, cfg)
	if d := diff(before, after); len(d) != 0 {
		t.Fatalf("an upgrade must keep worlds, backups, databases and certificates: %v", d)
	}
	got, err := config.Load(filepath.Join(h.root, ConfigDir, "config.json"))
	if err != nil || got.InstallID != cfg.InstallID || got.PanelPort != 8443 || got.ReleaseURL != "http://127.0.0.1:8765" {
		t.Fatalf("config must be kept, with the release location added: %+v %v", got, err)
	}
	var m Manifest
	json.Unmarshal([]byte(read(t, h, cfg.ManifestPath())), &m)
	if m.Version != "0.2.0" || !contains(m.Units, UpdatePathUnit) || !contains(m.FilesCreated, UnitDir+"/"+UpdateServiceUnit) {
		t.Fatalf("manifest must record the new version and units for uninstall: %+v", m)
	}
	cmds := strings.Join(h.cmds, "\n")
	for _, want := range []string{"systemctl stop playkeeper-panel.service playkeeper-agent.service", "systemctl enable --now playkeeper-update.path", "systemctl start playkeeper-agent.service playkeeper-panel.service"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("missing command %q in:\n%s", want, cmds)
		}
	}
	for _, never := range []string{"apt-get", "useradd", "docker"} {
		if strings.Contains(cmds, never) {
			t.Errorf("an upgrade must not run %s:\n%s", never, cmds)
		}
	}
	if len(h.healthChecks) == 0 || h.healthChecks[0] != "0.2.0" {
		t.Fatalf("the new version must be checked: %v", h.healthChecks)
	}
	if read(t, h, PreviousDir(cfg)+"/playkeeper") != "playkeeper 0.1.0 (installed)\n" {
		t.Fatal("a copy of the previous version must be kept")
	}
	if !strings.Contains(o.Out.(*bytes.Buffer).String(), "Keeps:") {
		t.Fatalf("the plan must say what is kept:\n%s", o.Out)
	}
}

func TestUnhealthyUpgradePutsTheOldVersionBack(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.1.0", false)
	before := snapshot(t, h.root)
	h.unhealthy = map[string]error{"0.2.0": errors.New("the agent exited")}
	starts := 0
	h.onStart = func() {
		if starts++; starts == 1 {
			// The new version migrates its database before it fails.
			os.WriteFile(filepath.Join(h.root, cfg.DataDir, "agent/agent.db"), []byte("migrated by 0.2.0"), 0o600)
			os.WriteFile(filepath.Join(h.root, cfg.DataDir, "agent/agent.db-shm"), []byte("new shm"), 0o600)
		}
	}
	sys := h.system(t)
	bin := newBinary(t, "0.2.0")
	sys.Executable = func() (string, error) { return bin, nil }
	o := opts("")
	o.Yes = true
	_, err := Run(context.Background(), sys, o, "0.2.0")
	var rb *RolledBackError
	if !errors.As(err, &rb) || rb.Version != "0.1.0" || !strings.Contains(err.Error(), "the agent exited") {
		t.Fatalf("want a rollback to 0.1.0, got %v", err)
	}
	after := snapshot(t, h.root)
	var d []string
	for _, x := range diff(before, after) {
		if !strings.Contains(x, "agent/update") {
			d = append(d, x)
		}
	}
	if len(d) != 0 {
		t.Fatalf("after the rollback the host must be as before (binary, services, config, databases): %v", d)
	}
	if strings.Join(h.healthChecks, " ") != "0.2.0 0.1.0 0.1.0" {
		t.Fatalf("the old version must be checked after the rollback: %v", h.healthChecks)
	}
	if !strings.Contains(strings.Join(h.cmds, "\n"), "systemctl disable --now playkeeper-update.path") {
		t.Fatal("the updater trigger that 0.1.0 did not have must be removed again")
	}
}

func TestInstallerNeverGoesBackAndLeavesTheCurrentVersionAlone(t *testing.T) {
	for _, tc := range []struct {
		installed, want string
		upToDate        bool
	}{{"0.3.0", "older than the installed 0.3.0", false}, {"0.2.0", "", true}} {
		h := newFakeHost(t)
		installedAt(t, h, tc.installed, true)
		before := snapshot(t, h.root)
		sys := h.system(t)
		bin := newBinary(t, "0.2.0")
		sys.Executable = func() (string, error) { return bin, nil }
		o := opts("")
		o.Yes = true
		res, err := Run(context.Background(), sys, o, "0.2.0")
		switch {
		case tc.upToDate && (err != nil || !res.UpToDate):
			t.Errorf("0.2.0 over 0.2.0 must be a no-op: %+v %v", res, err)
		case !tc.upToDate && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("0.2.0 over %s: want %q, got %v", tc.installed, tc.want, err)
		}
		if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
			t.Errorf("nothing may change: %v", d)
		}
	}
}

type stagedRelease struct {
	cfg  config.Config
	dir  string
	keys []ed25519.PublicKey
	priv ed25519.PrivateKey
}

// stage leaves a release the way the agent does: the binary, the signed
// manifest and the request.
func stage(t *testing.T, h *fakeHost, cfg config.Config, from, to string) *stagedRelease {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := &stagedRelease{cfg: cfg, dir: filepath.Join(h.root, UpdateDir(cfg)), keys: []ed25519.PublicKey{pub}, priv: priv}
	staged := filepath.Join(s.dir, update.StagedDir)
	os.MkdirAll(staged, 0o700)
	binary := []byte("playkeeper " + to + " (staged)\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "playkeeper-" + to + "-linux-amd64/playkeeper", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write(binary)
	tw.Close()
	gz.Close()
	tarball := filepath.Join(t.TempDir(), update.TarballFile)
	os.WriteFile(tarball, buf.Bytes(), 0o644)
	m, err := update.BuildManifest(to, "2026-10-01T00:00:00Z", "- test", tarball)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(staged, update.BinaryFile), binary, 0o755)
	os.WriteFile(filepath.Join(staged, update.ManifestFile), m, 0o600)
	os.WriteFile(filepath.Join(staged, update.SignatureFile), update.Sign(priv, m), 0o600)
	s.request(t, h, update.Request{OpID: "op1", Version: to, From: from, Actor: "admin", RequestedAt: time.Now()})
	return s
}

func (s *stagedRelease) request(t *testing.T, h *fakeHost, r update.Request) {
	b, _ := json.Marshal(r)
	os.WriteFile(filepath.Join(s.dir, update.RequestFile), b, 0o600)
}

func (s *stagedRelease) result(t *testing.T) update.Result {
	t.Helper()
	var r update.Result
	b, err := os.ReadFile(filepath.Join(s.dir, update.ResultFile))
	if err != nil {
		t.Fatalf("no result: %v", err)
	}
	json.Unmarshal(b, &r)
	return r
}

func trust(t *testing.T, keys []ed25519.PublicKey) {
	old := trustedKeys
	trustedKeys = func() []ed25519.PublicKey { return keys }
	t.Cleanup(func() { trustedKeys = old })
}

func TestUpdaterInstallsAVerifiedStagedRelease(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.2.0", true)
	s := stage(t, h, cfg, "0.2.0", "0.2.1")
	trust(t, s.keys)
	var out bytes.Buffer
	if err := SelfUpdate(context.Background(), h.system(t), cfg, "0.2.0", &out); err != nil {
		t.Fatalf("update failed: %v\n%s", err, out.String())
	}
	if r := s.result(t); r.Outcome != update.OutcomeUpdated || r.OpID != "op1" || r.From != "0.2.0" || r.To != "0.2.1" {
		t.Fatalf("result: %+v", r)
	}
	if got := read(t, h, BinPath); got != "playkeeper 0.2.1 (staged)\n" {
		t.Fatalf("binary: %q", got)
	}
	for _, gone := range []string{update.RequestFile, update.ApplyingFile, update.StagedDir} {
		if _, err := os.Stat(filepath.Join(s.dir, gone)); err == nil {
			t.Errorf("%s must be cleaned up", gone)
		}
	}
	if err := SelfUpdate(context.Background(), h.system(t), cfg, "0.2.1", &out); err != nil {
		t.Fatalf("without a request the updater has nothing to do: %v", err)
	}
}

func TestUpdaterRefusesAnythingItCannotVerify(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(t *testing.T, h *fakeHost, s *stagedRelease)
		why     string
	}{
		"signed by another key": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			m, _ := os.ReadFile(filepath.Join(s.dir, update.StagedDir, update.ManifestFile))
			os.WriteFile(filepath.Join(s.dir, update.StagedDir, update.SignatureFile), update.Sign(other, m), 0o600)
		}, "not signed"},
		"binary changed after staging": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			os.WriteFile(filepath.Join(s.dir, update.StagedDir, update.BinaryFile), []byte("playkeeper 0.2.1 (evil)\n"), 0o755)
		}, "does not match the signed release"},
		"stale request": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			s.request(t, h, update.Request{OpID: "op1", Version: "0.2.1", From: "0.2.0", RequestedAt: time.Now().Add(-3 * time.Hour)})
		}, "too long ago"},
		"not newer": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			s.request(t, h, update.Request{OpID: "op1", Version: "0.1.9", From: "0.2.0", RequestedAt: time.Now()})
		}, "not newer"},
		"prepared for another version": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			s.request(t, h, update.Request{OpID: "op1", Version: "0.2.1", From: "0.1.0", RequestedAt: time.Now()})
		}, "prepared for Playkeeper 0.1.0"},
		"unknown systemd unit": {func(t *testing.T, h *fakeHost, s *stagedRelease) {
			h.unitsJSON = `{"playkeeper-agent.service":"a","playkeeper-panel.service":"p","sshd.service":"x"}`
		}, "unknown systemd unit"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost(t)
			cfg := installedAt(t, h, "0.2.0", true)
			s := stage(t, h, cfg, "0.2.0", "0.2.1")
			trust(t, s.keys)
			tc.prepare(t, h, s)
			before := snapshot(t, filepath.Join(h.root, "usr"))
			err := SelfUpdate(context.Background(), h.system(t), cfg, "0.2.0", &bytes.Buffer{})
			r := s.result(t)
			if err == nil || r.Outcome != update.OutcomeRefused || !strings.Contains(r.Error, tc.why) {
				t.Fatalf("want refused because %q, got %+v (%v)", tc.why, r, err)
			}
			if d := diff(before, snapshot(t, filepath.Join(h.root, "usr"))); len(d) != 0 {
				t.Fatalf("nothing may change: %v", d)
			}
			if strings.Contains(strings.Join(h.cmds, "\n"), "systemctl stop") {
				t.Fatal("the services must not even be stopped")
			}
		})
	}
}

func TestUpdaterRollsBackAnUnhealthyReleaseAndFinishesAnInterruptedOne(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.2.0", true)
	s := stage(t, h, cfg, "0.2.0", "0.2.1")
	trust(t, s.keys)
	h.unhealthy = map[string]error{"0.2.1": errors.New("panel did not answer")}
	if err := SelfUpdate(context.Background(), h.system(t), cfg, "0.2.0", &bytes.Buffer{}); err == nil {
		t.Fatal("an unhealthy update must report failure")
	}
	if r := s.result(t); r.Outcome != update.OutcomeRolledBack || !strings.Contains(r.Error, "panel did not answer") {
		t.Fatalf("result: %+v", r)
	}
	if got := read(t, h, BinPath); got != "playkeeper 0.2.0 (installed)\n" {
		t.Fatalf("the previous version must be back: %q", got)
	}

	// Power loss after the swap: the updater finds applying.json at boot.
	h2 := newFakeHost(t)
	cfg2 := installedAt(t, h2, "0.2.0", true)
	s2 := stage(t, h2, cfg2, "0.2.0", "0.2.1")
	u := newUpgrader(h2.system(t), cfg2, UpgradeOptions{OldVersion: "0.2.0", Out: &bytes.Buffer{}})
	if err := u.saveCopy(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h2.root, BinPath), []byte("playkeeper 0.2.1 (staged)\n"), 0o755)
	os.Rename(filepath.Join(s2.dir, update.RequestFile), filepath.Join(s2.dir, update.ApplyingFile))
	if err := SelfUpdate(context.Background(), h2.system(t), cfg2, "0.2.1", &bytes.Buffer{}); err != nil {
		t.Fatalf("a healthy interrupted update must be kept: %v", err)
	}
	if r := s2.result(t); r.Outcome != update.OutcomeUpdated || r.From != "0.2.0" {
		t.Fatalf("result: %+v", r)
	}
	h2.unhealthy = map[string]error{"0.2.1": errors.New("crash loop")}
	os.WriteFile(filepath.Join(s2.dir, update.ApplyingFile), []byte(`{"opId":"op2","version":"0.2.1","from":"0.2.0"}`), 0o600)
	SelfUpdate(context.Background(), h2.system(t), cfg2, "0.2.1", &bytes.Buffer{})
	if r := s2.result(t); r.Outcome != update.OutcomeRolledBack || read(t, h2, BinPath) != "playkeeper 0.2.0 (installed)\n" {
		t.Fatalf("an unhealthy interrupted update must be rolled back: %+v", r)
	}
}

func TestUninstallAfterAnUpgradeRemovesTheUpdater(t *testing.T) {
	h := newFakeHost(t)
	cfg := installedAt(t, h, "0.1.0", false)
	sys := h.system(t)
	bin := newBinary(t, "0.2.0")
	sys.Executable = func() (string, error) { return bin, nil }
	o := opts("")
	o.Yes = true
	if _, err := Run(context.Background(), sys, o, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	for _, u := range unitNames {
		if _, err := os.Stat(filepath.Join(h.root, UnitDir, u)); err == nil {
			t.Errorf("%s left behind", u)
		}
	}
	if !strings.Contains(strings.Join(h.cmds, "\n"), "systemctl disable --now playkeeper-update.path") {
		t.Fatal("the updater trigger must be disabled")
	}
	if _, err := os.Stat(filepath.Join(h.root, UpdateDir(cfg))); err == nil {
		t.Fatal("the update directory (staged binaries, previous copy) must be removed")
	}
	if read(t, h, cfg.DataDir+"/server/data/world/level.dat") != "my world" {
		t.Fatal("uninstall keeps worlds")
	}
}
