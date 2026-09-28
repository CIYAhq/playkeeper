# playkeeper.io

This folder is the website at [playkeeper.io](https://playkeeper.io): the landing page, feature, comparison and guide pages, the sizing guide, the docs, pricing, the blog, the share page for server templates and the live demo, served by nginx in a container. `https://playkeeper.io/install` answers with a redirect (HTTP 302) to `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh`, so the one-line installer always gets `get.sh` from the latest release, and a new release never needs a redeploy of the site.

| Path | Answer |
| --- | --- |
| `/`, `/features/…`, `/alternatives/…`, `/guides/…`, `/pricing`, `/blog`, `/blog/…` | pages built from `pages/` |
| `/sizing` | the sizing guide: its calculator, table and several-servers rules come from `internal/sizing` (`internal/site/sizing.go`, `layouts/sizing.html`, `static/js/sizing.js`); the landing page's "How big a VPS?" is the same calculator |
| `/tools`, `/tools/…` | the free tools and their hub (`pages/tools.html`, `pages/tools/`): each runs in the browser, from `static/js/tools.js` and its own script in `static/js/tools/`, and makes nothing on a server |
| `/demo/` | the live demo: the dashboard in `web/` built with its sample data (`web/src/demo`); any path under it that isn't a file is one of its pages |
| `/docs`, `/docs/…` | the docs, built from the repository's own Markdown: `README.md`'s sections, `docs/RECOVERY.md`, `docs/TROUBLESHOOTING.md`, `CONTRIBUTING.md` and `SECURITY.md` |
| `/t` | the share page for server templates (`pages/t.html`, `static/js/t.js`), kept out of search engines |
| `/sitemap.xml`, `/robots.txt`, `/blog/feed.xml` | for search engines and feed readers |
| `/community` | `302` to where questions go (see Settings below) |
| `/install` | `302` to the latest release's `get.sh` |
| `/install/<code>` | the same `302`, with a channel's code, which the install log counts (see Channels below) |
| `/go/<code>` | `302` to the landing page with a channel's UTM tags (see Channels below) |
| `/healthz` | `200` with `ok`, for health checks |

## How it's built

`go run ./cmd/site` (or `make site`) builds the site into `site/dist`: `html/` is the web root and `nginx/site.conf` the part of `nginx.conf` that follows the settings. `site/Dockerfile` does the same in one stage, builds the live demo (`npm run build:demo` in `web/`) in another, then serves both with nginx (pinned by digest, on port 80) with a health check on `/healthz`. Because the site reads the product's code and docs and the demo is the dashboard, the image is built from the **repository root**: `docker build -f site/Dockerfile .`; `Dockerfile.dockerignore` sends only what it needs. Nothing built is committed.

The generator is `internal/site`:

- **Pages** are the files in `pages/`, one per page, at the address their settings give. A page starts with its settings in a template comment (`path`, `title`, `description`, `label`, `kind`, `section`, `layout` and so on; see `internal/site/pages.go`), then defines its parts: `main` for most pages; `short`, `article`, `after` and `keep-reading` for a guide or a blog post.
- **Layouts and blocks** are in `layouts/`: the page around every page (`base.html`), the guide, post and docs layouts (`article.html`), and the blocks pages are built from (`parts.html`): the install command, FAQ, steps, comparison tables, provider cards, screenshots in a browser or phone frame, template cards, the logo row, a terminal and "Keep reading".
- **Assets** in `static/` and the dashboard's art in `web/src/assets` (Pip, the pixel scenes and the server software logos, with their licences in `web/src/assets/logos/NOTICE.md`) are published under `/assets/` with a hash in their names, so nginx lets browsers keep them for a year.
- **Facts come from the product**: the server types, their count and logo row from `minecraft.Types`, the template cards' links from `internal/templates` (the templates are in `data/templates`), and the version from `CHANGELOG.md`. When a server type is added to the dashboard, the site shows it too.

A page that isn't built yet can already be linked: the header's menus, the footer and "Keep reading" show only pages that exist, and `first` picks a stand-in until then. `go test ./internal/site` builds the whole site and fails on a broken link or anchor, a missing title, description, canonical address or social preview, inline script or style (the Content-Security-Policy blocks them), an image without its size or alt text, or skipped heading levels.

### Add a page

1. Copy the page closest to it: a feature page (`pages/features/mods-and-modpacks.html`), a comparison (`pages/alternatives/aternos.html`), a guide (`pages/guides/modded-minecraft-server.html`) or a blog post (`pages/blog/playkeeper-0-4-0.html`).
2. Change its settings and words. A screenshot is `static/shots/<name>-<width>w.avif` and `.webp` at a few widths: `test/e2e/ui/site-captures.mjs` takes it from the dashboard of the release the site describes (its live demo, with the sample data the captures add), at 2 to 4 times its pixels, and `site/tools/shots.py` makes the widths the site shows it at. The page gives its `Sizes`, how wide it shows it, and marks the first thing it shows `Eager`, which its head asks for early. `test/e2e/ui/site-shots.mjs` takes screenshots of whole pages. Every page gets a social preview in `static/og/`, drawn by `test/e2e/ui/site-og.mjs`.
3. Run `go run ./cmd/site -serve 127.0.0.1:8080` and look at it (it sends the site's Content-Security-Policy, as nginx does, so a page that breaks it breaks there too), then run `go test ./internal/site`, the browser checks (in `test/e2e/ui`: `npx playwright test -c playwright.site.config.ts`, which open every page at desktop and phone sizes and fail on anything wider than the screen or a serious accessibility violation) and `scripts/site-check.sh`.

A free tool is a page like `pages/tools/server-icon.html`: its script in `static/js/tools/` (after `js/tools.js`, which copies, downloads and counts `tool_used`), its styles in `static/css/tools/`, the blocks every tool shares in `layouts/tools.html`, and a line in `tools` in `internal/site/tools.go` with the search it's made for, which its title and heading must say. That line puts it in the header's Tools menu and on `/tools` once its page exists. Its social preview is drawn by `test/e2e/ui/site-og.mjs`, and its browser checks go in `test/e2e/ui/site-tools*.spec.ts`.

### Settings

`internal/site/settings.go` holds the switches that change several pages at once. `Community` is where "Ask a question" links go: the repository's GitHub Discussions. Set it to `issues` if Discussions is ever off, and every link, its words and `/community` follow. Nothing on the site collects an email address. `Analytics` is the visit counter ([OpenAnalytics](https://github.com/OpenLabs-so/openanalytics), cookieless) that every page but the share page `/t` loads, and the Content-Security-Policy lets its script and collector in; the live demo loads the same script (`web/src/demo/vite.ts`). The site sends `Referrer-Policy: strict-origin-when-cross-origin` because under `no-referrer` Firefox and Safari send the counter's beacons with `Origin: null`, which its collector refuses.

Funnels in the analytics are built from pages and these custom events. Each also has `where`, the page it happened on. `static/js/site.js` sends them from the site's pages and `web/src/demo/analytics.ts` from the live demo; the browser checks answer the analytics' script with one that sends nothing.

| Event | When | Properties |
| --- | --- | --- |
| `install_copied` | The install command is copied, with a Copy or selected and copied by hand | `spot`: `box` (the page's install command), `closing` (the dark band at the bottom), `button` (Copy the install command on `/pricing` and beside guides), `code` (a code block in the docs), `selection` (by hand) or `card` (the live demo's); `channel`: the code, for a channel's command (see Channels below) |
| `github_clicked` | A link to the repository on GitHub, or to `/community` | `link`: `repo`, `releases`, `file`, `discussions`, `community` and so on |
| `provider_clicked` | See today's price at a VPS provider (`/sizing`, `/alternatives/aternos`), or Get a … server on its guide; its Setup guide link stays on the site and counts nothing | `provider`, and the `plan` it showed |
| `watch_releases_clicked` | Watch releases on GitHub on `/pricing`, which is also a `github_clicked` | `plan`: `storage` or `partner` |
| `install_shared` | Send to my computer, beside Copy on `/start` on phones: the page's address shared, or copied where the phone can't share it | `spot`: `box` or `closing`; `how`: `share` (the phone's share sheet) or `copy` |
| `demo_opened` | A link to the live demo | `spot`: `page`, `closing`, `header` or `menu` (the phone menu) |
| `tool_used` | A free tool's result is taken: a file downloaded or a result copied (`static/js/tools.js`) | `tool`: the tool, such as `server-icon`; `action`: `download` or `copy` |
| `demo_server_created` | New server finished in the live demo | `type`: the server type, such as `paper` |

### Providers and partner links

`providers` in `internal/site/providers.go` are the VPS providers the site suggests: the cards under "VPS that fit" on `/sizing` and the Aternos page, and a setup guide each (`pages/guides/<provider>-minecraft-server.html`). Each lists the plans that suit Minecraft, cheapest first, with the price and the day it was checked; the cards show plan names only, and the guides show prices with that day. The plan a card or guide shows is the cheapest that fits the sizing guide's answer.

`Partner` is the provider's affiliate or referral link. Every page that carries one says so before its first partner link: the note above the cards, or a guide's line under its title, which follows the page's `partner` setting. With `Partner` empty, as while a program hasn't approved Playkeeper, the site links the provider's own page with no disclosure. A partner link on another host, such as an affiliate network's, needs that host in `recordEvents` in `test/e2e/ui/site.spec.ts`.

### Channels

`Channels` in `internal/site/channels.go` are where visitors come from, such as a creator's sponsored video or a launch post, each with a code. `playkeeper.io/go/<code>` sends a visitor to the landing page with the channel's UTM tags (`utm_content` is the code), and the landing page then shows them that channel's install command, `curl -fsSL https://playkeeper.io/install/<code> | sudo sh`. It does this only for the codes it lists, reads the code from the address and stores nothing, so the site stays cookieless. Other pages, and other visitors, see the usual command. `install_copied` says which channel's command was copied in `channel`. To add a channel, add it to the list and redeploy.

Every `/install/<code>`, with any code, is the same redirect as `/install`, so a typo in a command still installs. nginx writes each request for `/install` or `/install/<code>` to the install log, and nowhere else: one file a day, `/var/log/playkeeper/installs-YYYY-MM-DD.log`, with the time, the visitor's address (from the Coolify proxy's `X-Forwarded-For`), the method, the path, the status and the user agent. `install-logs.sh` deletes each file after 30 days. For the log to survive redeploys, that folder is a volume in Coolify (step 5 below). Install runs per code, counting each address once and leaving out browsers, from a terminal in the site's container:

```bash
awk '$3 == "GET" && $5 == 302 && $6 !~ /^"Mozilla/ { print tolower($4), $2 }' /var/log/playkeeper/installs-*.log | sort -u | awk '{ print $1 }' | uniq -c
```

The release workflow's check of `/install` after each release counts as one there.

### /start

`/start` is where the Meta ads land (`pages/start.html`). It's kept out of search engines and the sitemap, and it answers with no redirect, so its query string stays: Whop's pixel reads the ad's IDs from it.

- **Install command:** the page's `channel: start` setting makes every install command on it `curl -fsSL https://playkeeper.io/install/start | sudo sh`.
- **Send to my computer:** the ads are seen on phones, where a command for a VPS is no use, so the page's `share: true` setting puts Send to my computer beside each Copy, on phones only. It opens the phone's share sheet with the page's address as it is, ad IDs included, so Whop can credit the ad when it's opened on a computer; where the browser can't share, it copies the address. Right under the hero's Copy, a line links to `/sizing` for anyone without a VPS.
- **The film:** `static/film/launch.mp4` is a 10.8-second, 720-pixel cut of the launch film, stopping before its "0.4.0 is out" card. It plays muted while it's in view, and not with reduced motion.
- **Whop's ad pixel:** `WhopPixel` in the settings names the Whop business. `js/start.js` loads the pixel with Whop's own snippet, on this page only. It sends a page view, then `install_copied`, `install_shared` and `demo_opened` whenever site.js counts one (the `playkeeper:count` event), each with the visit's one `event_id`, so Whop counts each once a visit. It doesn't load when the browser sends Global Privacy Control or Do Not Track.
- **Content-Security-Policy:** only `/start`'s policy, set in its nginx location, lets in `https://t.whop.tw`, the pixel's `blob:` worker and the film. The page ends with a note that says plainly what the pixel stores and sends. Keep forms, iframes and links to whop.com off the page: the pixel reads forms, posts to frames and tags those links.

### Modpack pages

`/modpacks` lists what each modpack's server needs, and `/modpacks/<id>-server` is one page per pack, like `/modpacks/atm10-server`. Each pack has its facts in `data/modpacks/<id>.json`: its source, project, version and release day, loader, Minecraft version, the mods its server runs, the download, the heap the pack's own settings ask for, its server files if its authors publish any, the site template that opens it (`data/templates`, Modrinth packs only), and the day all of it was checked, with the Playkeeper release whose install plan checked it (`release`). The pages name that release, not the newest `CHANGELOG.md` section, which comes before the release. The page gets Java and memory from the product (`minecraft.JavaFor`, `minecraft.PackNeedMB`, the memory New server suggests), so it says what the dashboard would.

- **Facts:** take them from the pack's source (Modrinth's API and the `.mrpack`'s index, or CurseForge's files) and from Playkeeper's own install plan for that version (`modpacks.Library.PlanInstall`, which counts the mods it puts on the server and names anything that blocks the install). CurseForge packs need a CurseForge API key for the plan.
- **The build refuses** a pack of a type the release doesn't run, for a Minecraft version older than the packs it offers (`modpacks.DefaultMinMinecraft`), or with a template that opens another version or less memory than the pack needs.
- **A template the release people install can't open yet is held.** Its entry in `data/templates/cards.json` gets `opensFrom`, the first release that opens it.
  - While it's held, no page links it: its cards stay out or give way to the stand-in a page names (`template-card`'s `Else`), and its pack page offers New server › A modpack. `TestNoPageOpensAHeldTemplate` checks every page.
  - 0.4.2 opens no 1.21.1 pack's template and no CurseForge pack's, although New server installs those packs. So check a pack's template through `POST /v1/templates/plan`, not only the pack.
  - Remove `opensFrom` once that release is out, not when its `CHANGELOG.md` section lands: sections come before the release.
