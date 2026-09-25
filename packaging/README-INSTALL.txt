Playkeeper — install on your own Linux VPS
==========================================

Tested on: Ubuntu 24.04 LTS, x86_64, systemd, 3 GB RAM or more (see the
project README for exactly how and where it was tested).

1. Check (optional; changes nothing):

     sudo ./playkeeper preflight

2. Install (shows every change and asks before doing anything):

     sudo ./install.sh

   When it finishes it prints an https:// link with a one-time setup code
   and the certificate fingerprint to compare in your browser.

3. Open the link, create your admin account, accept the Minecraft EULA and
   start your server. Everything after this happens in the browser.

Already running Playkeeper? The same installer upgrades it in place and
keeps your worlds, backups, settings and admin account. From 0.2.0 on,
Settings in the dashboard installs new releases (signed ones only) and puts
the previous version back if the new one is not healthy.

Uninstall (keeps your worlds and backups in /var/lib/playkeeper):

     sudo playkeeper uninstall

Useful:
     sudo playkeeper status                  # server state from the agent
     sudo playkeeper setup-code              # new setup code (before an admin exists)
     sudo playkeeper reset-password <user>   # new random admin password

Playkeeper is free software under the GNU Affero General Public License,
version 3 (see LICENSE). Source code: https://github.com/CIYAhq/playkeeper
The licences of the third-party code in the playkeeper binary are in
THIRD_PARTY_NOTICES.

Playkeeper is not an official Minecraft product. Not approved by or
associated with Mojang or Microsoft. Minecraft server software is downloaded
from PaperMC and Mojang on your server only after you accept the Minecraft
EULA; it is not included in this archive.
