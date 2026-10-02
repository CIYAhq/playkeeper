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
	"net/netip"
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
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/panel"
	"github.com/CIYAhq/playkeeper/internal/platform"
	"github.com/CIYAhq/playkeeper/internal/usage"
	"github.com/CIYAhq/playkeeper/internal/webservers"
	_ "modernc.org/sqlite"
)

const (
	BinPath         = "/usr/local/bin/playkeeper"
	ConfigDir       = "/etc/playkeeper"
	UnitDir         = "/etc/systemd/system"
	AgentUnit       = "playkeeper-agent.service"
	PanelUnit       = "playkeeper-panel.service"
	MinMemoryMB     = 2304
	MinDiskBytes    = 3 << 30
	RecommendedDisk = 5 << 30
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
	// Usage is what the install reports to the stats service, if anything.
	Usage Usage
	In    io.Reader
	Out   io.Writer
}

// kind is what usage stats call the install.
func (o Options) kind() string {
	if o.Join != "" {
		return usage.KindJoined
	}
	return usage.KindDashboard
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
	// Stats are what usage stats call the check when it fails, where they
	// say more than its ID (refusedChecks).
	Stats []string `json:"-"`
}

// Facts are what preflight learned; the plan and steps depend on them.
type Facts struct {
	Checks        []Check
	OS            platform.OS
	DockerPresent bool
	DockerVersion string
	// Docker is where the install gets Docker Engine from, when it isn't
	// present; Firewall is the active ufw or firewalld, if any.
	Docker   *dockerSource
	Firewall hostFirewall
	// SudoLink is set when the install links the binary into SudoLink, so
	// sudo finds it.
	SudoLink      bool
	ReuseData     bool
	ExistingAdmin bool
	PanelURLHost  string
	// Port443 is what uses port 443 or claims it ("" when nothing does), so
	// the dashboard answers there without a port once the machine has an
	// address (config.Dashboard443) only on a new install that found it free.
	Port443 string
	// Provider is the cloud or VPS provider the machine runs at, if it tells.
	Provider Provider
}

