# Changelog

Each release's section, headed `## MAJOR.MINOR.PATCH`, is shown in the dashboard as "what changed" when the update is offered, and in the release notes. The release workflow refuses to release a version without a section.

## 0.4.0

- A Plugins tab, called Mods on Fabric, Quilt and NeoForge servers: search Modrinth and Hangar in one list that only shows what works on your server, install with the dependencies it needs, update one or all, and remove with or without its settings. Every download is checked against the checksum its library publishes before it goes in the folder. Plugins you added by hand are listed too, and Playkeeper can take over the ones the library recognises.
- **Pre-generate the map** from a server's World tab so exploring doesn't lag: pick how far out from spawn, with the time and disk space each size takes, then follow its progress, pause, resume or cancel it. It can pause by itself while people are playing, and installs the Chunky plugin or mod the first time.
- **Resource and data packs** on the World tab: offer a resource pack that players download when they join, optionally required and with your own message, served by Playkeeper from the address you opened the dashboard at; and add, switch on or off and remove data packs.
- More server types: Vanilla, Fabric, Quilt, NeoForge and Purpur alongside Paper, each downloaded from its project and checked against the checksum it publishes.
- Modpacks from Modrinth, and from CurseForge with your own free key, added under Settings › Add-on sources: browse them, see what's inside, and create a server from one, with each file checked as it downloads. The key stays on the machine, is shown only by its last four characters, and only the owner can change it.
- Picked by Playkeeper: before you search the Plugins or Mods library, a few hand-picked add-ons that work with your server's type and version, each with what it does and one practical note.
- Proximity voice chat: installing Simple Voice Chat opens the UDP port it needs (24454, or the next free one) on the server and closes it again when you remove it. The dialog says to open it in your provider's firewall too, and gives you the link friends need for the mod.
- The Mods tab says what friends need of each mod you added: Friends need it, Optional for friends or Server only.
- Share with friends, on a modded server's Mods tab: one link to a page that shows friends what to install and gives them a file for the Modrinth App or Prism Launcher, or download that file and send it yourself. It holds names, versions and Modrinth download links only, server-only mods stay hidden, and the link is random and stops working when you stop sharing.
- Share a server as a template, a file or a link with its type, version, settings and add-ons (names and versions only, never files or code), and create a server from one someone shared. The template's data packs download only over HTTPS from public websites and must match their checksums. Anything the template names that can't be installed stays listed on the server's Overview with Try again. A template says who shared it and on which day.
- Each server runs on the Java its Minecraft version was made for, so modpacks for older versions start too. A pack's details say when it runs on an older Java.
- Fixed: the Console showed plugins' § colour codes and hid four-part version numbers, like NeoForge's, as IP addresses; a failed download step still said "Checksum matched"; and the sidebar kept saying Creating after a create failed.

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
