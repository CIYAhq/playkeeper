# Recovery runbook: move or restore a world

Use this when a server is lost, when you move to another VPS, or to go back to an earlier state. It is the flow Playkeeper's end-to-end tests run: [CI](../.github/workflows/ci.yml) restores a backup made on one fresh runner on a second one. It has not yet been run on a physically separate machine.

## 1. Get the archive off the old server

- **Panel still works:** open the server's **World** tab → pick a verified backup → **Download**. Note its SHA-256 (the backup's **…** menu → **Copy checksum**).
- **Panel does not work but you can SSH in:** archives are in `/var/lib/playkeeper/backups/`, each with a `.sha256` file next to it. Since 0.3.0 their names start with the server's name (`playkeeper-survival-…`); older ones start with the world's (`playkeeper-world-…`). Only root can read them, so log in as your normal user, list them with sudo and copy the one you want to your home directory (use the full file name; `*` does not work in a folder only root can read):

  ```bash
  sudo ls -l /var/lib/playkeeper/backups/
  B=/var/lib/playkeeper/backups/playkeeper-survival-20260924-183128-0eaf9f.tar.gz   # the name from the list
  sudo install -m 600 -o "$USER" "$B" "$B.sha256" ~/
  ```

  Then, on the computer you copy it to:

  ```bash
  scp YOU@OLD-SERVER:'playkeeper-survival-20260924-183128-0eaf9f.tar.gz*' .
  sha256sum -c playkeeper-survival-20260924-183128-0eaf9f.tar.gz.sha256
  ```

  Afterwards delete the copies in your home directory on the old server (`rm ~/playkeeper-*.tar.gz*`).

- **Server and disk are gone:** only what is kept somewhere else can help: backups you downloaded earlier, or the encrypted copies Playkeeper makes from 0.4.0 on (see [Copies somewhere else](#copies-somewhere-else)). Archives kept on the server itself are not disaster recovery.

## 2. Install Playkeeper on the new server

Follow the install steps in the [README](../README.md#install-on-your-vps). Open the printed link and create the admin account.

## 3. Restore in the browser

1. After **Checking this VPS**, choose **Skip for now** instead of creating a server.
2. On Home, open **New server** → **Restore it as a new server** → pick the `.tar.gz` file. Every file is checked against its SHA-256 before anything changes; a damaged or foreign file is refused with the reason.
3. Read the preview: world name, Minecraft/Paper version, size, what will happen, and what is not included. Compare the archive's SHA-256 with the one from step 1. Name the server and accept the Minecraft EULA.
4. Press **Restore as a new server**. Playkeeper downloads Paper for the backup's Minecraft version from PaperMC (the newest stable build, never an older build than the backup was made with), checks it against the SHA-256 PaperMC publishes and starts the server, so the new server needs outbound HTTPS to Docker Hub, PaperMC and Mojang while it restores.
5. When the server shows **Online**, give players the new join address. They are still on the allowlist from the backup.

To restore over an existing world instead (the server's **World** tab → a backup's **…** menu → **Restore this backup…**, or drop a file under **Restore a world**), you must type `replace <world name>`. Playkeeper first saves a **rollback archive** of the current world; if the restored world fails to start, it puts the previous world back automatically. To undo a restore later, restore that rollback archive.

## Copies somewhere else

From 0.4.0 a server can copy every backup to S3-compatible storage or to another machine over SFTP (the server's **World** tab → **Backup rules** → **Copies somewhere else**). Each copy is encrypted on the server before it leaves, and only the server's recovery key file opens it: download it (**Download recovery key**) when you turn copies on and keep it somewhere other than the server, like a password manager. Without it nobody can open the copies, you included.

- **The server still runs:** on its **World** tab, a backup that is only kept somewhere else says **Only on …**. Press **Restore…**: Playkeeper downloads the copy, decrypts it and checks it, then shows the same preview as for any backup. Nothing changes until you confirm.
- **On a new machine:** install Playkeeper (step 2) and choose **Skip for now**, then on Home press **Restore from a recovery key**:
  1. Pick the recovery key file (`playkeeper-recovery-key-<server>.txt`), unchanged.
  2. Enter where the copies are: for S3, the endpoint, bucket, key ID and secret key; for SFTP, the host, port, user and a password (on a new machine Playkeeper signs in with a password, not with the key it made before). The folder comes from the key file. For SFTP, compare the host key fingerprint Playkeeper shows with the one on that machine before you trust it: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` (or the `.pub` file for the key type shown).
  3. Pick a copy and press **Next: check what's inside**. Keep the page open while Playkeeper downloads, decrypts and checks it; then read the preview and go on as in [step 3](#3-restore-in-the-browser).

  Playkeeper saves neither the key nor the storage details from this. Turn copies on again for the new server: it gets a new recovery key, and you keep the old file for the old copies.
- **Without Playkeeper:** copies are [age](https://age-encryption.org) files named after the backup with `.age` added; on S3 they are in the folder the key file names (`playkeeper/<server>/` unless you chose another). Download one, decrypt it with the key file and restore the `.tar.gz` as in step 3:

  ```bash
  age --decrypt --identity playkeeper-recovery-key-survival.txt \
    --output playkeeper-survival-20260924-183128-0eaf9f.tar.gz playkeeper-survival-20260924-183128-0eaf9f.tar.gz.age
  ```

**Make a new key** (the recovery key's **…** menu) when the file may have got out, then download the new file: new copies use only the new key, and the new file holds the older keys too, so it opens every copy. Copies made before still open with the old file, so if it leaked, back up again and delete the older copies.

## What is not restored

- Playkeeper admin accounts and sessions (the new server keeps its own).
- Player analytics, sessions and the audit log from the old server (history starts again on the new one).
- Server jar and libraries (downloaded again, checksum-verified).
- The RCON password (each server generates its own).

Worlds, `server.properties` (without secrets), the allowlist, operators, bans and the `config/` and `plugins/` folders are restored. Paper's bStats usage statistics are switched off again before the restored server starts, whatever the archive says.

## A Playkeeper update went wrong

An update from the dashboard, and the one-line upgrade from 0.1.0, put the previous version back by themselves when the new one is not healthy within two minutes. The Minecraft servers keep running throughout. You only need this section when putting the previous version back also failed: the dashboard says so, or it does not load at all after an update.

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

If a Minecraft version change goes wrong instead, Playkeeper puts back the backup it took first ("Automatic backup before updating from Paper …"). If that also failed, restore that backup from the server's World tab.
