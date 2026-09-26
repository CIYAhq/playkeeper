// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, Backup, Candidate, DiscordSettings, InvitesResponse, JoinInfo, JoinPreview, JoinRequestView, MachineView, Me, Operation, PlayerProfile, PlayersSummary, Preflight, ProjectRole, ServerConfig, ServerStatus, TeamResponse } from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { formatDate, formatDuration } from '@/lib/format'
import { DiscordSettingsSection } from './discord'
import { HomePage } from './home'
import { JoinPage } from './join'
import { Onboarding } from './onboarding'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { PlayerProfilePage } from './server/profile'
import { ServerSettingsPage } from './server/settings'
import { WorldPage } from './server/world'
import { TeamSection } from './team'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  put: vi.fn(() => Promise.resolve({})),
  api: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}

const machine: MachineView = {
  id: 'm2345abcde',
  projectId: 'p2345abcde',
  name: 'my-vps',
  kind: 'local',
  live: {
    hostname: 'my-vps',
    os: 'Ubuntu 24.04',
    arch: 'x86-64',
    cpus: 4,
    cpuPercent: 22,
    memoryTotalMB: 16384,
    systemReserveMB: 1536,
    serversMemoryMB: 4096,
    memoryFreeMB: 10752,
    diskFreeBytes: 41 * 2 ** 30,
    diskTotalBytes: 80 * 2 ** 30,
    docker: true,
    dockerVersion: '27.3.1',
    agentVersion: '0.3.0',
    defaultGamePort: 25565,
    offlineModeTest: false,
    servers: 1,
  },
}

const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 4096, heapMB: 3072, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true, playStyle: 'friends' } as ServerConfig

function server(over: Partial<ServerStatus> = {}): ServerStatus {
  return {
    id: 'abcdefghjk',
    name: 'Survival',
    slug: 'survival',
    game: 'minecraft-java',
    type: 'paper',
    createdAt: '2026-09-20T10:00:00Z',
    exists: true,
    desired: 'running',
    phase: 'online',
    reachable: true,
    reachableAt: new Date().toISOString(),
    startedAt: '2026-09-22T10:00:00Z',
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    gameplay: {},
    config,
    firstSteps: { backedUp: false, downloaded: false },
    ...over,
  }
}

function workspace(over: Partial<Workspace> = {}): Workspace {
  return {
    me,
    servers: [server()],
    serversError: undefined,
    machine,
    machines: [machine],
    prefs: {},
    setPrefs: async () => {},
    refresh: async () => {},
    updating: undefined,
    updatingSince: undefined,
    agentDown: false,
    stale: false,
    lastSeenAt: undefined,
    machineName: 'my-vps',
    lastSlug: undefined,
    setLastSlug: () => {},
    signOut: async () => {},
    ...over,
  }
}

function failed(kind: string, phase: string, error: string, detail?: Record<string, unknown>): Operation {
  const at = new Date(Date.now() - 60_000).toISOString()
  return { id: `${kind}-1`, kind, status: 'failed', phase, actor: 'siya', startedAt: at, finishedAt: at, error, detail }
}

/** Answers GETs by path prefix, rejecting with an Error; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([prefix]) => path.includes(prefix))
    if (!hit) return new Promise(() => {})
    return hit[1] instanceof Error ? Promise.reject(hit[1]) : Promise.resolve(hit[1])
  }) as typeof client.get)
}

/** Answers POSTs by how their path ends: a value resolves, an Error rejects, a function answers each call. */
function answerPosts(routes: Record<string, unknown>) {
  vi.mocked(client.post).mockImplementation(((path: string, body?: unknown) => {
    const hit = Object.entries(routes).find(([end]) => path.endsWith(end))?.[1]
    const value: unknown = typeof hit === 'function' ? (hit as (body: unknown) => unknown)(body) : hit
    return value instanceof Error ? Promise.reject(value) : Promise.resolve(value ?? {})
  }) as typeof client.post)
}

const page = () => document.body.textContent ?? ''

