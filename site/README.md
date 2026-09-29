# playkeeper.io

This folder is the website at [playkeeper.io](https://playkeeper.io): the landing page, feature, comparison and guide pages, the sizing guide, the docs, pricing, the blog, the share page for server templates and the live demo, served by nginx in a container. `https://playkeeper.io/install` answers with a few lines of shell, `install.sh`, that download `https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh` and run it, so the one-line installer always gets `get.sh` from the latest release, and a new release never needs a redeploy of the site. They tell `get.sh` the install came through playkeeper.io, which the anonymous [usage stats](../README.md#usage-stats) count as an install on our domain.

| Path | Answer |
| --- | --- |
| `/`, `/features/…`, `/alternatives/…`, `/guides/…`, `/pricing`, `/blog`, `/blog/…` | pages built from `pages/` |
| `/sizing` | the sizing guide: its calculator, table and several-servers rules come from `internal/sizing` (`internal/site/sizing.go`, `layouts/sizing.html`, `static/js/sizing.js`); the landing page's "How big a VPS?" is the same calculator |
| `/tools`, `/tools/…` | the free tools and their hub (`pages/tools.html`, `pages/tools/`): each runs in the browser, from `static/js/tools.js` and its own script in `static/js/tools/`, and makes nothing on a server |
| `/demo/` | the live demo: the dashboard in `web/` built with its sample data (`web/src/demo`); any path under it that isn't a file is one of its pages |
| `/docs`, `/docs/…` | the docs, built from the repository's own Markdown: `README.md`'s sections, `docs/RECOVERY.md`, `docs/TROUBLESHOOTING.md`, `CONTRIBUTING.md` and `SECURITY.md` |
| `/templates`, `/templates/…` | the template directory: pages of templates, a page per category and per template, from `data/templates` (see Template directory below) |
| `/t` | the share page for server templates (`pages/t.html`, `static/js/t.js`), kept out of search engines |
| `/t/<id>` | a directory template's share page: sends the browser straight on to `/t` with the template, kept out of search engines |
| `/sitemap.xml`, `/robots.txt`, `/blog/feed.xml` | for search engines and feed readers |
| `/community` | `302` to where questions go (see Settings below) |
| `/install` | `install.sh`, which runs the latest release's `get.sh` and tells it the install came through playkeeper.io |
| `/install/<code>` | the same script with a channel's code filled in, for the usage stats and the install log (see Channels below) |
| `/go/<code>` | `302` to the landing page with a channel's UTM tags (see Channels below) |
| `/healthz` | `200` with `ok`, for health checks |

## How it's built

`go run ./cmd/site` (or `make site`) builds the site into `site/dist`: `html/` is the web root and `nginx/site.conf` the part of `nginx.conf` that follows the settings. `site/Dockerfile` does the same in one stage, builds the live demo (`npm run build:demo` in `web/`) in another, then serves both with nginx (pinned by digest, on port 80) with a health check on `/healthz`. Because the site reads the product's code and docs and the demo is the dashboard, the image is built from the **repository root**: `docker build -f site/Dockerfile .`; `Dockerfile.dockerignore` sends only what it needs. Nothing built is committed.

The generator is `internal/site`:

- **Pages** are the files in `pages/`, one per page, at the address their settings give. A page starts with its settings in a template comment (`path`, `title`, `description`, `label`, `kind`, `section`, `layout` and so on; see `internal/site/pages.go`), then defines its parts: `main` for most pages; `short`, `article`, `after` and `keep-reading` for a guide or a blog post.
- **Layouts and blocks** are in `layouts/`: the page around every page (`base.html`), the guide, post and docs layouts (`article.html`), and the blocks pages are built from (`parts.html`): the install command, FAQ, steps, comparison tables, provider cards, screenshots in a browser or phone frame, template cards, the logo row, a terminal and "Keep reading".
- **Assets** in `static/` and the dashboard's art in `web/src/assets` (Pip, the pixel scenes and the server software logos, with their licences in `web/src/assets/logos/NOTICE.md`) are published under `/assets/` with a hash in their names, so nginx lets browsers keep them for a year.
- **Facts come from the product**: the server types, their count and logo row from `minecraft.Types`, the template cards' links from `internal/templates` (the templates are in `data/templates`), and the version from `data/release.json`, the latest published release, which must have a section in `CHANGELOG.md` (a newer section is a release still being put together, and the site doesn't show it). When a server type is added to the dashboard, the site shows it too.

