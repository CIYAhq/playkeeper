package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/panel"
	"github.com/CIYAhq/playkeeper/internal/update"
)

// runUpgrade upgrades an existing install to this binary in place, for
// example when the one-line installer runs on a server that has Playkeeper
// 0.1.0. Worlds, backups, settings and the admin account are kept.
func runUpgrade(ctx context.Context, sys System, o Options, newVersion string) (*Result, error) {
	start := sys.Now()
	out := o.Out
	fmt.Fprintf(out, "Playkeeper %s installer\n\n", newVersion)
	cfg, err := config.Load(sys.P(ConfigDir + "/config.json"))
	if err != nil {
		return nil, fmt.Errorf("Playkeeper is installed, but %v. Nothing was changed. Fix: sudo playkeeper uninstall (keeps worlds and backups), then install again", err)
	}
	var m Manifest
	manifestErr := readJSONFile(sys.P(cfg.ManifestPath()), &m)
	old, verr := sys.Version(sys.P(BinPath))
	if verr != nil {
		old = m.Version
	}
	if old == "" {
		return nil, errors.New("Playkeeper is installed, but its version cannot be read. Nothing was changed. Fix: sudo playkeeper uninstall (keeps worlds and backups), then install again")
	}
	cmp, err := update.CompareVersions(newVersion, old)
	if err != nil {
		return nil, fmt.Errorf("cannot compare this installer's version with the installed one: %w. Nothing was changed", err)
	}
	res := &Result{URL: fmt.Sprintf("https://%s:%d", primaryIP(), cfg.PanelPort), FromVersion: old}
	if b, err := os.ReadFile(sys.P(filepath.Join(cfg.TLSDir(), "cert.pem"))); err == nil {
		res.Fingerprint, _ = panel.FingerprintPEM(b)
	}
	switch {
	case cmp == 0:
		fmt.Fprintf(out, "Playkeeper %s is already installed. Nothing to do.\n", old)
		res.UpToDate = true
		return res, nil
	case cmp < 0:
		return nil, fmt.Errorf("this installer is Playkeeper %s, older than the installed %s. Playkeeper does not go back to an older version, so nothing was changed", newVersion, old)
	}
	if manifestErr != nil {
		return nil, fmt.Errorf("Playkeeper %s is installed, but its install manifest cannot be read (%v). Nothing was changed. Fix: sudo playkeeper uninstall (keeps worlds and backups), then install again", old, manifestErr)
	}
	if err := upgradeChecks(sys, o); err != nil {
		return nil, err
	}
	if kind := busyWith(ctx, sys, cfg); kind != "" {
		return nil, fmt.Errorf("Playkeeper is busy (%s). Nothing was changed; run the installer again when it has finished", kind)
	}
	fmt.Fprintf(out, "Playkeeper %s is installed on this server. This upgrades it to %s in place:\n", old, newVersion)
	for _, line := range upgradePlan(sys, cfg, old, newVersion) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprintln(out)
	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")
		ans, _ := bufio.NewReader(o.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return nil, errors.New("upgrade cancelled; nothing was changed")
		}
	}
	var newCfg *config.Config
	if o.ReleaseURL != "" && o.ReleaseURL != cfg.ReleaseURL {
		c := cfg
		c.ReleaseURL = o.ReleaseURL
		newCfg = &c
	}
	exe, err := sys.Executable()
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, "Upgrading:")
	if err := Upgrade(ctx, sys, cfg, UpgradeOptions{NewBinary: exe, NewVersion: newVersion, OldVersion: old, Units: Units(cfg), Config: newCfg, Out: out}); err != nil {
		return nil, err
	}
	res.Upgraded = true
	res.Duration = sys.Now().Sub(start)
	return res, nil
}

func upgradeChecks(sys System, o Options) error {
	if !sys.IsRoot() {
		return errors.New("the installer must run as root; re-run the command with sudo. Nothing was changed")
	}
	if st, err := os.Stat(sys.P("/run/systemd/system")); err != nil || !st.IsDir() {
		return errors.New("systemd is not running; Playkeeper's services need it. Nothing was changed")
	}
	if arch := sys.Arch(); arch != "amd64" && !o.AllowUntestedOS {
		return fmt.Errorf("this build is for x86_64 (amd64), not %s. Nothing was changed", arch)
	}
	return nil
}

// busyWith names the operation the agent is running, if it is reachable.
func busyWith(ctx context.Context, sys System, cfg config.Config) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var st api.ServerStatus
	if _, err := agentclient.New(sys.P(cfg.SocketPath)).Do(cctx, "GET", "/v1/server", nil, nil, &st); err != nil || st.Operation == nil {
		return ""
	}
	return st.Operation.Kind
}

func upgradePlan(sys System, cfg config.Config, old, newVersion string) []string {
	p := []string{
		"Keeps:     your worlds, backups, settings and admin account in " + cfg.DataDir + ", and " + ConfigDir + "/config.json",
		"Keeps:     the Minecraft server running (only the Playkeeper agent and panel restart)",
		"Replaces:  " + BinPath + " (" + old + " → " + newVersion + ")",
		"Updates:   the " + AgentUnit + " and " + PanelUnit + " services",
	}
	if _, err := os.Stat(sys.P(UnitDir + "/" + UpdatePathUnit)); err != nil {
		p = append(p, "Adds:      "+UpdatePathUnit+" and "+UpdateServiceUnit+", which install later updates from the dashboard")
	}
	return append(p, "Saves:     a copy of Playkeeper "+old+" in "+PreviousDir(cfg)+"; it is put back automatically if "+newVersion+" does not come up healthy")
}