// Dashboard443 reports whether the install turns Serve the dashboard on the
// standard HTTPS port on: a new install with a dashboard, with nothing on
// port 443, and no data from an earlier install, whose address other things
// may keep.
func (f Facts) Dashboard443(o Options) bool {
	return o.Join == "" && f.Port443 == "" && !f.ReuseData
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

// panelDirs are where other game panels keep their files, with the panel's
// name.
var panelDirs = []struct{ dir, panel string }{
	{"/var/opt/minecraft/crafty", "Crafty"}, {"/opt/crafty", "Crafty"}, {"/opt/crafty-4", "Crafty"},
	{"/etc/pterodactyl", "Pterodactyl"}, {"/var/lib/pterodactyl", "Pterodactyl"},
	{"/etc/pufferpanel", "PufferPanel"}, {"/var/lib/pufferpanel", "PufferPanel"},
	{"/home/amp/.ampdata", "AMP"}, {"/opt/pelican", "Pelican"},
}

// setup is another Minecraft setup Preflight found on the machine.
type setup struct {
	// what says what it is, in plain words, and whether it runs.
	what    string
	running bool
	// stop stops it and keeps it from starting with the machine, when
	// Playkeeper knows how.
	stop string
	// stat is what usage stats call it, as a check that turned an install
	// away.
	stat string
}

// serviceSetup is the Minecraft service name, as systemd says it stands.
func serviceSetup(sys System, name string) setup {
	active, _ := sys.Run("systemctl", "is-active", name)
	enabled, _ := sys.Run("systemctl", "is-enabled", name)
	s := setup{what: "the service " + name + " (not running)", stat: "existing-service-stopped"}
	switch {
	case slices.Contains([]string{"active", "activating", "reloading"}, strings.TrimSpace(active)):
		s.what, s.running, s.stat = "the service "+name+" (running)", true, "existing-service-running"
	case strings.HasPrefix(strings.TrimSpace(enabled), "enabled"):
		s.what = "the service " + name + " (not running, but it starts with the machine)"
	default:
		return s
	}
	s.stop = "sudo systemctl disable --now " + name
	return s
}

// containerSetup is a Docker container of a Minecraft image.
func containerSetup(c docker.ContainerSummary) setup {
	name := c.ID
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	if c.State == "running" || c.State == "restarting" {
		return setup{what: "the Docker container " + name + " (" + c.Image + ", running)", running: true, stop: "sudo docker stop " + name, stat: "existing-container-running"}
	}
	return setup{what: "the Docker container " + name + " (" + c.Image + ", stopped)", stat: "existing-container-stopped"}
}

// setupsNext is the one thing to do about the setups found: stop what runs,
// if Playkeeper is to take over, or install next to what doesn't run, never
// touching it. A Minecraft server running outside a service or container
// Playkeeper found is one it can't name a way to stop.
func setupsNext(found []setup, o Options) string {
	var stops []string
	running, unexplained := false, false
	for _, s := range found {
		if s.stop != "" {
			stops = append(stops, s.stop)
		}
		running = running || s.running && s.stop != ""
		unexplained = unexplained || s.running && s.stop == ""
	}
	cmd := runAgain(o, stops, "--allow-existing-minecraft")
	switch {
	case unexplained && !running && o.Join != "":
		return "If Playkeeper is to take over, stop that server, then run the join command from your dashboard again."
	case unexplained && !running:
		return "If Playkeeper is to take over, stop that server, then run the install command again."
	case running:
		return "If Playkeeper is to take over, stop it and install next to its files: " + cmd
	}
	return "None of it is running, so Playkeeper can install next to it and never touch it: " + cmd
}

// runAgain is how to run the install again with flag, after the commands
// first: one command to paste, or, on a machine joining a dashboard, whose
// join code the installer doesn't keep, the dashboard's join command.
func runAgain(o Options, first []string, flag string) string {
	if o.Join == "" {
		return strings.Join(append(first, againWith(o, flag)), " && ")
	}
	join := "run the join command from your dashboard again, with " + flag + " at the end"
	if len(first) == 0 {
		return join
	}
	return strings.Join(first, " && ") + ", then " + join
}

// againWith is the install command run again with flag, the way this run
// came and with the flags it had, and usage stats kept off when they were.
func againWith(o Options, flag string) string {
	env := ""
	if o.Usage.Choice == usage.Off {
		env = o.Usage.Why + "=1 "
		if o.Usage.Why == usage.EnvSwitch {
			env = usage.EnvSwitch + "=off "
		}
	}
	args := strings.Join(append(rerunFlags(o), flag), " ")
	switch o.Usage.Source {
	case usage.SourceSite:
		url := "https://playkeeper.io/install"
		if o.Usage.Channel != "" {
			url += "/" + o.Usage.Channel
		}
		return "curl -fsSL " + url + " | sudo " + env + "sh -s -- " + args
	case usage.SourceGitHub:
		return "curl -fsSL https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh | sudo " + env + "sh -s -- " + args
	}
	return "sudo " + env + "./install.sh " + args
}

// rerunFlags are the flags of this run that a command running it again
// keeps: other ports, the release location and --allow-untested-os.
func rerunFlags(o Options) []string {
	var f []string
	if o.PanelPort != 0 && o.PanelPort != config.DefaultPanelPort {
		f = append(f, "--panel-port", strconv.Itoa(o.PanelPort))
	}
	if o.GamePort != 0 && o.GamePort != config.DefaultGamePort {
		f = append(f, "--game-port", strconv.Itoa(o.GamePort))
	}
	if o.ReleaseURL != "" {
		f = append(f, "--release-url", shellQuote(o.ReleaseURL))
	}
	if o.AllowUntestedOS {
		f = append(f, "--allow-untested-os")
	}
	return f
}

// shellQuote quotes s for a shell, unless it needs none.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:=@%+,-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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
	f.OS = platform.ReadOS(sys.P("/etc/os-release"))
	f.Checks = append(f.Checks, osCheck(f.OS, o.AllowUntestedOS))
	fam := familyOf(f.OS)
	if _, err := os.Stat(sys.P("/usr/bin/apt-get")); err == nil && fam == nil {
		fam = debianFamily
	}
	switch arch := sys.Arch(); {
	case archNames[arch] != "" && userland32(sys):
		add("arch", "CPU architecture", "fail", "This is a 32-bit system on a 64-bit CPU. Playkeeper and its Minecraft images need the 64-bit system.",
			"Install the 64-bit version of the operating system (on a Raspberry Pi, a 64-bit image), then run the installer again.")
	case archNames[arch] != "":
		add("arch", "CPU architecture", "pass", archNames[arch]+".", "")
	case o.AllowUntestedOS:
		add("arch", "CPU architecture", "warn", arch+" is not tested.", "")
	default:
		add("arch", "CPU architecture", "fail", arch+" is not supported; Playkeeper runs on x86_64 (amd64) and 64-bit ARM (arm64) servers.",
			"Use an x86_64 or 64-bit ARM server, or pass --allow-untested-os (unsupported).")
	}
	if st, err := os.Stat(sys.P("/run/systemd/system")); err == nil && st.IsDir() {
		add("systemd", "Service manager", "pass", "systemd is running.", "")
	} else {
		add("systemd", "Service manager", "fail", "systemd is not running; Playkeeper's services need it.", "Use a server or virtual machine that runs systemd, such as a standard Ubuntu, Debian or AlmaLinux server, not a container.")
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
		clean := ""
		if fam != nil {
			clean = fam.pm.cleanHint() + "; "
		}
		add("disk", "Disk space", "fail", fmt.Sprintf("Only %.1f GB free under /var/lib; at least 3 GB is needed.", gb(free)), "Free disk space (for example: "+clean+"sudo journalctl --vacuum-size=200M) or use a larger disk.")
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
	if o.Join == "" {
		f.Port443 = port443Taken(sys)
	}
	var found []setup
	// /lib is /usr/lib on the RHEL family and on newer Ubuntu and Debian.
	units := map[string]bool{}
	for _, dir := range []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system"} {
		entries, _ := os.ReadDir(sys.P(dir))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "playkeeper-") || units[e.Name()] {
				continue
			}
			if strings.HasSuffix(e.Name(), ".service") && unitPattern.MatchString(e.Name()) {
				units[e.Name()] = true
				found = append(found, serviceSetup(sys, e.Name()))
			}
		}
	}
	for _, p := range panelDirs {
		if _, err := os.Stat(sys.P(p.dir)); err == nil {
			found = append(found, setup{what: p.panel + "'s files in " + p.dir, stat: "existing-panel"})
		}
	}
	for _, p := range sys.Processes() {
		if javaMC.MatchString(p) {
			found = append(found, setup{what: "a Minecraft server running: " + truncate(p, 80), running: true, stat: "existing-java"})
		}
	}
	di, derr := sys.Docker(ctx)
	podman := derr == nil && di.Podman
	if podman {
		derr = errors.New("the Docker socket is Podman's")
	}
	if derr == nil {
		f.DockerPresent, f.DockerVersion = true, di.Version
		for _, c := range di.Containers {
			if c.Labels["io.playkeeper.managed"] == "true" {
				continue
			}
			if strings.Contains(strings.ToLower(c.Image), "minecraft") {
				found = append(found, containerSetup(c))
			}
			for _, p := range c.Ports {
				if p.PublicPort == 443 && p.Type == "tcp" && f.Port443 == "" && o.Join == "" {
					f.Port443 = "the Docker container " + strings.TrimPrefix(strings.Join(c.Names, ","), "/")
				}
			}
		}
	}
	slices.SortFunc(found, func(a, b setup) int { return strings.Compare(a.what, b.what) })
	var whats, stats []string
	for _, s := range found {
		whats = append(whats, s.what)
		if !slices.Contains(stats, s.stat) {
			stats = append(stats, s.stat)
		}
	}
	slices.Sort(stats)
	switch {
	case len(found) == 0:
		add("existing", "Existing Minecraft setups", "pass", "None found.", "")
	case o.AllowExistingMinecraft:
		add("existing", "Existing Minecraft setups", "warn", "Found "+strings.Join(whats, "; ")+". Playkeeper won't touch it (--allow-existing-minecraft).", "")
	default:
		add("existing", "Existing Minecraft setups", "fail", "Found "+strings.Join(whats, "; ")+". Playkeeper installs next to another Minecraft setup only when you say so.", setupsNext(found, o))
		f.Checks[len(f.Checks)-1].Stats = stats
	}
	if _, err := os.Stat(sys.P(ConfigDir + "/config.json")); err == nil {
		add("installed", "Existing Playkeeper", "fail", "Playkeeper is already installed.", "To upgrade it, run the one-line installer (or install.sh from a newer release) again: it upgrades in place and keeps worlds, backups and settings.")
	} else if len(worldDirs(sys, config.DefaultDataDir)) > 0 {
		f.ReuseData = true
		f.ExistingAdmin = hasAdmin(sys.P(filepath.Join(config.DefaultDataDir, "panel", "panel.db")))
		add("reuse", "Previous Playkeeper data", "info", "Found worlds and backups from an earlier Playkeeper install in /var/lib/playkeeper; they will be reused, not changed.", "")
	}
	switch {
	case podman:
		how := ""
		if fam != nil {
			how = " (" + fam.pm.uninstallHint("podman-docker") + ")"
		}
		add("docker", "Docker", "fail", "The Docker socket is Podman's ("+nonEmpty(di.Version, "Podman")+", through podman-docker), not Docker Engine's.",
			"Playkeeper needs Docker Engine. Turn off Podman's Docker socket (sudo systemctl disable --now podman.socket) and remove podman-docker"+how+"; Podman and its containers keep working. Then run the installer again.")
	case derr == nil:
		add("docker", "Docker", "pass", "Docker "+di.Version+" is running; Playkeeper only manages its own container.", "")
	case fam != nil && fam.docker(f.OS) != nil:
		f.Docker = fam.docker(f.OS).forHost(sys)
		var blockers []blocker
		if len(f.Docker.blockers) > 0 {
			installed, err := f.Docker.pm.installed(sys)
			if err != nil {
				add("docker", "Docker", "fail", "Docker is not installed, and the installed packages can't be listed: "+err.Error(), "Install Docker Engine (https://docs.docker.com/engine/install/), then run the installer again.")
				break
			}
			blockers = dockerBlockers(f.Docker, installed)
		}
		for _, b := range blockers {
			add("docker", "Docker", "fail", "Docker is not installed. "+b.why, b.fix)
		}
		if len(blockers) == 0 {
			add("docker", "Docker", "info", "Docker is not installed. The installer will install "+f.Docker.what+".", "")
		}
	default:
		add("docker", "Docker", "fail", "Docker is not installed, and Playkeeper doesn't install it on "+f.OS.Display()+".", "Install Docker Engine (https://docs.docker.com/engine/install/), then run the installer again.")
	}
	if mode := selinuxMode(sys); mode != "" {
		add("selinux", "SELinux", "pass", "SELinux is "+mode+"; Playkeeper works with it as it is.", "")
	}
	if o.Join == "" {
		switch {
		case f.ReuseData:
		case f.Port443 == "":
			add("port-443", "Port 443", "info", "Free: once this machine has an address, the dashboard answers there without :"+strconv.Itoa(o.PanelPort)+". Machine settings turns that off.", "")
		default:
			add("port-443", "Port 443", "info", "Used by "+f.Port443+", so the dashboard stays on port "+strconv.Itoa(o.PanelPort)+". Playkeeper leaves port 443 alone.", "")
		}
	}
	if _, err := os.Lstat(sys.P(SudoLink)); errors.Is(err, os.ErrNotExist) {
		f.SudoLink = sudoMissesBin(sys)
	}
	ports := joinAnd(firewallRules(o))
	// A machine with a dashboard says where its provider's firewall steps
	// are right under the setup link, where they're needed (Result.Provider);
	// a joined machine has no link, so its line says it here.
	cloud := ""
	if o.Join != "" {
		cloud = " If your provider has a cloud firewall, allow " + ports + " there too."
	}
	if fw := activeFirewall(sys); fw != nil {
		f.Firewall = fw
		add("firewall", "Firewall ("+fw.short()+")", "info", fw.short()+" is active; the installer will allow "+ports+fw.where()+"."+cloud, "")
	} else if name, allow := dropFirewall(sys, firewallRules(o)); name != "" {
		add("firewall", "Firewall ("+name+")", "warn", name+" drops incoming connections that no rule allows, and the installer opens ports only in ufw and firewalld. Allow "+ports+" in "+name+" unless a rule already does."+cloud,
			"For example: "+allow)
	} else {
		add("firewall", "Firewall", "info", "Found no firewall on this server that blocks incoming connections."+cloud, "")
	}
	f.PanelURLHost = primaryIP()
	f.Provider = DetectProvider(sys)
	if o.Join != "" {
		add("join", "Dashboard", "info", "Once installed, this machine joins the dashboard at "+o.Join+". It dials out to it, so no port opens for it here.", "")
	} else {
		add("tls", "HTTPS", "info", "The dashboard starts with a certificate of its own, so your browser warns that the connection isn't private: click Advanced, then Proceed. A free name in Machine settings ends the warning.", "")
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
	if !f.DockerPresent && f.Docker != nil {
		p = append(p, "Packages:  install "+f.Docker.plan)
		if r := f.Docker.repo; r != nil {
			p = append(p, "           after adding that repository ("+r.file+") and Docker's signing key,",
				"           which Playkeeper carries ("+r.keyFile+"); both go with Docker")
		}
	}
	runs, second := "runs the web panel", PanelUnit
	if o.Join != "" {
		runs, second = "runs the link to your dashboard", LinkUnit
	}
	p = append(p,
		"Users:     create 'playkeeper' ("+runs+"; no login shell; not in the docker group)",
		"           create 'playkeeper-mc' (owns world files; the Minecraft container runs as this user)",
		"Files:     "+BinPath,
	)
	if f.SudoLink {
		p = append(p, "           "+SudoLink+", a link to it, since sudo here leaves "+filepath.Dir(BinPath)+" out of its path")
	}
	p = append(p,
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
		panel := fmt.Sprintf("           playkeeper-panel (HTTPS on port %d)", o.PanelPort)
		if f.Dashboard443(o) {
			panel = fmt.Sprintf("           playkeeper-panel (HTTPS on port %d, and on 443 once the machine has an address)", o.PanelPort)
		}
		p = append(p,
			panel,
			fmt.Sprintf("Ports:     %d/tcp web panel now; %d/tcp Minecraft once you create a server;", o.PanelPort, o.GamePort),
			"           443/tcp and 80/tcp once the machine has an address, for the dashboard without a port and the public server page;",
			"           80/tcp also while Let's Encrypt checks your own domain",
		)
	}
	if !f.DockerPresent {
		p = append(p,
			"Network:   when Docker starts it turns on IP forwarding, sets the iptables FORWARD policy to DROP,",
			"           adds its DOCKER chains and NAT (masquerade) rules, and creates the docker0 bridge;",
		)
		if _, ok := f.Firewall.(firewalld); ok {
			p = append(p, "           it also adds a 'docker' zone and a 'docker-forwarding' policy to firewalld;")
		}
		p = append(p,
			"           uninstall puts these back as they were when it removes Docker",
			fmt.Sprintf("           the server gets its own Docker network, 'playkeeper' (a bridge), and Docker forwards %d/tcp to it", o.GamePort),
		)
	} else {
		p = append(p, fmt.Sprintf("Network:   the server gets its own Docker network, 'playkeeper' (a bridge), and Docker forwards %d/tcp to it", o.GamePort))
	}
	p = append(p, "           with iptables rules that keep servers from this machine and the cloud's metadata service")
	if f.Firewall != nil {
		p = append(p, f.Firewall.planLine(joinAnd(firewallRules(o))))
	}
	p = append(p, "Untouched: your other services, existing Docker containers, SSH, and your own firewall rules")
	return p
}

