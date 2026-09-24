# 0002 — Stack and process model

- **Date:** 2026-09-24. **Status:** accepted for the first release. Follows [0001-risk-spike.md](0001-risk-spike.md).

## Decision

One statically linked Go binary, `playkeeper`, plays every role; the browser UI is TypeScript/React compiled into it.

| Process | Runs as | Talks to | Listens on |
| --- | --- | --- | --- |
| `playkeeper agent` (systemd `playkeeper-agent`) | root, systemd sandbox (`ProtectSystem=strict`, 4 capabilities) | Docker Engine API over `/var/run/docker.sock`; RCON over the private `playkeeper` bridge | `/run/playkeeper/agent.sock` only (0660 root:playkeeper, SO_PEERCRED check) |
| `playkeeper panel` (systemd `playkeeper-panel`) | `playkeeper` (not in the `docker` group) | the agent socket | TCP 8443, TLS only |
| Minecraft container `playkeeper-minecraft` | `playkeeper-mc` UID, no capabilities, `no-new-privileges`, exact memory limit | internet (downloads), players | TCP 25565 (published); RCON 25575 unpublished |

State is local SQLite (`modernc.org/sqlite`, pure Go): `/var/lib/playkeeper/agent/agent.db` (server config, samples, events, sessions, backups, audit) and `/var/lib/playkeeper/panel/panel.db` (admin account, sessions, panel audit). World data lives in `/var/lib/playkeeper/server/data`, archives in `/var/lib/playkeeper/backups`. No external database, hosted control plane, telemetry or cloud API.

The only long-running services are the two systemd units and the one container. There is no plugin, adapter or multi-server layer: the code manages exactly one Paper server.

## Why

- **Single static binary:** the installer copies one file; nothing else to install besides Docker. Contributors need Go and Node only (pinned in `scripts/toolchains.txt`).
- **Go for agent and panel:** stdlib HTTP, TLS, Unix sockets and peer credentials; easy fault-injection tests; one language for all privileged code. A Rust agent would add a second toolchain for no V1 benefit.
- **Root agent + unprivileged panel:** Docker access is root-equivalent, so only the agent has it, behind a closed route table. The web process, which parses untrusted network input, has no Docker access and cannot write outside its own directory.
- **itzg image, pinned:** well-maintained (Apache-2.0), handles Paper downloads via Fill v3; pinned by digest, and Playkeeper verifies the Paper jar's SHA-256 before first run.
- **Log parsing + RCON `list`:** the spike showed both are available and trustworthy without a Paper plugin.
- **Self-signed TLS:** works on any VPS without a domain or DNS change; the installer prints the fingerprint to compare. Publicly trusted certificates (ACME) are out of scope for V1.

## Rejected alternatives

- **Next.js/Node panel:** needs a Node runtime on the VPS and a second process manager story.
- **Docker Compose stack for panel + agent:** would require mounting the Docker socket into a container, the thing the brief forbids for the management plane.
- **Paper plugin for events:** unnecessary given reliable log lines and RCON; adds a JVM build and plugin supply chain.
- **Pterodactyl/Pelican-style Wings daemon:** designed for multi-node hosting; far heavier than one server on one VPS.
- **Proxying RCON to the browser:** would expose a remote console protocol; instead console commands go through the audited agent API.
- **Caddy/nginx in front for TLS:** extra package and config to manage; Go's TLS is enough for one panel.

## Competing panels (advisory)

Crafty, Pterodactyl/Pelican, PufferPanel and AMP were considered for positioning, not code. They target many servers, multiple games or multiple nodes, and their installers assume a sysadmin. Playkeeper deliberately does less: one existing VPS, one Paper server, guided setup, honest analytics and a restore path. Playkeeper's installer refuses to run next to them by default so it never takes over an existing world.
