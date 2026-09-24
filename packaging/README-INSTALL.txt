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

Uninstall (keeps your worlds and backups in /var/lib/playkeeper):

     sudo playkeeper uninstall

Useful:
     sudo playkeeper status                  # server state from the agent
     sudo playkeeper setup-code              # new setup code (before an admin exists)
     sudo playkeeper reset-password <user>   # new random admin password

Playkeeper is not an official Minecraft product. Not approved by or
associated with Mojang or Microsoft. Minecraft server software is downloaded
from PaperMC and Mojang on your server only after you accept the Minecraft
EULA; it is not included in this archive.
