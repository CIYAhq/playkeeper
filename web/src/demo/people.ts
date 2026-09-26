// The live demo's people: who helps run the dashboard, the invite links
// friends join the servers with, a join request waiting for a yes, the
// Discord channel alerts go to, and each player's profile. Changing any of it
// answers "not in the demo", as the engine does for writes it has no answer for.

import { ApiError } from '@/api/client'
import type { DiscordKind, DiscordSettings, Invite, InvitesResponse, JoinRequestView, PlayerDay, PlayerProfile, Session, TeamResponse } from '@/api/types'
import { t } from '@/i18n'
import { cobblemonId, creativeId, demoUser, freeName, iso, noise, serverOf, survivalId, type DemoState, type Request, type Routes } from './data'

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

/** When this tab's demo was made, which its people's dates count from. */
const madeAt = (s: DemoState) => s.hour * hour

function team(s: DemoState): TeamResponse {
  const made = madeAt(s)
  const created = made - 41 * day
  return {
    projectId: 'demo',
    project: '',
    members: [
      { id: 1, username: demoUser, owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: false, addedAt: iso(created), canEdit: false },
      { id: 2, username: 'juno', owner: false, you: false, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: iso(created + 3 * day), canEdit: true },
      { id: 3, username: 'pia', owner: false, you: false, role: 'moderator', servers: { servers: [creativeId] }, twoFactor: false, addedAt: iso(made - 18 * day), canEdit: true },
      { id: 4, username: 'brick', owner: false, you: false, role: 'moderator', servers: { servers: [cobblemonId] }, twoFactor: true, addedAt: iso(made - 7 * day), canEdit: true },
    ],
    invites: [
      {
        id: 'tmkestrel',
        kind: 'member',
        projectId: 'demo',
        role: 'viewer',
        servers: { servers: [survivalId] },
        label: 'For Kestrel',
        createdBy: 1,
        createdAt: iso(made - day),
        expiresAt: iso(made + 6 * day),
        maxUses: 1,
        uses: 0,
        status: 'active',
        usesLeft: 1,
        path: '/join/Tk5MvQ8wRdNa',
        canEdit: true,
      },
    ],
    grantableRoles: ['admin', 'moderator', 'viewer'],
    servers: s.servers.map((x) => ({ id: x.id, name: x.name })),
  }
}

/** A player invite link on one of the sample servers. */
function link(serverId: string, id: string, over: Partial<Invite> & Pick<Invite, 'label' | 'createdAt' | 'uses' | 'status'>): Invite {
  return { id, kind: 'player', projectId: 'demo', serverId, approval: 'right_away', createdBy: 1, maxUses: 0, ...over }
}

function links(s: DemoState, serverId: string): Invite[] {
  const made = madeAt(s)
  switch (serverId) {
    case survivalId:
      return [
        link(serverId, 'plschool', { label: 'School friends', createdAt: iso(made - 7 * day), expiresAt: iso(made + 23 * day), uses: 3, status: 'active', path: '/join/Sq7KfV2mXcLp' }),
        link(serverId, 'pldiscord', { label: 'Discord crew', approval: 'after_yes', createdAt: iso(made - 12 * day), uses: 1, status: 'active', path: '/join/Dc4RtY8nWbQe' }),
        link(serverId, 'plweekend', { label: 'Weekend guests', createdAt: iso(made - 10 * day), expiresAt: iso(made - 3 * day), maxUses: 5, uses: 2, status: 'expired' }),
      ]
    case creativeId:
      return [link(serverId, 'plbuild', { label: 'Build team', createdAt: iso(made - 15 * day), uses: 2, status: 'active', path: '/join/Bt6HwZ3kPaJr' })]
    default:
      return []
  }
}

function invites(s: DemoState, r: Request): InvitesResponse {
  return { invites: links(s, serverOf(s, r).id), expiries: ['1d', '7d', '30d', 'until_turned_off'], link: { base: `https://${freeName}.playkeeper.io:8443`, friendly: true } }
}

/** Someone who found Survival's Discord crew link, which needs a yes. */
function joinRequests(s: DemoState, r: Request): JoinRequestView[] {
  if (serverOf(s, r).id !== survivalId) return []
  const player = 'Bramble_22'
  return [
    {
      request: { id: 'jrbramble', inviteId: 'pldiscord', serverId: survivalId, playerName: player, playerUuid: '7f3c2a1e-5b8d-4e6f-9a0b-c1d2e3f4a5b6', state: 'pending', createdAt: iso(madeAt(s) - 20 * minute) },
      notice: {
        title: { key: 'invite.request.title', params: { player }, text: `${player} wants to join` },
        detail: { key: 'invite.request.askedWith', params: { link: 'Discord crew' }, text: 'Asked with the Discord crew link' },
      },
    },
  ]
}

