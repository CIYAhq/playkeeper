package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
)

// UpgradeOptions describes replacing an installed Playkeeper in place.
type UpgradeOptions struct {
	NewBinary  string
	NewVersion string
	OldVersion string
	// Units are the systemd units of the new version, by name.
	Units map[string]string
	// Config, when set, is written as /etc/playkeeper/config.json after the
	// copy of the old one is saved.
	Config *config.Config
	Out    io.Writer
	// HealthTimeout bounds the wait for the new version (default 2 minutes).
	HealthTimeout time.Duration
}

// NothingChangedError reports an upgrade refused before it changed anything.
type NothingChangedError struct{ Cause error }

func (e *NothingChangedError) Error() string { return e.Cause.Error() + "; nothing was changed" }

func (e *NothingChangedError) Unwrap() error { return e.Cause }

// RolledBackError reports an upgrade that failed and was undone: the
// previous version is installed and healthy again.
type RolledBackError struct {
	Cause   error
	Version string
}

func (e *RolledBackError) Error() string {
	return fmt.Sprintf("%v; Playkeeper %s was put back and is running", e.Cause, e.Version)
}

func (e *RolledBackError) Unwrap() error { return e.Cause }

// stableAfter is how long a new version must keep answering after it first
// does; one that crashes right after starting is not healthy.
const stableAfter = 10 * time.Second

// databases are the SQLite files an upgrade copies before the new version
// runs, since it may migrate them; relative to the data directory.
var databases = []string{"agent/agent.db", "agent/agent.db-wal", "agent/agent.db-shm", "panel/panel.db", "panel/panel.db-wal", "panel/panel.db-shm"}

// PreviousDir holds the copy of the version an upgrade replaced.
func PreviousDir(cfg config.Config) string { return filepath.Join(UpdateDir(cfg), "previous") }

type savedCopy struct {
	Version string    `json:"version"`
	Units   []string  `json:"units"`
	TakenAt time.Time `json:"takenAt"`
}

type upgrader struct {
	sys     System
	cfg     config.Config
	o       UpgradeOptions
	out     io.Writer
	prev    string
	timeout time.Duration
}

func newUpgrader(sys System, cfg config.Config, o UpgradeOptions) *upgrader {
	u := &upgrader{sys: sys, cfg: cfg, o: o, out: o.Out, prev: sys.P(PreviousDir(cfg)), timeout: o.HealthTimeout}
	if u.out == nil {
		u.out = io.Discard
	}
	if u.timeout == 0 {
		u.timeout = 2 * time.Minute
	}
	return u
}

// checkUnits refuses a unit set that misses the agent or panel or names a
// file Playkeeper does not own.
func checkUnits(units map[string]string) error {
	allowed := map[string]bool{}
	for _, n := range unitNames {
		allowed[n] = true
	}
	for name, content := range units {
		if !allowed[name] {
			return fmt.Errorf("the new version wants to install an unknown systemd unit %q", name)
		}
		if content == "" {
			return fmt.Errorf("the new version's %s is empty", name)
		}
	}
	if units[AgentUnit] == "" || units[PanelUnit] == "" {
		return errors.New("the new version does not provide the agent and panel units")
	}
	return nil
}

// Upgrade replaces the installed Playkeeper with NewBinary and keeps all
// data. The Minecraft server keeps running; only the agent and panel
// restart. The binary, units, config and databases are copied first and put
// back if the new version does not come up healthy.
func Upgrade(ctx context.Context, sys System, cfg config.Config, o UpgradeOptions) error {
	if err := checkUnits(o.Units); err != nil {
		return &NothingChangedError{err}
	}
	u := newUpgrader(sys, cfg, o)
	if err := u.saveCopy(); err != nil {
		return &NothingChangedError{fmt.Errorf("could not save a copy of Playkeeper %s: %w", o.OldVersion, err)}
	}
	err := u.apply(ctx)
	if err == nil {
		u.recordVersion(o.NewVersion)
		return nil
	}
	fmt.Fprintf(u.out, "  ! %v\n  Putting Playkeeper %s back:\n", err, o.OldVersion)
	if rerr := u.restore(ctx, o.OldVersion); rerr != nil {
		return fmt.Errorf("%v; putting Playkeeper %s back also failed: %v (the copy of %s is in %s; see docs/RECOVERY.md)", err, o.OldVersion, rerr, o.OldVersion, PreviousDir(cfg))
	}
	return &RolledBackError{Cause: err, Version: o.OldVersion}
}

