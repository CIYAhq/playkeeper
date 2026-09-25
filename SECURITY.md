# Security

Playkeeper operates game servers and world data on the user's host. Treat the web panel as a sensitive administrative interface, not a public demo.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: on the repository's **Security** tab, choose **Report a vulnerability** ([direct link](https://github.com/CIYAhq/playkeeper/security/advisories/new)). The report, the discussion and the fix stay private until an advisory is published. There is no security email address.

Please include the Playkeeper version (`playkeeper version`), what an attacker needs (network position, credentials) and what they gain, and steps to reproduce. Do not put working exploits, secrets, world backups, player identifiers or server addresses in public issues, pull requests or discussions.

**Supported versions:** only the latest release (0.1.x today) receives security fixes, as a new release. The one-line installer always installs the latest release.

## Design

Implementation constraints: authenticated HTTPS management, fail-closed authorization, local allowlisted privilege boundary, no public Docker socket/host shell/RCON, redacted logs, bounded player-data retention, explicit restore preview and rollback, safe installation on previously unmodified hosts. These properties are covered by the unit tests and the end-to-end jobs in [CI](.github/workflows/ci.yml).

## Exposure review (v0.1.0, 2026-09-24)

**Network.** Two TCP listeners: the panel on 8443 (TLS only; plain HTTP gets `400 Client sent an HTTP request to an HTTPS server`) and Minecraft on 25565 (published by Docker). RCON (25575) stays inside the container on a private bridge; the Docker API is only the local Unix socket; the agent has no TCP listener. Checked with `nmap -p-` from another host during a rehearsal install.

**Privilege boundary.**
- `playkeeper panel` runs as the `playkeeper` user (not in the `docker` group) under a systemd sandbox that only allows writes to `/var/lib/playkeeper/panel`.
- `playkeeper agent` runs as root with 4 capabilities (`CAP_CHOWN`, `CAP_FOWNER`, `CAP_DAC_OVERRIDE`, `CAP_DAC_READ_SEARCH`) and a read-only view of the host except `/var/lib/playkeeper` and `/run/playkeeper`. It serves a closed route table on `/run/playkeeper/agent.sock` (0660 root:playkeeper), checks the caller's UID with `SO_PEERCRED`, rejects unknown fields and trailing data, and never runs a shell. Console commands go to Minecraft over RCON as literal text.
- The Minecraft container runs as `playkeeper-mc` with all capabilities dropped, `no-new-privileges`, a memory limit equal to the chosen budget, a PID limit, only the game port published, and no Docker socket mount.
- Docker access is root-equivalent; that is why only the agent has it.

**Authentication.** The first admin needs a one-time setup code printed by the installer (stored only as a SHA-256 hash, 24-hour expiry, deleted on use; setup is disabled once an admin exists; there is no sign-up). Passwords use argon2id. Session tokens are random, stored hashed, sent in a `__Host-` cookie with `Secure; HttpOnly; SameSite=Strict`, and expire after 12 hours idle or 7 days. Every state-changing request needs the session's CSRF token and a same-origin `Origin`; sign-in needs a same-origin marker header. Sign-in is rate-limited per address with per-account lockout; control actions are rate-limited per session. Responses carry a strict CSP and anti-framing headers.

**Data.** Player IP addresses are never stored or shown (`log-ips=false` plus redaction); player names, UUIDs and session times are kept for 180 days, samples for 30 days, audit for 365 days, with row caps. Secrets are generated on the host: RCON password (`/var/lib/playkeeper/agent/rcon.secret`, 0600 root, plus a read-only copy for the game user), TLS key (0600 panel user), session tokens. Backups strip `rcon.password` and `management-server-secret` from `server.properties` and never include files holding the RCON password. Audit rows record actor, action, target and result, never secret values.

**Outbound connections.** Playkeeper itself contacts Docker Hub (pulling the pinned image) and PaperMC (`fill.papermc.io`: the download check in onboarding, and the Paper download in a setup-only container after the EULA is accepted). The Paper server contacts Mojang (the matching vanilla jar from `piston-data.mojang.com` on its first start, Mojang services for keys and, in online mode, player authentication and allowlist name lookups) and PaperMC's version check when it starts. Playkeeper turns off two defaults that would send data elsewhere: Paper's bStats usage statistics (`plugins/bStats/config.yml` is written with `enabled: false` before every start, including after a restore) and the runtime image's download of default config files from a third-party GitHub repository (`SKIP_DOWNLOAD_DEFAULTS`).
**One-line installer.** `https://playkeeper.io/install` is a redirect (HTTP 302) to `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh`, served by the nginx container in [`site/`](site/README.md); after each release the release workflow checks that it resolves to the new `get.sh`. Whoever controls that server or the domain's DNS controls where the redirect points, so the GitHub URL, which skips it, is the one to use if you would rather trust GitHub alone. `get.sh` refuses plain HTTP (redirects included) unless `PLAYKEEPER_ALLOW_HTTP=1` is set for a local test mirror, and runs nothing from the download until the tarball matches its published `.sha256`. Because the checksum comes from the same GitHub release as the tarball, this catches corrupted, truncated or wrong files, not a compromised repository or release. To guard against that, read the script before running it (`curl -fsSL https://playkeeper.io/install | less`) and compare the tarball's SHA-256 with one you obtained another way, or use the tarball steps in the README.

**Supply chain.** The runtime image is pinned by digest; each Paper build is pinned with the SHA-256 published by PaperMC and verified before first run; contributor toolchains are pinned by checksum; CI actions are pinned by commit SHA. Nothing proprietary is shipped (see [docs/THIRD_PARTY.md](docs/THIRD_PARTY.md)).

**Scans (2026-09-24).** `govulncheck` v1.8.0 (Go 1.27.1): 0 vulnerabilities affecting Playkeeper (one advisory for the unmaintained `golang.org/x/crypto/openpgp`, which is not imported). `npm audit` for the UI: 0 vulnerabilities.

**Known limitations.**
- The certificate is self-signed; users must compare the fingerprint printed by the installer on first visit. Publicly trusted certificates are not supported in this release.
- One admin account; no two-factor authentication yet.
- The RCON password is also present in `server.properties` inside the world directory (readable by the game user and root), because the Minecraft server reads it from there.
- `PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1` lets anyone join under any name. It exists only for automated protocol-bot tests; the installer never sets it and the UI shows a permanent red warning when it is on.
- Backups never contain the Paper jar, so a restore downloads it again: restoring needs outbound HTTPS to PaperMC and Mojang.
- Automatic restarts give up after three failures in 15 minutes (crashes or failed starts) and say why; a start the user asked for that fails is not retried until they press Start again.
