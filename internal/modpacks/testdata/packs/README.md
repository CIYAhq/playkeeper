# Modpack fixtures

Trimmed copies of real Modrinth modpacks (`.mrpack`), downloaded through the
public Modrinth API on 2026-09-25 with the User-Agent
`CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)`.

Each folder holds the pack's `modrinth.index.json` (re-indented; the entries
that are kept are unchanged) and, where the pack's licence allows copying,
some of its real override files under `overrides/`. Tests zip a folder into an
`.mrpack` at run time and point its download addresses at a local fake CDN.

| Folder | Pack | Loader and Minecraft | Licence | What was kept |
| --- | --- | --- | --- | --- |
| `adrenaline` | [Adrenaline](https://modrinth.com/modpack/adrenaline) 26.5.0+mc26.2.fabric (version `EZaeTUP8`) by devin | Fabric Loader 0.19.5, 26.2 | MIT | Everything: 30 files (17 for both sides, 11 client-only, 2 server-only) and all 7 overrides |
| `vanilla-perfected-1.0.2`, `vanilla-perfected-1.0.3` | [Vanilla Perfected](https://modrinth.com/modpack/vanilla-perfected) 1.0.2+26.1.2 (`jmYEuzNA`) and 1.0.3+26.1.2 (`zCNpmrT6`) by demonjoeTV | Fabric Loader 0.19.2, 26.1.2 | MIT | 10 of 111 files each: three mods both versions share, four mods whose update changed the file name, the shader pack the update swapped, a resource pack and a data pack. 12 of 104 overrides, including the four the update changed and an empty file |
| `create-plus-6.0.0-alpha-f` | [Create+](https://modrinth.com/modpack/create_plus) 6.0.0 Alpha f (`BSg2ZS8u`) by Sshmoob | NeoForge 21.1.233, 1.21.1 | LGPL-3.0-only | 11 of 155 files, covering every environment combination in the pack, including the out-of-format `"unknown"`. The overrides are not copied: `overrides.txt` lists the real names of 9 of its 767 overrides (a hidden marker, mixin output, caches, configs, a Connector jar) and tests fill them with placeholders. `xmcl.json` is a stray file the pack has at the root of its zip |
| `create-plus-5.2.1b` | Create+ 5.2.1b (`OirSzesD`) | Forge 43.5.1, 1.19.2 | LGPL-3.0-only | 3 of 257 files, no overrides |
| `csmp-1.5` | [CSMP](https://modrinth.com/modpack/the-content-smp) 1.5 Release (`ck8SrkA4`) by GhostHollow | Quilt Loader 0.20.0-beta.4, 1.19.2 | MIT | 5 of 101 files (3 for both sides, 2 client-only) and 2 of 376 overrides |

The content copied from the MIT-licensed packs above is used under this
notice:

> Copyright (c) the pack authors named above
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.
