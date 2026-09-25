# Contributing to Playkeeper

Thanks for helping make self-hosted game servers easier. Outside contributions are welcome: bug reports, fixes, documentation, and features that fit the [product brief](docs/PRODUCT.md). Contributions are accepted under the project's licence, AGPL-3.0 (see [Licence and conduct](#licence-and-conduct)).

## First steps

1. Read [README.md](README.md) and the [product brief](docs/PRODUCT.md). The [architecture](docs/ARCHITECTURE.md) and the [stack decision](docs/decisions/0002-stack.md) explain how the pieces fit.
2. Pick one concrete user-facing outcome. Open or discuss an issue before large architecture changes; ordinary fixes can go straight to a focused PR.
3. Fork the repository, make the smallest coherent change on a branch, add tests for changed behavior, and run the checks below. If a check cannot run in your environment, say so in the PR rather than claiming a green build.
4. In your PR, say what changed, how you tested it, what you *didn't* test, and include screenshots for UI changes. Do not include real worlds, player data, credentials, or public server addresses.

Found a security problem? Report it privately as [SECURITY.md](SECURITY.md) describes, not in an issue or pull request.

## Set up and check (stock Ubuntu 24.04)

```bash
sudo apt-get update && sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh     # pinned Go 1.27.1 + Node 24.21.0 into .tools/ (checksum-verified), npm ci
make check             # gofmt, go vet, ESLint, TypeScript, Go, web and installer-script unit tests
```

CI runs the same commands (`.github/workflows/ci.yml`: `./scripts/setup.sh`, `make check`, `make lint-sh`, `make package`), then installs the packaged tarball on fresh runners for the end-to-end jobs. One of them installs the current release, upgrades it to the commit under test with the one-line installer, updates it from the dashboard, and forces a failed update to check the rollback; its test releases are signed with a key made for that run (`scripts/e2e/update-releases.sh`). Two more jobs get certificates from Pebble, Let's Encrypt's test certificate authority, and build and check the names service image. Pull requests from forks run the same CI; a maintainer may need to approve a first-time contributor's run.

## Run it

- `make dev` — agent and panel in one process with state in `.dev/`, using your Docker daemon. Run it as your normal user with access to `/var/run/docker.sock` (the `docker` group), not as root: the server container runs as the calling user. Open the printed `https://localhost:8443/setup#code=…` link. Without Docker the UI still runs and honestly reports Docker as unavailable.
- `cd web && npm run dev` — Vite dev server on port 5173 that proxies `/api` to a running `make dev`.
- `make package` — release tarball in `dist/` (static binary with the embedded UI, installer, notes), plus the other assets a release would carry: `get.sh`, the tarball under its stable name, and the release manifest `playkeeper-release.json`, which the release workflow signs.
- `make e2e-vm` — full rehearsal in fresh KVM guests (needs `/dev/kvm`, qemu, cloud-image-utils, sudo; see `scripts/e2e/vm-e2e.sh`).
- `./scripts/negative-controls.sh` — removes each safety guard in turn in a throwaway worktree and checks that the test covering it fails.
- `go test -count=1 -run '^TestPebble$' ./internal/certs/` — gets certificates through HTTP-01 and DNS-01 checks from Pebble and `pebble-challtestsrv`, found in `$PLAYKEEPER_PEBBLE_DIR` or on `$PATH`; without them the test is skipped and prints how to install the pinned version (as the `acme-pebble` job in `.github/workflows/ci.yml` does).
- `scripts/names-check.sh` and `go test ./internal/names/...` — build the names service image and check it, and test its client and service against a fake Cloudflare, without contacting Cloudflare. Deploying and running the service is described in [services/names/README.md](services/names/README.md).
- In `make dev`, **Machine settings › Address** talks to a names service at `http://127.0.0.1:8081` and to Let's Encrypt's staging certificate authority, never the real names service or real certificates, unless `"namesURL"` or `"acmeDirectoryURL"` in `.dev/config.json` names others (`make dev` fills in the two when they are empty). With nothing on port 8081, the page says the names service can't be reached. To have one answer, run the names service image as `scripts/names-check.sh` runs it (port 8081, a dummy Cloudflare token, `api.cloudflare.com` pointed at the container): it answers whether names are free and refuses claims from a private address, so nothing reaches Cloudflare or Let's Encrypt.
- `scripts/site-check.sh` — builds the playkeeper.io container from `site/` and checks `/`, `/healthz` and the `/install` redirect (needs Docker; CI runs it too). Hosting it is described in [site/README.md](site/README.md).
- `make notices` — regenerates `THIRD_PARTY_NOTICES`, the licence texts of the third-party code in the binary. Run it after changing Go or npm dependencies and commit the result; `make check` fails while it is out of date.

Protocol-bot tests need offline mode, which only the test harness enables (`PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1` on the agent). Never set it on a real server.

