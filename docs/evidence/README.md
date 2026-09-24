# Evidence

Raw text output from the runs summarised in [CURRENT_STATE.md](../../CURRENT_STATE.md). Screenshots and the two walkthrough videos are attached to [PR #1](https://github.com/CIYAhq/playkeeper/pull/1). One-time setup codes and lab passwords are redacted; guests use the documentation range 198.51.100.0/24 (the lab bridge), Docker bridge addresses are replaced with `<private-ip>`, and the public addresses of upstream services that appear in error messages with `<upstream-ip>`.

## `vm-rehearsal/` — run 7, the tested commit `0c99fa7` (`scripts/e2e/vm-e2e.sh`)

Fresh guests from official Ubuntu cloud images (2 vCPU, 3 GB RAM, 20 GB disk unless noted) on the Cloud Agent VM, one at a time, reached over a private bridge from the Cloud Agent VM acting as the admin's computer and as the players' network. Only the release tarball (or, for host C, the one-line installer) reached each guest. These are rehearsals for a VPS, not provider hosts. Run 7 stopped twice on bugs in the rehearsal script, not the product: at host B's blocked-egress report (fixed in `f2e1bde`, resumed from host B) and at host C's secret comparison, which ran before host C's server existed (fixed in `eed3013`, resumed from host C). Each resume used the same tarball; `run-log.txt` shows all three parts.

| Files | What they show |
| --- | --- |
| `artifact.sha256`, `artifact-contents.txt` | the tarball tested and its contents (no jars, keys, databases, `.git` or `node_modules`) |
| `host-a-facts.txt`, `host-a-preflight.txt` | guest details; read-only preflight |
| `host-a-decline.txt`, `host-a-rollback.txt`, `host-a-snapshot-*` | answering "n" and a failure injected mid-install both leave packages, users, groups, units, paths, containers and listeners identical |
| `host-a-install.txt`, `host-a-install-manifest.json`, `host-a-tls.txt`, `host-a-cookie.txt` | installer transcript and timing; what the install recorded for uninstall; certificate fingerprint check, TLS 1.3, plain HTTP refused; cookie flags and security headers on a real sign-in |
| `host-a-onboarding.txt` | keyboard-only browser onboarding, first sign-in to joinable; join address copied from the UI |
| `host-a-scenario.txt`, `host-a-*.json` (results, marker, status with two players, audit log, daily summary) | two protocol bots: marker placement, both online, sessions/events/charts, console literal text, a backup with a player online checked against the server log, an independent per-file archive check, a same-host restore and rollback, controls, CSRF refusals, settings, audit log, daily summary (72 checks) |
| `host-a-save-before-stop.txt`, `host-a-containers.txt` | the world is saved before every stop; one container after all of that |
| `host-a-operate.txt`, `ui-10-browser-download.txt` | console command, backup and download done in the browser only |
| `host-a-views.txt` | every view at 1440×900 and 390×844 with axe, keyboard and reduced-motion checks |
| `host-a-nmap.txt`, `host-a-privileges.txt` | ports seen from another host; panel/agent/container privileges; allowlist probes; container settings; bStats off; no IPs or secrets stored |
| `host-a-ground-truth.txt`, `host-a-agent-restart.txt` | overview numbers next to `docker stats`, `df`, `docker inspect`, the server log and `list`; an agent restart adds no events or sessions |
| `host-a-agent-down.txt`, `host-a-crash.txt`, `host-a-port-collision.txt`, `host-a-low-disk.txt`, `host-a-reboot.txt` | error states and recovery; a crash with a player online ends the session as uncertain |
| `host-a-uninstall.txt`, `host-a-data-*.txt`, `host-a-reinstall.txt` | uninstall keeps worlds and backups byte-identical; reinstall reuses them |
| `host-b-*.txt` | existing `minecraft.service`, Crafty-style directory, Minecraft-named Docker container and ufw: low disk and collisions refused with fixes; coexistence with `--allow-existing-minecraft`; blocked outbound HTTPS and recovery; purge needs the typed phrase; fixtures untouched throughout |
| `host-c-oneliner-*.txt`, `host-c-snapshot-*`, `host-c-install.txt` | one-line install from a local HTTP mirror: wrong checksum, changed tarball, missing `.sha256` and missing terminal refused with the host unchanged; then the install with the question answered in a terminal |
| `host-c-refusals.txt`, `host-c-restore-ui.txt`, `host-c-verify.txt`, `host-c-restore-preview.json`, `host-c-host-b-*-results.json` | second host (the scenario's `host-b` role runs on guest C): wrong setup code, EULA and corrupted-archive refusals, browser restore, marker verified by console and client |
| `host-c-tamper.txt`, `host-c-secrets.txt` | truncated, `../`, absolute-path and modified archives refused while the live world stays byte-identical; host C's secrets differ from host A's |
| `host-lowmem-*`, `host-jammy-*`, `containers-preflight.txt` | a 2 GB guest, an Ubuntu 22.04 guest and systemd-less Ubuntu 24.04 and Debian 12 containers are refused by preflight, with the guests unchanged |
| `dns-queries.txt` | every name each guest looked up during the run: Playkeeper and Paper used Docker Hub (registry, auth and its S3 blob storage), PaperMC (`fill`, `fill-data`) and Mojang (`piston-data`, `api.minecraftservices.com`); the rest are Ubuntu's own background services and apt; no `bstats.org`, no `raw.githubusercontent.com` |
| `run-log.txt` | the whole run in order |

## `vm-default-online/` — shipped defaults (`scripts/e2e/vm-default-online.sh`)

A fresh guest installed from the same tarball without the test-harness flag: `online-mode=true`, allowlist enforced, pinned `VERSION`, no third-party config downloads, bStats off, and a non-genuine client (`PkBotNoAuth`, not a Mojang account) refused by Mojang authentication.

## `contributor/`

The README's contributor commands in a stock `ubuntu:24.04` container as a normal user, from a git bundle of the PR head at `eed3013` (same product code as `0c99fa7`; the repository is private, so the container has no GitHub credentials): setup 7 s, `make check` 84 s, `make lint-sh`, `make package` 17 s, a `make dev` smoke test (agent and panel start from the fresh checkout and the panel answers `/api/health`; the container has no Docker) and `./scripts/negative-controls.sh` (all 12 guards caught, 72 s); 202 s in total.

## `resources/`

Guest memory and disk during play with two bots, measured in run 5 (build `f957b08`).

## CI

[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) repeats install, play, backup, the one-line install and second-host restore on fresh GitHub-hosted runners for every PR head; its logs and `evidence-host-a` / `evidence-host-b` artifacts are on the Actions tab.
