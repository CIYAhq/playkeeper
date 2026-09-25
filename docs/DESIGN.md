# Design direction

Playkeeper 2 is playful and guided: every screen says what to do next. It is built on [coss ui](https://coss.com/ui) components with Playkeeper's own look: warm neutrals, leaf green, system fonts, original blocky pixel art, and Pip the mascot. Decision and data model: [decisions/0004-playkeeper-2.md](decisions/0004-playkeeper-2.md).

## Principles

- **Content fills its card.** Cards in a row share one structure and line up; secondary actions sit at the bottom of the card, so spare space never collects under them.
- **Nothing generic.** No default blue or yellow alert boxes, sparkle icons, tinted pill badges with icons, icons in front of helper text, badges that repeat a heading, filler gradients or coloured left-border stripes. A recommendation is plain text or a preselected option.
- **One notice at a time, quietly.** Non-urgent notices, like an available update, sit in the sidebar or inline, never in a full-width banner.
- **Native on phones.** A bottom tab bar (Overview, Players, Console, World, More) and bottom sheets for menus, selects and dialogs; touch targets of 44 px or more.
- **No browser-default controls.** Selects, menus, checkboxes, radios, switches, sliders and number fields are all custom components.
- **Only what's needed at a glance.** Details live on the item's own page.
- **Recognisable visuals.** Real server software logos in one consistent tile, original pixel art for anything Minecraft, and real player faces. Never Mojang's logo, textures or art.
- **Honest data.** Charts break at gaps instead of drawing zeros, and every number says where it came from.

## Screens

- **Home:** every server as a card (status, what's happening, join address), activity across servers, and the machine's memory, CPU and disk.
- **Server:** Overview (first steps, join address, who's playing, how it's running, players over time, recent activity), Console, Players, World (backups and restore) and Settings (plain-language game settings, server list, memory, Minecraft version).
- **New server:** game and type, version, play style, memory, then name and start. Long jobs keep running while you look elsewhere, with their progress in the top bar.
- **Onboarding:** account, a check of the VPS, then create a first server or skip to an empty Home.

Accessible with the keyboard and screen readers, with enough contrast, and without motion when the system asks for reduced motion.

**Source boundary:** coss ui's `apps/ui` is MIT; the rest of the coss repository is AGPL-3.0 and is not used. OpenAnalytics, BoardUI Pro and PostHog code, assets and branding are not used; PostHog's patterns informed the guided parts. See [LICENSING.md](LICENSING.md) and [THIRD_PARTY.md](THIRD_PARTY.md).
