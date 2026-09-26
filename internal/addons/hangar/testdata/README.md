# Hangar fixtures

Real answers from `https://hangar.papermc.io/api/v1`, fetched once on 2026-09-25, then trimmed so the files stay small. Field names, types and nesting are exactly as Hangar sent them.

| File | Request |
| --- | --- |
| `search-paper-26.2.json` | `GET /projects?platform=PAPER&version=26.2&sort=-downloads&limit=5` |
| `project-<slug>.json` | `GET /projects/<slug>` |
| `versions-<slug>.json` | `GET /projects/<slug>/versions` |
| `error-404.json` | `GET /projects/this-project-does-not-exist-xyz` |

Trimming:

- Search results: `mainPageContent` and `sponsors` are cut to 240 characters; `supportedPlatforms` keeps 1.21 and later for Paper.
- Version lists keep a few versions per project (releases, snapshots, one for an older Minecraft version) and `pagination.count` is set to the number kept; `description` is cut to 200 characters and `platformDependencies` keeps 1.21 and later.

What the projects cover: ViaVersion, ViaBackwards and ViaRewind depend on each other (required and optional Hangar dependencies); Geyser offers only an external download link; Orebfuscator requires ProtocolLib, which is not on Hangar (an external dependency link).
