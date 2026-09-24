# Design direction

Reference: [OpenAnalytics](https://github.com/OpenLabs-so/openanalytics) — a quiet white workspace, compact navigation, restrained cards and separators, prominent clear charts, small useful labels, time-range controls, well-judged information density. Apply that visual *approach* to a game server, not its analytics information architecture verbatim.

- **Overview:** real online/joinable state and address first; player activity and system performance follow; quick actions and backup health remain visible.
- **Console:** bounded logs, command input limited to Minecraft console, clear error/connection state.
- **Players:** count-over-time, observed sessions and periods without data. Identify the source and time window; do not present guesses as facts.
- **World:** backup location, last verified archive, export and explicit destructive restore flow. On-host-only warning must be unmistakable.
- **Onboarding:** one task per step; actionable preflight failures, EULA and RAM choices, honest install phases.

Compare actual desktop and narrow-browser screenshots with the reference's hierarchy and rhythm; capture empty, loading, offline and failure states too. Make the design accessible with keyboard, contrast and reduced-motion support. Avoid a generic admin template, decorative fake charts or stock server metrics that hide the game-community layer.

**Source boundary:** OpenAnalytics source is AGPL-3.0 (its tracker has a separate exception); do not copy its CSS, components, assets, logo or brand into Playkeeper without an explicit licence review and decision. Build original components and tokens. Ghost is MIT and archived; it is a product reference, not a code transplant. See [LICENSING.md](LICENSING.md).
