// Command playkeeper is the single Playkeeper binary: installer, root agent,
// web panel and a few recovery commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/install"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/panel"
	"github.com/CIYAhq/playkeeper/internal/update"
	"github.com/CIYAhq/playkeeper/internal/version"
	"github.com/CIYAhq/playkeeper/web"
)

const usage = `Playkeeper — your VPS, your game servers, your worlds.

Usage:
  sudo playkeeper install     [--panel-port 8443] [--game-port 25565] [--yes]
                              [--join ADDRESS --code CODE --fingerprint FP [--name NAME]]
                              (on a server that has Playkeeper, upgrades it in place)
  sudo playkeeper uninstall   [--yes] [--purge] [--keep-docker]
       playkeeper preflight   [--json]            check this host without changing it
  sudo playkeeper status                          show the server state from the agent
  sudo playkeeper join ADDRESS --code CODE --fingerprint FP [--name NAME]
                                                  connect this machine to another dashboard
  sudo playkeeper leave       [--force]           disconnect it from that dashboard
  sudo playkeeper setup-code                      new one-time setup code (before the first admin exists)
  sudo playkeeper reset-password <username>       print a new random password for an admin
  sudo playkeeper mcp                             serve the tools to an AI assistant over stdio (for SSH)
  sudo playkeeper reset-2fa <username>            turn off two-factor sign-in for a lost phone
       playkeeper version
       playkeeper dev         [--dir .dev]        run agent + panel locally for development

Services (started by systemd after install):
  playkeeper agent        --config /etc/playkeeper/config.json
  playkeeper panel        --config /etc/playkeeper/config.json
  playkeeper link         --config /etc/playkeeper/config.json   keeps a joined machine's link to its dashboard
  playkeeper self-update  --config /etc/playkeeper/config.json   installs an update the agent verified
  playkeeper units        --config /etc/playkeeper/config.json   prints this version's systemd units (JSON)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println(version.String())
	case "agent":
		err = runAgent(args)
	case "panel":
		err = runPanel(args)
	case "dev":
		err = runDev(args)
	case "install":
		err = runInstall(args)
	case "uninstall":
		err = runUninstall(args)
	case "preflight":
		err = runPreflight(args)
	case "status":
		err = runStatus(args)
	case "join":
		err = runJoin(args)
	case "leave":
		err = runLeave(args)
	case "link":
		err = runLink(args)
	case "setup-code":
		err = runSetupCode(args)
	case "reset-password":
		err = runResetPassword(args)
	case "mcp":
		err = runMCP(args)
	case "reset-2fa":
		err = runResetTwoFactor(args)
	case "self-update":
		err = runSelfUpdate(args)
	case "units":
		err = runUnits(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	a, err := agent.New(agent.Options{Config: cfg, Logger: logger(), UpdateKeys: update.TrustedKeys(), OfflineModeTest: os.Getenv(agent.OfflineModeEnv) == "1"})
	if err != nil {
		return err
	}
	defer a.Close()
	a.Start()
	ctx, cancel := signalContext()
	defer cancel()
	return a.Serve(ctx)
}

func runPanel(args []string) error {
	fs := flag.NewFlagSet("panel", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 && !cfg.Dev {
		return errors.New("the panel must not run as root; it runs as the 'playkeeper' user via systemd")
	}
	s, err := panel.New(panel.Options{Config: cfg, Logger: logger(), Static: web.Dist(), LinkRoutes: agent.LinkRoutes()})
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, cancel := signalContext()
	defer cancel()
	return s.ListenAndServeTLS(ctx)
}

// runDev runs agent and panel in one process with state under --dir, for
// contributors. It uses the real Docker daemon when the user can reach it.
func runDev(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	dir := fs.String("dir", ".dev", "state directory")
	panelPort := fs.Int("panel-port", 8443, "HTTPS port for the panel")
	gamePort := fs.Int("game-port", 25565, "Minecraft port")
	fs.Parse(args)
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(abs, "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		cfg = config.Default()
		cfg.InstallID = fmt.Sprintf("dev-%d", time.Now().Unix())
	}
	cfg.Dev = true
	devDefaults(&cfg)
	cfg.DataDir = filepath.Join(abs, "data")
	cfg.SocketPath = filepath.Join(abs, "agent.sock")
	cfg.PanelUser = u.Username
	cfg.PanelPort, cfg.GamePort = *panelPort, *gamePort
	cfg.GameUID, _ = strconv.Atoi(u.Uid)
	cfg.GameGID, _ = strconv.Atoi(u.Gid)
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		cfg.DockerSocket = strings.TrimPrefix(h, "unix://")
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	log := logger()
	a, err := agent.New(agent.Options{Config: cfg, Logger: log, AllowedUIDs: []uint32{uint32(os.Getuid())}, UpdateKeys: update.TrustedKeys(), OfflineModeTest: os.Getenv(agent.OfflineModeEnv) == "1"})
	if err != nil {
		return err
	}
	defer a.Close()
	a.Start()
	s, err := panel.New(panel.Options{Config: cfg, Logger: log, Static: web.Dist(), LinkRoutes: agent.LinkRoutes()})
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, cancel := signalContext()
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- a.Serve(ctx) }()
	go func() { errc <- s.ListenAndServeTLS(ctx) }()
	if users, _ := s.Usernames(); len(users) == 0 {
		code, err := panel.NewSetupToken(cfg.SetupTokenPath(), 24*time.Hour, time.Now())
		if err != nil {
			return err
		}
		fmt.Printf("\nPlaykeeper dev server\n  Open: https://localhost:%d/setup#code=%s\n  (self-signed certificate; state in %s)\n\n", cfg.PanelPort, code, abs)
	} else {
		fmt.Printf("\nPlaykeeper dev server\n  Open: https://localhost:%d  (sign in as %s)\n\n", cfg.PanelPort, strings.Join(users, ", "))
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

// A dev install talks to a names service on this computer (where
// scripts/names-check.sh runs one) and Let's Encrypt's staging CA unless
// .dev/config.json names others, so make dev never claims real names or
// certificates by accident.
const (
	devNamesURL         = "http://127.0.0.1:8081"
	devACMEDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

func devDefaults(cfg *config.Config) {
	if cfg.NamesURL == "" {
		cfg.NamesURL = devNamesURL
	}
	if cfg.ACMEDirectoryURL == "" {
		cfg.ACMEDirectoryURL = devACMEDirectoryURL
	}
}

func installFlags(fs *flag.FlagSet) *install.Options {
	o := &install.Options{In: os.Stdin, Out: os.Stdout}
	fs.IntVar(&o.PanelPort, "panel-port", config.DefaultPanelPort, "HTTPS port for the web panel")
	fs.IntVar(&o.GamePort, "game-port", config.DefaultGamePort, "TCP port Minecraft players connect to")
	fs.BoolVar(&o.Yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&o.AllowUntestedOS, "allow-untested-os", false, "continue on an operating system or CPU Playkeeper is not tested on")
	fs.BoolVar(&o.AllowExistingMinecraft, "allow-existing-minecraft", false, "continue although another Minecraft setup exists (Playkeeper never touches it)")
	fs.StringVar(&o.ReleaseURL, "release-url", "", "where the installed Playkeeper looks for updates (default: the latest GitHub release); get.sh sets it when it downloads from elsewhere")
	fs.StringVar(&o.Join, "join", "", "after installing, join the dashboard at this address (from its join command); no dashboard runs here then")
	return o
}

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	o := installFlags(fs)
	j := joinArgs{config: config.DefaultPath}
	fs.StringVar(&j.code, "code", "", "with --join: the join code from the dashboard's command")
	fs.StringVar(&j.fingerprint, "fingerprint", "", "with --join: the dashboard's fingerprint from its command")
	fs.StringVar(&j.name, "name", "", "with --join: what the dashboard calls this machine (default: its host name)")
	fs.Parse(args)
	if o.PanelPort == o.GamePort {
		return errors.New("--panel-port and --game-port must differ")
	}
	if o.ReleaseURL != "" {
		if _, err := update.CheckReleaseURL(o.ReleaseURL); err != nil {
			return err
		}
	}
	switch {
	case o.Join == "" && (j.code != "" || j.fingerprint != "" || j.name != ""):
		return errors.New("--code, --fingerprint and --name go with --join ADDRESS; copy the whole command from the dashboard (Settings › Machines)")
	case o.Join != "" && (j.code == "" || j.fingerprint == ""):
		return errors.New("--join needs --code and --fingerprint too; copy the whole command from the dashboard (Settings › Machines)")
	case o.Join != "":
		if _, err := machinelink.NewCommand(o.Join, j.code, j.fingerprint); err != nil {
			return linkError(err)
		}
		j.address = o.Join
	}
	ctx, cancel := signalContext()
	defer cancel()
	res, err := install.Run(ctx, install.Real(), *o, version.Version)
	if err != nil {
		return err
	}
	switch {
	case res.UpToDate:
	case res.Upgraded:
		writeUpgradeSummary(os.Stdout, res)
	case res.NoPanel:
		fmt.Printf("\nPlaykeeper is installed (in %s).\n\n", res.Duration.Round(time.Second))
	default:
		writeInstallSummary(os.Stdout, res)
	}
	if o.Join == "" {
		return nil
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return err
	}
	return joinAfterInstall(ctx, os.Stdout, install.Real(), cfg, j)
}

func writeUpgradeSummary(w io.Writer, res *install.Result) {
	fmt.Fprintf(w, "\nPlaykeeper was upgraded from %s to %s in %s. Your worlds, backups and settings were kept.\n", res.FromVersion, version.Version, res.Duration.Round(time.Second))
	if res.NoPanel {
		fmt.Fprintf(w, "This machine has no dashboard of its own: its servers are in the dashboard it joined (sudo playkeeper status says which).\n")
		return
	}
	fmt.Fprintf(w, "Open %s and sign in as before", res.URL)
	if res.Fingerprint != "" {
		fmt.Fprintf(w, " (certificate fingerprint %s)", res.Fingerprint)
	}
	fmt.Fprintf(w, ".\nFrom now on, Playkeeper shows new versions in the dashboard (Settings) and installs them from there.\n")
}

// runSelfUpdate is the updater that playkeeper-update.service starts.
func runSelfUpdate(args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	if os.Geteuid() != 0 {
		return errors.New("the updater runs as root (systemd starts it as playkeeper-update.service)")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return install.SelfUpdate(ctx, install.Real(), cfg, version.Version, update.TrustedKeys(), os.Stdout)
}

// runUnits prints the systemd units this version installs, so an updater
// running the previous version can install them.
func runUnits(args []string) error {
	fs := flag.NewFlagSet("units", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(install.Units(cfg, install.Joined(cfg, "/")))
}

// writeInstallSummary tells the user what to do next. A reinstall that kept an
// admin account gets sign-in instructions instead of a setup code.
func writeInstallSummary(w io.Writer, res *install.Result) {
	fmt.Fprintf(w, "\nPlaykeeper is running.\n\n")
	if res.SetupCode != "" {
		fmt.Fprintf(w, "  1. Open this link in your browser:\n       %s/setup#code=%s\n", res.URL, res.SetupCode)
		fmt.Fprintf(w, "     (setup code: %s — works once, expires in 24 hours)\n", res.SetupCode)
	} else {
		fmt.Fprintf(w, "  1. Open %s and sign in with your existing admin account.\n", res.URL)
	}
	fmt.Fprintf(w, "  2. Your browser will warn that the certificate is self-signed. Continue only if it shows\n     this SHA-256 fingerprint:\n       %s\n", res.Fingerprint)
	if res.SetupCode != "" {
		fmt.Fprintf(w, "  3. Create your admin account, accept the Minecraft EULA and start your server.\n\n")
	} else {
		fmt.Fprintf(w, "  3. Your worlds and backups were kept; the server starts again if it was running before.\n\n")
	}
	fmt.Fprintf(w, "If %s is not your public address, use your VPS's public IP instead.\n", strings.TrimPrefix(res.URL, "https://"))
	if res.SetupCode != "" {
		fmt.Fprintf(w, "Lost the setup code? sudo playkeeper setup-code\n")
	} else {
		fmt.Fprintf(w, "Forgot the password? sudo playkeeper reset-password <username>\n")
	}
	fmt.Fprintf(w, "Uninstall any time: sudo playkeeper uninstall  (keeps your worlds and backups)\n")
	fmt.Fprintf(w, "Install finished in %s.\n", res.Duration.Round(time.Second))
}

func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	o := install.UninstallOptions{In: os.Stdin, Out: os.Stdout}
	fs.BoolVar(&o.Yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&o.Purge, "purge", false, "also delete /var/lib/playkeeper (worlds and backups); asks you to type a phrase")
	fs.BoolVar(&o.PurgeConfirmed, "yes-delete-worlds", false, "with --purge: confirm deleting worlds without the typed phrase")
	fs.BoolVar(&o.KeepDocker, "keep-docker", false, "keep Docker even if Playkeeper installed it")
	fs.Parse(args)
	ctx, cancel := signalContext()
	defer cancel()
	return install.Uninstall(ctx, install.Real(), o)
}

func runPreflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	o := installFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	f := install.Preflight(context.Background(), install.Real(), *o)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"ok": f.OK(), "checks": f.Checks})
	} else {
		install.PrintChecks(os.Stdout, f)
	}
	if !f.OK() {
		return errors.New("this host is not ready for Playkeeper (see FAIL items)")
	}
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	writeLinkStatus(os.Stdout, cfg, *path, time.Now())
	var servers []api.ServerStatus
	if _, err := agentclient.New(cfg.SocketPath).Do(context.Background(), "GET", "/v1/servers", nil, nil, &servers); err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Println("No servers yet. Create one in the dashboard.")
	}
	for i, st := range servers {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("Server:   %s (%s)\nPhase:    %s %s\nDesired:  %s\nReachable on port %d from this host: %v\n", st.Name, st.ID, st.Phase, st.PhaseDetail, st.Desired, st.GamePort, st.Reachable)
		if st.Config != nil {
			fmt.Printf("Version:  %s (Paper build %d)\nMemory:   %d MB budget, %d MB Java heap\n", st.Config.MinecraftVersion, st.Config.PaperBuild, st.Config.MemoryMB, st.Config.HeapMB)
		}
		if st.Players != nil {
			fmt.Printf("Players:  %d/%d %s (%s)\n", st.Players.Online, st.Players.Max, strings.Join(st.Players.Names, ", "), st.Players.Source)
		}
		if st.LastError != "" {
			fmt.Printf("Problem:  %s\n          %s\n", st.LastError, st.LastErrorHint)
		}
	}
	return nil
}

func panelUserOwn(path string) {
	if u, err := user.Lookup(config.DefaultPanelUser); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		os.Chown(path, uid, gid)
	}
}

func runSetupCode(args []string) error {
	fs := flag.NewFlagSet("setup-code", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo playkeeper setup-code")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	s, err := panel.New(panel.Options{Config: cfg, Logger: logger()})
	if err != nil {
		return err
	}
	defer s.Close()
	if users, _ := s.Usernames(); len(users) > 0 {
		return fmt.Errorf("an admin account already exists (%s); use: sudo playkeeper reset-password %s", strings.Join(users, ", "), users[0])
	}
	code, err := panel.NewSetupToken(cfg.SetupTokenPath(), 24*time.Hour, time.Now())
	if err != nil {
		return err
	}
	panelUserOwn(cfg.SetupTokenPath())
	for _, p := range []string{filepath.Join(cfg.PanelDir(), "panel.db"), filepath.Join(cfg.PanelDir(), "panel.db-wal"), filepath.Join(cfg.PanelDir(), "panel.db-shm")} {
		panelUserOwn(p)
	}
	fmt.Printf("New setup code: %s (works once, expires in 24 hours)\nOpen: https://YOUR-SERVER-IP:%d/setup#code=%s\n", code, cfg.PanelPort, code)
	return nil
}

func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: sudo playkeeper reset-password <username>")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo playkeeper reset-password <username>")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	s, err := panel.New(panel.Options{Config: cfg, Logger: logger()})
	if err != nil {
		return err
	}
	defer s.Close()
	pw := panel.RandomPassword()
	if err := s.ResetAdmin(fs.Arg(0), pw); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(cfg.PanelDir(), "panel.db"), filepath.Join(cfg.PanelDir(), "panel.db-wal"), filepath.Join(cfg.PanelDir(), "panel.db-shm")} {
		panelUserOwn(p)
	}
	fmt.Printf("New password for %s: %s\nSign in and change it under Settings. All of %s's sessions were signed out.\n", fs.Arg(0), pw, fs.Arg(0))
	return nil
}

func runResetTwoFactor(args []string) error {
	fs := flag.NewFlagSet("reset-2fa", flag.ExitOnError)
	path := fs.String("config", config.DefaultPath, "config file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: sudo playkeeper reset-2fa <username>")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root: sudo playkeeper reset-2fa <username>")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	s, err := panel.New(panel.Options{Config: cfg, Logger: logger()})
	if err != nil {
		return err
	}
	defer s.Close()
	name := fs.Arg(0)
	wasOn, err := s.ResetTwoFactor(name)
	if err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(cfg.PanelDir(), "panel.db"), filepath.Join(cfg.PanelDir(), "panel.db-wal"), filepath.Join(cfg.PanelDir(), "panel.db-shm")} {
		panelUserOwn(p)
	}
	if !wasOn {
		fmt.Printf("Two-factor sign-in was already off for %s. Nothing changed.\n", name)
		return nil
	}
	fmt.Printf("Two-factor sign-in is off for %s, and every session was signed out. Sign in with the password and turn it on again on the Account page.\n", name)
	return nil
}
