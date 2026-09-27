# Modrinth API fixtures for shares

Answers from the public Modrinth API (`https://api.modrinth.com/v2`), fetched
on 2026-09-25 with the User-Agent
`CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`. They hold
project and version metadata only: titles, sides, dependencies, file names,
sizes, hashes and download addresses. No mod files are included.

| File | Request | Trimmed |
| --- | --- | --- |
| `versions.json` | `GET /versions?ids=[...]` for the 30 files of Adrenaline 26.5.0+mc26.2.fabric (version `EZaeTUP8`, in `../../testdata/packs/adrenaline`), and for Waystones (`HUacBshD`), Balm (`qLWYe5p7`), Shogi (`wZdCqlCY`), Chunky (`4Eotm6ov`), spark (`e3hsPc1o`), Simple Voice Chat (`Ls232EsW`), Polymer (`561YnR5f`) and Better Fabric Console (`Yn9f9f9K`) | Kept: `id`, `project_id`, `name`, `version_number`, `version_type`, `status`, `game_versions`, `loaders`, `environment`, `date_published`, `files` (hashes, url, filename, primary, size, file_type) and `dependencies`. Sorted by id |
| `projects.json` | `GET /projects?ids=[...]` for the same versions' projects | Kept: `id`, `slug`, `project_type`, `title`, `client_side`, `server_side`, `environment`, `loaders`, and `game_versions` cut to 26.1.2 and 26.2. Sorted by id |

The download addresses stay on `cdn.modrinth.com`: shares link to them and
never download them, and tests never reach the network. Tests serve these
files from a local fake of `POST /version_files` and `GET /projects`.
