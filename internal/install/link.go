package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// ErrNotJoined means the machine is not connected to another dashboard.
var ErrNotJoined = errors.New("this machine isn't connected to another dashboard")

// JoinOptions are the parts of a join command and what the machine tells
// the dashboard about itself.
type JoinOptions struct {
	Address     string
	Code        string
	Fingerprint string
	// Name is what the dashboard should call this machine; empty means its
	// host name.
	Name    string
	Version string
}

// Join connects this machine to the dashboard at o.Address with a new key
// and starts the link. The machine checks the dashboard's key against
// o.Fingerprint before it sends the code. With a development config no
// service starts: run playkeeper link instead.
func Join(ctx context.Context, sys System, cfg config.Config, o JoinOptions) (machinelink.Dashboard, error) {
	var none machinelink.Dashboard
	if d, err := machinelink.LoadDashboard(sys.P(cfg.LinkDashboardPath())); err == nil {
		return none, &machinelink.Error{Code: machinelink.CodeMachineAlreadyJoined, Params: map[string]string{"name": d.Name, "address": d.Address},
			Msg:  "This machine is already connected to the dashboard at " + d.Address + ", as " + d.Name + ".",
			Hint: "To connect it to another dashboard, run sudo playkeeper leave first."}
	} else if !errors.Is(err, os.ErrNotExist) {
		return none, err
	}
	own, err := linkOwner(sys, cfg)
	if err != nil {
		return none, err
	}
	dir := sys.P(cfg.LinkDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return none, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return none, err
	}
	if err := own(dir); err != nil {
		return none, err
	}
	id, err := machinelink.NewIdentity()
	if err != nil {
		return none, err
	}
	keyPath, dashPath := sys.P(cfg.LinkKeyPath()), sys.P(cfg.LinkDashboardPath())
	if err := id.Save(keyPath); err != nil {
		return none, err
	}
	if err := own(keyPath); err != nil {
		os.Remove(keyPath)
		return none, err
	}
	name := o.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	d, err := machinelink.Join(ctx, machinelink.JoinOptions{Address: o.Address, Code: o.Code, Fingerprint: o.Fingerprint, Identity: id, Name: name, Version: o.Version, Now: sys.Now})
	if err != nil {
		os.Remove(keyPath)
		return none, err
	}
	if err := d.Save(dashPath); err == nil {
		err = own(dashPath)
	}
	if err != nil {
		os.Remove(dashPath)
		os.Remove(keyPath)
		return none, &machinelink.Error{Code: machinelink.CodeKeyFile, Params: map[string]string{"name": d.Name},
			Msg:  "The dashboard accepted this machine as " + d.Name + ", but this machine couldn't save what it needs to connect.",
			Hint: "Remove " + d.Name + " in the dashboard (Settings › Machines), then connect again with a new command.",
			Err:  err}
	}
	if cfg.Dev {
		return d, nil
	}
	if err := enableLink(sys, cfg); err != nil {
		return d, fmt.Errorf("this machine joined the dashboard as %s, but its link did not start: %w (start it with: sudo systemctl enable --now %s)", d.Name, err, LinkUnit)
	}
	return d, nil
}

// linkOwner returns what gives a file to the 'playkeeper' user, who runs
// the link. A development link runs as whoever started it.
func linkOwner(sys System, cfg config.Config) (func(path string) error, error) {
	if !sys.IsRoot() || cfg.Dev {
		return func(string) error { return nil }, nil
	}
	uid, gid, ok := sys.LookupUser(config.DefaultPanelUser)
	if !ok {
		return nil, fmt.Errorf("the '%s' user, who runs the link, is missing; install Playkeeper again first: sudo playkeeper install", config.DefaultPanelUser)
	}
	return func(path string) error { return sys.Chown(path, uid, gid) }, nil
}

// enableLink installs the link's unit and starts the link.
func enableLink(sys System, cfg config.Config) error {
	if err := writeFileAtomic(sys.P(UnitDir+"/"+LinkUnit), []byte(LinkUnitFile(cfg)), 0o644); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err := sys.Run("systemctl", "enable", "--now", LinkUnit)
	return err
}

// disableLink stops the link and removes its unit, if it is installed.
func disableLink(sys System) error {
	p := sys.P(UnitDir + "/" + LinkUnit)
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	var errs []error
	if _, err := sys.Run("systemctl", "disable", "--now", LinkUnit); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, removeIfExists(p))
	if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Left is what Leave did: which dashboard the machine left and, when it
// forgot the dashboard without telling it, why it couldn't.
type Left struct {
	Dashboard machinelink.Dashboard
	Untold    error
}

// Leave tells the dashboard that this machine leaves it, then stops the
// link and deletes the machine's key and what it knew of the dashboard. If
// the dashboard can't be told, nothing changes unless force is set: then
// the machine forgets the dashboard anyway, which shows it offline until
// it is removed there. Leave returns ErrNotJoined when there was no
// dashboard to leave.
func Leave(ctx context.Context, sys System, cfg config.Config, force bool) (Left, error) {
	d, err := machinelink.LoadDashboard(sys.P(cfg.LinkDashboardPath()))
	if errors.Is(err, os.ErrNotExist) {
		// A machine the dashboard removed has forgotten it already; its
		// link's unit may still be there.
		if err := forget(sys, cfg); err != nil {
			return Left{}, err
		}
		return Left{}, ErrNotJoined
	}
	if err == nil {
		var id *machinelink.Identity
		if id, err = machinelink.LoadIdentity(sys.P(cfg.LinkKeyPath())); err == nil {
			err = machinelink.Leave(ctx, machinelink.LeaveOptions{Dashboard: d, Identity: id})
		}
	}
	if err != nil && !force {
		return Left{Dashboard: d}, err
	}
	return Left{Dashboard: d, Untold: err}, forget(sys, cfg)
}

// forget stops the link and deletes the link's files.
func forget(sys System, cfg config.Config) error {
	var err error
	if !cfg.Dev {
		err = disableLink(sys)
	}
	return errors.Join(err, ForgetDashboard(sys, cfg))
}

// ForgetDashboard deletes what the machine knew of its dashboard, then its
// key. Without the first file the machine counts as not joined, and the
// link's unit no longer starts.
func ForgetDashboard(sys System, cfg config.Config) error {
	return errors.Join(removeIfExists(sys.P(cfg.LinkDashboardPath())), removeIfExists(sys.P(cfg.LinkKeyPath())))
}

// LinkStatusFile is where the running link keeps its state for playkeeper
// status: in its runtime directory, which systemd removes when the link
// stops, or beside its key in development.
func LinkStatusFile(cfg config.Config) string {
	if cfg.Dev {
		return filepath.Join(cfg.LinkDir(), "status.json")
	}
	return "/run/playkeeper-link/status.json"
}
