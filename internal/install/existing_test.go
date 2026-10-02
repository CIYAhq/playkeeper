package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/usage"
)

const (
	thenInstall = "curl -fsSL https://playkeeper.io/install | sudo sh"
	refuses     = " Playkeeper installs next to another Minecraft setup only when none of it runs or starts again by itself."
	leaves      = " None of it runs or starts again by itself, so Playkeeper installs next to it and leaves it alone."
	paper       = "java -Xmx2G -jar paper-1.21.11.jar nogui"
)

func unitFile(h *fakeHost) {
	os.WriteFile(filepath.Join(h.root, "/etc/systemd/system/minecraft.service"), []byte("[Service]\nExecStart=/usr/bin/java -jar server.jar\n"), 0o644)
}

func container(h *fakeHost, state, restart string) {
	h.dockerPresent = true
	h.containers = append(h.containers, docker.ContainerSummary{ID: "c0ffee", Names: []string{"/mc"}, Image: "itzg/minecraft-server", State: state})
	if restart != "-" {
		h.restart = map[string]string{"c0ffee": restart}
	}
}

func pterodactyl(h *fakeHost) {
	os.MkdirAll(filepath.Join(h.root, "/etc/pterodactyl"), 0o755)
	os.MkdirAll(filepath.Join(h.root, "/var/lib/pterodactyl"), 0o755)
}

