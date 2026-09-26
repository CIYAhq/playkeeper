# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- Try Playkeeper before you install it: the live demo at playkeeper.io/demo is the dashboard with sample servers, players, console, backups and settings, running in your browser. Nothing in it is real, and it starts over every hour.

## 0.3.1

- Security fix: Playkeeper no longer follows links that a plugin or mod puts in a server's files, which could make it overwrite or read other files on the machine, including its own. If one is in the way, Playkeeper stops and says which file to remove.
- Every button, link, switch and menu item does something, or is greyed out with a short reason. A click-through now checks this on every page, on desktop and phone.
- Fixed: hiding the first steps didn't stick, opening "Older versions" or "Show options" on the New server page crashed it, a finished backup took up to ten seconds to show up on the World tab, and on a phone a server's Settings tab had no heading for screen readers.
- Buttons react when pressed, and pages, tabs, dialogs, sheets, switches, lists and progress bars move smoothly between states. Nothing moves if your device asks for reduced motion.
- While something loads, grey shapes show where it will appear instead of "Loading…".
- Adding or removing a player, making someone an operator and hiding the first steps show at once, and are put back with a short message if Minecraft or Playkeeper says no.
- Less text on every screen: one short line where there was a paragraph.
- Home no longer shows an old player count while the agent isn't answering.
- A server icon over 64 KB is turned down next to the upload button before anything is sent, and the agent refuses one that isn't a 64 × 64 PNG before saving it.

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