// publicHost is the address for the dashboard's link: host, the machine's
// own, or, when that's a private one behind the provider's NAT, as on AWS,
// Google Cloud, Azure and Oracle Cloud, the public one the names service
// sees, so the link opens from home. A test install asks nothing.
func publicHost(ctx context.Context, sys System, o Options, host string) string {
	if !privateAddr(host) || sys.PublicIPv4 == nil || o.Usage.Test || testInstall(sys) {
		return host
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ip, err := sys.PublicIPv4(lctx)
	if a, perr := netip.ParseAddr(ip); err != nil || perr != nil || !a.Is4() || privateAddr(ip) {
		return host
	}
	return ip
}

// privateAddr reports whether host is an IPv4 address that only its own
// network reaches: a private one (RFC 1918), a shared one (100.64.0.0/10)
// or a link-local one.
func privateAddr(host string) bool {
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || sharedAddrs.Contains(ip))
}

var sharedAddrs = netip.MustParsePrefix("100.64.0.0/10")

// Short is the plan in a few plain lines. It goes right above the question,
// the last thing on the screen, where people look; the plan's details are
// above it for whoever reads them.
func Short(f Facts, o Options) []string {
	what := "Playkeeper"
	if !f.DockerPresent {
		what = "Docker and Playkeeper"
	}
	lines := []string{"In short:", "  • installs " + what}
	if o.Join != "" {
		lines = append(lines, fmt.Sprintf("  • joins your dashboard at %s; Minecraft servers here use port %d", o.Join, o.GamePort))
	} else {
		lines = append(lines, fmt.Sprintf("  • your dashboard on port %d, and your first Minecraft server on %d", o.PanelPort, o.GamePort))
	}
	if f.Firewall != nil {
		var ports []string
		for _, r := range firewallRules(o) {
			ports = append(ports, strings.TrimSuffix(r, "/tcp"))
		}
		lines = append(lines, "  • opens ports "+joinAnd(ports)+" in "+f.Firewall.short())
	}
	if f.ReuseData {
		lines = append(lines, "  • keeps the worlds and backups already here")
	}
	return append(lines, "  • nothing else changes; undo it any time: sudo playkeeper uninstall (keeps your worlds and backups)")
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
	// Firewall is the firewall FirewallRules are in: "firewalld", in
	// FirewallZone, "iptables" (rejectAll), or "" for ufw.
	// FirewallZoneFiles are the zone's files firewalld didn't have before
	// the install changed it, and FirewallZoneBefore its saved settings then.
	Firewall           string   `json:"firewall,omitempty"`
	FirewallZone       string   `json:"firewallZone,omitempty"`
	FirewallZoneFiles  []string `json:"firewallZoneFiles,omitempty"`
	FirewallZoneBefore string   `json:"firewallZoneBefore,omitempty"`
	// FirewallFamilies are the iptables commands whose rules FirewallRules
	// are in, for Firewall "iptables": "iptables" and maybe "ip6tables".
	FirewallFamilies []string `json:"firewallFamilies,omitempty"`
	ReusedData       bool     `json:"reusedData"`
	KeptOnUninstall  []string `json:"keptOnUninstall"`
	// PackageManager installed PackagesInstalled: "dnf", or "" for apt.
	PackageManager string `json:"packageManager,omitempty"`
	// NetBeforeDocker is the host network as it was before Playkeeper
	// installed Docker, so removing Docker can put it back.
	NetBeforeDocker *NetSettings `json:"netBeforeDocker,omitempty"`
	// The docker group and Docker's state directories, when installing Docker
	// created them; removing Docker removes them too.
	DockerGroupCreated bool     `json:"dockerGroupCreated,omitempty"`
	DockerDirsCreated  []string `json:"dockerDirsCreated,omitempty"`
	// DockerRepoFiles are the repository and signing key added to install
	// Docker, and DockerFirewalld the zone and policy Docker added to
	// firewalld; they go with Docker.
	DockerRepoFiles []string `json:"dockerRepoFiles,omitempty"`
	DockerFirewalld []string `json:"dockerFirewalld,omitempty"`
}

