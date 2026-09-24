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

CI runs the same commands (`.github/workflows/ci.yml`: `./scripts/setup.sh`, `make check`, `make lint-sh`, `make package`), then installs the packaged tarball on fresh runners for the end-to-end jobs. Pull requests from forks run the same CI; a maintainer may need to approve a first-time contributor's run.

## Run it

- `make dev` — agent and panel in one process with state in `.dev/`, using your Docker daemon. Run it as your normal user with access to `/var/run/docker.sock` (the `docker` group), not as root: the server container runs as the calling user. Open the printed `https://localhost:8443/setup#code=…` link. Without Docker the UI still runs and honestly reports Docker as unavailable.
- `cd web && npm run dev` — Vite dev server on port 5173 that proxies `/api` to a running `make dev`.
- `make package` — release tarball in `dist/` (static binary with the embedded UI, installer, notes), plus the one-line installer assets a release would carry (`get.sh` and the tarball under its stable name).
- `make e2e-vm` — full rehearsal in fresh KVM guests (needs `/dev/kvm`, qemu, cloud-image-utils, sudo; see `scripts/e2e/vm-e2e.sh`).
- `./scripts/negative-controls.sh` — removes each safety guard in turn in a throwaway worktree and checks that the test covering it fails.
- `scripts/build-site.sh packaging/get.sh _site` — builds the playkeeper.io page into `_site/`. The published site serves the latest release's `get.sh` as `/install`.

Protocol-bot tests need offline mode, which only the test harness enables (`PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1` on the agent). Never set it on a real server.

## Releases (maintainers)

Pushing a `vMAJOR.MINOR.PATCH` tag runs [the release workflow](.github/workflows/release.yml): `make check` and `make package` with the version from the tag, an install of the built assets on a fresh runner, then a normal (not pre-release) GitHub release with `get.sh`, `playkeeper-linux-amd64.tar.gz` and its `.sha256`, which becomes the latest release. It then redeploys playkeeper.io through [the Pages workflow](.github/workflows/pages.yml) and runs the one-line install against `https://playkeeper.io/install` on a fresh runner. 0.x releases are labelled early. For a dry run, start the release workflow by hand or open a pull request that touches the release path: it builds and checks everything and uploads the assets as an artifact instead of releasing them.

## Where things live

| Path | What |
| --- | --- |
| `cmd/playkeeper` | the single binary (install, agent, panel, recovery commands) |
| `internal/agent` | root agent: socket API, Docker lifecycle, collector, backups/restore |
| `internal/panel` | HTTPS panel: auth, sessions, CSRF, rate limits, API proxy |
| `internal/install` | preflight, installer with rollback, uninstall |
| `internal/backup`, `internal/minecraft`, `internal/docker` | archive format, Minecraft protocols and log parsing, Docker client |
| `web/` | React + TypeScript UI (embedded at build time) |
| `packaging/` | `install.sh`, the one-line installer `get.sh`, their tests, install notes |
| `scripts/` | toolchain setup, packaging, release checks, site build, KVM rehearsal harness, negative controls |
| `site/` | the playkeeper.io page (GitHub Pages) |
| `test/e2e/` | API client, scenario driver, protocol bot, Playwright specs |
| `.github/workflows/` | CI, the release workflow and the Pages deployment |

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
