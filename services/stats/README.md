# stats.playkeeper.io

This folder deploys the stats service behind Playkeeper's anonymous [usage stats](../../README.md#usage-stats): installs report how the install went, and running machines send a heartbeat twice a day, so the project can count installs, running machines and their servers, how far their setup got, and whether they came through playkeeper.io or not. A machine can also ask it whether its Minecraft port answers from the internet ([Checking a machine's port](#checking-a-machines-port)). The code is in `cmd/playkeeper-stats`, `internal/usage/service` and `internal/usage` (what a report may say, which both sides check); it is not part of the Playkeeper release.

It runs apart from the [names service](../names/README.md) on purpose: it holds no Cloudflare token and needs nothing from it, so neither can harm the other.

| Path | Answer |
| --- | --- |
| `/` | one line about the service, with links to what installs send and to its source code |
| `/healthz` | `200` with `ok`, for health checks |
| `POST /v1/install` | an installer's report: `started`, `succeeded`, `failed` or `refused` |
| `POST /v1/heartbeat` | a running machine's heartbeat |
| `POST /v1/reach` | whether the Minecraft port of the machine asking answers from the internet (see [Checking a machine's port](#checking-a-machines-port)) |
| `POST /v1/site` | a copy of the install command on playkeeper.io, from its pages alone |
| `GET /v1/summary` | the counts, as JSON, with `Authorization: Bearer <read token>` only (see [Reading the numbers](#reading-the-numbers)) |
| `/dashboard` | the counts as charts, once you sign in with the read token (see [The dashboard](#the-dashboard)) |

**What it keeps.** One row per install ID with what its reports last said, to the hour, and one row per install per day it sent a heartbeat; and for playkeeper.io, how many times its install command was copied each hour, by the channel's code. Nothing else: no IP address, no header, no user agent, no field a report type doesn't have. The address a request comes from decides the rate limits, in memory, and is dropped; a port check also connects back to it, and nothing of the check is kept either. The service keeps no access log, and drops net/http's own log lines, some of which name the client. An install it hears nothing from for 400 days is forgotten. `scripts/stats-check.sh` checks in CI that neither its files nor its log hold the address a report or a port check came from.

`Dockerfile` builds the image (Go and Alpine, pinned by digest; the service runs as user 10001 on port 8080) with a health check on `/healthz`. It is built from the repository root, and `Dockerfile.dockerignore` limits the build to the files it needs.

## Set it up

You need the Coolify server that hosts playkeeper.io (see `site/README.md`) and playkeeper.io's DNS at Cloudflare. It takes about ten minutes.

### 1. Add the stats record

In Cloudflare, open **playkeeper.io** → **DNS** → **Records** → **Add record** and add:

| Type | Name | IPv4 address | Proxy status |
| --- | --- | --- | --- |
| A | `stats` | your server's public IPv4 address | **DNS only** |

**DNS only**, like `names`: behind Cloudflare's proxy every machine would reach the service from one of Cloudflare's addresses, and share its rate limit.

### 2. Make the read token

On your computer, run:

```bash
openssl rand -hex 32
```

Keep the 64 characters it prints for steps 4 and 6: they open the counts. Put them only into Coolify and Cursor's secrets, never into files, chats or tickets.

### 3. Create the application in Coolify

1. In Coolify, open your project and its environment, select **+ New** → **Public Repository**, paste `https://github.com/CIYAhq/playkeeper` and select **Check Repository**.
2. Fill in:
   - **Branch**: `main`
   - **Build Pack**: **Dockerfile**
   - **Base Directory**: `/` (the whole repository: the image is built from its Go code)
   - **Dockerfile Location**: `/services/stats/Dockerfile`
   - **Ports Exposes**: `8080`
3. Select **Continue**. In **Configuration** → **General**, set **Domains** to `https://stats.playkeeper.io` and select **Save**.
4. In **Persistent Storage**, select **+ Add** → **Volume Mount**, set **Name** to `stats-data` and **Destination Path** to `/data`, and save. The counts live there; without the volume, every deploy starts from zero.

### 4. Set its variables

1. On the server, print the address of Coolify's proxy on the `coolify` network, as for the names service:

   ```bash
   docker inspect coolify-proxy --format '{{with index .NetworkSettings.Networks "coolify"}}{{.IPAddress}}{{end}}'
   ```

   It prints something like `10.0.1.5`. The service believes only this address when a request says which visitor it passes on, so nobody can pick an address to get round the rate limits.
2. In **Environment Variables**, add these two. On each, untick **Available at Buildtime** (older Coolify versions call it **Build Variable?**): the service reads them only when it runs, and build variables can end up in the image.

   | Name | Value |
   | --- | --- |
   | `STATS_READ_TOKEN` | the token from step 2 |
   | `STATS_TRUSTED_PROXIES` | the address the command above printed |

3. Select **Deploy** and wait until the deployment log says it has finished.

The image has its own health check, so Coolify's **Healthcheck** can stay off; if you turn it on, use port `8080` and path `/healthz`. The other settings have defaults that suit a start:

| Name | Default | Meaning |
| --- | --- | --- |
| `STATS_NEW_INSTALLS_PER_DAY` | `5000` | install IDs the service may hear of for the first time in a day, everyone together (1 to 1000000), so a flood of made-up IDs can't fill the disk |

Leave `STATS_DATA_DIR` (`/data`) and `STATS_LISTEN` (`:8080`) at their defaults; the image is built around them.

### 5. Check it

On your computer, with your token for `<token>`:

```bash
curl -s https://stats.playkeeper.io/healthz     # ok
curl -s https://stats.playkeeper.io/            # playkeeper-stats: anonymous install and usage counts for Playkeeper. ...
curl -s -H 'Authorization: Bearer <token>' https://stats.playkeeper.io/v1/summary | head -c 300   # {"generatedAt":"...","installs":{...
```

In Coolify, the application's **Logs** should show `playkeeper-stats is listening` with `trusted_proxies=1 summary=true`, and no line with `level=ERROR`. A warning that requests come through a proxy `STATS_TRUSTED_PROXIES` doesn't list means Coolify's proxy has a new address, as after a Coolify update: run the command in step 4 again, change the variable and redeploy.

Coolify's proxy writes no access log unless its configuration turns one on. Check **Servers** → your server → **Proxy** → **Configuration** has no `--accesslog` line, and keep it that way: an access log would keep the address of every report, which this service is built not to.

### 6. Let the analytics digest read the numbers

In the Cursor Dashboard, open **Cloud Agents** → **Secrets** and add `PLAYKEEPER_STATS_TOKEN` with the token from step 2, scoped to `CIYAhq/playkeeper`. A secret reaches only agents started after it is added.

The counts stay at zero until installs of 0.4.4 or later arrive: earlier versions send nothing.

### 7. Let the funnel read the site's visitors

The dashboard's funnel starts with playkeeper.io's visitors and demo opens, which the service reads from the site's analytics (Open Analytics) with a read key of its own. Without one, the funnel starts at copies of the install command.

1. In Open Analytics, open the **playkeeper** site, then **Settings** → **API**. In **API keys**, select **Create key**.
2. **Name**: `stats.playkeeper.io`. **Type**: **Read**. Leave **Read analytics** on. Select **Create**.
3. Copy the key it shows (`oa_sk_…`; it is shown only this once) and select **I saved it**.
4. In Coolify, open the stats application → **Environment Variables**, add `STATS_OA_KEY` with the key, and untick **Available at Buildtime**. Select **Save**, then **Redeploy** at the top right.
5. Open the dashboard: the funnel's first two steps show numbers, and its note says when they were read. In the application's **Logs**, the line `playkeeper-stats is listening` says `site_numbers=true`.

The key is used only by the service, only to read Open Analytics, and never reaches a browser. It is a separate key from the analytics digest's, so either can be revoked alone: in Open Analytics, the key's row has a revoke button, and the funnel then says why it can't read the site's numbers.

## The dashboard

Open **https://stats.playkeeper.io/dashboard** and paste the read token from step 2. Leave **Remember on this device** ticked on your own phone or computer; untick it on one you share. It shows:

- machines running today, in the last 7 days and in the last 30 days, with their Minecraft servers, and a chart of the machines running each day;
- installs per day for 30 days, by outcome (succeeded, failed, refused) or by how Playkeeper was fetched; tap a day for its numbers;
- failed installs by the step they stopped at, and refused installs by the check that turned them away;
- for the machines running in the last day, 7 days or 30 days: on or off our domain, versions, systems, CPU (x86 or ARM), address type, servers per machine, how Playkeeper was installed, dashboards and joined machines, the furthest setup step, and channels;
- a funnel for the last day, 7 days or 30 days, from playkeeper.io's visitors to installs that still run: visitors, demo opens, copies of the install command, then installs made with the playkeeper.io command that started, succeeded, and sent a heartbeat in the last day, each with its share of the step before. Each step counts the same window, not the same people, so a step can be larger than the one before it.

It reads the counts again every five minutes while it's open; **Refresh** reads them at once, and **Sign out** removes the token from the browser.

The token stays in your browser: in its local storage when remembered, in the tab's session storage when not. The page sends it only to this service, with each request for the counts. It loads nothing from anywhere else: the service sends it with a Content-Security-Policy that allows only its own files and requests to itself. It sets no cookie, and, like everything else the service answers, its requests aren't logged.

The dashboard is part of the service, so a new version of it arrives with a redeploy ([Updating](#updating)).

## Reading the numbers

```bash
curl -s -H "Authorization: Bearer $PLAYKEEPER_STATS_TOKEN" https://stats.playkeeper.io/v1/summary
```

Or, in Coolify, open the application's **Terminal**, choose its container and run `playkeeper-stats summary`, which reads the database without the token. Both answer the same JSON, counts only, never an install ID:

| Field | What it counts |
| --- | --- |
| `installs.1d`, `.7d`, `.30d`, `.all` | the installer's reports in the last day, 7 days, 30 days, and everything the service keeps |
| `…started`, `succeeded`, `failed`, `refused` | installs whose plan was accepted, that ended well, that failed, or that the check of the machine turned away |
| `…unfinished` | installs that started over an hour ago and never said how they ended: stopped, cut off, or unable to reach the service |
| `…bySource`, `byChannel` | the same, by how Playkeeper got onto the machine, and by the code of a `playkeeper.io/install/<code>` command |
| `…failedSteps`, `refusedChecks` | the steps failed installs stopped at (`services`, `docker`, …) and the checks that turned installs away (`memory`, `port`, …) |
| `active.1d`, `.7d`, `.30d` | machines that sent a heartbeat in the last day, 7 days or 30 days: where Playkeeper runs |
| `…installs` | how many |
| `…onOurDomain`, `offOurDomain`, `unknownSource` | installed with playkeeper.io's command; any other way (`github`, `mirror`, `tarball`, `source`); installed before 0.4.4, which didn't record how |
| `…bySource`, `byChannel`, `byVersion`, `byOS`, `byArch`, `byAddress`, `byKind` | the same machines by each field of their last heartbeat; `byOS` is like `ubuntu 24.04`, `byAddress` is `free`, `own` or `ip`, `byKind` is `dashboard` or `joined` |
| `…byReached` | the same machines by the furthest setup step their heartbeats said: `account` (the dashboard's first account made), `server` (a server came online), `played` (someone played) or `friends` (a second player played); `none` before the first account, or from a version before 0.4.18. A step a later version adds counts under its own name |
| `…servers`, `running`, `serversPerInstall` | their Minecraft servers, those running, and machines by how many servers they have (`0`, `1`, `2`, `3-5`, `6-10`, `11+`) |
| `daily` | each of the last 30 days (UTC), oldest first: machines that sent a heartbeat (`active`), and installs that started, succeeded, failed or were refused that day, in all and by how Playkeeper got onto the machine (`bySource`). An install whose first report was lost counts as started the day it ended |
| `funnel.1d`, `.7d`, `.30d` | `visitors` and `demoOpens` from the site's analytics (`null` without `STATS_OA_KEY`; demo opens are the demo's visitors at its start and those who arrived straight on one of its pages), `commandCopies` on playkeeper.io, then installs made with its command: `started`, `succeeded`, and `stillRunning`, those that succeeded and sent a heartbeat in the last day |
| `site` | `configured` (the service has `STATS_OA_KEY`), `readAt`, when it last read the site's analytics (it reads them again every 15 minutes at most), and `error`, why the latest read failed, if it did; the funnel then keeps the numbers read before |
| `test.started30d`, `test.active7d` | the project's own test installs, left out of everything above: made while a GitHub Actions job ran, by the end-to-end tests' harness, or with `PLAYKEEPER_USAGE_TEST=1`. The project's CI sends nothing, so these stay at zero unless an install forgets `DO_NOT_TRACK=1` |

A machine counts once whatever it sends: `installs` in `active` are machines, not heartbeats. An install whose machine is later uninstalled stays in `installs` and leaves `active` once 30 days pass without a heartbeat.

## Checking a machine's port

A machine can't see its provider's firewall from inside, so its dashboard asks the service whether its Minecraft port answers from the internet: `POST /v1/reach` with `{"port": 25565}` and nothing else, no install ID and no address. The service connects back to the address the request came from, the one the rate limits use, which only Coolify's proxy can name for a request. It answers `{"result": "…"}`:

| Result | What the service found |
| --- | --- |
| `reachable` | a Minecraft server answered its status request |
| `refused` | the port is closed: the connection was refused |
| `timeout` | nothing answered within 5 seconds, as when a firewall drops the connection |
| `unreachable` | the network said the address can't be reached, as when a firewall rejects the connection |
| `not-minecraft` | something else answered on the port |

It connects:

- only to the address the request came from: a request with any field but `port` is refused (`400`);
- only when that address is a public one, never one on a private network, this machine's own, a cloud's metadata service or carrier-grade NAT, nor one like NAT64's or 6to4's that leads on to another address (`403`, `not_public`);
- only on the ports Playkeeper gives Minecraft servers, 25565 to 26564 (`400` for any other).

Then it sends a Minecraft status request, the one a game sends to show a server in its list, and reads no more of the answer than shows it is one: never the players or the server's description. It keeps nothing of a check, in its files or its log: not the address, the port or what answered. Checks have [limits](#limits) of their own, and `scripts/stats-check.sh` checks in CI that a check connects back to the address asking, refuses the rest and leaves no address behind.

Nothing needs setting up: the service connects out from the Coolify server like any program there. The `stats` record is IPv4 only (step 1), so machines ask, and are checked, over IPv4.

## Running it

### Updating

A push doesn't redeploy it: an application added from a public repository URL hears nothing from GitHub. When a change to the service is on `main`, open the application in Coolify and select **Redeploy** at the top right. Coolify builds the new image and swaps it in; the counts survive, and the dashboard keeps you signed in. For new Go or Alpine versions, change the tag and the digest on both `FROM` lines of `Dockerfile`, then check and redeploy.

### Backups

Every day the service writes a snapshot of its database to `/data/backups/stats-YYYY-MM-DD.db` and keeps the last 7. To find the files on the server:

```bash
docker volume ls | grep stats-data                                          # the volume's full name
ls "$(docker volume inspect <volume> --format '{{.Mountpoint}}')/backups"
```

To restore one, **Stop** the application in Coolify, run this in the volume's directory with the snapshot's date, then select **Deploy**:

```bash
cp backups/stats-2026-10-31.db stats.db && rm -f stats.db-wal stats.db-shm && chown 10001:10001 stats.db
```

Reports that came after that snapshot are lost; running machines send their next heartbeat within 12 hours.

### Changing the read token

Make a new one as in step 2, replace `STATS_READ_TOKEN` in Coolify and select **Redeploy**, then replace `PLAYKEEPER_STATS_TOKEN` in Cursor's secrets. The old token stops working with the redeploy, and the dashboard asks you to sign in again with the new one.

### If the service is down

Nothing on anyone's machine notices: the installer waits a few seconds at most for its last report, and a machine that can't send a heartbeat logs one line and tries again 12 hours later. The reports sent meanwhile are lost, so a day's active machines can read low after an outage longer than a few hours. A dashboard that can't have its port checked says so, and shows what the machine sees for itself.

### Limits

| What | Limit | Setting |
| --- | --- | --- |
| Requests from one IPv4 address or IPv6 /64 | 60 at once, then 120 an hour | |
| Reports for one install ID | 6 at once, then 12 an hour | |
| Install IDs heard of for the first time, everyone together | 5000 a day | `STATS_NEW_INSTALLS_PER_DAY` |
| A report | JSON of at most 4 KiB, every field in the form `internal/usage` checks | |
| Port checks from one IPv4 address or IPv6 /64 | one at a time; 5 at once, then 10 an hour, on top of its requests | |
| Port checks, everyone together | 64 at once | |
| A port check | JSON of at most 64 bytes naming the port alone; 5 seconds to connect, then 5 for the answer | |
| Copies of the install command from one address | 10 at once, then 20 an hour | |
| Copies of the install command, everyone together | 20000 a day | |
| An install nothing is heard from | forgotten after 400 days, with its days | |
| Snapshots | one a day, the last 7 kept | |

## Try it on your computer

From the repository root:

```bash
scripts/stats-check.sh           # builds the image and checks what it serves and keeps (needs Docker)
go test ./internal/usage/...     # the reports, the client and the service
```
