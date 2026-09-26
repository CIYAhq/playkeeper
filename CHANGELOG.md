# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- **Backup rules** for each server: automatic backups every few hours or once a day, only when someone played if you like, and which ones to keep: every backup from the last hours, then one a day, one a week and one a month. Playkeeper shows about how many backups that keeps and how much space they take, and removes the rest.
- **Copies somewhere else:** every backup is also copied to S3-compatible storage (Backblaze B2, Cloudflare R2, Wasabi and similar) or to another machine over SFTP, encrypted on your server before it leaves. A connection test runs before copies start, an SFTP machine's host key is confirmed once and a changed key stops copies until you look at it, and a failed copy is tried again.
- The **recovery key** opens the copies: download it when you turn copies on and keep it off the server. Only the owner can see or download it, and each download is in the audit log. **Make a new key** if the file got out; the new file opens older copies too.
- The World tab says where each backup is kept and gets back a copy that is only kept somewhere else. On a new machine, **Restore from a recovery key** on Home finds a server's copies with its key file and restores one as a new server.
- **Disk space**, opened from the machine's Disk meter, shows what fills the disk and what each server uses, with ways to free space: old backups, logs and crash reports, unused server versions, folders set aside by restores and updates, and unfinished backups. Nothing is deleted until you've reviewed it, and off-site copies stay.

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
