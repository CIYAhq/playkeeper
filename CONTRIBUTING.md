# Contributing to Playkeeper

Thanks for helping make self-hosted game servers easier. **This repository is private.** Public contribution instructions will be updated when the repository opens.

## First steps

1. Read [README.md](README.md), [CURRENT_STATE.md](CURRENT_STATE.md), and the [product brief](docs/PRODUCT.md). The [stack decision](docs/decisions/0002-stack.md) explains how the pieces fit.
2. Pick one concrete user-facing outcome. Open or discuss an issue before large architecture changes; ordinary fixes can go straight to a focused PR.
3. Make the smallest coherent change, add tests for changed behavior, and run the checks below. If a check cannot run in your environment, say so in the PR rather than claiming a green build.
4. In your PR, say what changed, how you tested it, what you *didn't* test, and include screenshots for UI changes. Do not include real worlds, player data, credentials, or public server addresses.

## Set up and check (stock Ubuntu 24.04)

```bash
sudo apt-get install -y git make curl ca-certificates xz-utils   # only if missing
git clone https://github.com/CIYAhq/playkeeper.git && cd playkeeper
./scripts/setup.sh     # pinned Go 1.27.1 + Node 24.21.0 into .tools/ (checksum-verified), npm ci
make check             # gofmt, go vet, ESLint, TypeScript, Go and web unit tests
```

CI runs the same commands (`.github/workflows/ci.yml`: `./scripts/setup.sh`, `make check`, `make lint-sh`, `make package`), then installs the packaged tarball on fresh runners for the end-to-end jobs.

## Run it

- `make dev` — agent and panel in one process with state in `.dev/`, using your Docker daemon (your user must be able to reach `/var/run/docker.sock`). Open the printed `https://localhost:8443/setup#code=…` link. Without Docker the UI still runs and honestly reports Docker as unavailable.
- `cd web && npm run dev` — Vite dev server on port 5173 that proxies `/api` to a running `make dev`.
- `make package` — release tarball in `dist/` (static binary with the embedded UI, installer, notes).
- `make e2e-vm` — full rehearsal in fresh KVM guests (needs `/dev/kvm`, qemu, cloud-image-utils, sudo; see `scripts/e2e/vm-e2e.sh`).

Protocol-bot tests need offline mode, which only the test harness enables (`PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1` on the agent). Never set it on a real server.

## Where things live

| Path | What |
| --- | --- |
| `cmd/playkeeper` | the single binary (install, agent, panel, recovery commands) |
| `internal/agent` | root agent: socket API, Docker lifecycle, collector, backups/restore |
| `internal/panel` | HTTPS panel: auth, sessions, CSRF, rate limits, API proxy |
| `internal/install` | preflight, installer with rollback, uninstall |
| `internal/backup`, `internal/minecraft`, `internal/docker` | archive format, Minecraft protocols and log parsing, Docker client |
| `web/` | React + TypeScript UI (embedded at build time) |
| `test/e2e/` | API client, scenario driver, protocol bot, Playwright specs |

## Design principles

- A first-time user should know what to do next without reading an infrastructure manual.
- No simulated uptime/player analytics shown as real data. Label missing data and collection gaps.
- Backups must be restorable; an on-host-only archive is **not** disaster recovery.
- Default to one host and one game server. Avoid generic plugin systems or provider integrations until they solve an observed need.
- The management plane must not expose a Docker socket, host shell, or unauthenticated/RCON service.
- The installer must preflight and refuse collisions; do not take over existing Crafty or systemd-managed worlds.
- Keep docs short, task-oriented, and tested against the released artifact. Prefer one clear happy path with honest recovery instructions over many speculative options.

## Licence and conduct

An open-source licence is not chosen yet; no permission to copy third-party source or distribute a fork is implied. The owner will choose a licence **before** accepting outside code contributions or making the repo public. Treat others respectfully; technical disagreement is welcome, harassment is not. A fuller governance policy can follow actual contributor demand, not precede it.
