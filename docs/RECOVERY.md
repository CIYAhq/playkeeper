# Recovery runbook: move or restore a world

Use this when a server is lost, when you move to another VPS, or to go back to an earlier state. It is the flow Playkeeper's end-to-end tests run (see [CURRENT_STATE.md](../CURRENT_STATE.md) for where it was verified and what was not).

## 1. Get the archive off the old server

- **Panel still works:** World → pick a verified backup → **Download**. Note the SHA-256 shown next to it.
- **Panel does not work but you can SSH in:** archives are in `/var/lib/playkeeper/backups/`, each with a `.sha256` file next to it:

  ```bash
  sudo ls -l /var/lib/playkeeper/backups/
  scp root@OLD-SERVER:/var/lib/playkeeper/backups/playkeeper-world-*.tar.gz* .
  sha256sum -c playkeeper-world-*.tar.gz.sha256
  ```

- **Server and disk are gone:** only copies you downloaded earlier can help. Archives kept on the server itself are not disaster recovery.

## 2. Install Playkeeper on the new server

Follow the install steps in the [README](../README.md#install-on-your-vps). Open the printed link and create the admin account.

## 3. Restore in the browser

1. **Check server** → Continue. **Minecraft EULA** → tick the box → Continue.
2. **Your server** → choose **Restore a backup** → pick the `.tar.gz` file → **Upload and check**. Every file is checked against its SHA-256 before anything changes; a damaged or foreign file is refused with the reason.
3. Read the preview: world name, Minecraft/Paper version, size, what will happen, and what is not included. Compare the archive SHA-256 with the one from step 1.
4. Press **Restore this world**. Playkeeper downloads the pinned Paper build from PaperMC, verifies its checksum and starts the server.
5. When **Your server is ready** appears, give players the new join address. They are still on the allowlist from the backup.

To restore over an existing world instead (World → **Restore…** or upload a file there), you must type `replace <world name>`. Playkeeper first saves a **rollback archive** of the current world; if the restored world fails to start, it puts the previous world back automatically. To undo a restore later, restore that rollback archive.

## What is not restored

- Playkeeper admin accounts and sessions (the new server keeps its own).
- Player analytics, sessions and the audit log from the old server (history starts again on the new one).
- Server jar and libraries (downloaded again, checksum-verified).
- The RCON password (each server generates its own).

Worlds, `server.properties` (without secrets), the allowlist, operators, bans and the `config/` and `plugins/` folders are restored.
