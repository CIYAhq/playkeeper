# 0001 — Risk spike: local agent control of an isolated Minecraft container

- **Date:** 2026-09-24, 13:30–13:50 UTC. **Timebox:** 60 minutes (used about 20).
- **Host:** Cursor Cloud Agent VM (Ubuntu 24.04.4, x86_64, 4 vCPU, 15 GiB). Docker 29.1.3 from Ubuntu's `docker.io` package, `dockerd` started by hand (the VM has no systemd). This VM is a *development* host, not a fresh install target.
- **Question:** can a least-privilege local process start, stop and query one isolated Minecraft Java/Paper container and obtain player events trustworthy enough to show as analytics?
- **Outcome: yes.** Decisions that follow from it are in [0002-stack.md](0002-stack.md).

## What was run and observed

1. **Upstream download after EULA.** PaperMC's v2 API is gone (`https://api.papermc.io/v2/projects/paper` → HTTP 410). The pinned image `itzg/minecraft-server:2026.9.1-java25` (`@sha256:e8640538dac5d54c2838d57fa9641e735ad0cf2b71fb0e8a68da3b542a315749`) uses the Fill v3 API. With `EULA=TRUE TYPE=PAPER VERSION=26.1.2 PAPER_BUILD=74` it logged `Downloaded /data/paper-26.1.2-74.jar`. The jar's SHA-256 is published by Fill v3 as `1d70b1da…95e5f7`; the image does not log a checksum check, so Playkeeper verifies the jar hash itself.

   *Environment note:* the first attempt timed out reaching `fill.papermc.io` from inside the container. The cause was stale legacy-iptables rules on this dev VM (`FORWARD` policy `DROP`, only `docker0` allowed). `iptables-legacy -P FORWARD ACCEPT` fixed it. Clean hosts are not affected.

2. **Startup.** `Done (11.903s)! For help, type "help"` about 12 s after the download on this VM.

3. **Player events from a real protocol login.** A [mineflayer](https://github.com/PrismarineJS/mineflayer) 4.39.0 bot (protocol 775, offline-mode test harness, *not* an official client) joined. Docker log output with Docker's own timestamps:

   ```
   2026-09-24T13:35:15.314296785Z [13:35:15 INFO]: UUID of player PkSpikeBot is 5507140b-cf95-3383-b75a-47dd34196981
   2026-09-24T13:35:18.245633689Z [13:35:18 INFO]: PkSpikeBot joined the game
   2026-09-24T13:35:18.252767958Z [13:35:18 INFO]: PkSpikeBot[/172.18.0.1:50284] logged in with entity id 10 at (...)
   2026-09-24T13:35:18.292621015Z [13:35:18 INFO]: [Not Secure] <PkSpikeBot> hello from spike
   2026-09-24T13:35:21.341942276Z [13:35:21 INFO]: PkSpikeBot lost connection: Disconnected
   2026-09-24T13:35:21.486576616Z [13:35:21 INFO]: PkSpikeBot left the game
   ```

   Chat is always prefixed (`<Name>`, `[Not Secure] <Name>`, `[Name]`, `* Name`), and names cannot contain spaces, so an anchored `^[hh:mm:ss INFO]: <name> joined the game$` pattern cannot be forged from chat. The login line contains the client address; with `LOG_IPS=FALSE` Paper prints `PkBuilder[IP hidden] logged in…` instead.

4. **Authoritative snapshot over RCON, never published.** RCON listened only inside the container (`RCON running on 0.0.0.0:25575`); on the host `ss -ltn` showed only `0.0.0.0:25565` and `[::]:25565`. A client on the host reached RCON via the private bridge address: `auth ok`, `list` → `There are 0 of a max of 20 players online: `.

5. **Least-privilege container.** Second run with `--user 999:999 --cap-drop ALL --security-opt no-new-privileges --memory 1536m --memory-swap 1536m --pids-limit 1024`, `RCON_PASSWORD_FILE` mounted read-only, `ENABLE_WHITELIST=TRUE ENFORCE_WHITELIST=TRUE LOG_IPS=FALSE`: server started (`Done (11.454s)!`); `docker inspect` → `Privileged=false CapAdd=[] CapDrop=[ALL] Memory=1610612736 User=999:999`; `server.properties` had `log-ips=false`, `white-list=true`, `enforce-whitelist=true`. The RCON password never appeared in `docker logs`.

6. **Distinctive, verifiable world state.** The bot placed a gold block and an oak sign whose text is a nonce. Console checks over RCON:

   ```
   execute if block 9 121 8 minecraft:gold_block      → Test passed
   data get block 9 122 8 front_text.messages          → 9, 122, 8 has the following block data: ["pk-nonce-4f2a9c", "", "", ""]
   client view of the sign                             → "pk-nonce-4f2a9c\n\n\n"
   ```

   This is the marker used by the backup/restore evidence.

## Consequences

- One container, driven through the Docker Engine API over the local Unix socket by a root agent; the web panel never gets Docker access.
- Player sessions come from anchored log patterns with Docker timestamps, deduplicated by line, cross-checked against RCON `list` snapshots. No Paper plugin is needed.
- Data directory contents include secrets (`rcon.password`, `management-server-secret` in `server.properties`, `.rcon-cli.*`) and ~150 MB of re-downloadable binaries, so backups use an allowlist and strip secrets.
- Minecraft 26.x keeps all dimensions under `world/dimensions/`; 1.21.x uses `world_nether`/`world_the_end`. Backups handle both.
- The only protocol client available here is a bot in an offline-mode test harness. An official client with a genuine account remains a named unverified step.