type step struct {
	name string
	// code names the step in usage stats, when the install fails there.
	code string
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
	// rep sends the install's usage reports (nil when they're off), and
	// failed is the code of the step the install failed at.
	rep    *reporter
	failed string
}

// Result is what a successful install prints for the user.
type Result struct {
	URL string
	// PrivateHost says URL's address is a private one: the machine's own
	// behind a provider's NAT, when its public one wasn't found.
	PrivateHost bool

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
	// UsageOn says usage stats are on, for the summary's last word.
	UsageOn bool
	// Dashboard443 says the dashboard answers on port 443 once the machine
	// has an address (config.Dashboard443).
	Dashboard443 bool
	// PanelPort and GamePort are the ports a provider's firewall must let
	// through, and Provider whose firewall it is, for the line under the
	// setup link.
	PanelPort, GamePort int
	Provider            Provider
}

// Run installs Playkeeper, or upgrades an existing install in place. On any
// failure every completed step is undone.
func Run(ctx context.Context, sys System, o Options, version string) (*Result, error) {
	if _, err := os.Stat(sys.P(ConfigDir + "/config.json")); err == nil {
		return runUpgrade(ctx, sys, o, version)
	}
	start := sys.Now()
	out := o.Out
	fmt.Fprintf(out, "Playkeeper %s installer\n\n", version)
	for _, line := range o.Usage.notice(o.Usage.state("")) {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "\nChecking this server (nothing is changed yet):\n")
	f := Preflight(ctx, sys, o)
	PrintChecks(out, f)
	rep := newReporter(o.Usage, sys, f.OS, version, o.kind())
	if !f.OK() {
		rep.send(ctx, usage.EventRefused, refusedChecks(f))
		rep.wait()
		return nil, errors.New("preflight failed; fix the items marked FAIL above. Nothing was changed")
	}
	fmt.Fprintf(out, "\nPlaykeeper will make these changes:\n")
	for _, line := range Plan(f, o) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	fmt.Fprintln(out)
	for _, line := range Short(f, o) {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)
	if !o.Yes {
		fmt.Fprint(out, "Proceed? [y/N] ")
		ans, _ := bufio.NewReader(o.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return nil, errDeclined
		}
	}
	in := &installer{sys: sys, o: o, f: f, out: out, rep: rep}
	in.m = Manifest{Version: version, InstalledAt: start.UTC(), PanelPort: o.PanelPort, GamePort: o.GamePort, ReusedData: f.ReuseData,
		KeptOnUninstall: []string{config.DefaultDataDir + "/server (worlds)", config.DefaultDataDir + "/backups", config.DefaultDataDir + " (settings, admin account, analytics)"}}
	rep.send(ctx, usage.EventStarted, "")
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
		rep.send(ctx, usage.EventFailed, nonEmpty(in.failed, "other"))
		rep.wait()
		return nil, err
	}
	rep.send(ctx, usage.EventSucceeded, "")
	rep.wait()
	res.Duration = sys.Now().Sub(start)
	res.UsageOn = o.Usage.On()
	return res, nil
}

