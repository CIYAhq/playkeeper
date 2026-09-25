package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// diskCheck rates the free space for the world and its backups. Preflight and
// the Overview's low-disk warning use the same thresholds and advice.
func diskCheck(free int64) api.PreflightCheck {
	gb := float64(free) / (1 << 30)
	c := api.PreflightCheck{ID: "disk", Label: "Disk space"}
	switch {
	case free < 2<<30:
		c.Status, c.Detail, c.Fix = "fail", fmt.Sprintf("Only %.1f GB free.", gb), "Free at least 5 GB of disk space (old logs, unused Docker images: sudo docker image prune), then check again."
	case free < 5<<30:
		c.Status, c.Detail, c.Fix = "warn", fmt.Sprintf("%.1f GB free. Worlds and backups grow over time.", gb), "Keep at least 5 GB free for the world and its backups."
	default:
		c.Status, c.Detail = "pass", fmt.Sprintf("%.1f GB free.", gb)
	}
	return c
}

// Preflight reports whether this host can run the Minecraft server, with an
// actionable fix for every problem.
func (a *Agent) Preflight(ctx context.Context) api.Preflight {
	var checks []api.PreflightCheck
	add := func(id, label, status, detail, fix string) {
		checks = append(checks, api.PreflightCheck{ID: id, Label: label, Status: status, Detail: detail, Fix: fix})
	}
	dockerOK := false
	if v, err := a.docker.Negotiate(ctx); err != nil {
		add("docker", "Docker", "fail", "Docker is not reachable: "+err.Error(), "Start Docker with: sudo systemctl start docker")
	} else {
		dockerOK = true
		add("docker", "Docker", "pass", fmt.Sprintf("Docker %s (API %s) is running.", v.Version, v.APIVersion), "")
	}
	host := a.opts.HostMemoryMB()
	opts, rec, _ := minecraft.MemoryOptions(host)
	if len(opts) == 0 {
		add("memory", "Memory", "fail", fmt.Sprintf("This host has %d MB of RAM; a Minecraft server needs at least %d MB plus %d MB for the system.", host, 1536, minecraft.HostReserveMB),
			"Use a VPS with at least 3 GB of RAM.")
	} else {
		add("memory", "Memory", "pass", fmt.Sprintf("%.1f GB RAM. Suggested server budget: %.1f GB.", float64(host)/1024, float64(rec)/1024), "")
	}
	if free, _, err := a.opts.DiskUsage(a.cfg.DataDir); err != nil {
		add("disk", "Disk space", "warn", "Could not measure free space: "+err.Error(), "")
	} else {
		checks = append(checks, diskCheck(free))
	}
	port := a.cfg.GamePort
	ours := ""
	if dockerOK {
		for _, s := range a.serverList() {
			if _, running, _ := s.containerRunning(ctx); running && s.gamePort == port {
				ours = s.name()
			}
		}
	}
	switch {
	case ours != "":
		add("port", "Game port", "pass", fmt.Sprintf("Port %d is used by your Playkeeper server %s.", port, ours), "")
	case a.opts.PortInUse(port):
		add("port", "Game port", "fail", fmt.Sprintf("Port %d is already used by another program.", port),
			fmt.Sprintf("Find it with: sudo ss -ltnp 'sport = :%d' — stop it, or reinstall Playkeeper with a different --game-port.", port))
	default:
		add("port", "Game port", "pass", fmt.Sprintf("Port %d is free for Minecraft players.", port), "")
	}
	if err := a.opts.CheckEgress(ctx); err != nil {
		add("egress", "Download access", "fail", "Cannot reach PaperMC (fill.papermc.io): "+err.Error(),
			"Allow outbound HTTPS from this server to fill.papermc.io, piston-data.mojang.com and registry-1.docker.io.")
	} else {
		add("egress", "Download access", "pass", "PaperMC's download service is reachable.", "")
	}
	if dockerOK {
		if list, err := a.docker.ContainerList(ctx, true); err == nil {
			var others []string
			for _, c := range list {
				if c.Labels[labelManaged] == "true" {
					continue
				}
				if strings.Contains(strings.ToLower(c.Image), "minecraft") {
					name := strings.TrimPrefix(strings.Join(c.Names, ","), "/")
					others = append(others, name+" ("+c.State+")")
				}
			}
			if len(others) > 0 {
				add("existing", "Other Minecraft containers", "warn", "Found: "+strings.Join(others, ", ")+". Playkeeper will not touch them.",
					"Make sure they do not use port "+strconv.Itoa(port)+" or compete for memory.")
			}
		}
	}
	if a.offline() {
		add("offline", "Test harness mode", "warn", "Offline mode is enabled for automated protocol-bot tests: anyone could join with any name.",
			"Remove "+OfflineModeEnv+" from the agent's environment before real use.")
	}
	ok := true
	for _, c := range checks {
		if c.Status == "fail" {
			ok = false
		}
	}
	return api.Preflight{OK: ok, Checks: checks}
}