function buttons(label: string): HTMLButtonElement[] {
  return [...document.querySelectorAll('button')].filter((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label)
}

function button(label: string): HTMLButtonElement {
  const b = buttons(label)[0]
  if (!b) throw new Error(`no button "${label}"`)
  return b
}

function link(label: string): HTMLAnchorElement {
  const a = [...document.querySelectorAll('a')].find((x) => x.textContent?.trim() === label)
  if (!a) throw new Error(`no link "${label}"`)
  return a
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
}

/** Types into a controlled field the way a browser does, so React sees the change. */
async function typeInto(selector: string, value: string) {
  const input = document.querySelector<HTMLInputElement>(selector)
  if (!input) throw new Error(`no field ${selector}`)
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  await act(async () => {
    setValue?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function wait(ms: number) {
  await act(async () => new Promise((resolve) => setTimeout(resolve, ms)))
}

const moderatorCan: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make']

/** An account that joined the team with an invite link. */
function member(role: ProjectRole, can: Action[], over: Partial<Me['access']> = {}): Me {
  return { ...me, user: { username: 'mara', role: 'member' }, access: { projectId: 'p2345abcde', role, servers: { servers: ['abcdefghjk', 'bcdefghjkm'] }, twoFactor: false, can, ...over } }
}

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
const inHours = (h: number) => new Date(Date.now() + h * 3_600_000).toISOString()

let root: Root | undefined

async function render(node: ReactNode, ws: Workspace = workspace()): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
  return document.body.textContent ?? ''
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  vi.mocked(client.post).mockReset()
  vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  vi.mocked(client.put).mockReset()
  vi.mocked(client.put).mockImplementation(() => Promise.resolve({}))
})

describe('Home', () => {
  it('starts empty with the five first steps', async () => {
    const text = await render(<HomePage />, workspace({ servers: [] }))
    expect(text).toContain('No servers yet')
    for (const step of ['Create your first server', 'Invite a friend', 'A friend joins', 'Make a backup', 'Download it']) expect(text).toContain(step)
  })

  it('shows each server with who is playing and its address', async () => {
    const text = await render(<HomePage />, workspace({ servers: [server({ players: { online: 3, max: 10, names: ['mara_k', 'tobi2009', 'JunoFox'], source: 'rcon list', at: '' } })] }))
    expect(text).toContain('Survival')
    expect(text).toContain('Paper 26.1.2')
    expect(text).toContain('3 playing')
    expect(text).toContain(window.location.hostname)
    expect(text).toContain('1 server on my-vps · 3 playing')
  })

  it('says when the agent stopped answering, keeping names but not numbers', async () => {
    const text = await render(<HomePage />, workspace({ agentDown: true, stale: true, servers: [server({ players: { online: 3, max: 10, names: [], source: '', at: '' } })] }))
    expect(text).toContain('Playkeeper can’t see your servers right now')
    expect(text).toContain('Survival')
    expect(text).toContain('No live status')
    expect(text).toContain('1 server on my-vps')
    expect(text).not.toContain('playing')
  })
})

describe('Home for team members', () => {
  const both = () => [server(), server({ id: 'bcdefghjkm', name: 'Creative', slug: 'creative', phase: 'stopped', desired: 'stopped', startedAt: undefined })]

  it('welcomes a new moderator once, without the owner’s buttons', async () => {
    const setPrefs = vi.fn(async () => {})
    const text = await render(<HomePage />, workspace({ me: member('moderator', moderatorCan), servers: both(), prefs: { 'home.welcome': '1' }, setPrefs }))
    expect(text).toContain('Welcome, mara')
    expect(text).toContain('You help run Survival and Creative as a Moderator.')
    expect(text).not.toContain('New server')
    expect(text).not.toContain('Full audit log')
    await click(button('Dismiss'))
    expect(setPrefs).toHaveBeenCalledWith({ 'home.welcome': '' })
    expect(page()).not.toContain('Welcome, mara')
  })

  it('names the team once it has a name, and stays quiet after the welcome', async () => {
    const named = member('viewer', ['view', 'account.manage'], { team: 'Friends', servers: { all: true } })
    const text = await render(<HomePage />, workspace({ me: named, servers: both(), prefs: { 'home.welcome': '1' } }))
    expect(text).toContain('Welcome to Friends, mara')
    expect(text).toContain('You can see all the servers as a Viewer.')
    expect(await render(<HomePage />, workspace({ me: named, servers: both() }))).not.toContain('Welcome')
  })

  it('asks an admin without two-factor sign-in to turn it on, as the one notice', async () => {
    const text = await render(<HomePage />, workspace({ me: member('admin', moderatorCan, { servers: { all: true }, needsTwoFactor: true }), servers: both(), prefs: { 'home.welcome': '1' } }))
    expect(text).toContain('Turn on two-factor sign-in to use your Admin rights')
    expect(text).toContain('Until then you have Moderator rights.')
    expect(link('Turn it on').getAttribute('href')).toBe('/account/two-factor')
    expect(text).not.toContain('Welcome, mara')
  })

  it('tells the owner on Home that an admin waits for their rights, and confirms them with one click', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: hoursAgo(90), canEdit: false },
        { id: 3, username: 'alex', owner: false, you: false, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: hoursAgo(3), canEdit: true, waiting: true, canConfirm: true },
      ],
      invites: [],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    const text = await render(<HomePage />, workspace({ servers: both() }))
    expect(text).toContain('alex turned on two-factor sign-in.')
    expect(text).toContain('Confirm to give them Admin rights.')
    await click(button('Confirm Admin rights'))
    expect(client.post).toHaveBeenCalledWith('/api/team/members/3/confirm-admin')

    vi.mocked(client.get).mockClear()
    const moderator = await render(<HomePage />, workspace({ me: member('moderator', moderatorCan), servers: both() }))
    expect(moderator).not.toContain('turned on two-factor sign-in')
    expect(client.get).not.toHaveBeenCalledWith('/api/team')
  })

  it('says who got in with an invite link, never the link’s id', async () => {
    answer({ '/activity': [
      { ts: hoursAgo(1), serverId: 'abcdefghjk', kind: 'allowlisted', player: 'Lenn0x', actor: 'invite:ymckepm6wx' },
      { ts: hoursAgo(2), serverId: 'abcdefghjk', kind: 'allowlisted', player: 'pixelpia', actor: 'siya' },
    ] })
    const text = await render(<HomePage />, workspace({ me: member('moderator', moderatorCan), servers: both() }))
    expect(text).toContain('Lenn0x joined with an invite link')
    expect(text).toContain('siya added pixelpia to the allowlist')
    expect(text).not.toContain('invite:')
  })

  it('gives a viewer no Start button and a member no first steps', async () => {
    const stopped = [server({ phase: 'stopped', desired: 'stopped', startedAt: undefined })]
    await render(<HomePage />, workspace({ servers: stopped }))
    expect(buttons('Start')).toHaveLength(1)
    await render(<HomePage />, workspace({ me: member('viewer', ['view', 'account.manage']), servers: stopped }))
    expect(buttons('Start')).toHaveLength(0)
    const empty = await render(<HomePage />, workspace({ me: member('moderator', moderatorCan), servers: [] }))
    expect(empty).toContain('Servers you help run show up here.')
    expect(empty).not.toContain('Create your first server')
  })
})

