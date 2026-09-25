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

## Motion

Motion shows what changed, quickly and plainly: no bounce, no overshoot, nothing that moves just to be noticed. Every timing comes from these CSS variables in `web/src/styles.css`:

| Token | Value | For |
| --- | --- | --- |
| `--motion-fast` | 120 ms | hover, press, focus, checkboxes, menus, closing overlays |
| `--motion-standard` | 200 ms | opening dialogs and sheets, switches, list rows, status changes |
| `--motion-slow` | 280 ms | page and tab changes, progress bars and meters |
| `--motion-ease-standard` | `cubic-bezier(0.2, 0, 0, 1)` | things that change in place |
| `--motion-ease-enter` | `cubic-bezier(0, 0, 0.2, 1)` | things that appear |
| `--motion-ease-exit` | `cubic-bezier(0.4, 0, 1, 1)` | things that go away |
| `--motion-press-scale` | `0.98` | how far a pressed button shrinks |

In Tailwind they are `duration-(--motion-fast)`, `ease-standard`, `ease-enter`, `ease-exit`, `scale-(--motion-press-scale)` and the `animate-page`, `animate-enter` and `animate-fade` animations.

- **Press and hover.** Buttons shade on hover and shrink to `--motion-press-scale` while pressed. Links and hand-made controls dim while pressed; cards that wrap a radio, checkbox or switch dim a little less. Disabled controls don't react.
- **Pages and tabs.** A new page or server tab fades in while rising 6 px; the server header stays put between tabs.
- **Overlays.** Dialogs, sheets, menus and selects animate through Base UI's `data-starting-style` and `data-ending-style`: dialogs fade in from 98% size, sheets slide in from their edge, menus and selects fade in from 97%. Menus and selects open fast; everything closes fast with the exit easing.
- **Lists.** Rows added after a list first shows fade in from 4 px above (`data-entering`); removed rows fade out where they were before the list closes up (`data-leaving`). `useListPresence` in `web/src/lib/presence.ts` handles both.
- **State changes.** Switch thumbs slide, status dots and labels fade to their new state, and progress bars ease to their new value.
- **Optimistic updates.** Small changes that are easy to take back show at once: hiding the first steps, and adding or removing players and operators. If saving fails the change is undone and a plain message says what didn't happen. Deleting, restoring, updating, backups and anything else long or destructive waits for the server. Helpers are in `web/src/lib/optimistic.ts`.
- **Reduced motion.** When the system asks for less motion, every transition is instant, nothing shrinks when pressed, and spinners and pulses hold still.

## Screens

- **Home:** every server as a card (status, what's happening, join address), activity across servers, and the machine's memory, CPU and disk.
- **Server:** Overview (first steps, join address, who's playing, how it's running, players over time, recent activity), Console, Players, World (backups and restore) and Settings (plain-language game settings, server list, memory, Minecraft version).
- **New server:** game and type, version, play style, memory, then name and start. Long jobs keep running while you look elsewhere, with their progress in the top bar.
- **Onboarding:** account, a check of the VPS, then create a first server or skip to an empty Home.

Accessible with the keyboard and screen readers, with enough contrast, and without motion when the system asks for reduced motion.

**Source boundary:** coss ui's `apps/ui` is MIT; the rest of the coss repository is AGPL-3.0 and is not used. OpenAnalytics, BoardUI Pro and PostHog code, assets and branding are not used; PostHog's patterns informed the guided parts. See [LICENSING.md](LICENSING.md) and [THIRD_PARTY.md](THIRD_PARTY.md).
