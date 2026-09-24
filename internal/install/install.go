package install

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/panel"
	_ "modernc.org/sqlite"
)

const (
	BinPath          = "/usr/local/bin/playkeeper"
	ConfigDir        = "/etc/playkeeper"
	UnitDir          = "/etc/systemd/system"
	AgentUnit        = "playkeeper-agent.service"
	PanelUnit        = "playkeeper-panel.service"
	MinMemoryMB      = 2304
	MinDiskBytes     = 3 << 30
	RecommendedDisk  = 5 << 30
	SupportedOS      = "ubuntu"
	SupportedVersion = "24.04"
	// FailStepEnv makes the named step fail after it ran, to exercise rollback
	// in integration tests. It has no other effect.
	FailStepEnv = "PLAYKEEPER_TEST_FAIL_INSTALL_STEP"
)

type Options struct {
	PanelPort              int
	GamePort               int
	Yes                    bool
	AllowUntestedOS        bool
	AllowExistingMinecraft bool
	In                     io.Reader
	Out                    io.Writer
}

type Check struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"` // pass | warn | fail | info
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Facts are what preflight learned; the plan and steps depend on them.
type Facts struct {
	Checks        []Check
	DockerPresent bool
	DockerVersion string
	UFWActive     bool
	ReuseData     bool
	ExistingAdmin bool
	PanelURLHost  string
}

func (f Facts) OK() bool {
	for _, c := range f.Checks {
		if c.Status == "fail" {
			return false
		}
	}
	return true
}

var unitPattern = regexp.MustCompile(`(?i)(minecraft|crafty|pterodactyl|wings|pufferpanel|ampinstmgr|papermc|spigot|bukkit|mcserver)`)
var javaMC = regexp.MustCompile(`(?i)java\b.*(server\.jar|paper|spigot|bukkit|forge|fabric|minecraft|craftbukkit|purpur)`)

var panelDirs = []string{"/var/opt/minecraft/crafty", "/opt/crafty", "/opt/crafty-4", "/etc/pterodactyl", "/var/lib/pterodactyl", "/etc/pufferpanel", "/var/lib/pufferpanel", "/home/amp/.ampdata", "/opt/pelican"}