func (in *installer) exec(s step) error {
	fmt.Fprintf(in.out, "  • %s\n", s.name)
	err := s.do()
	if s.undo != nil {
		in.done = append(in.done, s)
	}
	if err == nil && os.Getenv(FailStepEnv) == s.name {
		err = fmt.Errorf("injected failure (%s)", FailStepEnv)
	}
	if err != nil {
		in.failed = s.code
		return fmt.Errorf("%s: %w", s.name, err)
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
	if in.f.Dashboard443(in.o) {
		cfg.Dashboard443 = "on"
	}
	cfg.InstallID = randomHex(16)
	in.m.InstallID = cfg.InstallID
	// An install with usage stats off gets no usage ID: the agent makes one
	// if they are ever turned on.
	u := in.o.Usage
	cfg.UsageSource, cfg.UsageChannel, cfg.UsageStats, cfg.StatsURL = u.Source, u.Channel, u.setting(), u.URL
	cfg.UsageTest = u.Test || testInstall(sys)
	if in.rep != nil {
		cfg.UsageID = in.rep.id
	}
	fmt.Fprintln(in.out, "\nInstalling:")

	if !in.f.DockerPresent {
		src := in.f.Docker
		if src == nil {
			in.failed = "docker"
			return nil, errors.New("Docker is not installed, and Playkeeper doesn't install it on " + in.f.OS.Display())
		}
		var before map[string]bool
		dockerGroupExisted := groupExists(sys, "docker")
		stateDirs := []string{"/var/lib/docker", "/var/lib/containerd", "/etc/docker", "/etc/containerd"}
		var newDirs []string
		for _, d := range stateDirs {
			if _, err := os.Stat(sys.P(d)); errors.Is(err, os.ErrNotExist) {
				newDirs = append(newDirs, d)
			}
		}
		if err := in.exec(step{name: "install Docker (" + strings.Join(src.pkgs, ", ") + ")", code: "docker", do: func() error {
			var err error
			if before, err = src.pm.installed(sys); err != nil {
				return err
			}
			net := readNetSettings(sys)
			in.m.NetBeforeDocker = &net
			in.m.DockerGroupCreated, in.m.DockerDirsCreated = !dockerGroupExisted, newDirs
			in.m.PackageManager = src.pm.id()
			// Docker adds its zone and policy to a firewalld that runs, whichever
			// firewall the ports go in.
			var zonesBefore map[string]bool
			if firewalldActive(sys) {
				zonesBefore = firewalldHas(sys)
			}
			if src.repo != nil {
				if err := in.addRepo(src.repo); err != nil {
					return err
				}
			}
			err = src.pm.install(ctx, sys, in.out, src.packages(), src.optional)
			after, perr := src.pm.installed(sys)
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
			if _, err = sys.Run("systemctl", "enable", "--now", "docker.service"); err == nil {
				err = waitFor(ctx, 60*time.Second, func() bool { _, err := sys.Docker(ctx); return err == nil })
			}
			// A Docker that fails to start may have added them first.
			if zonesBefore != nil {
				now := firewalldHas(sys)
				for _, o := range dockerFirewalld {
					if now[o] && !zonesBefore[o] {
						in.m.DockerFirewalld = append(in.m.DockerFirewalld, o)
					}
				}
			}
			return err
		}, undo: func() error {
			if len(in.m.PackagesInstalled) == 0 {
				return removeFiles(sys, in.m.DockerRepoFiles)
			}
			left, err := purgeDocker(sys, in.out, &in.m)
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
		if err := in.exec(step{name: "create user " + u.name, code: "users", do: func() error {
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
	if err := in.exec(step{name: "create directories", code: "directories", do: func() error {
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

	if err := in.exec(step{name: "install " + BinPath, code: "binary", do: func() error {
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

	if in.f.SudoLink {
		if err := in.exec(step{name: "link " + SudoLink + " to it, for sudo", code: "sudo-link", do: func() error {
			if err := os.Symlink(BinPath, sys.P(SudoLink)); err != nil {
				return err
			}
			in.m.FilesCreated = append(in.m.FilesCreated, SudoLink)
			return nil
		}, undo: func() error {
			if !contains(in.m.FilesCreated, SudoLink) {
				return nil
			}
			return removeIfExists(sys.P(SudoLink))
		}}); err != nil {
			return nil, err
		}
	}

	if err := in.exec(step{name: "write " + ConfigDir + "/config.json", code: "config", do: func() error {
		if err := cfg.Save(sys.P(ConfigDir + "/config.json")); err != nil {
			return err
		}
		in.m.FilesCreated = append(in.m.FilesCreated, ConfigDir+"/config.json")
		return nil
	}, undo: func() error { return removeIfExists(sys.P(ConfigDir + "/config.json")) }}); err != nil {
		return nil, err
	}

	res := &Result{NoPanel: cfg.NoPanel, Dashboard443: cfg.Dashboard443 == "on", PanelPort: cfg.PanelPort, GamePort: cfg.GamePort, Provider: in.f.Provider}
	if !cfg.NoPanel {
		host := publicHost(ctx, sys, in.o, in.f.PanelURLHost)
		res.URL, res.ExistingAdm, res.PrivateHost = fmt.Sprintf("https://%s:%d", host, cfg.PanelPort), in.f.ExistingAdmin, privateAddr(host)
		if err := in.exec(step{name: "generate HTTPS certificate and first-run setup code", code: "certificate", do: func() error {
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

	if err := in.exec(step{name: "install and start systemd services", code: "services", do: func() error {
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

	if fw := in.f.Firewall; fw != nil {
		name := "allow the panel, game and Let's Encrypt ports in " + fw.short()
		if cfg.NoPanel {
			name = "allow the game port in " + fw.short()
		}
		fw.record(sys, &in.m)
		if err := in.exec(step{name: name, code: "firewall", do: func() error {
			for _, rule := range firewallRules(in.o) {
				added, err := fw.allow(sys, rule)
				if added {
					in.m.FirewallRules = append(in.m.FirewallRules, rule)
				}
				if err != nil {
					return err
				}
			}
			return nil
		}, undo: func() error {
			var errs []error
			for _, r := range in.m.FirewallRules {
				errs = append(errs, fw.remove(sys, r))
			}
			return errors.Join(append(errs, fw.tidy(sys, in.m))...)
		}}); err != nil {
			return nil, err
		}
	}

	if err := in.exec(step{name: "write install manifest", code: "manifest", do: func() error {
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
func purgeDocker(sys System, out io.Writer, m *Manifest) ([]string, error) {
	_, _ = sys.Run("systemctl", "stop", "docker.service", "docker.socket", "containerd.service")
	var nb NetSettings
	if m.NetBeforeDocker != nil {
		nb = *m.NetBeforeDocker
	}
	left := revertDockerNetwork(sys, nb)
	err := packageManagerOf(*m).remove(sys, out, m.PackagesInstalled)
	_, _ = sys.Run("systemctl", "daemon-reload")
	_, _ = sys.Run("systemctl", "reset-failed")
	for _, p := range []string{"/run/docker.sock", "/run/docker", "/run/containerd"} {
		_ = os.RemoveAll(sys.P(p))
	}
	return left, err
}

// removeDockerLeftovers deletes, once Docker is purged, the docker group and
// the state directories that installing Docker created, the repository it
// came from, and what it added to firewalld.
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
	errs = append(errs, removeFiles(sys, m.DockerRepoFiles), removeFirewalld(sys, m.DockerFirewalld))
	return errors.Join(errs...)
}

// addRepo adds the package repository Docker is installed from, with the key
// its packages are signed with, and records both for uninstall first.
func (in *installer) addRepo(r *rpmRepo) error {
	for _, f := range []struct {
		path    string
		content []byte
	}{{r.keyFile, r.key}, {r.file, []byte(r.content())}} {
		in.m.DockerRepoFiles = append(in.m.DockerRepoFiles, f.path)
		p := in.sys.P(f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(p, f.content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func removeFiles(sys System, paths []string) error {
	var errs []error
	for _, p := range paths {
		errs = append(errs, removeIfExists(sys.P(p)))
	}
	return errors.Join(errs...)
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

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// port443Taken names what uses port 443 or claims it, as the agent's
// hand-over would find it: a program listening on it, or a web server set to
// start with the machine; Preflight finds a Docker container that publishes
// it with the others. "" when nothing does.
func port443Taken(sys System) string {
	if sys.Listening(443) {
		return "another program"
	}
	if unit := webservers.FirstEnabled(sys.P(UnitDir)); unit != "" {
		return unit + ", set to start with the machine,"
	}
	return ""
}

// webPortsWhy says what ports 443 and 80 are for, for the firewall's line.
const webPortsWhy = "Port 443 carries the dashboard without a port and the public server page once the machine has an address; port 80 sends browsers there and answers Let's Encrypt's checks of your own domain."

// httpsRule is the firewall rule for the dashboard without a port and the
// public server page.
const httpsRule = "443/tcp"

// acmeRule is the firewall rule for Let's Encrypt's checks of an own
// domain: only the agent answers on port 80, and only during a check.
const acmeRule = "80/tcp"

const port80Why = "Port 80 is only for Let's Encrypt's checks of your own domain; nothing answers on it otherwise."

// firewallRules are the rules the installer adds to the machine's firewall:
// the panel, the first server, the dashboard without a port with the public
// server page, and Let's Encrypt's checks. A machine that joins another
// dashboard runs no dashboard of its own, so it gets only the first
// server's.
func firewallRules(o Options) []string {
	return portRules(o.ports(), o.Join == "")
}

// installRules are the rules firewallRules gave the install that wrote cfg.
func installRules(cfg config.Config) []string {
	if cfg.NoPanel {
		return portRules([]int{cfg.GamePort}, false)
	}
	return portRules([]int{cfg.PanelPort, cfg.GamePort}, true)
}

// portRules are the rules for ports, and on a machine with a dashboard,
// port 443's and port 80's.
func portRules(ports []int, dashboard bool) []string {
	var out []string
	add := func(r string) {
		if !contains(out, r) {
			out = append(out, r)
		}
	}
	for _, p := range ports {
		add(strconv.Itoa(p) + "/tcp")
	}
	if dashboard {
		add(httpsRule)
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

// panelNeedsSetup reads, on the panel's public setup route, whether it still
// waits for its first-run setup.
func panelNeedsSetup(ctx context.Context, certPath string, port int) (bool, error) {
	hc, err := panelClient(certPath)
	if err != nil {
		return false, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://127.0.0.1:%d/api/setup/status", port), nil)
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var body struct {
		NeedsSetup *bool `json:"needsSetup"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil || body.NeedsSetup == nil {
		return false, fmt.Errorf("the panel's setup status returned %d", resp.StatusCode)
	}
	return *body.NeedsSetup, nil
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
