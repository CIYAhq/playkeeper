# Curated add-on fixtures

| File | What it is |
| --- | --- |
| `projects.json` | The public Modrinth API's answer to `GET /v2/projects?ids=[…]`, fetched on 2026-09-25 with the User-Agent `CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`, trimmed to the eight projects the curated list names and to the fields the tests read (plus `description`, `team` and `updated`); `game_versions` keeps its last three entries. |
| `search.json` | The same API's answer to `GET /v2/search?facets=[["project_id:9eGKb6K1","project_id:fALzjamp",…]]&limit=20` for those eight projects, fetched the same day, trimmed to each hit's id, slug, title, author, licence and project type. The tests take the author credited for each project from it. |
| `voicechat-server.properties` | A Simple Voice Chat server settings file written for these tests from the settings and defaults the add-on documents at <https://modrepo.de/minecraft/voicechat/wiki/server_config>. It is not a copy of a file the add-on wrote. |

Simple Voice Chat is listed on Modrinth as All Rights Reserved. Its author's
FAQ (<https://modrepo.de/minecraft/voicechat/faq>) answers "Am I allowed to use
this mod in a modpack?" with "Yes. But please make sure to give credit.", which
is why the curated list links that page and names the author. The other
projects are under open licences (MIT, Artistic-2.0, LGPL-3.0, GPL-3.0).
