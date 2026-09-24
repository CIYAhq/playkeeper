# Playkeeper

**Your VPS. Your game servers. Your worlds.**

Playkeeper is a self-hosted dashboard for setting up and running a Minecraft Java (Paper) server on a Linux VPS you already own: install it, create a server in the browser, invite friends, see real player and server activity, and keep a backup you can restore on another machine.

> **Status: v0.1.0, an early release.** The installer, dashboard, backups and restore work in rehearsals on fresh Ubuntu 24.04 KVM guests and fresh GitHub-hosted runners, with protocol-level test bots. **Not yet verified:** a real provider VPS reachable from the internet, an official Minecraft client with a genuine account, and restore on a physically separate machine. Keep your own copies of any backup you care about.

## Install on your VPS

On the VPS:

```bash
curl -fsSL https://playkeeper.io/install | sudo sh
```

`https://playkeeper.io/install` serves `get.sh` from the [latest release](https://github.com/CIYAhq/playkeeper/releases/latest). It downloads `playkeeper-linux-amd64.tar.gz` and its `.sha256` from that release, stops unless the SHA-256 matches, then runs the installer, which asks before changing anything. Installer options go after `sh -s --`, for example `… | sudo sh -s -- --yes --game-port 25566`. To read the script first: `curl -fsSL https://playkeeper.io/install | less`.

If playkeeper.io is unreachable, the same script comes straight from GitHub:

```bash
curl -fsSL https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh | sudo sh
```

To skip the script, download `playkeeper-linux-amd64.tar.gz` and `playkeeper-linux-amd64.tar.gz.sha256` from the [releases page](https://github.com/CIYAhq/playkeeper/releases/latest) to the VPS, then:

```bash
sha256sum -c playkeeper-linux-amd64.tar.gz.sha256 && tar -xzf playkeeper-linux-amd64.tar.gz
sudo ./playkeeper-*-linux-amd64/install.sh
```

**Tested on:** Ubuntu 24.04 LTS, x86_64, systemd, in fresh KVM guests built from the official Ubuntu cloud image (3 GB RAM, 2 vCPU, 20 GB disk) and on GitHub-hosted `ubuntu-24.04` runners. The installer refuses other distributions and CPUs unless you pass `--allow-untested-os`.

**You need:** root (sudo) on the VPS; at least 2 vCPUs (the size that was tested; one vCPU is untested, and the installer does not check the count); at least 3 GB RAM (2.3 GB is the hard minimum the installer accepts) and 5 GB free disk (3 GB minimum); TCP ports **8443** (panel) and **25565** (Minecraft) free and open in your provider's firewall; outbound HTTPS to GitHub, the Ubuntu archive, Docker Hub, PaperMC and Mojang. Docker is installed from Ubuntu's `docker.io` package if missing; an existing Docker is used as it is. To check a server without changing it, run `sudo ./playkeeper preflight` from the extracted tarball.

The installer checks the server first (changing nothing), lists every change it will make and how to undo it, and asks before continuing. It never takes over an existing Minecraft, Crafty or panel install. When it finishes it prints:

- an `https://<your-ip>:8443/setup#code=…` link with a **one-time setup code** (24 hours), and
- the **SHA-256 fingerprint** of the panel's self-signed certificate. Your browser will warn about the certificate; continue only if the fingerprint it shows matches.

Everything else happens in the browser: create the admin account, pass the server check, accept the Minecraft EULA, pick a version and memory (defaults are preselected), wait until the server is ready, then copy the join address and add your friends' usernames.

**Uninstall:** `sudo playkeeper uninstall` removes Playkeeper, its services, users, container and the Docker packages it installed, and keeps your worlds and backups in `/var/lib/playkeeper` (reinstalling picks them up). `--purge` deletes them too and asks you to type a confirmation.

**Recover or move a world:** see [docs/RECOVERY.md](docs/RECOVERY.md). Backups listed in the panel live on the same server; download copies to keep them safe.

Other commands: `sudo playkeeper status`, `sudo playkeeper setup-code` (new setup code before an admin exists), `sudo playkeeper reset-password <user>`.

## Build and contribute

Outside contributions are welcome, under the project's licence; [CONTRIBUTING.md](CONTRIBUTING.md) has the details and the PR checklist. Report security problems privately, as [SECURITY.md](SECURITY.md) describes, not in a public issue.

Stack: one Go binary (root agent on a Unix socket, unprivileged HTTPS panel, installer) with an embedded React/TypeScript UI, SQLite, and one pinned `itzg/minecraft-server` container. Why: [docs/decisions/0002-stack.md](docs/decisions/0002-stack.md). How it fits together: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

On a stock Ubuntu 24.04 machine:

```bash
sudo apt-get update && sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh    # pinned Go and Node into .tools/, npm ci
make check            # lint, typecheck, Go, web and installer-script unit tests (what CI runs)
make dev              # agent + panel locally at https://localhost:8443 (uses your Docker)
make package          # release tarball, get.sh and the stable-named copy in dist/
make e2e-vm           # the full KVM rehearsal: install, play, backup, restore, one-line install
```

Run `make dev` as your normal user with access to Docker (in the `docker` group), not as root: the server container runs as the calling user. `./scripts/negative-controls.sh` removes each safety guard in turn (in a throwaway worktree) and checks that its test fails.

## Scope of this release

One existing Linux VPS, one Minecraft Java/Paper server, guided setup, authenticated HTTPS management, real operations and player analytics, portable world backups and a guarded restore. Other games, multiple servers, modpacks, billing, VPS provisioning and migrating an existing production world come later.

The interface takes *visual inspiration* from [OpenAnalytics](https://github.com/OpenLabs-so/openanalytics) but uses independently written components and original branding. [Ghost](https://github.com/haydenbleasel/ghost) is a product reference, not our codebase or hosting model. See [design](docs/DESIGN.md), [licensing](docs/LICENSING.md) and [third-party components](docs/THIRD_PARTY.md).

## Licence

Playkeeper is free software under the GNU Affero General Public License, version 3 only (`AGPL-3.0-only`); see [LICENSE](LICENSE) and [docs/LICENSING.md](docs/LICENSING.md).

Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.

[playkeeper.io](https://playkeeper.io) is this repository's `site/` folder, published with GitHub Pages; its `/install` is the latest release's `get.sh`.
