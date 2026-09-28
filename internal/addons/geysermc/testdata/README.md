# GeyserMC fixtures

Real answers from `https://download.geysermc.org/v2`, fetched on 2026-09-28 with the User-Agent `CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`.

| File | Request |
| --- | --- |
| `latest-floodgate.json` | `GET /projects/floodgate/versions/latest/builds/latest`, which redirects to `/projects/floodgate/versions/2.2.5/builds/141` |
| `latest-geyser.json` | `GET /projects/geyser/versions/latest/builds/latest`, which redirects to `/projects/geyser/versions/2.11.3/builds/1247` |

`changes` keeps at most the first two entries. The add-on library's tests serve generated jars in place of the files and rewrite each `sha256` to match.
