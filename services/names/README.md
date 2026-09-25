# names.playkeeper.io

This folder deploys the names service behind free `yourname.playkeeper.io` addresses for Playkeeper servers. A Playkeeper install claims a name with a request signed by its own key, and the service points the name at the public IP address the request came from, by managing DNS records in the `playkeeper.io` zone at Cloudflare. Players join at `yourname.playkeeper.io`, and the server's dashboard gets a real certificate for the same address. The code is in `cmd/playkeeper-names` and `internal/names/service`; it is not part of the Playkeeper release.

For a name like `alice`, the service manages these records:

| Record | Type | What it is for |
| --- | --- | --- |
| `alice.playkeeper.io` | A, and AAAA for servers on IPv6 | the server's public address: the join address and the dashboard address |
| `_acme-challenge.alice.playkeeper.io` | TXT | a few minutes while the server gets its dashboard certificate from Let's Encrypt; removed after an hour at the latest |
| `_minecraft._tcp.alice.playkeeper.io`, `_minecraft._tcp.survival.alice.playkeeper.io` | SRV | lets players join a server on another port without typing the port; at most 5 per name |

Each of them is **DNS only**, has a 60-second TTL and carries the comment `playkeeper-names alice`. The service only ever changes or removes a record that has one of these forms under a claimed name **and** carries that name's comment. It never touches `playkeeper.io` itself, `www`, `names`, mail records, reserved names or records you add by hand. CI tests this on every pull request.

| Path | Answer |
| --- | --- |
| `/` | one line about the service, with a link to its source code |
| `/healthz` | `200` with `ok`, for health checks |
| `/v1/ip` | the address the service sees you at, as JSON |
| `/v1/names/<name>` | whether a name is free, as JSON |
| other `/v1/` paths | requests signed by a Playkeeper install only |

`Dockerfile` builds the image (Go and Alpine, pinned by digest; the service runs as user 10001 on port 8080) with a health check on `/healthz`. It is built from the repository root, and `Dockerfile.dockerignore` limits the build to the files it needs. CI builds the image and checks it on every pull request with `scripts/names-check.sh`.

## Set it up

You need the Coolify server that hosts playkeeper.io (see `site/README.md`), a free Cloudflare account and your Namecheap login. Do the steps in order: the service only starts once the zone is at Cloudflare.

### 1. Move playkeeper.io's DNS to Cloudflare

The domain stays registered at Namecheap; only its DNS moves to Cloudflare.

1. At Namecheap, open **Domain List** → **Manage** → **Advanced DNS** and keep the page open: it lists every record the domain has now. If **DNSSEC** is on there, turn it off, and do not change the nameservers (step 4) until `dig +short DS playkeeper.io` prints nothing, usually a day or two later. Otherwise the domain stops working for many visitors.
2. In Cloudflare, open **Domains** → **Onboard a domain**, enter `playkeeper.io`, keep the quick scan for DNS records, and choose the **Free** plan.
3. Cloudflare shows the records it found. Make them match the Namecheap page:
   - Add any record Cloudflare missed, and delete any that Namecheap does not have, such as a `*` record.
   - Set **Proxy status** to **DNS only** (grey cloud) on the `@` and `www` records, so the site keeps working exactly as now: straight to your server, with Coolify's certificate.
   - Keep the email records (MX, and TXT records starting with `v=spf1`) if there are any. Namecheap's free Email Forwarding only works while the domain uses Namecheap's DNS; if you use it, set up Cloudflare's **Email Routing** after step 5 instead.
4. Cloudflare then shows two nameservers, such as `ada.ns.cloudflare.com` and `bob.ns.cloudflare.com`. At Namecheap, open **Domain List** → **Manage**, set **Nameservers** to **Custom DNS**, enter the two names exactly, and select the green check mark.
5. Wait until Cloudflare shows the domain as **Active**; it also sends an email. This usually takes less than an hour and can take a day. Then check that the site still works:

   ```bash
   dig +short NS playkeeper.io              # the two Cloudflare nameservers
   curl -sI https://playkeeper.io/install   # still a 302 to get.sh
   ```

