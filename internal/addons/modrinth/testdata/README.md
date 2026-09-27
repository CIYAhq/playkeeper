# Modrinth fixtures

Real answers from `https://api.modrinth.com/v2`, fetched once on 2026-09-25 with the User-Agent `CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`, then trimmed so the files stay small. Field names, types and nesting are exactly as Modrinth sent them.

| File | Request |
| --- | --- |
| `search-plugins-paper-26.2.json` | `GET /search?index=downloads&limit=5&facets=[["categories:paper","categories:spigot","categories:bukkit"],["versions:26.2"],["project_type:plugin"],["server_side:required","server_side:optional"]]` |
| `search-mods-fabric-26.2.json` | `GET /search?index=downloads&limit=5&facets=[["categories:fabric"],["versions:26.2"],["project_type:mod"]]` (no side facet, so client-only mods such as Sodium are included) |
| `project-<slug>.json` | `GET /project/<slug>` |
| `versions-<slug>.json` | `GET /project/<slug>/version?include_changelog=false` |
| `projects.json` | `GET /projects?ids=["fALzjamp","P1OZGk5p"]` |
| `version-files.json` | `POST /version_files` with Chunky's two jar hashes and one unknown hash, `algorithm: sha512` |
| `version-files-update.json` | `POST /version_files/update` with Chunky's Paper jar hash, `loaders: ["paper"]`, `game_versions: ["26.2"]` |
| `error-400.json` | `GET /search` with malformed facets |

Trimming:

- Search hits: `versions` and project `game_versions` keep 1.21 and later; `gallery` keeps at most one image.
- Projects: `body` is cut to 240 characters, `gallery` to one image, `versions` to the version ids kept in `versions-<slug>.json`.
- Version lists keep a handful of versions per project, chosen to cover releases and pre-releases, several loaders (Paper, Fabric, NeoForge), other Minecraft versions, and every dependency type (required, optional, incompatible, a pinned `version_id`). `game_versions` keeps 1.21 and later.

Project ids used across files: `P1OZGk5p` viaversion, `NpvuJQoq` viabackwards, `fALzjamp` chunky, `4qmvXRB9` zconfig, `sml2FMaA` anti-xray, `KOHu7RCS` moonrise-opt, `P7dR8mSH` fabric-api, `Eldc1g37` tcdcommons.
