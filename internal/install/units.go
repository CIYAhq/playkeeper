package install

import "fmt"

// agentUnit runs the root agent with a read-only view of the host except its
// own state directory and runtime socket directory.
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
CapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH

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
