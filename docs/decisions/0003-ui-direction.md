# 0003 — UI direction compared with the reference

- **Date:** 2026-09-24. **Status:** accepted for the first release. Brief: [DESIGN.md](../DESIGN.md).

Compared by eye: Playkeeper's rehearsal screenshots (attached to the PR) against the Overview screenshot in the [OpenAnalytics README](https://github.com/OpenLabs-so/openanalytics#readme) (`docs/images/dashboard.png`, viewed 2026-09-24). The reference repository was not cloned and nothing from it is in this repository.

**Taken as an approach:** a quiet white workspace; a row of stat cards with a small label over a large value; one prominent chart per question inside a restrained card; the time-range control at the top right of the page; plain-language empty states inside the card that would hold the data; generous spacing at desktop width.

**Different on purpose:**

- The Overview starts with the server's state and join address, then numbers and charts: for a game server "can my friends join?" comes before any metric.
- A fixed left sidebar with the five views in the brief's order (a single column on phones) instead of a floating dock.
- Charts draw straight segments between measured buckets and break at shaded "server not running" and "no data" periods, with a table view, instead of smoothed curves that run across gaps and suggest values that were never measured.
- Single-border cards without nested inner panels, fewer colours, and warning banners where the brief asks for unmistakable labels (on-host backups, test-harness mode).

**Not used:** OpenAnalytics code, CSS, components, icons, fonts, images, logo or brand. Playkeeper's tokens, components, SVG chart and logo are its own; fonts are the system's. [THIRD_PARTY.md](../THIRD_PARTY.md) lists what is bundled.
