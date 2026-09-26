# Playkeeper

**Your VPS. Your game servers. Your worlds.**

Playkeeper is a self-hosted dashboard for setting up and running Minecraft Java (Paper) servers on a Linux VPS you already own: install it, create servers in the browser, invite friends, see real player and server activity, and keep backups you can restore on another machine. It works on a phone as well as a desktop.

> **Status: v0.3.0, an early release.** **Verified by the owner (on 0.1.0 and 0.2.0):** installing on a real provider VPS, and joining with the official Minecraft client from another network. **Tested on every change** on fresh GitHub-hosted Ubuntu 24.04 runners: install, onboarding, play with protocol-level test bots, backup, restore on a second runner, the upgrade from the current release (which keeps its server running) and an update from the dashboard with the automatic rollback, plus every page at desktop and phone width with an accessibility check; 0.1.0 also passed a fuller rehearsal in fresh KVM guests. **Not yet verified:** a second person joining, the server surviving a reboot of the VPS, restoring a backup on a physically separate machine, and 0.3.0's several servers side by side on a real VPS. Keep your own copies of any backup you care about.

## Install on your VPS

On the VPS:

```bash
curl -fsSL https://playkeeper.io/install | sudo sh
```

`https://playkeeper.io/install` redirects to `get.sh` from the [latest release](https://github.com/CIYAhq/playkeeper/releases/latest). The script downloads `playkeeper-linux-amd64.tar.gz` and its `.sha256` from that release, stops unless the SHA-256 matches, then runs the installer, which asks before changing anything. Installer options go after `sh -s --`, for example `… | sudo sh -s -- --yes --game-port 25566`. To read the script first: `curl -fsSL https://playkeeper.io/install | less`.

If playkeeper.io is unreachable, the same script comes straight from GitHub:

```bash
curl -fsSL https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh | sudo sh
```

To skip the script, download `playkeeper-linux-amd64.tar.gz` and `playkeeper-linux-amd64.tar.gz.sha256` from the [releases page](https://github.com/CIYAhq/playkeeper/releases/latest) to the VPS, then:

```bash
sha256sum -c playkeeper-linux-amd64.tar.gz.sha256 && tar -xzf playkeeper-linux-amd64.tar.gz
sudo ./playkeeper-*-linux-amd64/install.sh
```

**Tested on:** Ubuntu 24.04 LTS, x86_64, systemd, in fresh KVM guests built from the official Ubuntu cloud image (3 GB RAM, 2 vCPU, 20 GB disk) and on GitHub-hosted `ubuntu-24.04` runners. The owner has also installed it on a real provider VPS (see the status above). The installer refuses other distributions and CPUs unless you pass `--allow-untested-os`.

**You need:** root (sudo) on the VPS; at least 2 vCPUs (the size that was tested; one vCPU is untested, and the installer does not check the count); at least 3 GB RAM (2.3 GB is the hard minimum the installer accepts) and 5 GB free disk (3 GB minimum); TCP ports **8443** (panel) and **25565** (the first Minecraft server) free and open in your provider's firewall, plus one more from 25566 for each further server, UDP 24454 (or the next free port) for each server with voice chat, and port **80** if you'll give it your own domain; outbound HTTPS to GitHub, the Ubuntu archive, Docker Hub, PaperMC and Mojang, and for a name to Let's Encrypt and Playkeeper's names service or public DNS-over-HTTPS (see below). Docker is installed from Ubuntu's `docker.io` package if missing; an existing Docker is used as it is. To check a server without changing it, run `sudo ./playkeeper preflight` from the extracted tarball.

The installer checks the server first (changing nothing), lists every change it will make and how to undo it, and asks before continuing. It never takes over an existing Minecraft, Crafty or panel install. When it finishes it prints:

- an `https://<your-ip>:8443/setup#code=…` link with a **one-time setup code** (24 hours), and
- the **SHA-256 fingerprint** of the panel's self-signed certificate. Your browser will warn about the certificate; continue only if the fingerprint it shows matches.

Everything else happens in the browser: create the admin account and pass the check of the VPS, then create your first server or skip it for now. Creating one asks only how you'll play (with friends, creative building or just you, with hardcore and the world type under **More options**) and for the Minecraft EULA; the newest stable Paper release and a memory size that suits the play style are picked for you, and **Change** lets you pick others. Wait until the server is ready, then copy the join address and add your friends' usernames. The **Get started** steps on Home and each server's Overview walk you through inviting a friend and making and downloading your first backup. The versions come from [PaperMC](https://papermc.io); experimental ones are marked and need your confirmation, and every Paper download is checked against the SHA-256 PaperMC publishes for it.

**More servers:** **New server** sets up another one in five steps: game and type, version, play style, memory and name. Each server has its own world, players, backups, settings and game port, and its own share of the VPS's memory, kept even while it's stopped so it can always start. The dashboard shows how the memory is shared before you create it.

**A name instead of the IP:** **Machine settings › Address** (the **Machine settings** button on the machine page; on a phone, **More ›** your machine **› Address**) gives your servers a free `yourname.playkeeper.io` name or your own domain, and the dashboard a real certificate, so the browser warning goes away.
- **Free name:** type a name (3 to 32 lowercase letters, numbers or single dashes) and **Claim** it. Playkeeper's names service points it at the VPS's public IP and adds the DNS record Let's Encrypt checks, so port 80 isn't needed; it takes about a minute. The dashboard is then at `https://yourname.playkeeper.io:8443`, where you sign in once more (sign-in belongs to one address). Every few hours the names service checks that the name still reaches this dashboard on port 8443, so keep that port open. Three days after the claim, once a check has succeeded, each server joins at `<server>.yourname.playkeeper.io` with no port; until then players use `yourname.playkeeper.io` with the server's port. One name per install. **Release it** stops the addresses right away and holds the name for 30 days so nobody else takes over your players. A name stops working after a week in which the dashboard didn't answer the check, or about a month with the VPS offline, until you press **Refresh**. If the names service can't be reached, or has handed out as many certificates as Let's Encrypt allows it for now, Machine settings says so and when Playkeeper tries again; addresses that already work keep working.
- **Own domain:** enter it, add the records Playkeeper lists where you manage the domain (an A record pointing to the VPS, plus an SRV record for each server that isn't on port 25565, each with a copy button), and **Check records**; Playkeeper checks again every minute until they're right. The certificate comes from Let's Encrypt over port 80, which must be open in your provider's firewall while it is issued and renewed. A new install, or an upgrade with the one-line installer, also allows port 80 in ufw when ufw is on; an update from the dashboard can't change ufw, so after one, run `sudo ufw allow 80/tcp` yourself if you use it.

Certificates renew by themselves about a month before they run out; the machine page's **Dashboard certificate** line says which one the dashboard uses. The IP address and its self-signed certificate keep working.

**Two-factor sign-in:** open **Account** (your name at the bottom of the sidebar; **More › Account** on a phone) and **Turn on** two-factor sign-in: enter your password, scan the QR code with an authenticator app (on a phone, **Open authenticator app**) or type the key, type the code it shows, and save the ten recovery codes. From then on, signing in asks for a code after the password. Each recovery code works once, also while app codes are paused. Five wrong codes in a row pause app codes for a minute, doubling up to 16 minutes; 100 block them until a recovery code is used. **Make new codes** and **Turn off** ask for your password and a code. If you lose the phone and the recovery codes, run `sudo playkeeper reset-2fa <username>` on the VPS.

**Backups:** **Back up now** on a server's **World** tab copies the world while players stay online: Playkeeper saves it, pauses saving only while it copies the files, turns saving back on and checks the copy file by file. If saving can't be turned back on, the World tab and Overview say so, with a button that turns it back on, and Playkeeper keeps trying. If a plugin keeps writing to the world or the console doesn't answer, **Stop and back up** makes the backup with the server stopped instead. Changing the Minecraft version and restoring a backup still stop the server.

**When something's wrong:** **How it's running**, under a server's Overview, shows its tick rate, tick time, memory and processor use, and what slows it down, each with one thing to do. When a server crashes or doesn't start, its Overview explains why and offers the fix. **Settings › Memory** suggests a memory size from the last 14 days; a server created before this version starts measuring at its next restart.

**Friends on a modded server:** **Share with friends** on the Mods tab makes one link to a page that shows friends what to install and gives them a `.mrpack` file for the Modrinth App or Prism Launcher. The link is random and changes when you stop and start sharing, and uses the machine's name once it has one (Machine settings › Address). The page shows names and versions only; server-only mods stay hidden.

**Update Playkeeper:** when a new release is available, **Update available** appears in the sidebar (under **More** on a phone); it shows what changed and installs it when you click **Update**. Before anything from the download runs, Playkeeper checks that the release's manifest is signed with the Playkeeper release key built into your installed version, and that the download matches the manifest. Only the dashboard and agent restart; the Minecraft servers keep running. If the new version is not healthy within two minutes, the previous version, its services, settings and databases are put back automatically. Playkeeper looks for a new release a minute after it starts and then twice a day, and downloads nothing until you click. It never goes back to an older version.

**Upgrade from 0.2.0:** install 0.3.0 from the dashboard. Your server keeps running through the upgrade and becomes the first of your servers, with its world, backups, players and settings.

**Upgrade from 0.1.0:** 0.1.0 cannot update itself. Run the same one-line install command on the VPS: it sees the installed version, shows what it keeps and replaces, and upgrades in place. Worlds, backups, settings and the admin account are kept, and later updates come from the dashboard. Running the command again later is safe: it does nothing if the version is the same and refuses an older one.

**Change the Minecraft version:** **Minecraft version** in a server's Settings offers the newer Paper versions for it. When you click **Back up and update**, Playkeeper takes a backup first, and puts it back if the server does not start on the new version. Older versions are not offered: a world opened with a newer Minecraft version cannot go back.

**Pre-generate the map:** **Pre-generate the map** on a server's World tab builds the land out to 1,000, 2,500, 5,000 or 10,000 blocks from spawn ahead of time, so exploring doesn't lag. Each size shows about how long it takes on this VPS and how much disk it needs; a size that doesn't fit the free disk can't be picked. The first time, Playkeeper installs [Chunky](https://modrinth.com/plugin/chunky) and restarts the server to load it. It pauses while people play (unless you turn that off) and continues once the server is empty again, carries on after a restart, and can be paused, resumed or cancelled. When it's done, the World tab shows how many chunks it built, how long it took and how much the world grew.

**Resource and data packs:** **Resource and data packs** on the World tab takes `.zip` files of up to 250 MB. A data pack goes into the world and is switched on right away, or when a stopped server starts; it can be switched off, updated by uploading it again, or removed. A resource pack is offered to every player when they join, if you like with a message and as a condition of joining; a new one reaches players after the server restarts. Their games download it from the dashboard's address and port: over HTTPS when the dashboard has a Let's Encrypt certificate for that address, and otherwise over plain HTTP, as they refuse its self-signed certificate. So add it with the dashboard open at the address players join with.

**Uninstall:** `sudo playkeeper uninstall` removes Playkeeper, its services, users, containers and the Docker packages it installed, with the Docker folders that install created (it names them before asking), and keeps your worlds and backups in `/var/lib/playkeeper` (reinstalling picks them up). `--purge` deletes them too and asks you to type a confirmation.

**Recover or move a world:** see [docs/RECOVERY.md](docs/RECOVERY.md). Backups listed in the panel live on the same server; download copies to keep them safe.

Other commands: `sudo playkeeper status`, `sudo playkeeper setup-code` (new setup code before an admin exists), `sudo playkeeper reset-password <user>`, `sudo playkeeper reset-2fa <user>` (turns off two-factor sign-in for a lost phone).

## Build and contribute

Outside contributions are welcome, under the project's licence; [CONTRIBUTING.md](CONTRIBUTING.md) has the details and the PR checklist. Report security problems privately, as [SECURITY.md](SECURITY.md) describes, not in a public issue.

Stack: one Go binary (root agent on a Unix socket, unprivileged HTTPS panel, installer) with an embedded React/TypeScript UI built on [coss ui](https://coss.com/ui) components, SQLite, and a container per server from a pinned `itzg/minecraft-server` image (one per Java version, so each Minecraft version runs on the Java it was made for). Why: [docs/decisions/0002-stack.md](docs/decisions/0002-stack.md). How it fits together: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

On a stock Ubuntu 24.04 machine:

```bash
sudo apt-get update && sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh    # pinned Go and Node into .tools/, npm ci
make check            # lint, typecheck, Go, web and installer-script unit tests (what CI runs)
make dev              # agent + panel locally at https://localhost:8443 (uses your Docker)
make package          # release tarball, get.sh, the stable-named copy and the (unsigned) release manifest in dist/
make e2e-vm           # the full KVM rehearsal: install, play, backup, restore, one-line install
```

Run `make dev` as your normal user with access to Docker (in the `docker` group), not as root: the server container runs as the calling user. `./scripts/negative-controls.sh` removes each safety guard in turn (in a throwaway worktree) and checks that its test fails.

## Scope of this release

One existing Linux VPS with several Minecraft Java/Paper servers, guided setup, authenticated HTTPS management, real operations and player analytics, portable world backups and a guarded restore, in English (every string goes through a translation layer, ready for more languages). Other server types (shown as coming soon), plugins and mods, other games, more machines, more users, billing, VPS provisioning and migrating an existing production world come later.

The interface is built from [coss ui](https://coss.com/ui) components (MIT, copied into the repository and restyled) with an original design, mascot and art. [Ghost](https://github.com/haydenbleasel/ghost) is a product reference, not our codebase or hosting model. See [design](docs/DESIGN.md), [licensing](docs/LICENSING.md) and [third-party components](docs/THIRD_PARTY.md).

## Licence

Playkeeper is free software under the GNU Affero General Public License, version 3 only (`AGPL-3.0-only`); see [LICENSE](LICENSE) and [docs/LICENSING.md](docs/LICENSING.md). The licences of the third-party code in the binary are reproduced in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES), which every release tarball includes.

Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.

[playkeeper.io](https://playkeeper.io) is this repository's `site/` folder, an nginx container ([site/README.md](site/README.md)); its `/install` redirects to the latest release's `get.sh`.