describe('Overview notices', () => {
  // Regression for item 66: a failure notice stayed up to 15 minutes after its
  // cause cleared, such as "Start failed" next to Online and Joinable.
  it('drops a failed start once the server is online', async () => {
    const last = failed('start', '', 'The server did not finish starting within 10m0s.')
    expect(await render(<Overview server={server({ phase: 'stopped', startedAt: undefined, lastOperation: last })} />)).toContain('Starting Survival failed')
    expect(await render(<Overview server={server({ phase: 'online', lastOperation: last })} />)).not.toContain('Starting Survival failed')
  })

  it('drops a backup refused for space once enough disk is free', async () => {
    const last = failed('backup', '', 'Not enough disk space for a backup.', { neededBytes: 2 ** 30 })
    const at = new Date().toISOString()
    expect(await render(<Overview server={server({ lastOperation: last, resources: { diskFreeBytes: 400 * 2 ** 20, at } })} />)).toContain('Backing up Survival failed')
    expect(await render(<Overview server={server({ lastOperation: last, resources: { diskFreeBytes: 17 * 2 ** 30, at } })} />)).not.toContain('Backing up Survival failed')
  })

  it('warns about low disk space with the preflight advice', async () => {
    const diskWarning = { id: 'disk', label: 'Disk space', status: 'fail' as const, detail: 'Only 0.4 GB free.', fix: 'Free at least 5 GB of disk space, then check again.' }
    const text = await render(<Overview server={server()} />, workspace({ machine: { ...machine, live: machine.live && { ...machine.live, diskWarning } } }))
    expect(text).toContain('Low disk space: Only 0.4 GB free.')
    expect(text).toContain('Free at least 5 GB of disk space')
  })

  it('asks for a restart when settings changed', async () => {
    expect(await render(<Overview server={server({ pendingRestart: true })} />)).toContain('Restart Survival to use them.')
  })
})

describe('Overview', () => {
  it('shows the first steps with the next one to do', async () => {
    const text = await render(<Overview server={server({ firstSteps: { invited: 'mara_k', friendJoined: 'mara_k', friendJoinedAt: new Date().toISOString(), backedUp: false, downloaded: false } })} />)
    expect(text).toContain('Survival is up. 2 small steps left.')
    expect(text).toContain('2 of 4 done')
    expect(text).toContain('Back up now')
    expect(text).toContain('mara_k is on the allowlist.')
  })

  // Regression for items 62 and 86: after a failed create the steps must
  // point at the step the job failed in, not at the server's own phase.
  it('marks the step a failed create stopped at', async () => {
    const s = server({ phase: 'stopped', startedAt: undefined, lastOperation: failed('create', 'downloading_server', 'The download did not match its checksum.') })
    const text = await render(<Overview server={s} />)
    expect(text).toContain('Setting up Survival didn’t finish')
    expect(text).toContain('The download did not match its checksum.')
    const steps = [...document.querySelectorAll('ol > li')]
    expect(steps).toHaveLength(4)
    expect(steps[0]?.querySelector('.bg-primary')).not.toBeNull()
    expect(steps[1]?.querySelector('.text-destructive-foreground')?.textContent).toBe('Downloading Paper 26.1.2')
    expect(steps[2]?.querySelector('.text-destructive-foreground')).toBeNull()
  })

  it('offers more memory after running out of it', async () => {
    answer({ '/logs': { epoch: 'e', lines: [{ seq: 1, ts: '2026-09-25T18:52:57Z', text: '[18:52:57 ERROR]: java.lang.OutOfMemoryError: Java heap space' }], next: 1, truncated: false }, '/catalog': { memoryOptionsMB: [2048, 3072, 4096, 6144, 8192], maxMemoryMB: 8192, versions: [], types: [], servers: [] } })
    const text = await render(<Overview server={server({ phase: 'crashed', crashCount: 2, exitCode: 1 })} />)
    expect(text).toContain('It ran out of its 4 GB of memory.')
    expect(text).toContain('Give Survival 6 GB')
    expect(text).toContain('Fits in the 10.5 GB free')
  })

  it('says which file stopped a start and what to do, in one line', async () => {
    const refusal = { code: 'link' as const, params: { path: 'plugins/bStats/config.yml' }, message: 'plugins/bStats/config.yml in the server’s files is a link, which Playkeeper does not follow.', hint: 'Delete it.' }
    const lastOperation = failed('start', '', 'Paper’s bStats usage statistics could not be switched off, so the server was not started. ' + refusal.message)
    const text = await render(<Overview server={server({ phase: 'stopped', startedAt: undefined, exitCode: 0, lastOperation, refusal })} />)
    expect(text).toContain('Playkeeper won’t start Survival while plugins/bStats/config.yml is a link. Delete it, or replace it with what it points to.')
    expect(text).toContain('Once plugins/bStats/config.yml is fixed, start Survival.')
    expect(text).not.toContain('bStats usage statistics')
    expect(text).not.toContain('Last lines before it stopped')
    const pipe = { ...refusal, code: 'special_file' as const, params: { path: 'plugins/bStats/config.yml', type: 'named_pipe' } }
    expect(await render(<Overview server={server({ phase: 'stopped', refusal: pipe })} />)).toContain('while plugins/bStats/config.yml isn’t a normal file. Delete it.')
    const dockerDown = await render(<Overview server={server({ phase: 'docker_unavailable', lastError: 'Docker is not responding, so Playkeeper cannot see or control the server.', refusal })} />)
    expect(dockerDown).toContain('Docker is not responding')
    expect(dockerDown).not.toContain('Playkeeper won’t start Survival')
  })
})

describe('Server settings', () => {
  it('turns down an icon over 64 KB next to the upload, without sending it', async () => {
    await render(<ServerSettingsPage server={server()} />)
    const input = document.querySelector<HTMLInputElement>('input[type=file]')
    if (!input) throw new Error('no icon upload')
    Object.defineProperty(input, 'files', { value: [new File([new Uint8Array(64 * 1024 + 1)], 'logo.png', { type: 'image/png' })] })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    expect(document.querySelector('[role=alert]')?.textContent).toBe('Icons need to be 64 × 64 and under 64 KB.')
    expect(client.api).not.toHaveBeenCalled()
  })
})

