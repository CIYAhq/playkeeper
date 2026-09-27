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
	"slices"
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
	// ReleaseURL, when set, is where the installed agent looks for updates
	// (get.sh passes the location it downloaded from, if not the default).
	ReleaseURL string
	// Join, when set, is the address of the dashboard this machine joins
	// once installed. It then runs no dashboard of its own.
	Join string
	In   io.Reader
	Out  io.Writer
}

// ports are the ports the install opens: the panel's and the game's, or
// only the game's on a machine that joins another dashboard.
func (o Options) ports() []int {
	if o.Join != "" {
		return []int{o.GamePort}
	}
	return []int{o.PanelPort, o.GamePort}
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
		if !slices.Contains(o.ports(), p.port) {
			continue
		}
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
		add("installed", "Existing Playkeeper", "fail", "Playkeeper is already installed.", "To upgrade it, run the one-line installer (or install.sh from a newer release) again: it upgrades in place and keeps worlds, backups and settings.")
	} else if len(worldDirs(sys, config.DefaultDataDir)) > 0 {
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
	ports := joinAnd(firewallRules(o))
	why := ""
	if o.Join == "" {
		why = " " + port80Why
	}
	if ufwActive(sys) {
		f.UFWActive = true
		add("firewall", "Firewall (ufw)", "info", "ufw is active; the installer will allow "+ports+". If your provider has a cloud firewall, allow them there too."+why, "")
	} else {
		add("firewall", "Firewall", "info", "No active ufw firewall. If your provider has a cloud firewall, allow "+ports+" there."+why, "")
	}
	f.PanelURLHost = primaryIP()
	if o.Join != "" {
		add("join", "Dashboard", "info", "Once installed, this machine joins the dashboard at "+o.Join+". It dials out to it, so no port opens for it here.", "")
	} else {
		add("tls", "HTTPS", "info", "A self-signed certificate will be generated on this server. Your browser will ask you to trust it; compare the fingerprint the installer prints.", "")
	}
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
	runs, second := "runs the web panel", PanelUnit
	if o.Join != "" {
		runs, second = "runs the link to your dashboard", LinkUnit
	}
	p = append(p,
		"Users:     create 'playkeeper' ("+runs+"; no login shell; not in the docker group)",
		"           create 'playkeeper-mc' (owns world files; the Minecraft container runs as this user)",
		"Files:     "+BinPath,
		"           "+ConfigDir+"/config.json",
		"           "+UnitDir+"/"+AgentUnit+" and "+UnitDir+"/"+second,
	)
	if f.ReuseData {
		p = append(p, "Data:      reuse /var/lib/playkeeper (existing worlds and backups are kept as they are)")
	} else {
		p = append(p, "Data:      /var/lib/playkeeper (worlds, backups, settings)")
	}
	p = append(p, "Services:  playkeeper-agent (root; local Unix socket only, no network port)")
	if o.Join != "" {
		p = append(p,
			"           playkeeper-link (dials your dashboard at "+o.Join+"; no port opens for it here)",
			fmt.Sprintf("Ports:     %d/tcp Minecraft once you create a server; no dashboard runs here", o.GamePort),
			"Then:      join your dashboard, after checking that its key matches the fingerprint in the command",
		)
	} else {
		p = append(p,
			fmt.Sprintf("           playkeeper-panel (HTTPS on port %d)", o.PanelPort),
			fmt.Sprintf("Ports:     %d/tcp web panel now; %d/tcp Minecraft once you create a server;", o.PanelPort, o.GamePort),
			"           80/tcp only while Let's Encrypt checks your own domain",
		)
	}
	if !f.DockerPresent {
		p = append(p,
			"Network:   when Docker starts it turns on IP forwarding, sets the iptables FORWARD policy to DROP,",
			"           adds its DOCKER chains and NAT (masquerade) rules, and creates the docker0 bridge;",
			"           uninstall puts these back as they were when it removes Docker",
			fmt.Sprintf("           the server gets its own Docker network, 'playkeeper' (a bridge), and Docker forwards %d/tcp to it", o.GamePort),
		)
	} else {
		p = append(p, fmt.Sprintf("Network:   the server gets its own Docker network, 'playkeeper' (a bridge), and Docker forwards %d/tcp to it", o.GamePort))
	}
	if f.UFWActive {
		p = append(p, "Firewall:  ufw allow "+joinAnd(firewallRules(o))+" (rules that already exist stay yours)")
	}
	p = append(p, "Untouched: your other services, existing Docker containers, SSH, and your own firewall rules")
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
	// NetBeforeDocker is the host network as it was before Playkeeper
	// installed Docker, so removing Docker can put it back.
	NetBeforeDocker *NetSettings `json:"netBeforeDocker,omitempty"`
	// The docker group and Docker's state directories, when installing Docker
	// created them; removing Docker removes them too.
	DockerGroupCreated bool     `json:"dockerGroupCreated,omitempty"`
	DockerDirsCreated  []string `json:"dockerDirsCreated,omitempty"`
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
	// Upgraded is set when an existing install was upgraded in place from
	// FromVersion; UpToDate when it already ran this version.
	Upgraded    bool
	UpToDate    bool
	FromVersion string
	// NoPanel is set on a machine installed to join another dashboard.
	NoPanel bool
}

