# CurseForge fixtures

No real API key was used to make these, and none appears in them. Tests serve
them from a local fake that insists on a made-up key.

| File | What it is |
| --- | --- |
| `manifest.json` | The real `CurseForge/manifest.json` of [Fabulously Optimized](https://github.com/Fabulously-Optimized/fabulously-optimized) 15.0.0-alpha.3 (commit `2e05b759ce3284929c7d08d8f2bc811ccfc08e2b`, 2026-09-20), trimmed from 40 to 8 files: Fabric API, Sodium, Lithium, FerriteCore, Cloth Config, Text Placeholder API and two resource packs (Translations for Sodium, Chat Reporting Helper). BSD-3-Clause, notice below. |
| `mod-238222.json`, `file-238222-4644453.json`, `search-worldguard.json` | Answers recorded from the live API in August 2023 for the tests of [itzg/mc-image-helper](https://github.com/itzg/mc-image-helper) (MIT, notice below): the JEI project, one JEI file and a search for WorldGuard. Trimmed to one `latestFiles` entry and a few `latestFilesIndexes`; the JEI file's `downloadUrl`, which the recording had replaced with a template, is restored to CurseForge's usual `https://edge.forgecdn.net/files/<id / 1000>/<id % 1000>/<fileName>` address. |
| `search-modpacks.json`, `files-9100001.json` | Made up in the same shape as the recordings, with project ids from 9100001 and file ids from 9200001 that are not real: a modpack search page (a Fabric pack, a Forge pack, a NeoForge pack, a 1.19.2 Quilt pack and a pack whose files name no loader) and the Fabric pack's files, including a server pack. Tests fill in hashes, sizes and download addresses at run time. |

Fabulously Optimized's licence:

> Copyright 2020-2026 Fabulously Optimized Authors
>
> Redistribution and use in source and binary forms, with or without
> modification, are permitted provided that the following conditions are met:
>
> 1. Redistributions of source code must retain the above copyright notice,
>    this list of conditions and the following disclaimer.
> 2. Redistributions in binary form must reproduce the above copyright notice,
>    this list of conditions and the following disclaimer in the documentation
>    and/or other materials provided with the distribution.
> 3. Neither the name of the copyright holder nor the names of its
>    contributors may be used to endorse or promote products derived from this
>    software without specific prior written permission.
>
> THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
> AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
> IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
> ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
> LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
> CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
> SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
> INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
> CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
> ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
> POSSIBILITY OF SUCH DAMAGE.

mc-image-helper's licence:

> MIT License
>
> Copyright (c) 2021 Geoff Bourne
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