describe('Players', () => {
  const noInvites: InvitesResponse = { invites: [], expiries: ['1d', '7d', '30d', 'until_turned_off'], link: { base: 'https://203.0.113.10:8443', friendly: false } }
  const nobody: PlayersSummary = { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }

  // Like item 70: a session that ended in a crash has no exact length, so
  // its playtime is an estimate and says so.
  it('marks playtime that includes a crash-ended session as an estimate', async () => {
    const summary: PlayersSummary = {
      tz: 'UTC',
      days: [],
      players: [
        { name: 'Lenn0x', lastSeen: '2026-09-24T21:14:00Z', online: false, sessions: 5, playtimeSeconds: 6 * 3600 + 600, playtimeUncertain: true },
        { name: 'mara_k', lastSeen: '2026-09-25T10:00:00Z', online: false, sessions: 3, playtimeSeconds: 3600 },
      ],
      observedSessions: 8,
      uncertainSessions: 1,
      retentionDays: 180,
    }
    answer({ '/whitelist': [{ name: 'Lenn0x' }, { name: 'mara_k' }], '/operators': [], '/players/summary': summary, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': noInvites, '/join-requests': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('≈ 6 h 10 m')
    expect(text).toContain('1 hour')
    expect(text).not.toContain('≈ 1 hour')
    expect(text).toContain('≈ means it ended in a crash.')
  })

  it('shows a new player at once and puts the name back if Minecraft refuses it', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': noInvites, '/join-requests': [] })
    let refuse: (e: unknown) => void = () => {}
    vi.mocked(client.post).mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          refuse = reject
        }),
    )
    await render(<PlayersPage server={server()} />)
    const field = () => document.querySelector('input[aria-label="Minecraft username"]') as HTMLInputElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(field(), 'tobi2009')
      field().dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => field().form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(document.body.textContent).not.toContain('Nobody’s joined yet')
    expect(document.querySelector('li')?.textContent).toContain('tobi2009')
    expect(field().value).toBe('')
    await act(async () => refuse(new client.ApiError(422, { error: 'That player does not exist', code: 'invalid' })))
    expect(document.body.textContent).toContain('Nobody’s joined yet')
    expect(field().value).toBe('tobi2009')
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('That player does not exist')
  })

  it('shows rows shaped like players until the lists arrive', async () => {
    const text = await render(<PlayersPage server={server()} />)
    expect(text).not.toContain('Nobody’s joined yet')
    expect(text).not.toContain('Nobody has played yet.')
    expect(text).not.toContain('0 people')
    expect(text).toContain('Loading…')
    expect(document.querySelectorAll('li [data-slot="skeleton"]').length).toBeGreaterThan(0)
  })

  it('explains how to invite someone when nobody has joined', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': noInvites, '/join-requests': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('Nobody’s joined yet')
    expect(text).toContain('You add their name')
    expect(text).toContain('New invite link')
  })

  it('lists invite links, says how people got in, and puts a join request on top', async () => {
    const invites: InvitesResponse = {
      ...noInvites,
      invites: [
        { id: 'inv1', kind: 'player', projectId: 'p2345abcde', serverId: 'abcdefghjk', approval: 'right_away', label: 'Discord crew', createdBy: 1, createdAt: hoursAgo(26), expiresAt: inHours(6 * 24 + 1), maxUses: 5, uses: 2, usesLeft: 3, status: 'active', path: '/join/Qm7xK2pLw9RtVb4n' },
        { id: 'inv2', kind: 'player', projectId: 'p2345abcde', serverId: 'abcdefghjk', approval: 'after_yes', label: 'School friends', createdBy: 1, createdAt: hoursAgo(72), maxUses: 0, uses: 1, status: 'active', path: '/join/Zx8vB3nMq4LsWd6k' },
        { id: 'inv3', kind: 'player', projectId: 'p2345abcde', serverId: 'abcdefghjk', approval: 'right_away', createdBy: 1, createdAt: hoursAgo(200), expiresAt: hoursAgo(24), maxUses: 3, uses: 1, status: 'expired' },
      ],
    }
    const request: JoinRequestView = {
      request: { id: 'r1', inviteId: 'inv2', serverId: 'abcdefghjk', playerName: 'PixelPia', playerUuid: '6b7f0c8e2d9a4f1b8c3e5a7d9f1b3c5e', state: 'pending', createdAt: hoursAgo(0.05) },
      notice: { title: { key: 'invite.request.title', params: { player: 'PixelPia' }, text: 'PixelPia wants to join' }, detail: { key: 'invite.request.askedWith', params: { link: 'School friends' }, text: 'Asked with the School friends link' } },
    }
    const joined = { key: 'invite.origin.link', params: { link: 'Discord crew' }, text: 'Joined with the Discord crew link', at: hoursAgo(20) }
    answer({ '/whitelist': [{ name: 'Lenn0x' }, { name: 'mara_k', joined }], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': invites, '/join-requests': [request] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('PixelPia wants to join')
    expect(text).toContain('Asked with the School friends link')
    expect(text).toContain('Joined with the Discord crew link')
    expect(text).toContain('203.0.113.10:8443/join/Qm7xK2…')
    expect(text).not.toContain('Qm7xK2pLw9RtVb4n')
    for (const cell of ['Discord crew', '2 of 5', 'in 6 days', 'Right away', 'School friends', '1 · no limit', 'When you turn it off', 'After you say yes', 'Ran out']) expect(text).toContain(cell)
    expect(text).toContain('Set up an address')
    expect(buttons('New invite link').length).toBeGreaterThan(0)
    await click(button('Let in'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/join-requests/r1/approve')
  })

  it('shows a viewer the list without adding players or invite links', async () => {
    answer({ '/whitelist': [{ name: 'mara_k' }], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />, workspace({ me: member('viewer', ['view', 'account.manage']) }))
    expect(text).toContain('mara_k')
    for (const hidden of ['Add player', 'Invite links', 'New invite link']) expect(text).not.toContain(hidden)
    expect(client.get).not.toHaveBeenCalledWith(expect.stringContaining('/invites'))
    expect(client.get).not.toHaveBeenCalledWith(expect.stringContaining('/join-requests'))
  })
})

describe('World', () => {
  it('lists a new backup as soon as it is made, not at the next check', async () => {
    const at = new Date().toISOString()
    const job: Operation = { id: 'backup-1', kind: 'backup', status: 'running', phase: 'archiving', actor: 'siya', startedAt: at }
    const backup: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya', note: 'Before the dragon' }
    answer({ '/backups': [] })
    const text = await render(<WorldPage server={server({ phase: 'stopped', operation: job })} />)
    expect(text).toContain('No backups yet')
    expect(text).toContain('Backing up…')
    answer({ '/backups': [backup] })
    const done = server({ phase: 'stopped', lastOperation: { ...job, status: 'succeeded', finishedAt: at } })
    await act(async () =>
      root?.render(
        <WorkspaceContext.Provider value={workspace({ servers: [done] })}>
          <WorldPage server={done} />
        </WorkspaceContext.Provider>,
      ),
    )
    await act(async () => {})
    expect(document.body.textContent).not.toContain('No backups yet')
    expect(document.body.textContent).toContain('Before the dragon')
  })
})

describe('Get started', () => {
  it('follows the server with steps left', async () => {
    const text = await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ servers: [server({ firstSteps: { invited: 'mara_k', backedUp: false, downloaded: false } })] }))
    expect(text).toContain('1 of 4')
    expect(text).toContain('Next: Make your first backup')
  })

  it('hides once the owner hid it', async () => {
    expect(await render(<GetStartedCard route={{ name: 'home' }} />, workspace({ prefs: { 'checklist.hidden.abcdefghjk': '1' } }))).toBe('')
  })

  it('hides under a key the panel accepts', () => {
    for (const s of [server(), undefined]) expect(hiddenKey(s)).toMatch(/^[a-z][a-z0-9.:_-]{0,63}$/)
  })

  it('hides at once and comes back if the panel refuses', async () => {
    let refuse: (e: unknown) => void = () => {}
    vi.mocked(client.post).mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          refuse = reject
        }),
    )
    const failed = vi.fn()
    function Hide() {
      const { prefs, setPrefs } = useWorkspace()
      return (
        <button type="button" onClick={() => void setPrefs({ [hiddenKey(server())]: '1' }).catch(failed)}>
          {Object.keys(prefs).join(',')}
        </button>
      )
    }
    await render(
      <WorkspaceProvider me={me} onSignedOut={() => {}}>
        <Hide />
      </WorkspaceProvider>,
    )
    const button = document.querySelector('button') as HTMLButtonElement
    await act(async () => button.click())
    expect(button.textContent).toBe('checklist.hidden.abcdefghjk')
    await act(async () => refuse(new client.ApiError(0, { error: 'The dashboard can’t be reached.', code: 'internal' })))
    expect(button.textContent).toBe('')
    expect(failed).toHaveBeenCalled()
  })
})