const discordKinds: DiscordKind[] = ['crash', 'recovered', 'low_disk', 'backup_failed', 'backup_succeeded', 'update_available', 'started', 'stopped', 'player_joined', 'player_left', 'join_requested']

/** The alerts channel, whose last message said Cobblemon crashed. */
function discord(s: DemoState): DiscordSettings {
  const crashed = s.servers.find((x) => x.id === cobblemonId)?.crash?.at
  const made = madeAt(s)
  return {
    connected: true,
    webhookName: 'Minecraft alerts',
    connectedAt: iso(made - 20 * day),
    alerts: ['crash', 'recovered', 'join_requested', 'backup_failed', 'low_disk', 'update_available'],
    liveStatus: true,
    delivery: { sent: crashed ? iso(Date.parse(crashed) + 4000) : iso(made - 34 * minute) },
    kinds: discordKinds,
  }
}

/** A player on a server's list: how much they play, when, and how they got in. */
function profile(s: DemoState, r: Request): PlayerProfile {
  const srv = serverOf(s, r)
  const name = (r.query.get('name') ?? '').toLowerCase()
  const stat = s.roster[srv.id]?.find((p) => p.name.toLowerCase() === name)
  if (!stat) throw new ApiError(404, { error: t('error.http', { status: '404' }), code: 'not_found' })
  const on = s.live[srv.id]?.online.find((p) => p.name === stat.name)
  const seed = [...stat.name].reduce((n, c) => n + c.charCodeAt(0), 0)
  const last = on ? r.now : Date.parse(stat.lastSeen)
  const firstSeen = Math.max(Date.parse(srv.createdAt) + (seed % 3) * day, last - (stat.sessions * 2 + 3) * day)
  // The last two weeks, oldest first: evenings mostly, more for those who play more.
  const perDay = stat.playtimeSeconds / Math.max(1, (last - firstSeen) / day)
  const days: PlayerDay[] = Array.from({ length: 14 }, (_, i) => {
    const date = new Date(r.now - (13 - i) * day)
    const playedThen = date.getTime() >= firstSeen - day && date.getTime() <= last + day && noise(seed + i) < 0.4 + Math.min(0.5, perDay / 7200)
    return { date: date.toISOString().slice(0, 10), playtimeSeconds: playedThen ? Math.round(perDay * (0.6 + noise(seed * 7 + i) * 1.4)) : 0 }
  })
  const recent: Session[] = []
  if (on) recent.push({ id: 900, player: stat.name, start: iso(on.since), startUncertain: false, endUncertain: false, durationSeconds: Math.round((r.now - on.since) / 1000), source: 'rcon list' })
  // Earlier evenings; the newest of them ended when they were last seen, unless they're on now.
  const evening = (d: number) => {
    const date = new Date(last - d * day)
    return new Date(date.getFullYear(), date.getMonth(), date.getDate(), 18 + (seed % 3), (seed * 7) % 60).getTime()
  }
  for (const [i, d] of [1, 2, 4, 5].entries()) {
    const seconds = Math.round(1800 + noise(seed + d) * 5400)
    const end = !on && i === 0 ? last : evening(d) + seconds * 1000
    if (end - seconds * 1000 < firstSeen) break
    recent.push({ id: 800 - i, player: stat.name, start: iso(end - seconds * 1000), end: iso(end), endReason: 'left', startUncertain: false, endUncertain: false, durationSeconds: seconds, source: 'rcon list' })
  }
  const listed = s.whitelist[srv.id]?.find((p) => p.name === stat.name)
  return {
    name: stat.name,
    online: !!on,
    onlineSince: on ? iso(on.since) : undefined,
    allowlisted: !!listed,
    operator: !!s.operators[srv.id]?.some((p) => p.name === stat.name),
    banned: false,
    firstSeen: iso(firstSeen),
    sessions: stat.sessions + (on ? 1 : 0),
    playtimeSeconds: stat.playtimeSeconds + (on ? Math.round((r.now - on.since) / 1000) : 0),
    longestSeconds: Math.max(...recent.map((x) => x.durationSeconds), Math.round(stat.playtimeSeconds / Math.max(1, stat.sessions) * 2.2)),
    tz: r.query.get('tz') ?? 'UTC',
    days,
    mostly: days.some((d) => d.playtimeSeconds > 0) ? 'evening' : undefined,
    recent,
    joined: listed?.joined,
  }
}

export const peopleReads: Routes = {
  'GET /api/team': team,
  'GET /api/servers/:id/invites': invites,
  'GET /api/servers/:id/join-requests': joinRequests,
  'GET /api/discord': discord,
  'GET /api/servers/:id/players/profile': profile,
}
