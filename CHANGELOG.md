# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- A Plugins tab, called Mods on Fabric, Quilt and NeoForge servers: search Modrinth and Hangar in one list that only shows what works on your server, install with the dependencies it needs, update one or all, and remove with or without its settings. Every download is checked against the checksum its library publishes before it goes in the folder. Plugins you added by hand are listed too, and Playkeeper can take over the ones the library recognises.
- **Pre-generate the map** from a server's World tab so exploring doesn't lag: pick how far out from spawn, with the time and disk space each size takes, then follow its progress, pause, resume or cancel it. It can pause by itself while people are playing, and installs the Chunky plugin or mod the first time.
- **Resource and data packs** on the World tab: offer a resource pack that players download when they join, optionally required and with your own message, served by Playkeeper from the address you opened the dashboard at; and add, switch on or off and remove data packs.
- Start a new server from a world you already have: your singleplayer world, or one from Aternos, Minehut, Realms or another host, with steps for getting it. The upload carries on where it stopped if the connection drops, and Playkeeper shows what's inside and which Minecraft version it will run before anything changes. An upgraded world keeps the file you uploaded as a backup.
- A Map tab on Paper servers: see the world from above, drawn by squaremap on your own server, with who's playing and where. Share it with a link that works without signing in; each link is random, stops working when you switch sharing off, and shows no player positions unless you turn that on.

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
- If a restore is interrupted, for example by a power cut or a restart of Playkeeper, Playkeeper finishes it when it starts again: it keeps the restored world if it starts, and otherwise puts the previous world and its settings back. It never starts the server on an empty world in the meantime. This also covers a restore Playkeeper 0.3.0 was in the middle of when you upgraded.
- The **World** tab shows a world a restore left behind, such as a restored world that did not start, with a button to discard it and free the space.
- A backup, restore or Minecraft update that has to refuse the world, for example because a file's name is too long for a restore, now says so before stopping the server, so nobody is disconnected for nothing.
- `playkeeper uninstall` names the Docker folders it removes, and no longer shows an empty services line when it's run a second time.
- Restoring a backup as a new server no longer warns that you must accept the Minecraft EULA once you've ticked its box.
- The one-line installer also stops if the `.sha256` file names another file or none, or if it is over 1 MB or the tarball over 200 MB, before running anything from the download.
- Uploading a backup from the dashboard works again. Since 0.3.0, every file was turned down as too big before it was sent.

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
