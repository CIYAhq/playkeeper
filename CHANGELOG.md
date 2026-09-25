# Changelog

Each release's section is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses a version without a section.

## 0.2.0

- Update Playkeeper from the dashboard: Settings shows new versions and what changed, and installs them in one click. The download must be signed with Playkeeper's release key before anything from it runs, and if the new version does not come up healthy, the previous one is put back automatically. The Minecraft server keeps running during the update.
- Upgrading from 0.1.0: run the one-line installer again. It upgrades in place and keeps your worlds, backups, settings and admin account. After that, updates come from the dashboard.
- Minecraft versions now come live from PaperMC: the latest stable version is preselected, and experimental versions are shown with a warning and need your confirmation. Every Paper download is still checked against the checksum PaperMC publishes.
- Update an existing server's Minecraft version under Settings. Playkeeper takes a backup first and puts it back if the server does not start on the new version. Worlds cannot go back to an older version, so older versions are not offered.

## 0.1.0

- First public release: one-line install on Ubuntu 24.04, HTTPS dashboard, guided setup, player activity, backups and a guarded restore.
