# Troubleshooting

The four problems people hit most, with what to check first. If none of this helps, ask in [GitHub Discussions](https://github.com/CIYAhq/playkeeper/discussions) with what you tried.

## Can't reach the dashboard

- Open it at `https://` and port 8443: `https://<your-vps-ip>:8443`. Plain `http://` or the address without the port doesn't answer.
- Allow TCP 8443 in your provider's firewall, the one in its web console. The installer allows it in ufw or firewalld when one is on, but a provider's cloud firewall sits in front of the VPS and needs its own rule. So does another firewall on the VPS itself, such as nftables on Debian: the installer's check names the one it found and the command that allows the port.
- On Oracle Cloud, the VM's Ubuntu image also rejects every port but SSH with its own iptables rules, besides the security list in Oracle's console. Allow the dashboard there too: `sudo iptables -I INPUT -p tcp --dport 8443 -j ACCEPT && sudo netfilter-persistent save`, and the same with `--dport 80` if you give it your own domain.
- On the VPS, `sudo playkeeper status` shows whether Playkeeper's services are running.
- Lost the setup link before creating the admin account? `sudo playkeeper setup-code` makes a new one. Forgot the password? `sudo playkeeper reset-password <user>`.

## Friends can't join

- Allow the game ports in your provider's firewall: TCP 25565 for the first server, and one more from 25566 for each further server. A server with voice chat also needs its UDP port, 24454 or the next free one, and one with crossplay its UDP port, 19132 or the next free one. On Oracle Cloud that is the security list; if friends still can't connect, allow the ports in the VM's iptables as well, as for the dashboard above.
- Friends on Minecraft: Bedrock Edition (phones, tablets, consoles and Windows) can join a Paper or Purpur server with **Bedrock players** on in its Settings. They add the server with the address and port on its Join card, not the server's own address, which Bedrock can't follow. Xbox, PlayStation and Switch players need a workaround such as [BedrockConnect](https://geysermc.org/wiki/geyser/using-geyser-with-consoles/). When Bedrock updates, update Geyser on the Plugins tab: an older Geyser turns newer Bedrock versions away. Add Bedrock friends to the allowlist while the server runs, as `.` then their Xbox gamertag, like `.Steve`.
- They must be on the server's allowlist: send them an invite link from the Players tab, or add their Minecraft name there.
- Copy the join address from the server's Overview. With a free name, each server gets its own address without a port three days after the claim; until then, friends use `yourname.playkeeper.me` with the server's port.
- Free names now end in `.playkeeper.me`. If the dashboard says so when you refresh or change your name, update Playkeeper: it moves your name by itself, and the old `.playkeeper.io` address keeps working for two months.
- On a modded server, everyone needs the same loader, version and mods: **Share with friends** on the Mods tab gives them one link with everything.

## Certificate warning

- Until the dashboard has a name, it uses its own self-signed certificate, so the browser warns. The installer printed the certificate's SHA-256 fingerprint: continue only if the browser shows the same one.
- **Machine settings › Address** gives the dashboard a free `yourname.playkeeper.me` name or your own domain, with a certificate from Let's Encrypt that renews by itself, and the warning goes away. The IP address keeps the self-signed certificate.

## Out of memory

- When a server runs out of memory, its Overview says so and offers more; **Settings › Memory** suggests a size from how much the server needed over the last 14 days.
- The VPS's memory is shared between its servers, and each keeps its share while it's stopped, so a new server may need a bigger VPS or a smaller share for another server.
- For a new VPS, the [sizing guide](https://playkeeper.io/sizing) suggests a size for how many friends play at once and what you run.