// Another Minecraft setup turned away most install runs, mostly old ones
// that weren't running. The installer now goes next to what can't get in
// its way, which it never touches and names in one line, and refuses what
// runs or starts again by itself with the one thing to do: the command to
// paste, the way the install command came. The stats say which kind of
// setup refused it.
func TestTheInstallerGoesNextToOldSetupsAndRefusesOnesThatRun(t *testing.T) {
	site := Usage{Source: usage.SourceSite}
	for _, c := range []struct {
		name        string
		setup       func(h *fakeHost)
		usage       Usage
		opts        func(o *Options)
		status      string
		detail, fix string
		stats, left []string
	}{
		{name: "other ports and a release location", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "enabled"} }, usage: site, status: "fail",
			opts: func(o *Options) {
				o.PanelPort, o.GamePort, o.ReleaseURL = 9443, 25566, "https://example.com/releases?channel=beta&x=1"
			},
			detail: "Found the service minecraft.service (not running, but it starts with the machine)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo systemctl disable minecraft.service && curl -fsSL https://playkeeper.io/install | sudo sh -s -- --panel-port 9443 --game-port 25566 --release-url 'https://example.com/releases?channel=beta&x=1'",
			stats:  []string{"existing-service-enabled"}},
		{name: "a machine joining a dashboard", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "running"} }, usage: site, status: "fail",
			opts:   func(o *Options) { o.Join = "203.0.113.5:8443" },
			detail: "Found the service minecraft.service (running)." + refuses,
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo systemctl disable --now minecraft.service, then run the join command from your dashboard again",
			stats:  []string{"existing-service-running"}},
		{name: "a machine joining a dashboard, next to a server running on its own", setup: func(h *fakeHost) { h.procs = []string{paper} }, usage: site, status: "fail",
			opts:   func(o *Options) { o.Join = "203.0.113.5:8443" },
			detail: "Found a Minecraft server running: " + paper + "." + refuses,
			fix:    "If Playkeeper is to take over, stop that Minecraft server, then run the join command from your dashboard again.",
			stats:  []string{"existing-java"}},
		{name: "a service neither running nor enabled", setup: unitFile, usage: site, status: "info",
			detail: "Found the service minecraft.service (not running)." + leaves,
			left:   []string{"Left your old Minecraft service (minecraft.service) alone; it isn't running."}},
		{name: "a stopped container without a restart policy", setup: func(h *fakeHost) { container(h, "exited", "no") }, usage: site, status: "info",
			detail: "Found the Docker container mc (itzg/minecraft-server, stopped)." + leaves,
			left:   []string{"Left your old Minecraft container (mc) alone; it's stopped and won't start by itself."}},
		{name: "a stopped unless-stopped container", setup: func(h *fakeHost) { container(h, "exited", "unless-stopped") }, usage: site, status: "info",
			detail: "Found the Docker container mc (itzg/minecraft-server, stopped)." + leaves,
			left:   []string{"Left your old Minecraft container (mc) alone; it's stopped and won't start by itself."}},
		{name: "a panel's folders, the panel not running", setup: pterodactyl, usage: site, status: "info",
			detail: "Found Pterodactyl's files in /etc/pterodactyl and /var/lib/pterodactyl (Pterodactyl isn't running)." + leaves,
			left:   []string{"Left Pterodactyl's old files (/etc/pterodactyl and /var/lib/pterodactyl) alone; Pterodactyl isn't running."}},

		{name: "a running service", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "running"} }, usage: site, status: "fail",
			detail: "Found the service minecraft.service (running)." + refuses,
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo systemctl disable --now minecraft.service && " + thenInstall,
			stats:  []string{"existing-service-running"}},
		{name: "a stopped service that starts with the machine", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "enabled"} }, usage: site, status: "fail",
			detail: "Found the service minecraft.service (not running, but it starts with the machine)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo systemctl disable minecraft.service && " + thenInstall,
			stats:  []string{"existing-service-enabled"}},
		{name: "a stopped container Docker starts again", setup: func(h *fakeHost) { container(h, "exited", "always") }, usage: site, status: "fail",
			detail: "Found the Docker container mc (itzg/minecraft-server, stopped, but Docker starts it again)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo docker update --restart=no mc && " + thenInstall,
			stats:  []string{"existing-container-restarts"}},
		{name: "a stopped container whose restart policy can't be read", setup: func(h *fakeHost) { container(h, "exited", "-") }, usage: site, status: "fail",
			detail: "Found the Docker container mc (itzg/minecraft-server, stopped, but Docker starts it again)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo docker update --restart=no mc && " + thenInstall,
			stats:  []string{"existing-container-restarts"}},
		{name: "a running container and its server", setup: func(h *fakeHost) { container(h, "running", "unless-stopped"); h.procs = []string{paper} }, usage: site, status: "fail",
			detail: "Found a Minecraft server running: " + paper + "; the Docker container mc (itzg/minecraft-server, running)." + refuses,
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo docker stop mc && " + thenInstall,
			stats:  []string{"existing-container-running", "existing-java"}},
		{name: "a running container Docker would start again", setup: func(h *fakeHost) { container(h, "running", "always") }, usage: site, status: "fail",
			detail: "Found the Docker container mc (itzg/minecraft-server, running)." + refuses,
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo docker update --restart=no mc && sudo docker stop mc && " + thenInstall,
			stats:  []string{"existing-container-running"}},
		{name: "a server running on its own", setup: func(h *fakeHost) { h.procs = []string{paper} }, usage: site, status: "fail",
			detail: "Found a Minecraft server running: " + paper + "." + refuses,
			fix:    "If Playkeeper is to take over, stop that Minecraft server, then run the install command again.",
			stats:  []string{"existing-java"}},
		{name: "a running panel", setup: func(h *fakeHost) { pterodactyl(h); h.procs = []string{"/usr/local/bin/wings"} }, usage: site, status: "fail",
			detail: "Found Pterodactyl's files in /etc/pterodactyl and /var/lib/pterodactyl (Pterodactyl is running)." + refuses,
			fix:    "If Playkeeper is to take over, stop Pterodactyl, then run the install command again.",
			stats:  []string{"existing-panel-running"}},
		{name: "an old service next to a running server", setup: func(h *fakeHost) { unitFile(h); h.procs = []string{paper} }, usage: site, status: "fail",
			detail: "Found a Minecraft server running: " + paper + "; the service minecraft.service (not running)." + refuses,
			fix:    "If Playkeeper is to take over, stop that Minecraft server, then run the install command again.",
			stats:  []string{"existing-java"}},
		{name: "a channel's command", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "enabled"} },
			usage: Usage{Source: usage.SourceSite, Channel: "hn"}, status: "fail",
			detail: "Found the service minecraft.service (not running, but it starts with the machine)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo systemctl disable minecraft.service && curl -fsSL https://playkeeper.io/install/hn | sudo sh",
			stats:  []string{"existing-service-enabled"}},
		{name: "a release's installer, with usage stats off", setup: func(h *fakeHost) { unitFile(h); h.units = map[string]string{"minecraft.service": "enabled"} },
			usage: Usage{Source: usage.SourceTarball, Choice: usage.Off, Why: usage.EnvDoNotTrack}, status: "fail",
			detail: "Found the service minecraft.service (not running, but it starts with the machine)." + refuses,
			fix:    "It starts again by itself, so it could get in Playkeeper's way. To keep it off and install next to it: sudo systemctl disable minecraft.service && sudo DO_NOT_TRACK=1 ./install.sh",
			stats:  []string{"existing-service-enabled"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			c.setup(h)
			o := opts("")
			o.Usage = c.usage
			if c.opts != nil {
				c.opts(&o)
			}
			f := Preflight(context.Background(), h.system(t), o)
			ch := check(f, "existing")
			if ch == nil || ch.Status != c.status {
				t.Fatalf("check: %+v", ch)
			}
			if ch.Detail != c.detail {
				t.Errorf("detail:\n got %q\nwant %q", ch.Detail, c.detail)
			}
			if ch.Fix != c.fix {
				t.Errorf("fix:\n got %q\nwant %q", ch.Fix, c.fix)
			}
			if !slices.Equal(ch.Stats, c.stats) || !slices.Equal(f.LeftAlone, c.left) {
				t.Errorf("stats %v, want %v; left alone %q, want %q", ch.Stats, c.stats, f.LeftAlone, c.left)
			}
		})
	}
}

