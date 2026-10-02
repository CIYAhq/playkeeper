package install

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/usage"
)

// Another Minecraft setup turned away most install runs. The check now says
// exactly what it found, whether it runs, and the one thing to do: stop what
// runs if Playkeeper is to take over, or install next to what doesn't, with
// the command to paste. The stats say which kind of setup it was.
func TestTheExistingSetupRefusalSaysWhatItFoundAndWhatToDo(t *testing.T) {
	site := Usage{Source: usage.SourceSite}
	const again = "curl -fsSL https://playkeeper.io/install | sudo sh -s -- --allow-existing-minecraft"
	unit := func(h *fakeHost) {
		os.WriteFile(filepath.Join(h.root, "/etc/systemd/system/minecraft.service"), []byte("[Service]\n"), 0o644)
	}
	java := "java -Xmx2G -jar paper-1.21.11.jar nogui"
	for _, c := range []struct {
		name            string
		setup           func(h *fakeHost)
		active, enabled string
		usage           Usage
		opts            func(o *Options)
		detail, fix     string
		stats           []string
	}{
		{name: "other ports and a release location", setup: unit, active: "inactive", enabled: "disabled", usage: site,
			opts: func(o *Options) {
				o.PanelPort, o.GamePort, o.ReleaseURL = 9443, 25566, "https://example.com/releases?channel=beta&x=1"
			},
			detail: "Found the service minecraft.service (not running).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: curl -fsSL https://playkeeper.io/install | sudo sh -s -- --panel-port 9443 --game-port 25566 --release-url 'https://example.com/releases?channel=beta&x=1' --allow-existing-minecraft",
			stats:  []string{"existing-service-stopped"}},
		{name: "a machine joining a dashboard", setup: unit, active: "active", enabled: "enabled", usage: site,
			opts:   func(o *Options) { o.Join = "203.0.113.5:8443" },
			detail: "Found the service minecraft.service (running).",
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo systemctl disable --now minecraft.service, then run the join command from your dashboard again, with --allow-existing-minecraft at the end",
			stats:  []string{"existing-service-running"}},
		{name: "a machine joining a dashboard, next to a server running on its own", setup: func(h *fakeHost) { h.procs = []string{java} }, usage: site,
			opts:   func(o *Options) { o.Join = "203.0.113.5:8443" },
			detail: "Found a Minecraft server running: " + java + ".",
			fix:    "If Playkeeper is to take over, stop that server, then run the join command from your dashboard again.",
			stats:  []string{"existing-java"}},
		{name: "a service that isn't running", setup: unit, active: "inactive", enabled: "disabled", usage: site,
			detail: "Found the service minecraft.service (not running).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: " + again,
			stats:  []string{"existing-service-stopped"}},
		{name: "a stopped service that starts with the machine", setup: unit, active: "inactive", enabled: "enabled", usage: site,
			detail: "Found the service minecraft.service (not running, but it starts with the machine).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: sudo systemctl disable --now minecraft.service && " + again,
			stats:  []string{"existing-service-stopped"}},
		{name: "a running service", setup: unit, active: "active", enabled: "enabled", usage: site,
			detail: "Found the service minecraft.service (running).",
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo systemctl disable --now minecraft.service && " + again,
			stats:  []string{"existing-service-running"}},
		{name: "a stopped container", setup: func(h *fakeHost) {
			h.dockerPresent = true
			h.containers = []docker.ContainerSummary{{Names: []string{"/mc"}, Image: "itzg/minecraft-server", State: "exited"}}
		}, usage: site,
			detail: "Found the Docker container mc (itzg/minecraft-server, stopped).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: " + again,
			stats:  []string{"existing-container-stopped"}},
		{name: "a running container and its server", setup: func(h *fakeHost) {
			h.dockerPresent = true
			h.containers = []docker.ContainerSummary{{Names: []string{"/mc"}, Image: "itzg/minecraft-server", State: "running"}}
			h.procs = []string{java}
		}, usage: site,
			detail: "Found a Minecraft server running: " + java + "; the Docker container mc (itzg/minecraft-server, running).",
			fix:    "If Playkeeper is to take over, stop it and install next to its files: sudo docker stop mc && " + again,
			stats:  []string{"existing-container-running", "existing-java"}},
		{name: "a server running on its own", setup: func(h *fakeHost) { h.procs = []string{java} }, usage: site,
			detail: "Found a Minecraft server running: " + java + ".",
			fix:    "If Playkeeper is to take over, stop that server, then run the install command again.",
			stats:  []string{"existing-java"}},
		{name: "a panel's files", setup: func(h *fakeHost) { os.MkdirAll(filepath.Join(h.root, "/etc/pterodactyl"), 0o755) }, usage: site,
			detail: "Found Pterodactyl's files in /etc/pterodactyl.",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: " + again,
			stats:  []string{"existing-panel"}},
		{name: "a channel's command", setup: unit, active: "inactive", enabled: "disabled", usage: Usage{Source: usage.SourceSite, Channel: "hn"},
			detail: "Found the service minecraft.service (not running).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: curl -fsSL https://playkeeper.io/install/hn | sudo sh -s -- --allow-existing-minecraft",
			stats:  []string{"existing-service-stopped"}},
		{name: "a release's installer, with usage stats off", setup: unit, active: "inactive", enabled: "disabled",
			usage:  Usage{Source: usage.SourceTarball, Choice: usage.Off, Why: usage.EnvDoNotTrack},
			detail: "Found the service minecraft.service (not running).",
			fix:    "None of it is running, so Playkeeper can install next to it and never touch it: sudo DO_NOT_TRACK=1 ./install.sh --allow-existing-minecraft",
			stats:  []string{"existing-service-stopped"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			c.setup(h)
			sys := h.system(t)
			run := sys.Run
			sys.Run = func(name string, args ...string) (string, error) {
				if name == "systemctl" && len(args) == 2 && args[0] == "is-active" {
					return c.active + "\n", nil
				}
				if name == "systemctl" && len(args) == 2 && args[0] == "is-enabled" {
					return c.enabled + "\n", nil
				}
				return run(name, args...)
			}
			o := opts("")
			o.Usage = c.usage
			if c.opts != nil {
				c.opts(&o)
			}
			ch := check(Preflight(context.Background(), sys, o), "existing")
			if ch == nil || ch.Status != "fail" {
				t.Fatalf("check: %+v", ch)
			}
			if want := c.detail + " Playkeeper installs next to another Minecraft setup only when you say so."; ch.Detail != want {
				t.Errorf("detail:\n got %q\nwant %q", ch.Detail, want)
			}
			if ch.Fix != c.fix {
				t.Errorf("fix:\n got %q\nwant %q", ch.Fix, c.fix)
			}
			if !slices.Equal(ch.Stats, c.stats) {
				t.Errorf("stats %v, want %v", ch.Stats, c.stats)
			}
		})
	}

	// A refused install reports which kinds of setup turned it away.
	h := newFakeHost(t)
	h.dockerPresent = true
	h.containers = []docker.ContainerSummary{{Names: []string{"/mc"}, Image: "itzg/minecraft-server", State: "running"}}
	h.procs = []string{java}
	var r reports
	o := withUsage(opts(""), &r)
	o.Yes = true
	if _, err := Run(context.Background(), h.system(t), o, "test"); err == nil {
		t.Fatal("the install went ahead")
	}
	if got := strings.Join(r.events(), " "); got != "refused:existing-container-running+existing-java" {
		t.Errorf("sent %q", got)
	}
}