describe('Onboarding', () => {
  it('checks the machine in plain words, with the provider firewall to do by hand', async () => {
    const preflight: Preflight = {
      ok: true,
      checks: [
        { id: 'docker', label: 'Docker', status: 'pass', detail: 'Docker 27.3.1 is running.' },
        { id: 'memory', label: 'Memory', status: 'pass', detail: '16.0 GB RAM.' },
        { id: 'disk', label: 'Disk space', status: 'pass', detail: '41.0 GB free.' },
        { id: 'port', label: 'Game port', status: 'pass', detail: 'Port 25565 is free for Minecraft players.' },
        { id: 'egress', label: 'Download access', status: 'pass', detail: 'PaperMC is reachable.' },
      ],
    }
    answer({ '/preflight': preflight })
    const text = await render(<Onboarding />, workspace({ servers: [] }))
    expect(text).toContain('Checking this VPS')
    expect(text).toContain('Memory: 16 GB')
    expect(text).toContain('Ubuntu 24.04 on x86-64')
    expect(text).toContain('Port 25565 is free')
    expect(text).toContain('Your provider’s firewall')
    expect(text).toContain('6 of 7 look good')
  })
})

describe('Command palette', () => {
  it('keeps Tab and Shift+Tab inside the palette', async () => {
    await render(<CommandPalette open onOpenChange={() => {}} route={{ name: 'home' }} onShortcuts={() => {}} />)
    const palette = document.querySelector<HTMLElement>('[role="dialog"]')
    const search = palette?.querySelector<HTMLInputElement>('input[role="combobox"]')
    const shortcuts = [...(palette?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.includes('all shortcuts'))
    if (!search || !shortcuts) throw new Error('the palette has no search box or shortcuts button')
    const tab = async (from: HTMLElement, shiftKey: boolean) => {
      from.focus()
      await act(async () => {
        from.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true }))
      })
      return document.activeElement
    }
    expect(await tab(shortcuts, false)).toBe(search)
    expect(await tab(search, true)).toBe(shortcuts)
  })
})

