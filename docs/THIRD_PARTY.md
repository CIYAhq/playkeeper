# Third-party components and upstream terms

Checked 2026-09-26 against the pinned versions in `go.mod`, `web/package-lock.json` and `internal/minecraft/catalog.go`. This is an inventory, not legal advice. Playkeeper itself is licensed under AGPL-3.0-only (see [LICENSING.md](LICENSING.md)).

The full licence texts of everything below that is compiled into the binary, and the terms of the logos the web UI shows, are in [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES), which every release tarball includes and the dashboard links to from Settings. That covers the extra notices some modules carry: modernc.org/libc's third-party notices, SQLite's public-domain dedication and sqlite-vec's licence in modernc.org/sqlite, and the Go and mmap-go licences in modernc.org/memory. `scripts/third-party-notices.sh` generates it (`make notices`), `make check` fails while it is out of date, and `make package` refuses to package a binary that links a module missing from it.

## Compiled into the `playkeeper` binary

List exactly what is compiled in with `go version -m dist/playkeeper-*-linux-amd64/playkeeper` (checked against the table below for the tested build).

| Module | Version | Licence |
| --- | --- | --- |
| Go standard library | 1.27.1 | BSD-3-Clause |
| golang.org/x/crypto (argon2, acme, ssh, and age's primitives) | v0.57.0 | BSD-3-Clause |
| filippo.io/age | v1.3.2 | BSD-3-Clause |
| filippo.io/hpke (used by age) | v0.4.0 | BSD-3-Clause |
| golang.org/x/sys | v0.48.0 | BSD-3-Clause |
| modernc.org/sqlite | v1.59.0 | BSD-3-Clause |
| modernc.org/libc | v1.75.7 | BSD-3-Clause |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause |
| modernc.org/memory | v1.12.1 | BSD-3-Clause |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/remyoudompheng/bigfft | 24d4a6f8daec | BSD-3-Clause |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/pkg/sftp | v1.13.11 | BSD-2-Clause |
| github.com/kr/fs (used by sftp) | v0.1.0 | BSD-3-Clause |

## Bundled in the web UI

| Package | Version | Licence |
| --- | --- | --- |
| react | 19.3.0 | MIT |
| react-dom | 19.3.0 | MIT |
| scheduler | 0.28.0 | MIT |
| @base-ui/react, @base-ui/utils | 1.8.0, 0.4.0 | MIT |
| @floating-ui/core, dom, react-dom, utils | 1.8.0, 1.8.0, 2.1.9, 0.2.12 | MIT |
| reselect | 5.3.0 | MIT |
| use-sync-external-store | 1.7.0 | MIT |
| @babel/runtime | 7.29.7 | MIT |
| lucide-react (icons) | 1.48.0 | ISC |
| tailwindcss (its base styles) | 4.3.3 | MIT |
| tw-animate-css | 1.4.0 | MIT |
| class-variance-authority | 0.7.1 | Apache-2.0 |
| clsx | 2.1.1 | MIT |
| tailwind-merge | 3.7.0 | MIT |
| vite (its modulepreload polyfill only) | 8.3.1 | MIT |
| rolldown (its CommonJS runtime helper only) | 1.2.10 | MIT |
| coss ui components, copied into `web/src/components/ui` from cosscom/coss `apps/ui` at 59e8c88 and restyled | — | MIT (`web/src/components/ui/LICENSE.md`) |

The UI uses system fonts. Pip, the pixel art, the Vanilla type's icon and the Playkeeper mark are original to Playkeeper and use no Minecraft or Mojang art. No OpenAnalytics or Ghost source, CSS, assets or branding is used.

### Server software logos

The New server wizard shows these projects' own logos, unaltered, only to name their software. They are not covered by Playkeeper's licence; `web/src/assets/logos/NOTICE.md` has the sources, and the licence or terms files sit next to it.

| Logo | Source | Terms | Attribution |
| --- | --- | --- | --- |
| Paper | assets.papermc.io `papermc_logo.min.svg` (PaperMC/docs@90c5a40) | PaperMC's art-asset terms: allowed in server selectors; not to be altered or sold with other products without permission | — |
| Purpur | PurpurMC/PurpurWebsite `purpur.svg` (81833d8) | MIT | © PurpurMC |
| Quilt | QuiltMC/art `quilt_logo_dark.svg` (849d6df) | CC0 1.0 | Quilt logo by the QuiltMC community (courtesy) |
| NeoForge | neoforged/Documentation `logo.svg` (2924fdb) | MIT; the branding art is CC BY 4.0 | NeoForge logo © the NeoForged team, created by @Ridanisaurus, CC BY 4.0 |
| Fabric | FabricMC/fabric `icon.png` (ba0d6c0) | Apache-2.0 (no NOTICE file) | — |

Spigot, Bukkit and Folia are not offered, and Forge stays out until its team allows its logo to be shown. Add-on sources (Modrinth, Hangar, CurseForge) are named in text only.

Build and test tools (Vite, TypeScript, ESLint, Vitest, happy-dom [MIT], Playwright [Apache-2.0], axe-core [MPL-2.0], mineflayer [MIT]) are development dependencies and are not shipped, apart from the two small pieces of Vite and Rolldown code listed above that the bundler puts into the UI.

## Downloaded at runtime on the user's server (not redistributed)

| Component | How it is obtained | Terms |
| --- | --- | --- |
| `itzg/minecraft-server` image `2026.9.1-java25` | Pulled by digest `sha256:e8640538…315749` from Docker Hub when the user creates a server | Apache-2.0 (repository licence checked 2026-09-24) |
| `itzg/minecraft-server` images `2026.9.1-java21`, `-java17`, `-java16` and `-java8` | Pulled by digest (`sha256:21b3d6ba…`, `38afacde…`, `7a5a811a…`, `aea37afb…`, listed in `internal/minecraft/java.go`) only for a server on an older Minecraft version, which runs on the Java Mojang made that version for | Apache-2.0, same release as above |
| Paper 26.1.2 build 74 / 1.21.11 build 132 | Downloaded by the image from PaperMC's Fill v3 API after EULA acceptance; SHA-256 checked by Playkeeper before first run | GPL-3.0 with some MIT-licensed contributions (Paper `LICENSE.md`, checked 2026-09-24) |
| Minecraft: Java Edition server | Downloaded from Mojang by Paper's launcher on first start | Minecraft EULA (proprietary); Playkeeper never redistributes it |

## Minecraft EULA and usage guidelines (read 2026-09-24)

- The EULA allows installing the Java Edition server "on a server and host online play" but forbids distributing Mojang's software. Playkeeper ships no Minecraft or Paper binaries; the user's server downloads them only after the user ticks the EULA box, and the acceptance is recorded with user and time.
- Tools "must not seem official or approved". The README, UI footer and installer notes say: "Not an official Minecraft product. Not approved by or associated with Mojang or Microsoft." Playkeeper's name, logo and assets contain no Minecraft marks, fonts or textures.
- The usage guidelines require that server access "must only be granted to users who have a genuine paid-for version of Minecraft". Servers are created with `online-mode=true` and an enforced allowlist. Offline mode exists only behind the `PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE` agent variable, which the installer never sets and which puts a permanent red warning in the UI; it is used by the automated protocol-bot tests.
