# Troubleshooting

The problems people hit most, with what to check first. If none of this helps, ask in [GitHub Discussions](https://github.com/CIYAhq/playkeeper/discussions) with what you tried.

## Can't reach the dashboard

- Open it at `https://` and port 8443: `https://<your-vps-ip>:8443`. Plain `http://` or the IP address without the port doesn't answer. Your machine's name without the port opens the dashboard once **Serve the dashboard on the standard HTTPS port (443)** works (see below), and its public server page otherwise. Port 8443 keeps working either way.
- Allow TCP 8443 in your provider's firewall, the one in its web console. The installer allows it in ufw or firewalld when one is on, but a provider's cloud firewall sits in front of the VPS and needs its own rule. So does another firewall on the VPS itself, such as nftables on Debian: the installer's check names the one it found and the command that allows the port.
- On Oracle Cloud, the VM's Ubuntu image also rejects every port but SSH with its own iptables rules, besides the security list in Oracle's console. Allow the dashboard there too: `sudo iptables -I INPUT -p tcp --dport 8443 -j ACCEPT && sudo netfilter-persistent save`, and the same with `--dport 80` if you give it your own domain.
- On the VPS, `sudo playkeeper status` shows whether Playkeeper's services are running.
- Lost the setup link before creating the admin account? `sudo playkeeper setup-code` makes a new one. Forgot the password? `sudo playkeeper reset-password <user>`.

## The dashboard still has its port

**Machine settings › The dashboard's address** says where **Serve the dashboard on the standard HTTPS port (443)** stands:

- *It takes effect once this machine has an address with a certificate:* give it one in **Machine settings › Address** first.
- *Another program uses port 443*, *is set to use port 443* or *doesn't let Playkeeper use port 443:* a program listens on it, a Docker container publishes it, or a web server such as nginx or Caddy starts with the machine. Playkeeper never takes the port from them, and the dashboard stays at port 8443. To see what listens: `sudo ss -ltnp 'sport = :443'`. Once it's gone, select **Try again**.
- *Open https://… once to finish:* the dashboard listens on port 443, but no browser from outside the VPS has reached it there, and the address keeps its port until one does. Open it once. If it doesn't open, allow TCP 443 in your provider's firewall (on Oracle Cloud also `sudo iptables -I INPUT -p tcp --dport 443 -j ACCEPT && sudo netfilter-persistent save`, and `sudo ufw allow 443/tcp` if ufw is on). A browser on the machine's own network, like a home server's, doesn't count: open it once from outside, such as on a phone using mobile data.
- *Opens on port 443 a few minutes after the machine starts:* for a few minutes after the VPS starts, the port is left to whatever starts with it.

Customers can't sign in with Whop at the new address? Until the Whop app lists it, Playkeeper keeps sending them back through `https://…:8443/api/public/whop/signin/callback`, which keeps working. **Settings › Sell on Whop** shows the redirect URL to add on the app's OAuth tab.

## Friends can't join

- Allow the game ports in your provider's firewall: TCP 25565 for the first server, and one more from 25566 for each further server. A server with voice chat also needs its UDP port, 24454 or the next free one, and one with crossplay its UDP port, 19132 or the next free one. On Oracle Cloud that is the security list; if friends still can't connect, allow the ports in the VM's iptables as well, as for the dashboard above.
- Friends on Minecraft: Bedrock Edition (phones, tablets, consoles and Windows) can join a Paper or Purpur server with **Bedrock players** on in its Settings. They add the server with the address and port on its Join card, not the server's own address, which Bedrock can't follow. Xbox, PlayStation and Switch players need a workaround such as [BedrockConnect](https://geysermc.org/wiki/geyser/using-geyser-with-consoles/). When Bedrock updates, update Geyser on the Plugins tab: an older Geyser turns newer Bedrock versions away. Add Bedrock friends to the allowlist while the server runs, as `.` then their Xbox gamertag, like `.Steve`.
- They must be on the server's allowlist: send them an invite link from the Players tab, or add their Minecraft name there.
- Copy the join address from the server's Overview. With a free name, each server gets its own address without a port three days after the claim; until then, friends use `yourname.playkeeper.me` with the server's port.
- A server on a machine joined to your dashboard joins at its address without a port, under the dashboard's domain, once **Addresses without a port** works. Friends type it as it is: with a port added, or in Bedrock, the name reaches the dashboard's machine instead. Bedrock players use the address and port on the Join card, which is the joined machine's.
- Free names now end in `.playkeeper.me`. If the dashboard says so when you refresh or change your name, update Playkeeper: it moves your name by itself, and the old `.playkeeper.io` address keeps working for two months.
- On a modded server, everyone needs the same loader, version and mods: **Share with friends** on the Mods tab gives them one link with everything.

## The address doesn't open in a browser

- The page lives at the machine's name, so it needs one first: **Machine settings › Address**. The IP address doesn't show it.
- A server on a machine joined to your dashboard has its page at its address without a port, served by the dashboard's machine, once **Addresses without a port** works and the joined machine runs 0.4.14. It opens over HTTPS only with **An address for each server** on, which gives the dashboard a certificate for every server's name; without it, browsers get the page over plain HTTP.
- Each server's **Settings › Public page** says whether browsers reach it, and what holds a port back:
  - Another program listens on port 443 or 80, a Docker container publishes one, or a web server such as nginx or Caddy is set to start with the machine. Playkeeper never takes a port from them. To see what listens: `sudo ss -ltnp 'sport = :443'`. Once it's gone, select **Try again**.
  - For a few minutes after the VPS starts, the ports are left to whatever starts with it.
- Allow TCP 443 and 80 in your provider's firewall, as for the dashboard above; on Oracle Cloud also in the VM's iptables.
- Want the ports for something else, like your own website? Turn off **Show this server** on every server, and **Serve the dashboard on the standard HTTPS port** in Machine settings: Playkeeper gives both ports back at once.
- With the dashboard on port 443, the machine's name opens the dashboard for anyone signed in, and the page, with **Sign in**, for everyone else. Where customers sign in with Whop, it opens the sign-in page instead; each server's own address still opens its page.

## Certificate warning

- Until the dashboard has a name, it uses its own self-signed certificate, so the browser warns. The installer printed the certificate's SHA-256 fingerprint: continue only if the browser shows the same one.
- **Machine settings › Address** gives the dashboard a free `yourname.playkeeper.me` name or your own domain, with a certificate from Let's Encrypt that renews by itself, and the warning goes away. The IP address keeps the self-signed certificate.

## Out of memory

- When a server runs out of memory, its Overview says so and offers more; **Settings › Memory** suggests a size from how much the server needed over the last 14 days.
- The VPS's memory is shared between its servers, and each keeps its share while it's stopped, so a new server may need a bigger VPS or a smaller share for another server.
- For a new VPS, the [sizing guide](https://playkeeper.io/sizing) suggests a size for how many friends play at once and what you run.

## A plugin can't reach something on the VPS

- With **Keep servers away from this machine** on in **Machine settings**, servers can't open connections to the VPS they run on. That keeps plugins and mods away from the dashboard and anything else the VPS runs, and a plugin that uses a database on the same VPS, such as MySQL for LuckPerms or CoreProtect, gets "connection refused". Turn it off there to let servers reach the VPS again; they keep running meanwhile.
- It turns on by itself when you invite a creator, and stays on while you have creators or a creator invite that still works, since their servers mustn't reach your VPS. Run that database on another machine instead.
- Servers never reach the cloud's metadata service. A database on another machine, and the internet, are reachable either way. `sudo playkeeper status` says which applies.
