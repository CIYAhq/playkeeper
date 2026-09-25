# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- Backups no longer disconnect anyone: Playkeeper saves the world and pauses saving only while it copies the files, so players stay online. If saving can't be turned back on, the World tab and Overview say so, with a button that turns it back on, and Playkeeper keeps trying. Changing the Minecraft version, restoring a backup and **Stop and back up** still stop the server, and only those warn players in chat first.
- **How it's running**, under a server's Overview: charts of the tick rate, tick time, memory and processor use, and what slows the server down, ranked, each with one thing to do.
- When a server crashes or doesn't start, its Overview explains why from the server's own log and crash report, with the fix one click away: more memory, removing the plugin or mod that failed, restoring a backup, or starting again. If a link or pipe in the server's folder stopped the start, it names the file to remove.
- Settings › Memory suggests a size from how much memory the server needed over the last 14 days. Servers created before this version start measuring at their next restart.

## 0.3.1

- Security fix: Playkeeper no longer follows links that a plugin or mod puts in a server's files, which could make it overwrite or read other files on the machine, including its own. If one is in the way, Playkeeper stops and says which file to remove.

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