## Releases (maintainers)

**Once, before the first signed release,** make the release signing key and store it as the repository secret the release workflow signs with:

```bash
go run ./cmd/release-sign keygen | gh secret set PLAYKEEPER_RELEASE_SIGNING_KEY --repo CIYAhq/playkeeper
git add internal/update/release.pub && git commit -m "Add the release signing key" && git push
```

`keygen` adds the public key to `internal/update/release.pub`, which is compiled into every build, and writes the private key only to standard output, here straight into the secret. Keep an offline copy in a password manager if you want one; nothing else needs it. Installed versions only install updates signed with a key compiled into them, and the release workflow refuses to sign with a key missing from the released commit's `release.pub`, so every release can verify the next one. Replacing the key takes two releases; see the known limitations in [SECURITY.md](SECURITY.md).

**Each release:** add a `## MAJOR.MINOR.PATCH` section to [CHANGELOG.md](CHANGELOG.md) saying what changed (the dashboard shows it when it offers the update), then push a `vMAJOR.MINOR.PATCH` tag. That runs [the release workflow](.github/workflows/release.yml): `make check` and `make package` with the version from the tag, the signature of `playkeeper-release.json`, an install of the built assets on a fresh runner, then a normal (not pre-release) GitHub release with five assets, `get.sh`, `playkeeper-linux-amd64.tar.gz` and its `.sha256`, and `playkeeper-release.json` and its `.sig`, which becomes the latest release. Installed versions from 0.2.0 on offer it in the dashboard within 12 hours. The workflow then runs the one-line install from the GitHub release URL on a fresh runner and checks that `https://playkeeper.io/install`, a redirect to the latest release's `get.sh`, resolves to the new one; that last check only warns, because the site runs on its own server and needs no redeploy for a release. 0.x releases are labelled early. For a dry run, start the release workflow by hand or open a pull request that touches the release path: it builds and checks everything, signs with a throwaway key, and uploads the assets as an artifact instead of releasing them.

## Where things live

| Path | What |
| --- | --- |
| `cmd/playkeeper` | the single binary (install, agent, panel, recovery commands) |
| `internal/agent` | root agent: socket API, Docker lifecycle, collector, backups/restore |
| `internal/panel` | HTTPS panel: auth, sessions, CSRF, rate limits, API proxy |
| `internal/install` | preflight, installer with rollback, in-place upgrade, the updater, uninstall |
| `internal/update`, `cmd/release-sign` | signed release manifests (the release key is in `internal/update/release.pub`), version order, update downloads; the maintainer tool that makes and signs them |
| `internal/backup`, `internal/minecraft`, `internal/docker` | archive format, Minecraft protocols, log parsing and PaperMC's version list, Docker client |
| `internal/certs` | the machine's certificates: Let's Encrypt (ACME) with HTTP-01 and DNS-01 checks, DNS checks of an own domain, join addresses and their records, renewal |
| `internal/names`, `cmd/playkeeper-names`, `services/names` | the client for free `playkeeper.io` names and its signed requests; the names service, its image and how to run it (not part of the release) |
| `internal/twofactor`, `internal/totp`, `internal/qrcode` | two-factor sign-in rules and recovery codes, authenticator codes, the setup QR code |
| `web/` | React + TypeScript UI (embedded at build time) |
| `packaging/` | `install.sh`, the one-line installer `get.sh`, their tests, install notes |
| `scripts/` | toolchain setup, packaging, release checks, site check, KVM rehearsal harness, negative controls |
| `site/` | the playkeeper.io page and its nginx container (hosted with Coolify) |
| `test/e2e/` | API client, scenario driver, protocol bot, Playwright specs |
| `.github/workflows/` | CI and the release workflow |

## Design principles

- A first-time user should know what to do next without reading an infrastructure manual.
- No simulated uptime/player analytics shown as real data. Label missing data and collection gaps.
- Backups must be restorable; an on-host-only archive is **not** disaster recovery.
- Default to one host and one game server. Avoid generic plugin systems or provider integrations until they solve an observed need.
- The management plane must not expose a Docker socket, host shell, or unauthenticated/RCON service.
- The installer must preflight and refuse collisions; do not take over existing Crafty or systemd-managed worlds.
- Keep docs short, task-oriented, and tested against the released artifact. Prefer one clear happy path with honest recovery instructions over many speculative options.

## Licence and conduct

Playkeeper is licensed under the [GNU AGPL v3.0 only](LICENSE). By opening a pull request you agree that your contribution is licensed under the same terms; there is no separate contributor agreement. Only submit work you wrote or have the right to contribute under that licence, and keep existing copyright and licence notices intact.

Treat others respectfully; technical disagreement is welcome, harassment is not. A fuller governance policy can follow actual contributor demand, not precede it.