6. If you turned DNSSEC off in step 1: in Cloudflare, open **DNS** → **Settings**, select **Enable DNSSEC**, and add the DS record it shows at Namecheap under **Advanced DNS** → **DNSSEC**.

### 2. Create the API token and copy the zone ID

1. In Cloudflare, open playkeeper.io's **Overview** page and copy the **Zone ID** from the **API** section: 32 characters, `0-9` and `a-f`.
2. Open **My Profile** → **API Tokens** → **Create Token**, and select **Use template** next to **Edit zone DNS**.
3. Set:
   - **Permissions**: keep **Zone** · **DNS** · **Edit**, then select **+ Add more** and add **Zone** · **Zone** · **Read**.
   - **Zone Resources**: **Include** · **Specific zone** · **playkeeper.io**.
   - **Client IP Address Filtering**: **Is in**, your server's public IPv4 address, plus its IPv6 network (such as `2001:db8:1:2::/64`) if it has IPv6. The token then only works from your server.
   - **TTL**: leave it empty.
4. Select **Continue to summary**, then **Create Token**, and copy the token. Cloudflare shows it only once. Put it only into Coolify (step 4), never into files, chats or tickets.

### 3. Add the names record

In Cloudflare, open **DNS** → **Records** → **Add record** and add:

| Type | Name | IPv4 address | Proxy status |
| --- | --- | --- | --- |
| A | `names` | your server's public IPv4 address | **DNS only** |

It must be **DNS only**: the service has to see each Playkeeper server's own address, and behind Cloudflare's proxy it would only see Cloudflare's. If the record is proxied, the service refuses claims and says why in its log. Leave out an AAAA record for now; step 5 shows when to add one.

### 4. Create the application in Coolify

1. In Coolify, open your project and its environment, select **+ New** → **Public Repository**, paste `https://github.com/CIYAhq/playkeeper` and select **Check Repository**.
2. Fill in:
   - **Branch**: `main`
   - **Build Pack**: **Dockerfile**
   - **Base Directory**: `/` (the whole repository: the image is built from its Go code)
   - **Dockerfile Location**: `/services/names/Dockerfile`
   - **Ports Exposes**: `8080`
3. Select **Continue**. In **Configuration** → **General**, set **Domains** to `https://names.playkeeper.io` and select **Save**.
4. In **Persistent Storage**, select **+ Add** → **Volume Mount**, set **Name** to `names-data` and **Destination Path** to `/data`, and save. The database of who owns which name lives there; without the volume, every deploy forgets all names.
5. On the server, print the network of Coolify's proxy:

   ```bash
   docker network inspect coolify --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}'
   ```

   It prints something like `10.0.1.0/24`.
6. In **Environment Variables**, add these three. On each, untick **Available at Buildtime** (older Coolify versions call it **Build Variable?**): the service reads them only when it runs, and build variables can end up in the image.

   | Name | Value |
   | --- | --- |
   | `NAMES_CLOUDFLARE_API_TOKEN` | the token from step 2 |
   | `NAMES_CLOUDFLARE_ZONE_ID` | the zone ID from step 2 |
   | `NAMES_TRUSTED_PROXIES` | what the command in step 5 printed |

7. Select **Deploy** and wait until the deployment log says it has finished. The first build takes a minute or two and needs about 1 GB of free memory.

The image has its own health check, so Coolify's **Healthcheck** can stay off; if you turn it on, use port `8080` and path `/healthz`. The other settings have defaults that suit a start ([Limits](#limits) explains them):

