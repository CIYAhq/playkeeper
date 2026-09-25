# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- A name instead of the IP address: **Machine settings › Address** gives your servers a free `yourname.playkeeper.io` name or your own domain. Each server joins at its own address without a port, Home and each server's Overview show it, and the dashboard gets a Let's Encrypt certificate that renews by itself, so the browser warning goes away. The IP address keeps working. A free name needs the dashboard's port 8443 to stay reachable, which the names service checks, and servers get their own addresses three days after the claim. An own domain needs port 80 open while its certificate is issued and renewed; if you use ufw, allow it after this update with `sudo ufw allow 80/tcp`.
- Two-factor sign-in with an authenticator app, turned on under **Account**, with ten recovery codes. Wrong codes pause and then block app codes, the dashboard tells you after signing in when someone entered wrong codes or your recovery codes run low, and `sudo playkeeper reset-2fa <user>` turns it off if you lose your phone.

## 0.3.0

- A new dashboard, for desktop and phone: Home shows every server and who's playing, each server has Overview, Console, Players, World and Settings, and on a phone there are bottom tabs and sheets.
- Run several Minecraft servers on one VPS. **New server** sets one up in five steps and shows how the memory is shared; each server has its own world, players, backups, settings and port. Your server keeps running through the upgrade and becomes the first of them.
- **Get started** steps walk you through inviting a friend and making and downloading your first backup, and setup can skip creating a server.
- Settings in plain words: difficulty, PvP, view distance, game mode, the server list message and icon, and memory, applied with one restart.
- Player faces from each player's own skin, the world's size, the tick rate, a chat warning before a backup stops the server, restoring a backup as a new server, and Ctrl+K (⌘K) to jump anywhere or run an action.

## 0.2.0

- Update Playkeeper from the dashboard: Settings shows new versions and what changed, and installs them in one click. The download must be signed with Playkeeper's release key before anything from it runs, and if the new version does not come up healthy, the previous one is put back automatically. The Minecraft server keeps running during the update.
- Upgrading from 0.1.0: run the one-line installer again. It upgrades in place and keeps your worlds, backups, settings and admin account. After that, updates come from the dashboard.
- Minecraft versions now come live from PaperMC: the latest stable version is preselected, and experimental versions are shown with a warning and need your confirmation. Every Paper download is still checked against the checksum PaperMC publishes.
- Update an existing server's Minecraft version under Settings. Playkeeper takes a backup first and puts it back if the server does not start on the new version. Worlds cannot go back to an older version, so older versions are not offered.

## 0.1.0

- First public release: one-line install on Ubuntu 24.04, HTTPS dashboard, guided setup, player activity, backups and a guarded restore.
