# Modrinth API fixtures

Answers from the public Modrinth API (`https://api.modrinth.com/v2`), fetched
on 2026-09-25 with the User-Agent
`CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`. They hold
project metadata only (titles, one-line descriptions, licences, counts and
file listings), none of the packs' content. The packs themselves are in
`../packs`.

| File | Request | Trimmed |
| --- | --- | --- |
| `search-modpacks.json` | `GET /search` for modpacks, first page of 20 | 7 of the 20 hits, chosen for their mix of loaders (Fabric, Quilt, Forge, NeoForge) and licences (MIT, MPL-2.0, LGPL-3.0, GPL-3.0, All Rights Reserved); `total_hits`, `offset` and `limit` are as returned |
| `projects.json` | `GET /projects?ids=[...]` for CSMP, Adrenaline, Vanilla Perfected, Create+ and Sodium Plus, and `GET /project/the-respect-my-rights-modpack` (fetched 2026-09-26) | `body` and `gallery` emptied; each project's `versions` list cut to at most 8 ids |
| `versions-adrenaline.json` | `GET /project/adrenaline/version` | 7 versions: 26.5.0 for six Minecraft versions (26.3 as a beta) and 26.4.2 for 26.2 |
| `versions-vanilla-perfected.json` | `GET /project/vanilla-perfected/version` | 7 versions, including the two whose packs are in `../packs` |
| `versions-create_plus.json` | `GET /project/create_plus/version` | 3 versions: two NeoForge alphas and the Forge 5.2.1b release with its separate server pack file |
| `versions-the-respect-my-rights-modpack.json` | `GET /project/the-respect-my-rights-modpack/version` (fetched 2026-09-26) | None: the project has one version, for Forge |
| `versions-the-content-smp.json` | `GET /project/the-content-smp/version` | None: the project has one version |

Every version's `changelog` is set to null and its `dependencies` list is
emptied, as tests do not use them. Tests serve these files from a local fake API and
rewrite the `cdn.modrinth.com` addresses to a local fake CDN.