| Name | Default | Meaning |
| --- | --- | --- |
| `NAMES_MAX_NAMES_PER_KEY` | `1` | names one install may hold (1 to 100) |
| `NAMES_CLAIMS_PER_DAY` | `30` | new names per day, everyone together (1 to 100000) |
| `NAMES_RECORD_RESERVE` | `10` | DNS records new names must leave free in the zone (0 to 100000) |
| `NAMES_BLOCKLIST_FILE` | none | a file of names nobody may have (see [Blocking names](#blocking-names)) |
| `NAMES_BASE_DOMAIN` | `playkeeper.io` | the zone names live in; Playkeeper installs use `playkeeper.io` |

Leave `NAMES_DATA_DIR` (`/data`) and `NAMES_LISTEN` (`:8080`) at their defaults; the image is built around them.

### 5. Check it

On your computer:

```bash
curl -s https://names.playkeeper.io/healthz        # ok
curl -s https://names.playkeeper.io/v1/ip          # {"ip":"<your public IPv4 address>","family":"ipv4","public":true}
curl -s https://names.playkeeper.io/v1/names/www   # ... "available":false,"code":"name_reserved" ...
```

`/v1/ip` must show your own public address with `"public":true`. An address starting with `10.`, `172.` or `192.168.` means the service sees Coolify's proxy instead of you: check `NAMES_TRUSTED_PROXIES`. Any other address that is not yours means the `names` record is proxied: make it **DNS only**.

In Coolify, the application's **Logs** should show `playkeeper-names is listening` and no line with `level=ERROR`. If the token, its IP filter or the zone ID is wrong, the service stops at start and the log names the setting to fix; for the IP filter, Cloudflare's message includes the address it saw.

**IPv6 (optional).** If the server has a public IPv6 address and your computer has IPv6 too, ask the service over the server's IPv6 address (put it between the brackets):

```bash
curl -s -6 --resolve 'names.playkeeper.io:443:[2001:db8:1:2::10]' https://names.playkeeper.io/v1/ip
```

If it shows your computer's IPv6 address with `"public":true`, add an AAAA record for `names` (**DNS only**) with the server's IPv6 address. If it shows anything else, or fails, leave it out: Docker then hands IPv6 visitors to the proxy from an internal address, so the service could not see who is asking. Without the AAAA record, names get IPv4 addresses only, which is what nearly every player uses.

Once a Playkeeper version with free addresses is out, claim a name from its dashboard: the record appears under **DNS** → **Records** in Cloudflare with the comment `playkeeper-names <name>`.

## Running it

### Updating

When a change to the service is on `main`, select **Deploy** again in Coolify; an application added by repository URL is not redeployed on its own. Names and their records survive a deploy. For new Go or Alpine versions, change the tag and the digest on both `FROM` lines of `Dockerfile`, then check and redeploy.

### Backups

Every day the service writes a snapshot of its database to `/data/backups/names-YYYY-MM-DD.db` and keeps the last 7. Keep copies off the server as well: without the database, the service no longer knows which install owns which name, and anyone could take over a name that is in use. To find the files on the server:

```bash
docker volume ls | grep names-data                                          # the volume's full name
ls "$(docker volume inspect <volume> --format '{{.Mountpoint}}')/backups"
```

To restore one, **Stop** the application in Coolify, run this in the volume's directory with the snapshot's date, then select **Deploy**:

```bash
cp backups/names-2026-01-31.db names.db && rm -f names.db-wal names.db-shm && chown 10001:10001 names.db
```

Names claimed after that snapshot are unknown to the service again; their servers can claim them again.

### Rotating the token

Create a new token as in step 2, replace the value of `NAMES_CLOUDFLARE_API_TOKEN` in Coolify, select **Redeploy** and check the log. Then delete the old token under **My Profile** → **API Tokens**. If the token may have leaked, delete it first: names that already work keep working while the service waits for the new token.

### If the service is down

Names that already work keep working: their records live at Cloudflare. Meanwhile, installs cannot claim names, move a name to a new address, or get and renew dashboard certificates; certificates are renewed weeks before they expire, so an outage of a few days does no harm. A name lapses only after 30 days without a refresh, and only a running service lapses it. When Cloudflare is down or rate-limits the service, changes are retried: after a minute at first, then less often, down to once an hour.

### Records made by hand

Add your own records anywhere except under a claimed name (`alice.playkeeper.io`, or anything ending in `.alice.playkeeper.io`). The service never touches them, and a name that has records of your own cannot be claimed. If a hand-made record sits at a claimed name's own address or at one of its server addresses, the service leaves that address alone and logs `has a hand-made … record` until you delete it. Never put the comment `playkeeper-names …` on records of your own.

### Blocking names

Beyond the built-in list (`www`, `names`, `api`, mail names, everything containing `playkeeper`, and more: see `reserved` in `internal/names/service/limits.go`), names nobody may claim go into a blocklist. In Coolify, open **Persistent Storage** → **+ Add** → **File Mount**, set **Destination Path** to `/config/blocklist` and write one name per line as the content (`#` starts a comment). Then add `NAMES_BLOCKLIST_FILE` with the value `/config/blocklist` and redeploy. The service rereads the file within a minute of a change and logs `Loaded the blocklist`; if that line does not appear after you edit the list, redeploy. Listed names cannot be claimed, and a listed name that is in use is released and its records removed.

### Zone full

Cloudflare's Free plan allows 200 DNS records in the zone, your own and Email Routing's included. A name uses one record (two with IPv6), plus one per server address, plus a TXT record for a few minutes while a certificate is renewed. The service keeps `NAMES_RECORD_RESERVE` records (10) free for you: a new name or server address that would dip into them is refused, and the log says `The zone is almost out of DNS records`. Records of released and lapsed names are removed on their own. For more room, move the zone to Cloudflare's Pro plan (3,500 records).

### Let's Encrypt limits

Each dashboard at a name gets its own certificate, and Let's Encrypt counts them all against playkeeper.io: at most 50 new certificates per 7 days for the whole domain, refilling one every 202 minutes. Renewals do not count. In a week with more than 50 new names, the rest get their certificates days later. To avoid that, lower `NAMES_CLAIMS_PER_DAY` to `7`, or ask Let's Encrypt to raise the limit for playkeeper.io with their [rate limit form](https://isrg.formstack.com/forms/rate_limit_adjustment_request), which takes a few weeks. If you ever add CAA records to the zone, include `letsencrypt.org`.

### The token can edit the whole zone

Cloudflare cannot limit a token to some of a zone's records, so this token can change every playkeeper.io record, including `@` and `www`: the website and the `/install` address the one-line installer downloads from. The service's guard keeps the service itself away from them; the risk is someone else getting the token. That is why the token only works from your server (step 2), lives only in Coolify's environment variables and not in the image, and never appears in the service's log. Cloudflare's **Audit Log** (**Manage Account** → **Audit Log**) lists every change made with it. To rule the risk out completely, free names would need a separate domain in its own Cloudflare zone, with a token for that zone only; that takes a Playkeeper release that uses the other domain.

## Limits

These keep one install, one address or a flood of claims from using up the zone. The ones with a setting can be changed in Coolify (step 4).

| What | Limit | Setting |
| --- | --- | --- |
| A name | 3 to 32 characters: `a-z`, `0-9` and single hyphens inside; not reserved or blocked | |
| Names per install | 1 | `NAMES_MAX_NAMES_PER_KEY` |
| New names per day, everyone together | 30 | `NAMES_CLAIMS_PER_DAY` |
| New names per day from one IPv4 address or IPv6 /56 | 3 | |
| Requests from one IPv4 address or IPv6 /64 | 60 at once, then 120 an hour | |
| Signed requests from one install | 30 at once, then 60 an hour | |
| Server addresses (SRV records) per name | 5 | |
| Certificate challenge records per name | 2 at a time, each removed after an hour at the latest | |
| DNS records kept free in the zone | 10 | `NAMES_RECORD_RESERVE` |
| A name that is not refreshed | records removed after 30 days (its install can still refresh it); free for others 60 days later | |
| A released name | held for 30 days; only its own install can take it back | |
| A signed request | within 5 minutes of the service's clock, used once, body up to 4 KiB | |

## Try it on your computer

From the repository root:

```bash
scripts/names-check.sh          # builds the image and checks it without contacting Cloudflare (needs Docker)
go test ./internal/names/...    # the client and the service, against a fake Cloudflare
```
