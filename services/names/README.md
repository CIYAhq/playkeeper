# names.playkeeper.io

This folder deploys the names service behind free `yourname.playkeeper.io` addresses for Playkeeper servers. A Playkeeper install claims a name with a request signed by its own key, and the service points the name at the public IP address the request came from, by managing DNS records in the `playkeeper.io` zone at Cloudflare. Players join at `yourname.playkeeper.io`, and the server's dashboard gets a real certificate for the same address. The code is in `cmd/playkeeper-names` and `internal/names/service`; it is not part of the Playkeeper release.

For a name like `alice`, the service manages these records:

| Record | Type | What it is for |
| --- | --- | --- |
| `alice.playkeeper.io` | A, and AAAA for servers on IPv6 | the server's public address: the join address and the dashboard address |
| `_acme-challenge.alice.playkeeper.io` | TXT | a few minutes while the server gets its dashboard certificate from Let's Encrypt; removed after an hour at the latest |
| `_minecraft._tcp.alice.playkeeper.io`, `_minecraft._tcp.survival.alice.playkeeper.io` | SRV | lets players join a server on another port without typing the port; at most 5 per install, from 3 days after the claim and once the dashboard has answered (see [Names must answer](#names-must-answer)) |

Each of them is **DNS only**, has a 60-second TTL and carries the comment `playkeeper-names alice`. The service only ever changes or removes a record that has one of these forms under a claimed name **and** carries that name's comment. It never touches `playkeeper.io` itself, `www`, `names`, mail records, reserved names or records you add by hand. CI tests this on every pull request.

| Path | Answer |
| --- | --- |
| `/` | one line about the service, with a link to its source code |
| `/healthz` | `200` with `ok`, for health checks |
| `/v1/ip` | the address the service sees you at, as JSON |
| `/v1/names/<name>` | whether a name is free, as JSON |
| other `/v1/` paths | requests signed by a Playkeeper install only |

The service also connects out: every 6 hours it asks each name's dashboard on port 8443 to prove it is still there (see [Names must answer](#names-must-answer)).

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
5. On the server, print the address of Coolify's proxy on the `coolify` network:

   ```bash
   docker inspect coolify-proxy --format '{{with index .NetworkSettings.Networks "coolify"}}{{.IPAddress}}{{end}}'
   ```

   It prints something like `10.0.1.5`. The service believes only this address when a request says which visitor it passes on, so other containers on the server cannot pretend to be Playkeeper servers somewhere else.
6. In **Environment Variables**, add these three. On each, untick **Available at Buildtime** (older Coolify versions call it **Build Variable?**): the service reads them only when it runs, and build variables can end up in the image.

   | Name | Value |
   | --- | --- |
   | `NAMES_CLOUDFLARE_API_TOKEN` | the token from step 2 |
   | `NAMES_CLOUDFLARE_ZONE_ID` | the zone ID from step 2 |
   | `NAMES_TRUSTED_PROXIES` | the address the command in step 5 printed |

7. Select **Deploy** and wait until the deployment log says it has finished. The first build takes a minute or two and needs about 1 GB of free memory.

The image has its own health check, so Coolify's **Healthcheck** can stay off; if you turn it on, use port `8080` and path `/healthz`. The other settings have defaults that suit a start ([Limits](#limits) explains them):

| Name | Default | Meaning |
| --- | --- | --- |
| `NAMES_MAX_NAMES_PER_KEY` | `1` | names one install may hold (1 to 100) |
| `NAMES_MAX_NAMES_PER_NETWORK` | `3` | names pointing into one IPv4 /24 or IPv6 /48 network, together (1 to 10000) |
| `NAMES_CLAIMS_PER_DAY` | `30` | new names per day, everyone together (1 to 100000) |
| `NAMES_RECORD_RESERVE` | `10` | DNS records the service always leaves free in the zone for you (0 to 100000; see [Zone full](#zone-full)) |
| `NAMES_RECORD_QUOTA` | `200` | the most DNS records the zone may hold; Cloudflare's own quota wins when it is lower (1 to 1000000) |
| `NAMES_NEW_CERTIFICATES_PER_WEEK` | `40` | names that may get their first certificate in 7 days, everyone together (1 to 50; see [Let's Encrypt limits](#lets-encrypt-limits)) |
| `NAMES_ALERT_WEBHOOK_URL` | none | a Discord webhook that gets the service's alerts (see [Alerts](#alerts)) |
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

Coolify's proxy can get a new address when it is recreated, for example by a Coolify update. The service then refuses claims until you update `NAMES_TRUSTED_PROXIES`, and says so in its log with `alert=unlisted_proxy` and the proxy's new address (and on the webhook in `NAMES_ALERT_WEBHOOK_URL`, if you set one). Check the new address with the `docker inspect` command from step 4, change the variable and redeploy.

**Or give the service a network of its own.** Its address then stays the same when the proxy is recreated. Before step 4, open **Servers** → your server → **Destinations** in Coolify and add one named `playkeeper-names`, then choose it as the destination when you create the application. Coolify connects its proxy to every destination's network. On the server, check that the network holds only the proxy and the names service, and print its range:

```bash
docker network inspect playkeeper-names --format '{{range .Containers}}{{.Name}} {{end}}'   # coolify-proxy and the names container, nothing else
docker network inspect playkeeper-names --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}'
```

If `coolify-proxy` is missing from the list, restart the proxy (**Servers** → your server → **Proxy** → **Restart Proxy**) and check again. Set `NAMES_TRUSTED_PROXIES` to the range, such as `10.0.5.0/24`. The service warns at start that it trusts a whole network; that is expected here. Deploy nothing else to this destination.

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

Names that already work keep working: their records live at Cloudflare. Meanwhile, installs cannot claim names, move a name to a new address, or get and renew dashboard certificates; certificates are renewed weeks before they expire, so an outage of a few days does no harm. A name lapses only after 30 days without a refresh, or when its dashboard fails 4 checks in a row after a week without answering, and only a running service lapses it. When Cloudflare is down or rate-limits the service, changes are retried: after a minute at first, then less often, down to once an hour.

### Names must answer

A name stays only while a Playkeeper dashboard answers for it. Every 6 hours the service connects to port 8443 of the name's address (IPv4 first, then IPv6) and asks for

```text
GET https://alice.playkeeper.io:8443/.well-known/playkeeper-names/<random value>
```

The dashboard must answer with that value signed by the key that claimed the name. The service does not check the dashboard's certificate: the signature is the proof. This way names cannot be claimed in bulk and parked, since every name needs a machine that answers for its key.

- A name whose dashboard has not answered for 7 days (since its last answer, or its claim if it never answered), and failed at least the last 4 checks, lapses: its records are removed. Its install gets it back by refreshing the address once the dashboard answers again; the service checks right away when it refreshes.
- A name that lapsed without ever answering is free for others 7 days later. One that did answer is held for 60 days, like a name that was not refreshed.
- Server addresses wait until the dashboard has answered once.

What this means for you:

- The service checks up to 20 names a minute, 8 at once and one per address at a time. The server must allow outgoing connections to port 8443, which it does unless you restricted it.
- If the service itself cannot reach the internet, every check fails. So names lapse for not answering only while at least one name answered within the last day; otherwise none lapse and the log says `alert=liveness_checks`.
- Playkeeper users need port 8443 open to the internet. The dashboard tells them when a check failed and what to open.

### Alerts

When the service runs into something that needs you, it writes a warning to its log with `alert=` and the kind below. Each kind is repeated at most every 6 hours. To get alerts in Discord too:

1. In Discord, open the channel's settings (**Edit Channel**) → **Integrations** → **Webhooks** → **New Webhook**, then select **Copy Webhook URL**.
2. In Coolify, add `NAMES_ALERT_WEBHOOK_URL` with that URL, untick **Available at Buildtime**, and redeploy.

The URL is a secret: anyone who has it can post in the channel. The service accepts only `https://` URLs, never shows the URL in its log, and does not follow redirects. Any webhook that takes Discord's `{"content": "..."}` JSON works.

| Alert | What happened | What to do |
| --- | --- | --- |
| `claims_budget` | the day's `NAMES_CLAIMS_PER_DAY` new names are used up | nothing for a real rush; otherwise look in the log for one network claiming many names |
| `zone_nearly_full` | server addresses are refused to keep room in the zone | see [Zone full](#zone-full) |
| `zone_full` | new names are refused | see [Zone full](#zone-full) |
| `challenges_refused` | certificate challenges are refused: only your reserve is left | see [Zone full](#zone-full) |
| `liveness_checks` | names have not answered for a week, but none lapse because no name answered for a day | check that the server can reach port 8443 on the internet |
| `certificate_budget` | this week's `NAMES_NEW_CERTIFICATES_PER_WEEK` new certificates are used up | see [Let's Encrypt limits](#lets-encrypt-limits) |
| `unlisted_proxy` | requests come through a proxy that `NAMES_TRUSTED_PROXIES` does not list | update the setting as [Check it](#5-check-it) describes |

### Records made by hand

Add your own records anywhere except under a claimed name (`alice.playkeeper.io`, or anything ending in `.alice.playkeeper.io`). The service never touches them, and a name that has records of your own cannot be claimed. If a hand-made record sits at a claimed name's own address or at one of its server addresses, the service leaves that address alone and logs `has a hand-made … record` until you delete it. Never put the comment `playkeeper-names …` on records of your own.

### Blocking names

Beyond the built-in list (`www`, `names`, `api`, mail names, everything containing `playkeeper`, and more: see `reserved` in `internal/names/service/limits.go`), names nobody may claim go into a blocklist. In Coolify, open **Persistent Storage** → **+ Add** → **File Mount**, set **Destination Path** to `/config/blocklist` and write one name per line as the content (`#` starts a comment). Then add `NAMES_BLOCKLIST_FILE` with the value `/config/blocklist` and redeploy. The service rereads the file within a minute of a change and logs `Loaded the blocklist`; if that line does not appear after you edit the list, redeploy. Listed names cannot be claimed, and a listed name that is in use is released and its records removed.

### Zone full

Cloudflare's Free plan allows 200 DNS records in the zone, your own and Email Routing's included. A name uses one record (two with IPv6), plus one per server address, plus a TXT record for a few minutes while a certificate is renewed. Before each change the service counts the zone's records against the lower of Cloudflare's quota and `NAMES_RECORD_QUOTA` (200), and leaves free:

| Change | Leaves free |
| --- | --- |
| a certificate challenge | `NAMES_RECORD_RESERVE` (10), which is yours |
| a new name | the reserve, and 10 more for certificate challenges |
| a server address | the reserve, 10 for challenges, and 40 more for new names |

So a filling zone refuses server addresses first, then new names, and certificate challenges last, and the service alerts you at each step (`zone_nearly_full`, `zone_full`, `challenges_refused`; see [Alerts](#alerts)). Records of released and lapsed names are removed on their own, and names whose dashboard stopped answering lapse after a week. For more room, move the zone to Cloudflare's Pro plan (3,500 records) and set `NAMES_RECORD_QUOTA` to `3500`, or give free names a [domain of their own](#a-domain-of-its-own-for-free-names).

### Let's Encrypt limits

Each dashboard at a name gets its own certificate, and Let's Encrypt counts them all against playkeeper.io, the registered domain: at most 50 new certificates in 7 days for the whole domain, the website's own included. A renewal for exactly the same names does not count. The service keeps within that as far as it can see:

- A dashboard proves it holds its name with a TXT record. The values a name publishes within an hour of the first, up to 4, count as one attempt at a certificate, and a name may make 3 attempts in 7 days. Further attempts are refused with `certificate_limit` and the time to try again.
- A name's first attempt since its claim, or in 90 days, is a new certificate. At most `NAMES_NEW_CERTIFICATES_PER_WEEK` (40) of those start in any 7 days. When they are used up, new dashboards wait and you get the `certificate_budget` alert. Names that already have a certificate keep renewing meanwhile: their later attempts count as renewals, separately. Each attempt is logged with `kind=new` or `kind=renewal` and the week's counts (`new_this_week`, `renewals_this_week`).

This covers a rush of new names and a dashboard stuck retrying. It cannot stop someone who holds several names on purpose. Let's Encrypt also lets them prove a name over HTTP, since its address points at their machine, and reuses a proven name for a while, so they can order certificates for many combinations of their names without new TXT records. Six names make 63 combinations, more than a week's 50, and then every new dashboard shows a browser warning until the week is over. Only a [domain of its own](#a-domain-of-its-own-for-free-names) on the Public Suffix List fixes that. Let's Encrypt's [rate limit form](https://isrg.formstack.com/forms/rate_limit_adjustment_request) can raise the limit for playkeeper.io, which helps with volume but not against that. If you ever add CAA records to the zone, include `letsencrypt.org`.

### The token can edit the whole zone

Cloudflare cannot limit a token to some of a zone's records, so this token can change every playkeeper.io record, including `@` and `www`: the website and the `/install` address the one-line installer downloads from. The service's guard keeps the service itself away from them; the risk is someone else getting the token, for example through a flaw in the service, which faces the internet. That is why the token only works from your server (step 2), lives only in Coolify's environment variables and not in the image, and never appears in the service's log. Cloudflare's **Audit Log** (**Manage Account** → **Audit Log**) lists every change made with it. The IP filter does not help against a flaw in the service, since the service runs on that server; only a [domain of its own](#a-domain-of-its-own-for-free-names) rules the risk out.

### A domain of its own for free names

Free names live in the playkeeper.io zone for now. Moving them to a domain used for nothing else, in its own Cloudflare zone and on the private section of the [Public Suffix List](https://publicsuffix.org/), fixes three things at once:

- The token can then edit only that zone, so it can no longer touch the website or `/install`.
- Each name becomes a registered domain of its own, so each gets Let's Encrypt's 50 a week to itself, and nobody can use up everyone's.
- Browsers treat each name as a separate site, so one dashboard cannot make same-site requests to another, or to playkeeper.io.

This is your decision. It is easiest before the first Playkeeper release with free names, because names claimed under playkeeper.io do not move on their own. It takes:

1. A new domain, registered for at least two more years (the list requires it).
2. A Cloudflare zone for it, set up as in steps 1 and 3, and a token as in step 2 for that zone only.
3. A pull request to the private section of the list, following its [guidelines](https://github.com/publicsuffix/list/wiki/Guidelines), including the `_psl` TXT record they ask for. Give site isolation as the reason: independent people run their own servers and dashboards under the domain and must be kept apart, as with duckdns.org or github.io. The list turns down requests made only to get around Let's Encrypt's limits. Review takes weeks, and browsers and Let's Encrypt pick up the change in their next updates.
4. A Playkeeper release with the new domain as `names.DefaultBase`, and in Coolify, `NAMES_BASE_DOMAIN`, `NAMES_CLOUDFLARE_ZONE_ID` and `NAMES_CLOUDFLARE_API_TOKEN` for the new zone.

## Limits

These keep one install, one network or a flood of claims from using up the zone or playkeeper.io's certificates. The ones with a setting can be changed in Coolify (step 4).

| What | Limit | Setting |
| --- | --- | --- |
| A name | 3 to 32 characters: `a-z`, `0-9` and single hyphens inside; not reserved or blocked | |
| Names per install | 1 | `NAMES_MAX_NAMES_PER_KEY` |
| Names pointing into one IPv4 /24 or IPv6 /48 network, together; a refresh that would move a name into a full one is refused | 3 | `NAMES_MAX_NAMES_PER_NETWORK` |
| New names per day, everyone together | 30 | `NAMES_CLAIMS_PER_DAY` |
| New names per day from one IPv4 address or IPv6 /56 | 3 | |
| Requests from one IPv4 address or IPv6 /64 | 60 at once, then 120 an hour | |
| Signed requests from one install | 30 at once, then 60 an hour | |
| Server addresses (SRV records) | 5 per install and 10 per network, from 3 days after the claim and once the dashboard has answered, so names claimed in bulk cost only their address records | |
| DNS records in the zone | 200, or Cloudflare's quota if that is lower | `NAMES_RECORD_QUOTA` |
| DNS records kept free in the zone | 10 for you; new names leave 10 more for certificates, and server addresses 40 more for new names | `NAMES_RECORD_RESERVE` |
| Certificate challenge records per name | 2 at a time (a name and its wildcard), each removed after an hour at the latest | |
| Certificate attempts per name | 3 in 7 days: a certificate and two retries. An attempt is the challenge values published within an hour, up to 4: one order for a name and its wildcard, retried once | |
| New certificates, everyone together | 40 in 7 days, 10 below Let's Encrypt's 50 for playkeeper.io, leaving room for the website's own certificates. A name's first attempt since its claim or in 90 days counts; later ones are renewals and do not | `NAMES_NEW_CERTIFICATES_PER_WEEK` |
| Liveness checks | every 6 hours per name; at most 20 a minute, 8 at once, one per address | |
| A name whose dashboard does not answer | records removed after 7 days without an answer and 4 failed checks in a row; free for others 7 days later if it never answered, else 60 days later | |
| Checks when a lapsed name is refreshed | right away, 3 at once, then 6 an hour per name | |
| A name that is not refreshed | records removed after 30 days (its install can still refresh it); free for others 60 days later | |
| A released name | held for 30 days; only its own install can take it back | |
| A signed request | within 5 minutes of the service's clock, used once, body up to 4 KiB | |
| Alerts | each kind at most every 6 hours | `NAMES_ALERT_WEBHOOK_URL` |

The certificate numbers follow how dashboards get certificates. A dashboard renews at two thirds of a certificate's life, every 60 days for Let's Encrypt's 90-day certificates, so renewals stay far below 3 attempts a week and always come within 90 days of the previous one. Playkeeper checks that its record can be seen before it asks Let's Encrypt to look, so attempts rarely fail; failed ones count too, and a dashboard that keeps failing uses up its 3 within hours, then waits until a week after its first. Through the service's records, someone holding 6 names can order at most 18 certificates a week instead of the 63 combinations; what the records cannot see is explained in [Let's Encrypt limits](#lets-encrypt-limits).

## Try it on your computer

From the repository root:

```bash
scripts/names-check.sh          # builds the image and checks it without contacting Cloudflare (needs Docker)
go test ./internal/names/...    # the client and the service, against a fake Cloudflare
```