- **A new page** copies the closest one in `pages/modpacks/` and keeps the blocks in `layouts/modpack.html` (facts, template card, the Docker and mrpack-install commands). What's true of that pack alone, like its restricted mods or the settings it expects, is what the page is for: `TestModpackAndTemplatePagesAreMostlyTheirOwn` fails a pack page when fewer than 60% of its sentences are its own. Search engines treat pages made at scale from one template as spam.
- **Social previews** come from the packs too: `node site-og.mjs modpacks modpack-<id>` draws the hub's and the page's.

### Template library

`/templates` is the public library of server templates, and `/templates/<id>` one page per template, like `/templates/towny`. Each is a site template in `data/templates` (the share format of `internal/templates`, so its card opens it in the visitor's dashboard) plus what happened when a server was created and started from it, in `data/library/<id>.json`: the Playkeeper release and server build, the seconds its log took to say Done, each plugin as it installed with its version, licence (the SPDX id its source lists) and downloads, and the day. The page gets Java and Java's share of the memory from the product (`minecraft.JavaFor`, `minecraft.HeapFor`), and its Docker command sizes the container the way Playkeeper does.

- **Check a template before it gets a page:** create a server from it on a Playkeeper release (the agent API's `POST /v1/templates/plan`, then `POST /v1/servers` and start it), and keep it only if the server comes online with every plugin enabled and nothing failing in its log. `GET /v1/servers/<id>/addons` has the versions that installed. A plugin another one needs, like Vault, goes in the template, not in a note on the page.
- **The build refuses** a facts file whose plugins aren't exactly the template's add-ons in its order, one that doesn't say which release, build and start time it was checked with, and one for a modpack's template, whose page is under `/modpacks`.
- **A new page** copies the closest one in `pages/templates/` and keeps the blocks in `layouts/library.html` (facts, Docker, template cards). What the plugins make you do first, with the commands and defaults from their own configs, is what the page is for, and the 60% rule above holds for these pages too.
- **Templates take each plugin's latest version,** so check them again when Minecraft or a plugin has a new release, and update the facts file with what installed.
- **Social previews** carry the pages' headings: `node site-og.mjs templates template-<id>`.

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
   - **Base Directory**: `/` (the whole repository: the site reads the product's code and docs, and the image builds the live demo from `web/`)
   - **Dockerfile Location**: `/site/Dockerfile`
   - **Ports Exposes**: `80`
5. Select **Continue**. Coolify opens the application's configuration.

### 3. Add the domains and deploy

1. In **Configuration** → **General**, set **Domains** to:

   ```
   https://playkeeper.io,https://www.playkeeper.io
   ```

   Both addresses, separated by a comma, each starting with `https://`.
2. Leave **Direction** at **Allow www & non-www**, or set **Redirect to non-www**: the site sends `www.playkeeper.io` to `playkeeper.io` itself (`nginx.conf`). Never choose **Redirect to www**, which would send visitors round in a loop.
3. Select **Save**, then **Deploy**, and wait until the deployment log says it has finished.

HTTPS is automatic: once DNS points at the server, the Coolify proxy fetches the certificate by itself. Until it has one, browsers may show a certificate warning for a few minutes.

### 4. Check it

On your computer:

```bash
curl -sI https://playkeeper.io/install   # a 302, with location: https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
curl -s https://playkeeper.io/healthz    # ok
curl -sI https://www.playkeeper.io/docs  # a 301, with location: https://playkeeper.io/docs
```

Then open `https://playkeeper.io` in a browser. `https://playkeeper.io/t` should say that the link has no template in it, and `https://playkeeper.io/sizing` should suggest a VPS with 6 GB of memory for 5–10 friends on Vanilla or Paper. Open `https://playkeeper.io/demo/` too: on a first visit it says it's the live demo, then shows the dashboard with its sample servers and the amber "Live demo · resets every hour" line under the brand.

### 5. Keep the install log

The install log (see Channels above) is written inside the container, and each deploy replaces the container. A volume keeps it. Once, in Coolify:

1. Open the application, then **Configuration** → **Persistent Storage**.
2. Select **Add** → **Volume Mount**.
3. Fill in:
   - **Name**: `install-log`
   - **Source Path**: leave it empty, so Docker keeps the log in a named volume
   - **Destination Path**: `/var/log/playkeeper`
4. Select **Add**, then **Redeploy**, and wait until the deployment log says it has finished.
5. Check it. On your computer, `curl -s -o /dev/null -w '%{http_code}\n' https://playkeeper.io/install/check` prints `302`. Then, in Coolify, open the application's **Terminal** (or **Terminal** in the sidebar), choose the site's running container, select **Connect** and run `tail -n 1 /var/log/playkeeper/installs-*.log`: the line has today's date, your computer's address and `GET /install/check 302`. After the next deploy, the same command still shows it.

Keep **Delete Unused Volumes** off in the server's Docker cleanup settings, or a cleanup can delete the log.

### Already hosting the site? Switch it to the repository root

Before 0.4.0, Coolify built the image from the `site` folder alone. The image now needs the whole repository: one stage builds the live demo from `web/`, another builds the site with `cmd/site`, which reads `go.mod`, `internal/`, the docs and the dashboard's art. An application set up the old way fails to build, with errors such as `"/web/package-lock.json": not found` or `"/go.mod": not found`. Once, in Coolify:

1. Open the application, then **Configuration** → **General**.
2. Under **Build**, change **Base Directory** from `/site` to `/`.
3. Change **Dockerfile Location** from `/Dockerfile` to `/site/Dockerfile`. It's read from the Base Directory, so this is the repository's `site/Dockerfile`. BuildKit, Docker's default builder, picks up `site/Dockerfile.dockerignore` beside it, which keeps the build to the files the image needs.
4. Leave **Build Pack** at **Dockerfile**, **Ports Exposes** at `80` and the domains as they are. Nothing else changes: nginx still listens on port 80, and the image's own health check still asks `/healthz`.
5. Select **Save**, then **Deploy**, and wait until the deployment log says it has finished. Builds take a few minutes longer than before, while they build the site with Go and the demo with Node.
6. Check it as in step 4 above, including `https://playkeeper.io/sizing` and `https://playkeeper.io/demo/`.

## Updating

- **A new Playkeeper release:** nothing to do for `/install`, which always points at the latest release. The site's docs, version and server types come from the repository, so deploy again once the release is on `main`.
- **A change to this folder, the docs or the dashboard:** in Coolify, open the application and select **Deploy** again. An application added by repository URL is not redeployed on its own when `main` changes. That includes new sizing numbers in `internal/sizing`, which reach `/sizing` only with the next deploy, and changes in `web/`, which reach the live demo only with the next deploy.
- **A new nginx, Go or Node version:** change the tag and the digest on the matching `FROM` line of `Dockerfile` (Docker Hub lists both for each tag; nginx uses an `…-alpine-slim` tag, Go and Node the `…-alpine` tags of the versions in `scripts/toolchains.txt`), then check and redeploy.

## Try it on your computer

With Go (`./scripts/setup.sh` installs it) or Docker, from the repository root:

```bash
make site                                     # builds the site into site/dist
go run ./cmd/site -serve 127.0.0.1:8080       # serves it as nginx would, at http://127.0.0.1:8080
scripts/site-check.sh                         # builds the image and checks every page, /sizing, /demo/, /t, /healthz, /install and its log, /go/ and the headers; with Chrome installed, opens /sizing and /t in it
docker build -f site/Dockerfile -t playkeeper-site . && docker run --rm -p 8080:80 playkeeper-site   # then open http://localhost:8080 and http://localhost:8080/demo/
```