describe('Invite page', () => {
  const code = 'Qm7xK2pLw9RtVb4n'
  const friend: JoinPreview = { kind: 'player', inviter: '', server: 'Survival', version: '26.1.2', online: true, playing: 2, approval: 'right_away' }
  const mara: Candidate = { name: 'mara_k', uuid: '0f3a6c2e9b1d4e7fa2c5b8d1e4f7a0c3', face: 'data:image/png;base64,iVBORw0KGgo=' }
  const steps = [
    { key: 'invite.join.open', params: { version: '26.1.2' }, text: 'Open Minecraft: Java Edition 26.1.2.' },
    { key: 'invite.join.addServer', text: 'Pick Multiplayer, then Add Server.' },
    { key: 'invite.join.paste', text: 'Paste the address and press Done, then Join.' },
  ]
  const notFound = (name: string) => new client.ApiError(422, { error: `No Minecraft: Java Edition account is called ${name}.`, code: 'player_not_found', hint: 'Check the spelling.', params: { name } })

  it('checks a friend’s Minecraft name, shows their face and puts them on the list', async () => {
    const joined: JoinInfo = { player: 'mara_k', server: 'Survival', address: '203.0.113.10', version: '26.1.2', steps }
    answerPosts({ '/preview': friend, '/lookup': mara, '/redeem': joined })
    const text = await render(<JoinPage code={code} onSignedIn={() => {}} />)
    expect(client.post).toHaveBeenCalledWith('/api/public/join/preview', { code })
    expect(text).toContain('You’re invited to Survival')
    expect(text).toContain('Java Edition 26.1.2 · 2 playing now')
    expect(text).toContain('Needs Minecraft: Java Edition 26.1.2.')
    expect(button('Add me to Survival').disabled).toBe(true)
    await typeInto('#join-name', 'mara_k')
    expect(page()).toContain('Looking up mara_k…')
    await wait(500)
    expect(client.post).toHaveBeenCalledWith('/api/public/join/lookup', { code, name: 'mara_k' })
    expect(page()).toContain('Is this you?')
    expect(document.querySelector(`img[src="${mara.face}"]`)).not.toBeNull()
    await click(button('Add me to Survival'))
    expect(client.post).toHaveBeenCalledWith('/api/public/join/redeem', { code, name: 'mara_k' })
    expect(page()).toContain('You’re on the list!')
    expect(page()).toContain('Welcome to Survival, mara_k.')
    expect(page()).toContain('203.0.113.10')
    expect([...document.querySelectorAll('ol > li')].map((li) => li.textContent)).toEqual(steps.map((s, i) => `${i + 1}.${s.text}`))
    expect(page()).not.toContain(code)
  })

  it('says when no Java account has the name, and asks for a yes on links that need one', async () => {
    const waiting: JoinInfo = { player: 'mara_k', server: 'Survival', address: '203.0.113.10', waiting: true, steps: [{ key: 'invite.join.waitAnyone', text: 'Wait for the person who sent the link to let you in.' }, ...steps] }
    answerPosts({ '/preview': { ...friend, approval: 'after_yes' }, '/lookup': (body: { name: string }) => (body.name === 'mara_k' ? mara : notFound(body.name)), '/redeem': waiting })
    await render(<JoinPage code={code} onSignedIn={() => {}} />)
    await typeInto('#join-name', 'mara_kk')
    await wait(500)
    expect(page()).toContain('No Minecraft: Java Edition account is called mara_kk. Check the spelling.')
    expect(document.querySelector('#join-name')?.getAttribute('aria-invalid')).toBe('true')
    expect(button('Ask to join Survival').disabled).toBe(true)
    await typeInto('#join-name', 'mara_k')
    await wait(500)
    expect(page()).not.toContain('No Minecraft: Java Edition account')
    await click(button('Ask to join Survival'))
    expect(page()).toContain('Almost there!')
    expect(page()).toContain('You asked to join Survival as mara_k.')
    expect(page()).toContain('1.Wait for the person who sent the link to let you in.')
  })

  it('makes a team account with the role the link gives, and asks Home to welcome it', async () => {
    const preview: JoinPreview = { kind: 'member', inviter: '', role: 'moderator', servers: { servers: ['abcdefghjk', 'bcdefghjkm'] }, expiresAt: '2026-10-02T12:00:00Z', serverNames: ['Survival', 'Creative'] }
    const signedIn = member('moderator', moderatorCan)
    let tries = 0
    answerPosts({ '/preview': preview, '/accept': () => (++tries === 1 ? new client.ApiError(409, { error: 'That username is taken.', code: 'username_taken', hint: 'Choose another one.' }) : signedIn) })
    const onSignedIn = vi.fn()
    const text = await render(<JoinPage code={code} onSignedIn={onSignedIn} />)
    expect(text).toContain('Join the team as Moderator')
    expect(text).toContain('You’re invited to a Playkeeper dashboard.')
    expect(text).toContain('Runs the servers day to day')
    expect(text).toContain('Survival and Creative')
    expect(text).toContain(`${formatDate('2026-10-02T12:00:00Z')}, for one person`)
    expect(text).toContain('This link works once.')
    expect(button('Join as Moderator').disabled).toBe(true)
    await typeInto('#join-username', 'siya')
    await typeInto('#join-password', 'correct horse battery')
    await typeInto('#join-again', 'correct horse batterz')
    expect(page()).toContain('The passwords don’t match.')
    expect(button('Join as Moderator').disabled).toBe(true)
    await typeInto('#join-again', 'correct horse battery')
    expect(page()).not.toContain('The passwords don’t match.')
    await click(button('Join as Moderator'))
    expect(page()).toContain('That username is taken. Choose another one.')
    expect(document.querySelector('#join-username')?.getAttribute('aria-invalid')).toBe('true')
    await typeInto('#join-username', 'mara')
    expect(page()).not.toContain('That username is taken.')
    await click(button('Join as Moderator'))
    expect(client.post).toHaveBeenCalledWith('/api/public/join/accept', { code, username: 'mara', password: 'correct horse battery' })
    expect(client.post).toHaveBeenCalledWith('/api/me/prefs', { 'home.welcome': '1' })
    expect(onSignedIn).toHaveBeenCalledWith(signedIn)
  })

  it('offers a new admin two-factor sign-in, or Moderator rights until it’s on', async () => {
    const preview: JoinPreview = { kind: 'member', inviter: '', role: 'admin', servers: { all: true }, expiresAt: '2026-10-02T12:00:00Z', serverNames: [], team: 'Friends' }
    const signedIn = member('admin', moderatorCan, { servers: { all: true }, needsTwoFactor: true })
    answerPosts({ '/preview': preview, '/accept': signedIn, '/2fa/setup': new client.ApiError(404, { error: 'Not found.', code: 'not_found' }) })
    const join = async (onSignedIn: (me: Me, to?: string) => void) => {
      const text = await render(<JoinPage code={code} onSignedIn={onSignedIn} />)
      expect(text).toContain('Help run Friends as Admin')
      await typeInto('#join-username', 'alex')
      await typeInto('#join-password', 'correct horse battery')
      await typeInto('#join-again', 'correct horse battery')
      await click(button('Join as Admin'))
    }

    const later = vi.fn()
    await join(later)
    expect(later).not.toHaveBeenCalled()
    expect(page()).toContain('One more step: two-factor sign-in')
    expect(page()).toContain('Admins must use it. Until then, you have Moderator rights.')
    expect(document.querySelector('[aria-current="step"]')?.textContent).toContain('Two-factor')
    await click(button('Not now, continue as Moderator'))
    expect(later).toHaveBeenCalledWith(signedIn)
    expect(client.post).not.toHaveBeenCalledWith('/api/auth/2fa/setup', expect.anything())

    const setUp = vi.fn()
    await join(setUp)
    await click(button('Set up two-factor'))
    expect(client.post).toHaveBeenCalledWith('/api/auth/2fa/setup', { password: 'correct horse battery' })
    expect(setUp).toHaveBeenCalledWith(signedIn, '/account/two-factor')
  })

  it('says when a link can’t be used, offering sign-in only for team links', async () => {
    answerPosts({ '/preview': new client.ApiError(410, { error: 'This invite was for 5 friends, and they’ve all joined.', code: 'invite_used_up', hint: 'Ask the person who sent it for a new link.', params: { inviter: '', maxUses: '5' } }) })
    let text = await render(<JoinPage code={code} onSignedIn={() => {}} />)
    expect(text).toContain('This invite has run out')
    expect(text).toContain('It was for 5 friends, and they’ve all joined.')
    expect(text).toContain('Ask whoever sent it for a new link.')
    expect(text).not.toContain('Sign in')

    answerPosts({ '/preview': new client.ApiError(410, { error: 'This invite has already been used.', code: 'invite_used_up', hint: 'If you accepted it, sign in with the username and password you chose.' }) })
    text = await render(<JoinPage code={code} onSignedIn={() => {}} />)
    expect(text).toContain('This invite link was already used')
    expect(link('Sign in').getAttribute('href')).toBe('/login')

    answerPosts({ '/preview': new client.ApiError(410, { error: 'This invite link has expired.', code: 'invite_expired', hint: 'Ask the person who sent it for a new link.' }) })
    text = await render(<JoinPage code={code} onSignedIn={() => {}} />)
    expect(text).toContain('This invite link has expired')
    expect(text).toContain('Ask whoever sent it for a new one.')

    text = await render(<JoinPage code="" onSignedIn={() => {}} />)
    expect(text).toContain('This invite link doesn’t work any more')
    expect(link('Already on the team? Sign in').getAttribute('href')).toBe('/login')
  })

  it('lets someone try again after too many tries', async () => {
    let calls = 0
    answerPosts({ '/preview': () => (++calls === 1 ? new client.ApiError(429, { error: 'Too many tries from your network.', code: 'rate_limited', hint: 'Wait a few minutes, then try again.' }) : friend) })
    const text = await render(<JoinPage code={code} onSignedIn={() => {}} />)
    expect(text).toContain('Too many tries from your network.')
    expect(text).toContain('Wait a few minutes, then try again.')
    await click(button('Try again'))
    expect(page()).toContain('You’re invited to Survival')
  })
})