// Run installs Playkeeper, or upgrades an existing install in place. On any
// failure every completed step is undone.
func Run(ctx context.Context, sys System, o Options, version string) (*Result, error) {
	if _, err := os.Stat(sys.P(ConfigDir + "/config.json")); err == nil {
		return runUpgrade(ctx, sys, o, version)
	}
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
			for _, line := range strings.Split(err.Error(), "\n") {
				problems = append(problems, s.name+": "+line)
			}
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
	cfg.ReleaseURL = in.o.ReleaseURL
	cfg.NoPanel = in.o.Join != ""
	cfg.InstallID = randomHex(16)
	in.m.InstallID = cfg.InstallID
	fmt.Fprintln(in.out, "\nInstalling:")

	if !in.f.DockerPresent {
		var before map[string]bool
		dockerGroupExisted := groupExists(sys, "docker")
		stateDirs := []string{"/var/lib/docker", "/var/lib/containerd", "/etc/docker", "/etc/containerd"}
		var newDirs []string
		for _, d := range stateDirs {
			if _, err := os.Stat(sys.P(d)); errors.Is(err, os.ErrNotExist) {
				newDirs = append(newDirs, d)
			}
		}
		if err := in.exec(step{name: "install Docker (docker.io)", do: func() error {
			var err error
			if before, err = installedPackages(sys); err != nil {
				return err
			}
			net := readNetSettings(sys)
			in.m.NetBeforeDocker = &net
			in.m.DockerGroupCreated, in.m.DockerDirsCreated = !dockerGroupExisted, newDirs
			if _, err := aptGet(sys, in.out, "update"); err != nil {
				return err
			}
			_, err = aptGet(sys, in.out, "install", "-y", "--no-install-recommends", "docker.io")
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
			left, err := purgeDocker(sys, in.out, in.m.PackagesInstalled, in.m.NetBeforeDocker)
			if err == nil {
				err = removeDockerLeftovers(sys, &in.m)
			}
			errs := []error{err}
			for _, l := range left {
				errs = append(errs, errors.New(l))
			}
			return errors.Join(errs...)
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
	// The 'playkeeper' user's directory: the panel's, or the link's on a
	// machine that joins another dashboard.
	own := cfg.PanelDir()
	if cfg.NoPanel {
		own = cfg.LinkDir()
	}
	dirs := []dir{
		{ConfigDir, 0o755, 0, 0},
		{cfg.DataDir, 0o755, 0, 0},
		{cfg.AgentDir(), 0o700, 0, 0},
		{own, 0o700, puid, pgid},
		{filepath.Join(cfg.DataDir, "servers"), 0o755, 0, 0},
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
				if err := chownR(sys, p, d.uid, d.gid, d.path == own); err != nil {
					return err
				}
			}
		}
		// Worlds kept from an earlier install belong to the game user, whose
		// user id may have changed since.
		if sys.IsRoot() {
			for _, w := range worldDirs(sys, cfg.DataDir) {
				if err := chownR(sys, w, guid, ggid, true); err != nil {
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

	res := &Result{NoPanel: cfg.NoPanel}
	if !cfg.NoPanel {
		res.URL, res.ExistingAdm = fmt.Sprintf("https://%s:%d", in.f.PanelURLHost, cfg.PanelPort), in.f.ExistingAdmin
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
	}

	if err := in.exec(step{name: "install and start systemd services", do: func() error {
		units := Units(cfg, false)
		for _, name := range unitNames {
			content, ok := units[name]
			if !ok {
				continue
			}
			if err := os.WriteFile(sys.P(UnitDir+"/"+name), []byte(content), 0o644); err != nil {
				return err
			}
			in.m.FilesCreated = append(in.m.FilesCreated, UnitDir+"/"+name)
			in.m.Units = append(in.m.Units, name)
		}
		if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		start, panelPort := []string{AgentUnit, PanelUnit, UpdatePathUnit}, cfg.PanelPort
		if cfg.NoPanel {
			start, panelPort = []string{AgentUnit, UpdatePathUnit}, 0
		}
		for _, name := range start {
			if _, err := sys.Run("systemctl", "enable", "--now", name); err != nil {
				return err
			}
		}
		hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return sys.WaitHealthy(hctx, cfg.SocketPath, sys.P(filepath.Join(cfg.TLSDir(), "cert.pem")), panelPort)
	}, undo: func() error {
		var errs []error
		for _, name := range []string{UpdatePathUnit, PanelUnit, AgentUnit, UpdateServiceUnit} {
			if _, err := os.Stat(sys.P(UnitDir + "/" + name)); err != nil {
				continue
			}
			// The updater is a oneshot that never ran during an install.
			if name == UpdateServiceUnit {
				if err := removeIfExists(sys.P(UnitDir + "/" + name)); err != nil {
					errs = append(errs, err)
				}
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
		name := "allow the panel, game and Let's Encrypt ports in ufw"
		if cfg.NoPanel {
			name = "allow the game port in ufw"
		}
		if err := in.exec(step{name: name, do: func() error {
			for _, rule := range firewallRules(in.o) {
				added, err := ufwAllow(sys, rule)
				if err != nil {
					return err
				}
				if added {
					in.m.FirewallRules = append(in.m.FirewallRules, rule)
				}
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

// purgeDocker stops Docker's units, puts back the network settings Docker
// changed (while its iptables is still installed), then removes the packages
// Playkeeper installed. Purging while docker.socket is active leaves a dead
// socket unit behind, and a later reinstall's docker.service then fails to
// start. It returns what it could not put back, with how to do it by hand.
func purgeDocker(sys System, out io.Writer, pkgs []string, before *NetSettings) ([]string, error) {
	_, _ = sys.Run("systemctl", "stop", "docker.service", "docker.socket", "containerd.service")
	var nb NetSettings
	if before != nil {
		nb = *before
	}
	left := revertDockerNetwork(sys, nb)
	_, err := aptGet(sys, out, append([]string{"purge", "-y"}, pkgs...)...)
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, _ = sys.Run("systemctl", "reset-failed")
	for _, p := range []string{"/run/docker.sock", "/run/docker", "/run/containerd"} {
		_ = os.RemoveAll(sys.P(p))
	}
	return left, err
}

// removeDockerLeftovers deletes, once Docker is purged, the docker group and
// the state directories that installing Docker created.
func removeDockerLeftovers(sys System, m *Manifest) error {
	var errs []error
	if m.DockerGroupCreated && groupExists(sys, "docker") {
		if _, err := sys.Run("groupdel", "docker"); err != nil {
			errs = append(errs, err)
		}
	}
	for _, d := range m.DockerDirsCreated {
		if err := os.RemoveAll(sys.P(d)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// lockWait bounds how long install and uninstall wait for another package
// manager to finish; on a new server unattended-upgrades often runs first.
const lockWait = 15 * time.Minute

// aptGet runs apt-get once no other package manager holds apt's lock, and
// waits again if another one takes it first.
func aptGet(sys System, out io.Writer, args ...string) (string, error) {
	deadline := sys.Now().Add(lockWait)
	args = append([]string{"-o", "DPkg::Lock::Timeout=60"}, args...)
	for {
		if err := waitForPackageLock(sys, out, deadline); err != nil {
			return "", err
		}
		o, err := sys.Run("apt-get", args...)
		if err == nil || !aptLockError(o, err) || !sys.Now().Before(deadline) {
			return o, err
		}
		sys.Sleep(5 * time.Second)
	}
}

func waitForPackageLock(sys System, out io.Writer, deadline time.Time) error {
	told := false
	for sys.PackageLockHeld() {
		if !sys.Now().Before(deadline) {
			return fmt.Errorf("another package manager has held apt's lock for over %s (on a new server this is usually unattended-upgrades); run this again when `ps -C apt,apt-get,dpkg,unattended-upgr` shows nothing", lockWait)
		}
		if !told {
			fmt.Fprintln(out, "    waiting for another package manager to finish (on a new server this is usually unattended-upgrades)...")
			told = true
		}
		sys.Sleep(5 * time.Second)
	}
	return nil
}

func aptLockError(out string, err error) bool {
	s := out + " " + err.Error()
	return strings.Contains(s, "Could not get lock") || strings.Contains(s, "Unable to acquire the dpkg frontend lock") || strings.Contains(s, "Unable to lock directory")
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

// acmeRule is the firewall rule for Let's Encrypt's checks of an own
// domain: only the agent answers on port 80, and only during a check.
const acmeRule = "80/tcp"

const port80Why = "Port 80 is only for Let's Encrypt's checks of your own domain; nothing answers on it otherwise."

// firewallRules are the ufw rules the installer adds: the panel, the first
// server and Let's Encrypt's checks. A machine that joins another dashboard
// runs no dashboard of its own, so it gets only the first server's.
func firewallRules(o Options) []string {
	var out []string
	add := func(r string) {
		if !contains(out, r) {
			out = append(out, r)
		}
	}
	for _, p := range o.ports() {
		add(strconv.Itoa(p) + "/tcp")
	}
	if o.Join == "" {
		add(acmeRule)
	}
	return out
}

// joinAnd lists items as "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func ufwActive(sys System) bool {
	out, err := sys.Run("ufw", "status")
	return err == nil && strings.Contains(out, "Status: active")
}

// ufwAllow allows rule in ufw and reports whether that added it. A rule
// that was already there is the admin's, so uninstall must leave it.
func ufwAllow(sys System, rule string) (added bool, err error) {
	out, err := sys.Run("ufw", "allow", rule)
	if err != nil {
		return false, err
	}
	return !strings.Contains(out, "Skipping adding existing rule"), nil
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
		os.Remove(tmp)
		return err
	}
	// The mode passed to OpenFile is narrowed by the umask (the updater runs
	// with 0077), and the panel user must be able to run the binary.
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
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
// panel certificate is pinned (loaded from disk), never skipped. Port 0
// means the machine has no panel, so a timeout names only the agent's
// journal.
func waitHealthy(ctx context.Context, socket, certPath string, port int) error {
	ac := agentclient.New(socket)
	journals := "-u playkeeper-agent -u playkeeper-panel"
	if port == 0 {
		journals = "-u playkeeper-agent"
	}
	var lastErr error
	for {
		var h api.Health
		_, err := ac.Do(ctx, "GET", "/v1/health", nil, nil, &h)
		switch {
		case err != nil:
			lastErr = err
		case port == 0:
			return nil
		default:
			lastErr = panelHealth(ctx, certPath, port)
			if lastErr == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("services did not become healthy: %v (see: sudo journalctl %s)", lastErr, journals)
		case <-time.After(time.Second):
		}
	}
}

// waitVersion is waitHealthy for an upgrade: the agent and the panel must
// both report version want, so the old version still running is not taken
// for the new one.
func waitVersion(ctx context.Context, socket, certPath string, port int, want string) error {
	ac := agentclient.New(socket)
	var lastErr error
	for {
		var h api.Health
		_, err := ac.Do(ctx, "GET", "/v1/health", nil, nil, &h)
		switch {
		case err != nil:
			lastErr = err
		case h.Version != want:
			lastErr = fmt.Errorf("the agent reports version %s", h.Version)
		case port == 0:
			return nil
		default:
			lastErr = panelVersion(ctx, certPath, port, want)
			if lastErr == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no healthy answer in time (last error: %v)", lastErr)
		case <-time.After(time.Second):
		}
	}
}

func panelClient(certPath string) (*http.Client, error) {
	pem, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("cannot parse panel certificate")
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}}}, nil
}

// panelVersion reads the version the panel reports on its public health route.
func panelVersion(ctx context.Context, certPath string, port int, want string) error {
	hc, err := panelClient(certPath)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://127.0.0.1:%d/api/health", port), nil)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return fmt.Errorf("panel health returned %d", resp.StatusCode)
	}
	if body.Version != want {
		return fmt.Errorf("the panel reports version %s", body.Version)
	}
	return nil
}

func panelHealth(ctx context.Context, certPath string, port int) error {
	hc, err := panelClient(certPath)
	if err != nil {
		return err
	}
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

// worldDirs are the server data directories under dataDir: the single
// server of 0.1.0 and 0.2.0, and every server made since.
func worldDirs(sys System, dataDir string) []string {
	var out []string
	if st, err := os.Stat(sys.P(filepath.Join(dataDir, "server", "data"))); err == nil && st.IsDir() {
		out = append(out, sys.P(filepath.Join(dataDir, "server", "data")))
	}
	entries, _ := os.ReadDir(sys.P(filepath.Join(dataDir, "servers")))
	for _, e := range entries {
		d := sys.P(filepath.Join(dataDir, "servers", e.Name(), "data"))
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}
