package install

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CIYAhq/playkeeper/internal/config"
)

const (
	UpdatePathUnit    = "playkeeper-update.path"
	UpdateServiceUnit = "playkeeper-update.service"
	LinkUnit          = "playkeeper-link.service"
)

// unitNames are the only systemd units Playkeeper installs; an update may
// write no other file into /etc/systemd/system.
var unitNames = []string{AgentUnit, PanelUnit, UpdatePathUnit, UpdateServiceUnit, LinkUnit}

// Units returns the systemd units this version installs on a machine, by
// name. A machine installed to join another dashboard has no panel, and
// only a joined machine has the link: an older version's updater refuses
// units it doesn't know, and it only ever runs on machines that never
// joined.
func Units(cfg config.Config, joined bool) map[string]string {
	units := map[string]string{
		AgentUnit:         agentUnit(),
		UpdatePathUnit:    updatePathUnit(cfg),
		UpdateServiceUnit: updateServiceUnit(),
	}
	if !cfg.NoPanel {
		units[PanelUnit] = panelUnit(cfg.PanelPort)
	}
	if joined {
		units[LinkUnit] = LinkUnitFile(cfg)
	}
	return units
}

// Joined reports whether the machine under root has joined a dashboard.
func Joined(cfg config.Config, root string) bool {
	_, err := os.Stat(filepath.Join(root, cfg.LinkDashboardPath()))
	return err == nil
}

// LinkUnitFile keeps a joined machine's link to its dashboard, as the
// unprivileged 'playkeeper' user: it dials out and answers the dashboard
// through the agent's socket, so no port opens on the machine. It starts
// only while the machine is joined; when the dashboard removes the machine,
// the link deletes the file that says so and stays stopped.
func LinkUnitFile(cfg config.Config) string {
	return fmt.Sprintf(`[Unit]
Description=Playkeeper machine link (dials the dashboard this machine joined)
After=network-online.target playkeeper-agent.service
Wants=network-online.target
ConditionPathExists=%s

[Service]
Type=simple
User=playkeeper
Group=playkeeper
ExecStart=/usr/local/bin/playkeeper link --config /etc/playkeeper/config.json
Restart=always
RestartSec=5
RuntimeDirectory=playkeeper-link
RuntimeDirectoryMode=0700
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=%s
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
`, cfg.LinkDashboardPath(), cfg.LinkDir())
}

// UpdateDir holds a verified update the agent staged, the updater's
// request and result, and the copy of the previous version it can put back.
func UpdateDir(cfg config.Config) string { return filepath.Join(cfg.AgentDir(), "update") }

// updatePathUnit starts the updater when the agent has staged a verified
// update, and again if an update was interrupted (the updater then finishes
// or rolls it back). The agent cannot replace the binary itself: its sandbox
// is read-only outside /var/lib/playkeeper.
func updatePathUnit(cfg config.Config) string {
	dir := UpdateDir(cfg)
	return fmt.Sprintf(`[Unit]
Description=Playkeeper updater trigger

[Path]
PathExists=%s
PathExists=%s
Unit=%s

[Install]
WantedBy=multi-user.target
`, filepath.Join(dir, "request.json"), filepath.Join(dir, "applying.json"), UpdateServiceUnit)
}

// updateServiceUnit runs the installed (previous) binary as the updater: it
// installs the staged release, restarts the agent and panel, and puts the
// previous version back if the new one does not come up healthy. It may only
// write the binary, the units, the config and Playkeeper's state.
func updateServiceUnit() string {
	return `[Unit]
Description=Playkeeper updater (installs a verified release; rolls back if it is unhealthy)

[Service]
Type=oneshot
ExecStart=/usr/local/bin/playkeeper self-update --config /etc/playkeeper/config.json
TimeoutStartSec=20min
UMask=0077
NoNewPrivileges=yes
ProtectSystem=full
ReadWritePaths=/usr/local/bin /etc/systemd/system /etc/playkeeper /var/lib/playkeeper
ProtectHome=yes
PrivateTmp=yes
`
}

// agentUnit runs the root agent with a read-only view of the host except its
// own state directory and runtime socket directory, and with at most half the
// memory minecraft.HostReserveMB keeps free for the host. CAP_NET_BIND_SERVICE
// lets it answer Let's Encrypt's checks on port 80 for the seconds a
// certificate for an own domain is being issued.
func agentUnit() string {
	return `[Unit]
Description=Playkeeper agent (local control of the Minecraft container)
Documentation=file:///usr/local/share/doc/playkeeper
After=docker.service network-online.target
Requires=docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/playkeeper agent --config /etc/playkeeper/config.json
Restart=on-failure
RestartSec=3
RuntimeDirectory=playkeeper
RuntimeDirectoryMode=0755
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/playkeeper /run/playkeeper
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE
# Playkeeper keeps 768 MB free for the system, Docker and itself. The agent
# may use half of it, so if a file crafted by a plugin or mod makes it use
# more, the agent is stopped and restarted instead of starving the game
# servers. Its Go runtime collects garbage harder as it nears GOMEMLIMIT,
# before the kernel slows it down at MemoryHigh.
MemoryHigh=256M
MemoryMax=384M
Environment=GOMEMLIMIT=192MiB

[Install]
WantedBy=multi-user.target
`
}

// panelUnit runs the web panel as the unprivileged 'playkeeper' user. It
// cannot write outside its own state directory and has no Docker access.
func panelUnit(port int) string {
	caps := "CapabilityBoundingSet=\nAmbientCapabilities="
	if port < 1024 {
		caps = "CapabilityBoundingSet=CAP_NET_BIND_SERVICE\nAmbientCapabilities=CAP_NET_BIND_SERVICE"
	}
	return fmt.Sprintf(`[Unit]
Description=Playkeeper web panel (HTTPS)
After=network-online.target playkeeper-agent.service
Wants=network-online.target

[Service]
Type=simple
User=playkeeper
Group=playkeeper
ExecStart=/usr/local/bin/playkeeper panel --config /etc/playkeeper/config.json
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/playkeeper/panel
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
%s

[Install]
WantedBy=multi-user.target
`, caps)
}
