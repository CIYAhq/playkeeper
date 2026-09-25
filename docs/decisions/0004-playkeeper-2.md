# 0004 — Playkeeper 2: coss ui, several servers, and a data model with room to grow

- **Date:** 2026-09-25. **Status:** accepted for 0.3.0. Replaces the visual reference in [0003](0003-ui-direction.md) and the "exactly one server" rule in [0002](0002-stack.md). Brief: [DESIGN.md](../DESIGN.md).

## Decision

1. The browser UI is rebuilt on [coss ui](https://coss.com/ui) (MIT, built on Base UI and Tailwind CSS v4). Its component sources are copied into `web/src/components/ui/` and changed there; nothing is installed from a component registry at build time. Every select, menu, checkbox, switch, radio, slider, number field and dialog is one of these components; the UI uses no browser-default form controls.
2. One machine runs any number of Minecraft servers side by side. Each server has its own container, world, port, memory budget, console, players, backups and operations.
3. Everything the UI shows goes through a translation layer (`web/src/i18n`). English is the only language for now.
4. The data model is shaped for what comes next (server types, add-ons, schedules, team members with roles, two-factor sign-in, more machines): those features add tables and fields, but do not change the keys and relations below.

## Data model

Hierarchy: **project → machine → server**. A game is a property of a server, not a level.

| Thing | Where it lives | Key | Notes |
| --- | --- | --- | --- |
| Project | `panel.db` `projects` | random id | A group you run servers for. One project in 0.3.0; the switcher stays hidden until there are two. |
| Membership | `panel.db` `project_members` | (project, user) | `role` per project: `admin` in 0.3.0; `moderator` and `viewer` come with co-admins. |
| User | `panel.db` `users` | integer id | `role` on the install: `owner` (everything, including updates and users) or `member` (only what memberships grant). The first account is the owner. |
| Machine | `panel.db` `machines` | random id | A computer running a Playkeeper agent. `kind` is `local` (this host, over the Unix socket) in 0.3.0; `remote` machines come with "connect other machines". |
| Server | the machine's `agent.db` `servers` | random id | Owned by the agent that runs it. `game` (`minecraft-java`), `type` (`paper`), `slug` (short, stable name for URLs), `name`, `layout`, `game_port`, `config` (JSON), `desired`. |
| Operation, sample, event, session, backup, audit row | `agent.db` | as before | Each row carries `server_id`. Machine-wide operations (a Playkeeper update) have an empty `server_id`. |

- **Server ids are random**, never derived from the name, so servers from different machines never collide and a rename changes nothing but the name. The slug is chosen once from the name and never changes, so links keep working.
- **The panel routes by server.** Browser routes are `/api/servers/{id}/…`; the panel finds the machine that runs the server and forwards the request to that machine's agent. In 0.3.0 that is always the local agent. Machine-wide routes are `/api/machines/{id}/…`.
- **Server types** are a registry in `internal/minecraft/types.go`: each type has an id, a display name, whether it is available, its version catalog, and how its software is downloaded and verified. Paper is available; Vanilla, Purpur, Fabric, Quilt and NeoForge are listed as coming later. `config.type` defaults to `paper`, and type-specific build details sit next to the version (`paperBuild` for Paper).
- **Add-ons** (plugins, mods, modpacks) will be an `addons` table in `agent.db` keyed by `(server_id, source, project)`, because the agent owns the server's files. Nothing about servers changes.
- **Schedules** (backups, restarts, tasks) will be a `schedules` table in `agent.db` keyed by `server_id`, run by the agent so they work while the panel is down. Scheduled work becomes an ordinary operation whose actor is `schedule:<id>`.
- **Two-factor sign-in** will add a `user_factors` table and a pending state to `sessions`; the login route already answers with a JSON body that can grow a "second factor needed" step.
- **Co-admins** add rows to `users` and `project_members`. Every panel route already checks the session's role with one `permit` function, so limiting a role is a change in one place.
- **Per-user preferences** (a dismissed checklist, for example) are rows in `panel.db` `user_prefs`.

## Several servers on one machine

| | The migrated server (layout `v1`) | New servers (layout `v2`) |
| --- | --- | --- |
| World | `/var/lib/playkeeper/server/data` | `/var/lib/playkeeper/servers/<id>/data` |
| Container | `playkeeper-minecraft` | `playkeeper-mc-<id>` |
| RCON secret | `agent/rcon.secret`, `server/rcon_password` | `agent/servers/<id>/rcon.secret`, `servers/<id>/rcon_password` |
| Labels | as in 0.2.0 | adds `io.playkeeper.server=<id>` |
| Game port | the install's game port | the lowest free port above it, picked for you |

- **Safe migration:** on its first start, 0.3.0 turns the single server of an existing install into a `v1` server in one database transaction and moves no files. Its container definition stays byte-for-byte the same, so the running server is not restarted and needs no restart. Backups, sessions, events, samples and audit history are assigned to it. If the update is rolled back, the updater puts the 0.2.0 database back.
- **Operations** are exclusive per server, so one server can back up while another is created. A Playkeeper update waits for every server to be idle, and servers wait while it installs.
- **Memory:** each server's budget stays reserved while it is stopped, so it can always start. A new server can only take what the machine has left after the system reserve and the other servers.
- **Networks:** all servers share the private `playkeeper` bridge; each has its own random RCON password, and RCON is never published.

## Translation

- `t(key, values)` looks up `web/src/i18n/en.ts`. Keys are typed, so a missing key fails the type check. Plurals use `Intl.PluralRules`; dates and numbers use `Intl` with the active locale.
- A unit test fails if a component renders text, or an `aria-label`, `title`, `placeholder` or `alt`, that does not come from `t`.
- Messages from the agent and panel are shown as they are, next to their error `code`, until they carry message keys too.

## Other choices

- **Player faces:** the panel fetches each player's skin from Mojang, crops the face and caches it in `panel.db`, so the browser only ever loads images from the panel and viewers' addresses never reach a third party. Players on a default skin, or whose skin can't be found, show their initials, because default skins are Mojang art.
- **Logos:** the Paper, Purpur, Quilt, NeoForge and Fabric logos are shipped unaltered in one tile style, with their licences and attributions in `THIRD_PARTY_NOTICES` and [THIRD_PARTY.md](../THIRD_PARTY.md). Vanilla uses original pixel art. PaperMC's terms require written permission before any paid or cloud-hosted Playkeeper shows its logo.
- **No Mojang art:** pixel art for play styles, worlds and empty states is original, as is Pip the mascot.

## Rejected alternatives

- **BoardUI:** its free components add a second focus-management library and a second token set, and Pro can't be published in an AGPL repository.
- **Moving the existing world into the new layout:** it would restart the running server and risk the world for no benefit.
- **Names as server ids:** a rename or a second machine with a "Survival" would break links, container names and backups.
- **A per-server Docker network:** more networks to create and clean up for no gain while every server belongs to the same admin and RCON passwords are random.
