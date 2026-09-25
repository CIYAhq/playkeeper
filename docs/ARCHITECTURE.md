# Architecture

Playkeeper runs **one game server on one host**: an authenticated HTTPS web panel, a narrow local control agent on a Unix socket, one isolated Minecraft Java container with persistent world data, and SQLite for application and event state. The web process does not run as root and never receives the Docker socket. Only the agent performs operations, from an explicit allowlist, and user input is never interpolated into host-shell commands. The game port is separate from management. No external database, provider VM API or hosted control plane is involved.

All of it is one Go binary with the TypeScript/React UI compiled in; the processes, users and files are described in [decisions/0002-stack.md](decisions/0002-stack.md). The game image is pinned by digest and the server software by version and checksum. Minecraft server software is downloaded from its upstream on the user's server after explicit EULA acceptance; Playkeeper never ships proprietary server binaries.

## Invariants

- Preflight existing listeners, services, resources and file paths before changing a host. Never assume ownership of a running Crafty or systemd Minecraft world. Keep an install manifest and a reversible uninstall path that does not destroy user worlds.
- Bootstrap auth locally; HTTPS, CSRF protections, session expiration, rate limiting and administrative authorization on control actions. Neither Docker nor RCON is externally available. Audit privileged actions, not secret values.
- Define desired vs observed state and idempotent start/stop/restart. Handle agent disconnect, simultaneous operations, crash and reboot coherently.
- Bound log/event retention. Prefer real player event logs with deduplication and session-gap handling; add a Paper plugin only if observed sources are insufficient. Avoid storing player IPs unless essential and documented.
- Consistent world backup can require a graceful stop. Record archive manifest/integrity and storage location; a local archive alone must be labelled on-host only. Protect restore with explicit preview, validation and rollback archive.
- Keep one tested clean-host install route and one short recovery runbook ([RECOVERY.md](RECOVERY.md)). A packaged release must run outside the developer checkout.
