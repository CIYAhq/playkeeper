# playkeeper.io

This folder is the website at [playkeeper.io](https://playkeeper.io): a page with the install command, a guide to how big a VPS to rent and a live demo of the dashboard, served by nginx in a container. `https://playkeeper.io/install` answers with a redirect (HTTP 302) to `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh`, so the one-line installer always gets `get.sh` from the latest release, and a new release never needs a redeploy of the site.

| Path | Answer |
| --- | --- |
| `/` | the page (`index.html`, `style.css`, `copy.js`, `favicon.svg`) |
| `/sizing` | the VPS sizing guide (`sizing.html`, `sizing-data.js`, `sizing.css`, `sizing-nojs.css`, `sizing.js`, `pip-wave.svg`, `playkeeper-mark.svg`); `/sizing/` redirects to it |
| `/demo/` | the live demo: the dashboard from `web/`, built with sample data (`npm run build:demo`), running entirely in the visitor's browser; every page under `/demo/` is answered by it, and `/demo` redirects to it |
| `/install` | `302` to the latest release's `get.sh` |
| `/healthz` | `200` with `ok`, for health checks |

`Dockerfile` builds the image in two stages: Node (pinned by digest) builds the live demo from `web/`, then nginx (pinned by digest, on port 80) serves this folder and the demo, with a health check on `/healthz`. Because it needs `web/` as well as this folder, the image is built from the **repository root**: `docker build -f site/Dockerfile .`. `Dockerfile.dockerignore` sends only `site/` and `web/` (without `node_modules` and builds) to the build; Docker reads it because it sits next to the Dockerfile with the same name. `nginx.conf` holds the redirects, the health path, the demo's routes and the security headers. CI builds the image and checks those paths on every pull request with `scripts/site-check.sh`.

The demo has no panel, agent or Minecraft behind it: its sample servers, players, console, backups and settings live in the browser tab, start again on the hour and on every new visit, and never send a request anywhere else. The sample data is in `web/src/demo/data.ts`.

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
   - **Base Directory**: `/` (the whole repository: the image builds the live demo from `web/`)
   - **Dockerfile Location**: `/site/Dockerfile`
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

Then open `https://playkeeper.io` in a browser: you should see the install page. Open `https://playkeeper.io/demo/` too: you should see the dashboard with its sample servers and the amber "Live demo · resets every hour" line under the brand.

### Already hosting the site? Switch it to the repository root

Until the live demo, Coolify built the image from the `site` folder alone. The image now needs `web/` too, so an application set up the old way fails to build, with errors such as `"/web/package-lock.json": not found`. Once, in Coolify:

1. Open the application, then **Configuration** → **General**.
2. Under **Build**, change **Base Directory** from `/site` to `/`.
3. Change **Dockerfile Location** from `/Dockerfile` to `/site/Dockerfile`.
4. Leave **Ports Exposes** at `80` and the domains as they are.
5. Select **Save**, then **Deploy**, and wait until the deployment log says it has finished. Builds take a little longer than before, while they build the demo.
6. Check it as in step 4 above, including `https://playkeeper.io/demo/`.

## Updating

- **A new Playkeeper release:** nothing to do. `/install` always points at the latest release.
- **A change to this folder or to the dashboard:** in Coolify, open the application and select **Deploy** again. An application added by repository URL is not redeployed on its own when `main` changes. That includes new sizing numbers, which reach `/sizing` only with the next deploy, and changes in `web/`, which reach the live demo only with the next deploy.
- **A new nginx or Node version:** change the tag and the digest on the matching `FROM` line of `Dockerfile` (Docker Hub lists both for each tag; nginx uses an `…-alpine-slim` tag, Node the `…-alpine` tag of the version in `scripts/toolchains.txt`), then check and redeploy.

## Try it on your computer

With Docker installed, from the repository root:

```bash
scripts/site-check.sh                         # builds the image and checks /, /sizing, /demo/, /healthz, /install and the headers
docker build -f site/Dockerfile -t playkeeper-site . && docker run --rm -p 8080:80 playkeeper-site   # then open http://localhost:8080 and http://localhost:8080/demo/
```

To work on the demo itself, `npm run build:demo` in `web/` builds it into `web/dist-demo/`, and `npx vite --mode demo` there serves it at `http://localhost:5173/demo/` while you edit.
