# Licensing and references

Playkeeper is free software under the **GNU Affero General Public License, version 3 only** (SPDX `AGPL-3.0-only`). The full text is in [LICENSE](../LICENSE), and every release tarball includes it. What follows is a summary, not legal advice:

- Running Playkeeper on your own server asks nothing of you.
- If you pass Playkeeper on, changed or not, pass on the licence and the corresponding source code with it.
- If you change Playkeeper and let other people use your changed version over a network, offer them the source code of your version (section 13 of the licence).
- Contributions are accepted under the same licence; see [CONTRIBUTING.md](../CONTRIBUTING.md).

Bundled third-party code keeps its own licence. [THIRD_PARTY.md](THIRD_PARTY.md) lists it, and [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES), which every release tarball includes, reproduces those licences in full.

## References

- [Ghost](https://github.com/haydenbleasel/ghost) is an archived MIT-licensed product reference. Its Vercel/Hetzner provisioning model is not our existing-VPS installer, and none of its code is used.
- [coss ui](https://coss.com/ui) (MIT) is the base of the dashboard's components: they are copied into `web/src/components/ui`, restyled, and keep their licence (`LICENSE.md` there, and in THIRD_PARTY_NOTICES). The design, mascot and art are Playkeeper's own; see [DESIGN.md](DESIGN.md).
- [OpenAnalytics](https://github.com/OpenLabs-so/openanalytics) (AGPL-3.0, with a separate browser tracker exception) was a *visual reference* for 0.1 and 0.2. None of its components, CSS, assets or branding is used.
- The server software logos in the New server wizard are their projects' own, unaltered, used only to name the software; each keeps its licence or terms (see [THIRD_PARTY.md](THIRD_PARTY.md#server-software-logos)).
- [itzg/minecraft-server](https://docker-minecraft-server.readthedocs.io/) is the runtime container (Apache-2.0), pinned by digest and pulled on the user's server.
- [Minecraft EULA](https://www.minecraft.net/en-us/eula) and [usage guidelines](https://www.minecraft.net/en-us/usage-guidelines): the user accepts the EULA explicitly, and server binaries are downloaded from upstream on the user's server, never redistributed. Playkeeper does not imply Mojang or Microsoft endorsement.
- Alternatives worth knowing: [Crafty](https://craftycontrol.com/), [Pterodactyl](https://pterodactyl.io/), [Pelican](https://pelican.dev/), [PufferPanel](https://pufferpanel.com/), [AMP](https://cubecoders.com/AMP).

Reference listings are not a dependency inventory, and upstream terms can change; check them again before relying on them.