// An install next to an old service, a stopped container and a panel's
// folders goes ahead, names each in one line, and the install, its
// uninstall and both runs' commands leave them as they were.
func TestAnInstallNextToOldSetupsNeverTouchesThem(t *testing.T) {
	h := newFakeHost(t)
	unitFile(h)
	container(h, "exited", "no")
	pterodactyl(h)
	os.WriteFile(filepath.Join(h.root, "/etc/pterodactyl/config.yml"), []byte("their panel"), 0o640)
	theirs := func() string {
		a, _ := os.ReadFile(filepath.Join(h.root, "/etc/systemd/system/minecraft.service"))
		b, _ := os.ReadFile(filepath.Join(h.root, "/etc/pterodactyl/config.yml"))
		s := sha256.Sum256(append(a, b...))
		return hex.EncodeToString(s[:])
	}
	before := theirs()
	var r reports
	o := withUsage(opts(""), &r)
	o.Yes = true
	sys := h.system(t)
	res, err := Run(context.Background(), sys, o, "test")
	if err != nil {
		t.Fatalf("install next to old setups: %v\n%s", err, o.Out)
	}
	want := []string{
		"Left Pterodactyl's old files (/etc/pterodactyl and /var/lib/pterodactyl) alone; Pterodactyl isn't running.",
		"Left your old Minecraft container (mc) alone; it's stopped and won't start by itself.",
		"Left your old Minecraft service (minecraft.service) alone; it isn't running.",
	}
	if !slices.Equal(res.LeftAlone, want) {
		t.Errorf("left alone: %q", res.LeftAlone)
	}
	if got := strings.Join(r.events(), " "); got != "started: succeeded:" {
		t.Errorf("sent %q", got)
	}
	if err := Uninstall(context.Background(), sys, UninstallOptions{Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if theirs() != before || len(h.containers) != 1 {
		t.Fatal("the old service, the panel's files or the container changed")
	}
	for _, cmd := range h.cmds {
		if (strings.Contains(cmd, "minecraft.service") || strings.Contains(cmd, "pterodactyl") || strings.Contains(cmd, " mc")) && !readOnly(cmd) {
			t.Fatalf("the installer touched an old setup: %q", cmd)
		}
	}
}
