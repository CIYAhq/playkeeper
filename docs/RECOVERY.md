# Recovery runbook: move or restore a world

Use this when a server is lost, when you move to another VPS, or to go back to an earlier state. It is the flow Playkeeper's end-to-end tests run: [CI](../.github/workflows/ci.yml) restores a backup made on one fresh runner on a second one. It has not yet been run on a physically separate machine.

## 1. Get the archive off the old server

- **Panel still works:** World → pick a verified backup → **Download**. Note the SHA-256 shown next to it.
- **Panel does not work but you can SSH in:** archives are in `/var/lib/playkeeper/backups/`, each with a `.sha256` file next to it. Only root can read them, so log in as your normal user, list them with sudo and copy the one you want to your home directory (use the full file name; `*` does not work in a folder only root can read):

  ```bash
  sudo ls -l /var/lib/playkeeper/backups/
  B=/var/lib/playkeeper/backups/playkeeper-world-20260924-183128-0eaf9f.tar.gz   # the name from the list
  sudo install -m 600 -o "$USER" "$B" "$B.sha256" ~/
  ```

  Then, on the computer you copy it to:

  ```bash
  scp YOU@OLD-SERVER:'playkeeper-world-20260924-183128-0eaf9f.tar.gz*' .
  sha256sum -c playkeeper-world-20260924-183128-0eaf9f.tar.gz.sha256
  ```

  Afterwards delete the copies in your home directory on the old server (`rm ~/playkeeper-world-*.tar.gz*`).

- **Server and disk are gone:** only copies you downloaded earlier can help. Archives kept on the server itself are not disaster recovery.

## 2. Install Playkeeper on the new server

Follow the install steps in the [README](../README.md#install-on-your-vps). Open the printed link and create the admin account.

## 3. Restore in the browser

1. **Check server** → Continue. **Minecraft EULA** → tick the box → Continue.
2. **Your server** → choose **Restore a backup** → pick the `.tar.gz` file → **Upload and check**. Every file is checked against its SHA-256 before anything changes; a damaged or foreign file is refused with the reason.
3. Read the preview: world name, Minecraft/Paper version, size, what will happen, and what is not included. Compare the archive SHA-256 with the one from step 1.
4. Press **Restore this world**. Playkeeper downloads Paper for the backup's Minecraft version from PaperMC (the newest stable build, never an older build than the backup was made with), checks it against the SHA-256 PaperMC publishes and starts the server, so the new server needs outbound HTTPS to Docker Hub, PaperMC and Mojang while it restores.
5. When **Your server is ready** appears, give players the new join address. They are still on the allowlist from the backup.

To restore over an existing world instead (World → **Restore…** or upload a file there), you must type `replace <world name>`. Playkeeper first saves a **rollback archive** of the current world; if the restored world fails to start, it puts the previous world back automatically. To undo a restore later, restore that rollback archive.

## What is not restored

- Playkeeper admin accounts and sessions (the new server keeps its own).
- Player analytics, sessions and the audit log from the old server (history starts again on the new one).
- Server jar and libraries (downloaded again, checksum-verified).
- The RCON password (each server generates its own).

Worlds, `server.properties` (without secrets), the allowlist, operators, bans and the `config/` and `plugins/` folders are restored. Paper's bStats usage statistics are switched off again before the restored server starts, whatever the archive says.

## A Playkeeper update went wrong

An update from the dashboard, and the one-line upgrade from 0.1.0, put the previous version back by themselves when the new one is not healthy within two minutes. The Minecraft server keeps running throughout. You only need this section when putting the previous version back also failed: the dashboard says so, or it does not load at all after an update.

1. See what is installed and what the updater did:

   ```bash
   playkeeper version
   sudo systemctl status playkeeper-agent playkeeper-panel
   sudo journalctl -u playkeeper-update -n 100 --no-pager
   ```

   If an update did not finish (`/var/lib/playkeeper/agent/update/applying.json` exists; the one-line installer refuses to run until it is resolved), let the updater finish it or put the previous version back: `sudo systemctl start playkeeper-update.service`. If that does not help, continue with step 2.

2. The copy of the previous version is in `/var/lib/playkeeper/agent/update/previous/`: `snapshot.json` names its version and services, next to its `playkeeper` binary, `config.json`, the systemd units in `units/`, and the databases in `db/` as they were just before the update. Put it back:

   ```bash
   P=/var/lib/playkeeper/agent/update/previous
   sudo cat $P/snapshot.json
   sudo systemctl stop playkeeper-panel playkeeper-agent
   sudo cp -p $P/playkeeper /usr/local/bin/playkeeper
   sudo cp -p $P/config.json /etc/playkeeper/config.json
   sudo sh -c "cp -p $P/units/* /etc/systemd/system/"
   if sudo test -e $P/databases.done; then
     for f in agent/agent.db agent/agent.db-wal agent/agent.db-shm panel/panel.db panel/panel.db-wal panel/panel.db-shm; do
       sudo rm -f /var/lib/playkeeper/$f
       if sudo test -e $P/db/$f; then sudo cp -p $P/db/$f /var/lib/playkeeper/$f; fi
     done
   fi
   sudo systemctl daemon-reload
   sudo systemctl reset-failed playkeeper-agent playkeeper-panel
   sudo systemctl start playkeeper-agent playkeeper-panel
   ```

   Without `databases.done` the update stopped before it copied the databases, so the new version never ran and the databases are as they were. If `snapshot.json` does not list `playkeeper-update.path` (0.1.0 had no updater), also remove the updater: `sudo systemctl disable --now playkeeper-update.path`, then delete `/etc/systemd/system/playkeeper-update.path` and `/etc/systemd/system/playkeeper-update.service` and run `sudo systemctl daemon-reload`.

3. `playkeeper version` shows the previous version again and the dashboard loads. Your worlds and backups were never part of the update.

If a Minecraft version change goes wrong instead, Playkeeper puts back the backup it took first ("Automatic backup before updating from Paper …"). If that also failed, restore that backup from the World page.
