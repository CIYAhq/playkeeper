# Playkeeper product brief

## Promise

A gamer with their own VPS can set up a Minecraft Java server, invite friends, understand what is happening, and recover their world on another machine without becoming a Linux/server expert. The user still owns the host and world data. The dashboard is not a VPS reseller.

## V1 journey

1. **Prepare:** guided installation on one clean supported Linux VPS. Preflight RAM/disk/architecture, occupied ports, existing supervisors, Docker and TLS prerequisites. Show exactly what will change and how to back out. Initial install may use a terminal; creating/managing the Minecraft server thereafter should not.
2. **Secure:** first-run admin setup, authenticated HTTPS panel, secrets generated on-host. Management port and Minecraft game port are distinct. No exposed Docker API, host shell or public RCON.
3. **Create:** accept the Minecraft EULA explicitly, choose a pinned Paper/Minecraft version and RAM budget, start one server. Show downloading/starting/reachable/joinable and actionable failures. Copy a real join address. A real client joins.
4. **Operate:** actual status, player count, process CPU/RAM, disk free, uptime/version and last backup; state-aware start/stop/restart; bounded console logs and audited game-console commands (not host commands). Honest empty/error states for unavailable agent, crashes, port collisions and low space.
5. **Understand:** player count over time, observed join/leave sessions, daily activity/aggregate playtime when evidence supports it, plus server performance. Sampling and event provenance must be visible; restart/offline gaps are gaps, not zeros or invented events. Do not backfill pre-install history.
6. **Protect:** first consistent backup may stop the server with visible downtime. Verify archive integrity; allow download/export. Clearly distinguish on-host from off-host storage. Restore on a second isolated host, join, and inspect a distinctive world state. Restore over an existing live world requires preview, confirmation and a rollback archive.

## Main views

Onboarding → Overview → Console → Players → World/backups → Settings. Desktop and narrow layouts must be usable, legible and honest when no data exists.

## Not V1

Other games; multiple servers/hosts; provisioning Hetzner or other cloud VMs; billing; public account signups; modpack/plugin marketplaces; importing a live Crafty/systemd-managed world; generic game-adapter architecture. Do not touch an existing world during first deployment.

## Success criteria

A new user follows the supported install path, creates a server, joins and has a friend join, sees dashboard state agree with actual server activity, exports a backup and restores the same world on another host. Installation and recovery steps must be executable from a clean checkout/package, not just in a developer worktree. Log failures and real timings; make no unmeasured speed or reliability claim. Five unrelated self-hosters are a later product validation target, not a fabricated release test.
