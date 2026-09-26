# Playkeeper

**Your VPS. Your game servers. Your worlds.**

Playkeeper is a self-hosted dashboard for running Minecraft Java servers on a Linux VPS you already own, or on several machines: install it, create servers in the browser, add plugins, mods and modpacks, invite friends and co-admins, see real player and server activity, and keep backups on the server and off it. It works on a phone as well as a desktop. To click around first, open the [live demo](https://playkeeper.io/demo/): sample servers and players, running in your browser, nothing real, starting over every hour. Questions and ideas are welcome in [GitHub Discussions](https://github.com/CIYAhq/playkeeper/discussions).

> **Status: v0.4.0, an early release.** **Tested on every change** on fresh GitHub-hosted Ubuntu 24.04 runners: install, onboarding, play with protocol-level test bots, backup, restore on a second runner, the upgrade from the current release and an update from the dashboard with the automatic rollback, certificates from Let's Encrypt's test CA, and every page at desktop and phone width with an accessibility check. **Verified by the owner:** installing on a real provider VPS, and playing there with friends on 0.3.1. **Not yet tried with the real services:** a Discord channel, a free name from Playkeeper's names service, a Let's Encrypt certificate, a CurseForge modpack with the key built into the release, and copies to S3-compatible storage. Keep your own copies of any backup you care about.

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

**How big a VPS?** The [sizing guide](https://playkeeper.io/sizing) answers for how many friends play at once and what you'll run.

**Tested on:** Ubuntu 24.04 LTS, x86_64, systemd, in fresh KVM guests built from the official Ubuntu cloud image (3 GB RAM, 2 vCPU, 20 GB disk) and on GitHub-hosted `ubuntu-24.04` runners. The owner has also installed it on a real provider VPS (see the status above). The installer runs only on x86_64, and refuses other distributions unless you pass `--allow-untested-os`.

**You need:** root (sudo) on the VPS; at least 2 vCPUs (the size that was tested; the installer does not check the count); at least 3 GB RAM (2.3 GB is the hard minimum the installer accepts) and 5 GB free disk (3 GB minimum). Open these in your provider's firewall:

- TCP **8443** for the dashboard;
- TCP **25565** for the first Minecraft server, plus one more from 25566 for each further server;
- UDP **24454** (or the next free port) for each server with voice chat;
- TCP **80** only if you give the dashboard your own domain: Let's Encrypt checks it there while it issues or renews the certificate.

Outbound, Playkeeper needs the Ubuntu archive if it installs Docker, and HTTPS to GitHub, Docker Hub, PaperMC and Mojang and, for the features you use, to Modrinth and Hangar (plugins, mods and modpacks; a Modrinth modpack may also fetch files from GitHub or GitLab), CurseForge, the download sites of Purpur, Fabric, Quilt, NeoForge and Forge, the websites a template's data packs come from, Discord, Let's Encrypt with Playkeeper's names service or public DNS-over-HTTPS (addresses), and your own storage (off-site copies). Docker is installed from Ubuntu's `docker.io` package if missing; an existing Docker is used as it is. To check a server without changing it, extract the tarball as above and run `sudo ./playkeeper-*-linux-amd64/playkeeper preflight`.

The installer checks the server first (changing nothing), lists every change it will make and how to undo it, and asks before continuing. It never takes over an existing Minecraft, Crafty or panel install. When it finishes it prints:

- an `https://<your-ip>:8443/setup#code=…` link with a **one-time setup code** (24 hours), and
- the **SHA-256 fingerprint** of the dashboard's self-signed certificate. Your browser will warn about the certificate; continue only if the fingerprint it shows matches. A name with a real certificate (below) makes the warning go away.

Everything else happens in the browser: create the admin account, pass the check of the VPS, then create your first server or skip it for now. The first server gets the newest stable Paper and a memory size for how you'll play; **Change** picks others. The **Get started** card in the sidebar and **First steps** on each server's Overview walk you through inviting a friend and making and downloading your first backup.

## Servers

**New server** walks through the type, version, play style, memory and name, with the recommended version and a memory size already picked. **Start from** can also begin with a modpack, a template or a world you already have. Each server has its own world, players, backups, settings, game port and share of the machine's memory.

- **Server types:** Paper, Purpur, Vanilla, Fabric, Quilt, NeoForge and Forge, each downloaded from its own project and checked against the checksum it publishes. Each server runs on the Java its Minecraft version was made for.
- **Modpacks:** browse Modrinth, and CurseForge through the key built into the release or your own free key (**Settings › Add-on sources**; the key stays on the machine, and only the owner can change it there). See what's inside, then create the server, with each file checked as it downloads.
- **Templates:** **Share as a template** in a server's menu makes a `.playkeeper-template` file or a `https://playkeeper.io/t#…` link with its type, version, settings and add-ons: names, versions and checksums only, never files, worlds, players or secrets. Anyone with their own Playkeeper can create a server from it.
- **Your own world:** **Start from › A world**, or **Start from your own world** on a server's World tab, takes your singleplayer world or one from Aternos, Minehut, Realms or another host (with steps for getting it out), as up to 16 `.zip`, `.tar.gz` or `.tar` files. The upload carries on where it stopped if the connection drops. Before anything changes, Playkeeper shows what's inside and which Minecraft version will run it; a world that gets upgraded keeps the file you uploaded as a backup.
- **Minecraft version:** **Minecraft version** in a server's Settings offers newer versions. **Back up and update** takes a backup first and puts it back if the server doesn't start. A world opened with a newer version can't go back.

## Plugins, mods and the world

- **Plugins and mods:** the **Plugins** tab (**Mods** on Fabric, Quilt, NeoForge and Forge; Vanilla has neither) searches [Modrinth](https://modrinth.com), and for plugins [Hangar](https://hangar.papermc.io) too, in one list of what works on the server's type and version, starting with a few add-ons **Picked by Playkeeper**. Installing shows what will be added, dependencies included, and checks every file against the checksum its library publishes. Update one or all, remove with or without settings, and take over plugins you added by hand so they get updates too. New and updated plugins load when the server restarts.
- **Voice chat:** installing Simple Voice Chat opens its UDP port (24454, or the next free one) on that server and closes it when you remove it. A template that has it opens the port too; a modpack that brings it opens the port only if you choose **Create and open the port**. Open the port in your provider's firewall too; the dialog gives you the link friends need for the mod.
- **Friends on a modded server:** **Share with friends** on the Mods tab makes one link to a page that shows friends what to install, with a `.mrpack` file for the Modrinth App or Prism Launcher. It shows names and versions only, and server-only mods stay hidden. The link is random and changes when you stop and start sharing.
- **Pre-generate the map:** on the World tab (every server type but Vanilla), builds the land out to 1,000, 2,500, 5,000 or 10,000 blocks from spawn ahead of time, so exploring doesn't lag. Each size shows how long it takes and how much disk it needs. It installs [Chunky](https://modrinth.com/plugin/chunky) the first time, pauses while people play unless you turn that off, and can be paused, resumed or cancelled.
- **Resource and data packs:** on the World tab, `.zip` files up to 250 MB. A data pack goes into the world; a resource pack is offered to every player when they join, optionally required and with your message. Players' games download it from the dashboard's address and port, over HTTPS once the dashboard has a Let's Encrypt certificate for that address and over plain HTTP until then, so add it with the dashboard open at the address players join with.
- **Map:** the **Map** tab (every server type but Vanilla and Forge) shows the world from above, drawn by [squaremap](https://modrinth.com/plugin/squaremap) on your own server, with who's playing and where. **Turn on the map** installs squaremap, which loads with one restart. **Share with a link** gives a link that works without signing in; each new link retires the old one, and player positions stay hidden unless you turn on **Show players on the shared map**.

## Keep it running

- **Backups:** **Back up now** on the World tab copies the world while players stay online: Playkeeper pauses saving only while it copies, then checks the copy file by file. If a plugin keeps writing to the world, **Stop and back up** makes the backup with the server stopped instead.
- **Backup rules:** **World › Backup rules** makes backups by themselves, every few hours or once a day, if you like only when someone played, and keeps every backup from the last hours, then one a day, a week and a month. By default that is everything from the last 24 hours, one a day for 7 days and one a week for 4 weeks. The page shows about how many backups that keeps and how much space they take.
- **Copies somewhere else:** every backup can also go to S3-compatible storage (Backblaze B2, Cloudflare R2, Wasabi and similar) or to another machine over SFTP, encrypted on your server first. Download the recovery key when you turn copies on and keep it off the server: on a new machine, **Restore from a recovery key** on Home finds a server's copies and restores one as a new server.
- **Schedules:** **Schedules** in a server's Settings runs restarts that warn players first, backups and a short list of safe console commands, every day, on chosen days or every few hours, in your time zone. A restart or backup can skip while people play and try again an hour later. Schedules keep running while the dashboard is down.
- **Sleep when nobody's playing:** off until you turn it on in a server's Settings. While another job runs on that server, such as a backup, changing it is refused as busy; save it again once the job is done. After 5 minutes to 4 hours with nobody on (15 minutes by default), the server stops and frees its memory, and players see "Asleep · join to wake it". Joining wakes it in about 30 seconds: a banned player never wakes it; with the allowlist off anyone else can, and with it on only players on the allowlist and operators can. You can also wake it from the dashboard.
- **When something's wrong:** **How it's running**, under a server's Overview, shows the tick rate, tick time, memory and processor use, and what slows the server down, each with one thing to do. When a server crashes or doesn't start, its Overview explains why from its own log and crash report and offers the fix. **Memory** in a server's Settings suggests a size from the last 14 days.
- **Disk space:** the machine's Disk meter opens a page showing what fills the disk and what each server uses, with ways to free space: old backups, logs and crash reports, unused server versions, downloaded add-on files, folders set aside by restores and updates, and unfinished files. Nothing is deleted until you press its button and confirm, old backups and set-aside folders are listed one by one so you pick what goes, and off-site copies stay.

## Friends and your team

- **Invite friends:** **New invite link** on a server's Players tab. A friend opens it, types their Minecraft name and is on the allowlist, right away or once you say yes. A link works for a day, a week, a month or until you turn it off, for a set number of friends, and the Players tab shows who joined with which link.
- **Player pages:** each player's page shows when and how long they play and whether they're on the allowlist or an operator, and lets you message, kick or ban them.
- **Team:** **Settings › Team** invites someone as an Admin, Moderator or Viewer, for every server or only some, with their own sign-in. Admins must use two-factor sign-in; until they turn it on and you confirm it with one click, they have Moderator rights.
- **Discord:** **Settings › Discord** takes a channel's webhook link for alerts (a crash, back online, low disk space, a failed backup, a new Playkeeper or Minecraft version, a join request, and players joining and leaving if you want them) and keeps one live status message with each server and who's playing. It covers the servers on the dashboard's machine; servers on connected machines don't post to Discord. Alerts come from that machine's agent, so a crash is reported even while the dashboard is down.
- **Two-factor sign-in:** **Your account** (your name at the bottom of the sidebar; on a phone, **More ›** your name) › **Turn on**: enter your password, scan the QR code with an authenticator app, type its code and save the ten recovery codes. Five wrong codes in a row pause app codes for a minute, doubling up to 16 minutes; 100 block them until a recovery code is used. If you lose the phone and the codes, run `sudo playkeeper reset-2fa <username>` on the VPS.

## A name for your VPS

**Machine settings › Address** (on a phone: **More ›** your machine **› Address**) gives the dashboard's machine a free `yourname.playkeeper.io` name or your own domain, and the dashboard a Let's Encrypt certificate that renews by itself. With your own domain, add the records Playkeeper lists where you manage the domain (an A record, and an SRV record for each server not on port 25565) and open port 80 for Let's Encrypt's checks; each server then joins at its own address, without a port. A free name comes from Playkeeper's names service: three days after the claim, once the service has reached the dashboard on port 8443, the first five servers get their own address, and until then (and for any further servers) players add the server's port to the name. Servers on connected machines join at that machine's IP address and each server's port. The IP address and its self-signed certificate keep working. Invite, map and pack links use the name once there is one.

## More machines and AI agents

- **Servers on more machines:** **Settings › Machines › Connect another machine** makes a one-line command for a second VPS or a home server (Ubuntu 24.04, at least 2 CPU cores, 3 GB of memory and 5 GB of free disk). On a new machine it installs Playkeeper without a dashboard of its own; on one that already runs Playkeeper, it's `sudo playkeeper join …`. The machine dials out to your dashboard's address and port and reconnects by itself, so no port opens on it for Playkeeper; its servers' game ports need opening there as usual. Its servers show up on Home and in the sidebar with the others. Each code works once, for 30 minutes, and the fingerprint in the command proves the machine found your dashboard. Open the dashboard at its IP address or domain name to get the command, and use `sudo playkeeper leave` on the machine to disconnect it.
- **AI agents:** **Settings › AI agents** shows the dashboard's MCP address and makes a token for each AI tool, such as Claude or Cursor, for 30, 60, 90 or 365 days and for all servers or some. A token has a Viewer's rights (status, players, the console and crashes, and searching for plugins and mods), a Moderator's (also start and stop, backups and the allowlist) or an Admin's (also console commands, and installing and removing plugins and mods), and never more than your own account: it stops working if you're removed from the team, your role is lowered or you lose a server it's for. What agents do shows up there and in the activity log. Over SSH, `sudo playkeeper mcp` serves the same tools with the owner's rights, for that machine's servers.

## Update, upgrade and uninstall

**Update Playkeeper:** when a new release is available, **Update available** appears in the sidebar (under **More** on a phone); it shows what changed and installs it when you click **Update**. Before anything from the download runs, Playkeeper checks that the release's manifest is signed with the Playkeeper release key built into your installed version, and that the download matches the manifest. Only the dashboard and agent restart; the Minecraft servers keep running. If the new version is not healthy within two minutes, the previous version, its services, settings and databases are put back automatically. Playkeeper looks for a new release a minute after it starts and then twice a day, downloads nothing until you click, and never goes back to an older version. Machines connected to your dashboard update from **Settings › Machines**.

**Upgrade to 0.4.0:** install it from the dashboard. A server on a Minecraft version before 26 moves to the Java that version was made for, so its next start downloads that runtime once and takes longer.

**Upgrade from 0.1.0:** 0.1.0 cannot update itself. Run the same one-line install command on the VPS: it sees the installed version, shows what it keeps and replaces, and upgrades in place. Worlds, backups, settings and the admin account are kept, and later updates come from the dashboard. Running the command again later is safe: it does nothing if the version is the same and refuses an older one.

**Uninstall:** `sudo playkeeper uninstall` lists what it removes and asks first: Playkeeper, its services, users, containers and firewall rules, and the Docker packages it installed with the Docker folders that install created, unless other containers still use Docker or you pass `--keep-docker`. On a connected machine it leaves the dashboard first. It keeps your worlds and backups in `/var/lib/playkeeper` (reinstalling picks them up); `--purge` deletes them too and asks you to type a confirmation.

**Recover or move a world:** see [docs/RECOVERY.md](docs/RECOVERY.md). Backups listed in the dashboard live on the same server unless you turn on copies somewhere else; download copies of the ones you care about.

**Other commands:** `sudo playkeeper status`, `sudo playkeeper setup-code` (a new setup code before an admin exists), `sudo playkeeper reset-password <user>`, `sudo playkeeper reset-2fa <user>` (turns off two-factor sign-in for a lost phone), `sudo playkeeper join` and `sudo playkeeper leave` (connect a machine to another dashboard, or disconnect it), and `sudo playkeeper mcp` (the AI tools over SSH).

## Build and contribute

Outside contributions are welcome, under the project's licence; [CONTRIBUTING.md](CONTRIBUTING.md) has the details and the PR checklist. Report security problems privately, as [SECURITY.md](SECURITY.md) describes, not in a public issue.

Stack: one Go binary (root agent on a Unix socket, unprivileged HTTPS panel, installer) with an embedded React/TypeScript UI built on [coss ui](https://coss.com/ui) components, SQLite, and a container per server from a pinned `itzg/minecraft-server` image (one per Java version, so each Minecraft version runs on the Java it was made for). Why: [docs/decisions/0002-stack.md](docs/decisions/0002-stack.md). How it fits together: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

On a stock Ubuntu 24.04 machine:

```bash
sudo apt-get update && sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh    # pinned Go and Node into .tools/, npm ci
make check            # lint, typecheck, Go, web and installer-script unit tests (CI's check job)
make dev              # agent + panel locally at https://localhost:8443 (uses your Docker)
make package          # release tarball, get.sh, the stable-named copy and the (unsigned) release manifest in dist/
make e2e-vm           # the full KVM rehearsal: install, play, backup, restore, one-line install
```

Run `make dev` as your normal user with access to Docker (in the `docker` group), not as root: the server container runs as the calling user. `./scripts/negative-controls.sh` removes each safety guard in turn (in a throwaway worktree) and checks that its test fails.

## Scope of this release

Several Minecraft Java servers of seven types, on one Linux VPS or more machines, with plugins, mods and modpacks, guided setup, authenticated HTTPS management with your own address, a team with roles and two-factor sign-in, real operations and player analytics, schedules, portable backups with rules for what to keep, encrypted copies elsewhere and a guarded restore, in English (every string goes through a translation layer, ready for more languages). Other games, billing, VPS provisioning and taking over a world another panel runs on the VPS come later.

The interface is built from [coss ui](https://coss.com/ui) components (MIT, copied into the repository and restyled) with an original design, mascot and art. [Ghost](https://github.com/haydenbleasel/ghost) is a product reference, not our codebase or hosting model. See [design](docs/DESIGN.md), [licensing](docs/LICENSING.md) and [third-party components](docs/THIRD_PARTY.md).

## Licence

Playkeeper is free software under the GNU Affero General Public License, version 3 only (`AGPL-3.0-only`); see [LICENSE](LICENSE) and [docs/LICENSING.md](docs/LICENSING.md). The licences of the third-party code in the binary are reproduced in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES), which every release tarball includes.

Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft.

[playkeeper.io](https://playkeeper.io) is this repository's `site/` folder, an nginx container ([site/README.md](site/README.md)): `/install` redirects to the latest release's `get.sh`, `/sizing` is the sizing guide, `/t` opens shared server templates in your own dashboard, and `/demo/` is the live demo, the dashboard from `web/` built with sample data (`web/src/demo/`).