// Preflight inspects the host without changing anything.
func Preflight(ctx context.Context, sys System, o Options) Facts {
	var f Facts
	add := func(id, label, status, detail, fix string) {
		f.Checks = append(f.Checks, Check{ID: id, Label: label, Status: status, Detail: detail, Fix: fix})
	}
	if sys.IsRoot() {
		add("root", "Administrator rights", "pass", "Running as root.", "")
	} else {
		add("root", "Administrator rights", "fail", "The installer must run as root.", "Re-run the command with sudo.")
	}
	id, ver := osRelease(sys)
	switch {
	case id == SupportedOS && ver == SupportedVersion:
		add("os", "Operating system", "pass", "Ubuntu 24.04 LTS (the tested platform).", "")
	case o.AllowUntestedOS:
		add("os", "Operating system", "warn", fmt.Sprintf("%s %s is not tested; continuing because --allow-untested-os was given.", nonEmpty(id, "unknown"), ver), "")
	default:
		add("os", "Operating system", "fail", fmt.Sprintf("Found %s %s. Playkeeper is only tested on Ubuntu 24.04 LTS.", nonEmpty(id, "an unknown system"), ver),
			"Use an Ubuntu 24.04 LTS server, or pass --allow-untested-os to try anyway (unsupported).")
	}
	switch arch := sys.Arch(); {
	case arch == "amd64":
		add("arch", "CPU architecture", "pass", "x86_64 (amd64).", "")
	case o.AllowUntestedOS:
		add("arch", "CPU architecture", "warn", arch+" is not tested.", "")
	default:
		add("arch", "CPU architecture", "fail", arch+" is not tested; only x86_64 (amd64) is supported.", "Use an x86_64 server, or pass --allow-untested-os (unsupported).")
	}
	if st, err := os.Stat(sys.P("/run/systemd/system")); err == nil && st.IsDir() {
		add("systemd", "Service manager", "pass", "systemd is running.", "")
	} else {
		add("systemd", "Service manager", "fail", "systemd is not running; Playkeeper's services need it.", "Use a standard Ubuntu 24.04 server (not a container) with systemd.")
	}
	mem := sys.MemTotalMB()
	switch {
	case mem >= 2900:
		add("memory", "Memory", "pass", fmt.Sprintf("%d MB RAM.", mem), "")
	case mem >= MinMemoryMB:
		add("memory", "Memory", "warn", fmt.Sprintf("%d MB RAM is enough for a small server; 3 GB or more is the tested size.", mem), "")
	default:
		add("memory", "Memory", "fail", fmt.Sprintf("%d MB RAM; at least %d MB is needed (1.5 GB for Minecraft plus room for the system).", mem, MinMemoryMB), "Use a VPS with at least 3 GB of RAM.")
	}
	free := sys.DiskFree(sys.P("/var/lib"))
	switch {
	case free >= RecommendedDisk:
		add("disk", "Disk space", "pass", fmt.Sprintf("%.1f GB free under /var/lib.", gb(free)), "")
	case free >= MinDiskBytes:
		add("disk", "Disk space", "warn", fmt.Sprintf("%.1f GB free under /var/lib; worlds and backups grow over time.", gb(free)), "Keep at least 5 GB free.")
	default:
		add("disk", "Disk space", "fail", fmt.Sprintf("Only %.1f GB free under /var/lib; at least 3 GB is needed.", gb(free)), "Free disk space (for example: sudo apt-get clean; sudo journalctl --vacuum-size=200M) or use a larger disk.")
	}
	for _, p := range []struct {
		port int
		what string
		flag string
	}{{o.PanelPort, "web panel (HTTPS)", "--panel-port"}, {o.GamePort, "Minecraft players", "--game-port"}} {
		if sys.Listening(p.port) {
			add("port-"+strconv.Itoa(p.port), fmt.Sprintf("Port %d", p.port), "fail", fmt.Sprintf("Port %d (for the %s) is already used by another program.", p.port, p.what),
				fmt.Sprintf("See what uses it with: sudo ss -ltnp 'sport = :%d'. Stop that program, or choose another port with %s.", p.port, p.flag))
		} else {
			add("port-"+strconv.Itoa(p.port), fmt.Sprintf("Port %d", p.port), "pass", fmt.Sprintf("Free for the %s.", p.what), "")
		}
	}
	var existing []string
	for _, dir := range []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system"} {
		entries, _ := os.ReadDir(sys.P(dir))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "playkeeper-") {
				continue
			}
			if strings.HasSuffix(e.Name(), ".service") && unitPattern.MatchString(e.Name()) {
				existing = append(existing, "service "+e.Name())
			}
		}
	}
	for _, d := range panelDirs {
		if _, err := os.Stat(sys.P(d)); err == nil {
			existing = append(existing, "directory "+d)
		}
	}
	for _, p := range sys.Processes() {
		if javaMC.MatchString(p) {
			existing = append(existing, "running process: "+truncate(p, 80))
		}
	}
	di, derr := sys.Docker(ctx)
	if derr == nil {
		f.DockerPresent, f.DockerVersion = true, di.Version
		for _, c := range di.Containers {
			if c.Labels["io.playkeeper.managed"] == "true" {
				continue
			}
			if strings.Contains(strings.ToLower(c.Image), "minecraft") {
				existing = append(existing, "Docker container "+strings.TrimPrefix(strings.Join(c.Names, ","), "/")+" ("+c.Image+")")
			}
		}
	}
	sort.Strings(existing)
	switch {
	case len(existing) == 0:
		add("existing", "Existing Minecraft setups", "pass", "None found.", "")
	case o.AllowExistingMinecraft:
		add("existing", "Existing Minecraft setups", "warn", "Found "+strings.Join(existing, "; ")+". Playkeeper will not touch them (--allow-existing-minecraft).", "")
	default:
		add("existing", "Existing Minecraft setups", "fail", "Found "+strings.Join(existing, "; ")+". Playkeeper will not take over or run next to an existing Minecraft setup by default.",
			"Leave that server alone and use a different VPS, or, if you are sure the two will not conflict, re-run with --allow-existing-minecraft (Playkeeper never modifies it) and a free --game-port.")
	}
	if _, err := os.Stat(sys.P(ConfigDir + "/config.json")); err == nil {
		add("installed", "Existing Playkeeper", "fail", "Playkeeper is already installed.", "To reinstall, run: sudo playkeeper uninstall (keeps worlds and backups), then install again.")
	} else if _, err := os.Stat(sys.P(filepath.Join(config.DefaultDataDir, "server", "data"))); err == nil {
		f.ReuseData = true
		f.ExistingAdmin = hasAdmin(sys.P(filepath.Join(config.DefaultDataDir, "panel", "panel.db")))
		add("reuse", "Previous Playkeeper data", "info", "Found worlds and backups from an earlier Playkeeper install in /var/lib/playkeeper; they will be reused, not changed.", "")
	}
	if derr == nil {
		add("docker", "Docker", "pass", "Docker "+di.Version+" is running; Playkeeper only manages its own container.", "")
	} else if _, err := os.Stat(sys.P("/usr/bin/apt-get")); err == nil {
		add("docker", "Docker", "info", "Docker is not installed. The installer will install Ubuntu's docker.io package.", "")
	} else {
		add("docker", "Docker", "fail", "Docker is not installed and apt-get is unavailable.", "Install Docker Engine, then run the installer again.")
	}
	if out, err := sys.Run("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
		f.UFWActive = true
		add("firewall", "Firewall (ufw)", "info", fmt.Sprintf("ufw is active; the installer will allow ports %d and %d.", o.PanelPort, o.GamePort), "")
	} else {
		add("firewall", "Firewall", "info", "No active ufw firewall. If your provider has a cloud firewall, allow these ports there: "+strconv.Itoa(o.PanelPort)+"/tcp and "+strconv.Itoa(o.GamePort)+"/tcp.", "")
	}
	f.PanelURLHost = primaryIP()
	add("tls", "HTTPS", "info", "A self-signed certificate will be generated on this server. Your browser will ask you to trust it; compare the fingerprint the installer prints.", "")
	return f
}

