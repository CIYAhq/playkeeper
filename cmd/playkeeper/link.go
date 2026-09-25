package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// joinArgs are playkeeper join's arguments, the same as the installer's
// --join ADDRESS --code … --fingerprint … [--name …].
type joinArgs struct {
	address, code, fingerprint, name, config string
}

// parseJoinArgs reads playkeeper join's arguments: the address first, as
// the dashboard prints the command, or after the flags.
func parseJoinArgs(args []string) (joinArgs, error) {
	var j joinArgs
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&j.code, "code", "", "the join code from the dashboard's command")
	fs.StringVar(&j.fingerprint, "fingerprint", "", "the dashboard's fingerprint from its command")
	fs.StringVar(&j.name, "name", "", "what the dashboard calls this machine (default: its host name)")
	fs.StringVar(&j.config, "config", config.DefaultPath, "config file")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		j.address, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return j, fmt.Errorf("%v\nusage: sudo playkeeper join ADDRESS --code CODE --fingerprint FINGERPRINT [--name NAME]", err)
	}
	rest := fs.Args()
	if j.address == "" && len(rest) > 0 {
		j.address, rest = rest[0], rest[1:]
	}
	switch {
	case len(rest) > 0:
		return j, fmt.Errorf("unexpected argument %q; copy the whole command from the dashboard (Settings › Machines)", rest[0])
	case j.address == "" || j.code == "" || j.fingerprint == "":
		return j, errors.New("usage: sudo playkeeper join ADDRESS --code CODE --fingerprint FINGERPRINT [--name NAME]\nThe dashboard makes this command in Settings › Machines › Connect a machine.")
	}
	if _, err := machinelink.NewCommand(j.address, j.code, j.fingerprint); err != nil {
		return j, linkError(err)
	}
	return j, nil
}

// linkError puts a machine link error's hint under its message.
func linkError(err error) error {
	var e *machinelink.Error
	if errors.As(err, &e) && e.Hint != "" {
		return fmt.Errorf("%s\n       %s", e.Msg, e.Hint)
	}
	return err
}

func runJoin(args []string) error {
	j, err := parseJoinArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(j.config)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 && !cfg.Dev {
		return errors.New("run as root: sudo playkeeper join …")
	}
	ctx, cancel := signalContext()
	defer cancel()
	return linkError(join(ctx, os.Stdout, cfg, j))
}

// join connects this machine to the dashboard and says what happened.
func join(ctx context.Context, w io.Writer, cfg config.Config, j joinArgs) error {
	fmt.Fprintf(w, "Connecting to the dashboard at %s…\n", j.address)
	d, err := install.Join(ctx, install.Real(), cfg, install.JoinOptions{Address: j.address, Code: j.code, Fingerprint: j.fingerprint, Name: j.name, Version: version.Version})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\nThis machine joined the dashboard at %s as %s.\n", d.Address, d.Name)
	if id, err := machinelink.LoadIdentity(cfg.LinkKeyPath()); err == nil {
		fmt.Fprintf(w, "Its fingerprint is %s; the dashboard shows the same in Settings › Machines › %s.\n", groupFingerprint(id.Fingerprint()), d.Name)
	}
	if cfg.Dev {
		fmt.Fprintf(w, "Start its link, which stays in the foreground:\n  playkeeper link --config %s\n", absPath(j.config))
		return nil
	}
	fmt.Fprintf(w, "It dials out to the dashboard and reconnects by itself; no port opens for it here.\nCreate servers on it from the dashboard. To disconnect it: sudo playkeeper leave\n")
	return nil
}

