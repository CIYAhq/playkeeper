# playkeeper.io

This folder is the website at [playkeeper.io](https://playkeeper.io): a page with the install command and a guide to how big a VPS to rent, served by nginx in a container. `https://playkeeper.io/install` answers with a redirect (HTTP 302) to `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh`, so the one-line installer always gets `get.sh` from the latest release, and a new release never needs a redeploy of the site.

| Path | Answer |
| --- | --- |
| `/` | the page (`index.html`, `style.css`, `copy.js`, `favicon.svg`) |
| `/sizing` | the VPS sizing guide (`sizing.html`, `sizing-data.js`, `sizing.css`, `sizing-nojs.css`, `sizing.js`, `pip-wave.svg`, `playkeeper-mark.svg`); `/sizing/` redirects to it |
| `/install` | `302` to the latest release's `get.sh` |
| `/healthz` | `200` with `ok`, for health checks |

`Dockerfile` builds the image (nginx, pinned by digest, on port 80) with a health check on `/healthz`. `nginx.conf` holds the redirect, the health path and the security headers. CI builds the image and checks those paths on every pull request with `scripts/site-check.sh`.

`sizing.html` and `sizing-data.js` are generated: `make sizing` (`go run ./cmd/sizing-guide`) writes them from `sizing.html.tmpl` and the numbers in `internal/sizing`. Change those, not the generated files, then run it and commit the result; `make check` fails while they are out of date. The table works without JavaScript; the questions need it.

## Host it with Coolify

You need a server with Coolify on it, the Coolify proxy running (it is by default), and inbound TCP ports **80** and **443** open in the server's firewall. Coolify gets the HTTPS certificate from Let's Encrypt, which checks the domain through those ports.

### 1. Point the domain at the server

Open the DNS settings for `playkeeper.io` at your domain provider (at Namecheap: **Domain List** → **Manage** → **Advanced DNS**). Delete any parking or redirect records for `@` and `www`, then add these two:

| Type | Host | Value |
| --- | --- | --- |
| A Record | `@` | your server's public IPv4 address |
| CNAME Record | `www` | `playkeeper.io.` |

Add an AAAA record for `@` only if the server has a working public IPv6 address; a wrong one stops the certificate from being issued. Leave email records (MX and TXT) as they are.

DNS changes can take a while to spread; carry on with the next steps meanwhile. Once `dig +short playkeeper.io A` on your computer prints your server's address, the change has arrived.

### 2. Create the application

1. In Coolify, open your project and its environment, then select **+ New**.
2. Select **Public Repository**. If Coolify asks which server to use, choose the one the domain points at.
3. Paste `https://github.com/CIYAhq/playkeeper` and select **Check Repository**.
4. Fill in:
   - **Branch**: `main`
   - **Build Pack**: **Dockerfile**
   - **Base Directory**: `/site` (the `site` folder of the repository)
   - **Dockerfile Location**: `/Dockerfile`
   - **Ports Exposes**: `80`
5. Select **Continue**. Coolify opens the application's configuration.

### 3. Add the domains and deploy

1. In **Configuration** → **General**, set **Domains** to:

   ```
   https://playkeeper.io,https://www.playkeeper.io
   ```

   Both addresses, separated by a comma, each starting with `https://`.
2. Optional: set **Direction** to **Redirect to non-www**, so `www.playkeeper.io` sends visitors to `playkeeper.io`.
3. Select **Save**, then **Deploy**, and wait until the deployment log says it has finished.

HTTPS is automatic: once DNS points at the server, the Coolify proxy fetches the certificate by itself. Until it has one, browsers may show a certificate warning for a few minutes.

### 4. Check it

On your computer:

```bash
curl -sI https://playkeeper.io/install   # a 302, with location: https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
curl -s https://playkeeper.io/healthz    # ok
```

Then open `https://playkeeper.io` in a browser: you should see the install page.

## Updating

- **A new Playkeeper release:** nothing to do. `/install` always points at the latest release.
- **A change to this folder:** in Coolify, open the application and select **Deploy** again. An application added by repository URL is not redeployed on its own when `main` changes. That includes new sizing numbers: they reach `/sizing` only with the next deploy.
- **A new nginx version:** change the tag and the digest on the `FROM` line of `Dockerfile` (Docker Hub lists both for each `…-alpine-slim` tag), then check and redeploy.

## Try it on your computer

With Docker installed, from the repository root:

```bash
scripts/site-check.sh                         # builds the image and checks /, /sizing, /healthz, /install and the headers
docker build -t playkeeper-site site && docker run --rm -p 8080:80 playkeeper-site   # then open http://localhost:8080
```