func hasAdmin(dbPath string) bool {
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n) == nil && n > 0
}

func osRelease(sys System) (id, version string) {
	b, err := os.ReadFile(sys.P("/etc/os-release"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

func primaryIP() string {
	ips := panel.HostIPs()
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	if len(ips) > 0 {
		return "[" + ips[0].String() + "]"
	}
	return "YOUR-SERVER-IP"
}

func gb(n int64) float64 { return float64(n) / (1 << 30) }

func nonEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// PrintChecks renders preflight results for the terminal.
func PrintChecks(w io.Writer, f Facts) {
	icon := map[string]string{"pass": "ok  ", "warn": "WARN", "fail": "FAIL", "info": "info"}
	for _, c := range f.Checks {
		fmt.Fprintf(w, "  [%s] %-26s %s\n", icon[c.Status], c.Label, c.Detail)
		if c.Fix != "" && c.Status != "pass" {
			fmt.Fprintf(w, "         → Fix: %s\n", c.Fix)
		}
	}
}

// Plan describes every change the installer will make.
func Plan(f Facts, o Options) []string {
	var p []string
	if !f.DockerPresent {
		p = append(p, "Packages:  install docker.io from Ubuntu's archive (with the packages it depends on)")
	}
	p = append(p,
		"Users:     create 'playkeeper' (runs the web panel; no login shell; not in the docker group)",
		"           create 'playkeeper-mc' (owns world files; the Minecraft container runs as this user)",
		"Files:     "+BinPath,
		"           "+ConfigDir+"/config.json",
		"           "+UnitDir+"/"+AgentUnit+" and "+UnitDir+"/"+PanelUnit,
	)
	if f.ReuseData {
		p = append(p, "Data:      reuse /var/lib/playkeeper (existing worlds and backups are kept as they are)")
	} else {
		p = append(p, "Data:      /var/lib/playkeeper (worlds, backups, settings)")
	}
	p = append(p,
		"Services:  playkeeper-agent (root; local Unix socket only, no network port)",
		fmt.Sprintf("           playkeeper-panel (HTTPS on port %d)", o.PanelPort),
		fmt.Sprintf("Ports:     %d/tcp web panel now; %d/tcp Minecraft once you create a server", o.PanelPort, o.GamePort),
	)
	if f.UFWActive {
		p = append(p, fmt.Sprintf("Firewall:  ufw allow %d/tcp and %d/tcp", o.PanelPort, o.GamePort))
	}
	p = append(p, "Untouched: your other services, existing Docker containers, SSH and firewall rules")
	return p
}

// Manifest records everything an install created, for uninstall.
type Manifest struct {
	Version           string    `json:"version"`
	InstalledAt       time.Time `json:"installedAt"`
	InstallID         string    `json:"installId"`
	PanelPort         int       `json:"panelPort"`
	GamePort          int       `json:"gamePort"`
	PackagesInstalled []string  `json:"packagesInstalled"`
	UsersCreated      []string  `json:"usersCreated"`
	GroupsCreated     []string  `json:"groupsCreated"`
	FilesCreated      []string  `json:"filesCreated"`
	DirsCreated       []string  `json:"dirsCreated"`
	Units             []string  `json:"units"`
	FirewallRules     []string  `json:"firewallRules"`
	ReusedData        bool      `json:"reusedData"`
	KeptOnUninstall   []string  `json:"keptOnUninstall"`
}

type step struct {
	name string
	do   func() error
	undo func() error
}

type installer struct {
	sys  System
	o    Options
	f    Facts
	m    Manifest
	cfg  config.Config
	out  io.Writer
	done []step
}

// Result is what a successful install prints for the user.
type Result struct {
	URL         string
	SetupCode   string
	Fingerprint string
	Duration    time.Duration
	ExistingAdm bool
}

// Run installs Playkeeper. On any failure every completed step is undone.
func Run(ctx context.Context, sys System, o Options, version string) (*Result, error) {
	start := sys.Now()
	out := o.Out
	fmt.Fprintf(out, "Playkeeper %s installer\n\nChecking this server (nothing is changed yet):\n", version)
	f := Preflight(ctx, sys, o)
	PrintChecks(out, f)
	if !f.OK() {
		return nil, errors.New("preflight failed; fix the items marked FAIL above. Nothing was changed")
	}
	fmt.Fprintf(out, "\nPlaykeeper will make these changes:\n")
	for _, line := range Plan(f, o) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprintf(out, "\nTo undo later: sudo playkeeper uninstall   (removes Playkeeper, keeps your worlds and backups)\n\n")
	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")
		ans, _ := bufio.NewReader(o.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return nil, errDeclined
		}
	}
	in := &installer{sys: sys, o: o, f: f, out: out}
	in.m = Manifest{Version: version, InstalledAt: start.UTC(), PanelPort: o.PanelPort, GamePort: o.GamePort, ReusedData: f.ReuseData,
		KeptOnUninstall: []string{config.DefaultDataDir + "/server (worlds)", config.DefaultDataDir + "/backups", config.DefaultDataDir + " (settings, admin account, analytics)"}}
	res, err := in.run(ctx)
	if err != nil {
		fmt.Fprintf(out, "\nInstall failed: %v\nRolling back:\n", err)
		problems := in.rollback()
		if len(problems) == 0 {
			fmt.Fprintln(out, "Rollback complete: the server is back to how it was before the install.")
		} else {
			fmt.Fprintln(out, "Rollback finished with problems; please remove these by hand:")
			for _, p := range problems {
				fmt.Fprintln(out, "  - "+p)
			}
		}
		return nil, err
	}
	res.Duration = sys.Now().Sub(start)
	return res, nil
}

func (in *installer) exec(s step) error {
	fmt.Fprintf(in.out, "  • %s\n", s.name)
	err := s.do()
	if s.undo != nil {
		in.done = append(in.done, s)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}
	if os.Getenv(FailStepEnv) == s.name {
		return fmt.Errorf("%s: injected failure (%s)", s.name, FailStepEnv)
	}
	return nil
}

func (in *installer) rollback() []string {
	var problems []string
	for i := len(in.done) - 1; i >= 0; i-- {
		s := in.done[i]
		fmt.Fprintf(in.out, "  ↺ undo: %s\n", s.name)
		if err := s.undo(); err != nil {
			problems = append(problems, s.name+": "+err.Error())
		}
	}
	return problems
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (in *installer) run(ctx context.Context) (*Result, error) {
	sys := in.sys
	cfg := config.Default()
	cfg.PanelPort, cfg.GamePort = in.o.PanelPort, in.o.GamePort
	cfg.InstallID = randomHex(16)
	in.m.InstallID = cfg.InstallID
	fmt.Fprintln(in.out, "\nInstalling:")

	if !in.f.DockerPresent {
		var before map[string]bool
		dockerGroupExisted := groupExists(sys, "docker")
		if err := in.exec(step{name: "install Docker (docker.io)", do: func() error {
			var err error
			if before, err = installedPackages(sys); err != nil {
				return err
			}
			if _, err := sys.Run("apt-get", "update"); err != nil {
				return err
			}
			_, err = sys.Run("apt-get", "install", "-y", "--no-install-recommends", "docker.io")
			after, perr := installedPackages(sys)
			if perr == nil {
				for p := range after {
					if !before[p] {
						in.m.PackagesInstalled = append(in.m.PackagesInstalled, p)
					}
				}
				sort.Strings(in.m.PackagesInstalled)
			}
			if err != nil {
				return err
			}
			if _, err := sys.Run("systemctl", "enable", "--now", "docker.service"); err != nil {
				return err
			}
			return waitFor(ctx, 60*time.Second, func() bool { _, err := sys.Docker(ctx); return err == nil })
		}, undo: func() error {
			if len(in.m.PackagesInstalled) == 0 {
				return nil
			}
			args := append([]string{"purge", "-y"}, in.m.PackagesInstalled...)
			_, err := sys.Run("apt-get", args...)
			if err == nil && !dockerGroupExisted && groupExists(sys, "docker") {
				_, err = sys.Run("groupdel", "docker")
			}
			return err
		}}); err != nil {
			return nil, err
		}
	}

	for _, u := range []struct{ name, home string }{{config.DefaultPanelUser, config.DefaultDataDir + "/panel"}, {config.DefaultGameUser, config.DefaultDataDir + "/server"}} {
		u := u
		if _, _, ok := sys.LookupUser(u.name); ok {
			continue
		}
		if err := in.exec(step{name: "create user " + u.name, do: func() error {
			if _, err := sys.Run("groupadd", "--system", u.name); err != nil {
				return err
			}
			in.m.GroupsCreated = append(in.m.GroupsCreated, u.name)
			if _, err := sys.Run("useradd", "--system", "--gid", u.name, "--home-dir", u.home, "--no-create-home", "--shell", "/usr/sbin/nologin", u.name); err != nil {
				return err
			}
			in.m.UsersCreated = append(in.m.UsersCreated, u.name)
			return nil
		}, undo: func() error {
			var errs []error
			if contains(in.m.UsersCreated, u.name) {
				if _, err := sys.Run("userdel", u.name); err != nil {
					errs = append(errs, err)
				}
			}
			if contains(in.m.GroupsCreated, u.name) {
				if _, err := sys.Run("groupdel", u.name); err != nil && !strings.Contains(err.Error(), "does not exist") {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}}); err != nil {
			return nil, err
		}
	}
	puid, pgid, _ := sys.LookupUser(config.DefaultPanelUser)
	guid, ggid, _ := sys.LookupUser(config.DefaultGameUser)
	cfg.GameUID, cfg.GameGID = guid, ggid
	in.cfg = cfg

	type dir struct {
		path     string
		mode     os.FileMode
		uid, gid int
	}
	dirs := []dir{
		{ConfigDir, 0o755, 0, 0},
		{cfg.DataDir, 0o755, 0, 0},
		{cfg.AgentDir(), 0o700, 0, 0},
		{cfg.PanelDir(), 0o700, puid, pgid},
		{filepath.Dir(cfg.ServerDataDir()), 0o755, 0, 0},
		{cfg.ServerDataDir(), 0o750, guid, ggid},
		{cfg.BackupsDir(), 0o700, 0, 0},
		{cfg.StagingDir(), 0o700, 0, 0},
	}
	if err := in.exec(step{name: "create directories", do: func() error {
		for _, d := range dirs {
			p := sys.P(d.path)
			if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(p, d.mode); err != nil {
					return err
				}
				in.m.DirsCreated = append(in.m.DirsCreated, d.path)
			}
			if err := os.Chmod(p, d.mode); err != nil {
				return err
			}
			if sys.IsRoot() {
				if err := chownR(sys, p, d.uid, d.gid, d.path == cfg.PanelDir() || d.path == cfg.ServerDataDir()); err != nil {
					return err
				}
			}
		}
		return nil
	}, undo: func() error {
		var errs []error
		for i := len(in.m.DirsCreated) - 1; i >= 0; i-- {
			if err := os.RemoveAll(sys.P(in.m.DirsCreated[i])); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}}); err != nil {
		return nil, err
	}

	if err := in.exec(step{name: "install " + BinPath, do: func() error {
		src, err := sys.Executable()
		if err != nil {
			return err
		}
		if err := copyFile(src, sys.P(BinPath), 0o755); err != nil {
			return err
		}
		in.m.FilesCreated = append(in.m.FilesCreated, BinPath)
		return nil
	}, undo: func() error { return removeIfExists(sys.P(BinPath)) }}); err != nil {
		return nil, err
	}

	if err := in.exec(step{name: "write " + ConfigDir + "/config.json", do: func() error {
		if err := cfg.Save(sys.P(ConfigDir + "/config.json")); err != nil {
			return err
		}
		in.m.FilesCreated = append(in.m.FilesCreated, ConfigDir+"/config.json")
		return nil
	}, undo: func() error { return removeIfExists(sys.P(ConfigDir + "/config.json")) }}); err != nil {
		return nil, err
	}

	res := &Result{URL: fmt.Sprintf("https://%s:%d", in.f.PanelURLHost, cfg.PanelPort), ExistingAdm: in.f.ExistingAdmin}
	if err := in.exec(step{name: "generate HTTPS certificate and first-run setup code", do: func() error {
		tlsDir := sys.P(cfg.TLSDir())
		fp, err := panel.EnsureSelfSignedCert(tlsDir, sys.Now())
		if err != nil {
			return err
		}
		res.Fingerprint = fp
		if !in.f.ExistingAdmin {
			code, err := panel.NewSetupToken(sys.P(cfg.SetupTokenPath()), 24*time.Hour, sys.Now())
			if err != nil {
				return err
			}
			res.SetupCode = code
		}
		if sys.IsRoot() {
			return chownR(sys, sys.P(cfg.PanelDir()), puid, pgid, true)
		}
		return nil
	}, undo: func() error {
		if in.f.ReuseData {
			return nil
		}
		os.Remove(sys.P(cfg.SetupTokenPath()))
		return os.RemoveAll(sys.P(cfg.TLSDir()))
	}}); err != nil {
		return nil, err
	}

	if err := in.exec(step{name: "install and start systemd services", do: func() error {
		units := map[string]string{AgentUnit: agentUnit(), PanelUnit: panelUnit(cfg.PanelPort)}
		for _, name := range []string{AgentUnit, PanelUnit} {
			if err := os.WriteFile(sys.P(UnitDir+"/"+name), []byte(units[name]), 0o644); err != nil {
				return err
			}
			in.m.FilesCreated = append(in.m.FilesCreated, UnitDir+"/"+name)
			in.m.Units = append(in.m.Units, name)
		}
		if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := sys.Run("systemctl", "enable", "--now", AgentUnit); err != nil {
			return err
		}
		if _, err := sys.Run("systemctl", "enable", "--now", PanelUnit); err != nil {
			return err
		}
		hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return sys.WaitHealthy(hctx, cfg.SocketPath, sys.P(filepath.Join(cfg.TLSDir(), "cert.pem")), cfg.PanelPort)
	}, undo: func() error {
		var errs []error
		for _, name := range []string{PanelUnit, AgentUnit} {
			if _, err := os.Stat(sys.P(UnitDir + "/" + name)); err != nil {
				continue
			}
			if _, err := sys.Run("systemctl", "disable", "--now", name); err != nil {
				errs = append(errs, err)
			}
			if err := removeIfExists(sys.P(UnitDir + "/" + name)); err != nil {
				errs = append(errs, err)
			}
		}
		if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}}); err != nil {
		return nil, err
	}

	if in.f.UFWActive {
		if err := in.exec(step{name: "allow panel and game ports in ufw", do: func() error {
			for _, p := range []int{cfg.PanelPort, cfg.GamePort} {
				rule := strconv.Itoa(p) + "/tcp"
				if _, err := sys.Run("ufw", "allow", rule); err != nil {
					return err
				}
				in.m.FirewallRules = append(in.m.FirewallRules, rule)
			}
			return nil
		}, undo: func() error {
			var errs []error
			for _, r := range in.m.FirewallRules {
				if _, err := sys.Run("ufw", "delete", "allow", r); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}}); err != nil {
			return nil, err
		}
	}

	if err := in.exec(step{name: "write install manifest", do: func() error {
		b, _ := json.MarshalIndent(in.m, "", "  ")
		return os.WriteFile(sys.P(cfg.ManifestPath()), append(b, '\n'), 0o600)
	}, undo: func() error { return removeIfExists(sys.P(cfg.ManifestPath())) }}); err != nil {
		return nil, err
	}
	return res, nil
}

func groupExists(sys System, name string) bool {
	b, err := os.ReadFile(sys.P("/etc/group"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, name+":") {
			return true
		}
	}
	return false
}

func installedPackages(sys System) (map[string]bool, error) {
	out, err := sys.Run("dpkg-query", "-W", "-f", "${Package}\\n")
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	return m, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func waitFor(ctx context.Context, d time.Duration, ok func() bool) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("timed out")
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func chownR(sys System, root string, uid, gid int, recursive bool) error {
	if !recursive {
		return sys.Chown(root, uid, gid)
	}
	return filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return sys.Chown(p, uid, gid)
	})
}

// waitHealthy checks the agent socket and the panel's HTTPS endpoint. The
// panel certificate is pinned (loaded from disk), never skipped.
func waitHealthy(ctx context.Context, socket, certPath string, port int) error {
	ac := agentclient.New(socket)
	var lastErr error
	for {
		var h api.Health
		_, err := ac.Do(ctx, "GET", "/v1/health", nil, nil, &h)
		if err == nil {
			lastErr = panelHealth(ctx, certPath, port)
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("services did not become healthy: %v (see: sudo journalctl -u playkeeper-agent -u playkeeper-panel)", lastErr)
		case <-time.After(time.Second):
		}
	}
}

func panelHealth(ctx context.Context, certPath string, port int) error {
	pem, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("cannot parse panel certificate")
	}
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://127.0.0.1:%d/healthz", port), nil)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("panel health returned %d", resp.StatusCode)
	}
	return nil
}
