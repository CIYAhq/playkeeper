# Curated add-on fixtures

| File | What it is |
| --- | --- |
| `projects.json` | The public Modrinth API's answer to `GET /v2/projects?ids=[…]`, fetched on 2026-09-25 (DiscordSRV's on 2026-09-28) with the User-Agent `CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`, trimmed to the nine projects the curated list names and to the fields the tests read (plus `description`, `team` and `updated`); `game_versions` keeps its last three entries. |
| `search.json` | The same API's answer to `GET /v2/search?facets=[["project_id:9eGKb6K1","project_id:fALzjamp",…]]&limit=20` for those nine projects, fetched the same days, trimmed to each hit's id, slug, title, author, licence and project type. The tests take the author credited for each project from it. |
| `voicechat-server.properties` | A Simple Voice Chat server settings file written for these tests from the settings and defaults the add-on documents at <https://modrepo.de/minecraft/voicechat/wiki/server_config>. It is not a copy of a file the add-on wrote. |
| `geyser-config.yml` | The `plugins/Geyser-Spigot/config.yml` that Geyser 2.11.3-b1247 wrote on its first start on Paper 26.2 with Floodgate 2.2.5-b141 beside it, on 2026-09-28, unchanged. |
| `floodgate-config.yml` | The `plugins/floodgate/config.yml` Floodgate 2.2.5-b141 wrote on that start, with its random `metrics.uuid` replaced by a fixed one. |
| `geyser-project.json` | The public Modrinth API's answer to `GET /v2/project/geyser` on 2026-09-28, trimmed to the fields the tests read (plus `description`, `team` and `updated`); `game_versions` keeps its last three entries. |
| `floodgate-project.json` | Hangar's answer to `GET /api/v1/projects/Floodgate` on 2026-09-28, the same file as `internal/addons/hangar/testdata/project-Floodgate.json`. |

Simple Voice Chat is listed on Modrinth as All Rights Reserved. Its author's
FAQ (<https://modrepo.de/minecraft/voicechat/faq>) answers "Am I allowed to use
this mod in a modpack?" with "Yes. But please make sure to give credit.", which
is why the curated list links that page and names the author. The other
projects are under open licences (MIT, Artistic-2.0, LGPL-3.0, GPL-3.0).