A page that isn't built yet can already be linked: the header's menus, the footer and "Keep reading" show only pages that exist, and `first` picks a stand-in until then. `go test ./internal/site` builds the whole site and fails on a broken link or anchor, a missing title, description, canonical address or social preview, inline script or style (the Content-Security-Policy blocks them), an image without its size or alt text, or skipped heading levels.

### Add a page

1. Copy the page closest to it: a feature page (`pages/features/mods-and-modpacks.html`), a comparison (`pages/alternatives/aternos.html`), a guide (`pages/guides/modded-minecraft-server.html`) or a blog post (`pages/blog/playkeeper-0-4-0.html`).
2. Change its settings and words. A screenshot is `static/shots/<name>-<width>w.avif` and `.webp` at a few widths: `test/e2e/ui/site-captures.mjs` takes it from the dashboard of the release the site describes (its live demo, with the sample data the captures add), at 2 to 4 times its pixels, and `site/tools/shots.py` makes the widths the site shows it at. The page gives its `Sizes`, how wide it shows it, and marks the first thing it shows `Eager`, which its head asks for early. `test/e2e/ui/site-shots.mjs` takes screenshots of whole pages. Every page gets a social preview in `static/og/`, drawn by `test/e2e/ui/site-og.mjs`.
3. Run `go run ./cmd/site -serve 127.0.0.1:8080` and look at it (it sends the site's Content-Security-Policy, as nginx does, so a page that breaks it breaks there too), then run `go test ./internal/site`, the browser checks (in `test/e2e/ui`: `npx playwright test -c playwright.site.config.ts`, which open every page at desktop and phone sizes and fail on anything wider than the screen or a serious accessibility violation) and `scripts/site-check.sh`.

A free tool is a page like `pages/tools/server-icon.html`: its script in `static/js/tools/` (after `js/tools.js`, which copies, downloads and counts `tool_used`), its styles in `static/css/tools/`, the blocks every tool shares in `layouts/tools.html`, and a line in `tools` in `internal/site/tools.go` with the search it's made for, which its title and heading must say. That line puts it in the header's Tools menu and on `/tools` once its page exists. Its social preview is drawn by `test/e2e/ui/site-og.mjs`, and its browser checks go in `test/e2e/ui/site-tools*.spec.ts`.

### Settings

`internal/site/settings.go` holds the switches that change several pages at once. `Community` is where "Ask a question" links go: the repository's GitHub Discussions. Set it to `issues` if Discussions is ever off, and every link, its words and `/community` follow. Nothing on the site collects an email address. `Analytics` is the visit counter ([OpenAnalytics](https://github.com/OpenLabs-so/openanalytics), cookieless) that every page but the share page `/t` loads, and the Content-Security-Policy lets its script and collector in; the live demo loads the same script (`web/src/demo/vite.ts`). The site sends `Referrer-Policy: strict-origin-when-cross-origin` because under `no-referrer` Firefox and Safari send the counter's beacons with `Origin: null`, which its collector refuses. `Stats` is the stats service (`services/stats`), whose dashboard's funnel counts copies of the install command: on the same pages as the counter, a copy (with a Copy or by hand) tells it so, once a page view, with the channel's code and nothing else. It's a CORS request with no cookie and no referrer, because a no-cors one without a referrer names no origin in Firefox and Safari, and the service takes counts only from `https://playkeeper.io`; a browser that sends Global Privacy Control or Do Not Track sends none. The Content-Security-Policy lets pages reach it, and the browser checks answer for it, so no copy is counted from a test run.

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
| `template_opened` | Open in my dashboard on a template's card or page in the template directory | `template`: its id, such as `towny`; `spot`: `card`, `page` (the template's page) or `related` (a card under it) |
| `demo_server_created` | New server finished in the live demo | `type`: the server type, such as `paper` |

### Providers and partner links

`providers` in `internal/site/providers.go` are the VPS providers the site suggests: the cards under "VPS that fit" on `/sizing` and the Aternos page, and a setup guide each (`pages/guides/<provider>-minecraft-server.html`). Each lists the plans that suit Minecraft, cheapest first, with the price and the day it was checked; the cards show plan names only, and the guides show prices with that day. The plan a card or guide shows is the cheapest that fits the sizing guide's answer.

