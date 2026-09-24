# Playkeeper

**Your VPS. Your game servers. Your worlds.**

Playkeeper is a planned open-source, self-hosted dashboard for setting up and running game servers on a VPS you already own. We are starting with Minecraft Java. The intended experience is: install on a clean Linux VPS, create a server, invite friends, see real player/server activity, and keep a portable backup you can restore on another machine.

> **Status: foundation only.** There is no working installer, dashboard, Minecraft server, release, or safe production deployment in this repository yet. Do not run this on a VPS expecting it to work. Follow [CURRENT_STATE.md](CURRENT_STATE.md) for observed progress.

## Where to start

- **Using Playkeeper:** Installation steps will appear here only after a fresh-host installation has actually been tested.
- **Building Playkeeper:** Read [CONTRIBUTING.md](CONTRIBUTING.md), then [the product brief](docs/PRODUCT.md) and [the architecture brief](docs/ARCHITECTURE.md). The implementation stack and commands will be documented once code exists.
- **Following the build:** [CURRENT_STATE.md](CURRENT_STATE.md) distinguishes planned, locally tested, and end-to-end verified work.
- **Reporting security issues:** See [SECURITY.md](SECURITY.md); do not post exploitable details publicly.

## First release, not the entire roadmap

One existing Linux VPS; one Minecraft Java server; guided setup; authenticated HTTPS management; real operations and player analytics; portable world backup and a demonstrated restore on a second host. Other games, multiple servers, modpacks, billing, VPS provisioning, and migration of an existing production world come later.

The interface takes *visual inspiration* from [OpenAnalytics](https://github.com/OpenLabs-so/openanalytics) but uses independently written components and original branding. [Ghost](https://github.com/haydenbleasel/ghost) is a product/technical reference, not our codebase or hosting model. See [design](docs/DESIGN.md) and [licensing](docs/LICENSING.md).

**Domain:** [playkeeper.io](https://playkeeper.io) is owned by the project, but this repository does not yet deploy a site there. **Repository visibility:** private while the foundation and licence are settled; open-source release needs an explicit visibility/licensing decision.