describe('Team', () => {
  it('lists the owner, members and unused links, and confirms an admin', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false },
        { id: 2, username: 'mara', owner: false, you: false, role: 'moderator', servers: { servers: ['bcdefghjkm', 'abcdefghjk'] }, twoFactor: false, addedAt: hoursAgo(49), canEdit: true },
        { id: 3, username: 'tobi', owner: false, you: false, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: hoursAgo(3), canEdit: true, waiting: true, canConfirm: true },
      ],
      invites: [{ id: 'ti1', kind: 'member', projectId: 'p2345abcde', role: 'viewer', servers: { servers: ['abcdefghjk'] }, label: 'Juno', createdBy: 1, createdAt: hoursAgo(1), expiresAt: inHours(6 * 24 + 1), maxUses: 1, uses: 0, status: 'active', canEdit: true }],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [
        { id: 'abcdefghjk', name: 'Survival' },
        { id: 'bcdefghjkm', name: 'Creative' },
      ],
    }
    answer({ '/api/team': team })
    const text = await render(<TeamSection />)
    for (const line of ['You · owner', 'Owner · Admin', 'Survival and Creative', 'two-factor off', 'waiting for confirmation', 'Juno', 'Invite link not used yet · runs out in 6 days', 'Survival only', 'What each role can do']) expect(text).toContain(line)
    expect(text).toContain('tobi turned on two-factor sign-in.')
    await click(button('Confirm Admin rights'))
    expect(client.post).toHaveBeenCalledWith('/api/team/members/3/confirm-admin')
  })
})