func runLeave(args []string) error {
	fs := flag.NewFlagSet("leave", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	force := fs.Bool("force", false, "forget the dashboard even if it can't be told (it then shows this machine offline until it is removed there)")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 && !cfg.Dev {
		return errors.New("run as root: sudo playkeeper leave")
	}
	ctx, cancel := signalContext()
	defer cancel()
	return leave(ctx, os.Stdout, cfg, *force)
}

// leave disconnects this machine from its dashboard and says what happened.
func leave(ctx context.Context, w io.Writer, cfg config.Config, force bool) error {
	left, err := install.Leave(ctx, install.Real(), cfg, force)
	d := left.Dashboard
	switch {
	case errors.Is(err, install.ErrNotJoined):
		fmt.Fprintln(w, "This machine isn't connected to another dashboard. Nothing to do.")
		return nil
	case err != nil && !force:
		return fmt.Errorf("%v\n       If that dashboard is gone for good: sudo playkeeper leave --force", linkError(err))
	case err != nil:
		return linkError(err)
	case left.Untold != nil:
		fmt.Fprintf(w, "This machine forgot the dashboard at %s without telling it: %v\nIf that dashboard still exists, remove %s there, in Settings › Machines.\n", d.Address, left.Untold, d.Name)
		return nil
	}
	fmt.Fprintf(w, "This machine left the dashboard at %s, where it was %s. Its servers keep running here.\n", d.Address, d.Name)
	return nil
}

func runLink(args []string) error {
	fs := flag.NewFlagSet("link", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 && !cfg.Dev {
		return errors.New("the link must not run as root; it runs as the 'playkeeper' user via systemd")
	}
	ctx, cancel := signalContext()
	defer cancel()
	return serveLink(ctx, cfg, logger(), time.Second)
}

// serveLink keeps this machine's link to its dashboard until ctx ends,
// publishing its state for playkeeper status every tick. When the
// dashboard removes the machine, it forgets the dashboard and returns nil;
// the service then doesn't start again (see install.LinkUnitFile).
func serveLink(ctx context.Context, cfg config.Config, log *slog.Logger, tick time.Duration) error {
	d, err := machinelink.LoadDashboard(cfg.LinkDashboardPath())
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this machine isn't connected to a dashboard; connect it with the command from the dashboard (Settings › Machines › Connect a machine)")
	}
	if err != nil {
		return linkError(err)
	}
	id, err := machinelink.LoadIdentity(cfg.LinkKeyPath())
	if err != nil {
		return linkError(err)
	}
	l, err := machinelink.NewLink(machinelink.LinkOptions{
		Dashboard: d,
		Identity:  id,
		Handler:   machinelink.AgentProxy(cfg.SocketPath),
		Routes:    agent.LinkRoutes(),
		Version:   version.Version,
		Logger:    log,
		OnRequest: func(r machinelink.RequestRecord) {
			log.Info("dashboard request", "actor", r.Actor, "method", r.Method, "route", r.Route, "path", r.Path,
				"status", r.Status, "bytes", r.Bytes, "duration", r.Duration.Round(time.Millisecond))
		},
	})
	if err != nil {
		return err
	}
	status := install.LinkStatusFile(cfg)
	done, published := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(published)
		publishStatus(status, l, tick, done)
	}()
	log.Info("machine link starting", "dashboard", d.Address, "machine", d.Name, "fingerprint", id.Fingerprint())
	err = l.Run(ctx)
	close(done)
	<-published
	os.Remove(status)
	switch {
	case machinelink.CodeOf(err) == machinelink.CodeMachineRemoved:
		log.Warn("the dashboard removed this machine; forgetting it", "dashboard", d.Address)
		return install.ForgetDashboard(install.Real(), cfg)
	case ctx.Err() != nil:
		return nil
	}
	return err
}

// publishStatus writes the link's state to path whenever it changes, until
// done is closed.
func publishStatus(path string, l *machinelink.Link, tick time.Duration, done <-chan struct{}) {
	var last []byte
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if b, err := json.Marshal(l.Status()); err == nil && !bytes.Equal(b, last) && writeStatusFile(path, b) == nil {
			last = b
		}
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

func writeStatusFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// writeLinkStatus is playkeeper status's part about the machine link: this
// machine's dashboard and its link's state, or, on a dashboard, the key
// fingerprint machines check when they join.
func writeLinkStatus(w io.Writer, cfg config.Config, configPath string, now time.Time) {
	if id, err := machinelink.LoadIdentity(filepath.Join(cfg.PanelDir(), "link.key")); err == nil {
		fmt.Fprintf(w, "Dashboard fingerprint: %s (machines check it when they join)\n", groupFingerprint(id.Fingerprint()))
	}
	d, err := machinelink.LoadDashboard(cfg.LinkDashboardPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return
	case err != nil:
		fmt.Fprintf(w, "Machine link: %v\n\n", linkError(err))
		return
	}
	fmt.Fprintf(w, "This machine is %s in the dashboard at %s.\n", d.Name, d.Address)
	if id, err := machinelink.LoadIdentity(cfg.LinkKeyPath()); err == nil {
		fmt.Fprintf(w, "  Fingerprint:  %s (the dashboard shows the same for %s)\n", groupFingerprint(id.Fingerprint()), d.Name)
	}
	fmt.Fprintf(w, "  Dashboard:    %s (its key)\n", groupFingerprint(d.Fingerprint()))
	var st machinelink.LinkStatus
	b, err := os.ReadFile(install.LinkStatusFile(cfg))
	if err != nil || json.Unmarshal(b, &st) != nil {
		how := "sudo systemctl status " + install.LinkUnit
		if cfg.Dev {
			how = "start it: playkeeper link --config " + absPath(configPath)
		}
		fmt.Fprintf(w, "  Link:         not running (%s)\n\n", how)
		return
	}
	fmt.Fprintf(w, "  Link:         %s\n", describeLink(st, now))
	if p := st.Problem; p != nil {
		fmt.Fprintf(w, "  Problem:      %s\n", p.Message)
		if p.Hint != "" {
			fmt.Fprintf(w, "                %s\n", p.Hint)
		}
	}
	fmt.Fprintln(w)
}

func describeLink(st machinelink.LinkStatus, now time.Time) string {
	switch st.State {
	case machinelink.LinkConnected:
		s := "connected since " + st.ConnectedAt.Local().Format("15:04")
		if !st.LastSeen.IsZero() {
			s += fmt.Sprintf(", last heard from the dashboard %s ago", now.Sub(st.LastSeen).Round(time.Second))
		}
		return s
	case machinelink.LinkConnecting:
		return "connecting…"
	case machinelink.LinkRetrying:
		return "not connected; trying again at " + st.NextAttempt.Local().Format("15:04:05")
	case machinelink.LinkRemoved:
		return "removed from the dashboard"
	case machinelink.LinkStopped:
		return "stopped"
	default:
		return string(st.State)
	}
}

// groupFingerprint splits a fingerprint into groups of four for reading,
// as the dashboard shows it.
func groupFingerprint(fp string) string {
	var groups []string
	for len(fp) > 4 {
		groups = append(groups, fp[:4])
		fp = fp[4:]
	}
	return strings.Join(append(groups, fp), " ")
}
