# Contributing to Playkeeper

Thanks for helping make self-hosted game servers easier. **This repository is currently private and contains a brief, not a runnable product.** Public contribution instructions will be updated when the repository opens.

## First steps

1. Read [README.md](README.md), [CURRENT_STATE.md](CURRENT_STATE.md), and the [product brief](docs/PRODUCT.md).
2. Pick one concrete user-facing outcome. Open or discuss an issue before large architecture changes; ordinary fixes can go straight to a focused PR once code exists.
3. Make the smallest coherent change, add tests for changed behavior, and run the documented checks. If checks do not exist yet, state that plainly rather than claiming a green build.
4. In your PR, say what changed, how you tested it, what you *didn't* test, and include screenshots for UI changes. Do not include real worlds, player data, credentials, or public server addresses.

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