describe('Discord', () => {
  const kinds = ['crash', 'recovered', 'low_disk', 'backup_failed', 'backup_succeeded', 'update_available', 'started', 'stopped', 'player_joined', 'player_left', 'join_requested']

  it('connects with a pasted webhook link and doesn’t keep it on the page', async () => {
    answer({ '/api/discord': { connected: false, alerts: [], liveStatus: false, delivery: {}, kinds } satisfies DiscordSettings })
    const text = await render(<DiscordSettingsSection />)
    expect(text).toContain('Alerts and live status in your Discord.')
    expect(text).toContain('Keep the link private.')
    expect(button('Connect').disabled).toBe(true)
    const url = 'https://discord.com/api/webhooks/000000000000000000/redacted-for-tests'
    await typeInto('input[type="url"]', url)
    await click(button('Connect'))
    expect(client.post).toHaveBeenCalledWith('/api/discord/connect', { webhookUrl: url })
    expect(document.querySelector<HTMLInputElement>('input[type="url"]')?.value ?? '').toBe('')
  })

  it('shows the connection, the alert switches and the live status message', async () => {
    const connected: DiscordSettings = { connected: true, webhookName: 'Playkeeper alerts', connectedAt: hoursAgo(1), alerts: ['crash', 'recovered', 'join_requested', 'backup_failed', 'low_disk', 'update_available'], liveStatus: true, delivery: { sent: hoursAgo(0.5) }, kinds }
    answer({ '/api/discord': connected })
    const text = await render(<DiscordSettingsSection />)
    expect(text).toContain('Webhook “Playkeeper alerts” · connected 1 h ago')
    for (const line of ['A server crashed or couldn’t start', 'Someone asks to join', 'Someone joined or left', 'Keep a live status message', 'How it looks in the channel']) expect(text).toContain(line)
    expect(text).not.toContain('discord.com')
    const row = [...document.querySelectorAll('li')].find((li) => li.textContent?.includes('Someone joined or left'))
    const players = row?.querySelector<HTMLElement>('[role="switch"]')
    if (!players) throw new Error('no switch for players joining')
    await click(players)
    expect(client.put).toHaveBeenCalledWith('/api/discord', { alerts: [...connected.alerts, 'player_joined', 'player_left'], liveStatus: true })
  })
})

describe('Player profile', () => {
  const maraProfile = (): PlayerProfile => ({
    name: 'mara_k',
    uuid: '0f3a6c2e9b1d4e7fa2c5b8d1e4f7a0c3',
    online: true,
    onlineSince: hoursAgo(0.5),
    allowlisted: true,
    operator: false,
    firstSeen: '2026-09-20T18:00:00Z',
    sessions: 12,
    playtimeSeconds: 14 * 3600 + 20 * 60,
    longestSeconds: 3 * 3600 + 5 * 60,
    tz: 'UTC',
    days: [],
    recent: [],
    joined: { key: 'invite.origin.link', params: { link: 'Discord crew' }, text: 'Joined with the Discord crew link', at: '2026-09-20T18:00:00Z' },
  })

  it('shows who a player is, how they got in and what a moderator can do', async () => {
    const profile = maraProfile()
    answer({ '/players/profile': profile })
    const text = await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('moderator', moderatorCan) }))
    expect(client.get).toHaveBeenCalledWith(expect.stringContaining('/api/servers/abcdefghjk/players/profile?name=mara_k&tz='))
    expect(text).toContain('On the allowlist')
    expect(text).toContain(`Joined with the Discord crew link on ${formatDate('2026-09-20T18:00:00Z')}`)
    expect(text).toContain(formatDuration(profile.playtimeSeconds))
    expect(text).toContain('12')
    expect(buttons('Send a message')).toHaveLength(1)
    expect(buttons('Kick')).toHaveLength(1)
    const viewer = await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('viewer', ['view', 'account.manage']) }))
    expect(viewer).toContain('On the allowlist')
    expect(buttons('Send a message')).toHaveLength(0)
    expect(buttons('Kick')).toHaveLength(0)
  })

  async function onPhone(check: () => Promise<void>) {
    const happy = (window as unknown as { happyDOM: { setViewport(size: { width: number; height: number }): void } }).happyDOM
    happy.setViewport({ width: 390, height: 844 })
    try {
      await check()
    } finally {
      happy.setViewport({ width: 1024, height: 768 })
    }
  }

  it('says since when a player is on the allowlist on a phone, where the line is short', async () => {
    await onPhone(async () => {
      answer({ '/players/profile': maraProfile() })
      const text = await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('moderator', moderatorCan) }))
      expect(text).toContain(`On the allowlist since ${formatDate('2026-09-20T18:00:00Z')}`)
      expect(text).not.toContain('Joined with the Discord crew link')
      expect(buttons('Message')).toHaveLength(1)
      answer({ '/players/profile': { ...maraProfile(), joined: undefined, operator: true } })
      expect(await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('moderator', moderatorCan) }))).toContain('On the allowlist · operator')
    })
  })

  it('says a banned player is banned, and offers no second ban', async () => {
    answer({ '/players/profile': { ...maraProfile(), online: false, onlineSince: undefined, banned: true } })
    const text = await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('moderator', moderatorCan) }))
    expect(text).toContain('Banned')
    expect(text).not.toContain('On the allowlist')
    await onPhone(async () => {
      const phone = await render(<PlayerProfilePage server={server()} name="mara_k" />, workspace({ me: member('moderator', moderatorCan) }))
      expect(phone).toContain('Banned')
      expect(phone).toContain('Make operator')
      expect(phone).not.toContain('Ban from Survival')
    })
  })

  it('says when there is no such player', async () => {
    answer({ '/players/profile': new client.ApiError(404, { error: 'No player called Nobody.', code: 'player_not_found' }) })
    expect(await render(<PlayerProfilePage server={server()} name="Nobody" />)).toContain('No player called Nobody on Survival.')
  })
})
