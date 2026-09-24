# Architecture brief (proposal, not implementation)

Prefer **one host, one game server**. A suggested shape is an authenticated HTTPS web panel, a narrow local Unix-socket control agent, one isolated Minecraft Java container with persistent world data, and SQLite for application/event state. The web process does not run as root or receive the Docker socket. Only the agent can perform explicitly allowlisted operations; never interpolate user input into host-shell commands. The game port is separate from management. No Vercel, external database, provider VM API or hosted control plane is required.

Cursor should timebox a spike for actual local agent/container control and player event sourcing before finalizing the stack. TypeScript for the UI and a small Go or Rust Linux agent are proposals, not mandates; record the decision and avoid multiple services for hypothetical future games. Pin game image by digest and server software version; obtain Minecraft server software from authorized upstream during installation after explicit EULA acceptance, never vendor proprietary server binaries.

## Invariants

- Preflight existing listeners, services, resources and file paths before changing a host. Never assume ownership of a running Crafty or systemd Minecraft world. Keep an install manifest and reversible uninstall path that does not destroy user worlds.
- Bootstrap auth locally; HTTPS, CSRF protections, session expiration, rate limiting and administrative authorization on control actions. Neither Docker nor RCON is externally available. Audit privileged actions, not secret values.
- Define desired vs observed state and idempotent start/stop/restart. Handle agent disconnect, simultaneous operations, crash and reboot coherently.
- Bound log/event retention. Prefer real player event logs with deduplication and session-gap handling; add a Paper plugin only if observed sources are insufficient. Avoid storing player IPs unless essential and documented.
- Consistent world backup can require a graceful stop. Record archive manifest/integrity and storage location; a local archive alone must be labelled on-host only. Protect restore with explicit preview, validation and rollback archive.
- Use one tested clean-host install route and one short recovery runbook before making the installer broadly available. A packaged release must run outside the developer checkout.

## Approval boundary

Do not buy/provision a VPS, edit CIYA DNS/firewalls, deploy to a real CIYA host, migrate a live world, publish/release, change GitHub visibility or commit secrets without Siya's explicit approval. Disposable local/isolated no-cost testing is in scope. If Cloud Agent cannot run Docker, report the exact unverified E2E stage; do not replace it with mocked proof.
