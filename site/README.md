# playkeeper.io

This folder is the website at [playkeeper.io](https://playkeeper.io): the landing page, feature, comparison and guide pages, the docs, pricing, the blog and the share page for server templates, served by nginx in a container. `https://playkeeper.io/install` answers with a redirect (HTTP 302) to `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh`, so the one-line installer always gets `get.sh` from the latest release, and a new release never needs a redeploy of the site.

| Path | Answer |
| --- | --- |
| `/`, `/features/…`, `/alternatives/…`, `/guides/…`, `/pricing`, `/blog`, `/blog/…` | pages built from `pages/` |
| `/docs`, `/docs/…` | the docs, built from the repository's own Markdown: `README.md`'s sections, `docs/RECOVERY.md`, `docs/TROUBLESHOOTING.md`, `CONTRIBUTING.md` and `SECURITY.md` |
| `/t` | the share page for server templates (`pages/t.html`, `static/js/t.js`), kept out of search engines |
| `/sitemap.xml`, `/robots.txt`, `/blog/feed.xml` | for search engines and feed readers |
| `/community` | `302` to where questions go (see Settings below) |
| `/install` | `302` to the latest release's `get.sh` |
| `/healthz` | `200` with `ok`, for health checks |

## How it's built

`go run ./cmd/site` (or `make site`) builds the site into `site/dist`: `html/` is the web root and `nginx/site.conf` the part of `nginx.conf` that follows the settings. `site/Dockerfile` does the same in its first stage, then serves the result with nginx (pinned by digest, on port 80) with a health check on `/healthz`. Because the site reads the product's code and docs, the image is built from the **repository root**: `docker build -f site/Dockerfile .`; `Dockerfile.dockerignore` sends only what it needs. Nothing built is committed.

The generator is `internal/site`:

- **Pages** are the files in `pages/`, one per page, at the address their settings give. A page starts with its settings in a template comment (`path`, `title`, `description`, `label`, `kind`, `section`, `layout` and so on; see `internal/site/pages.go`), then defines its parts: `main` for most pages; `short`, `article`, `after` and `keep-reading` for a guide or a blog post.
- **Layouts and blocks** are in `layouts/`: the page around every page (`base.html`), the guide, post and docs layouts (`article.html`), and the blocks pages are built from (`parts.html`): the install command, FAQ, steps, comparison tables, provider cards, screenshots in a browser or phone frame, template cards, the logo row, a terminal and "Keep reading".
- **Assets** in `static/` and the dashboard's art in `web/src/assets` (Pip, the pixel scenes and the server software logos, with their licences in `web/src/assets/logos/NOTICE.md`) are published under `/assets/` with a hash in their names, so nginx lets browsers keep them for a year.
- **Facts come from the product**: the server types, their count and logo row from `minecraft.Types`, the template cards' links from `internal/templates` (the templates are in `data/templates`), and the version from `CHANGELOG.md`. When a server type is added to the dashboard, the site shows it too.

A page that isn't built yet can already be linked: the header's menus, the footer and "Keep reading" show only pages that exist, and `first` picks a stand-in until then. `go test ./internal/site` builds the whole site and fails on a broken link or anchor, a missing title, description, canonical address or social preview, inline script or style (the Content-Security-Policy blocks them), an image without its size or alt text, or skipped heading levels.

### Add a page

1. Copy the page closest to it: a feature page (`pages/features/mods-and-modpacks.html`), a comparison (`pages/alternatives/aternos.html`), a guide (`pages/guides/modded-minecraft-server.html`) or a blog post (`pages/blog/playkeeper-0-4-0.html`).
2. Change its settings and words. Screenshots go in `static/shots/` as `<name>.webp` with a `<name>@2x.webp` beside it; `test/e2e/ui/site-shots.mjs` takes page screenshots. Every page gets a social preview in `static/og/`.
3. Run `make site` and open `site/dist/html`, or build the image, then run `go test ./internal/site` and `scripts/site-check.sh`.

### Settings

`internal/site/settings.go` holds the switches that change several pages at once. `Community` is where "Ask a question" links go: the repository's GitHub Discussions. Set it to `issues` if Discussions is ever off, and every link, its words and `/community` follow. Nothing on the site collects an email address.

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
   - **Base Directory**: `/` (the whole repository: the site reads the product's code and docs)
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

Then open `https://playkeeper.io` in a browser. `https://playkeeper.io/t` should say that the link has no template in it.

## Updating

- **A new Playkeeper release:** nothing to do for `/install`, which always points at the latest release. The site's docs, version and server types come from the repository, so deploy again once the release is on `main`.
- **A change to this folder or the docs:** in Coolify, open the application and select **Deploy** again. An application added by repository URL is not redeployed on its own when `main` changes.
- **A new nginx or Go version:** change the tag and the digest on its `FROM` line of `Dockerfile` (Docker Hub lists both for each tag), then check and redeploy.

## Try it on your computer

With Go (`./scripts/setup.sh` installs it) or Docker, from the repository root:

```bash
make site                                     # builds the site into site/dist; open site/dist/html/index.html's pages through any web server
scripts/site-check.sh                         # builds the image and checks every page, /t, /healthz, /install and the headers; with Chrome installed, opens /t in it
docker build -f site/Dockerfile -t playkeeper-site . && docker run --rm -p 8080:80 playkeeper-site   # then open http://localhost:8080
```
