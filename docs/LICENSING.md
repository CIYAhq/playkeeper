# Licensing and references

Playkeeper is free software under the **GNU Affero General Public License, version 3 only** (SPDX `AGPL-3.0-only`). The full text is in [LICENSE](../LICENSE), and every release tarball includes it. What follows is a summary, not legal advice:

- Running Playkeeper on your own server asks nothing of you.
- If you pass Playkeeper on, changed or not, pass on the licence and the corresponding source code with it.
- If you change Playkeeper and let other people use your changed version over a network, offer them the source code of your version (section 13 of the licence).
- Contributions are accepted under the same licence; see [CONTRIBUTING.md](../CONTRIBUTING.md).

Bundled third-party code keeps its own licence; [THIRD_PARTY.md](THIRD_PARTY.md) lists it.

## References

- [Ghost](https://github.com/haydenbleasel/ghost) is an archived MIT-licensed product reference. Its Vercel/Hetzner provisioning model is not our existing-VPS installer, and none of its code is used.
- [OpenAnalytics](https://github.com/OpenLabs-so/openanalytics) (AGPL-3.0, with a separate browser tracker exception) is a *visual reference* only. Playkeeper's components, CSS, assets and branding are written independently; see [DESIGN.md](DESIGN.md).
- [itzg/minecraft-server](https://docker-minecraft-server.readthedocs.io/) is the runtime container (Apache-2.0), pinned by digest and pulled on the user's server.
- [Minecraft EULA](https://www.minecraft.net/en-us/eula) and [usage guidelines](https://www.minecraft.net/en-us/usage-guidelines): the user accepts the EULA explicitly, and server binaries are downloaded from upstream on the user's server, never redistributed. Playkeeper does not imply Mojang or Microsoft endorsement.
- Alternatives worth knowing: [Crafty](https://craftycontrol.com/), [Pterodactyl](https://pterodactyl.io/), [Pelican](https://pelican.dev/), [PufferPanel](https://pufferpanel.com/), [AMP](https://cubecoders.com/AMP).

Reference listings are not a dependency inventory, and upstream terms can change; check them again before relying on them.
