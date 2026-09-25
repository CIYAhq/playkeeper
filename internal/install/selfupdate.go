package install

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/update"
)

// lockFile in the update directory is held by whoever is upgrading.
const lockFile = "upgrade.lock"

// errUpgradeRunning means the updater or the one-line installer is upgrading.
var errUpgradeRunning = errors.New("another Playkeeper upgrade is running")

// lockUpgrades keeps the updater and the one-line installer from upgrading at
// the same time. The updater waits for the lock; the installer does not.
func lockUpgrades(sys System, cfg config.Config, wait bool) (unlock func(), err error) {
	dir := sys.P(UpdateDir(cfg))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errUpgradeRunning
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// pendingUpdate describes a dashboard update that is waiting for the updater
// or did not finish, if there is one.
func pendingUpdate(sys System, cfg config.Config) string {
	dir := sys.P(UpdateDir(cfg))
	version := func(f string) string {
		var req update.Request
		if readJSONFile(filepath.Join(dir, f), &req) != nil || req.Version == "" {
			return "update"
		}
		return req.Version
	}
	if _, err := os.Stat(filepath.Join(dir, update.RequestFile)); err == nil {
		return fmt.Sprintf("Playkeeper %s from the dashboard is about to be installed. Nothing was changed; run the installer again when it has finished", version(update.RequestFile))
	}
	if _, err := os.Stat(filepath.Join(dir, update.ApplyingFile)); err == nil {
		return fmt.Sprintf("the update to Playkeeper %s from the dashboard did not finish. Nothing was changed. Finish it first with: sudo systemctl start %s, then run the installer again", version(update.ApplyingFile), UpdateServiceUnit)
	}
	return ""
}

// SelfUpdate is the updater. playkeeper-update.service runs it with the
// installed binary when the agent has staged a verified update, and again if
// an update was interrupted. It checks the staged release once more against
// keys, the release keys compiled into this (the installed) version,
// upgrades, and leaves a result for the agent to report.
func SelfUpdate(ctx context.Context, sys System, cfg config.Config, current string, keys []ed25519.PublicKey, out io.Writer) error {
	unlock, err := lockUpgrades(sys, cfg, true)
	if err != nil {
		return err
	}
	defer unlock()
	dir := sys.P(UpdateDir(cfg))
	work := filepath.Join(dir, update.ApplyingFile)
	resuming := false
	if err := os.Rename(filepath.Join(dir, update.RequestFile), work); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Stat(work); err != nil {
			fmt.Fprintln(out, "No update is waiting to be installed.")
			return nil
		}
		resuming = true
	}
	var req update.Request
	res := update.Result{From: current}
	if err := readJSONFile(work, &req); err != nil {
		res.Outcome, res.Error = update.OutcomeRefused, "the update request cannot be read: "+err.Error()
	} else {
		res.OpID, res.To, res.Actor = req.OpID, req.Version, req.Actor
		if resuming {
			res.From = SavedVersion(sys, cfg)
			fmt.Fprintf(out, "Finishing an interrupted update to Playkeeper %s:\n", req.Version)
			updated, err := Recover(ctx, sys, cfg, req.Version, out)
			switch {
			case updated:
				res.Outcome = update.OutcomeUpdated
			case err == nil:
				res.Outcome, res.Error = update.OutcomeRolledBack, "the update was interrupted"
			default:
				res.Outcome, res.Error = update.OutcomeFailed, "the update was interrupted, and putting the previous version back failed: "+err.Error()
			}
		} else {
			fmt.Fprintf(out, "Updating Playkeeper %s to %s:\n", current, req.Version)
			res.Outcome, res.Error = applyStaged(ctx, sys, cfg, current, keys, req, out)
		}
	}
	res.FinishedAt = sys.Now().UTC()
	fmt.Fprintf(out, "Result: %s %s\n", res.Outcome, res.Error)
	if err := writeJSONFile(filepath.Join(dir, update.ResultFile), res); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(dir, update.StagedDir))
	if err := os.Remove(work); err != nil {
		return err
	}
	if res.Outcome != update.OutcomeUpdated {
		return errors.New(res.Error)
	}
	return nil
}

// applyStaged checks and installs the staged release.
func applyStaged(ctx context.Context, sys System, cfg config.Config, current string, keys []ed25519.PublicKey, req update.Request, out io.Writer) (outcome, reason string) {
	refuse := func(format string, args ...any) (string, string) {
		return update.OutcomeRefused, fmt.Sprintf(format, args...)
	}
	// This updater may have waited for the lock while the one-line installer
	// replaced the installed binary; it must not install over that.
	installed, err := sys.Version(sys.P(BinPath))
	if err != nil {
		return refuse("the installed Playkeeper's version cannot be read (%v)", err)
	}
	if installed != current {
		return refuse("Playkeeper %s was installed while this update waited; check for updates again", installed)
	}
	if age := sys.Now().Sub(req.RequestedAt); age > update.StaleAfter || age < -update.StaleAfter {
		return refuse("the update request is from %s, too long ago; check for updates again", req.RequestedAt.Format("2006-01-02 15:04"))
	}
	if req.From != current {
		return refuse("the update was prepared for Playkeeper %s, but %s is installed", req.From, current)
	}
	if c, err := update.CompareVersions(req.Version, current); err != nil || c <= 0 {
		return refuse("Playkeeper %s is not newer than the installed %s", req.Version, current)
	}
	staged := filepath.Join(sys.P(UpdateDir(cfg)), update.StagedDir)
	bin := filepath.Join(staged, update.BinaryFile)
	if err := verifyStaged(staged, bin, req.Version, keys); err != nil {
		return refuse("%v", err)
	}
	if v, err := sys.Version(bin); err != nil || v != req.Version {
		return refuse("the staged binary reports version %q, not %s (%v)", v, req.Version, err)
	}
	raw, err := sys.Run(bin, "units", "--config", ConfigDir+"/config.json")
	if err != nil {
		return refuse("the new version could not describe its services: %v", err)
	}
	var units map[string]string
	if err := json.Unmarshal([]byte(raw), &units); err != nil {
		return refuse("the new version described its services in an unexpected format: %v", err)
	}
	err = Upgrade(ctx, sys, cfg, UpgradeOptions{NewBinary: bin, NewVersion: req.Version, OldVersion: current, Units: units, Out: out})
	var unchanged *NothingChangedError
	var rolledBack *RolledBackError
	switch {
	case err == nil:
		return update.OutcomeUpdated, ""
	case errors.As(err, &unchanged):
		return update.OutcomeRefused, err.Error()
	case errors.As(err, &rolledBack):
		return update.OutcomeRolledBack, rolledBack.Cause.Error()
	}
	return update.OutcomeFailed, err.Error()
}

// verifyStaged checks the staged manifest's signature with keys and the
// staged binary against it. The agent checked the same before staging; the
// updater does not take that on trust.
func verifyStaged(staged, bin, version string, keys []ed25519.PublicKey) error {
	raw, err := os.ReadFile(filepath.Join(staged, update.ManifestFile))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(staged, update.SignatureFile))
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(raw, sig, keys)
	if err != nil {
		return err
	}
	if m.Version != version {
		return fmt.Errorf("the staged release is %s, not %s", m.Version, version)
	}
	a, err := m.Asset()
	if err != nil {
		return err
	}
	sum, err := update.FileSHA256(bin)
	if err != nil {
		return err
	}
	if sum != a.BinarySHA256 {
		return fmt.Errorf("the staged binary does not match the signed release (SHA-256 %s, want %s)", sum[:16], a.BinarySHA256[:16])
	}
	return nil
}
