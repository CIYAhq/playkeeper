# Architecture

Playkeeper runs **several game servers on one host**: an authenticated HTTPS web panel, a narrow local control agent on a Unix socket, an isolated Minecraft Java container per server with its own persistent world data, and SQLite for application and event state. The web process does not run as root and never receives the Docker socket. Only the agent performs operations, from an explicit allowlist, and user input is never interpolated into host-shell commands. Game ports are separate from management. No external database, provider VM API or hosted control plane is involved.

All of it is one Go binary with the TypeScript/React UI compiled in; the processes, users and files are described in [decisions/0002-stack.md](decisions/0002-stack.md), and how several servers, the data model and the dashboard fit together in [decisions/0004-playkeeper-2.md](decisions/0004-playkeeper-2.md). The game image is pinned by digest; the server software is a Paper build from PaperMC's version list, checked against the SHA-256 PaperMC publishes for it. Minecraft server software is downloaded from its upstream on the user's server after explicit EULA acceptance; Playkeeper never ships proprietary server binaries.

## Servers, machines and projects

- The panel keeps projects, the machines in them and who may do what; for now there is one project, one machine (the local agent) and one admin. The agent keeps the machine's servers. Every server has a random ten-character id, a name and a URL slug; every per-server row (operations, events, sessions, samples, backups, audit) carries its id.
- A new server gets its own directory (`/var/lib/playkeeper/servers/<id>/`), container (`playkeeper-mc-<id>`), RCON secret and the next free game port. A server migrated from 0.2.0 keeps 0.2.0's container, paths, secret and labels exactly, so its container definition does not change and it keeps running through the upgrade.
- Operations are exclusive per server, so two servers can back up or restart at the same time; a Playkeeper update waits until every server is idle. Each server reserves its memory budget even while stopped, so it can always start.
- The panel routes each request by server to the machine that runs it. Player faces are fetched from Mojang by the panel and cached in its database; browsers never contact Mojang.

## Invariants

- Preflight existing listeners, services, resources and file paths before changing a host. Never assume ownership of a running Crafty or systemd Minecraft world. Keep an install manifest and a reversible uninstall path that does not destroy user worlds.
- Bootstrap auth locally; HTTPS, CSRF protections, session expiration, rate limiting and administrative authorization on control actions. Neither Docker nor RCON is externally available. Audit privileged actions, not secret values.
- Define desired vs observed state and idempotent start/stop/restart, per server. Handle agent disconnect, simultaneous operations, crash and reboot coherently.
- Bound log/event retention. Prefer real player event logs with deduplication and session-gap handling; add a Paper plugin only if observed sources are insufficient. Avoid storing player IPs unless essential and documented.
- Consistent world backup can require a graceful stop. Record archive manifest/integrity and storage location; a local archive alone must be labelled on-host only. Protect restore with explicit preview, validation and rollback archive. A backup can also be restored as a new server.
- Changes to Playkeeper and to the game version only go forward and can be undone. An installed Playkeeper installs only a newer release signed with a key compiled into it. The sandboxed agent cannot replace its own binary, so it verifies and stages the release, and a root oneshot updater started by a systemd path unit checks it again, swaps the binary and units, restarts only the agent and panel, and puts the previous binary, units, config and databases back if the new version is not healthy. A Minecraft version change backs up the world first and restores it if the new version does not start.
- Every UI string goes through the translation layer (`web/src/i18n`); a test fails on text written straight into a component.
- Keep one tested clean-host install route and one short recovery runbook ([RECOVERY.md](RECOVERY.md)). A packaged release must run outside the developer checkout.