`Partner` is the provider's affiliate or referral link. Every page that carries one says so before its first partner link: the note above the cards, or a guide's line under its title, which follows the page's `partner` setting. With `Partner` empty, as while a program hasn't approved Playkeeper, the site links the provider's own page with no disclosure. `OfferUSD`, `OfferDays` and `OfferTerms` are the credit a new account gets through the partner link, like Vultr's $300 for 30 days: its card and its guide show it with its conditions, only while the partner link is there, and a guide calls a month free only when the credit pays for that plan's month. A partner link on another host, such as an affiliate network's, needs that host in `recordEvents` in `test/e2e/ui/site.spec.ts`.

### Channels

`Channels` in `internal/site/channels.go` are where visitors come from, such as a creator's sponsored video or a launch post, each with a code. `playkeeper.io/go/<code>` sends a visitor to the landing page with the channel's UTM tags (`utm_content` is the code), and the landing page then shows them that channel's install command, `curl -fsSL https://playkeeper.io/install/<code> | sudo sh`. It does this only for the codes it lists, reads the code from the address and stores nothing, so the site stays cookieless. Other pages, and other visitors, see the usual command. `install_copied` says which channel's command was copied in `channel`. To add a channel, add it to the list and redeploy.

Every `/install/<code>`, with any code, is the same script as `/install`, with the code filled in (nginx's `sub_filter`), so a typo in a command still installs, and the usage stats count installs per code: `byChannel` in the stats service's counts ([services/stats](../services/stats/README.md#reading-the-numbers)). nginx writes each request for `/install` or `/install/<code>` to the install log, and nowhere else: one file a day, `/var/log/playkeeper/installs-YYYY-MM-DD.log`, with the time, the visitor's address (from the Coolify proxy's `X-Forwarded-For`), the method, the path, the status and the user agent. `install-logs.sh` deletes each file after 30 days. For the log to survive redeploys, that folder is a volume in Coolify (step 5 below). Install runs per code, counting each address once and leaving out browsers, from a terminal in the site's container:

```bash
awk '$3 == "GET" && $5 == 200 && $6 !~ /^"Mozilla/ { print tolower($4), $2 }' /var/log/playkeeper/installs-*.log | sort -u | awk '{ print $1 }' | uniq -c
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

### Template directory

`/templates` is the directory of server templates: every template the release people install can open whose last check passed (Checks, below), as cards to search, filter (game mode, type, loader, Minecraft version, memory and features) and sort (popular, newest, A–Z). A template without a passing check stays out until it has one, like a held template (above). Each is a site template in `data/templates`, in the share format of `internal/templates`, so its card's Open in my dashboard opens it in the visitor's own dashboard.

- **Without scripts** it's pages of 24 cards (`/templates`, `/templates/page/2`…), a page per category (`/templates/<category>`) and a page per template (`/templates/<category>/<id>`), all linked, so search engines and visitors without scripts reach every template. `js/templates.js` adds search, filters and sorting over all of them at once, from the index the build writes (`js/templates-index.js`, loaded once the page is quiet), and keeps what's chosen in the address, like `/templates?loader=paper&sort=new`. Its cards open templates through `/t/<id>`, which sends the browser straight on to the share page.
- **What a template is listed under:** `data/templates/cards.json` gives each its categories (the first is the one its page is under), its tags, the day it was added and its popularity, which sorts Popular. `data/taxonomy.json` (beside `data/templates`, since `cmd/template-check` reads every other file in there as a template) names the categories and tags, with each category's intro, and the tags each add-on brings, so a template with CoreProtect is tagged Grief rollback without saying so. A tag's `search` words find its templates too. Type, loader, version and memory come from the template itself. The build refuses a category or tag that isn't in `taxonomy.json`, a template with no category or no day, and the slug `page`, which the directory's pages use.
- **Thumbnails:** a picture of a template's own world takes the place of its pixel-art scene wherever it shows: its card, its page, the share page, its category's picture and the game-mode cards. Name the 16:10 captures (1920 × 1200 is best, at least 1200 wide) `templates/<id>.png` in a folder, and `python3 tools/shots.py <folder>` writes `static/shots/templates/<id>-480w`, `-960w` and `-1200w` (a phone's full-width card at 3x) in AVIF and WebP. The build finds them by themselves, and refuses a thumbnail for no template, at another size, or missing one of its six files. Without one, a template keeps its scene.
  - **Making them:** `make template-thumbnails SOCKET=/tmp/pk/agent.sock ARGS="-only towny"`, on `playkeeper dev` started with `PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1`. `cmd/template-check -shots` checks each template as usual, then a bot (`tools/thumbnails/capture.js`, mineflayer) joins its server and saves the world around spawn, or first reaches the mode's showpiece as a player would (`tools/thumbnails/recipes.json`: the island `/is create` makes, the OneBlock block). `tools/thumbnails/render.mjs` draws it with prismarine-viewer and Minecraft's own textures in headless Chrome, choosing the camera from the world, and `tools/shots.py` writes the files. Nothing is built for the picture, and it shows no Mojang logo or mark.
  - **The bot** speaks the newest Minecraft version mineflayer knows (`tools/thumbnails/version.js`), so a server on another version gets ViaVersion, and ViaBackwards when it's newer (ViaFabric on Fabric), for the capture. A server it can't join, like a NeoForge pack's or one whose mods players must have, gets pictures made of its modpack's icon from Modrinth: `node tools/thumbnails/pack-art.mjs <folder> <id>`, then `tools/shots.py`.
  - **Nightly,** `.github/workflows/templates.yml` makes them for templates without one whose servers the bot can join, on an offline-mode Playkeeper, and files an issue with them as an artifact for the scheduled template agent to commit.
- **The share page's picture** (`/t`) is a directory template's own, the same as its card's and page's (`share.go`): the page carries each directory template's picture, marked with the first ten characters of its link, which carry its checksum, and `js/t.js` shows the one for the link it reads. A template from anywhere else gets the scene of how its server is played.
- **What a template installs** shows with each project's own icon from Modrinth or Hangar: up to three on its card with "+N more", and every one on its page. `cmd/site` fetches the icons while it builds (Modrinth's in one request) and publishes each as the site's own 96-pixel PNG (`assets/icons/<source>-<project>.png`), since the Content-Security-Policy allows only the site's own images. A project without an icon, a CurseForge pack (whose API needs a key), or one whose source doesn't answer shows its initial, and the build says which. `go run ./cmd/site -icons=false` builds without fetching, as the tests do.
- **Open in my dashboard** is one click once the browser knows the visitor's dashboard (`site.js`, on every page with a template link). The first time, a dialog asks for it: a free name like `alex` means `https://alex.playkeeper.me:8443`, an address without `https://` gets port 8443 unless it has one, and a server's address like `survival.alex.playkeeper.me` means its machine's dashboard. The browser keeps it in `localStorage` (`playkeeper.dashboard`), and it's never sent anywhere. From then on, every link to `/t#<template>` goes straight to `<dashboard>/servers/new#template=<template>`, and "Opens in … Change" shows where, with Forget. Cards drawn by `js/templates.js` link `/t/<id>`, and that script reads the template from that page first. Without a dashboard, the dialog links the share page, which says how to get one.
  - **The share page** (`/t`) offers "Open in <dashboard>" when the browser knows one, and takes a short address the same way.
  - **The dashboard's Browse templates link** is `https://playkeeper.io/t#dashboard=<its address>`. The share page saves the address and goes on to `/templates`. It lands on `/t` because no analytics runs there.
- **Crossplay** comes from checks alone (Checks, below). A template whose last check passed with crossplay on gets the Crossplay feature, a mark on its card's picture instead of a tag, and a line on its page, and searching Bedrock or GeyserMC finds it. The build refuses `crossplay` in `cards.json` or `addonTags`.
- **A category's page** lists its templates, with search and filters from seven. `pages/templates/<category>.html` (with `layout: category`) is that category's guide, shown under its templates; a category without one needs a `description` in `taxonomy.json`. Search engines index a category's page once it has a guide or three templates.
- **A template's page** is made from the template and its check: what it installs, with each add-on's version, licence and downloads, its rules and memory, when and on which release it started, and the same server with Docker. It's kept out of search engines, so a variant never competes with its category, unless `pages/templates/<category>/<id>.html` (with `layout: template`) adds notes of its own, which the 60% rule above then covers. A modpack's template shows its pack as it installed, and for a Modrinth pack the same `TYPE=MODRINTH` Docker command the pack pages give.
- **At scale:** `TestTheDirectoryScalesToHundredsOfTemplates` builds the site with 600 more templates made from the real ones' add-ons and checks, some without a check or with a failing one, and fails when a page grows past its size, the index past 700 bytes a template, a template without a passing check is listed, or a listed one can't be reached from `/templates` by links. `PK_DIRECTORY_PREVIEW=<dir> go test -run TestWriteDirectoryPreview ./internal/site` writes that data out, to look at in a browser.

**Checks.** `cmd/template-check` creates a server from a template on a running Playkeeper, starts it, reads its log and its add-ons, and removes it. A template passes when its server reaches Done with every add-on installed and nothing failing to load. The command writes `data/checks/<id>.json`, which nobody edits by hand. Its facts are what a template's page and its category's guide show: the Playkeeper release and server build, the seconds its log took to say Done, each add-on as it installed with its version, licence (the SPDX id its source lists) and downloads, and the day. They get Java and Java's share of the memory from the product (`minecraft.JavaFor`, `minecraft.HeapFor`), and the Docker command sizes the container the way Playkeeper does and installs the template's pinned versions.

- **Check a template before its PR,** against `playkeeper dev` on a release: `make template-check SOCKET=/tmp/pk/agent.sock ARGS="-only towny -write -pin"`.
  - `-write` records the check.
  - `-pin` rewrites the template's add-ons to the exact versions that installed.
  - A plugin another one needs, like Vault, goes in the template, not in a note on the page.
- **Templates pin every add-on,** so a passing check stays true until a pin changes. `-bump` tries each add-on's newest version, and pins the new versions if the template still passes. The scheduled template agent runs it weekly and opens a PR with what changed.
- **CI (`.github/workflows/templates.yml`)** is a scheduled job that checks templates on the latest release:
  - **Nightly and on each release:** all of them. It opens an issue naming any that fail, and the template agent fixes their pins or holds them.
  - **On a PR:** only the templates it adds or changes. It fails when `data/checks` doesn't list the versions that installed.
- **A failing check holds its template:** no card or page links it (`TestNoPageOpensAHeldTemplate`), and the directory leaves it out. A guide needs a passing check, so fix the template or take its guide down in the same PR.
- **Crossplay:** on a release that has it (0.4.4 and later), a passing check also turns crossplay on and records `crossplay: true` when Geyser and Floodgate start beside the template's add-ons. `TemplateCard.Crossplay` carries it, for the directory's Crossplay feature and mark. A crossplay that doesn't turn on never fails the template, but `-verify` fails a PR whose check says otherwise.
- **The build refuses** a check whose add-ons aren't exactly the template's in its order, or that doesn't say which release, build and start time it ran with, and a guide for a modpack's template, whose page is under `/modpacks`.
- **A guide** is `pages/templates/<category>.html`, and its `data/library/<name>.json` names the template it describes (the seven that came first are named after their category). It copies the closest one and keeps the blocks in `layouts/library.html` (facts and Docker). What the plugins make you do first, with the commands and defaults from their own configs, is what it's for, and the 60% rule above holds for guides too.
- **Social previews** carry the guides' headings: `node site-og.mjs templates template-<id>`. Each other category page and each template's page (and its `/t/<id>`, which Copy link shares) gets its own, drawn while the site builds (`internal/site/previews.go`): what it is, its name, what it runs on and its memory, and the same picture as its card, its thumbnail once it has one. They're drawn on `og/frame.png` (`node site-og.mjs frame`) in Inter, cut down to Latin letters (`og/`, SIL Open Font License). `go run ./cmd/site -previews=false` builds without them, as the tests do, and those pages then share `og/templates.png`.

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
curl -s https://playkeeper.io/install    # the lines of install.sh, which download https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
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
5. Check it. On your computer, `curl -s -o /dev/null -w '%{http_code}\n' https://playkeeper.io/install/check` prints `200`. Then, in Coolify, open the application's **Terminal** (or **Terminal** in the sidebar), choose the site's running container, select **Connect** and run `tail -n 1 /var/log/playkeeper/installs-*.log`: the line has today's date, your computer's address and `GET /install/check 200`. After the next deploy, the same command still shows it.

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

- **A new Playkeeper release:** nothing to do for `/install`, which always runs the latest release's `get.sh`. The site's docs, version and server types come from the repository, so deploy again once the release is on `main`.
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
