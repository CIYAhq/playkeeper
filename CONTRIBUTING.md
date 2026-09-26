# Contributing to Playkeeper

Thanks for helping make self-hosted game servers easier. Outside contributions are welcome: bug reports, fixes, documentation, and features that fit the [product brief](docs/PRODUCT.md). Contributions are accepted under the project's licence, AGPL-3.0 (see [Licence and conduct](#licence-and-conduct)).

## First steps

1. Read [README.md](README.md) and the [product brief](docs/PRODUCT.md). The [architecture](docs/ARCHITECTURE.md) and the [stack decision](docs/decisions/0002-stack.md) explain how the pieces fit.
2. Pick one concrete user-facing outcome. Open an issue or start a thread in [GitHub Discussions](https://github.com/CIYAhq/playkeeper/discussions) before large architecture changes; ordinary fixes can go straight to a focused PR.
3. Fork the repository, make the smallest coherent change on a branch, add tests for changed behavior, and run the checks below. If a check cannot run in your environment, say so in the PR rather than claiming a green build.
4. In your PR, say what changed, how you tested it, what you *didn't* test, and include screenshots for UI changes. Do not include real worlds, player data, credentials, or public server addresses.

Found a security problem? Report it privately as [SECURITY.md](SECURITY.md) describes, not in an issue or pull request.

## Set up and check (stock Ubuntu 24.04)

```bash
sudo apt-get update && sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh     # pinned Go 1.27.1 + Node 24.21.0 into .tools/ (checksum-verified), npm ci
make check             # gofmt, go vet, ESLint (UI and browser tests), TypeScript, Go, web and installer-script unit tests
```

CI runs the same commands (`.github/workflows/ci.yml`: `./scripts/setup.sh`, `make check`, `make lint-sh`, `make package`), then installs the packaged tarball on fresh runners for the end-to-end jobs. One of them installs the current release, upgrades it to the commit under test with the one-line installer, updates it from the dashboard, and forces a failed update to check the rollback; its test releases are signed with a key made for that run (`scripts/e2e/update-releases.sh`). Other jobs get certificates from Pebble, Let's Encrypt's test certificate authority, build and check the names service image, and build the playkeeper.io image and walk its live demo. Pull requests from forks run the same CI; a maintainer may need to approve a first-time contributor's run.

## Run it

- `make dev` — agent and panel in one process with state in `.dev/`, using your Docker daemon. Run it as your normal user with access to `/var/run/docker.sock` (the `docker` group), not as root: the server container runs as the calling user. Open the printed `https://localhost:8443/setup#code=…` link. Without Docker the UI still runs and honestly reports Docker as unavailable.
- `cd web && npm run dev` — Vite dev server on port 5173 that proxies `/api` to a running `make dev`. Start `make dev` first: the dev server then serves HTTPS with its certificate (`.dev/data/panel/tls/`), which signing in needs; without it, the page is plain HTTP and the panel refuses the sign-in.
- `cd web && npx vite --mode demo` — the live demo (the dashboard with sample data from `web/src/demo/`, no panel behind it) at `http://localhost:5173/demo/` while you edit; `npm run build:demo` builds it into `web/dist-demo/`.
- The click-through presses every button, link, switch, tab, slider and menu item on every page at desktop and phone sizes, then again on the pages that change when a server is stopped, crashed or busy, when there's nothing to list, when an update is waiting, when the disk has space to free, when plugins, packs and pre-generation are in use or paused, when friends, a team and Discord are set up, when the map is on or waits for a restart, at two-factor sign-in's second step and before setup (states laid over the panel's real answers in `test/e2e/ui/fakes.ts`). It fails with a list of the controls that do nothing visible, answer with an error or have no name, and also when it can't get back to a state it found, when a page has fewer controls than its minimum, or when it misses one of the places listed in `clickthrough.spec.ts`; for each of those places it breaks the control on purpose and checks that the crawl notices. A control that can't do anything right now must be disabled and say why. Changes go to fakes, so nothing on the server changes, apart from one AI agent token it makes so Settings › AI agents has one to open. With `make dev` running and a server like CI's (a running Paper server with players and a backup): `cd test/e2e/ui && npm ci && npx playwright install chromium`, then `PK_URL=https://localhost:8443 PK_PASSWORD=<your admin password> npx playwright test clickthrough.spec.ts --workers=2`; it signs in as `admin`. CI runs it on a fresh install, which has no other machines connected, so the pages of connected machines are crawled only where one is.
- `make package` — release tarball in `dist/` (static binary with the embedded UI, installer, notes), plus the other assets a release would carry: `get.sh`, the tarball under its stable name, and the release manifest `playkeeper-release.json`, which the release workflow signs.
- `make web`, then in `test/e2e/ui`: `npm ci`, `npx playwright install chromium` and `npx playwright test -c playwright.fake-panel.config.ts` — browser checks against a faked panel API (`fake-panel.ts`), so they need no agent, Docker or Minecraft server: the Console with a long log arriving, and a backup file uploaded from the World tab and as a new server, at desktop and phone sizes. CI runs them too.
- `make e2e-vm` — full rehearsal in fresh KVM guests (see `scripts/e2e/vm-e2e.sh`). It needs `qemu-system-x86`, `qemu-utils`, `cloud-image-utils` and Docker, sudo without a password, read and write access to `/dev/kvm` for your own user (qemu runs without sudo), and Playwright's Chromium (`cd test/e2e/ui && npm ci && npx playwright install --with-deps chromium`). It leaves a bridge, `pkbr0`, and iptables rules that forward and NAT 198.51.100.0/24 on your machine. The **VM rehearsal** workflow (`.github/workflows/vm-rehearsal.yml`) runs it on a GitHub-hosted runner when started by hand, before a release.
- `./scripts/negative-controls.sh` — removes each safety guard in turn in a throwaway worktree and checks that the test covering it fails. It tests the last commit, so commit first.
- `go test -count=1 -run '^TestPebble$' ./internal/certs/` — gets certificates through HTTP-01 and DNS-01 checks from Pebble and `pebble-challtestsrv`, found in `$PLAYKEEPER_PEBBLE_DIR` or on `$PATH`; without them the test is skipped and prints how to install the pinned version (as the `acme-pebble` job in `.github/workflows/ci.yml` does).
- `scripts/names-check.sh` and `go test ./internal/names/...` — build the names service image and check it, and test its client and service against a fake Cloudflare, without contacting Cloudflare. Deploying and running the service is described in [services/names/README.md](services/names/README.md).
- In `make dev`, **Machine settings › Address** talks to a names service at `http://127.0.0.1:8081` and to Let's Encrypt's staging certificate authority, never the real names service or real certificates, unless `"namesURL"` or `"acmeDirectoryURL"` in `.dev/config.json` names others (`make dev` fills in the two when they are empty). With nothing on port 8081, the page says the names service can't be reached. To have one answer, run the names service image as `scripts/names-check.sh` runs it (port 8081, a dummy Cloudflare token, `api.cloudflare.com` pointed at the container): it answers whether names are free and refuses claims from a private address, so nothing reaches Cloudflare or Let's Encrypt.
- `scripts/site-check.sh` — builds the playkeeper.io image (from the repository root, since it builds the live demo from `web/`) and checks `/`, the `/sizing` guide, `/demo/`, the `/t` template page, `/healthz` and the `/install` redirect (needs Docker; CI runs it too). Hosting it is described in [site/README.md](site/README.md).
- `make notices` — regenerates `THIRD_PARTY_NOTICES`, the licence texts of the third-party code in the binary. Run it after changing Go or npm dependencies and commit the result; `make check` fails while it is out of date, and while [docs/THIRD_PARTY.md](docs/THIRD_PARTY.md) doesn't list each Go module and npm package in it at its version.
- `make sizing` — regenerates the sizing guide on playkeeper.io (`site/sizing.html` and `site/sizing-data.js`) from `internal/sizing` and `site/sizing.html.tmpl`. Run it after changing either and commit the result; `make check` fails while they are out of date.

Protocol-bot tests need offline mode, which only the test harness enables (`PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1` on the agent). Never set it on a real server. The harness sets it in `/etc/systemd/system/playkeeper-agent.service.d/e2e-offline.conf`. `playkeeper uninstall` keeps that file, even with `--purge`, because Playkeeper did not create it, so `make e2e-vm` removes it after uninstalling; if you add it by hand, remove it yourself or the next install on that machine starts in offline mode. Likewise, `PLAYKEEPER_E2E_DISCORD_URL_UNSAFE` sends the agent's Discord requests to a fake endpoint on a loopback address, for tests only; the agent refuses any other address and logs a warning while it is set.

## Releases (maintainers)

**Once, before the first signed release,** make the release signing key and store it as the repository secret the release workflow signs with:

```bash
go run ./cmd/release-sign keygen | gh secret set PLAYKEEPER_RELEASE_SIGNING_KEY --repo CIYAhq/playkeeper
git add internal/update/release.pub && git commit -m "Add the release signing key" && git push
```

`keygen` adds the public key to `internal/update/release.pub`, which is compiled into every build, and writes the private key only to standard output, here straight into the secret. Keep an offline copy in a password manager if you want one; nothing else needs it. Installed versions only install updates signed with a key compiled into them, and the release workflow refuses to sign with a key missing from the released commit's `release.pub`, so every release can verify the next one. Replacing the key takes two releases; see the known limitations in [SECURITY.md](SECURITY.md).

**Optionally, CurseForge's API key.** With a repository secret named `CURSEFORGE_API_KEY`, release builds carry CurseForge's API key, so owners get CurseForge modpacks without a key of their own; a key an owner adds under Settings › Add-on sources still wins. Request a key in the CurseForge for Studios console (console.curseforge.com), then run `gh secret set CURSEFORGE_API_KEY --repo CIYAhq/playkeeper` and paste it. Only tag pushes pass it to `make package`; pull requests and manual runs build without it, and `scripts/package.sh` never prints it (`make test-sh` checks the wiring with a dummy key). Anyone can pull a key out of a public binary, so first check that CurseForge's terms allow shipping one.

**Each release:** add a `## MAJOR.MINOR.PATCH` section to [CHANGELOG.md](CHANGELOG.md) saying what changed (the dashboard shows it when it offers the update), then push a `vMAJOR.MINOR.PATCH` tag. That runs [the release workflow](.github/workflows/release.yml): `make check` and `make package` with the version from the tag, the signature of `playkeeper-release.json`, an install of the built assets on a fresh runner, then a normal (not pre-release) GitHub release with five assets, `get.sh`, `playkeeper-linux-amd64.tar.gz` and its `.sha256`, and `playkeeper-release.json` and its `.sig`, which becomes the latest release. Installed versions from 0.2.0 on offer it in the dashboard within 12 hours. The workflow then runs the one-line install from the GitHub release URL on a fresh runner and checks that `https://playkeeper.io/install`, a redirect to the latest release's `get.sh`, resolves to the new one; that last check only warns, because the site runs on its own server and needs no redeploy for a release. 0.x releases are labelled early. After each release, move the default version of builds that aren't releases in `scripts/package.sh` to the next version (`0.6.0-dev` once v0.4.0 is out), so it stays above both the latest release and the one after: a build named after a published release sorts below it, and CI's upgrade and "update available" checks then fail. For a dry run, start the release workflow by hand or open a pull request that touches the release path: it builds and checks everything, signs with a throwaway key, and uploads the assets as an artifact instead of releasing them. The site isn't part of the release: after changing `site/`, the sizing guide or the demo, redeploy it as [site/README.md](site/README.md) describes.

## Where things live

| Path | What |
| --- | --- |
| `cmd/playkeeper` | the single binary (install, agent, panel, machine link, MCP over SSH, recovery commands) |
| `internal/agent` | root agent: socket API, Docker lifecycle, collector, backups/restore, schedules, sleep, off-site copies |
| `internal/gamefiles` | reading and writing a server's files, which the game can change, as root: no links or named pipes, capped reads, refusals that name the file |
| `internal/panel` | HTTPS panel: auth and two-factor sign-in, sessions, CSRF, rate limits, team roles, API proxy (to this machine and joined ones), public routes (resource packs, invite, map and pack pages), AI agent tokens and `/mcp` |
| `internal/api`, `internal/agentclient`, `internal/config`, `internal/store`, `internal/version` | the JSON shapes the agent, panel and UI share (`web/src/api/types.ts` mirrors them; keep them in sync); talking to an agent over its socket or a joined machine's link; the host config; SQLite with append-only migrations; the build's version |
| `internal/install` | preflight, installer with rollback, in-place upgrade, the updater, joining a machine, uninstall |
| `internal/update`, `cmd/release-sign` | signed release manifests (the release key is in `internal/update/release.pub`), version order, update downloads; the maintainer tool that makes and signs them |
| `internal/backup`, `internal/backup/retention` | archive format; which backups the backup rules keep |
| `internal/offsite` | encrypted copies of backups on S3-compatible storage or over SFTP |
| `internal/minecraft`, `internal/minecraft/software`, `internal/docker` | Minecraft protocols, log parsing, PaperMC's version list and the Java for each version; downloads of the other server types; Docker client |
| `internal/addons`, `internal/curated` | the plugin and mod library (Modrinth, Hangar); the hand-picked add-ons and voice chat's port |
| `internal/modpacks`, `internal/templates` | modpacks from Modrinth and CurseForge, and the friends' pack page; server templates as files and links |
| `internal/pregen`, `internal/packs`, `internal/webmap` | map pre-generation with Chunky; resource and data packs; the live map with squaremap |
| `internal/worldimport`, `internal/nbt`, `internal/zipdir` | uploaded worlds; reading Minecraft's NBT files; checking zip archives |
| `internal/portshare` | the panel's HTTPS and the resource packs' plain HTTP on one port |
| `internal/diagnose` | how a server is running, what slows it down and why it crashed |
| `internal/schedule`, `internal/sleep` | scheduled tasks; sleeping when nobody's playing and waking on a join |
| `internal/diskusage` | what fills the disk and the ways to free space |
| `internal/invites`, `internal/mojang` | invite links for friends and team members; Minecraft account lookups by name |
| `internal/discord` | Discord alerts and the live status message |
| `internal/certs` | the machine's certificates: Let's Encrypt (ACME) with HTTP-01 and DNS-01 checks, DNS checks of an own domain, join addresses and their records, renewal |
| `internal/names`, `cmd/playkeeper-names`, `services/names` | the client for free `playkeeper.io` names and its signed requests; the names service, its image and how to run it (not part of the release) |
| `internal/twofactor`, `internal/totp`, `internal/qrcode` | two-factor sign-in rules and recovery codes, authenticator codes, the setup QR code |
| `internal/machinelink` | connecting other machines to one dashboard: join codes, the link each machine dials, and requests through it |
| `internal/mcp`, `internal/mcptools` | the MCP server (JSON-RPC over HTTP and stdio) and Playkeeper's tools for AI agents |
| `internal/sizing`, `cmd/sizing-guide` | how big a VPS to rent for how many players and what they run, with the sources for each number; the tool that writes the sizing guide into `site/` |
| `web/`, `web/src/demo/` | React + TypeScript UI (embedded at build time); the live demo: a make-believe panel in the browser and its sample data |
| `packaging/` | `install.sh`, the one-line installer `get.sh`, their tests, install notes |
| `scripts/` | toolchain setup, packaging, release checks, site and names checks, KVM rehearsal harness, negative controls |
| `site/` | playkeeper.io: the install page, the sizing guide, the template page, the live demo and its nginx container (hosted with Coolify) |
| `test/e2e/` | API client, scenario driver, protocol bot, Playwright specs |
| `.github/workflows/` | CI, the release workflow and the VM rehearsal |

## Design principles

- A first-time user should know what to do next without reading an infrastructure manual.
- No simulated uptime/player analytics shown as real data. Label missing data and collection gaps.
- Backups must be restorable; an on-host-only archive is **not** disaster recovery.
- Keep the simple case simple: one machine and one server need no extra steps. Add integrations only when they solve an observed need.
- The management plane must not expose a Docker socket, host shell, or unauthenticated/RCON service.
- The installer must preflight and refuse collisions; do not take over existing Crafty or systemd-managed worlds.
- Keep docs short, task-oriented, and tested against the released artifact. Prefer one clear happy path with honest recovery instructions over many speculative options.

## Licence and conduct

Playkeeper is licensed under the [GNU AGPL v3.0 only](LICENSE). By opening a pull request you agree that your contribution is licensed under the same terms; there is no separate contributor agreement. Only submit work you wrote or have the right to contribute under that licence, and keep existing copyright and licence notices intact.

Treat others respectfully; technical disagreement is welcome, harassment is not. A fuller governance policy can follow actual contributor demand, not precede it.