func (u *upgrader) step(name string) { fmt.Fprintf(u.out, "  • %s\n", name) }

func (u *upgrader) saveCopy() error {
	u.step("save a copy of Playkeeper " + u.o.OldVersion + " (binary, services and config)")
	if err := os.RemoveAll(u.prev); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(u.prev, "units"), 0o700); err != nil {
		return err
	}
	if err := u.copyPreserving(u.sys.P(BinPath), filepath.Join(u.prev, "playkeeper")); err != nil {
		return err
	}
	if err := u.copyPreserving(u.sys.P(ConfigDir+"/config.json"), filepath.Join(u.prev, "config.json")); err != nil {
		return err
	}
	var existed []string
	for _, name := range unitNames {
		p := u.sys.P(UnitDir + "/" + name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := u.copyPreserving(p, filepath.Join(u.prev, "units", name)); err != nil {
			return err
		}
		existed = append(existed, name)
	}
	return writeJSONFile(filepath.Join(u.prev, "snapshot.json"), savedCopy{Version: u.o.OldVersion, Units: existed, TakenAt: u.sys.Now().UTC()})
}

func (u *upgrader) apply(ctx context.Context) error {
	steps := []struct {
		name string
		do   func() error
	}{
		{"stop the agent and panel (the Minecraft server keeps running)", func() error {
			_, err := u.sys.Run("systemctl", "stop", PanelUnit, AgentUnit)
			return err
		}},
		{"save a copy of the databases", u.saveDatabases},
		{"install Playkeeper " + u.o.NewVersion, func() error { return copyFile(u.o.NewBinary, u.sys.P(BinPath), 0o755) }},
		{"write the config", func() error {
			if u.o.Config == nil {
				return nil
			}
			return u.o.Config.Save(u.sys.P(ConfigDir + "/config.json"))
		}},
		{"install the systemd services", u.writeUnits},
		{"start Playkeeper " + u.o.NewVersion, func() error {
			_, err := u.sys.Run("systemctl", "start", AgentUnit, PanelUnit)
			return err
		}},
		{"wait until Playkeeper " + u.o.NewVersion + " is healthy", func() error { return u.waitHealthy(ctx, u.o.NewVersion) }},
	}
	for _, s := range steps {
		u.step(s.name)
		if err := s.do(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

func (u *upgrader) writeUnits() error {
	for _, name := range unitNames {
		content, ok := u.o.Units[name]
		if !ok {
			continue
		}
		if err := writeFileAtomic(u.sys.P(UnitDir+"/"+name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if _, err := u.sys.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, ok := u.o.Units[UpdatePathUnit]; ok {
		if _, err := u.sys.Run("systemctl", "enable", "--now", UpdatePathUnit); err != nil {
			return err
		}
	}
	return nil
}

func (u *upgrader) waitHealthy(ctx context.Context, version string) error {
	cert := u.sys.P(filepath.Join(u.cfg.TLSDir(), "cert.pem"))
	hctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()
	if err := u.sys.WaitVersion(hctx, u.cfg.SocketPath, cert, u.cfg.PanelPort, version); err != nil {
		return err
	}
	u.sys.Sleep(stableAfter)
	hctx2, cancel2 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel2()
	if err := u.sys.WaitVersion(hctx2, u.cfg.SocketPath, cert, u.cfg.PanelPort, version); err != nil {
		return fmt.Errorf("Playkeeper %s answered, then stopped: %w", version, err)
	}
	return nil
}

func (u *upgrader) saveDatabases() error {
	for _, rel := range databases {
		src := u.sys.P(filepath.Join(u.cfg.DataDir, rel))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(u.prev, "db", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := u.copyPreserving(src, dst); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(u.prev, "databases.done"), nil, 0o600)
}

func (u *upgrader) restoreDatabases() error {
	var errs []error
	for _, rel := range databases {
		live := u.sys.P(filepath.Join(u.cfg.DataDir, rel))
		saved := filepath.Join(u.prev, "db", rel)
		if err := removeIfExists(live); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Stat(saved); err == nil {
			errs = append(errs, u.copyPreserving(saved, live))
		}
	}
	return errors.Join(errs...)
}

// restore puts the saved copy back and waits until it is healthy.
func (u *upgrader) restore(ctx context.Context, version string) error {
	var snap savedCopy
	if err := readJSONFile(filepath.Join(u.prev, "snapshot.json"), &snap); err != nil {
		return err
	}
	has := map[string]bool{}
	for _, n := range snap.Units {
		has[n] = true
	}
	var errs []error
	u.step("stop the agent and panel")
	_, _ = u.sys.Run("systemctl", "stop", PanelUnit, AgentUnit)
	// A version that kept crashing may have hit systemd's start limit, which
	// would refuse the start below.
	_, _ = u.sys.Run("systemctl", "reset-failed", PanelUnit, AgentUnit)
	u.step("put back Playkeeper " + version + ", its services and config")
	errs = append(errs, copyFile(filepath.Join(u.prev, "playkeeper"), u.sys.P(BinPath), 0o755))
	errs = append(errs, u.copyPreserving(filepath.Join(u.prev, "config.json"), u.sys.P(ConfigDir+"/config.json")))
	for _, name := range unitNames {
		p := u.sys.P(UnitDir + "/" + name)
		if has[name] {
			errs = append(errs, u.copyPreserving(filepath.Join(u.prev, "units", name), p))
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		// Never stop the updater service here: it may be the process running this.
		if name == UpdatePathUnit {
			_, _ = u.sys.Run("systemctl", "disable", "--now", name)
		}
		errs = append(errs, removeIfExists(p))
	}
	if _, err := os.Stat(filepath.Join(u.prev, "databases.done")); err == nil {
		u.step("put back the databases")
		errs = append(errs, u.restoreDatabases())
	}
	if _, err := u.sys.Run("systemctl", "daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	u.step("start Playkeeper " + version)
	if _, err := u.sys.Run("systemctl", "start", AgentUnit, PanelUnit); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	u.step("wait until Playkeeper " + version + " is healthy")
	return u.waitHealthy(ctx, version)
}

// recordVersion records the running version and its units in the install
// manifest, for uninstall. The upgrade has succeeded either way, so a
// failure is reported, not returned.
func (u *upgrader) recordVersion(version string) {
	if err := u.updateManifest(version); err != nil {
		fmt.Fprintf(u.out, "  ! Playkeeper %s is running, but the install manifest %s could not be updated: %v\n", version, u.cfg.ManifestPath(), err)
	}
}

func (u *upgrader) updateManifest(version string) error {
	path := u.sys.P(u.cfg.ManifestPath())
	var m Manifest
	if err := readJSONFile(path, &m); err != nil {
		return err
	}
	m.Version = version
	for _, name := range unitNames {
		f := UnitDir + "/" + name
		if _, err := os.Stat(u.sys.P(f)); err != nil {
			continue
		}
		if !contains(m.Units, name) {
			m.Units = append(m.Units, name)
		}
		if !contains(m.FilesCreated, f) {
			m.FilesCreated = append(m.FilesCreated, f)
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// Recover finishes an update the updater could not report on, for example
// after a power loss: if Playkeeper want is installed and healthy it stays,
// otherwise the copy saved before the update is put back.
func Recover(ctx context.Context, sys System, cfg config.Config, want string, out io.Writer) (updated bool, err error) {
	u := newUpgrader(sys, cfg, UpgradeOptions{Out: out})
	var snap savedCopy
	if err := readJSONFile(filepath.Join(u.prev, "snapshot.json"), &snap); err != nil {
		return false, nil
	}
	installed, _ := sys.Version(sys.P(BinPath))
	switch installed {
	case want:
		if u.waitHealthy(ctx, want) == nil {
			u.recordVersion(want)
			return true, nil
		}
	case snap.Version:
		// The new version was never installed; make sure the old one runs.
		if _, err := sys.Run("systemctl", "start", AgentUnit, PanelUnit); err != nil {
			return false, err
		}
		return false, u.waitHealthy(ctx, snap.Version)
	}
	return false, u.restore(ctx, snap.Version)
}

// SavedVersion is the version whose copy an upgrade saved, if any.
func SavedVersion(sys System, cfg config.Config) string {
	var snap savedCopy
	if readJSONFile(sys.P(filepath.Join(PreviousDir(cfg), "snapshot.json")), &snap) != nil {
		return ""
	}
	return snap.Version
}

// copyPreserving copies a file with its mode and owner.
func (u *upgrader) copyPreserving(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := copyFile(src, dst, st.Mode().Perm()); err != nil {
		return err
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok && u.sys.IsRoot() {
		return u.sys.Chown(dst, int(s.Uid), int(s.Gid))
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
