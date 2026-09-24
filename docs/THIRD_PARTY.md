# Third-party components and upstream terms

Checked 2026-09-24 against the pinned versions in `go.mod`, `web/package-lock.json` and `internal/minecraft/catalog.go`. This is an inventory, not legal advice. Playkeeper itself has **no licence chosen yet** (see [LICENSING.md](LICENSING.md)).

## Compiled into the `playkeeper` binary

List exactly what is compiled in with `go version -m dist/playkeeper-*-linux-amd64/playkeeper` (checked against the table below for the tested build), and the licences with `go run github.com/google/go-licenses/v2@latest report ./cmd/playkeeper`.

| Module | Version | Licence |
| --- | --- | --- |
| Go standard library | 1.27.1 | BSD-3-Clause |
| golang.org/x/crypto (argon2) | v0.57.0 | BSD-3-Clause |
| golang.org/x/sys | v0.48.0 | BSD-3-Clause |
| modernc.org/sqlite | v1.59.0 | BSD-3-Clause |
| modernc.org/libc | v1.75.7 | BSD-3-Clause |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause |
| modernc.org/memory | v1.12.1 | BSD-3-Clause |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/remyoudompheng/bigfft | 24d4a6f8daec | BSD-3-Clause |
| github.com/dustin/go-humanize | v1.0.1 | MIT |

## Bundled in the web UI

| Package | Version | Licence |
| --- | --- | --- |
| react | 19.3.0 | MIT |
| react-dom | 19.3.0 | MIT |
| scheduler | 0.28.0 | MIT |

No fonts, icon sets, images or CSS frameworks are bundled: the UI uses system fonts, and its logo, icons and styles are original to Playkeeper. No OpenAnalytics or Ghost source, CSS, assets or branding is used.

Build and test tools (Vite, TypeScript, ESLint, Vitest, Playwright [Apache-2.0], axe-core [MPL-2.0], mineflayer [MIT]) are development dependencies only and are not shipped.

## Downloaded at runtime on the user's server (not redistributed)

| Component | How it is obtained | Terms |
| --- | --- | --- |
| `itzg/minecraft-server` image `2026.9.1-java25` | Pulled by digest `sha256:e8640538…315749` from Docker Hub when the user creates a server | Apache-2.0 (repository licence checked 2026-09-24) |
| Paper 26.1.2 build 74 / 1.21.11 build 132 | Downloaded by the image from PaperMC's Fill v3 API after EULA acceptance; SHA-256 checked by Playkeeper before first run | GPL-3.0 with some MIT-licensed contributions (Paper `LICENSE.md`, checked 2026-09-24) |
| Minecraft: Java Edition server | Downloaded from Mojang by Paper's launcher on first start | Minecraft EULA (proprietary); Playkeeper never redistributes it |

## Minecraft EULA and usage guidelines (read 2026-09-24)

- The EULA allows installing the Java Edition server "on a server and host online play" but forbids distributing Mojang's software. Playkeeper ships no Minecraft or Paper binaries; the user's server downloads them only after the user ticks the EULA box, and the acceptance is recorded with user and time.
- Tools "must not seem official or approved". The README, UI footer and installer notes say: "Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft." Playkeeper's name, logo and assets contain no Minecraft marks, fonts or textures.
- The usage guidelines require that server access "must only be granted to users who have a genuine paid-for version of Minecraft". Servers are created with `online-mode=true` and an enforced allowlist. Offline mode exists only behind the `PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE` agent variable, which the installer never sets and which puts a permanent red warning in the UI; it is used by the automated protocol-bot tests.
