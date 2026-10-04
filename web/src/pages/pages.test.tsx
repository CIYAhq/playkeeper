// @vitest-environment happy-dom
import { act, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type {
  Action,
  AddonSources,
  Address,
  Backup,
  BackupRefusal,
  BackupRulesView,
  Candidate,
  Catalog,
  Crash,
  DiscordSettings,
  FileRefusal,
  HetznerStock,
  Invite,
  InvitesResponse,
  JoinInfo,
  JoinPreview,
  JoinRequestView,
  MachineView,
  Me,
  MemoryAdvice,
  MetricsResponse,
  ModpackCard,
  ModpackDetail,
  ModpackResults,
  OffsiteCopy,
  OffsiteTestResult,
  OffsiteView,
  Operation,
  PlayerProfile,
  PlayersSummary,
  Preflight,
  ProjectRole,
  RestorePreview,
  RetentionEstimate,
  Running,
  SaleRoom,
  ServerConfig,
  ServerStatus,
  SignInNotice,
  StoresResponse,
  TeamInvite,
  TeamMember,
  TeamResponse,
  TemplateContents,
  TemplateExport,
  TemplateLibrary,
  TemplatePlan,
  TwoFactorSetup,
  WhopPlan,
  WhopStore,
} from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { activityText } from '@/components/app/activity'
import { AddonSourcesCard } from '@/components/app/addon-sources'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { ModpackPicker } from '@/components/app/modpacks'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { AppShell } from '@/components/app/shell'
import { TemplateDialog } from '@/components/app/templates'
import { toastManager } from '@/components/ui/toast'
import { formatClock, formatDate, formatDuration, formatLongDate } from '@/lib/format'
import { parse } from '@/lib/router'
import { AiAgentsSection } from './ai-agents'
import { DiscordSettingsSection } from './discord'
import { HetznerStockCard } from './hetzner-stock'
import { SaleRoomCard } from './sale-room'
import { HomePage } from './home'
import { JoinPage } from './join'
import { DashboardMachineOnly, MachinePage } from './machine'
import { MachineSettingsPage } from './machine-settings'
import { forgetJoinCode, MachineDetailsSection, MachinesSection } from './machines'
import { createNote, NewServerPage } from './new-server'
import { Onboarding } from './onboarding'
import { RecoverPage } from './recover'
import { ServerPage } from './server'
import { BackupRulesPage } from './server/backups'
import { CopiesCard } from './server/copies'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { PlayerProfilePage } from './server/profile'
import { RunningPage } from './server/running'
import { ServerSettingsPage } from './server/settings'
import { AsleepCard } from './server/sleep'
import { WorldPage } from './server/world'
import { GlobalSettingsPage } from './settings'
import { TeamSection } from './team'
import { SellOnWhopSection } from './whop'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  put: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover', 'machines.view']
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
    reloadMe: async () => {},
    signInNotice: undefined,
    dismissSignInNotice: () => {},
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

/** Clicks an element, or the button whose text is exactly the label. */
async function click(target: HTMLElement | string) {
  const el = typeof target === 'string' ? [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === target) : target
  if (!el) throw new Error(`no button “${String(target)}”`)
  await act(async () => el.click())
  await act(async () => {})
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

const moderatorCan: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'machines.view']

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

/** The POSTs made so far, as [path under the server, body]. */
const posts = () => vi.mocked(client.post).mock.calls.map(([path, body]) => [path.replace(/^.*\/servers\/[^/]+/, ''), body])

async function press(text: string) {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.includes(text))
  if (!b) throw new Error(`no button "${text}"`)
  await act(async () => b.click())
  await act(async () => {})
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
  vi.mocked(client.api).mockReset()
  vi.mocked(client.api).mockImplementation(() => new Promise(() => {}))
  window.history.replaceState(null, '', '/')
})

/** Ticks or unticks the checkbox whose label starts with the text. */
async function toggle(label: string) {
  const input = [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))?.querySelector('input[type="checkbox"]')
  if (!(input instanceof HTMLInputElement)) throw new Error(`no checkbox “${label}”`)
  await act(async () => input.click())
  await act(async () => {})
}

describe('Home', () => {
  it('starts empty with the five first steps', async () => {
    const text = await render(<HomePage />, workspace({ servers: [] }))
    expect(text).toContain('No servers yet')
    for (const step of ['Create your first server', 'Invite a friend', 'A friend joins', 'Make a backup', 'Download it']) expect(text).toContain(step)
  })

  it('tells a paused customer their plan has ended, and until when they can download', async () => {
    const paused = member('admin', ['view', 'backups.make'], { servers: { servers: ['abcdefghjk'] }, pausedUntil: '2026-10-13T12:00:00Z' })
    const text = await render(<HomePage />, workspace({ me: paused }))
    expect(text).toContain('Your plan has ended, so your servers are paused')
    expect(text).toContain(`You can still see them and download backups until ${formatLongDate('2026-10-13T12:00:00Z')}.`)
  })

  it('tells a paused customer with no servers that their plan has ended, not that one is on its way', async () => {
    const paused = member('admin', ['view', 'backups.make'], { servers: {}, waitingForRoom: true, pausedUntil: '2026-10-13T12:00:00Z' })
    const text = await render(<HomePage />, workspace({ servers: [], me: paused }))
    expect(text).toContain('Your plan has ended')
    expect(text).toContain('Renew your plan to create your server.')
    expect(text).not.toContain('Your server is being set up')
    expect(text).not.toContain('No servers yet')
  })

  it('offers a customer whose servers were deleted their final backups, each until it goes', async () => {
    vi.mocked(client.get).mockImplementation(((path: string) =>
      path === '/api/final-backups'
        ? Promise.resolve([{ id: '20260929-150405-abc123', serverName: 'Survival', sizeBytes: 1.5 * 1024 ** 3, madeAt: '2026-10-13T12:00:00Z', expiresAt: '2026-11-12T12:00:00Z' }])
        : new Promise(() => {})) as typeof client.get)
    const deleted = member('admin', ['view', 'backups.make'], { servers: {}, serversDeleted: true, finalBackups: true })
    const text = await render(<HomePage />, workspace({ servers: [], me: deleted }))
    expect(text).toContain('Your plan has ended')
    expect(text).toContain('Survival')
    expect(text).toContain(`download until ${formatLongDate('2026-11-12T12:00:00Z')}`)
    expect(document.querySelector('a[href="/api/final-backups/20260929-150405-abc123/download"]')).not.toBeNull()
    expect(text).not.toContain('No servers yet')
  })

  it('still offers a customer who renewed their deleted servers’ final backups', async () => {
    vi.mocked(client.get).mockImplementation(((path: string) =>
      path === '/api/final-backups'
        ? Promise.resolve([{ id: '20260929-150405-abc123', serverName: 'Old survival', sizeBytes: 1.5 * 1024 ** 3, madeAt: '2026-10-13T12:00:00Z', expiresAt: '2026-11-12T12:00:00Z' }])
        : new Promise(() => {})) as typeof client.get)
    const renewed = member('admin', ['view', 'servers.run', 'backups.make', 'servers.create_own'], { servers: { servers: ['abcdefghjk'] }, finalBackups: true })
    const text = await render(<HomePage />, workspace({ me: renewed }))
    expect(text).toContain('Final backups')
    expect(text).toContain('Old survival')
    expect(document.querySelector('a[href="/api/final-backups/20260929-150405-abc123/download"]')).not.toBeNull()
  })

  it('says a customer’s server is being set up while it waits for room, and offers no way to create one', async () => {
    const text = await render(<HomePage />, workspace({ servers: [], me: member('admin', ['view', 'servers.create_own'], { servers: {}, waitingForRoom: true }) }))
    expect(text).toContain('Your server is being set up')
    expect(text).toContain('We’ll message you as soon as it’s ready.')
    expect(text).not.toContain('No servers yet')
    expect(text).not.toContain('Create your first server')
  })

  it('says on its card that a server being moved is being moved', async () => {
    await render(<HomePage />, workspace({ servers: [server({ phase: 'online', moving: true })] }))
    expect([...document.querySelectorAll('.animate-spin')].map((spin) => spin.parentElement?.textContent)).toEqual(['Being moved', 'Being moved'])
  })

  it('tells a customer who lost their machine there’s no room for their servers, not that one is being set up', async () => {
    const text = await render(<HomePage />, workspace({ servers: [], me: member('admin', ['view', 'servers.create_own'], { servers: {}, waitingForRoom: true, waitingAgain: true }) }))
    expect(text).toContain('No room for your servers yet')
    expect(text).toContain('There’s no room for your servers right now. We’ll message you as soon as there is.')
    expect(text).not.toContain('being set up')
    expect(text).not.toContain('Create your first server')
  })

  it('shows a sleeping server, the memory it gave back, and wakes it', async () => {
    const asleep = server({ phase: 'asleep', desired: 'sleeping', sleep: { enabled: true, idleMinutes: 15, listening: true } })
    const sleeping = { ...machine, live: machine.live && { ...machine.live, sleepingMemoryMB: 4096 } }
    const text = await render(<HomePage />, workspace({ machine: sleeping, machines: [sleeping], servers: [asleep] }))
    expect(text).toContain('Asleep · wakes on join')
    expect(text).toContain('Survival gave back 4 GB')
    await click(button('Wake up'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/start', {})
  })

  it('shows each server with who is playing and its address', async () => {
    const text = await render(<HomePage />, workspace({ servers: [server({ players: { online: 3, max: 10, names: ['mara_k', 'tobi2009', 'JunoFox'], source: 'rcon list', at: '' } })] }))
    expect(text).toContain('Survival')
    expect(text).toContain('Paper 26.1.2')
    expect(text).toContain('3 playing')
    expect(text).toContain(window.location.hostname)
    expect(text).toContain('1 server on my-vps · 3 playing')
  })

  const home: MachineView = {
    id: 'h2345abcde',
    projectId: machine.projectId,
    name: 'home-server',
    kind: 'remote',
    live: { ...machine.live!, hostname: 'home-server', memoryTotalMB: 32768 },
    link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
  }
  const away: MachineView = { ...home, id: 'a2345abcde', name: 'attic', live: undefined, link: { ...home.link!, machineId: 'a2345abcde', name: 'attic', state: 'offline' } }
  const activityAsked = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes('/activity'))

  it('groups servers by machine, and asks no machine for activity', async () => {
    vi.mocked(client.get).mockImplementation(((path: string) => {
      if (path === '/api/activity?limit=5')
        return Promise.resolve([
          { ts: '2026-09-25T11:00:00Z', serverId: 'cobblemon1', kind: 'restarted', actor: 'siya' },
          { ts: '2026-09-25T10:00:00Z', serverId: 'abcdefghjk', kind: 'backup', actor: 'siya' },
        ])
      if (path.includes(`/${away.id}/`)) return Promise.reject(new client.ApiError(503, { error: 'offline', code: 'machine_offline' }))
      return new Promise(() => {})
    }) as typeof client.get)
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id })
    const attic = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: away.id, lastKnownAt: new Date().toISOString() })
    const text = await render(<HomePage />, workspace({ machines: [machine, home, away], servers: [server({ machineId: machine.id }), cobblemon, attic] }))
    expect(text).toContain('On my-vps')
    expect(text).toContain('On home-server')
    expect(text).toContain('On attic')
    expect(text).toContain('3 servers on 3 machines')
    const heading = (id: string) => document.querySelector(`#on-${id} a`)?.getAttribute('href')
    expect([machine.id, home.id].map(heading)).toEqual([`/machines/${machine.id}`, `/settings/machines/${home.id}`])
    const atticCard = [...document.querySelectorAll('article')].find((a) => a.textContent?.includes('Attic'))?.textContent ?? ''
    expect(atticCard).toContain('No live status')
    expect(atticCard).toContain('Can’t reach attic')
    // As designed, the machine groups end the page: no activity or one machine's meters after them.
    expect(text).not.toContain('Across your servers')
    expect(text).not.toContain('Memory reserved')
    expect(activityAsked()).toEqual([])
    expect(vi.mocked(client.get).mock.calls.some(([p]) => String(p).includes(`/${away.id}/`))).toBe(false)
  })

  // The dashboard merges every machine's activity and adds each team join once, so Home asks it once. Home grouped by machine has no activity card (above).
  it('shows a team join once', async () => {
    answer({ '/api/activity': [{ ts: hoursAgo(1), kind: 'team_joined', actor: 'alex', detail: 'moderator' }] })
    vi.mocked(client.get).mockClear()
    const text = await render(<HomePage />, workspace({ machines: [machine], servers: [server({ machineId: machine.id })] }))
    expect(text.split('alex joined the team as Moderator').length - 1).toBe(1)
    expect(activityAsked()).toEqual(['/api/activity?limit=5'])
  })

  // Activity that is slow to come holds up only its card, which waits in place.
  it('shows the servers while the activity is still on its way', async () => {
    answer({})
    const text = await render(<HomePage />, workspace({ machines: [machine], servers: [server({ machineId: machine.id })] }))
    expect(activityAsked()).toEqual(['/api/activity?limit=5'])
    expect(text).toContain('Survival')
    expect(text).toContain('1 server on my-vps')
    const card = [...document.querySelectorAll('section, article, div')].find((el) => el.firstElementChild?.textContent === 'Across your servers')
    expect(card?.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(text).not.toContain('Nothing yet. What happens on your servers shows up here.')
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

  it('says why a scheduled backup was refused', async () => {
    answer({ '/activity': [
      { ts: hoursAgo(1), serverId: 'abcdefghjk', kind: 'backup_refused', actor: 'playkeeper', detail: 'unexpected_reply' },
      { ts: hoursAgo(2), serverId: 'abcdefghjk', kind: 'backup_refused', actor: 'playkeeper', detail: 'not_online' },
      { ts: hoursAgo(3), serverId: 'abcdefghjk', kind: 'backup_refused', actor: 'playkeeper', detail: 'something_newer' },
      { ts: hoursAgo(4), serverId: 'abcdefghjk', kind: 'backup_refused', actor: 'playkeeper', detail: 'disk_limit_reached' },
    ] })
    const text = await render(<HomePage />, workspace({ servers: both() }))
    expect(text).toContain('Scheduled backup of Survival refused · the server gave an unexpected reply')
    expect(text).toContain('Scheduled backup of Survival refused · the server was starting or stopping')
    expect(text).toContain('Scheduled backup of Survival refused · world saving couldn’t be paused')
    expect(text).toContain('Scheduled backup of Survival refused · the servers’ disk limit is reached')
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

describe('Activity', () => {
  it('names the AI agent or command-line user that acted, never its token id', () => {
    const token = { actor: 'token:t2345abcde', actorKind: 'token' as const, actorName: 'Claude on my laptop' }
    expect(activityText({ ts: '', kind: 'backup', ...token }, 'Survival', 'siya')).toBe('Claude on my laptop backed up Survival')
    expect(activityText({ ts: '', kind: 'restarted', ...token }, 'Survival', 'siya')).toBe('Claude on my laptop restarted Survival')
    expect(activityText({ ts: '', kind: 'stopped', actor: 'cli:alice', actorKind: 'cli', actorName: 'alice' }, 'Survival', 'siya')).toBe('alice stopped Survival')
    expect(activityText({ ts: '', kind: 'restarted', actor: 'siya' }, 'Survival', 'siya')).toBe('Survival restarted')
    expect(activityText({ ts: '', kind: 'backup', actor: 'siya' }, 'Survival', 'siya')).toBe('You backed up Survival')
  })
})

describe('The notice after signing in', () => {
  const wrongCodes: SignInNotice = { kind: 'failed_attempts', count: 3, text: '' }
  const codesLow: SignInNotice = { kind: 'recovery_codes_low', count: 2, text: '' }
  const signedIn = (notices: SignInNotice[]) => (
    <WorkspaceProvider me={{ ...me, notices }} onMe={() => {}} onSignedOut={() => {}}>
      <HomePage />
    </WorkspaceProvider>
  )
  const control = (name: string) => {
    const found = [...document.querySelectorAll<HTMLElement>('button, a')].find((el) => el.getAttribute('aria-label') === name || el.textContent === name)
    if (!found) throw new Error(`no control named ${name}`)
    return found
  }
  const click = (el: HTMLElement) => act(async () => el.click())
  const shown = () => document.body.textContent ?? ''

  it('says wrong codes first, then the recovery codes left, one at a time', async () => {
    await render(signedIn([{ kind: 'recovery_code_used', count: 2, text: '' }, codesLow, wrongCodes]))
    expect(shown()).toContain('Someone entered a wrong code 3 times since you last signed in')
    expect(shown()).toContain('Your password was right each time, so change it if that wasn’t you.')
    expect(shown()).not.toContain('recovery codes left')
    await click(control('It was me'))
    expect(shown()).not.toContain('wrong code')
    expect(shown()).toContain('2 recovery codes left')
    expect(control('Go to Account').getAttribute('href')).toBe('/account')
    await click(control('Dismiss'))
    expect(shown()).not.toContain('recovery codes left')
  })

  it('opens the password dialog from Change password, which dismisses it', async () => {
    await render(signedIn([wrongCodes]))
    const link = control('Change password')
    expect(link.getAttribute('href')).toBe('/account#password')
    await click(link)
    expect(window.location.pathname + window.location.hash).toBe('/account#password')
    expect(shown()).not.toContain('wrong code')
    window.history.replaceState(null, '', '/')
  })

  it('reads naturally for one wrong code and for no codes left', async () => {
    const one = await render(<HomePage />, workspace({ signInNotice: { kind: 'failed_attempts', count: 1, text: '' } }))
    expect(one).toContain('Someone entered a wrong code once since you last signed in')
    expect(one).toContain('Your password was right, so change it if that wasn’t you.')
    expect(await render(<HomePage />, workspace({ signInNotice: { kind: 'no_recovery_codes', count: 0, text: '' } }))).toContain('No recovery codes left')
  })

  it('comes before low disk space but not before the agent not answering', async () => {
    const diskWarning = { id: 'disk', label: 'Disk space', status: 'warn' as const, detail: 'Only 3 GB free.', fix: 'Free some disk space.' }
    const low = await render(<HomePage />, workspace({ signInNotice: codesLow, machine: { ...machine, live: machine.live && { ...machine.live, diskWarning } } }))
    expect(low).toContain('2 recovery codes left')
    expect(low).not.toContain('Low disk space')
    const down = await render(<HomePage />, workspace({ signInNotice: codesLow, agentDown: true, stale: true }))
    expect(down).toContain('Playkeeper can’t see your servers right now')
    expect(down).not.toContain('recovery codes left')
  })

  it('is a card above the join address on a phone’s Overview, and not on a computer’s', async () => {
    expect(await render(<Overview server={server()} />, workspace({ signInNotice: wrongCodes }))).not.toContain('wrong code')
    const real = window.matchMedia.bind(window)
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
      const list = real(query)
      if (query === '(max-width: 639px)') Object.defineProperty(list, 'matches', { value: true })
      return list
    })
    try {
      const text = await render(<Overview server={server()} />, workspace({ signInNotice: wrongCodes }))
      expect(text.indexOf('Someone entered a wrong code 3 times')).toBeGreaterThanOrEqual(0)
      expect(text.indexOf('Someone entered a wrong code 3 times')).toBeLessThan(text.indexOf('Join address'))
      expect(control('It was me').tagName).toBe('BUTTON')
      expect(control('Change password').getAttribute('href')).toBe('/account#password')
    } finally {
      phone.mockRestore()
    }
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
    const local = { ...machine, live: machine.live && { ...machine.live, diskWarning } }
    const text = await render(<Overview server={server()} />, workspace({ machine: local, machines: [local] }))
    expect(text).toContain('Low disk space: Only 0.4 GB free.')
    expect(text).toContain('Free at least 5 GB of disk space')
  })

  it('asks for a restart when settings changed', async () => {
    expect(await render(<Overview server={server({ pendingRestart: true })} />)).toContain('Restart Survival to use them.')
  })

  const memoryKill = (over: Partial<Crash>): Crash => ({
    at: new Date(Date.now() - 60_000).toISOString(),
    start: false,
    kind: 'container_memory_limit',
    params: { budget_mb: 2048, heap_mb: 1268 },
    certain: true,
    title: 'It ran out of memory',
    explanation: '',
    evidence: [],
    fixes: [
      { kind: 'raise_memory', params: { from_mb: 2048, to_mb: 3072 }, title: 'Give it 3 GB instead of 2 GB', recommended: true },
      { kind: 'restart', title: 'Start the server again' },
    ],
    lines: [],
    roomMB: 1280,
    ...over,
  })
  const noticeButton = (label: string) => {
    const notice = [...document.querySelectorAll('[role="status"]')].find((n) => n.textContent?.includes('ran out of memory'))
    return [...(notice?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.includes(label))
  }

  it('says a server that came back on its own had run out of memory, and gives it more with a restart', async () => {
    // The notice gives the time alone for a crash today and adds the date
    // for an older one, so a crash a minute before midnight isn't today.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 8, 27, 12, 0, 0))
    try {
      vi.mocked(client.post).mockClear()
      const recoveredCrash = memoryKill({})
      const text = await render(<Overview server={server({ recoveredCrash })} />)
      expect(text).toContain(`Survival ran out of memory at ${formatClock(recoveredCrash.at)}`)
      expect(text).toContain('Docker stopped it at its 2 GB limit, and Playkeeper started it again.')
      await press('Give Survival 3 GB')
      expect(posts()).toEqual([['/settings', { memoryMB: 3072, restart: true }]])
      await act(async () => noticeButton('Dismiss')?.click())
      expect(document.body.textContent).not.toContain('ran out of memory')
    } finally {
      vi.useRealTimers()
    }
  })

  it('says when the machine has no memory to give a server that ran out of it', async () => {
    const recoveredCrash = memoryKill({
      roomMB: 0,
      fixes: [
        { kind: 'lower_view_distance', params: { from: 12, to: 8 }, title: 'Lower the view distance from 12 to 8', recommended: true },
        { kind: 'upgrade_host', params: { resource: 'memory' }, title: 'Move to a machine with more memory' },
      ],
    })
    const text = await render(<Overview server={server({ recoveredCrash })} />)
    expect(text).toContain('Docker stopped it at its 2 GB limit, and Playkeeper started it again. my-vps has no memory to spare for more.')
    expect(noticeButton('Lower view distance to 8')).toBeDefined()
    expect(await render(<Overview server={server({ recoveredCrash: memoryKill({ kind: 'port_in_use', params: { port: 25565 } }) })} />)).not.toContain('ran out of memory')
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

  // The walkthrough of 1 Oct 2026: "Survival is up" under "Starting Survival failed".
  it('says the server is up in its first steps only while it is', async () => {
    const start = failed('start', 'starting', 'The server stopped while starting (exit code 1).')
    for (const s of [server({ phase: 'stopped', lastOperation: start }), server({ phase: 'starting', operation: { ...start, status: 'running' } })]) {
      const text = await render(<Overview server={s} />)
      expect(text, s.phase).toContain('Here’s how to make Survival yours.')
      expect(text, s.phase).not.toContain('is up')
    }
    const later = await render(<Overview server={server({ phase: 'stopped', firstSteps: { invited: 'mara_k', backedUp: false, downloaded: false } })} />)
    expect(later).toContain('3 small steps left for Survival.')
    expect(later).not.toContain('is up')
  })

  // The walkthrough of 4 Oct 2026: with port 25565 closed at the provider,
  // the card still said "Answering on port 25565" by a green dot. The machine
  // only pings its own server, so the line says that, and what friends need.
  it('says the server answers on its machine, and that friends need the port open at their provider', async () => {
    const text = await render(<Overview server={server()} />)
    expect(text).toContain('Answering on my-vps · checked')
    expect(text).toContain('Friends also need port 25565 open at your provider: how to open it')
    expect(link('how to open it').href).toBe('https://playkeeper.io/ports')
    const down = await render(<Overview server={server({ reachable: false })} />)
    expect(down).toContain('Not answering yet')
    expect(down).not.toContain('Friends also need port')
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
    expect(steps[1]?.textContent).not.toContain('Checksum matched')
    expect(text).not.toContain('Checksum matched')
  })

  // A create that failed before the server ever started shows this card on every
  // tab, Settings too, so its Delete server… has to open the dialog right here.
  it('deletes a server whose create never started from the card', async () => {
    const s = server({ phase: 'stopped', startedAt: undefined, lastOperation: failed('create', 'downloading_server', 'Downloading the Minecraft server software failed (exit code 1).') })
    vi.mocked(client.post).mockClear()
    await render(<Overview server={s} />)
    await press('Delete server')
    expect(document.body.textContent).toContain('Delete Survival?')
    const input = document.querySelector<HTMLInputElement>('[role="dialog"] input')
    if (!input) throw new Error('no name field in the delete dialog')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'Survival')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await press('Delete Survival')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/delete', { confirm: 'Survival' })
  })

  it('keeps a template’s skipped add-on on the Overview after setup, with Try again', async () => {
    const skipped = [{ kind: 'plan_changed', params: { name: 'ViaRewind' }, message: 'What this would do has changed since you confirmed it.' }]
    const s = server({ config: { ...config, template: { name: 'Paper check', skipped } } })
    vi.mocked(client.post).mockClear()
    const text = await render(<Overview server={s} />)
    expect(text).toContain('ViaRewind from the template isn’t installed')
    expect(text).toContain('What this would do has changed since you confirmed it.')
    await click('Try again')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/template/retry', {})

    const busy = server({ config: s.config, operation: { id: 'op-1', kind: 'template-retry', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: new Date().toISOString() } })
    await render(<Overview server={busy} />)
    const retry = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Try again'))
    expect(retry?.disabled).toBe(true)
    expect(retry?.title).toBe('Installing Survival’s template add-ons. Try again when it’s done.')

    const two = [...skipped, { kind: 'pack_hash_mismatch', params: { name: 'Terralith' }, message: 'Terralith doesn’t match.' }]
    expect(await render(<Overview server={server({ config: { ...config, template: { name: 'Paper check', skipped: two } } })} />)).toContain('ViaRewind and Terralith from the template aren’t installed')
  })

  it('says when a restored backup doesn’t record which modpack the server ran', async () => {
    const text = await render(<Overview server={server({ config: { ...config, modpackUnknown: true } })} />)
    expect(text).toContain('The backup doesn’t say which modpack Survival ran')
    expect(text).toContain('Playkeeper doesn’t manage a pack on it')
  })

  it('says when the list of what a template adds was lost, with nothing to try again', async () => {
    const text = await render(<Overview server={server({ config: { ...config, template: { name: 'Paper check', lost: true } } })} />)
    expect(text).toContain('Playkeeper lost the list of what Paper check adds')
    expect(text).toContain('None of the template’s add-ons or data packs were installed.')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent?.includes('Try again'))).toBe(false)
  })

  it('counts a modpack’s files as each one is checked', async () => {
    const at = new Date().toISOString()
    const s = server({
      name: 'Cobblemon',
      type: 'fabric',
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, minecraftVersion: '26.1.2', software: { type: 'fabric', minecraftVersion: '26.1.2', fabricLoader: '0.17.2' }, modpack: { source: 'modrinth', projectId: 'TPK00001', versionId: 'TPV00001', name: 'Cobblemon Modpack', versionNumber: '1.0.0', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_modpack', actor: 'siya', startedAt: at, detail: { packFiles: 71, packFilesTotal: 112 } },
    })
    const text = await render(<Overview server={s} />)
    expect(text).toContain('About 5 minutes. You can leave this page.')
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps).toHaveLength(5)
    expect(steps[1]).toContain('Downloaded Fabric for 26.1.2 and Fabric Loader 0.17.2')
    expect(steps[2]).toContain('Downloading the modpack’s mods')
    expect(steps[2]).toContain('71 of 112 files · each one checked')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('63')
  })

  it('counts a template’s plugins and names the ones it skipped', async () => {
    const at = new Date().toISOString()
    const skipped = [{ kind: 'addon_unsupported', params: { name: 'Simple Voice Chat' }, message: 'Simple Voice Chat has no version for Paper 26.1.2.' }]
    const s = server({
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, template: { name: 'Survival with friends', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: at, detail: { addons: 2, addonsTotal: 5, skipped } },
    })
    await render(<Overview server={s} />)
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps).toHaveLength(5)
    expect(steps[1]).toContain('Downloaded Paper 26.1.2 and checked it')
    expect(steps[2]).toContain('Downloading the template’s plugins')
    expect(steps[2]).toContain('2 of 5 files · each one checked · 1 skipped: Simple Voice Chat')
    // The agent checks the port from the VPS itself, which says nothing of a provider's firewall.
    expect(steps[4]).toBe('Checking the server answers on port 25565')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('40')
  })

  it('names a template’s data packs when it brings no plugins', async () => {
    const at = new Date().toISOString()
    const skipped = [{ kind: 'pack_hash_mismatch', params: { name: 'Terralith' }, message: 'The data pack Terralith doesn’t match the template’s checksum, so it wasn’t used.' }]
    const s = server({
      phase: 'downloading_server',
      startedAt: undefined,
      config: { ...config, template: { name: 'Survival with friends', pending: true } },
      operation: { id: 'create-1', kind: 'create', status: 'running', phase: 'installing_addons', actor: 'siya', startedAt: at, detail: { packs: 1, packsTotal: 2, skipped } },
    })
    await render(<Overview server={s} />)
    const steps = [...document.querySelectorAll('ol > li')].map((li) => li.textContent ?? '')
    expect(steps[2]).toContain('Downloading the template’s data packs')
    expect(steps[2]).toContain('1 of 2 files · each one checked · 1 skipped: Terralith')
  })

  it('opens How it’s running from the whole card, with the tick rate behind in amber', async () => {
    const at = new Date().toISOString()
    await render(<Overview server={server({ resources: { tps: 17.1, mspt: 58, lag: 'a_bit_behind', memBytes: 2 ** 31, cpuPercent: 40, at } })} />)
    const link = [...document.querySelectorAll('a')].find((a) => a.textContent === 'How it’s running')
    expect(link?.getAttribute('href')).toBe('/servers/survival/running')
    const rate = [...document.querySelectorAll('dd')].find((d) => d.textContent?.startsWith('17.1'))
    expect(rate?.textContent).toBe('17.1 · a bit behind')
    expect(rate?.className).toContain('text-warning-foreground')
  })
})

describe('How it’s running', () => {
  const at = new Date().toISOString()
  const since = new Date()
  since.setHours(18, 20, 0, 0)
  const minute = 60_000
  const metrics: MetricsResponse = {
    from: new Date(Date.now() - 60 * minute).toISOString(),
    to: at,
    bucketSeconds: 60,
    sampleIntervalSeconds: 30,
    gaps: [],
    source: 'rcon',
    buckets: Array.from({ length: 60 }, (_, i) => ({
      start: new Date(Date.now() - (60 - i) * minute).toISOString(),
      playersMax: 3,
      cpuAvg: i < 30 ? 22 : 61,
      memAvg: 2.4 * 2 ** 30,
      tpsAvg: i < 30 ? 20 : 17,
      msptAvg: i < 30 ? 31 : 58,
      coverage: 1,
      state: 'online' as const,
    })),
  }
  const behind: Running = {
    status: 'a_bit_behind',
    params: { tps: 17.1, target_tps: 20, mspt: 58.2, overloads: 4 },
    title: 'A bit behind',
    explanation: '',
    evidence: [],
    causes: [
      {
        kind: 'chunk_generation',
        params: { count: 1240 },
        score: 70,
        title: 'Players are exploring new land',
        explanation: '',
        evidence: [{ kind: 'new_chunks', params: { count: 1240, minutes: 10 }, text: '' }],
        actions: [{ kind: 'pregenerate_world', title: 'Pre-generate the map around spawn', recommended: true }],
      },
      {
        kind: 'memory_pressure',
        params: { heap_mb: 3072 },
        score: 60,
        title: 'The server is short on memory',
        explanation: '',
        evidence: [
          { kind: 'heap_after_gc', params: { used_mb: 2800, heap_mb: 3072, percent: 91.1 }, text: '' },
          { kind: 'gc_pauses', params: { percent: 9, longest_ms: 800 }, text: '' },
        ],
        actions: [{ kind: 'raise_memory', params: { from_mb: 4096, to_mb: 6144 }, title: 'Give it 6 GB instead of 4 GB', recommended: true }],
      },
      {
        kind: 'high_distance',
        params: { view_distance: 16 },
        score: 45,
        title: 'The server keeps a lot of the world running',
        explanation: '',
        evidence: [],
        actions: [{ kind: 'lower_view_distance', params: { from: 16, to: 10 }, title: 'Lower the view distance from 16 to 10', recommended: true }],
      },
    ],
    windowMinutes: 10,
    at,
    behindSince: since.toISOString(),
    players: 3,
  }
  const live = { tps: 17.1, mspt: 58.2, lag: 'a_bit_behind' as const, memBytes: 3.8 * 2 ** 30, cpuPercent: 61, at }
  const link = (text: string) => [...document.querySelectorAll('a')].find((a) => a.textContent === text)?.getAttribute('href')
  const value = (text: string) => [...document.querySelectorAll('span')].find((e) => e.textContent === text)

  it('says how far behind it is and ranks the causes, each with one action', async () => {
    answer({ '/running': behind, '/metrics': metrics })
    const text = await render(<RunningPage server={server({ resources: live })} />)
    expect(text).toContain('A bit behind: 17 of 20 ticks a second')
    expect(text).toContain('Since about 18:20, while 3 players explore new land.')
    expect(text).toContain('17.1ticks a second')
    expect(text).toContain('58ms a tick')
    expect(text).toContain('3.8of 4 GB')
    expect(text).toContain('20 is smooth')
    expect(text).toContain('50 ms budget')
    expect(text).toContain('4 GB limit')
    expect(document.querySelectorAll('svg path[stroke="#D97706"]').length).toBeGreaterThan(0)
    expect(value('3.8')?.className).toContain('text-warning-foreground')

    const rows = [...document.querySelectorAll('ol > li')].map((li) => li.textContent)
    expect(rows).toHaveLength(3)
    expect(rows[0]).toContain('New land is being built as players explore')
    expect(rows[0]).toContain('1,240 new chunks in the last 10 minutes')
    expect(rows[1]).toContain('Survival used 2.7 GB of its 3 GB, so Java keeps pausing.')
    expect(rows[1]).toContain('Pauses took 9% of the last 10 minutes')
    expect(rows[2]).toContain('View distance is 16 chunks, a lot of land per player.')
    expect(rows[2]).toContain('1,089 chunks in view per player')

    expect(link('Pre-generate the map')).toBe('/servers/survival/world/pregen')
    expect(rows[0]).not.toContain('Coming later')
    expect(document.querySelector('ol > li a')?.className).toContain('bg-primary')
    expect(link('Give it 6 GB')).toBe('/servers/survival/settings?memory=6144#memory')
    expect(link('Lower view distance to 10')).toBe('/servers/survival/settings?view=10#game')
  })

  it('says nothing is slowing it down when it keeps up', async () => {
    answer({ '/running': { ...behind, status: 'smooth', params: { tps: 20, target_tps: 20, mspt: 31, overloads: 0 }, causes: [], behindSince: undefined }, '/metrics': metrics })
    const text = await render(<RunningPage server={server({ resources: { ...live, tps: 20, mspt: 31, lag: 'smooth' } })} />)
    expect(text).toContain('Running smoothly: 20 of 20 ticks a second')
    expect(text).toContain('3 players are on and Survival has room to spare.')
    expect(text).toContain('Nothing is slowing it down')
    expect(document.querySelectorAll('ol > li')).toHaveLength(0)
    expect(value('3.8')?.className).not.toContain('text-warning-foreground')
  })

  it('shows what it measured before while the server is stopped', async () => {
    answer({ '/running': { status: 'unknown', params: { running: false }, title: 'Not running', explanation: '', evidence: [], causes: [], windowMinutes: 10 }, '/metrics': metrics })
    const text = await render(<RunningPage server={server({ phase: 'stopped' })} />)
    expect(text).toContain('Not running')
    expect(text).toContain('Playkeeper measures how Survival runs while it’s online.')
    expect(text).not.toContain('Nothing is slowing it down')
    expect(document.querySelectorAll('svg path').length).toBeGreaterThan(0)
  })
})

describe('Settings › Memory', () => {
  const peaks = [2150, 2300, 2200, 2400, 2350, 2450, 2300, 2250, 2400, 2420, 2380, 2350, 2500, 2560]
  const options: MemoryAdvice['options'] = [
    { memoryMB: 2048, heapMB: 1536, fits: true, fit: 'too_tight' },
    { memoryMB: 3072, heapMB: 2304, fits: true, fit: 'little_room' },
    { memoryMB: 4096, heapMB: 3072, fits: true, fit: 'room_to_grow' },
    { memoryMB: 6144, heapMB: 4608, fits: true, fit: 'more_than_needed' },
    { memoryMB: 8192, heapMB: 6144, fits: false, fit: 'more_than_needed' },
  ]
  const keep: MemoryAdvice = {
    verdict: 'keep',
    params: { budget_mb: 4096, heap_mb: 3072, peak_mb: 2560, days: 14, reason: 'fits', smaller_mb: 3072, smaller_heap_mb: 2304 },
    title: 'Its memory fits',
    explanation: 'It needed up to 2.5 GB in the last 14 days, and it has 3 GB for the game.',
    evidence: [],
    actions: [],
    budgetMB: 4096,
    heapMB: 3072,
    recommendedMB: 4096,
    days: peaks.map((peakMB, i) => ({ date: `2026-09-${String(12 + i).padStart(2, '0')}`, peakMB })),
    options,
  }
  const at = (path: string) => window.history.replaceState(null, '', path)

  afterEach(() => at('/'))

  it('says what the last 14 days needed, with a bar for each day', async () => {
    answer({ '/memory': keep })
    const text = await render(<ServerSettingsPage server={server()} />)
    expect(text).toContain('It never needed more than 2.5 GB in the last 14 days, so 4 GB is plenty.')
    const chart = document.querySelector('[role="img"]')
    expect(chart?.getAttribute('aria-label')).toBe('Most memory it needed each day for 14 days: at most 2.5 GB of 4 GB')
    expect(chart?.querySelectorAll('[title]')).toHaveLength(14)
    expect(chart?.textContent).toContain('14 days agoPeak each daytoday')
    expect(text).not.toContain('How much of my-vps')
    expect(text).toContain('Frees its memory while empty. Wakes when a friend joins.')
    expect(text).not.toContain('unsaved change')
  })

  it('says why a size that doesn’t fit can’t be picked', async () => {
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    answer({ '/memory': keep })
    await render(<ServerSettingsPage server={server()} />)
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="Memory"]')?.click())
    const big = [...document.querySelectorAll<HTMLButtonElement>('[role="option"]')].find((o) => o.textContent?.startsWith('8 GB'))
    expect(big?.disabled).toBe(true)
    expect(document.getElementById(big?.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Not enough free on my-vps')
    const fits = [...document.querySelectorAll('[role="option"]')].find((o) => o.textContent?.startsWith('6 GB'))
    expect(fits?.hasAttribute('aria-describedby')).toBe(false)
    phone.mockRestore()
  })

  it('counts the days until it can suggest a size', async () => {
    const early: MemoryAdvice = {
      ...keep,
      verdict: 'not_enough_data',
      params: { days: 1, min_days: 3, min_span_days: 7, peak_mb: 2200, heap_mb: 3072, span_days: 1 },
      recommendedMB: undefined,
      options: options.map((o) => ({ ...o, fit: undefined })),
    }
    answer({ '/memory': early })
    const text = await render(<ServerSettingsPage server={server()} />)
    expect(text).toContain('Suggests a size after 3 days of play. Until then, 4 GB suits up to 10 friends.')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('33')
    expect(text).toContain('Day 1 of 3')
    expect(document.querySelector('[role="img"]')).toBeNull()
  })

  it('counts fewer friends for a mod loader until it can suggest a size', async () => {
    const early: MemoryAdvice = { ...keep, verdict: 'not_enough_data', params: { days: 1, min_days: 3, min_span_days: 7, span_days: 1 }, recommendedMB: undefined, options: options.map((o) => ({ ...o, fit: undefined })) }
    answer({ '/memory': early })
    const text = await render(<ServerSettingsPage server={server({ type: 'neoforge' })} />)
    expect(text).toContain('Suggests a size after 3 days of play. Until then, 4 GB suits up to 4 friends.')
  })

  it('opens at the section in the address, and glides to a section pressed in the list', async () => {
    const scrolled = vi.fn()
    const scrollIntoView = Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = function (this: Element, arg?: boolean | ScrollIntoViewOptions) {
      scrolled(this.id, arg)
    }
    at('/servers/survival/settings#memory')
    answer({ '/memory': keep })
    await render(<ServerSettingsPage server={server()} />)
    const current = () => document.querySelector('nav [aria-current="location"]')?.textContent
    expect(current()).toBe('Memory')
    expect(scrolled).toHaveBeenLastCalledWith('memory', { block: 'start' })
    const entry = [...document.querySelectorAll('nav a')].find((a) => a.textContent === 'Minecraft version')
    await click(entry as HTMLElement)
    expect(scrolled).toHaveBeenLastCalledWith('version', { block: 'start', behavior: 'smooth' })
    expect(current()).toBe('Minecraft version')
    expect(window.location.hash).toBe('#version')
    expect(document.activeElement?.id).toBe('version')
    // Typing afterwards doesn't pull the page back to the section it opened at.
    await typeInto('#server-name', 'Survival 2')
    expect(scrolled).toHaveBeenCalledTimes(2)
    Element.prototype.scrollIntoView = scrollIntoView
  })

  it('takes the fixes How it’s running links to as unsaved changes', async () => {
    at('/servers/survival/settings?memory=6144&view=10#memory')
    answer({ '/memory': { ...keep, verdict: 'raise', params: { budget_mb: 4096, heap_mb: 3072, days: 14, full_gcs: 2, to_mb: 6144 }, recommendedMB: 6144 } })
    const text = await render(<ServerSettingsPage server={server({ gameplay: { viewDistance: 16 } })} />)
    expect(text).toContain('It ran short of memory in the last 14 days, so give it 6 GB.')
    expect(text).toContain('2 unsaved changes')
    expect(document.querySelector('#memory [aria-label="Memory"]')?.textContent).toContain('6 GB')
    expect(document.querySelector('nav [aria-current="location"]')?.textContent).toBe('Memory')
  })

  it('ignores a size the machine has no room for', async () => {
    at('/servers/survival/settings?memory=8192#memory')
    answer({ '/memory': keep })
    const text = await render(<ServerSettingsPage server={server()} />)
    expect(text).not.toContain('unsaved change')
  })
})

describe('Backups with players online', () => {
  const backup = (over: Partial<Backup> = {}): Backup => ({
    id: 'b1',
    serverId: 'abcdefghjk',
    kind: 'manual',
    createdAt: new Date().toISOString(),
    fileName: 'survival-2026-09-25-1847.tar.gz',
    sizeBytes: 312 * 2 ** 20,
    sha256: 'a'.repeat(64),
    location: 'local',
    verified: true,
    downtimeMs: 0,
    method: 'online_copy',
    savingPausedMs: 1800,
    durationMs: 17_400,
    minecraftVersion: '26.1.2',
    levelName: 'world',
    fileCount: 2114,
    createdBy: 'siya',
    note: 'Before the nether trip',
    ...over,
  })
  const older = backup({ id: 'b0', method: 'stopped', downtimeMs: 14_000, durationMs: 16_000, fileCount: 1902, note: undefined, createdAt: '2026-09-22T20:30:00Z' })
  const since = new Date()
  since.setHours(18, 47, 0, 0)

  it('says players stay online and how long the last one took', async () => {
    answer({ '/backups': [backup(), older] })
    const text = await render(<WorldPage server={server()} />)
    expect(text).toContain('Players stay online. It takes about 20 seconds.')
    expect(text).toContain('Manual · no downtime · 2,114 files')
    expect(text).toContain('Manual · 14 s offline · 1,902 files')
    expect(text).toContain('Stored on this VPS. Download one to keep it safe.')
    expect(text).toContain('Your current world is saved first, so you can undo.')
    expect(text).not.toContain('World saving is paused')
  })

  it('makes the first backup without a warning in chat', async () => {
    answer({ '/backups': [] })
    const text = await render(<WorldPage server={server({ players: { online: 2, max: 10, names: ['mara_k', 'tobi2009'], source: 'rcon list', at: '' } })} />)
    expect(text).toContain('About 15 seconds.')
    expect(text).not.toContain('About 15 seconds. Players stay online.')
    expect(text).not.toContain('heads-up')
    expect(await render(<Overview server={server()} />)).toContain('Make your first backupTakes about 15 s.')
  })

  it('says when world saving is paused, with the console and a way to turn it back on', async () => {
    vi.mocked(client.post).mockClear()
    answer({ '/backups': [backup()] })
    const text = await render(<WorldPage server={server({ savingPausedSince: since.toISOString() })} />)
    expect(text).toContain('World saving is paused')
    expect(text).toContain('If Survival stops unexpectedly, progress since 18:47 could be lost.')
    const link = [...document.querySelectorAll('a')].find((a) => a.textContent === 'Open console')
    expect(link?.getAttribute('href')).toBe('/servers/survival/console')
    await press('Turn saving back on')
    expect(posts()).toEqual([['/saving/resume', undefined]])
    const restarting: Operation = { id: 'op-2', kind: 'restart', status: 'running', phase: 'stopping', actor: 'siya', startedAt: since.toISOString() }
    await render(<WorldPage server={server({ savingPausedSince: since.toISOString(), operation: restarting })} />)
    const resume = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Turn saving back on')
    expect([resume?.disabled, resume?.title]).toEqual([true, 'Restarting Survival. Try again when it’s done.'])
  })

  it('offers a backup with the server stopped when the console couldn’t take one', async () => {
    vi.mocked(client.post).mockClear()
    answer({ '/backups': [backup()] })
    const timeout: Operation = { ...failed('backup', 'saving', 'The server didn’t confirm the save within 1m0s.', { errorKind: 'save_timeout', timeoutMs: 60_000 }), hint: 'Try again, or back up with the server stopped.' }
    const text = await render(<WorldPage server={server({ lastOperation: timeout })} />)
    expect(text).toContain('Backing up Survival failed: The server didn’t confirm the save within 1m0s.')
    await press('Stop and back up')
    expect(posts()).toEqual([['/backups', { stopped: true }]])
    const updating: Operation = { id: 'op-2', kind: 'update-version', status: 'running', phase: '', actor: 'siya', startedAt: new Date().toISOString() }
    await render(<WorldPage server={server({ lastOperation: timeout, operation: updating })} />)
    const stopped = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Stop and back up')
    expect(stopped?.disabled).toBe(true)
    expect(stopped?.title).toMatch(/Try again when it’s done\.$/)
  })

  it('leaves a backup refused for space to its own advice', async () => {
    answer({ '/backups': [backup()] })
    const space = failed('backup', '', 'Not enough disk space for a backup.', { errorKind: 'insufficient_space', neededBytes: 2 ** 30 })
    const text = await render(<WorldPage server={server({ lastOperation: space, resources: { diskFreeBytes: 400 * 2 ** 20, at: new Date().toISOString() } })} />)
    expect(text).toContain('Backing up Survival failed')
    expect(text).not.toContain('Stop and back up')
  })

  const unexpected = 'The server gave an unexpected reply to "save-off": "Unknown command". No backup was made.'
  const refusal = (over: Partial<BackupRefusal> = {}): BackupRefusal => ({
    at: hoursAgo(2),
    since: hoursAgo(26),
    count: 2,
    kind: 'unexpected_reply',
    error: unexpected,
    hint: 'A plugin or mod may have changed this command. Back up with the server stopped instead.',
    scheduleId: 'qrstuvwxyz',
    operationId: 'backup-1',
    ...over,
  })
  const refusedNotice = () => [...document.querySelectorAll('[role="status"]')].find((n) => n.textContent?.includes('refused'))
  /** The refusal notice's "Back up now", not the backup card's. */
  function backUpFromNotice(): HTMLButtonElement {
    const b = refusedNotice()?.querySelector('button')
    if (!b) throw new Error('no Back up now in the refusal notice')
    return b
  }

  it('keeps refused scheduled backups on the World tab, and stops the server for a backup only after saying so', async () => {
    vi.mocked(client.post).mockClear()
    answer({ '/backups': [backup()] })
    const refused = failed('backup', 'saving', unexpected, { errorKind: 'unexpected_reply' })
    const text = await render(<WorldPage server={server({ backupRefused: refusal(), lastOperation: refused, lastBackup: backup({ createdAt: '2026-09-01T10:00:00Z' }) })} />)
    expect(text).toContain('2 scheduled backups in a row were refused')
    expect(text).toContain(`${unexpected} Scheduled backups never stop the server. The last backup is from ${formatDate('2026-09-01T10:00:00Z')}.`)
    expect(text).not.toContain('Backing up Survival failed')
    await click(backUpFromNotice())
    expect(posts()).toEqual([])
    expect(page()).toContain('Stop Survival for a backup?')
    expect(page()).toContain('Survival stops for the backup and starts again once it’s made, so anyone playing is disconnected until then.')
    await press('Stop and back up')
    expect(posts()).toEqual([['/backups', { stopped: true }]])
  })

  it('backs up a server that isn’t running as it is, and still shows a later failure', async () => {
    vi.mocked(client.post).mockClear()
    answer({ '/backups': [] })
    const stopped = server({ phase: 'stopped', desired: 'stopped', startedAt: undefined, backupRefused: refusal({ count: 1, kind: 'not_online', error: 'Survival is starting or stopping, so it can’t be backed up right now.' }) })
    const text = await render(<WorldPage server={stopped} />)
    expect(text).toContain('A scheduled backup was refused')
    expect(text).toContain('Survival was starting or stopping, so no backup was made. Scheduled backups never stop the server, and there’s no backup of this world yet.')
    expect(text).not.toContain('right now')
    await click(backUpFromNotice())
    expect(posts()).toEqual([['/backups', {}]])
    expect(page()).not.toContain('for a backup?')
    const space = { ...failed('backup', '', 'Not enough disk space for a backup.', { errorKind: 'insufficient_space' }), id: 'backup-2' }
    const later = await render(<WorldPage server={{ ...stopped, lastOperation: space }} />)
    expect(later).toContain('A scheduled backup was refused')
    expect(later).toContain('Backing up Survival failed: Not enough disk space for a backup.')
  })

  it('shows refused backups beside world saving paused, waits for a starting server, and gives no button without backup rights', async () => {
    answer({ '/backups': [backup()] })
    const paused = await render(<WorldPage server={server({ backupRefused: refusal(), savingPausedSince: since.toISOString() })} />)
    expect(paused).toContain('World saving is paused')
    expect(paused).toContain('2 scheduled backups in a row were refused')
    await render(<WorldPage server={server({ phase: 'starting', backupRefused: refusal() })} />)
    expect([backUpFromNotice().disabled, backUpFromNotice().title]).toEqual([true, 'Starting Survival. Try again when it’s done.'])
    const viewer = await render(<WorldPage server={server({ backupRefused: refusal() })} />, workspace({ me: member('viewer', ['view', 'account.manage']) }))
    expect(viewer).toContain('2 scheduled backups in a row were refused')
    expect(refusedNotice()?.querySelector('button')).toBeNull()
    const cleared = await render(<WorldPage server={server()} />)
    expect(cleared).not.toContain('refused')
  })

  it('says what to do when the disk limit stops scheduled backups, with no button', async () => {
    answer({ '/backups': [backup()] })
    const limit = refusal({ count: 1, kind: 'disk_limit_reached', error: 'That needs about 1.2 GB, and these servers have 300.0 MB of their 30.0 GB disk limit left.', hint: 'Delete backups or files you don’t need to make room.' })
    const text = await render(<WorldPage server={server({ backupRefused: limit })} />)
    expect(text).toContain('A scheduled backup was refused')
    expect(text).toContain('That needs about 1.2 GB, and these servers have 300.0 MB of their 30.0 GB disk limit left. Delete backups or files you don’t need to make room.')
    expect(text).not.toContain('Scheduled backups never stop the server')
    expect(refusedNotice()?.querySelector('button')).toBeNull()
  })

  it('puts world saving paused first on the Overview, and drops its failure once saving is back on', async () => {
    const paused = failed('backup', '', 'World saving is still paused, and turning it back on failed. No backup was saved.', { errorKind: 'saving_paused', savingPaused: true })
    const text = await render(<Overview server={server({ lastOperation: paused, savingPausedSince: since.toISOString() })} />)
    expect(text).toContain('If Survival stops unexpectedly, progress since 18:47 could be lost.')
    expect(text).not.toContain('Backing up Survival failed')
    const after = await render(<Overview server={server({ lastOperation: paused })} />)
    expect(after).not.toContain('World saving is')
    expect(after).not.toContain('Backing up Survival failed')
  })
})

describe('Crash helper', () => {
  const crash = (over: Partial<Crash>): Crash => ({
    at: new Date(Date.now() - 120_000).toISOString(),
    start: false,
    kind: 'unknown',
    certain: true,
    title: '',
    explanation: '',
    evidence: [],
    fixes: [],
    lines: [],
    roomMB: 3584,
    ...over,
  })
  const oom = crash({
    kind: 'heap_out_of_memory',
    params: { budget_mb: 4096, heap_mb: 3072 },
    fixes: [
      { kind: 'raise_memory', params: { from_mb: 4096, to_mb: 6144 }, title: 'Give it 6 GB instead of 4 GB', recommended: true },
      { kind: 'restart', title: 'Start the server again' },
    ],
    lines: [
      { time: '18:52:40', level: 'WARN', text: 'Can’t keep up! Is the server overloaded? Running 5210ms or 104 ticks behind' },
      { time: '18:52:57', level: 'ERROR', text: 'java.lang.OutOfMemoryError: Java heap space' },
      { time: '18:52:58', text: 'Stopping server' },
    ],
  })

  const labelled = (text: string) => [...document.querySelectorAll('label')].find((l) => l.textContent?.includes(text))

  it('offers more memory after running out of it, and saves it before starting', async () => {
    vi.mocked(client.post).mockClear()
    const text = await render(<Overview server={server({ phase: 'crashed', crash: oom })} />)
    expect(text).toContain('It ran out of its 4 GB of memory.')
    expect(text).toContain('Give Survival 6 GBRecommended')
    expect(text).toContain('Fits in the 3.5 GB free')
    expect(text).toContain('Keep 4 GB and start again')
    expect(text).toContain('java.lang.OutOfMemoryError: Java heap space')
    expect(text).toContain('Stopping server')
    await press('Save and start Survival')
    expect(posts()).toEqual([
      ['/settings', { memoryMB: 6144 }],
      ['/start', undefined],
    ])
  })

  it('explains a start that failed and removes the add-on it names', async () => {
    vi.mocked(client.post).mockClear()
    const plugin = crash({
      start: true,
      kind: 'addon_failed',
      certain: false,
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.0.2.jar' },
      fixes: [
        { kind: 'update_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Update Multiverse-Portals-5.0.2.jar', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Remove Multiverse-Portals-5.0.2.jar' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: plugin })} />)
    expect(text).toContain('Multiverse-Portals hit an error while starting.')
    expect(text).toContain('Update Multiverse-PortalsChecking the library…')
    expect(text).not.toContain('Recommended')
    expect(labelled('Update Multiverse-Portals')?.querySelector('[data-disabled]')).not.toBeNull()
    expect(labelled('Remove Multiverse-Portals')?.querySelector('[data-checked]')).not.toBeNull()
    await press('Remove and start Survival')
    expect(posts()).toEqual([['/addons/remove-file', { jar: 'Multiverse-Portals-5.0.2.jar', start: true }]])
  })

  const portals = { source: 'modrinth', projectId: 'mvportal' } as const
  const core = { source: 'modrinth', projectId: 'mvcore00' } as const
  const portalsRecord = { ...portals, name: 'Multiverse-Portals', slug: 'multiverse-portals', summary: '', versionId: 'v1', versionNumber: '5.0.2', channel: 'release', published: '2026-06-02T12:00:00Z', fileName: 'Multiverse-Portals-5.0.2.jar', size: 1000, installedAt: '2026-09-20T10:00:00Z' }
  const addonFiles = (status: 'managed' | 'unknown') => ({
    target: { kind: 'plugin', folder: 'plugins', sources: ['modrinth', 'hangar'], categories: [], minecraftVersion: '26.1.2' },
    files: [{ fileName: 'Multiverse-Portals-5.0.2.jar', size: 1000, status, addon: status === 'managed' ? portalsRecord : undefined }],
    missing: [],
    warnings: [],
    restartNeeded: false,
  })
  const plan = (key: typeof portals | typeof core, name: string, version: string) => ({
    steps: [{ action: key === core ? 'install' : 'update', ...key, name, versionNumber: version, channel: 'release', fileName: `${name}-${version}.jar`, size: 1000 }],
    manual: [],
    blockers: [],
    warnings: [],
    ready: true,
    fingerprint: 'c'.repeat(32),
  })

  it('updates the plugin that broke through the library, then starts', async () => {
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/addons/update/plan') ? plan(portals, 'Multiverse-Portals', '5.1.0') : {})) as typeof client.post)
    answer({ '/addons': addonFiles('managed') })
    const plugin = crash({
      start: true,
      kind: 'addon_failed',
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.0.2.jar' },
      fixes: [
        { kind: 'update_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Update it', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Remove it' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: plugin })} />)
    expect(text).toContain('Update Multiverse-Portals to 5.1.0RecommendedMade for 26.1.2')
    expect(text).toContain('Remove Multiverse-Portals')
    expect(labelled('Update Multiverse-Portals to 5.1.0')?.querySelector('[data-checked]')).not.toBeNull()
    await press('Update and start Survival')
    expect(posts()).toEqual([
      ['/addons/update/plan', { addons: [portals] }],
      ['/addons/update', { addons: [portals], fingerprint: 'c'.repeat(32), start: true }],
    ])
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  })

  it('says a plugin added by hand can’t be updated, and keeps Remove', async () => {
    vi.mocked(client.post).mockClear()
    answer({ '/addons': addonFiles('unknown') })
    const plugin = crash({
      start: true,
      kind: 'addon_failed',
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.0.2.jar' },
      fixes: [
        { kind: 'update_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Update it', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.0.2.jar' }, title: 'Remove it' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: plugin })} />)
    expect(text).toContain('Update Multiverse-PortalsAdded by hand, so Playkeeper can’t update it')
    expect(labelled('Remove Multiverse-Portals')?.querySelector('[data-checked]')).not.toBeNull()
    expect(posts()).toEqual([])
  })

  it('installs the missing plugin from the library, then starts', async () => {
    vi.mocked(client.post).mockClear()
    const card = { ...core, slug: 'multiverse-core', name: 'Multiverse-Core', summary: '', categories: [], downloads: 900000, updated: '2026-09-01T00:00:00Z', pageUrl: '', installed: false }
    answer({
      '/addons/search': { cards: [{ ...card, projectId: 'other', name: 'Multiverse-Inventories', slug: 'multiverse-inventories' }, card], more: false, unanswered: [] },
      '/addons/project/modrinth/mvcore00': { card, plan: plan(core, 'Multiverse-Core', '5.1.2') },
    })
    const dep = crash({
      start: true,
      kind: 'missing_dependency',
      params: { addon: 'Multiverse-Portals', jar: 'Multiverse-Portals-5.1.0.jar', dependencies: ['Multiverse-Core'] },
      fixes: [
        { kind: 'install_addon', params: { name: 'Multiverse-Core' }, title: 'Install Multiverse-Core', recommended: true },
        { kind: 'remove_addon', params: { jar: 'Multiverse-Portals-5.1.0.jar' }, title: 'Remove it' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: dep })} />)
    expect(text).toContain('Multiverse-Portals needs Multiverse-Core, which isn’t installed.')
    expect(text).toContain('Install Multiverse-Core 5.1.2RecommendedThe version it asks for')
    expect(vi.mocked(client.get).mock.calls.map(([path]) => path)).toContain('/api/servers/abcdefghjk/addons/search?q=Multiverse-Core')
    await press('Install and start Survival')
    expect(posts()).toEqual([['/addons/install', { ...core, fingerprint: 'c'.repeat(32), start: true }]])
  })

  it('names the program on a taken port', async () => {
    const port = crash({
      start: true,
      kind: 'port_in_use',
      params: { port: 25565, holder: 'java', holder_pid: 48211 },
      fixes: [
        { kind: 'change_port', params: { port: 25565 }, title: 'Change the port', recommended: true },
        { kind: 'restart', title: 'Start again' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: port })} />)
    expect(text).toContain('Another program on my-vps is using port 25565.')
    expect(text).toContain('It’s java, process 48211, not started by Playkeeper.')
    expect(labelled('Start again on 25565')?.querySelector('[data-checked]')).not.toBeNull()
    expect(labelled('Move Survival to another port')?.title).toBe('Coming later')
  })

  it('names the Docker container on a taken port', async () => {
    const port = crash({
      start: true,
      kind: 'port_in_use',
      params: { port: 25565, holder_container: 'old-minecraft' },
      fixes: [
        { kind: 'change_port', params: { port: 25565 }, title: 'Change the port', recommended: true },
        { kind: 'restart', title: 'Start again' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: port })} />)
    expect(text).toContain('Another program on my-vps is using port 25565.')
    expect(text).toContain('It’s the Docker container old-minecraft, not started by Playkeeper.')
    expect(labelled('Start again on 25565')?.querySelector('[data-checked]')).not.toBeNull()
  })

  it('puts the damaged area back as the agent recommends, or restores the backup it names', async () => {
    vi.mocked(client.post).mockClear()
    const made = new Date()
    made.setHours(0, 5, 0, 0)
    const world = crash({
      kind: 'corrupt_world',
      certain: false,
      params: { chunk_x: 64, chunk_z: -32 },
      fixes: [
        { kind: 'restart', title: 'Start the server again', recommended: true },
        { kind: 'restore_backup', params: { backup_id: 'b20260925', made_at: made.toISOString() }, title: 'Restore the latest backup of the world' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'crashed', crash: world })} />)
    expect(text).toContain('Part of the world is damaged, around x 1,024, z -512.')
    expect(text).toContain('Rebuild just the damaged areaRecommended')
    expect(text).toContain('Restore today’s 00:05 backup')
    expect(text).toContain('Anything built after 00:05 is lost')
    expect(text).not.toContain('Your current world is saved first')
    await act(async () => labelled('Restore today’s')?.click())
    expect(document.body.textContent).toContain('Your current world is saved first, so you can undo.')
    vi.mocked(client.post).mockResolvedValueOnce({
      id: 'r1',
      serverId: 'abcdefghjk',
      source: 'backup',
      receivedAt: made.toISOString(),
      sizeBytes: 2 ** 30,
      sha256: 'a'.repeat(64),
      compatible: true,
      problems: [],
      warnings: [],
      currentWorld: { exists: true, levelName: 'world', sizeBytes: 2 ** 30 },
      willCreateRollback: true,
      needsEula: false,
      memoryMB: 4096,
      confirmPhrase: 'Survival',
      steps: [],
      notRestored: [],
    } satisfies RestorePreview)
    await press('Restore and start Survival')
    expect(posts()).toEqual([['/backups/b20260925/restore', undefined]])
    expect(document.body.textContent).toContain('Restore this backup?')
  })

  it('takes the entity that crashes the server out of its world, then starts', async () => {
    vi.mocked(client.post).mockClear()
    const target = { what: 'entity', type: 'minecraft:minecart', dimension: 'minecraft:overworld', x: 6, y: 120, z: 6, pos: [6.5, 120, 6.5] }
    const ticking = crash({
      kind: 'ticking_entity',
      params: { what: 'entity', type: 'minecraft:minecart', name: 'Minecart', x: 6, y: 120, z: 6, dimension: 'minecraft:overworld' },
      fixes: [{ kind: 'remove_entity', params: target, title: 'Remove the minecart at 6, 120, 6', recommended: true }],
    })
    const text = await render(<Overview server={server({ phase: 'crashed', crash: ticking })} />)
    expect(text).toContain('The minecart at x 6, y 120, z 6 crashes it each time the game runs it.')
    expect(text).toContain('It’s in the Overworld, saved in the world, so starting again crashes again.')
    expect(text).toContain('Remove the minecartRecommendedOnly it goes. Blocks, chests and other mobs stay.')
    expect(text).not.toContain('Start Survival again')
    await press('Back up, remove and start Survival')
    expect(posts()).toEqual([['/world/remove-entity', { ...target, start: true }]])
  })

  it('makes a new level.dat only once the owner has read what resets', async () => {
    vi.mocked(client.post).mockClear()
    const made = new Date()
    made.setHours(0, 5, 0, 0)
    const level = crash({
      start: true,
      kind: 'corrupt_world',
      params: { file: 'level.dat', world: 'world' },
      fixes: [
        { kind: 'restore_backup', params: { backup_id: 'b20260925', made_at: made.toISOString() }, title: 'Restore the latest backup of the world', recommended: true },
        { kind: 'rebuild_level', params: { world: 'world', seed_from: 'backup', resets: ['game_rules', 'time', 'spawn'] }, title: 'Make a new level.dat' },
      ],
    })
    const text = await render(<Overview server={server({ phase: 'stopped', crash: level })} />)
    expect(text).toContain('The world’s level.dat file is damaged.')
    expect(text).toContain('Restore today’s 00:05 backupRecommended')
    expect(text).toContain('Make a new level.datKeeps every build. Resets the game rules, the time of day and the spawn point.')
    await act(async () => labelled('Make a new level.dat')?.click())
    expect(document.body.textContent).toContain('Your world is backed up first, so you can undo.')
    await press('Back up, repair and start Survival')
    expect(posts()).toEqual([])
    const dialog = document.body.textContent ?? ''
    expect(dialog).toContain('Make a new level.dat for Survival?')
    expect(dialog).toContain('The seed, read from a backup, so new terrain matches the old')
    expect(dialog).toContain('The game rules go back to their defaults')
    expect(dialog).toContain('The spawn point goes back to where the world first had it')
    expect(dialog).not.toContain('won’t match the old')
    await press('Back up and repair')
    expect(posts()).toEqual([['/world/rebuild-level', { world: 'world', seedFrom: 'backup', start: true }]])
  })

  it('says plainly that new terrain won’t match when it can’t find the seed', async () => {
    for (const [seedFrom, line] of [
      ['', 'The seed: Playkeeper can’t find the world’s, so new terrain won’t match the old'],
      ['properties', 'The seed: Playkeeper can’t find the world’s, so new terrain won’t match the old unless the level-seed in server.properties is the one it was made with'],
    ]) {
      vi.mocked(client.post).mockClear()
      const level = crash({
        start: true,
        kind: 'corrupt_world',
        params: { file: 'level.dat', world: 'world' },
        fixes: [{ kind: 'rebuild_level', params: { world: 'world', seed_from: seedFrom, resets: ['spawn'] }, title: 'Make a new level.dat', recommended: true }],
      })
      const text = await render(<Overview server={server({ phase: 'stopped', crash: level })} />)
      expect(text).toContain('Keeps every build. Resets the spawn point. New terrain won’t match the old')
      await press('Back up, repair and start Survival')
      const dialog = document.body.textContent ?? ''
      expect(dialog).toContain(line)
      expect(dialog).not.toContain('so new terrain matches the old')
      await press('Back up and repair')
      expect(posts()).toEqual([['/world/rebuild-level', { world: 'world', seedFrom, start: true }]])
    }
  })

  it('deletes the oldest backups it planned, then starts', async () => {
    vi.mocked(client.post).mockClear()
    vi.mocked(client.del).mockClear()
    const disk = crash({
      kind: 'disk_full',
      params: { free_mb: 180, backups_mb: 18636, disk_mb: 81920 },
      fixes: [{ kind: 'free_disk', params: { free_mb: 180, backup_ids: ['b1', 'b2'], backups: 2, frees_mb: 5530, keep: 3 }, title: 'Free up disk space', recommended: true }],
    })
    const text = await render(<Overview server={server({ phase: 'crashed', crash: disk })} />)
    expect(text).toContain('my-vps ran out of disk space, so Survival stopped to keep the world safe.')
    expect(text).toContain('Backups use 18.2 GB of the 80 GB disk.')
    expect(text).toContain('Delete the 2 oldest backups')
    expect(text).toContain('Frees 5.4 GB. The 3 newest stay.')
    expect(text).toContain('I’ll make room myself')
    await press('Delete 2 backups and start Survival')
    expect(vi.mocked(client.del).mock.calls.map(([path]) => path.replace(/^.*\/servers\/[^/]+/, ''))).toEqual(['/backups/b1', '/backups/b2'])
    expect(posts()).toEqual([['/start', undefined]])
  })

  it('falls back to the agent’s error when there is no diagnosis', async () => {
    answer({ '/logs': { epoch: 'e', lines: [{ seq: 1, ts: '2026-09-25T18:52:57Z', text: '[18:52:57 ERROR]: Something broke' }], next: 1 } })
    const text = await render(<Overview server={server({ phase: 'crashed', lastError: 'Pulling the server image failed.', lastErrorHint: 'Check the internet connection.' })} />)
    expect(text).toContain('Pulling the server image failed.')
    expect(text).toContain('Check the internet connection.')
    expect(text).toContain('Something broke')
    expect(text).toContain('Start Survival again')
    expect(text).toContain('Start Survival')
  })

  it('says which file stopped a start and what to do in one line, for a link and a named pipe', async () => {
    const link: FileRefusal = { code: 'link', params: { path: 'plugins/bStats/config.yml' }, message: 'plugins/bStats/config.yml in the server’s files is a link, which Playkeeper does not follow.', hint: 'Delete it.' }
    const pipe: FileRefusal = { code: 'special_file', params: { path: 'plugins/bStats/config.yml', type: 'named_pipe' }, message: 'plugins/bStats/config.yml in the server’s files is not a normal file (it is a named pipe).', hint: 'Delete it.' }
    for (const [refusal, line] of [
      [link, 'Playkeeper won’t start Survival while plugins/bStats/config.yml is a link. Delete it, or replace it with what it points to.'],
      [pipe, 'Playkeeper won’t start Survival while plugins/bStats/config.yml isn’t a normal file. Delete it.'],
    ] as const) {
      vi.mocked(client.get).mockClear()
      vi.mocked(client.post).mockClear()
      const lastOperation = failed('start', '', 'Paper’s bStats usage statistics could not be switched off, so the server was not started. ' + refusal.message)
      const text = await render(<Overview server={server({ phase: 'stopped', startedAt: undefined, exitCode: 0, lastOperation, lastError: lastOperation.error, refusal })} />)
      expect(text.split(line)).toHaveLength(2)
      expect(text.split('What happened')).toHaveLength(2)
      expect(text).toContain('Start Survival againRecommendedOnce it’s deleted')
      expect(text).not.toContain('bStats usage statistics')
      expect(text).not.toContain('Last lines before it stopped')
      expect(vi.mocked(client.get).mock.calls.filter(([path]) => path.includes('/logs'))).toEqual([])
      await press('Start Survival')
      expect(posts()).toEqual([['/start', undefined]])
    }
    const dockerDown = await render(<Overview server={server({ phase: 'docker_unavailable', lastError: 'Docker is not responding, so Playkeeper cannot see or control the server.', refusal: link })} />)
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

describe('Templates', () => {
  const contents: TemplateContents = {
    name: 'Survival with friends',
    type: 'paper',
    minecraftVersion: '26.1.2',
    settings: { difficulty: 'normal', pvp: false, viewDistance: 10, maxPlayers: 10, memoryMB: 3072 },
    addons: [
      { source: 'modrinth', name: 'Chunky', versionNumber: '1.4.40' },
      { source: 'hangar', name: 'LuckPerms', versionNumber: 'v5.5.0' },
    ],
    resourcePacks: 0,
    dataPacks: 0,
    packs: [],
  }
  const plan: TemplatePlan = { contents, type: 'paper', versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 3072, skipped: [], warnings: [], blockers: [], ready: true, fingerprint: 'fp-1' }
  const catalog: Catalog = { type: 'paper', types: [], versions: [], memoryOptionsMB: [2048, 3072, 4096], recommendedMemoryMB: 2048, hostMemoryMB: 16384, maxMemoryMB: 4096, systemReserveMB: 1536, memoryFreeMB: 10752, servers: [], image: '' }

  it('starts a server from a shared link, sending only the plan the user saw', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue(plan)
    vi.mocked(client.post).mockClear()
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    const [method, path, , body] = vi.mocked(client.api).mock.calls[0] ?? []
    expect([method, path]).toEqual(['POST', '/api/machines/m2345abcde/templates/plan'])
    expect(await (body as Blob).text()).toBe('eyJ2IjoxfQ')
    expect(window.location.hash).toBe('')
    const text = document.body.textContent ?? ''
    expect(text).not.toContain('· from')
    for (const line of ['Survival with friends', 'From a link', 'Paper 26.1.2', '2, with the same versions', 'Chunky, LuckPerms', 'Normal difficulty · friends can’t hurt each other · view distance 10 · up to 10 players', 'The new server starts with fresh land', 'Paper, from the template', 'Type, version and plugins come from the template']) expect(text).toContain(line)

    await click('Continue to memory')
    expect(document.body.textContent).toContain('The template suggests 3 GB.')
    await click('Continue to name')
    await toggle('I accept the Minecraft End User License Agreement')
    await click('Create and start Survival with friends')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/machines/m2345abcde/servers', { name: 'Survival with friends', acceptEula: true, memoryMB: 3072, acceptExperimental: false, template: { fingerprint: 'fp-1' } })
  })

  it('lists Playkeeper’s templates, and plans the one picked like a file', async () => {
    window.history.replaceState(null, '', '/servers/new')
    const towny = { ...contents, name: 'Towny', minecraftVersion: '26.2', settings: { ...contents.settings, memoryMB: 4096 }, addons: [{ source: 'modrinth', name: 'LuckPerms' }, { source: 'modrinth', name: 'Towny' }] }
    const library: TemplateLibrary = { templates: [{ id: 'towny', art: 'world-big-biomes.svg', page: 'https://playkeeper.io/templates/towny', checked: '2026-09-28', release: '0.4.2', contents: towny, file: '{"name":"Towny"}' }] }
    vi.mocked(client.api).mockResolvedValue({ ...plan, contents: towny })
    answer({ '/catalog': catalog, '/templates/library': library })
    await render(<NewServerPage />)
    await click('A template')
    const text = document.body.textContent ?? ''
    for (const line of ['Playkeeper’s templates', 'Paper 26.2 · 4 GB', 'LuckPerms, Towny', 'Or use a template file']) expect(text).toContain(line)

    const card = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Towny'))
    if (!card) throw new Error('no Towny card')
    await click(card)
    const [method, path, , body] = vi.mocked(client.api).mock.calls.at(-1) ?? []
    expect([method, path]).toEqual(['POST', '/api/machines/m2345abcde/templates/plan'])
    expect(await (body as Blob).text()).toBe('{"name":"Towny"}')
    expect(document.body.textContent).toContain('TownyFrom Playkeeper’s templates')
    expect(document.querySelector('a[href="https://playkeeper.io/templates/towny"]')?.textContent).toBe('Setup guide on playkeeper.io')

    await click('Choose another template')
    expect(document.body.textContent).toContain('Paper 26.2 · 4 GB')
  })

  it('shows each listed template’s own world, and its scene when this dashboard has no picture of it', async () => {
    window.history.replaceState(null, '', '/servers/new')
    const library: TemplateLibrary = {
      templates: [
        { id: 'towny', art: 'world-big-biomes.svg', contents: { ...contents, name: 'Towny' }, file: '{"name":"Towny"}' },
        { id: 'from-a-newer-release', art: 'world-amplified.svg', contents: { ...contents, name: 'Newer' }, file: '{"name":"Newer"}' },
      ],
    }
    answer({ '/catalog': catalog, '/templates/library': library })
    await render(<NewServerPage />)
    await click('A template')
    const picture = (name: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith(name))?.querySelector('img')
    expect(picture('Towny')?.getAttribute('src')).toMatch(/\/template-thumbs\/towny\.webp$/)
    expect(picture('Towny')?.classList.contains('pixelated')).toBe(false)
    expect(picture('Newer')?.getAttribute('src')).toMatch(/\/pixel-art\/world-amplified\.svg$/)
    expect(picture('Newer')?.classList.contains('pixelated')).toBe(true)
  })

  it('shows no template list when the machine’s release carries none', async () => {
    window.history.replaceState(null, '', '/servers/new')
    answer({ '/catalog': catalog, '/templates/library': new Error('Not found.') })
    await render(<NewServerPage />)
    await click('A template')
    const text = document.body.textContent ?? ''
    expect(text).not.toContain('Playkeeper’s templates')
    expect(text).not.toContain('Or use a template file')
    expect(text).toMatch(/Drop a template file here|Choose a template file/)
  })

  it('sizes a mod loader template’s memory for its type and mods', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue({ ...plan, type: 'quilt', versionId: 'quilt-26.1.2', contents: { ...contents, type: 'quilt' } })
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    await click('Continue to memory')
    const text = document.body.textContent ?? ''
    expect(text).toContain('Room for about 4 players')
    expect(text).toContain('Java gets 2.2 GB of it')
  })

  it('says on which day a template was made, and never who made it', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue({ ...plan, contents: { ...contents, author: 'siya', created: '2026-09-25' } })
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    const text = document.body.textContent ?? ''
    expect(text).toMatch(/Survival with friendsFrom a link · made (25 Sep|Sep 25)/)
    expect(text).not.toContain('siya')
  })

  it('shows the new plan when the machine no longer has the one the user saw', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValueOnce(plan).mockResolvedValueOnce({ ...plan, fingerprint: 'fp-2' })
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(409, { code: 'plan_changed', error: 'Playkeeper no longer has that template.', hint: 'Choose it again.' }))
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    await click('Continue to memory')
    await click('Continue to name')
    await toggle('I accept the Minecraft End User License Agreement')
    await click('Create and start Survival with friends')
    expect(vi.mocked(client.api)).toHaveBeenCalledTimes(2)
    const text = document.body.textContent ?? ''
    expect(text).toContain('Playkeeper no longer has that template.')
    expect(text).toContain('Check it again, then continue.')
    expect(text).toContain('Chunky, LuckPerms')
  })

  it('says what a template carries and offers the file when the link is too long', async () => {
    const link = `https://playkeeper.io/t#${'a'.repeat(2376)}`
    const exported: TemplateExport = {
      fileName: 'survival.playkeeper-template',
      file: 'x'.repeat(6000),
      link,
      linkLong: true,
      contents,
      available: { ...contents, settings: { difficulty: 'normal', pvp: false, viewDistance: 10, motd: 'Hi', maxPlayers: 10 } },
      packsHere: 1,
      leftOut: [{ kind: 'left_out_addon_upload', params: { name: 'MyPlugin' }, message: 'MyPlugin was uploaded, so it stays here.' }],
      notes: [],
    }
    answer({ '/template': exported })
    await render(<TemplateDialog server={server()} open onOpenChange={() => {}} />)
    const text = document.body.textContent ?? ''
    for (const line of ['Share Survival as a template', 'Paper 26.1.2 · always included', '2 plugins', 'Left out: MyPlugin', 'Difficulty, PvP, view distance, server list message and 1 more', 'Uploaded packs stay on this server', 'Pin exact versions', 'The same versions Survival runs', 'This link is 2,400 characters, too long for some chats', 'Send the file instead.', 'Download file · 6 KB']) expect(text).toContain(line)
    expect(vi.mocked(client.get)).toHaveBeenLastCalledWith('/api/servers/abcdefghjk/template')

    await toggle('Plugins')
    expect(vi.mocked(client.get)).toHaveBeenLastCalledWith('/api/servers/abcdefghjk/template?addons=off')
    expect(document.body.textContent).not.toContain('Pin exact versions')
  })
})

describe('Add-on sources', () => {
  const none: AddonSources = { curseforge: { key: 'none' } }
  const keyField = () => document.querySelector<HTMLInputElement>('input[aria-label="CurseForge API key"]')
  async function paste(value: string) {
    const input = keyField()
    if (!input) throw new Error('no key field')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)

  it('is the Settings section between Team and Discord, for who manages the machine', async () => {
    answer({ '/addon-sources': none })
    await render(<GlobalSettingsPage page={{ name: 'addon-sources' }} />)
    const nav = document.querySelector('nav[aria-label="Settings sections"]')
    expect([...(nav?.querySelectorAll('a') ?? [])].map((a) => a.textContent)).toEqual(['Team', 'Add-on sources', 'Discord', 'AI agents', 'Machines', 'Playkeeper'])
    expect(nav?.querySelector('[aria-current="page"]')?.getAttribute('href')).toBe('/settings/addon-sources')
    expect(document.getElementById('addon-sources')).not.toBeNull()
    const moderator = await render(<GlobalSettingsPage page={{ name: 'addon-sources' }} />, workspace({ me: member('moderator', moderatorCan) }))
    expect(moderator).not.toContain('CurseForge')
    window.history.replaceState(null, '', '/')
  })

  it('lists the general page as Playkeeper, last in the Settings sections', async () => {
    await render(<GlobalSettingsPage page={{ name: 'settings' }} />)
    const nav = document.querySelector('nav[aria-label="Settings sections"]')
    const current = nav?.querySelector('[aria-current="page"]')
    expect(current?.textContent).toBe('Playkeeper')
    expect(current?.getAttribute('href')).toBe('/settings')
    expect(document.querySelector('h1')?.textContent).toBe('Settings')
  })

  it('lands on its own section, with Modrinth and Hangar built in and CurseForge asking for a key', async () => {
    answer({ '/addon-sources': none })
    const text = await render(<AddonSourcesCard />)
    expect(document.getElementById('addon-sources')).not.toBeNull()
    expect(text).toContain('ModrinthOnBuilt in')
    expect(text).toContain('HangarOnBuilt in')
    expect(text).toContain('CurseForgeNeeds a key')
    for (const step of ['Sign in at console.curseforge.com', 'Open API keys and copy yours', 'Paste it here', 'The key stays on this machine.']) expect(text).toContain(step)
    expect(keyField()?.type).toBe('password')
    expect(button('Save key')?.disabled).toBe(true)
    expect(button('Save key')?.title).toBe('Paste a key first.')
  })

  it('says when CurseForge refuses the key, and keeps nothing', async () => {
    answer({ '/addon-sources': none })
    await render(<AddonSourcesCard />)
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(400, { code: 'curseforge_key_refused', error: "That key didn't work. Copy it again from console.curseforge.com." }))
    await paste('made-up-key-000000000000')
    await act(async () => button('Save key')?.closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await act(async () => {})
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/machines/m2345abcde/addon-sources/curseforge', { key: 'made-up-key-000000000000' })
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('That key didn’t work. Copy it again from console.curseforge.com.')
    expect(keyField()?.getAttribute('aria-invalid')).toBe('true')
    expect(document.body.textContent).not.toContain('Key ending')
  })

  it('shows a saved key by its ending, with Replace key and Remove', async () => {
    answer({ '/addon-sources': none })
    await render(<AddonSourcesCard />)
    vi.mocked(client.post).mockResolvedValueOnce({ curseforge: { key: 'file', ending: '3f9a' } })
    await paste(' pasted-key-0123456789abc3f9a ')
    await act(async () => button('Save key')?.closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await act(async () => {})
    const text = document.body.textContent ?? ''
    expect(text).toContain('CurseForgeOnKey ending 3f9a')
    expect(text).not.toContain('pasted-key')
    expect(button('Replace key')).toBeDefined()
    expect(button('Remove')).toBeDefined()
    expect(keyField()).toBeNull()
    await click('Replace key')
    expect(keyField()).not.toBeNull()
    expect(button('Cancel')).toBeDefined()
  })

  it('shows a key the release carries as on and built in, with nothing to change', async () => {
    answer({ '/addon-sources': { curseforge: { key: 'build' } } })
    const text = await render(<AddonSourcesCard />)
    expect(text).toContain('CurseForgeOnBuilt in')
    expect(keyField()).toBeNull()
    expect(button('Remove')).toBeUndefined()
  })

  it('lets only the owner change the key', async () => {
    answer({ '/addon-sources': { curseforge: { key: 'file', ending: '3f9a' } } })
    await render(<AddonSourcesCard />, workspace({ me: { ...me, user: { username: 'friend', role: 'member' } } }))
    for (const label of ['Replace key', 'Remove']) {
      expect(button(label)?.disabled).toBe(true)
      expect(button(label)?.title).toBe('Only the owner can change this.')
    }
  })

  // Each machine keeps its own key, so the card names the one it shows and saves the key there.
  const home: MachineView = {
    id: 'h2345abcde',
    projectId: machine.projectId,
    name: 'home-server',
    kind: 'remote',
    link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
  }
  it.each([
    { name: 'the dashboard’s machine when none is chosen', machine: undefined, want: machine.id, label: 'my-vps' },
    { name: 'the joined machine New server linked to', machine: home.id, want: home.id, label: 'home-server' },
  ])('shows and saves the key of $name', async ({ machine: chosen, want, label }) => {
    answer({ '/addon-sources': none })
    vi.mocked(client.get).mockClear()
    const text = await render(<GlobalSettingsPage page={{ name: 'addon-sources', machine: chosen }} />, workspace({ machines: [machine, home] }))
    expect(vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes('/addon-sources'))).toEqual([`/api/machines/${want}/addon-sources`])
    expect(text).toContain(`The key stays on ${label}.`)
    vi.mocked(client.post).mockResolvedValueOnce({ curseforge: { key: 'file', ending: 'c3f9' } })
    await paste('pasted-key-0123456789abc3f9a')
    await act(async () => button('Save key')?.closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(vi.mocked(client.post)).toHaveBeenCalledWith(`/api/machines/${want}/addon-sources/curseforge`, { key: 'pasted-key-0123456789abc3f9a' })
    expect(document.body.textContent).toContain('Key ending c3f9')
    await render(<GlobalSettingsPage page={{ name: 'addon-sources', machine: chosen }} />, workspace({ machines: [machine, home], me: { ...me, user: { username: 'friend', role: 'member' } } }))
    expect(button('Save key')?.disabled).toBe(true)
    expect(button('Save key')?.title).toBe('Only the owner can change this.')
  })

  it('says so when the chosen machine isn’t connected to this dashboard, and asks no other', async () => {
    vi.mocked(client.get).mockClear()
    const text = await render(<GlobalSettingsPage page={{ name: 'addon-sources', machine: 'z2345abcde' }} />, workspace({ machines: [machine, home] }))
    expect(text).toContain('That machine isn’t connected to this dashboard')
    expect(keyField()).toBeNull()
    expect(vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes('/addon-sources'))).toEqual([])
  })
})

describe('Sell on Whop', () => {
  const needs = ['access_pass:basic:read', 'plan:basic:read', 'support_chat:create']
  const closed: WhopStore = { connected: false, dashboard: 'https://my-vps.playkeeper.me:8443', plans: [], webhook: false, customers: [], needs }
  const starter: WhopPlan = { id: 'plan_starter', productId: 'prod_mc', productTitle: 'Minecraft server', title: 'Starter', price: '$8.00 / month', visibility: 'hidden', trialDays: 3, allowance: { servers: 1, memoryMB: 4096 }, allowanceFrom: 'store' }
  const big: WhopPlan = { id: 'plan_big', productId: 'prod_mc', productTitle: 'Minecraft server', title: 'Big', price: '$16.00 / month', visibility: 'visible' }
  const open: WhopStore = {
    ...closed,
    connected: true,
    account: { id: 'biz_pip', title: 'Pip Hosting', route: 'pip-hosting' },
    keyEnding: 'abcd',
    connectedBy: 'siya',
    syncedAt: hoursAgo(1),
    plans: [starter, big],
    webhook: true,
    customers: [
      { whopUserId: 'user_alex', handle: 'alexplays', status: 'active', plan: 'Starter', account: 'alexplays', allowance: { servers: 1, memoryMB: 4096 } },
      { whopUserId: 'user_sam', handle: 'samcrafts', status: 'paused', plan: 'Old plan', allowance: { servers: 2, memoryMB: 8192 } },
      { whopUserId: 'user_kai', status: 'starting', plan: 'Starter', allowance: { servers: 1, memoryMB: 4096 }, problem: 'This dashboard can’t host customers yet.' },
      { whopUserId: 'user_lee', handle: 'leebuilds', status: 'ended' },
    ],
  }
  const owner = workspace({ me: { ...me, access: { ...me.access, can: [...everything, 'whop.manage'] } } })
  const keyField = () => document.querySelector<HTMLInputElement>('input[aria-label="Whop API key"]')
  const submitKey = async () => act(async () => button('Connect').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))

  it('is a Settings section for the owner alone, after Discord', async () => {
    answer({ '/api/whop': closed })
    await render(<GlobalSettingsPage page={{ name: 'whop' }} />, owner)
    const nav = document.querySelector('nav[aria-label="Settings sections"]')
    expect([...(nav?.querySelectorAll('a') ?? [])].map((a) => a.textContent)).toEqual(['Team', 'Add-on sources', 'Discord', 'Sell on Whop', 'AI agents', 'Machines', 'Playkeeper'])
    expect(nav?.querySelector('[aria-current="page"]')?.getAttribute('href')).toBe('/settings/whop')
    const admin = await render(<GlobalSettingsPage page={{ name: 'discord' }} />)
    expect(admin).not.toContain('Sell on Whop')
    window.history.replaceState(null, '', '/')
  })

  it('lists the steps and the permissions a key needs, and asks for the key', async () => {
    answer({ '/api/whop': closed })
    const text = await render(<SellOnWhopSection />, owner)
    for (const step of ['deploy the Playkeeper Hosting blueprint', 'Make an API key on Whop', 'Paste the key here.', 'access_pass:basic:read, plan:basic:read, support_chat:create', 'only ever sent to Whop']) expect(text).toContain(step)
    expect(keyField()?.type).toBe('password')
    expect(button('Connect').disabled).toBe(true)
    expect(text).not.toContain('needs an address')
  })

  it('turns on Sign in with Whop with the app’s ID, then off again', async () => {
    const redirect = 'https://my-vps.playkeeper.me:8443/api/public/whop/signin/callback'
    answer({ '/api/whop': { ...open, signIn: { redirectUri: redirect } } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain(`Redirect URL: ${redirect}`)
    expect(text).toContain('add the oauth:token_exchange permission on the app’s own Permissions tab (not on an API key), then paste the app’s ID and client secret here.')
    expect(document.querySelector<HTMLInputElement>('input[aria-label="App’s client secret"]')?.type).toBe('password')
    expect(button('Turn on').disabled).toBe(true)
    await typeInto('input[aria-label="Whop app ID"]', ' app_pipcloud ')
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, signIn: { clientId: 'app_pipcloud', secretEnding: 'wxyz', redirectUri: redirect } })
    await act(async () => button('Turn on').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/whop/signin', { clientId: 'app_pipcloud', clientSecret: '' })
    expect(document.body.textContent).toContain('Customers sign in with their Whop account, through app_pipcloud. Its secret ends wxyz.')
    vi.mocked(client.del).mockResolvedValueOnce({ ...open, signIn: { redirectUri: redirect } })
    await click('Turn off')
    expect(vi.mocked(client.del)).toHaveBeenLastCalledWith('/api/whop/signin')
    expect(document.body.textContent).toContain(`Redirect URL: ${redirect}`)
  })

  it('says when customers still come back through the dashboard’s old address', async () => {
    const redirect = 'https://beta.playkeeper.me/api/public/whop/signin/callback'
    const using = 'https://beta.playkeeper.me:8443/api/public/whop/signin/callback'
    answer({ '/api/whop': { ...open, signIn: { clientId: 'app_pipcloud', redirectUri: redirect, using } } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain(`Whop still sends customers back through ${using}. To move them, add this redirect URL on the app’s OAuth tab: ${redirect}`)
    answer({ '/api/whop': { ...open, signIn: { clientId: 'app_pipcloud', redirectUri: redirect } } })
    expect(await render(<SellOnWhopSection />, owner)).not.toContain('still sends customers back')
  })

  it('lists the businesses selling through the app, and lets the owner suspend one with a reason or lift it', async () => {
    const stores: StoresResponse = {
      stores: [
        { id: 'biz_other', title: 'Other Hosting', customers: 2 },
        { id: 'biz_gone', title: 'Gone Hosting', customers: 1, suspendedAt: hoursAgo(2), suspendReason: 'selling to cheaters' },
        { id: 'biz_left', title: 'Left Hosting', customers: 3, leftAt: hoursAgo(5), leftWhy: 'Playkeeper’s share has been gone for 72 hours' },
        { id: 'biz_new', title: 'New Hosting', customers: 0, closedWhy: 'Not open yet' },
      ],
    }
    answer({ '/api/whop/stores': stores, '/api/whop': open })
    answerPosts({})
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Businesses selling through your app')
    expect(text).toContain('Other Hosting2 customers · selling')
    expect(text).toContain('Gone Hosting1 customer · suspended: selling to cheaters')
    expect(text).toContain('Left Hosting3 customers · left: Playkeeper’s share has been gone for 72 hours')
    expect(text).toContain('New Hosting0 customers · Not open yet')
    expect(buttons('Suspend')).toHaveLength(2)
    await click('Suspend')
    expect(page()).toContain('Suspend Other Hosting?')
    expect(button('Suspend Other Hosting').disabled).toBe(true)
    await typeInto('input[placeholder="Why, for the audit log"]', ' selling to cheaters ')
    await act(async () => button('Suspend Other Hosting').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    // internal/panel's TestSuspendingAStoreSuspendsItsOwnCustomersAlone posts this body.
    expect(client.post).toHaveBeenCalledWith('/api/whop/stores/biz_other/suspension', { reason: 'selling to cheaters' })
    await click('Lift suspension')
    expect(page()).toContain('Lift Gone Hosting’s suspension?')
    await act(async () => [...document.querySelectorAll('[role="dialog"] form')].at(-1)?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(client.del).toHaveBeenCalledWith('/api/whop/stores/biz_gone/suspension')
  })

  it('lets the owner choose how long after their servers a customer is deleted, from the days their final backups are kept', async () => {
    answer({ '/api/customers/retention': { days: 45, default: 30, min: 30, max: 3650 }, '/api/whop': open })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Deleting customers')
    expect(text).toContain('Their payments stay for the store’s accounts, without who paid.')
    expect(text).toContain('At least 30 days, while their final backups can still be downloaded. A shorter time deletes anyone already past it at once.')
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="Delete their account"]')?.click())
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    expect(options.map((o) => o.textContent)).toEqual(['30 days after their servers', '45 days after their servers', '60 days after their servers', '90 days after their servers', '180 days after their servers', '365 days after their servers', '730 days after their servers'])
    await act(async () => options.find((o) => o.textContent === '90 days after their servers')?.click())
    // internal/panel's TestACustomerIsDeletedSomeDaysAfterTheirServers puts this body.
    expect(client.put).toHaveBeenCalledWith('/api/customers/retention', { days: 90 })
  })

  it('saves the app’s key and webhook secret, so businesses that install the app sell here', async () => {
    const redirect = 'https://my-vps.playkeeper.me:8443/api/public/whop/signin/callback'
    const webhookUrl = 'https://my-vps.playkeeper.me:8443/api/public/whop/app-webhook'
    const signIn = { clientId: 'app_pipcloud', secretEnding: 'wxyz', redirectUri: redirect }
    answer({ '/api/whop': { ...open, signIn, app: { stores: 0, webhook: false, webhookUrl } } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Other businesses on Whop can sell servers from this dashboard by installing app_pipcloud, the app customers sign in through.')
    expect(text).toContain('The app’s API key isn’t set, so businesses that install it wait.No business that installed it sells here yet.Without the app’s webhook, their purchases are read every minute.')
    expect(text).toContain(`membership.cancel_at_period_end_changed, payment.succeeded, refund.created and refund.updated, then paste its secret here. Its URL: ${webhookUrl}`)
    for (const label of ['App’s API key', 'App webhook’s secret']) expect(document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)?.type).toBe('password')
    expect(button('Save').disabled).toBe(true)
    await typeInto('input[aria-label="App’s API key"]', ' apik_cloud_0123456789abcdef ')
    await typeInto('input[aria-label="App webhook’s secret"]', 'ws_0123456789abcdef0123')
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, signIn, app: { keyEnding: 'cdef', stores: 2, webhook: true, webhookUrl } })
    await act(async () => button('Save').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/whop/app', { key: 'apik_cloud_0123456789abcdef', webhookSecret: 'ws_0123456789abcdef0123' })
    const after = document.body.textContent ?? ''
    expect(after).toContain('The app’s API key ends cdef.2 businesses that installed it sell here.Whop tells this dashboard of their purchases through the app’s webhook.')
    expect(after).not.toContain('Its URL:')
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, signIn, app: { stores: 2, webhook: false, webhookUrl } })
    await click('Remove the key and webhook secret')
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/whop/app', { key: '', webhookSecret: '' })
  })

  it('names the Whop account that receives Playkeeper’s share of their sales', async () => {
    const signIn = { clientId: 'app_pipcloud', secretEnding: 'wxyz', redirectUri: 'https://my-vps.playkeeper.me:8443/api/public/whop/signin/callback' }
    answer({ '/api/whop': { ...open, signIn, app: { keyEnding: 'cdef', stores: 0, webhook: true } } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Nobody receives Playkeeper’s share of their sales yet, so they can’t open their stores.')
    expect(button('Set').disabled).toBe(true)
    await typeInto('input[aria-label="Whop username that receives Playkeeper’s share"]', ' @ ')
    expect(button('Set').disabled).toBe(true)
    await typeInto('input[aria-label="Whop username that receives Playkeeper’s share"]', ' @siyabuilt ')
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, signIn, app: { keyEnding: 'cdef', stores: 0, webhook: true, shareUser: 'siyabuilt' } })
    await act(async () => button('Set').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/whop/app', { shareUser: '@siyabuilt' })
    expect(document.body.textContent).toContain('Playkeeper’s share of their sales goes to @siyabuilt.')
  })

  it('sets how many days a renewal may be late before its customer’s servers stop', async () => {
    const signIn = { clientId: 'app_pipcloud', secretEnding: 'wxyz', redirectUri: 'https://my-vps.playkeeper.me:8443/api/public/whop/signin/callback' }
    const app = { keyEnding: 'cdef', stores: 1, webhook: true, shareUser: 'siyabuilt' }
    answer({ '/api/whop': { ...open, signIn, app: { ...app, renewalGraceDays: 7 } } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('A payment pays for 30 days of servers. When no renewal that carried Playkeeper’s share comes by then, the customer’s servers stop this many days later')
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="A renewal may be late by"]')?.click())
    const options = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
    expect(options.map((o) => o.textContent)).toEqual(['0 days', '1 day', '2 days', '3 days', '5 days', '7 days', '10 days', '14 days', '21 days', '30 days'])
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, signIn, app: { ...app, renewalGraceDays: 2 } })
    await act(async () => options.find((o) => o.textContent === '2 days')?.click())
    // internal/panel's TestTheRenewalGraceIsTheOwnersToSet puts this body.
    expect(client.put).toHaveBeenLastCalledWith('/api/whop/app', { renewalGraceDays: 2 })
  })

  it('shows the businesses that install the app only once Sign in with Whop is on', async () => {
    answer({ '/api/whop': { ...open, signIn: { redirectUri: 'https://my-vps.playkeeper.me:8443/api/public/whop/signin/callback' }, app: { stores: 0, webhook: false } } })
    expect(await render(<SellOnWhopSection />, owner)).not.toContain('Businesses that install your app')
  })

  it('says why a message to a customer hasn’t gone out, as the store needing a look', async () => {
    answer({ '/api/whop': { ...open, customers: [{ ...open.customers[0], messageProblem: 'Whop said: Something went wrong' }] } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Pip HostingNeeds a look')
    expect(text).toContain('alexplaysActive as alexplays · Starter · Up to 1 server with 4 GBTheir messages on Whop aren’t going out. Whop said: Something went wrong')
  })

  it('says Sign in with Whop needs the machine’s address', async () => {
    answer({ '/api/whop': { ...open, signIn: {} } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Give this machine an address first, in Machine settings › Address.')
  })

  it('says the machine needs an address first, with a way to set one', async () => {
    answer({ '/api/whop': { ...closed, dashboard: '' } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('This machine needs an address first')
    expect(link('Set one up').getAttribute('href')).toBe(`/machines/${machine.id}/settings`)
  })

  it('says why Whop refused a key, and which permissions a key lacks', async () => {
    answer({ '/api/whop': closed })
    await render(<SellOnWhopSection />, owner)
    await typeInto('input[aria-label="Whop API key"]', ' apik_wrong_0123456789 ')
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(400, { code: 'whop_key_refused', error: 'Whop didn’t take that key.' }))
    await submitKey()
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/whop/connect', { key: 'apik_wrong_0123456789' })
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('Whop didn’t take that key')
    expect(keyField()?.getAttribute('aria-invalid')).toBe('true')
    await typeInto('input[aria-label="Whop API key"]', 'apik_partial_0123456789')
    vi.mocked(client.post).mockRejectedValueOnce(new client.ApiError(400, { code: 'whop_permissions', error: 'That key can’t do everything selling needs.', params: { missing: 'support_chat:create,developer:manage_webhook' } }))
    await submitKey()
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('That key lacks support_chat:create, developer:manage_webhook. Make a new key with those too.')
  })

  it('offers to take over a store another dashboard sells for', async () => {
    answer({ '/api/whop': closed })
    await render(<SellOnWhopSection />, owner)
    await typeInto('input[aria-label="Whop API key"]', 'apik_pip_hosting_0123456789abcd')
    vi.mocked(client.post).mockRejectedValueOnce(
      new client.ApiError(409, { code: 'whop_other_seller', error: 'Another Playkeeper sells for Pip Hosting.', params: { dashboard: 'https://beta.playkeeper.me:8443' } }),
    )
    await submitKey()
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('Another Playkeeper sells for this store, at https://beta.playkeeper.me:8443.')
    vi.mocked(client.post).mockResolvedValueOnce(open)
    await click('Take it over')
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/whop/connect', { key: 'apik_pip_hosting_0123456789abcd', takeOver: true })
    expect(page()).toContain('Pip HostingSelling')
  })

  it('says another dashboard took the store over, and takes it back', async () => {
    answer({ '/api/whop': { ...open, takenOverBy: 'https://other.playkeeper.me:8443', takenOverAt: hoursAgo(2) } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Pip HostingNeeds a look')
    expect(text).toContain('Another Playkeeper, at https://other.playkeeper.me:8443, took this store over 2 h ago, so this dashboard stopped selling.')
    expect(text).not.toContain('The store sends buyers to')
    vi.mocked(client.post).mockResolvedValueOnce(open)
    await click('Take it back')
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/whop/sync', { takeOver: true })
    expect(page()).toContain('Pip HostingSelling')
  })

  it('says the other dashboard still sells while taking the store over isn’t done', async () => {
    const pending = { ...open, takenOverBy: 'https://beta.playkeeper.me:8443', problem: 'Whop said: Try again' }
    answer({ '/api/whop': pending })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('Another Playkeeper, at https://beta.playkeeper.me:8443, still sells for this store, so this dashboard doesn’t yet.')
    expect(text).toContain('Whop said: Try again')
    const toast = vi.spyOn(toastManager, 'add')
    vi.mocked(client.post).mockResolvedValueOnce(pending)
    await click('Take it over')
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/whop/sync', { takeOver: true })
    expect(toast).toHaveBeenLastCalledWith({ title: 'The store still sells from https://beta.playkeeper.me:8443.', type: 'error' })
    vi.mocked(client.post).mockResolvedValueOnce(open)
    await click('Take it over')
    expect(toast).toHaveBeenLastCalledWith({ title: 'Selling from this dashboard now', type: 'success' })
    expect(page()).toContain('Pip HostingSelling')
    toast.mockRestore()
  })

  it('shows the connected store with its plans and what each allows', async () => {
    answer({ '/api/whop': closed })
    await render(<SellOnWhopSection />, owner)
    await typeInto('input[aria-label="Whop API key"]', 'apik_pip_hosting_0123456789abcd')
    vi.mocked(client.post).mockResolvedValueOnce(open)
    await submitKey()
    const text = page()
    expect(text).toContain('Pip HostingSelling')
    expect(text).toContain('Key ending abcd')
    expect(text).toContain('The store sends buyers to https://my-vps.playkeeper.me:8443.')
    expect(text).toContain('StarterHidden$8.00 / month · 3-day free trial · Up to 1 server with 4 GB · set on Whop')
    expect(text).toContain('BigVisible$16.00 / month · No allowance yet')
    expect(buttons('Set allowance')).toHaveLength(1)
    expect(keyField()).toBeNull()
    expect(text).toContain('alexplaysActive as alexplays · Starter · Up to 1 server with 4 GB')
    expect(text).toContain('samcraftsPaused: their plan ended · Old plan · Up to 2 servers with 8 GB')
    expect(text).toContain('user_kaiSetting up their account · Starter · Up to 1 server with 4 GBThis dashboard can’t host customers yet.')
    expect(text).toContain('leebuildsTheir plan ended')
    expect(text).not.toContain('so it checks every minute')
  })

  it('says when Whop can’t tell the dashboard about customers as they buy', async () => {
    answer({ '/api/whop': { ...open, webhook: false, customers: [] } })
    const text = await render(<SellOnWhopSection />, owner)
    expect(text).toContain('so it checks every minute')
    expect(text).toContain('No customers yet.')
  })

  it('sets what a plan without metadata allows', async () => {
    answer({ '/api/whop': open })
    await render(<SellOnWhopSection />, owner)
    await click('Set allowance')
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('What Big allows')
    vi.mocked(client.put).mockResolvedValueOnce({ ...open, plans: [starter, { ...big, allowance: { servers: 1, memoryMB: 4096 }, allowanceFrom: 'owner' }] })
    await act(async () => button('Save').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await act(async () => {})
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/whop/plans/plan_big', { servers: 1, memoryMB: 4096 })
    expect(page()).toContain('BigVisible$16.00 / month · Up to 1 server with 4 GB')
    expect(buttons('Change')).toHaveLength(1)
  })

  it('reads the store again, and disconnects after asking', async () => {
    answer({ '/api/whop': open })
    await render(<SellOnWhopSection />, owner)
    answerPosts({ '/api/whop/sync': { ...open, plans: [starter] } })
    await click('Read the store again')
    expect(page()).not.toContain('Big')
    await click('Disconnect')
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Stop selling for Pip Hosting?')
    vi.mocked(client.del).mockResolvedValueOnce(closed)
    const confirm = buttons('Disconnect').find((b) => b.closest('[role="dialog"]'))
    if (!confirm) throw new Error('no Disconnect in the dialog')
    await click(confirm)
    expect(vi.mocked(client.del)).toHaveBeenLastCalledWith('/api/whop')
    expect(keyField()).not.toBeNull()
  })
})

describe('Hetzner stock', () => {
  const off: HetznerStock = { connected: false, serverType: 'cx53', types: ['cx23', 'cx33', 'cx43', 'cx53'], places: [], discord: true }
  const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
  const fsnBuy = 'https://console.hetzner.com/create/server?location=fsn1&type=cx53&useIPv4=true'
  /** Watching CX53 with Falkenstein in stock, its times counted back from the test's clock when it's called. */
  const watching = (): HetznerStock => ({
    ...off,
    connected: true,
    tokenEnding: 'WXyz',
    setBy: 'siya',
    setAt: hoursAgo(2),
    checkedAt: minutesAgo(1),
    places: [
      { location: 'fsn1', city: 'Falkenstein', available: true, since: minutesAgo(4), buyUrl: fsnBuy },
      { location: 'nbg1', city: 'Nuremberg', available: false, since: minutesAgo(90) },
      { location: 'hel1', city: 'Helsinki', available: false },
    ],
  })
  const owner = workspace({ me: { ...me, access: { ...me.access, can: [...everything, 'machines.stock'] } } })
  const tokenField = () => document.querySelector<HTMLInputElement>('input[aria-label="Hetzner API token"]')
  const submit = async (label: string) => act(async () => button(label).closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  const machineLink = { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5, systems: [{ name: 'Ubuntu', version: '20.04' }] }, sizingUrl: '', available: false, codes: [] }

  it('is on the Machines page for the owner alone', async () => {
    answer({ '/api/machines/link': machineLink, '/api/hetzner': off })
    expect(await render(<MachinesSection />, owner)).toContain('Hetzner stock')
    expect(await render(<MachinesSection />)).not.toContain('Hetzner stock')
  })

  it('asks for a read-only token, and says when Hetzner refuses one', async () => {
    answer({ '/api/hetzner': off })
    const text = await render(<HetznerStockCard />, owner)
    expect(text).toContain('Hear in Discord when Hetzner has the machines you add here, with a link that buys one.')
    expect(text).toContain('open Security › API tokens and generate a token with Read permission.')
    expect(link('Open Hetzner Console').getAttribute('href')).toBe('https://console.hetzner.com/projects')
    expect(text).toContain('The token stays on this machine and only reads.')
    expect(tokenField()?.type).toBe('password')
    expect(button('Watch').disabled).toBe(true)
    await typeInto('input[aria-label="Hetzner API token"]', ' not-a-token ')
    vi.mocked(client.put).mockRejectedValueOnce(new client.ApiError(400, { code: 'hetzner_token_refused', error: 'That doesn’t look like a Hetzner API token.' }))
    await submit('Watch')
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/hetzner', { token: 'not-a-token', serverType: 'cx53' })
    expect(document.querySelector('[role="alert"]')?.textContent).toBe('Hetzner didn’t take that token. Copy it again, or make a new one with Read permission.')
    expect(tokenField()?.getAttribute('aria-invalid')).toBe('true')
  })

  it('shows where the type is in stock, with a link that buys one there', async () => {
    // "checked 1 min ago" and "since Today 14:10" read the clock, so it stands still: a slow run or midnight changes neither.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 8, 29, 14, 14, 26))
    try {
      answer({ '/api/hetzner': off })
      await render(<HetznerStockCard />, owner)
      await typeInto('input[aria-label="Hetzner API token"]', 'a-token')
      const on = watching()
      vi.mocked(client.put).mockResolvedValueOnce(on)
      await submit('Watch')
      const text = page()
      expect(text).toContain('CX53In stock')
      expect(text).toContain('Token ending WXyz · checked 1 min ago')
      expect(text).toContain(`FalkensteinIn stock since Today ${formatClock(on.places[0]?.since ?? '')}`)
      expect(text).toContain('NurembergSold out')
      expect(text).toContain('HelsinkiSold out')
      const buy = [...document.querySelectorAll('a')].filter((a) => a.textContent === 'Buy one')
      expect(buy.map((a) => [a.getAttribute('href'), a.getAttribute('target')])).toEqual([[fsnBuy, '_blank']])
      expect(text).not.toContain('Connect Discord')
      expect(tokenField()).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('says a problem needs a look, and that Discord isn’t connected', async () => {
    answer({ '/api/hetzner': { ...watching(), discord: false, problem: 'Hetzner no longer takes the token, so the watch stopped. Paste a new read-only token.' } })
    const text = await render(<HetznerStockCard />, owner)
    expect(text).toContain('CX53Needs a look')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('no longer takes the token')
    expect(text).toContain('Alerts go to Discord, which isn’t connected.')
    expect(link('Connect Discord').getAttribute('href')).toBe('/settings/discord')
  })

  it('watches another type with the token kept, and stops watching', async () => {
    const on = watching()
    answer({ '/api/hetzner': on })
    await render(<HetznerStockCard />, owner)
    await click('Change')
    expect(tokenField()?.placeholder).toBe('Keep the token ending WXyz')
    expect(button('Save').disabled).toBe(true)
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="Server type"]')?.click())
    const cx43 = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent === 'CX43')
    await act(async () => cx43?.click())
    expect(button('Save').disabled).toBe(false)
    vi.mocked(client.put).mockResolvedValueOnce({ ...on, serverType: 'cx43' })
    await submit('Save')
    expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/hetzner', { token: '', serverType: 'cx43' })
    expect(page()).toContain('CX43In stock')
    vi.mocked(client.del).mockResolvedValueOnce(off)
    await click('Stop watching')
    expect(vi.mocked(client.del)).toHaveBeenLastCalledWith('/api/hetzner')
    expect(tokenField()).not.toBeNull()
  })
})

describe('Room for customers', () => {
  const owner = workspace({ me: { ...me, access: { ...me.access, can: [...everything, 'machines.customers'] } } })
  const home: MachineView = { id: 'h2345abcde', projectId: machine.projectId, name: 'home-server', kind: 'remote' }
  const pip = { store: 'biz_pip', storeName: 'Pip Hosting' }
  const room: SaleRoom = {
    plans: [
      { id: 'plan_starter', name: 'Starter', ...pip, memoryMB: 4096, free: false, left: 3 },
      { id: 'plan_big', name: 'Big', ...pip, memoryMB: 8192, free: false, left: 0 },
      { id: 'plan_creator', name: 'Creator', ...pip, memoryMB: 4096, free: true, left: 1 },
    ],
    machines: [
      { id: machine.id, freeMB: 20480, takes: true },
      { id: home.id, freeMB: 30000, takes: false, why: 'Joined machines take customers once you confirm them.' },
    ],
  }
  const machineLink = { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5, systems: [{ name: 'Ubuntu', version: '20.04' }] }, sizingUrl: '', available: false, codes: [] }

  it('is on the Machines page for the owner alone, and only while plans are on sale', async () => {
    answer({ '/api/machines/link': machineLink, '/api/machines/room': room })
    expect(await render(<MachinesSection />, owner)).toContain('Room for customers')
    expect(await render(<MachinesSection />)).not.toContain('Room for customers')
    answer({ '/api/machines/link': machineLink, '/api/machines/room': { plans: [], machines: room.machines } })
    expect(await render(<MachinesSection />, owner)).not.toContain('Room for customers')
  })

  it('says how many more of each plan fit, and what each machine can still set aside', async () => {
    answer({ '/api/machines/room': room })
    await render(<SaleRoomCard />, { ...owner, machines: [machine, home] })
    const rows = [...document.querySelectorAll('li')].map((li) => li.textContent)
    expect(rows).toEqual(['Starter 4 GB3 more', 'Big 8 GBSold out', 'Creator 4 GB · free1 more', 'my-vps20 GB free for customers', 'home-serverJoined machines take customers once you confirm them.'])
    expect(document.body.textContent).not.toContain('Pip Hosting')
  })

  it('puts each plan under its store when stores share the machines, and says how they share', async () => {
    const other = { store: 'biz_other', storeName: 'Other Hosting' }
    answer({ '/api/machines/room': { ...room, plans: [...room.plans, { id: 'plan_other', name: 'Other', ...other, memoryMB: 4096, free: false, left: 4 }] } })
    const text = await render(<SaleRoomCard />, { ...owner, machines: [machine, home] })
    const stores = [...document.querySelectorAll('[data-store]')].map((group) => [group.querySelector('h3')?.textContent, [...group.querySelectorAll('li')].map((li) => li.textContent)])
    expect(stores).toEqual([
      ['Pip Hosting', ['Starter 4 GB3 more', 'Big 8 GBSold out', 'Creator 4 GB · free1 more']],
      ['Other Hosting', ['Other 4 GB4 more']],
    ])
    expect(text).toContain('Each store gets an even share of the room, and each plan is offered once while there’s room for it.')
  })
})

describe('Sidebar', () => {
  it('stops saying Creating once a create failed, like the server’s page', async () => {
    const failedCreate = failed('create', 'downloading_server', 'The server stopped while starting (exit code 1).')
    const text = await render(<AppShell route={{ name: 'home' }}><p>page</p></AppShell>, workspace({ servers: [server({ name: 'Modpack check', phase: 'stopped', startedAt: undefined, lastOperation: failedCreate })] }))
    const row = [...document.querySelectorAll('aside a')].find((a) => a.textContent?.includes('Modpack check'))
    expect(row?.textContent).toContain('Stopped')
    expect(text).not.toContain('Creating')
    expect(row?.querySelector('[data-slot="spinner"], .animate-spin')).toBeNull()
  })

  it('says a server being moved is being moved, whatever it last said', async () => {
    await render(<AppShell route={{ name: 'home' }}><p>page</p></AppShell>, workspace({ servers: [server({ name: 'Moving one', phase: 'online', moving: true })] }))
    const row = [...document.querySelectorAll('aside a')].find((a) => a.textContent?.includes('Moving one'))
    expect(row?.textContent).toContain('Being moved')
    expect(row?.textContent).not.toContain('Online')
  })
})

describe('Modpacks', () => {
  const results: ModpackResults = {
    cards: [{ source: 'modrinth', projectId: 'SMTH0001', slug: 'smoothserver', name: 'Smooth Server', summary: 'Runs smoothly.', downloads: 1200, updated: '2026-09-01T00:00:00Z', pageUrl: 'https://modrinth.com/modpack/smoothserver', types: ['fabric'], minecraftVersions: ['26.2', '26.1'], mods: 17, memoryMB: 4096 }],
    total: 1,
    offset: 0,
    limit: 12,
    sources: ['modrinth'],
  }

  it('searches when Enter is pressed, and only then or after typing stops', async () => {
    vi.useFakeTimers()
    try {
      answer({ '/modpacks?': results })
      await render(<ModpackPicker machineId="m2345abcde" onChange={() => {}} onUse={() => {}} phone={false} />)
      const form = document.querySelector('form')
      const input = form?.querySelector('input')
      if (!form || !input) throw new Error('no search form')
      expect(form.querySelectorAll('input')).toHaveLength(1)
      const searched = () => vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes('q=simply+optimized')).length
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'simply optimized')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
      expect(searched()).toBe(0)
      await act(async () => {
        form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      })
      expect(searched()).toBe(1)
      await act(async () => {
        input.dispatchEvent(new FocusEvent('blur'))
        vi.advanceTimersByTime(500)
      })
      expect(searched()).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('says why a pack can’t be used, on the button and above it', async () => {
    const card = results.cards[0]
    if (!card) throw new Error('no card')
    const detail: ModpackDetail = { ...card, versions: [{ id: 'SMV00012', number: '1.2', channel: 'release', published: '2026-09-01T00:00:00Z', size: 16_000_000, type: 'fabric', minecraftVersion: '26.2', mods: 17 }], newest: 'SMV00012' }
    const blocker = { kind: 'file_too_large', message: 'Smooth Server has a file larger than Playkeeper accepts.' }
    answer({ '/preview': { type: 'fabric', minecraftVersion: '26.2', loaderVersion: '0.19.3', files: 17, downloadSize: 16_000_000, ready: false, blockers: [blocker], warnings: [], manual: [] }, '/modpacks/modrinth/SMTH0001': detail, '/modpacks?': results })
    await render(<ModpackPicker machineId="m2345abcde" onChange={() => {}} onUse={() => {}} phone={false} />)
    const row = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Smooth Server'))
    await act(async () => row?.click())
    await act(async () => {})
    await act(async () => {})
    const use = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Use this modpack')
    expect(use?.disabled).toBe(true)
    expect(use?.title).toBe(blocker.message)
    expect(document.body.textContent).toContain('Playkeeper can’t set up this pack')
  })

  /** Opens the details of a pack whose plan has these ports, on a machine of its own: modpack answers are kept for a few minutes. */
  async function openPackPlan(projectId: string, ports?: { protocol: 'udp'; port: number }[]): Promise<string> {
    const plan = { type: 'fabric', minecraftVersion: '26.2', loaderVersion: '0.19.3', files: 17, downloadSize: 16_000_000, ready: true, blockers: [], warnings: [], manual: [], ports }
    const card = { ...results.cards[0], projectId, name: `Voice Pack ${projectId}` } as ModpackCard
    const detail: ModpackDetail = { ...card, versions: [{ id: `${projectId}V`, number: '1.2', channel: 'release', published: '2026-09-01T00:00:00Z', size: 16_000_000, type: 'fabric', minecraftVersion: '26.2', mods: 17 }], newest: `${projectId}V` }
    answer({ '/preview': plan, [`/modpacks/modrinth/${projectId}`]: detail, '/modpacks?': { ...results, cards: [card] } })
    await render(<ModpackPicker machineId={`m${projectId.toLowerCase()}`} onChange={() => {}} onUse={() => {}} phone={false} />)
    const row = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith(card.name))
    await act(async () => row?.click())
    for (let i = 0; i < 4; i++) await act(async () => {})
    expect([...document.querySelectorAll('button')].find((b) => b.textContent === 'Use this modpack')?.disabled).toBe(false)
    return document.body.textContent ?? ''
  }

  it('names the UDP port voice chat needs in the pack’s plan', async () => {
    const text = await openPackPlan('VOIC0001', [{ protocol: 'udp', port: 24455 }])
    expect(text).toContain('Voice travels on its own port')
    expect(text).toContain('Playkeeper opens it on my-vps. Open UDP 24455 in your provider’s firewall too.')
  })

  it('says nothing about a port for a pack without voice chat', async () => {
    expect(await openPackPlan('VOIC0002')).not.toContain('Voice travels on its own port')
  })

  it('lays out every server type in whole rows, Forge with its logo', async () => {
    window.history.replaceState(null, '', '/servers/new')
    const ids = ['paper', 'vanilla', 'purpur', 'fabric', 'quilt', 'neoforge', 'forge']
    const names: Record<string, string> = { paper: 'Paper', vanilla: 'Vanilla', purpur: 'Purpur', fabric: 'Fabric', quilt: 'Quilt', neoforge: 'NeoForge', forge: 'Forge' }
    const cards = () => [...document.querySelectorAll('[role="radiogroup"][aria-label="Server type"] label')]
    const catalog: Catalog = { type: 'paper', types: [], versions: [], memoryOptionsMB: [2048, 3072, 4096], recommendedMemoryMB: 2048, hostMemoryMB: 16384, maxMemoryMB: 4096, systemReserveMB: 1536, memoryFreeMB: 10752, servers: [], image: '' }
    answer({ '/catalog': { ...catalog, types: ids.map((id) => ({ id, name: names[id] ?? id, available: true })) } })
    await render(<NewServerPage />)
    await act(async () => {})
    const all = cards()
    expect(all.map((c) => c.querySelector('.font-semibold')?.textContent?.replace('Recommended', ''))).toEqual(Object.values(names))
    expect(all.map((c) => c.classList.contains('col-span-full'))).toEqual([true, false, false, false, false, false, false])
    const forge = all[6]
    expect(forge?.textContent).toContain('The original loader for Forge mods.')
    expect(forge?.querySelector('img')?.getAttribute('src')).toContain('forge-apple-touch-icon')

    answer({ '/catalog': { ...catalog, types: ids.slice(0, 6).map((id) => ({ id, name: names[id] ?? id, available: true })) } })
    await render(<NewServerPage />)
    await act(async () => {})
    expect(cards().some((c) => c.classList.contains('col-span-full'))).toBe(false)
  })

  // Each row asks another machine, as the picker keeps what it loaded for a while.
  it.each([
    { name: 'its page on Modrinth', pageUrl: 'https://modrinth.com/modpack/smoothserver', machineId: 'm2345abcdf', shown: true },
    { name: 'a path on the dashboard', pageUrl: '/api/servers/abcdefghjk/offsite/recovery-key', machineId: 'm2345abcdg', shown: false },
    { name: 'a script', pageUrl: 'javascript:alert(document.cookie)', machineId: 'm2345abcdh', shown: false },
  ])('links a pack to $name only when it is another site', async ({ pageUrl, machineId, shown }) => {
    const card = results.cards[0]
    if (!card) throw new Error('no card')
    const detail: ModpackDetail = { ...card, pageUrl, versions: [{ id: 'SMV00012', number: '1.2', channel: 'release', published: '2026-09-01T00:00:00Z', size: 16_000_000, type: 'fabric', minecraftVersion: '26.2', mods: 17 }], newest: 'SMV00012' }
    answer({
      '/preview': { type: 'fabric', minecraftVersion: '26.2', loaderVersion: '0.19.3', files: 17, downloadSize: 16_000_000, ready: true, blockers: [], warnings: [], manual: [] },
      '/modpacks/modrinth/SMTH0001': detail,
      '/modpacks?': { ...results, cards: [{ ...card, pageUrl }] },
    })
    await render(<ModpackPicker machineId={machineId} onChange={() => {}} onUse={() => {}} phone={false} />)
    const row = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Smooth Server'))
    await act(async () => row?.click())
    await act(async () => {})
    await act(async () => {})
    expect(document.body.textContent).toContain('What friends do')
    expect(document.body.textContent?.includes('Open on Modrinth')).toBe(shown)
    expect(document.querySelector(`a[href="${pageUrl}"]`) !== null).toBe(shown)
  })

  // The key goes on the machine the server is made on, so the link to it chooses that machine.
  it.each([
    { name: 'the dashboard’s machine', machineId: 'm2345abcde' },
    { name: 'a joined machine', machineId: 'h2345abcde' },
  ])('sends whoever needs a CurseForge key to the key of $name', async ({ machineId }) => {
    answer({ '/modpacks?': results })
    await render(<ModpackPicker machineId={machineId} onChange={() => {}} onUse={() => {}} phone={false} />)
    const link = [...document.querySelectorAll('a')].find((a) => a.getAttribute('href')?.startsWith('/settings/addon-sources'))
    expect(link?.getAttribute('href')).toBe(`/settings/addon-sources?machine=${machineId}`)
    expect(parse('/settings/addon-sources', `?machine=${machineId}`)).toEqual({ name: 'addon-sources', machine: machineId })
  })

  // All the Mods 10 is on CurseForge only, as CurseForge listed it on 27 Sep.
  const atm10: ModpackCard = {
    source: 'curseforge',
    projectId: '925200',
    slug: 'all-the-mods-10',
    name: 'All the Mods 10 - ATM10',
    author: 'ATMTeam',
    summary: 'All the Mods started out as a private modpack.',
    downloads: 21_947_994,
    updated: '2026-09-22T09:34:09Z',
    pageUrl: 'https://www.curseforge.com/minecraft/modpacks/all-the-mods-10',
    types: ['neoforge'],
    minecraftVersions: ['1.21.1'],
  }
  const keyed: ModpackResults = { ...results, sources: ['modrinth', 'curseforge'] }

  /** Types text into the picker's search and presses Enter. */
  async function searchPacks(text: string) {
    const form = document.querySelector('form[role="search"]')
    const input = form?.querySelector('input')
    if (!form || !input) throw new Error('no search form')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, text)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    for (let i = 0; i < 3; i++) await act(async () => {})
  }
  const packRows = () => [...document.querySelectorAll('[role="radiogroup"] > div')].map((r) => r.textContent ?? '')
  const packSearches = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes('/modpacks?'))
  const searchField = () => document.querySelector('form[role="search"] input')?.getAttribute('placeholder')

  it('lists CurseForge’s packs with Modrinth’s once the machine offers CurseForge', async () => {
    answer({ 'source=curseforge': { ...keyed, cards: [atm10] }, 'source=modrinth': keyed })
    await render(<ModpackPicker machineId="c2345abcde" onChange={() => {}} onUse={() => {}} phone={false} />)
    await searchPacks('All the Mods 10')
    expect(packSearches().filter((p) => p.includes('q=All+the+Mods+10'))).toEqual([
      '/api/machines/c2345abcde/modpacks?source=modrinth&sort=downloads&limit=12&q=All+the+Mods+10',
      '/api/machines/c2345abcde/modpacks?source=curseforge&sort=downloads&limit=12&q=All+the+Mods+10',
    ])
    const rows = packRows()
    expect(rows).toHaveLength(2)
    expect(rows[0]).toContain('All the Mods 10 - ATM10')
    expect(rows[0]).toContain('21.9M downloads on CurseForge')
    expect(rows[1]).toContain('Smooth Server')
    expect(rows[1]).toContain('1.2k downloads on Modrinth')
    expect(searchField()).toBe('Search Modrinth and CurseForge modpacks')
    expect(page()).not.toContain('need a free key')
  })

  it('asks only Modrinth on a machine without a CurseForge key, and says how to get one', async () => {
    answer({ 'source=curseforge': { ...keyed, cards: [atm10] }, 'source=modrinth': results })
    await render(<ModpackPicker machineId="c2345abcdf" onChange={() => {}} onUse={() => {}} phone={false} />)
    await searchPacks('All the Mods 10')
    expect(packSearches().some((p) => p.includes('source=curseforge'))).toBe(false)
    expect(packRows()).toEqual([expect.stringContaining('Smooth Server')])
    expect(packRows()[0]).toContain('1.2k downloads')
    expect(packRows()[0]).not.toContain('on Modrinth')
    expect(searchField()).toBe('Search Modrinth modpacks')
    expect(page()).toContain('CurseForge modpacks need a free key.')
  })

  it('says nothing about the mods of a CurseForge pack, which CurseForge doesn’t list', async () => {
    const detail: ModpackDetail = { ...atm10, versions: [{ id: '8945086', number: 'All the Mods 10-8.2', channel: 'release', published: '2026-09-22T09:34:09Z', size: 199_005_126, type: 'neoforge', minecraftVersion: '1.21.1' }], newest: '8945086' }
    answer({
      '/preview': { type: 'neoforge', minecraftVersion: '1.21.1', loaderVersion: '21.1.251', files: 3904, downloadSize: 1_361_589_811, ready: true, blockers: [], warnings: [], manual: [] },
      '/modpacks/curseforge/925200': detail,
      'source=curseforge': { ...keyed, cards: [atm10] },
      'source=modrinth': keyed,
    })
    await render(<ModpackPicker machineId="c2345abcdh" onChange={() => {}} onUse={() => {}} phone={false} />)
    await act(async () => {})
    const row = [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('All the Mods 10 - ATM10'))
    await act(async () => row?.click())
    for (let i = 0; i < 4; i++) await act(async () => {})
    expect(page()).toContain('by ATMTeam · CurseForge')
    expect(page()).toContain('NeoForge 21.1.251')
    expect(page()).not.toContain('What’s inside')
    expect(page()).not.toContain('0 mods')
  })

  it('opens on the modpack list when a link asks for it', async () => {
    window.history.replaceState(null, '', '/servers/new#modpack')
    answer({ '/modpacks?': results })
    await render(<NewServerPage />)
    await act(async () => {})
    expect([...document.querySelectorAll('[aria-label="Start from"] button')].find((b) => b.hasAttribute('data-pressed'))?.textContent).toBe('A modpack')
    expect(searchField()).toBe('Search Modrinth modpacks')
    window.history.replaceState(null, '', '/servers/new')
  })

  it('keeps one source’s packs while the other fails, and says why', async () => {
    answer({ 'source=curseforge': new client.ApiError(409, { error: 'CurseForge refused Playkeeper’s API key.', code: 'curseforge_key_refused' }), 'source=modrinth': keyed })
    await render(<ModpackPicker machineId="c2345abcdg" onChange={() => {}} onUse={() => {}} phone={false} />)
    await act(async () => {})
    expect(packRows()).toEqual([expect.stringContaining('Smooth Server')])
    expect(page()).toContain('CurseForge refused Playkeeper’s API key.')
    // The machine said it offers CurseForge, so a search Modrinth fails still lists CurseForge's packs.
    answer({ 'source=curseforge': { ...keyed, cards: [atm10] }, 'source=modrinth': new client.ApiError(502, { error: 'Playkeeper could not reach Modrinth.', code: 'unreachable' }) })
    await searchPacks('ATM10')
    expect(packRows()).toEqual([expect.stringContaining('All the Mods 10 - ATM10')])
    expect(page()).toContain('Playkeeper could not reach Modrinth.')
    expect(page()).not.toContain('Couldn’t load modpacks')
  })

  it('says what a pack’s server downloads, not Paper', () => {
    expect(createNote(4, 'modpack', 'fabric')).toBe('After you start it, Playkeeper downloads Fabric and the pack’s mods, checks each file, and tells you once it’s running.')
    expect(createNote(4, 'template', 'neoforge')).toContain('downloads NeoForge and the template’s add-ons')
    expect(createNote(4, 'modpack', 'forge')).toContain('downloads Forge and the pack’s mods')
    expect(createNote(4, 'modpack', '')).not.toContain('Paper')
    expect(createNote(4, 'type', 'purpur')).toContain('downloads Purpur, checks it')
    expect(createNote(0, 'modpack', 'fabric')).toBe('Friends install the same modpack. You get a link to send.')
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

  // The second-day walkthrough of 4 Oct 2026: on a machine with no name, an
  // invite link opens the browser's "Your connection is not private" for the
  // friend, whom nothing had told. The new link's dialog says so and offers
  // the free name, and what's copied says the warning is expected.
  it('warns of the browser warning on a link to a machine with no name, and says it in what it copies', async () => {
    const made: Invite = { id: 'inv9', kind: 'player', projectId: 'p2345abcde', serverId: 'abcdefghjk', approval: 'right_away', createdBy: 1, createdAt: new Date().toISOString(), expiresAt: inHours(7 * 24), maxUses: 5, uses: 0, usesLeft: 5, status: 'active', path: '/join/Nw3vT8kLp2QsXa7m' }
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    for (const named of [false, true]) {
      const data: InvitesResponse = named ? { ...noInvites, link: { base: 'https://alex.playkeeper.me', friendly: true } } : noInvites
      answer({ '/whitelist': [{ name: 'mara_k' }], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': data, '/join-requests': [] })
      answerPosts({ '/invites': made })
      await render(<PlayersPage server={server()} />)
      await click(buttons('New invite link')[0] ?? button('New invite link'))
      await click(button('Create link'))
      if (named) {
        expect(copy).toHaveBeenLastCalledWith('https://alex.playkeeper.me/join/Nw3vT8kLp2QsXa7m')
        continue
      }
      expect(copy).toHaveBeenLastCalledWith('Your browser will say this link isn’t private. That’s expected for a new server: click Advanced, then Proceed (Safari: Show Details, then visit this website). https://203.0.113.10:8443/join/Nw3vT8kLp2QsXa7m')
    }
    copy.mockRestore()
    answer({ '/whitelist': [{ name: 'mara_k' }], '/operators': [], '/players/summary': nobody, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [], '/invites': noInvites, '/join-requests': [] })
    await render(<PlayersPage server={server()} />)
    await click(buttons('New invite link')[0] ?? button('New invite link'))
    expect(page()).toContain('Friends will see a browser warning')
    expect(link('Give it a free name first').getAttribute('href')).toBe(`/machines/${machine.id}/settings`)
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
    const backup: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya', note: 'Before the dragon' }
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
      <WorkspaceProvider me={me} onMe={() => {}} onSignedOut={() => {}}>
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
  const checkAgain = () => [...document.querySelectorAll('button')].find((b) => /^(Check again|Checked)$/.test(b.textContent ?? ''))

  // The walkthrough of 1 Oct 2026: the card said "exit code 1" over a Java
  // stack trace, and offered only "Go to my dashboard".
  it('says in plain words why setting up the first server failed, and tries again from there', async () => {
    const crash = { at: new Date().toISOString(), start: true, kind: 'download_failed' as const, params: { reason: 'tls', file: 'mojang_26.2.jar' }, certain: true, title: '', explanation: '', evidence: [], fixes: [], lines: [], roomMB: 0 }
    const s = server({ phase: 'stopped', desired: 'stopped', startedAt: undefined, lastOperation: failed('create', 'starting', 'The server stopped while starting (exit code 1).'), crash })
    answer({ '/logs': { epoch: 'e', lines: [], next: 0, truncated: false } })
    const started = vi.fn(() => ({}))
    answerPosts({ '/start': started })
    const text = await render(<Onboarding />, workspace({ servers: [s] }))
    expect(text).toContain('Setting up Survival didn’t finish')
    expect(text).toContain('Couldn’t download Minecraft from Mojang. Check this VPS’s internet, then try again.')
    expect(text).not.toContain('exit code 1')
    const retry = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Try again')
    await act(async () => retry?.click())
    expect(started).toHaveBeenCalledOnce()
    expect(vi.mocked(client.post).mock.calls.at(-1)?.[0]).toBe('/api/servers/abcdefghjk/start')
  })

  // The walkthrough of 1 Oct 2026: the free name cures the browser warning,
  // but setup never offered it.
  describe('the free name it offers', () => {
    const none: Address = { kind: '', ip: '198.51.100.10', panelPort: 8443, base: 'playkeeper.me', servers: [], names: { url: 'https://names.playkeeper.io' } }
    const admin = workspace({ servers: [], me: { ...me, user: { ...me.user, username: 'admin' } } })
    const reserved = { name: 'admin', address: 'admin.playkeeper.me', available: false, code: 'name_reserved', message: 'Reserved.' }
    const offer = () => [...document.querySelectorAll('button')].find((b) => b.textContent?.startsWith('Get '))

    it('is a free one, never a reserved one, and one button claims it while setup goes on', async () => {
      answer({ '/preflight': preflight, '/address/available?name=admin': reserved, '/address/available': { available: true }, '/address': none })
      await render(<Onboarding />, admin)
      await act(async () => {})
      expect(document.body.textContent).toContain('A free name for this VPS')
      expect(document.body.textContent).toContain('Your browser stops warning you.')
      expect(document.body.textContent).toContain('By continuing you accept Let’s Encrypt’s terms.')
      const name = /^Get ([a-z]+-[a-z]+-\d{2})\.playkeeper\.me$/.exec(offer()?.textContent ?? '')?.[1]
      expect(name, offer()?.textContent).toBeDefined()

      // Another name offers a different free one.
      await act(async () => [...document.querySelectorAll('button')].find((b) => b.textContent === 'Another name')?.click())
      await act(async () => {})
      const next = /^Get (.+)\.playkeeper\.me$/.exec(offer()?.textContent ?? '')?.[1]
      expect(next).toBeDefined()
      expect(next).not.toBe(name)

      const claimed: Address = { ...none, kind: 'playkeeper', host: `${next}.playkeeper.me`, free: { name: next ?? '', state: 'active', dns: 'pending', claimedAt: new Date().toISOString(), refreshedAt: new Date().toISOString(), checkedAt: new Date().toISOString(), holdDays: 30 }, operation: { id: 'op1', kind: 'address.publish', status: 'running', phase: 'pointing', actor: 'admin', startedAt: new Date().toISOString() } }
      answerPosts({ '/address/claim': claimed })
      answer({ '/preflight': preflight, '/address': claimed })
      await act(async () => offer()?.click())
      expect(client.post).toHaveBeenCalledWith('/api/machines/m2345abcde/address/claim', { name: next, acceptTerms: true })
      expect(document.body.textContent).toContain(`Getting ${next}.playkeeper.me`)
      expect(document.body.textContent).toContain('You can carry on meanwhile.')
      expect([...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Looks good, continue'))?.disabled).toBe(false)
    })

    it('says when the name couldn’t be finished, and where to finish it, rather than getting it for ever', async () => {
      answer({ '/preflight': preflight, '/address/available?name=admin': reserved, '/address/available': { available: true }, '/address': none })
      await render(<Onboarding />, admin)
      await act(async () => {})
      const name = /^Get (.+)\.playkeeper\.me$/.exec(offer()?.textContent ?? '')?.[1] ?? ''
      const at = new Date().toISOString()
      const failedPublish: Address = {
        ...none,
        kind: 'playkeeper',
        host: `${name}.playkeeper.me`,
        free: { name, state: 'active', dns: 'pending', claimedAt: at, refreshedAt: at, checkedAt: at, holdDays: 30 },
        operation: { id: 'op1', kind: 'address.publish', status: 'failed', phase: 'certificate', actor: 'admin', startedAt: at, finishedAt: at, error: 'Let’s Encrypt didn’t answer.' },
      }
      answerPosts({ '/address/claim': failedPublish })
      answer({ '/preflight': preflight, '/address': failedPublish })
      await act(async () => offer()?.click())
      expect(document.body.textContent).toContain(`Couldn’t finish ${name}.playkeeper.me`)
      expect(document.body.textContent).toContain('Let’s Encrypt didn’t answer.')
      expect(document.body.textContent).not.toContain('Getting')
      expect([...document.querySelectorAll('a')].find((l) => l.textContent === 'Finish it in Machine settings')?.getAttribute('href')).toBe('/machines/m2345abcde/settings')
    })

    it('shows a claim that went through as being got, not the offer again, while the machine doesn’t show it yet', async () => {
      answer({ '/preflight': preflight, '/address/available?name=admin': reserved, '/address/available': { available: true }, '/address': none })
      await render(<Onboarding />, admin)
      await act(async () => {})
      const name = /^Get (.+)\.playkeeper\.me$/.exec(offer()?.textContent ?? '')?.[1] ?? ''
      answerPosts({ '/address/claim': { ...none, kind: 'playkeeper', host: `${name}.playkeeper.me` } })
      // The machine's address still answers as before the claim, as a poll that left before it can.
      await act(async () => offer()?.click())
      expect(document.body.textContent).toContain(`Getting ${name}.playkeeper.me`)
      expect(offer()).toBeUndefined()
      expect(vi.mocked(client.post).mock.calls.filter(([path]) => path.endsWith('/address/claim'))).toHaveLength(1)
    })

    it('isn’t offered while the names service can’t be reached, or the machine has an address', async () => {
      answer({ '/preflight': preflight, '/address/available': new client.ApiError(503, { error: 'Playkeeper couldn’t reach the free address service.', code: 'names_unreachable' }), '/address': none })
      await render(<Onboarding />, admin)
      await act(async () => {})
      expect(document.body.textContent).toContain('Checking this VPS')
      expect(document.body.textContent).not.toContain('A free name for this VPS')

      answer({ '/preflight': preflight, '/address': { ...none, kind: 'own', host: 'play.example.com' } })
      await render(<Onboarding />, admin)
      await act(async () => {})
      expect(document.body.textContent).not.toContain('A free name for this VPS')
      expect(vi.mocked(client.get).mock.calls.some(([p]) => p.includes('/address/available'))).toBe(true)
    })
  })

  // The walkthrough of 1 Oct 2026: the allowlist is on, and nobody asked for
  // the owner's own Minecraft name, so their first join was refused.
  describe('your own Minecraft name', () => {
    const catalog: Catalog = {
      type: 'paper',
      types: [{ id: 'paper', name: 'Paper', available: true }],
      versions: [{ id: 'paper-26.2-12', label: '26.2', minecraftVersion: '26.2', paperBuild: 12, jarSha256: 'cd'.repeat(32), java: 25, recommended: true, notes: '', channel: 'STABLE', experimental: false, supported: true }],
      memoryOptionsMB: [2048, 3072],
      recommendedMemoryMB: 3072,
      hostMemoryMB: 16384,
      maxMemoryMB: 3072,
      systemReserveMB: 1536,
      memoryFreeMB: 10752,
      servers: [],
      image: '',
    }
    const steve = { name: 'Steve_Builds', uuid: '00000000-0000-0000-0000-0000000000a1' }
    const field = () => document.getElementById([...document.querySelectorAll('label')].find((l) => l.textContent === 'Your Minecraft name')?.htmlFor ?? '') as HTMLInputElement | null
    const typeName = async (v: string) =>
      act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(field(), v)
        field()?.dispatchEvent(new Event('input', { bubbles: true }))
      })
    const create = () => [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Create my server'))
    /** Goes to "How will you play?" and accepts the EULA. */
    const toStyle = async (ws: Workspace) => {
      answer({ '/preflight': preflight, '/catalog': catalog })
      answerPosts({ '/servers': { id: 'op1', kind: 'create', status: 'running', serverId: 'abcdefghjk', phase: '', actor: 'siya', startedAt: new Date().toISOString() } })
      await render(<Onboarding />, ws)
      await press('Looks good, continue')
      await press('Create a server')
      await act(async () => [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith('I accept the Minecraft'))?.click())
    }
    /** The server runs: the online stage reads the lists the agent added the name to, as they answer here. */
    const online = async (ws: Workspace, lists: Record<string, unknown>, over: Partial<ServerStatus> = {}) => {
      answer({ ...lists, '/logs': { epoch: 'e', lines: [], next: 0, truncated: false } })
      await act(async () => root?.render(<WorkspaceContext.Provider value={{ ...ws, servers: [server(over)] }}>{<Onboarding />}</WorkspaceContext.Provider>))
      await act(async () => {})
    }

    it('is asked for, made an operator on the new server, and said to be one once it runs', async () => {
      const setPrefs = vi.fn(async () => {})
      const ws = workspace({ servers: [], setPrefs })
      await toStyle(ws)
      expect(field()?.placeholder).toBe('Optional')
      await typeName('bad name')
      expect(create()?.title).toBe('Minecraft usernames are 3–16 letters, numbers or underscores.')
      // Create pressed now moves to the name, whose hint already says why, rather than saying it again.
      await press('Create my server')
      expect(document.activeElement).toBe(field())
      expect(document.querySelectorAll('[role="alert"]')).toHaveLength(0)
      expect(vi.mocked(client.post).mock.calls.some(([path]) => path.endsWith('/servers'))).toBe(false)
      await typeName('Steve_Builds')
      expect(create()?.title).toBe('')
      await press('Create my server')
      expect(vi.mocked(client.post).mock.calls.at(-1)).toEqual(['/api/machines/m2345abcde/servers', expect.objectContaining({ acceptEula: true, operators: ['Steve_Builds'] })])
      expect(setPrefs).toHaveBeenCalledWith({ 'minecraft.name': 'Steve_Builds' })

      await online(ws, { '/whitelist': [steve], '/operators': [{ ...steve, level: 4 }] })
      expect(page()).toContain('Survival is online!')
      expect(page()).toContain('Steve_Builds is on the allowlist and an operator.')
    })

    // Setting up says you can leave the page: back on it, the server is still
    // being set up, and the name it was created with is the account's.
    it('is still confirmed after leaving the page while the server was set up', async () => {
      const settingUp = server({ phase: 'starting', startedAt: undefined, operation: { id: 'op1', kind: 'create', status: 'running', phase: 'starting', actor: 'siya', startedAt: new Date().toISOString() } })
      const ws = workspace({ servers: [settingUp], prefs: { 'minecraft.name': 'Steve_Builds' } })
      answer({ '/logs': { epoch: 'e', lines: [], next: 0, truncated: false } })
      await render(<Onboarding />, ws)
      expect(page()).toContain('Setting up Survival')
      await online(ws, { '/whitelist': [steve], '/operators': [{ ...steve, level: 4 }] })
      expect(page()).toContain('Steve_Builds is on the allowlist and an operator.')
    })

    // A first start that timed out but came up after all: the agent adds the
    // name once it sees the server online.
    it('waits for the agent to add it before saying how it went', async () => {
      const ws = workspace({ servers: [] })
      await toStyle(ws)
      await typeName('Steve_Builds')
      await press('Create my server')
      const lists = { '/whitelist': [steve], '/operators': [{ ...steve, level: 4 }] }
      await online(ws, lists, { config: { ...config, pendingOperators: 'Steve_Builds' } })
      expect(page()).toContain('Survival is online!')
      expect(page()).not.toContain('Steve_Builds is on the allowlist')
      expect(page()).not.toContain('Couldn’t add')
      expect(vi.mocked(client.get).mock.calls.some(([path]) => path.includes('/whitelist'))).toBe(false)

      await online(ws, lists)
      expect(page()).toContain('Steve_Builds is on the allowlist and an operator.')
    })

    it('says only what the server’s lists show, and offers to add a name that didn’t get on', async () => {
      const outcomes = [
        { lists: { '/whitelist': [steve], '/operators': [] }, says: 'Steve_Builds is on the allowlist.', offered: '' },
        { lists: { '/whitelist': [], '/operators': [] }, says: 'Couldn’t add Steve_Builds. Check the spelling, then add it below.', offered: 'Steve_Builds' },
      ]
      for (const { lists, says, offered } of outcomes) {
        const ws = workspace({ servers: [] })
        await toStyle(ws)
        await typeName('Steve_Builds')
        await press('Create my server')
        await online(ws, lists)
        expect(page()).toContain(says)
        expect(page()).not.toContain('an operator.')
        expect((document.getElementById('invite') as HTMLInputElement | null)?.value).toBe(offered)
      }
    })
  })

  it('says the checks ran again, but not when they could not be run', async () => {
    answer({ '/preflight': preflight })
    await render(<Onboarding />, workspace({ servers: [] }))
    await act(async () => checkAgain()?.click())
    expect(checkAgain()?.textContent).toBe('Checked')

    await render(<Onboarding />, workspace({ servers: [] }))
    vi.mocked(client.get).mockImplementation((() => Promise.reject(new client.ApiError(0, { error: 'The dashboard can’t be reached.', code: 'internal' }))) as typeof client.get)
    await act(async () => checkAgain()?.click())
    expect(document.body.textContent).toContain('The dashboard can’t be reached.')
    expect(checkAgain()?.textContent).toBe('Check again')
  })

  it('checks the machine in plain words, with the provider firewall a note that doesn’t count against it', async () => {
    answer({ '/preflight': preflight })
    const text = await render(<Onboarding />, workspace({ servers: [] }))
    expect(text).toContain('Checking this VPS')
    expect(text).toContain('Memory: 16 GB')
    expect(text).toContain('Ubuntu 24.04 on x86-64')
    expect(text).toContain('Port 25565 is free')
    expect(text).toContain('6 of 6 look good')
    // The VPS can't see its provider's firewall, so the note neither warns nor counts.
    expect(text).toContain('Friends join on port 25565')
    expect(text).not.toContain('friends can’t join')
    const how = [...document.querySelectorAll('a')].find((a) => a.textContent === 'How to open a port')
    expect(how?.getAttribute('href')).toBe('https://playkeeper.io/ports')
    expect(document.querySelectorAll('li .text-warning')).toHaveLength(0)
  })
})

describe('Machine page', () => {
  const none: Address = { kind: '', ip: '198.51.100.10', panelPort: 8443, base: 'playkeeper.me', servers: [], names: { url: 'https://names.playkeeper.io' } }
  const day = 24 * 3600_000
  const certificate = { names: ['alex.playkeeper.me'], challenge: 'dns-01', notBefore: new Date(Date.now() - 30 * day).toISOString(), notAfter: new Date(Date.now() + 60 * day).toISOString() }
  const free: Address = { ...none, kind: 'playkeeper', host: 'alex.playkeeper.me', certificate }

  const health = async (address: Address) => {
    answer({ '/address': address })
    await render(<MachinePage id={machine.id} />)
    const line = [...document.querySelectorAll('a')].find((a) => a.textContent?.includes('Dashboard certificate'))
    if (!line) throw new Error('the machine page has no certificate line')
    return line
  }

  it('says the dashboard’s certificate is self-signed until there’s an address, and links to Machine settings', async () => {
    const line = await health(none)
    expect(line.textContent).toBe('Dashboard certificateSelf-signed')
    expect(line.getAttribute('href')).toBe(`/machines/${machine.id}/settings`)
  })

  it('names the real certificate with the day it runs out, and a failed renewal', async () => {
    expect((await health(free)).textContent).toContain(`Let’s Encrypt until ${formatLongDate(certificate.notAfter)}`)
    const renewal = { ...certificate, problem: { code: 'port80_unreachable', message: 'Port 80 is closed.' } }
    expect((await health({ ...free, certificate: renewal })).textContent).toContain('Couldn’t renew the certificate')
    expect((await health({ ...free, certificate: { ...renewal, notAfter: undefined, notBefore: undefined } })).textContent).toContain('Couldn’t get a certificate')
  })
})

describe('Command palette', () => {
  it('opens from the sidebar once its code loads, and its shortcuts from its footer', async () => {
    await render(<AppShell route={{ name: 'home' }}><p>page</p></AppShell>)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    const search = [...document.querySelectorAll('aside button')].find((b) => b.textContent?.includes('Search or jump to…'))
    if (!search) throw new Error('no Search in the sidebar')
    await act(async () => (search as HTMLButtonElement).click())
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"][aria-label="Search or jump to"]')).not.toBeNull())
    const shortcuts = [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent?.includes('all shortcuts'))
    if (!shortcuts) throw new Error('no shortcuts button in the palette')
    await act(async () => (shortcuts as HTMLButtonElement).click())
    await vi.waitFor(() => expect(page()).toContain('Keyboard shortcuts'))
  })

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

  it('offers nothing to do to a server being moved, a backup neither', async () => {
    const palette = (moving: boolean) => render(<CommandPalette open onOpenChange={() => {}} route={{ name: 'home' }} onShortcuts={() => {}} />, workspace({ servers: [server({ phase: 'online', moving })] }))
    expect(await palette(false)).toContain('Back up Survival now')
    const text = await palette(true)
    for (const action of ['Back up Survival now', 'Restart Survival', 'Stop Survival']) expect(text).not.toContain(action)
  })

  it('offers each server the tabs its tab bar shows', async () => {
    const servers = [server(), server({ id: 'bcdefghjkm', name: 'Modded', slug: 'modded', type: 'fabric' })]
    const palette = (who: Me) => render(<CommandPalette open onOpenChange={() => {}} route={{ name: 'home' }} onShortcuts={() => {}} />, workspace({ me: who, servers }))
    const moderator = await palette(member('moderator', moderatorCan))
    expect(moderator).toContain('Survival › Console')
    expect(moderator).toContain('Modded › World')
    for (const page of ['Survival › Settings', 'Survival › Plugins', 'Modded › Settings', 'Modded › Mods']) expect(moderator).not.toContain(page)
    const viewer = await palette(member('viewer', ['view', 'account.manage']))
    expect(viewer).toContain('Survival › Players')
    expect(viewer).not.toContain('› Settings')
    const admin = await palette(me)
    for (const page of ['Survival › Plugins', 'Survival › Settings', 'Modded › Mods', 'Modded › Settings']) expect(admin).toContain(page)
    for (const page of ['Survival › Mods', 'Modded › Plugins']) expect(admin).not.toContain(page)
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
    expect(button('Add me to Survival').title).toBe('Type your Minecraft name first.')
    await typeInto('#join-name', 'mara_k')
    expect(page()).toContain('Looking up mara_k…')
    expect(button('Add me to Survival').title).toBe('Looking up mara_k…')
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
    expect(button('Ask to join Survival').title).toBe('No Minecraft: Java Edition account is called mara_kk.')
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
    expect(button('Join as Moderator').title).toBe('Fill in the fields above first.')
    await typeInto('#join-username', 'siya')
    await typeInto('#join-password', 'correct horse battery')
    await typeInto('#join-again', 'correct horse batterz')
    expect(page()).toContain('The passwords don’t match.')
    expect(button('Join as Moderator').disabled).toBe(true)
    expect(button('Join as Moderator').title).toBe('The passwords don’t match.')
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

  it('has a new admin turn on two-factor sign-in with the password just chosen, or go on as a Moderator', async () => {
    const preview: JoinPreview = { kind: 'member', inviter: '', role: 'admin', servers: { all: true }, expiresAt: '2026-10-02T12:00:00Z', serverNames: [], team: 'Friends' }
    const signedIn = member('admin', moderatorCan, { servers: { all: true }, needsTwoFactor: true })
    const setup: TwoFactorSetup = {
      qrCodeSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 21 21"/>',
      manualKey: '4KQZ 7MXP 2RDN 6WYA 5HTB 3JCE LN2V QF7S',
      uri: 'otpauth://totp/Playkeeper:alex?secret=4KQZ7MXP2RDN6WYA5HTB3JCELN2VQF7S&issuer=Playkeeper',
      issuer: 'Playkeeper',
      account: 'alex',
      expiresAt: '2026-10-02T12:15:00Z',
    }
    const codes = ['k7qm-4tzd-9hxw-2rbn', 'p3vc-8jwa-6fke-5msy', 'x2nd-7gqr-4bzh-9tce', 'm9wf-3kpa-8vrn-6dqj', 'c4ht-9xme-2qwz-7bnk', 'r6ya-5dkq-3pjw-8fmx', 'v8bn-2tce-7hqk-4wzr', 'e5jx-6mra-9dvf-3kpt', 'h3wq-8zcn-5tbm-2yja', 'z7kp-4fve-6xrd-9qhm']
    answerPosts({ '/preview': preview, '/accept': signedIn, '/2fa/setup': setup, '/2fa/confirm': { recoveryCodes: codes } })
    const join = async (onSignedIn: (me: Me, to?: string) => void) => {
      const text = await render(<JoinPage code={code} onSignedIn={onSignedIn} />)
      expect(text).toContain('Help run Friends as Admin')
      await typeInto('#join-username', 'alex')
      await typeInto('#join-password', 'correct horse battery')
      await typeInto('#join-again', 'correct horse battery')
      await click(button('Join as Admin'))
    }
    const current = () => document.querySelector('[aria-current="step"]')?.textContent

    const later = vi.fn()
    await join(later)
    expect(later).not.toHaveBeenCalled()
    expect(client.post).toHaveBeenCalledWith('/api/auth/2fa/setup', { password: 'correct horse battery' })
    expect(page()).toContain('One more step: two-factor sign-in')
    expect(page()).toContain('Admins must use it. Until then, you have Moderator rights.')
    expect(page()).toContain(setup.manualKey)
    expect(page()).toContain('Next: save your recovery codes')
    expect(document.querySelector('img[alt="QR code for your authenticator app"]')).not.toBeNull()
    expect(current()).toContain('Two-factor')
    await click(button('Not now, continue as Moderator'))
    expect(client.del).toHaveBeenCalledWith('/api/auth/2fa/setup')
    expect(later).toHaveBeenCalledWith(signedIn)

    const confirmed = member('admin', moderatorCan, { servers: { all: true }, twoFactor: true, awaitingConfirmation: true })
    answer({ '/api/auth/me': confirmed })
    const done = vi.fn()
    await join(done)
    for (const [i, digit] of [...'482913'].entries()) {
      const box = document.querySelectorAll<HTMLInputElement>('input[inputmode="numeric"]')[i]
      if (!box) throw new Error(`no code box ${i}`)
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(box, digit)
        box.dispatchEvent(new Event('input', { bubbles: true }))
      })
    }
    expect(client.post).toHaveBeenCalledWith('/api/auth/2fa/confirm', { code: '482913' })
    expect(page()).toContain('Save your recovery codes')
    expect(page()).toContain('Step 3 of 3')
    for (const c of codes) expect(page()).toContain(c)
    expect(current()).toContain('Recovery codes')
    expect(done).not.toHaveBeenCalled()
    await click(button('I’ve saved them'))
    expect(done).toHaveBeenCalledWith(confirmed)
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

  it('tells an admin why Admin, all servers or no servers can’t be given', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [{ id: 2, username: 'mara', owner: false, you: true, role: 'admin', servers: { servers: ['abcdefghjk'] }, twoFactor: true, addedAt: hoursAgo(49), canEdit: false }],
      invites: [],
      grantableRoles: ['moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    await render(<TeamSection />, workspace({ me: member('admin', everything, { servers: { servers: ['abcdefghjk'] }, twoFactor: true }) }))
    await click(button('Add a team member'))
    const choice = (start: string) => [...document.querySelectorAll('label')].find((l) => l.textContent?.startsWith(start))
    const why = (el: Element | null | undefined) => document.getElementById(el?.getAttribute('aria-describedby') ?? '')?.textContent
    const admin = choice('Admin')?.querySelector('[data-slot="radio"]')
    const all = choice('All servers')?.querySelector('[data-slot="radio"]')
    expect(admin?.hasAttribute('data-disabled')).toBe(true)
    expect(why(admin)).toBe('Only the owner can give this role.')
    expect(all?.hasAttribute('data-disabled')).toBe(true)
    expect(why(all)).toBe('You can give only the servers you can use.')
    expect(button('Create link').disabled).toBe(false)
    const survival = choice('Survival')
    if (!survival) throw new Error('no checkbox for Survival')
    await click(survival)
    expect(button('Create link').disabled).toBe(true)
    expect(button('Create link').title).toBe('Pick at least one server.')
  })

  it('lets the owner invite a creator with an allowance, and shows creators and their links apart', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false },
        { id: 4, username: 'alex', owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, addedAt: hoursAgo(2), canEdit: true, allowance: { servers: 1, memoryMB: 4096 } },
      ],
      invites: [{ id: 'ti2', kind: 'member', projectId: 'p2345abcde', role: 'admin', label: 'cambam', createdBy: 1, createdAt: hoursAgo(1), expiresAt: inHours(6 * 24 + 1), maxUses: 1, uses: 0, status: 'active', canEdit: true, allowance: { servers: 1, memoryMB: 8192 } }],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    answerPosts({ '/api/team/invites': { invite: team.invites[0], path: '/join/Qm7xK2pLw9RtVb4n', link: { base: 'https://beta.playkeeper.me:8443', friendly: true } } })
    const text = await render(<TeamSection />)
    for (const line of ['Up to 1 server with 4 GB', 'cambam', 'Creator invite not used yet · runs out in 6 days', 'Up to 1 server with 8 GB']) expect(text).toContain(line)
    await click(button('More for alex'))
    expect(page()).toContain('Remove from team')
    expect(page()).not.toContain('Change servers')
    await click(button('Invite a creator'))
    expect(page()).toContain('They see only their own servers, and only you see this link.')
    await typeInto('input[placeholder="Their handle, like alex"]', 'mogswamp')
    await click(button('Create link'))
    expect(client.post).toHaveBeenCalledWith('/api/team/invites', { role: 'admin', servers: {}, label: 'mogswamp', allowance: { servers: 1, memoryMB: 4096 } })
    expect(document.querySelector<HTMLInputElement>('input[readonly]')?.value).toBe('https://beta.playkeeper.me:8443/join/Qm7xK2pLw9RtVb4n')
  })

  it('shows each creator’s disk, and how much their servers take once it’s counted', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false },
        { id: 4, username: 'alex', owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, addedAt: hoursAgo(2), canEdit: true, allowance: { servers: 1, memoryMB: 4096 }, diskUsedBytes: 12 * 1024 ** 3 },
        { id: 5, username: 'sam', owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, addedAt: hoursAgo(3), canEdit: true, allowance: { servers: 2, memoryMB: 8192, diskGB: 100 } },
      ],
      invites: [],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    const text = await render(<TeamSection />)
    expect(text).toContain('12 GB of 30 GB of disk used')
    expect(text).toContain('100 GB of disk')
    expect(text).not.toContain('100 GB of disk used')
  })

  it('shows a customer as one, signing in with Whop rather than two-factor sign-in here', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false },
        { id: 6, username: 'siya-2', owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, addedAt: hoursAgo(1), canEdit: true, allowance: { servers: 1, memoryMB: 4096 }, customer: 'whop', handle: 'Siya' },
      ],
      invites: [],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    const text = await render(<TeamSection />)
    expect(text).toContain('Customer')
    expect(text).toContain('signs in with Whop as Siya')
    expect(text).not.toContain('two-factor on')
  })

  it('shows a customer’s store and suspension, and lets the owner suspend them with a reason or lift it', async () => {
    const customer = { owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, canEdit: false, allowance: { servers: 1, memoryMB: 4096 }, customer: 'whop', store: 'biz_other', storeName: 'Other Hosting', canSuspend: true } as const
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [
        { id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false },
        { ...customer, id: 6, username: 'alexplays', handle: 'alexplays', addedAt: hoursAgo(1), customerState: 'active' },
        { ...customer, id: 7, username: 'samcrafts', handle: 'samcrafts', addedAt: hoursAgo(2), customerState: 'suspended', suspendedSelf: true, suspendReason: 'griefing' },
      ],
      invites: [],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    answerPosts({})
    const text = await render(<TeamSection />)
    expect(text).toContain('signs in with Whop as alexplays · 30 GB of disk · bought from Other Hosting')
    expect(text).toContain('bought from Other Hosting · suspended: griefing')
    const item = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent === label)
    await click(button('More for alexplays'))
    await act(async () => item('Suspend')?.click())
    await act(async () => {})
    expect(page()).toContain('Suspend alexplays?')
    expect(button('Suspend alexplays').disabled).toBe(true)
    await typeInto('input[placeholder="Why, for the audit log"]', ' a DDoS from their server ')
    await act(async () => button('Suspend alexplays').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    // internal/panel's TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt posts this body.
    expect(client.post).toHaveBeenCalledWith('/api/customers/6/suspension', { reason: 'a DDoS from their server' })
    await click(button('More for samcrafts'))
    await act(async () => item('Lift suspension')?.click())
    await act(async () => {})
    expect(page()).toContain('Lift samcrafts’s suspension?')
    await act(async () => button('Lift suspension').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(client.del).toHaveBeenCalledWith('/api/customers/7/suspension')
  })

  const deletable = { owner: false, you: false, role: 'admin', servers: {}, twoFactor: true, canEdit: false, allowance: { servers: 1, memoryMB: 4096 }, customer: 'whop', store: 'biz_other', storeName: 'Other Hosting', canSuspend: true, canDelete: true } as const
  const deletingTeam = (members: TeamMember[]): TeamResponse => ({
    projectId: 'p2345abcde',
    project: 'My servers',
    members: [{ id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false }, ...members],
    invites: [],
    grantableRoles: ['admin', 'moderator', 'viewer'],
    servers: [{ id: 'abcdefghjk', name: 'Survival' }],
  })

  it('lets the owner delete a customer whose plan ended once their name is typed, and says who is being deleted', async () => {
    answer({
      '/api/team': deletingTeam([
        { ...deletable, id: 6, username: 'alexplays', handle: 'alexplays', addedAt: hoursAgo(1), customerState: 'active' },
        { ...deletable, id: 8, username: 'kimbuilds', handle: 'kimbuilds', addedAt: hoursAgo(3), customerState: 'paused' },
        { ...deletable, id: 9, username: 'patmines', handle: 'patmines', addedAt: hoursAgo(4), customerState: 'paused', deleting: true },
      ]),
    })
    vi.mocked(client.api).mockResolvedValue({ deleting: true })
    const text = await render(<TeamSection />)
    expect(text).toContain('bought from Other Hosting · being deleted')
    const item = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent === label)
    await click(button('More for alexplays'))
    await act(async () => item('Delete account…')?.click())
    await act(async () => {})
    expect(page()).toContain('Delete alexplays’s account?')
    expect(page()).toContain('They still have a plan at their store. Cancel it on Whop first, then delete them.')
    await typeInto('[role="dialog"] input', 'alexplays')
    expect(button('Delete alexplays').disabled).toBe(true)
    await click('Cancel')
    await click(button('More for kimbuilds'))
    await act(async () => item('Delete account…')?.click())
    await act(async () => {})
    expect(page()).toContain('Delete kimbuilds’s account?')
    expect(page()).toContain('Their account and personal records at Other Hosting are deleted: sign-ins, messages, memberships, and their servers with every backup. They’re signed out at once.')
    expect(button('Delete kimbuilds').disabled).toBe(true)
    await typeInto('[role="dialog"] input', ' kimbuilds ')
    expect(button('Delete kimbuilds').disabled).toBe(false)
    await act(async () => button('Delete kimbuilds').closest('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    // internal/panel's TestOnlyTheOwnerDeletesACustomerWhosePlanEnded sends this body.
    expect(client.api).toHaveBeenCalledWith('DELETE', '/api/customers/8', { confirm: 'kimbuilds' })
    await click(button('More for patmines'))
    expect(item('Suspend')).toBeDefined()
    expect(item('Delete account…')).toBeUndefined()
  })

  it('leads from a customer to deleting them on phones', async () => {
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    answer({ '/api/team': deletingTeam([{ ...deletable, id: 8, username: 'kimbuilds', handle: 'kimbuilds', addedAt: hoursAgo(3), customerState: 'paused' }]) })
    await render(<TeamSection />)
    await click([...document.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.includes('kimbuilds')) ?? 'kimbuilds')
    expect(page()).toContain('Suspend kimbuilds?')
    await click('Delete account…')
    expect(page()).toContain('Delete kimbuilds’s account?')
    phone.mockRestore()
  })

  it('makes a creator invite with the dialog’s defaults, in the body the panel takes', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [{ id: 1, username: 'siya', owner: true, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: '2026-09-01T10:00:00Z', canEdit: false }],
      invites: [],
      grantableRoles: ['admin', 'moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    const invite: TeamInvite = { id: 'ti3', kind: 'member', projectId: 'p2345abcde', role: 'admin', createdBy: 1, createdAt: hoursAgo(0), expiresAt: inHours(7 * 24), maxUses: 1, uses: 0, status: 'active', canEdit: true, allowance: { servers: 1, memoryMB: 4096 } }
    answer({ '/api/team': team })
    answerPosts({ '/api/team/invites': { invite, path: '/join/Qm7xK2pLw9RtVb4n', link: { base: 'https://beta.playkeeper.me:8443', friendly: true } } })
    await render(<TeamSection />)
    await click(button('Invite a creator'))
    await click(button('Create link'))
    const [path, body] = vi.mocked(client.post).mock.calls[0] ?? []
    expect(path).toBe('/api/team/invites')
    // internal/panel's TestTheCreatorDialogsDefaultsMakeAnInvite posts this body.
    expect(JSON.stringify(body)).toBe('{"role":"admin","servers":{},"allowance":{"servers":1,"memoryMB":4096}}')
    expect(document.querySelector<HTMLInputElement>('input[readonly]')?.value).toBe('https://beta.playkeeper.me:8443/join/Qm7xK2pLw9RtVb4n')
  })

  it('offers creator invites only to the owner', async () => {
    const team: TeamResponse = {
      projectId: 'p2345abcde',
      project: 'My servers',
      members: [{ id: 2, username: 'mara', owner: false, you: true, role: 'admin', servers: { all: true }, twoFactor: true, addedAt: hoursAgo(49), canEdit: false }],
      invites: [],
      grantableRoles: ['moderator', 'viewer'],
      servers: [{ id: 'abcdefghjk', name: 'Survival' }],
    }
    answer({ '/api/team': team })
    await render(<TeamSection />, workspace({ me: member('admin', everything, { servers: { all: true }, twoFactor: true }) }))
    expect(buttons('Add a team member')).toHaveLength(1)
    expect(buttons('Invite a creator')).toHaveLength(0)
  })
})

describe('Creator invite page', () => {
  it('shows what a creator may create instead of servers', async () => {
    const preview: JoinPreview = { kind: 'member', inviter: '', role: 'admin', servers: {}, expiresAt: '2026-10-02T12:00:00Z', serverNames: [], team: 'Playkeeper beta', allowance: { servers: 1, memoryMB: 4096 } }
    answerPosts({ '/preview': preview })
    const text = await render(<JoinPage code="Qm7xK2pLw9RtVb4n" onSignedIn={() => {}} />)
    for (const line of ['Create your own Minecraft server on Playkeeper beta', 'Creator', 'You create and run your own servers here', 'Up to 1 server with 4 GB']) expect(text).toContain(line)
    expect(text).not.toContain('No servers')
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
    expect(button('Connect').title).toBe('Paste the webhook link first.')
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

  // The dashboard's agent posts the message with its own machine's servers, so the preview shows only those.
  const home: MachineView = {
    id: 'h2345abcde',
    projectId: machine.projectId,
    name: 'home-server',
    kind: 'remote',
    link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
  }
  const survival = server({ machineId: machine.id, gamePort: 25566 })
  const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id, gamePort: 25570 })
  it.each([
    { name: 'the dashboard’s servers only', servers: [survival], note: false },
    { name: 'the same and a joined machine’s server that is first online', servers: [cobblemon, survival], note: true },
  ])('previews the live status with $name', async ({ servers, note }) => {
    answer({ '/api/discord': { connected: true, webhookName: 'Playkeeper alerts', connectedAt: hoursAgo(1), alerts: [], liveStatus: true, delivery: {}, kinds } satisfies DiscordSettings })
    const text = await render(<DiscordSettingsSection />, workspace({ machines: [machine, home], servers }))
    const preview = document.querySelector('[role="img"][aria-label="How it looks in the channel"]')
    expect([...(preview?.querySelectorAll('li') ?? [])].map((li) => li.textContent?.split('Online')[0])).toEqual(['Survival'])
    expect(preview?.textContent).toContain(`Join: ${window.location.hostname}:25566`)
    expect(preview?.textContent).not.toContain('25570')
    expect(text.includes('Servers on your other machines aren’t posted yet.')).toBe(note)
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

describe('Copies somewhere else', () => {
  const sftp: OffsiteView = {
    enabled: false,
    configured: true,
    type: 'sftp',
    place: 'vault.example.net',
    sftp: { host: 'vault.example.net', port: 22, user: 'playkeeper', folder: 'backups/survival', auth: 'key' },
    sshKey: { publicKey: 'ssh-ed25519 AAAA', authorizedKey: 'restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGm4bWJpbmFyeWtleWJ5dGVzZm9yYXRlc3Q1q7Rk playkeeper-survival', fingerprint: 'SHA256:x' },
    copies: 0,
    copiesBytes: 0,
    queued: 0,
    providers: [],
  }
  const hostKey = { key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey', type: 'ssh-ed25519', fingerprint: 'SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA' }
  afterEach(() => {
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  })
  const click = async (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(label))
    if (!button) throw new Error(`no button "${label}"`)
    await act(async () => button.click())
    await act(async () => {})
  }

  it('confirms a new host key, saves it and tests again before turning copies on', async () => {
    answer({ '/offsite': sftp })
    const unknown: OffsiteTestResult = { ok: false, skew: 0, hostKey, checks: [{ step: 'connect', ok: false, msg: 'Playkeeper has not seen this host key yet.', kind: 'host_key_unknown' }] }
    const passed: OffsiteTestResult = { ok: true, skew: 0, checks: ['connect', 'folder', 'write', 'rename', 'read', 'list', 'delete'].map((step) => ({ step, ok: true, msg: '' })) }
    const tests = [unknown, passed]
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/offsite/test') ? tests.shift() : sftp)) as typeof client.post)
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(document.body.textContent).toContain('Encrypted before they leave. Copies start once the test passes.')
    await click('Test connection')
    expect(document.body.textContent).toContain('Is this really vault.example.net?')
    expect(document.body.textContent).not.toContain('A check failed')
    expect(document.body.textContent).toContain(hostKey.fingerprint)
    expect(document.body.textContent).toContain('ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub')
    await click('It matches, confirm')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite', { hostKey: hostKey.key })
    const text = document.body.textContent ?? ''
    expect(text).toContain('All checks passed')
    expect(text).toContain('Connected and signed in as playkeeper')
    expect(text).toContain('Turn on copies')
  })

  const pinned = 'SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA'
  const now = 'SHA256:Zx81bQe4Wn7cHs2LmP0vA9tYd6KfR3gJuN5oE1iXwTk'
  const stopped: OffsiteView = {
    ...sftp,
    enabled: true,
    key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, savedAt: '2026-09-24T10:05:00Z', fileName: 'playkeeper-recovery-key-survival.txt' },
    pending: { backupId: 'b1', fileName: 'b1.tar.zst', uploading: false, sent: 0, total: 1, attempts: 1, error: 'The key changed.', errorKind: 'host_key_changed', params: { fingerprint: now, pinnedFingerprint: pinned } },
  }

  it('stops copies when the host key changed and shows both fingerprints', async () => {
    answer({ '/offsite': stopped })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(document.body.textContent).toContain('Stopped')
    expect(document.body.textContent).toContain('Copies stopped: vault.example.net’s key changed')
    expect(document.body.textContent).toContain('Downloaded')
    await click('Review')
    const text = document.body.textContent ?? ''
    expect(text).toContain('vault.example.net’s key changed')
    expect(text).toContain(pinned)
    expect(text).toContain(now)
    expect(text).toContain('Check the new key')
  })

  it('keeps Test connection while copies are stopped, so the owner can check the new key', async () => {
    answer({ '/offsite': stopped })
    const changed: OffsiteTestResult = { ok: false, skew: 0, hostKey: { ...hostKey, fingerprint: now }, checks: [{ step: 'connect', ok: false, msg: 'The host key changed.', kind: 'host_key_changed', params: { pinnedFingerprint: pinned } }] }
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/offsite/test') ? changed : stopped)) as typeof client.post)
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(document.body.textContent).toContain('Copies stopped: vault.example.net’s key changed')
    await click('Test connection')
    expect(vi.mocked(client.post).mock.calls.map(([path]) => path)).toContain('/api/servers/abcdefghjk/offsite/test')
    const text = document.body.textContent ?? ''
    expect(text).toContain('vault.example.net’s key changed')
    expect(text).toContain(pinned)
    expect(text).toContain(now)
    await click('Check the new key')
    expect(document.body.textContent).toContain('Is this really vault.example.net?')
  })

  it('lets only those who may hold backup keys change where copies go or download the key', async () => {
    answer({ '/offsite': { ...sftp, enabled: true, key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, fileName: 'playkeeper-recovery-key-survival.txt' } } })
    const download = () => [...document.querySelectorAll('button')].find((b) => b.textContent === 'Download')
    // An admin without two-factor sign-in has a moderator's rights.
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />, workspace({ me: member('admin', moderatorCan) }))
    expect(document.body.textContent).toContain('Only the owner, or an admin with two-factor sign-in, can change where copies go.')
    expect(document.querySelector<HTMLInputElement>('#offsite-host')?.disabled).toBe(true)
    expect(download()?.disabled).toBe(true)
    expect(download()?.title).toBe('Only the owner, or an admin with two-factor sign-in, can hold the recovery key.')
    const test = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Test connection')
    expect(test?.title).toBe('Only the owner, or an admin with two-factor sign-in, can change where copies go.')

    const keys: Action[] = ['backups.copies.manage', 'backups.recovery_key']
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />, workspace({ me: member('admin', [...moderatorCan, 'backups.restore', 'servers.manage', ...keys], { twoFactor: true }) }))
    expect(document.body.textContent).not.toContain('Only the owner')
    expect(document.querySelector<HTMLInputElement>('#offsite-host')?.disabled).toBe(false)
    expect(download()?.disabled).toBe(false)
  })

  describe('a new place while copies are recorded at the old one', () => {
    const on: OffsiteView = { ...sftp, enabled: true, copies: 2, key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, savedAt: '2026-09-24T10:05:00Z', fileName: 'playkeeper-recovery-key-survival.txt' } }
    const copy = (backupId: string, createdAt: string, sizeBytes: number, onHost: boolean): OffsiteCopy => ({
      backupId,
      kind: 'scheduled',
      createdAt,
      fileName: `${backupId}.tar.gz`,
      name: `${backupId}.tar.gz.age`,
      sizeBytes,
      copySizeBytes: sizeBytes + 200,
      minecraftVersion: '26.1.2',
      levelName: 'world',
      copiedAt: createdAt,
      checked: 'size',
      onHost,
    })
    const passed: OffsiteTestResult = { ok: true, skew: 0, checks: ['connect', 'folder', 'write', 'rename', 'read', 'list', 'delete'].map((step) => ({ step, ok: true, msg: '' })) }
    const refusal = new client.ApiError(409, {
      code: 'conflict',
      error: 'Changing where copies go forgets the 2 copies on vault.example.net.',
      reason: 'copies_recorded',
      params: { place: 'vault.example.net', copies: 2, onlyThere: 1 },
    })
    let saves: Record<string, unknown>[] = []
    const dialog = () => document.querySelector('[role="dialog"]')?.textContent ?? ''

    async function saveNewFolder() {
      saves = []
      answer({ '/offsite/copies': { copies: [copy('b2', '2026-09-24T10:19:00Z', 7.3 * 2 ** 20, true), copy('b1', '2026-09-22T10:07:00Z', 4.5 * 2 ** 20, false)] }, '/offsite': on })
      answerPosts({
        '/offsite/test': passed,
        '/offsite': (body: Record<string, unknown>) => {
          saves.push(body)
          return body.forgetCopies ? { ...on, sftp: { ...sftp.sftp, folder: 'copies' }, copies: 0 } : refusal
        },
      })
      await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
      await typeInto('#offsite-folder', 'copies')
      await click('Test connection')
      await click('Save changes')
    }

    it('asks first, naming the backups whose only copy is there, and saves once agreed', async () => {
      await saveNewFolder()
      expect(dialog()).toContain('Forget the copies on vault.example.net?')
      expect(dialog()).toContain('The 2 copies stay on vault.example.net, but Playkeeper stops listing them once copies go somewhere else.')
      expect(dialog()).toContain('This backup has no other copy')
      expect(dialog()).toContain('4.5 MB')
      expect(dialog()).not.toContain('7.3 MB')
      expect(saves).toHaveLength(1)
      await click('Change where copies go')
      expect(saves).toHaveLength(2)
      expect(saves[1]).toMatchObject({ forgetCopies: true, config: { type: 'sftp', sftp: { folder: 'copies' } } })
      expect(dialog()).toBe('')
      expect(page()).not.toContain(refusal.message)
    })

    it('changes nothing when the user keeps the copies', async () => {
      await saveNewFolder()
      await click('Cancel')
      expect(dialog()).toBe('')
      expect(saves).toHaveLength(1)
      expect(document.querySelector<HTMLInputElement>('#offsite-folder')?.value).toBe('copies')
      expect(page()).toContain('Save changes')
    })
  })

  it('calls the last copy the first only when it is', async () => {
    const last: OffsiteCopy = { backupId: 'b9', kind: 'scheduled', createdAt: '2026-09-24T10:50:00Z', fileName: 'b9.tar.gz', name: 'b9.tar.gz.age', sizeBytes: 4.5 * 2 ** 20, copySizeBytes: 4.6 * 2 ** 20, minecraftVersion: '26.1.2', levelName: 'world', copiedAt: '2026-09-24T10:50:00Z', checked: 'sha256', onHost: true }
    const pruned: OffsiteView = { ...sftp, enabled: true, copies: 1, lastCopy: last }
    answer({ '/offsite': pruned })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(page()).toContain('Last copy:')
    expect(page()).not.toContain('First copy:')
    answer({ '/offsite': { ...pruned, firstCopy: true } })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
    expect(page()).toContain('First copy:')
  })

  describe('the recovery key file after copies go to another folder', () => {
    const key = { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, savedAt: '2026-09-24T10:05:00Z', fileName: 'playkeeper-recovery-key-survival.txt', folder: 'backups/survival' }
    const on: OffsiteView = { ...sftp, enabled: true, key }
    const moved: OffsiteView = { ...on, sftp: { host: 'vault.example.net', port: 22, user: 'playkeeper', folder: 'copies', auth: 'key' }, key: { ...key, folder: 'copies', stale: true, savedFolder: 'backups/survival' } }
    const passed: OffsiteTestResult = { ok: true, skew: 0, checks: ['connect', 'folder', 'write', 'rename', 'read', 'list', 'delete'].map((step) => ({ step, ok: true, msg: '' })) }
    const dialog = () => document.querySelector('[role="dialog"]')?.textContent ?? ''
    const hint = 'The file you have says copies are in backups/survival. They go to copies now.'

    async function saveChange(field: string, value: string, saved: OffsiteView) {
      answer({ '/offsite': on })
      answerPosts({
        '/offsite/test': passed,
        '/offsite': () => {
          answer({ '/offsite': saved })
          return saved
        },
      })
      await render(<CopiesCard server={server()} onChangeRules={() => {}} />)
      await typeInto(field, value)
      await click('Test connection')
      await click('Save changes')
    }

    it('asks for the file again once saved, naming both folders, and keeps saying so until it’s downloaded', async () => {
      await saveChange('#offsite-folder', 'copies', moved)
      expect(dialog()).toContain('Download the recovery key again')
      expect(dialog()).toContain(hint)
      expect(dialog()).toContain('Same key, new folder · keep it private')
      await click('Later')
      expect(dialog()).toBe('')
      expect(page()).toContain('Recovery key file out of date')
      expect(page()).toContain(hint)
      expect(page()).not.toContain('Downloaded')
    })

    it('doesn’t ask when the file still names the folder copies go to', async () => {
      await saveChange('#offsite-user', 'backup', on)
      expect(dialog()).toBe('')
      expect(page()).not.toContain('Recovery key file out of date')
      expect(page()).toContain('Downloaded')
    })
  })
})

describe('World backups with copies', () => {
  const b2: OffsiteView = {
    enabled: true,
    configured: true,
    type: 's3',
    place: 'Backblaze B2',
    copies: 2,
    copiesBytes: 0,
    queued: 0,
    providers: [],
    pending: { backupId: 'b3', fileName: 'survival-3.tar.zst', uploading: true, sent: 62, total: 100, attempts: 1 },
  }
  const backup = (id: string, createdAt: string): Backup => ({ id, serverId: 'abcdefghjk', kind: 'scheduled', createdAt, fileName: `survival-${id}.tar.zst`, sizeBytes: 311e6, sha256: 'a'.repeat(64), location: '', verified: true, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 2110, createdBy: 'playkeeper' })
  const copy = (backupId: string, createdAt: string, onHost: boolean): OffsiteCopy => ({ backupId, kind: 'scheduled', createdAt, fileName: `survival-${backupId}.tar.zst`, name: `survival-${backupId}.tar.zst.age`, sizeBytes: 305e6, copySizeBytes: 318e6, minecraftVersion: '26.1.2', levelName: 'world', copiedAt: createdAt, checked: 'sha256', onHost })
  const started: Operation = { id: 'op-copy', kind: 'offsite-restore', status: 'running', phase: 'downloading', actor: 'siya', startedAt: '2026-09-25T18:50:00Z', detail: { name: 'survival-b1.tar.zst.age' } }
  const rerender = async (s: ServerStatus) => {
    await act(async () => root?.render(<WorkspaceContext.Provider value={workspace()}>{<WorldPage server={s} />}</WorkspaceContext.Provider>))
    await act(async () => {})
  }
  afterEach(() => {
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  })

  async function restoreOldCopy() {
    answer({ '/offsite/copies': { copies: [copy('b2', '2026-09-25T12:47:00Z', true), { ...copy('b1', '2026-09-20T18:47:00Z', false), removed: 'rules' }] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z'), backup('b2', '2026-09-25T12:47:00Z')] })
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/offsite/restore') ? started : {})) as typeof client.post)
    await render(<WorldPage server={server()} />)
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Restore…')
    if (!button) throw new Error('no Restore… button')
    await act(async () => button.click())
    await act(async () => {})
  }

  it('says where each backup is and fetches one that is only in the copies', async () => {
    await restoreOldCopy()
    const text = document.body.textContent ?? ''
    expect(text).toContain('Stored here and on Backblaze B2.')
    expect(text).toContain('Backup rules')
    expect(text).toContain('Here · copying')
    expect(text).toContain('62% to Backblaze B2')
    expect(text).toContain('Here and on Backblaze B2')
    expect(text).toContain('Only on Backblaze B2')
    expect(text).toContain('Removed here by your rules')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite/restore', { name: 'survival-b1.tar.zst.age' })
    expect(text).toContain('The encrypted copy from Backblaze B2')
    expect(text).toContain('You can close this. Progress stays in the top bar.')

    await rerender(server({ lastOperation: { ...started, status: 'succeeded', phase: 'checking', detail: { name: 'survival-b1.tar.zst.age', restoreId: 'r1' } } }))
    expect(document.body.textContent).toContain('Decrypted and checked')
    expect(vi.mocked(client.get)).toHaveBeenCalledWith('/api/machines/m2345abcde/restore/r1')
  })

  it('says the rules removed a backup here only when they did', async () => {
    const copies = [{ ...copy('b1', '2026-09-20T18:47:00Z', false), removed: 'rules' }, { ...copy('b0', '2026-09-19T18:47:00Z', false), removed: 'person', removedBy: 'mara_k' }, copy('a9', '2026-09-18T18:47:00Z', false)]
    answer({ '/offsite/copies': { copies }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await render(<WorldPage server={server()} />)
    const rows = [...document.querySelectorAll('tr')].filter((r) => r.textContent?.includes('Only on Backblaze B2')).map((r) => r.textContent ?? '')
    expect(rows).toHaveLength(3)
    expect(rows[0]).toContain('Removed here by your rules')
    expect(rows[1]).toContain('Deleted here by mara_k')
    expect(rows[1]).not.toContain('your rules')
    expect(rows[2]).not.toContain('your rules')
    expect(rows[2]).not.toContain('Deleted here')
  })

  it('says why a copy could not be fetched', async () => {
    await restoreOldCopy()
    await rerender(server({ lastOperation: { ...started, status: 'failed', error: 'That copy is no longer there.', hint: 'Restore another copy.' } }))
    const text = document.body.textContent ?? ''
    expect(text).toContain('The copy couldn’t be restored')
    expect(text).toContain('That copy is no longer there.')
    expect(text).toContain('Restore another copy.')
    expect([...document.querySelectorAll('[role="dialog"] button')].map((b) => b.textContent)).toContain('Close')
  })

  it('cancels a restore from a copy while it runs and says nothing was changed', async () => {
    const toast = vi.spyOn(toastManager, 'add')
    await restoreOldCopy()
    const buttons = () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')]
    expect(buttons().map((b) => b.textContent)).not.toContain('Close')
    await act(async () => buttons().find((b) => b.textContent === 'Cancel')?.click())
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite/restore/cancel', { operationId: 'op-copy' })

    await rerender(server({ lastOperation: { ...started, status: 'cancelled', finishedAt: '2026-09-25T18:51:00Z' } }))
    expect(document.body.textContent).not.toContain('The encrypted copy from Backblaze B2')
    expect(toast).toHaveBeenCalledWith({ title: 'Restore cancelled', description: 'Nothing was changed. What was already downloaded is deleted.' })
    toast.mockRestore()
  })

  async function openCopyMenu(onlyThere: OffsiteCopy, ws = workspace()) {
    answer({ '/offsite/copies': { copies: [onlyThere] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await render(<WorldPage server={server()} />, ws)
    const row = [...document.querySelectorAll('tr')].find((r) => r.textContent?.includes('Only on Backblaze B2'))
    const trigger = row?.querySelector<HTMLButtonElement>('button[aria-label^="Actions for the backup from"]')
    if (!row || !trigger) throw new Error('no menu on the row kept only on Backblaze B2')
    await act(async () => trigger.click())
    await act(async () => {})
    return { row, item: (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(label)) }
  }

  it('gives a backup kept only somewhere else a menu to restore, check, copy its checksum or delete it', async () => {
    const onlyThere = { ...copy('b1', '2026-09-20T18:47:00Z', false), sha256: 'c'.repeat(58) + 'd00d42', checkError: 'The copy doesn’t match the backup it was made from.' }
    const { row, item } = await openCopyMenu(onlyThere)
    const failed = [...row.querySelectorAll('td')].find((td) => td.textContent === 'Failed check')
    expect(failed?.title).toBe('The copy doesn’t match the backup it was made from.')
    expect([...document.querySelectorAll('[role="menuitem"]')].map((el) => el.textContent)).toEqual(['Restore this backup…Your current world is saved first', 'Check it again', 'Copy checksumSHA-256 cccccc…0d42', 'Delete backup'])

    await act(async () => item('Check it again')?.click())
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite/copies/survival-b1.tar.zst.age/check')

    const trigger = row.querySelector<HTMLButtonElement>('button[aria-label^="Actions for the backup from"]')
    await act(async () => trigger?.click())
    await act(async () => item('Delete backup')?.click())
    await act(async () => {})
    expect(document.body.textContent).toContain('is deleted from Backblaze B2. It isn’t on this VPS any more, so it can’t be brought back.')
    const confirm = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent === 'Delete backup')
    await act(async () => confirm?.click())
    expect(vi.mocked(client.del)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite/copies/survival-b1.tar.zst.age')
  })

  it('cancels a check of a backup kept only somewhere else from its menu while the check runs', async () => {
    answer({ '/offsite/copies': { copies: [copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    const checking: Operation = { ...started, id: 'op-check', kind: 'offsite-check' }
    await render(<WorldPage server={server({ operation: checking })} />)
    const row = [...document.querySelectorAll('tr')].find((r) => r.textContent?.includes('Only on Backblaze B2'))
    const trigger = row?.querySelector<HTMLButtonElement>('button[aria-label^="Actions for the backup from"]')
    if (!trigger) throw new Error('no menu on the row kept only on Backblaze B2')
    await act(async () => trigger.click())
    await act(async () => {})
    const item = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(label))
    expect(item('Check it again')).toBeUndefined()
    await act(async () => item('Cancel the check')?.click())
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/servers/abcdefghjk/offsite/check/cancel', { operationId: 'op-check' })
  })

  it('shows each role only the backup controls it may use', async () => {
    const viewer = member('viewer', ['view', 'account.manage'])
    const { row, item } = await openCopyMenu(copy('b1', '2026-09-20T18:47:00Z', false), workspace({ me: viewer }))
    expect(document.body.textContent).not.toContain('Back up now')
    expect(row.textContent).not.toContain('Restore…')
    expect(item('Restore this backup')).toBeUndefined()
    expect(item('Check it again')).toBeUndefined()
    expect(item('Delete backup')?.getAttribute('aria-disabled')).toBe('true')

    const estimate: RetentionEstimate = { where: 'on-host', rows: [], count: 7, bytes: 2e9, summary: { code: 'keeps', text: '' } }
    const rules: BackupRulesView = { automatic: { enabled: true, everyHours: 6, onlyIfPlayed: true }, rules: { onHost: { daily: 7 }, offSite: { daily: 7 } }, custom: false, describe: [], onHost: estimate, offSite: { ...estimate, where: 'off-site' }, limits: { hours: 48, last: 50, daily: 31, weekly: 26, monthly: 24 } }
    answer({ '/backup-rules': rules })
    const text = await render(<BackupRulesPage server={server()} />, workspace({ me: member('moderator', moderatorCan) }))
    expect(text).toContain('Only admins can change the backup rules.')
    expect(document.querySelector('[role="switch"][aria-label="Automatic backups"]')?.hasAttribute('data-disabled')).toBe(true)
    expect(text).not.toContain('Change rules')
    await render(<BackupRulesPage server={server()} />)
    expect(document.body.textContent).toContain('Change rules')
    expect(document.querySelector('[role="switch"][aria-label="Automatic backups"]')?.hasAttribute('data-disabled')).toBe(false)

    const asleep = server({ phase: 'asleep', desired: 'sleeping', sleep: { enabled: true, idleMinutes: 15, listening: true } })
    expect(await render(<AsleepCard server={asleep} />, workspace({ me: viewer }))).not.toContain('Wake up now')
    const moderator = await render(<AsleepCard server={asleep} />, workspace({ me: member('moderator', moderatorCan) }))
    expect(moderator).toContain('Wake up now')
    expect(moderator).not.toContain('Sleep settings')
  })

  it('says why a copy’s checksum or deleting it is out of reach', async () => {
    const { item } = await openCopyMenu(copy('b1', '2026-09-20T18:47:00Z', false), workspace({ me: member('admin', [...moderatorCan, 'backups.restore', 'servers.manage']) }))
    for (const [label, reason] of [
      ['Copy checksum', 'This copy’s checksum wasn’t recorded.'],
      ['Delete backup', 'Only the owner, or an admin with two-factor sign-in, can delete copies kept on Backblaze B2.'],
    ] as const) {
      expect(item(label)?.getAttribute('aria-disabled')).toBe('true')
      expect(item(label)?.title).toBe(reason)
    }
    expect(item('Check it again')?.getAttribute('aria-disabled')).not.toBe('true')
  })

  it('stays closed once the restore that follows takes over the status', async () => {
    await restoreOldCopy()
    const preview: RestorePreview = { id: 'r1', serverId: 'abcdefghjk', source: 'copy survival-b1.tar.zst.age', receivedAt: '2026-09-25T18:51:00Z', sizeBytes: 305e6, sha256: 'b'.repeat(64), compatible: true, problems: [], warnings: [], currentWorld: { exists: true, levelName: 'world', sizeBytes: 311e6 }, willCreateRollback: true, needsEula: false, memoryMB: 2048, confirmPhrase: 'replace world', steps: [], notRestored: [] }
    answer({ '/restore/r1': preview, '/offsite/copies': { copies: [copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await rerender(server({ lastOperation: { ...started, status: 'succeeded', phase: 'checking', detail: { name: 'survival-b1.tar.zst.age', restoreId: 'r1' } } }))
    expect(document.body.textContent).not.toContain('The encrypted copy from Backblaze B2')
    await rerender(server({ lastOperation: { id: 'op-restore', kind: 'restore', status: 'succeeded', phase: 'starting', actor: 'siya', startedAt: '2026-09-25T18:52:00Z' } }))
    expect(document.body.textContent).not.toContain('The encrypted copy from Backblaze B2')
  })

  it('won’t restore a copy while a restore left the world folder missing, and says why', async () => {
    const worldMissing = { previous: '/var/lib/playkeeper/servers/abcdefghjk/data.replaced-20260926-103028', dataDir: '/var/lib/playkeeper/servers/abcdefghjk/data', setAsideAt: '2026-09-26T10:30:28Z' }
    answer({ '/offsite/copies': { copies: [copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing })} />)
    const restore = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Restore…')
    expect(restore?.disabled).toBe(true)
    expect(restore?.title).toBe('Its world folder is missing. Move the previous world back first.')
  })

  const onPhone = () => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))

  it('won’t restore a copy from a phone while a restore left the world folder missing, and says why', async () => {
    const phone = onPhone()
    const worldMissing = { previous: '/var/lib/playkeeper/servers/abcdefghjk/data.replaced-20260926-103028', dataDir: '/var/lib/playkeeper/servers/abcdefghjk/data', setAsideAt: '2026-09-26T10:30:28Z' }
    answer({ '/offsite/copies': { copies: [copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing })} />)
    const restore = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Restore')
    expect(restore?.disabled).toBe(true)
    expect(restore?.title).toBe('Its world folder is missing. Move the previous world back first.')
    phone.mockRestore()
  })

  it('stops offering restores in the phone’s sheet once a restore isn’t finished, and says why', async () => {
    const why = 'A restore isn’t finished. Playkeeper finishes it once the server is stopped.'
    const phone = onPhone()
    answer({ '/offsite/copies': { copies: [copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z')] })
    await render(<WorldPage server={server({ phase: 'stopped' })} />)
    await act(async () => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Restore a world')?.click())
    await act(async () => {})
    await rerender(server({ phase: 'stopped', restoreUnsettled: {} }))
    const sheet = document.querySelector('[role="dialog"]')
    const choose = [...(sheet?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === 'choose a file')
    expect(choose?.disabled).toBe(true)
    expect(choose?.title).toBe(why)
    const rows = [...(sheet?.querySelectorAll<HTMLButtonElement>('ul li button') ?? [])]
    expect(rows).toHaveLength(2)
    expect(rows.map((b) => [b.disabled, b.title])).toEqual([
      [true, why],
      [true, why],
    ])
    phone.mockRestore()
  })
})

describe('Restore from a recovery key', () => {
  const keyText = ['# Playkeeper recovery key for Survival', '#', '# Made 2026-09-24 18:47 UTC. The newest key comes first.', '', '# public key: age1new', 'AGE-SECRET-KEY-1NEWKEY', '', '# public key: age1old', 'AGE-SECRET-KEY-1OLDKEY', ''].join('\n')
  const copies = Array.from({ length: 5 }, (_, i) => ({ name: `survival-2026092${5 - i}-184700-abcd.tar.gz.age`, sizeBytes: (318 - i) * 2 ** 20, createdAt: `2026-09-2${5 - i}T18:47:00Z` }))
  afterEach(() => {
    vi.mocked(client.post).mockReset()
    vi.mocked(client.post).mockImplementation(() => Promise.resolve({}))
  })
  const typeInto = async (id: string, value: string) => {
    const el = document.getElementById(id) as HTMLInputElement
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    await act(async () => {
      setValue?.call(el, value)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  const click = async (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(label))
    if (!button) throw new Error(`no button "${label}"`)
    await act(async () => button.click())
    await act(async () => {})
  }
  async function pickKeyFile() {
    const input = document.querySelector<HTMLInputElement>('input[type=file]')
    if (!input) throw new Error('no file input')
    Object.defineProperty(input, 'files', { configurable: true, value: [new File([keyText], 'playkeeper-recovery-key-survival.txt', { type: 'text/plain' })] })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    await act(async () => {})
  }

  it('reads the key file, finds the copies and fetches the one picked', async () => {
    vi.mocked(client.post).mockImplementation(((path: string) =>
      Promise.resolve(path.endsWith('/recover/restore') ? { id: 'op-recover', kind: 'offsite-recover', status: 'running', phase: 'listing', actor: 'siya', startedAt: '2026-09-25T19:00:00Z', detail: {} } : { server: 'Survival', keys: 2, place: 'Backblaze B2', copies })) as typeof client.post)
    await render(<RecoverPage />, workspace({ servers: [] }))
    expect(document.body.textContent).toContain('Choose the recovery key file')
    await pickKeyFile()
    expect(document.body.textContent).toContain('playkeeper-recovery-key-survival.txt')
    expect(document.body.textContent).toContain('Survival · 2 keys')
    await typeInto('recover-endpoint', 's3.eu-central-003.backblazeb2.com')
    await typeInto('recover-bucket', 'siya-minecraft')
    await typeInto('recover-keyid', '003a8f91c2')
    await typeInto('recover-secret', 'not-a-real-secret')
    await click('Find the copies')
    expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/machines/m2345abcde/offsite/recover', {
      recoveryKey: keyText,
      config: { type: 's3', s3: { endpoint: 's3.eu-central-003.backblazeb2.com', bucket: 'siya-minecraft', accessKeyId: '003a8f91c2' } },
      secretKey: 'not-a-real-secret',
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('Connected · 5 copies of Survival found')
    expect(text).toContain('Show all 5')
    expect(text).toContain('318 MB · encrypted')
    await click('Next: check what’s inside')
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/machines/m2345abcde/offsite/recover/restore', expect.objectContaining({ recoveryKey: keyText, name: copies[0]?.name }))
    expect(document.body.textContent).toContain('Keep this page open until it’s done.')
  })

  it('asks before trusting an SFTP host key it has not seen', async () => {
    const refusal = new client.ApiError(400, { error: 'Playkeeper has not seen this host key yet.', code: 'invalid', reason: 'host_key_unknown', params: { hostKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey', keyType: 'ssh-ed25519', fingerprint: 'SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA' } })
    const answers: (() => Promise<unknown>)[] = [() => Promise.reject(refusal), () => Promise.resolve({ server: 'Survival', keys: 2, place: 'vault.example.net', copies })]
    vi.mocked(client.post).mockImplementation((() => answers.shift()?.()) as typeof client.post)
    await render(<RecoverPage />, workspace({ servers: [] }))
    await pickKeyFile()
    const sftp = [...document.querySelectorAll('label')].find((l) => l.textContent?.includes('Another machine over SFTP'))
    await act(async () => sftp?.click())
    await typeInto('recover-host', 'vault.example.net')
    await typeInto('recover-user', 'playkeeper')
    await typeInto('recover-password', 'not-a-real-password')
    await click('Find the copies')
    expect(document.body.textContent).toContain('Is this really vault.example.net?')
    expect(document.body.textContent).toContain('SHA256:q3Jd8m0tLr4w9KbXo2V7yZ1cN5sF6hPaE8gT0uRkIiA')
    await click('It matches, confirm')
    expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/machines/m2345abcde/offsite/recover', expect.objectContaining({ password: 'not-a-real-password', hostKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKey' }))
    expect(document.body.textContent).toContain('Connected · 5 copies of Survival found')
  })

  it('is for those who may hold backup keys and use every server', async () => {
    const keys: Action[] = ['backups.copies.manage', 'backups.recovery_key']
    const text = await render(<RecoverPage />, workspace({ servers: [], me: member('admin', [...moderatorCan, ...keys], { twoFactor: true }) }))
    expect(text).toContain('Only the owner, or an admin of every server with two-factor sign-in, can restore from a recovery key.')
    expect(document.querySelector('input[type=file]')).toBeNull()
    await render(<RecoverPage />, workspace({ servers: [], me: member('admin', [...moderatorCan, ...keys, 'backups.recover'], { twoFactor: true, servers: { all: true } }) }))
    expect(document.querySelector('input[type=file]')).not.toBeNull()
  })
})

describe('Backups and copies on a joined machine', () => {
  const attic: MachineView = {
    id: 'a2345abcde',
    projectId: machine.projectId,
    name: 'attic',
    kind: 'remote',
    link: { machineId: 'a2345abcde', name: 'attic', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), address: '203.0.113.7:48211', problems: [] },
    live: { ...machine.live!, hostname: 'attic' },
  }
  const onAttic = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: attic.id })
  const joined = workspace({ machines: [machine, attic], servers: [server({ machineId: machine.id }), onAttic] })
  const estimate = (where: RetentionEstimate['where']): RetentionEstimate => ({ where, rows: [], count: 12, bytes: 3 * 2 ** 30, summary: { code: 'kept', text: 'Keeps 12 backups.' } })
  const rules: BackupRulesView = {
    automatic: { enabled: true, everyHours: 6, onlyIfPlayed: false },
    rules: { onHost: { last: 12 }, offSite: {} },
    custom: false,
    describe: [],
    onHost: estimate('on-host'),
    offSite: estimate('off-site'),
    limits: { hours: 168, last: 100, daily: 60, weekly: 104, monthly: 120 },
  }

  it('says the backups are on the machine that runs the server', async () => {
    answer({ '/backup-rules': rules })
    const text = await render(<BackupRulesPage server={onAttic} />, joined)
    expect(text).toContain('About 12 backups · roughly 3 GB on attic')
    expect(text).not.toContain('my-vps')
  })

  it('reads a copy it fetched back from the machine that runs the server', async () => {
    const started: Operation = { id: 'op-copy', serverId: onAttic.id, kind: 'offsite-restore', status: 'running', phase: 'downloading', actor: 'siya', startedAt: '2026-09-25T18:50:00Z', detail: { name: 'attic-b1.tar.zst.age' } }
    answer({ '/offsite/copies': { copies: [] }, '/offsite': { enabled: true, configured: true, type: 's3', place: 'Backblaze B2', copies: 0, copiesBytes: 0, queued: 0, providers: [] }, '/backups': [] })
    await render(<WorldPage server={{ ...onAttic, operation: started }} />, joined)
    await act(async () => root?.render(<WorkspaceContext.Provider value={joined}>{<WorldPage server={{ ...onAttic, lastOperation: { ...started, status: 'succeeded', detail: { name: 'attic-b1.tar.zst.age', restoreId: 'r1' } } }} />}</WorkspaceContext.Provider>))
    await act(async () => {})
    expect(vi.mocked(client.get)).toHaveBeenCalledWith(`/api/machines/${attic.id}/restore/r1`)
    expect(vi.mocked(client.get)).not.toHaveBeenCalledWith(`/api/machines/${machine.id}/restore/r1`)
  })
})

describe('Machines and AI agents', () => {
  it('says a server’s controls wait for its machine while that machine is away', async () => {
    const away: MachineView = {
      id: 'a2345abcde',
      projectId: machine.projectId,
      name: 'attic',
      kind: 'remote',
      link: { machineId: 'a2345abcde', name: 'attic', fingerprint: 'X'.repeat(26), state: 'offline', lastSeen: new Date(Date.now() - 600_000).toISOString(), problems: [] },
    }
    const attic = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: away.id, lastKnownAt: new Date().toISOString() })
    const text = await render(<ServerPage slug="attic" tab="overview" />, workspace({ machines: [machine, away], servers: [server({ machineId: machine.id }), attic] }))
    expect(text).toContain('attic hasn’t called in for 10 minutes')
    const restart = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Restart'))
    expect(restart?.disabled).toBe(true)
    expect(restart?.title).toBe('Can’t reach attic')
    for (const tab of ['console', 'players', 'world', 'settings'] as const) {
      vi.mocked(client.get).mockClear()
      expect(await render(<ServerPage slug="attic" tab={tab} />, workspace({ machines: [machine, away], servers: [server({ machineId: machine.id }), attic] }))).toContain('attic hasn’t called in for 10 minutes')
      expect(vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes(attic.id)), `${tab} asks the away machine`).toEqual([])
    }
  })

  it('shows the not-answering view on every tab while a joined machine’s agent doesn’t answer, and asks it nothing', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      error: { error: 'The agent on home-server isn’t answering.', code: 'agent_unavailable' },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id, lastKnownAt: new Date(Date.now() - 120_000).toISOString() })
    const ws = workspace({ machines: [machine, home], servers: [server({ machineId: machine.id }), cobblemon] })
    const asked = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes(cobblemon.id) || p.includes(home.id))
    for (const tab of ['overview', 'console', 'players', 'world', 'settings'] as const) {
      vi.mocked(client.get).mockClear()
      const text = await render(<ServerPage slug="cobblemon" tab={tab} />, ws)
      expect(text, tab).toContain('Playkeeper can’t see your servers right now')
      expect(text, tab).toContain('Fix it on home-server')
      expect(asked(), `${tab} asks home-server`).toEqual([])
    }
    vi.mocked(client.get).mockClear()
    await render(<HomePage />, ws)
    expect(asked(), 'Home asks home-server for its activity').toEqual([])
    // Home grouped by machine shows no activity, so it asks no machine for it.
    expect(vi.mocked(client.get).mock.calls.some(([p]) => String(p).includes('/activity'))).toBe(false)
  })

  it('asks a joined machine, not the dashboard’s, about the servers it runs', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'home-server', memoryTotalMB: 32768 },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id })
    const ws = workspace({ machines: [machine, home], servers: [server({ machineId: machine.id }), cobblemon] })
    const asked = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p))
    vi.mocked(client.get).mockClear()
    await render(<ServerSettingsPage server={cobblemon} />, ws)
    await render(<Overview server={{ ...cobblemon, phase: 'crashed', stoppedAt: new Date().toISOString() }} />, ws)
    expect(asked().some((p) => p.startsWith(`/api/machines/${home.id}/catalog`))).toBe(true)
    expect(asked().filter((p) => p.startsWith(`/api/machines/${machine.id}/`))).toEqual([])
  })

  it('joins a joined machine’s servers at its IP and port, whatever name its agent reports', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'home-server' },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), address: '203.0.113.20', problems: [] },
    }
    const survival = server({ machineId: machine.id, joinAddress: 'survival.alex.playkeeper.me' })
    const cobblemon = server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', machineId: home.id, gamePort: 25566, joinAddress: 'cobblemon.home.playkeeper.me' })
    const ws = workspace({ machines: [machine, home], servers: [survival, cobblemon] })
    let text = await render(<HomePage />, ws)
    expect(text).toContain('survival.alex.playkeeper.me')
    expect(text).toContain('203.0.113.20:25566')
    expect(text).not.toContain('cobblemon.home.playkeeper.me')
    text = await render(<Overview server={cobblemon} />, ws)
    expect(text).toContain('203.0.113.20:25566')
    expect(text).not.toContain('cobblemon.home.playkeeper.me')
  })

  it('says why a joined machine’s server has no address yet, and never gives the dashboard’s host or a reported name', async () => {
    const attic: MachineView = {
      id: 'a2345abcde',
      projectId: machine.projectId,
      name: 'attic',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'attic' },
      link: { machineId: 'a2345abcde', name: 'attic', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const box = server({ id: 'atticsrv01', name: 'Attic', slug: 'attic', machineId: attic.id, gamePort: 25567, joinAddress: 'attic.old.playkeeper.me' })
    const ws = workspace({ machines: [machine, attic], servers: [server({ machineId: machine.id }), box] })
    const reason = 'No address yet: the dashboard hasn’t seen attic’s IP.'
    for (const node of [<HomePage key="home" />, <Overview key="overview" server={box} />]) {
      const text = await render(node, ws)
      expect(text).toContain(reason)
      expect(text).not.toContain(`${window.location.hostname}:25567`)
      expect(text).not.toContain('attic.old.playkeeper.me')
    }
    await render(<ServerPage slug="attic" tab="overview" />, ws)
    const copy = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Copy join address'))
    expect(copy?.disabled).toBe(true)
    expect(copy?.title).toBe(reason)
  })

  it('says on a joined machine’s details that its agent stopped answering, as the sidebar does', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      error: { error: 'The agent on home-server isn’t answering.', code: 'agent_unavailable' },
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), rttMs: 0.5, problems: [] },
    }
    const silent = await render(<MachineDetailsSection id={home.id} />, workspace({ machines: [machine, home] }))
    expect(silent).toContain('The agent on home-server stopped answering')
    expect(silent).toContain('Restart it on home-server: sudo systemctl restart playkeeper-agent')
    const answering = await render(<MachineDetailsSection id={home.id} />, workspace({ machines: [machine, { ...home, error: undefined, live: machine.live }] }))
    expect(answering).not.toContain('stopped answering')
  })

  describe('customers on a joined machine', () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      live: { ...machine.live!, hostname: 'home-server' },
      joinedAt: '2026-09-29T13:40:00Z',
      joinedFrom: '65.108.10.20',
      addedBy: 'siya',
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'Z287KN4CDZD0Z8A4XXJA514NKG', state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const owner = (m: MachineView, can: Action[] = [...everything, 'machines.customers']) => workspace({ machines: [machine, m], me: { ...me, access: { ...me.access, can } } })

    it('places customers on a joined machine once the owner checks it’s theirs', async () => {
      const refresh = vi.fn(async () => {})
      await render(<MachineDetailsSection id={home.id} />, { ...owner(home), refresh })
      expect(page()).toContain('home-server takes no customers')
      expect(page()).toContain('New customers go on the dashboard’s machine, and on joined machines you confirm are yours or that your Hetzner token finds in your project.')
      await click('Take customers…')
      expect(vi.mocked(client.put)).not.toHaveBeenCalled()
      const dialog = document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog).toContain('Place customers on home-server?')
      expect(dialog).toContain('Only if it’s yours: customers’ servers and worlds will run on it.')
      expect(dialog).toContain('by siya, from 65.108.10.20')
      expect(dialog).toContain('Z287 KN4C DZD0 Z8A4 XXJA 514N KG')
      expect(dialog).toContain('Playkeeper keeps servers away from home-server first.')
      const eventReads = () => vi.mocked(client.get).mock.calls.filter(([p]) => p === '/api/machines/h2345abcde/events').length
      const before = eventReads()
      await click('Take customers')
      expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/machines/h2345abcde/customers', { on: true })
      expect(refresh).toHaveBeenCalled()
      expect(eventReads()).toBeGreaterThan(before)
      expect(document.querySelector('[role="dialog"]')).toBeNull()
    })

    it('says since when a joined machine takes customers and how many are on it, and stops it', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' }, customers: 3 }
      await render(<MachineDetailsSection id={home.id} />, owner(taking))
      expect(page()).toContain('home-server takes customers')
      expect(page()).toContain('Confirmed by siya on Sep 29')
      expect(page()).toContain('3 customers are on it')
      await click('Stop taking customers')
      expect(vi.mocked(client.put)).toHaveBeenLastCalledWith('/api/machines/h2345abcde/customers', { on: false })

      await render(<MachinesSection />, owner(taking))
      expect(page()).toContain('takes customers')
      await render(<MachinesSection />, owner(taking, everything))
      expect(page()).not.toContain('takes customers')
    })

    it('says when the dashboard confirmed a joined machine itself, on finding it in the owner’s Hetzner project', async () => {
      const found = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'hetzner:fleet-1' } }
      await render(<MachineDetailsSection id={home.id} />, owner(found))
      expect(page()).toContain('home-server takes customers')
      expect(page()).toContain('Found in your Hetzner project as fleet-1 on Sep 29.')
      expect(page()).not.toContain('Confirmed by hetzner')
    })

    it('is the owner’s alone, and waits for a machine that’s away', async () => {
      await render(<MachineDetailsSection id={home.id} />, owner(home, everything))
      expect(page()).not.toContain('takes no customers')
      expect(buttons('Take customers…')).toEqual([])
      const away = { ...home, link: { ...home.link!, state: 'offline' as const, lastSeen: new Date(Date.now() - 600_000).toISOString() } }
      await render(<MachineDetailsSection id={home.id} />, owner(away))
      expect(button('Take customers…').disabled).toBe(true)
      expect(button('Take customers…').title).toBe('Can’t reach home-server')
    })

    it('lists the customers on a machine, and moves one to the machine the owner picks', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' }, customers: 2 }
      const attic: MachineView = { ...taking, id: 'a2345abcde', name: 'attic', customers: 0 }
      const cellar: MachineView = { ...home, id: 'c2345abcde', name: 'cellar' }
      answer({
        '/api/machines/h2345abcde/customers': [
          { id: 7, name: 'alex', handle: 'alex', state: 'active', planId: 'plan_plus', memoryMB: 8192, servers: 2, machineId: 'h2345abcde', here: 2 },
          { id: 8, name: 'sam', state: 'paused', memoryMB: 4096, servers: 1, machineId: 'h2345abcde', here: 1 },
        ],
      })
      answerPosts({ '/api/customers/7/move': { machineId: 'a2345abcde' } })
      const refresh = vi.fn(async () => {})
      await render(<MachineDetailsSection id={home.id} />, { ...workspace({ machines: [machine, taking, attic, cellar], me: { ...me, access: { ...me.access, can: [...everything, 'machines.customers'] } } }), refresh })
      expect(page()).toContain('8 GB plan · 2 servers')
      expect(page()).toContain('4 GB plan · 1 server · Paused')
      expect(buttons('Move…')).toHaveLength(2)
      await click(buttons('Move…')[0]!)
      const dialog = () => document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog()).toContain('Move alex to another machine?')
      expect(dialog()).toContain('The machine each one leaves keeps its final backup for 7 days.')
      expect(dialog()).toContain('The fullest machine with room for their plan')
      expect(dialog()).toContain('my-vps')
      expect(dialog()).toContain('attic')
      expect(dialog()).not.toContain('cellar')
      expect(dialog()).not.toContain('home-server')
      const pick = [...document.querySelectorAll('label')].find((l) => l.textContent === 'attic')?.querySelector<HTMLElement>('[role="radio"]')
      if (!pick) throw new Error('no attic choice')
      await click(pick)
      await click('Move alex')
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/7/move', { machineId: 'a2345abcde' })
      expect(refresh).toHaveBeenCalled()
      expect(document.querySelector('[role="dialog"]')).toBeNull()

      await click(buttons('Move…')[1]!)
      await click('Move sam')
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/8/move', {})
    })

    it('lists the customers on the dashboard’s machine on its page, for the owner alone', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' } }
      answer({ '/api/machines/m2345abcde/customers': [{ id: 7, name: 'alex', state: 'active', memoryMB: 4096, servers: 1, machineId: 'm2345abcde', here: 1 }] })
      await render(<MachinePage id={machine.id} />, owner(taking))
      expect(document.querySelector('#machine-customers')?.textContent).toBe('Customers')
      expect(page()).toContain('4 GB plan · 1 server')
      await click('Move…')
      const dialog = document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog).toContain('home-server')
      expect(dialog).not.toContain('my-vps')
      await render(<MachinePage id={machine.id} />, owner(taking, everything))
      expect(page()).not.toContain('4 GB plan')
    })

    it('follows a move to a machine, and tries one that stopped again', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' }, customers: 2 }
      answer({
        '/api/machines/h2345abcde/customers': [
          { id: 7, name: 'alex', state: 'active', memoryMB: 8192, servers: 2, machineId: 'h2345abcde', here: 1, move: { startedAt: '2026-09-30T02:00:00Z', startedBy: 'siya', left: 1 } },
          { id: 8, name: 'sam', state: 'active', memoryMB: 4096, servers: 1, machineId: 'h2345abcde', here: 0, move: { startedAt: '2026-09-30T02:00:00Z', startedBy: 'siya', left: 1, error: 'survival: attic couldn’t make it from its folder.' } },
        ],
      })
      await render(<MachineDetailsSection id={home.id} />, owner(taking))
      expect(page()).toContain('8 GB plan · 2 servers · Moving here: 1 server to go')
      expect(page()).toContain('Their move here stopped: survival: attic couldn’t make it from its folder.')
      expect(buttons('Move…')).toEqual([])
      await click('Try again')
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/8/move', { machineId: 'h2345abcde' })
    })

    it('lists customers a stopped move left servers with, and tries their move again to their own machine', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' }, customers: 2 }
      const attic: MachineView = { ...taking, id: 'a2345abcde', name: 'attic' }
      const stopped = { startedAt: '2026-09-30T02:00:00Z', startedBy: 'siya', left: 1, error: 'survival didn’t stop.' }
      answer({
        '/api/machines/h2345abcde/customers': [
          { id: 7, name: 'alex', state: 'active', memoryMB: 8192, servers: 2, machineId: 'a2345abcde', here: 1, move: stopped },
          { id: 8, name: 'sam', state: 'active', memoryMB: 4096, servers: 1, machineId: '', here: 1, move: stopped },
        ],
      })
      await render(<MachineDetailsSection id={home.id} />, workspace({ machines: [machine, taking, attic], me: { ...me, access: { ...me.access, can: [...everything, 'machines.customers'] } } }))
      expect(page()).toContain('8 GB plan · 2 servers · 1 still here, their servers go on attic · Their move stopped: survival didn’t stop.')
      expect(page()).toContain('4 GB plan · 1 server · 1 still here, waiting for room on another machine · Their move stopped: survival didn’t stop.')
      expect(page()).not.toContain('Their move here')
      await click(buttons('Try again')[0]!)
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/7/move', { machineId: 'a2345abcde' })
      await click(buttons('Try again')[1]!)
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/8/move', {})
    })

    it('moves a customer with servers left on a machine to their own machine by default, though it takes no new customers', async () => {
      const taking = { ...home, takesCustomers: { since: '2026-09-29T14:00:00Z', by: 'siya' }, customers: 1 }
      const attic: MachineView = { ...home, id: 'a2345abcde', name: 'attic' }
      const cellar: MachineView = { ...taking, id: 'c2345abcde', name: 'cellar' }
      answer({ '/api/machines/h2345abcde/customers': [{ id: 7, name: 'alex', state: 'active', memoryMB: 8192, servers: 2, machineId: 'a2345abcde', here: 1 }] })
      answerPosts({ '/api/customers/7/move': { machineId: 'a2345abcde' } })
      await render(<MachineDetailsSection id={home.id} />, workspace({ machines: [machine, taking, attic, cellar], me: { ...me, access: { ...me.access, can: [...everything, 'machines.customers'] } } }))
      expect(page()).toContain('1 still here, their servers go on attic')
      await click('Move…')
      const dialog = document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog).toContain('attic, their machine')
      expect(dialog).toContain('cellar')
      await click('Move alex')
      expect(vi.mocked(client.post)).toHaveBeenLastCalledWith('/api/customers/7/move', { machineId: 'a2345abcde' })
    })

    it('warns before removing a machine customers are on', async () => {
      await render(<MachineDetailsSection id={home.id} />, owner({ ...home, customers: 2 }))
      await click('Remove home-server…')
      const dialog = document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog).toContain('2 customers are on home-server')
      expect(dialog).toContain('Move them first, in Customers on this page, to take their servers along. Otherwise they get room on another machine to start again, and their servers stay on home-server, out of reach.')
      expect(button('Remove home-server').disabled).toBe(false)
      await render(<MachineDetailsSection id={home.id} />, owner(home))
      await click('Remove home-server…')
      expect(document.querySelector('[role="dialog"]')?.textContent).not.toContain('customers are on')
    })

    it('leaves removing a machine customers are on to the owner', async () => {
      await render(<MachineDetailsSection id={home.id} />, owner({ ...home, customers: 2 }, everything))
      await click('Remove home-server…')
      const dialog = document.querySelector('[role="dialog"]')?.textContent ?? ''
      expect(dialog).toContain('2 customers are on home-server')
      expect(dialog).toContain('Only the owner can remove a machine customers are on, as removing it places them on another machine.')
      expect(dialog).not.toContain('Move them first')
      expect(button('Remove home-server').disabled).toBe(true)
      await render(<MachineDetailsSection id={home.id} />, owner(home, everything))
      await click('Remove home-server…')
      expect(button('Remove home-server').disabled).toBe(false)
    })
  })

  it('opens a joined machine’s details for its machine page and Machine settings, and never asks it for an address', async () => {
    const home: MachineView = {
      id: 'h2345abcde',
      projectId: machine.projectId,
      name: 'home-server',
      kind: 'remote',
      link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: new Date().toISOString(), problems: [] },
    }
    const ws = workspace({ machines: [machine, home] })
    for (const page of [<MachinePage key="page" id={home.id} />, <MachineSettingsPage key="settings" id={home.id} />]) {
      window.history.replaceState(null, '', '/')
      vi.mocked(client.get).mockClear()
      const text = await render(<DashboardMachineOnly id={home.id}>{page}</DashboardMachineOnly>, ws)
      expect(window.location.pathname).toBe(`/settings/machines/${home.id}`)
      expect(text).not.toContain('Machine settings')
      expect(vi.mocked(client.get).mock.calls.some(([p]) => String(p).includes('/address'))).toBe(false)
    }
    window.history.replaceState(null, '', '/')
    const text = await render(
      <DashboardMachineOnly id={machine.id}>
        <MachinePage id={machine.id} />
      </DashboardMachineOnly>,
      ws,
    )
    expect(window.location.pathname).toBe('/')
    expect(text).toContain('Machine settings')
  })

  it('says how to get a command when the dashboard has no address another machine can dial', async () => {
    forgetJoinCode()
    vi.mocked(client.post).mockClear()
    answer({ '/api/machines/link': { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5, systems: [{ name: 'Ubuntu', version: '20.04' }, { name: 'Debian', version: '12' }] }, sizingUrl: 'https://playkeeper.io/sizing', available: true, codes: [] } })
    const text = await render(<MachinesSection />)
    expect(text).toContain('Open this dashboard at its IP address or domain name, not localhost, to get the command.')
    expect(text).toContain('Ubuntu 20.04+ or Debian 12+ on x86-64 or ARM64, at least 2 CPU cores, 3 GB of memory and 5 GB of free disk.')
    expect(vi.mocked(client.post).mock.calls.some(([p]) => String(p).includes('/join-codes'))).toBe(false)
  })

  it('gives the command as cloud config for a cloud server that’s being created', async () => {
    forgetJoinCode()
    const fp = 'Z287KN4CDZD0Z8A4XXJA514NKG'
    const cloudConfig = `#cloud-config\nruncmd:\n  - "curl -fsSL https://playkeeper.io/install | sh -s -- --yes --join 203.0.113.5:8443 --code 7KQ2-M9XD --fingerprint ${fp}"\n`
    const cmd = {
      id: 'jc1',
      dials: '203.0.113.5:8443',
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(),
      createdBy: 'siya',
      state: 'waiting',
      code: '7KQ2-M9XD',
      install: `curl -fsSL https://playkeeper.io/install | sudo sh -s -- --join 203.0.113.5:8443 --code 7KQ2-M9XD --fingerprint ${fp}`,
      join: `sudo playkeeper join 203.0.113.5:8443 --code 7KQ2-M9XD --fingerprint ${fp}`,
      installLines: ['curl -fsSL https://playkeeper.io/install | sudo sh -s -- \\', '  --join 203.0.113.5:8443 \\', '  --code 7KQ2-M9XD \\', `  --fingerprint ${fp}`],
      joinLines: ['sudo playkeeper join 203.0.113.5:8443 \\', '  --code 7KQ2-M9XD \\', `  --fingerprint ${fp}`],
      cloudConfig,
    }
    answer({ '/api/machines/link': { addresses: [{ kind: 'ip', address: '203.0.113.5:8443' }], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5, systems: [{ name: 'Ubuntu', version: '20.04' }] }, sizingUrl: '', available: true, codes: [] } })
    answerPosts({ '/api/join-codes': cmd })
    await render(<MachinesSection />)
    expect(page()).toContain('Run this on it')
    await click('New cloud server')
    const shown = [...document.querySelectorAll('[role="group"] pre span')].map((s) => s.textContent)
    expect(shown).toEqual(cloudConfig.trimEnd().split('\n'))
    expect(page()).toContain('Paste this while you create it')
    expect(page()).toContain('Paste it in the cloud’s Cloud config or User data box, with Ubuntu or Debian.')
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    await click(button('Copy'))
    expect(copy).toHaveBeenLastCalledWith(cloudConfig)
    copy.mockRestore()

    // A phone picks the form from a list, which three choices fit.
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    await render(<MachinesSection />)
    expect(document.querySelector('[role="group"][aria-label="Which command"]')).toBeNull()
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="Which command"]')?.click())
    const cloud = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.textContent === 'New cloud server')
    await act(async () => cloud?.click())
    expect([...document.querySelectorAll('[role="group"] pre span')].map((s) => s.textContent)).toEqual(cloudConfig.trimEnd().split('\n'))
    phone.mockRestore()
    forgetJoinCode()
  })

  it('says why a token can’t be made yet', async () => {
    answer({ '/api/machines/link': { addresses: [], minimum: { cores: 2, memoryGB: 3, freeDiskGB: 5, systems: [{ name: 'Ubuntu', version: '20.04' }, { name: 'Debian', version: '12' }] }, sizingUrl: '', available: true }, '/api/tokens': [] })
    await render(<AiAgentsSection />)
    const open = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('New token'))
    await act(async () => open?.click())
    const make = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Make token')
    expect(make?.disabled).toBe(true)
    expect(make?.title).toBe('Give the token a name first.')
  })
})

// Follow-ups after 0.3.0.
describe('World', () => {
  const click = async (label: string) => {
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)
    if (!button) throw new Error(`no ${label} button`)
    await act(async () => button.click())
  }

  it('shows the newest world copy a restore left, and discards it only after asking', async () => {
    const copies = [
      { name: 'data.failed-restore-20260925-101500', kind: 'failed_restore', createdAt: '2026-09-25T10:15:00Z', sizeBytes: 1100 * 2 ** 20 },
      { name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 900 * 2 ** 20 },
    ]
    answer({ '/world-copies': copies, '/backups': [] })
    vi.mocked(client.del).mockClear()
    const text = await render(<WorldPage server={server()} />)
    expect(text).toContain('A restored world that didn’t start is still on this VPS')
    expect(text).toContain('1.1 GB')
    expect(text).not.toContain('Your previous world')
    await click('Discard')
    expect(client.del).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Discard this world copy?')
    answer({ '/world-copies': copies.slice(1), '/backups': [] })
    await click('Discard copy')
    expect(client.del).toHaveBeenCalledWith('/api/servers/abcdefghjk/world-copies/data.failed-restore-20260925-101500')
    expect(document.body.textContent).toContain('Your previous world is still on this VPS')
  })

  it('says why a copy can’t be discarded while a job runs', async () => {
    answer({ '/world-copies': [{ name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 2 ** 30 }], '/backups': [] })
    const job: Operation = { id: 'restore-1', serverId: 'abcdefghjk', kind: 'restore', status: 'running', phase: 'swapping', actor: 'siya', startedAt: '2026-09-25T10:00:00Z' }
    await render(<WorldPage server={server({ operation: job })} />)
    const discard = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Discard')
    expect(discard?.disabled).toBe(true)
    expect(discard?.title).toBe('Restoring Survival. Try again when it’s done.')
  })

  it('shows nothing when no restore left a copy', async () => {
    answer({ '/world-copies': [], '/backups': [] })
    expect(await render(<WorldPage server={server()} />)).not.toContain('still on this VPS')
  })
})

describe('Restore as a new server', () => {
  const preview: RestorePreview = {
    id: 'r2345abcde',
    source: 'Uploaded file world.tar.gz',
    receivedAt: '2026-09-25T10:00:00Z',
    sizeBytes: 50 * 2 ** 20,
    sha256: 'ab'.repeat(32),
    manifest: { createdAt: '2026-09-24T09:00:00Z', playkeeperVersion: '0.3.0', minecraftVersion: '26.1.2', paperBuild: 74, versionId: 'paper-26.1.2', levelName: 'world', fileCount: 89, totalBytes: 120 * 2 ** 20, sourceInstall: 'a1b2c3', settings: {} },
    compatible: true,
    problems: [],
    warnings: ['This backup was made on a different Playkeeper host.'],
    currentWorld: { exists: false, sizeBytes: 0 },
    willCreateRollback: false,
    needsEula: true,
    memoryMB: 1536,
    confirmPhrase: 'restore',
    steps: ['Install the world "world" from the backup'],
    notRestored: [],
  }

  it('asks for the EULA only until its box is ticked', async () => {
    const text = await render(<RestoreDialog preview={preview} onClose={() => {}} />)
    expect(text).toContain('you must accept the Minecraft EULA first')
    const box = document.querySelector<HTMLElement>('[role="checkbox"]')
    const label = box?.closest('label')
    if (!box || !label) throw new Error('no EULA checkbox')
    await act(async () => label.click())
    expect(box.getAttribute('aria-checked')).toBe('true')
    expect(document.body.textContent).not.toContain('you must accept the Minecraft EULA first')
    expect(document.body.textContent).toContain('This backup was made on a different Playkeeper host.')
  })
})

describe('Backup upload', () => {
  const choose = async (size: number) => {
    const file = new File(['x'], 'world.tar.gz', { type: 'application/gzip' })
    Object.defineProperty(file, 'size', { value: size })
    const input = document.querySelector<HTMLInputElement>('input[type=file]')
    if (!input) throw new Error('no backup upload')
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    return file
  }

  it('sends a 1 GB file and turns down a 21 GB one without sending it', async () => {
    const toast = vi.spyOn(toastManager, 'add')
    vi.mocked(client.api).mockClear()
    await render(<RestoreDropZone server={server()} onPreview={() => {}} />)
    const file = await choose(2 ** 30)
    expect(client.api).toHaveBeenCalledWith('POST', '/api/servers/abcdefghjk/restore/upload', undefined, file)
    expect(toast).not.toHaveBeenCalled()
    vi.mocked(client.api).mockClear()
    await choose(21 * 2 ** 30)
    expect(client.api).not.toHaveBeenCalled()
    expect(toast).toHaveBeenCalledWith({ title: 'That file is too big to be a Playkeeper backup.', type: 'error' })
    toast.mockRestore()
  })
})

// Found checking the restore path on a real server.
describe('A restore that didn’t finish', () => {
  const missing = { previous: '/var/lib/playkeeper/servers/abcdefghjk/data.replaced-20260926-103028', dataDir: '/var/lib/playkeeper/servers/abcdefghjk/data', setAsideAt: '2026-09-26T10:30:28Z' }
  const copies = [
    { name: 'data.failed-restore-20260926-103028', kind: 'failed_restore', createdAt: '2026-09-26T10:30:28Z', sizeBytes: 70 * 2 ** 20 },
    { name: 'data.replaced-20260926-103028', kind: 'previous', createdAt: '2026-09-26T10:30:28Z', sizeBytes: 16 * 2 ** 20 },
  ]
  const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)

  it('says where the previous world is while the world folder is missing, long after the restore failed', async () => {
    const hourAgo = new Date(Date.now() - 3_600_000).toISOString()
    const restore: Operation = { ...failed('restore', 'reverting', 'The restored world did not start. Putting the previous world back failed.'), startedAt: hourAgo, finishedAt: hourAgo }
    const s = server({ phase: 'stopped', worldMissing: missing, lastOperation: restore })
    const overview = await render(<Overview server={s} />)
    expect(overview).toContain('A restore didn’t finish, so Survival has no world folder')
    expect(overview).toContain(`Your previous world is safe in ${missing.previous}. Move it back to ${missing.dataDir}, then press Start.`)
    answer({ '/world-copies': copies, '/backups': [] })
    const world = await render(<WorldPage server={s} />)
    expect(world).toContain('A restore didn’t finish, so Survival has no world folder')
    expect(world).not.toContain('A restored world that didn’t start is still on this VPS')
  })

  it('drops a start or a backup refused for the missing world folder once the world is back', async () => {
    const refused = failed('backup', '', `The world folder is missing because a restore did not finish; the previous world is at ${missing.previous}.`, { errorKind: 'world_missing' })
    answer({ '/world-copies': copies, '/backups': [] })
    expect(await render(<WorldPage server={server({ phase: 'stopped', worldMissing: missing, lastOperation: refused })} />)).toContain('A restore didn’t finish, so Survival has no world folder')
    answer({ '/world-copies': copies.slice(0, 1), '/backups': [] })
    const back = await render(<WorldPage server={server({ phase: 'stopped', lastOperation: refused })} />)
    expect(back).not.toContain('Backing up Survival failed')
    expect(back).toContain('A restored world that didn’t start is still on this VPS')
    const start = failed('start', '', 'The world folder is missing because a restore did not finish.', { errorKind: 'world_missing' })
    expect(await render(<Overview server={server({ phase: 'stopped', lastOperation: start })} />)).not.toContain('Starting Survival failed')
  })

  it('drops a start refused while a restore wasn’t finished once it is', async () => {
    const start = failed('start', '', "A restore isn't finished, so Survival can't start until it is.", { errorKind: 'restore_unsettled' })
    expect(await render(<Overview server={server({ phase: 'stopped', restoreUnsettled: {}, lastOperation: start })} />)).toContain('Starting Survival failed')
    expect(await render(<Overview server={server({ phase: 'stopped', lastOperation: start })} />)).not.toContain('Starting Survival failed')
  })

  it('keeps a world copy’s Discard under a backup that just failed', async () => {
    answer({ '/world-copies': copies.slice(0, 1), '/backups': [] })
    const text = await render(<WorldPage server={server({ lastOperation: failed('backup', '', 'Not enough disk space for a backup.', { neededBytes: 2 ** 40 }) })} />)
    expect(text).toContain('Backing up Survival failed')
    expect(text).toContain('A restored world that didn’t start is still on this VPS')
    expect(button('Discard')).toBeDefined()
  })

  it('says on Home that the world folder is missing, and lets only a stopped server nap', async () => {
    const stoppedAt = new Date(Date.now() - 5 * 60_000).toISOString()
    const text = await render(<HomePage />, workspace({ servers: [server({ phase: 'stopped', stoppedAt, worldMissing: missing })] }))
    expect(text).toContain('World folder missing')
    expect(text).not.toContain('Napping')
    expect(button('Start')).toBeUndefined()
    expect(await render(<HomePage />, workspace({ servers: [server({ phase: 'stopped', stoppedAt })] }))).toContain('Napping for 5 minutes')
  })

  it('doesn’t offer a backup while the world folder is missing, and says why', async () => {
    const why = 'Its world folder is missing. Move the previous world back first.'
    const at = '2026-09-26T10:28:00Z'
    const made: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya' }
    answer({ '/world-copies': copies, '/backups': [] })
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing: missing })} />)
    expect(button('Make my first backup')?.disabled).toBe(true)
    expect(button('Make my first backup')?.title).toBe(why)
    answer({ '/world-copies': copies, '/backups': [made] })
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing: missing })} />)
    expect(button('Back up now')?.disabled).toBe(true)
    expect(button('Back up now')?.title).toBe(why)
    answer({ '/world-copies': [], '/backups': [made] })
    await render(<WorldPage server={server({ phase: 'stopped' })} />)
    expect(button('Back up now')?.disabled).toBe(false)

    await render(<ServerPage slug="survival" tab="overview" />, workspace({ servers: [server({ phase: 'stopped', worldMissing: missing })] }))
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label="More actions"]')?.click())
    await act(async () => {})
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent === 'Back up now')
    expect(item?.getAttribute('aria-disabled')).toBe('true')
    expect(item?.title).toBe(why)
  })

  it('doesn’t offer a restore while the world folder is missing, and says why', async () => {
    const why = 'Its world folder is missing. Move the previous world back first.'
    const at = '2026-09-26T10:28:00Z'
    const made: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya' }
    answer({ '/world-copies': copies, '/backups': [made] })
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing: missing })} />)
    expect(button('choose a file')?.disabled).toBe(true)
    expect(button('choose a file')?.title).toBe(why)
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label^="Actions for the backup from"]')?.click())
    await act(async () => {})
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.startsWith('Restore this backup'))
    expect(item?.getAttribute('aria-disabled')).toBe('true')
    expect(item?.title).toBe(why)

    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    await render(<WorldPage server={server({ phase: 'stopped', worldMissing: missing })} />)
    expect(button('Restore a world')?.disabled).toBe(true)
    expect(button('Restore a world')?.title).toBe(why)
    phone.mockRestore()

    answer({ '/world-copies': [], '/backups': [made] })
    await render(<WorldPage server={server({ phase: 'stopped' })} />)
    expect(button('choose a file')?.disabled).toBe(false)
  })

  it('doesn’t offer a restore while another isn’t finished, and says why', async () => {
    const why = 'A restore isn’t finished. Playkeeper finishes it once the server is stopped.'
    const at = '2026-09-26T10:28:00Z'
    const made: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya' }
    answer({ '/world-copies': copies.slice(0, 1), '/backups': [made] })
    await render(<WorldPage server={server({ phase: 'stopped', restoreUnsettled: {} })} />)
    expect(button('choose a file')?.disabled).toBe(true)
    expect(button('choose a file')?.title).toBe(why)
    expect(button('Back up now')?.disabled).toBe(false)
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label^="Actions for the backup from"]')?.click())
    await act(async () => {})
    const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.startsWith('Restore this backup'))
    expect(item?.getAttribute('aria-disabled')).toBe('true')
    expect(item?.title).toBe(why)
  })

  it('says on the World tab when a restore isn’t finished, and what finishes it', async () => {
    const at = '2026-09-26T10:28:00Z'
    const made: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya' }
    const stuck = "The swap journal in /var/lib/playkeeper/restore-staging/8fae916f8b8f5482 can't be read (unreadable swap journal: unexpected end of JSON input)."
    const cases: { name: string; over: Partial<ServerStatus>; phone?: boolean; notice?: string }[] = [
      { name: 'stopped', over: { phase: 'stopped', restoreUnsettled: {} }, notice: 'A restore isn’t finished. Playkeeper finishes it in a moment.' },
      { name: 'running', over: { phase: 'online', restoreUnsettled: {} }, notice: 'A restore isn’t finished. Stop Survival and Playkeeper finishes it.' },
      { name: 'running, on a phone', over: { phase: 'online', restoreUnsettled: {} }, phone: true, notice: 'A restore isn’t finished. Stop Survival and Playkeeper finishes it.' },
      { name: 'stuck', over: { phase: 'stopped', restoreUnsettled: { problem: stuck } }, notice: `A restore isn’t finished, and Playkeeper couldn’t finish it.${stuck}` },
      { name: 'world folder missing', over: { phase: 'stopped', restoreUnsettled: {}, worldMissing: missing } },
      { name: 'settled', over: { phase: 'stopped' } },
    ]
    for (const c of cases) {
      const phone = c.phone ? vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false })) : undefined
      answer({ '/world-copies': [], '/backups': [made] })
      await render(<WorldPage server={server(c.over)} />)
      const shown = [...document.querySelectorAll('[role="status"], [role="alert"]')].map((el) => el.textContent ?? '').find((text) => text.startsWith('A restore isn’t finished'))
      expect(shown, c.name).toBe(c.notice)
      phone?.mockRestore()
    }
  })

  it('keeps a world copy’s Discard off while a restore isn’t finished, and says why', async () => {
    const at = '2026-09-26T10:28:00Z'
    const made: Backup = { id: 'b2345abcde', serverId: 'abcdefghjk', kind: 'manual', createdAt: at, fileName: 'survival.tar.gz', sizeBytes: 446 * 1024, sha256: 'a'.repeat(64), location: 'local', verified: true, verifiedAt: at, downtimeMs: 0, savingPausedMs: 0, durationMs: 0, minecraftVersion: '26.1.2', levelName: 'world', fileCount: 120, createdBy: 'siya' }
    const operation: Operation = { id: 'backup-1', kind: 'backup', status: 'running', phase: 'archiving', actor: 'siya', startedAt: new Date().toISOString() }
    const cases: { name: string; over: Partial<ServerStatus>; discard: [boolean, string] }[] = [
      { name: 'a restore isn’t finished', over: { phase: 'stopped', restoreUnsettled: {} }, discard: [true, 'A restore isn’t finished. Playkeeper finishes it once the server is stopped.'] },
      { name: 'a restore Playkeeper couldn’t finish', over: { phase: 'stopped', restoreUnsettled: { problem: 'Saving the settings failed: disk I/O error.' } }, discard: [true, 'A restore isn’t finished, and Playkeeper couldn’t finish it.'] },
      { name: 'another job runs', over: { phase: 'stopped', operation }, discard: [true, 'Backing up Survival. Try again when it’s done.'] },
      { name: 'nothing to wait for', over: { phase: 'stopped' }, discard: [false, ''] },
    ]
    for (const c of cases) {
      answer({ '/world-copies': copies.slice(0, 1), '/backups': [made] })
      await render(<WorldPage server={server(c.over)} />)
      const discard = button('Discard')
      expect([discard?.disabled, discard?.title], c.name).toEqual(c.discard)
    }
  })

  it('says what to do when a new icon is refused for the missing world folder', async () => {
    const message = `The world folder is missing because a restore did not finish; the previous world is at ${missing.previous}.`
    const hint = `Move that folder back to ${missing.dataDir}, then upload the icon again.`
    vi.mocked(client.api).mockImplementation(((_method: string, path: string) => (path.endsWith('/icon') ? Promise.reject(new client.ApiError(409, { error: message, code: 'world_missing', hint })) : new Promise(() => {}))) as typeof client.api)
    // happy-dom has no images or canvas, so the picture comes out of stand-ins as a 64 × 64 PNG header.
    const png = new Blob([Uint8Array.of(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 64, 0, 0, 0, 64)], { type: 'image/png' })
    vi.stubGlobal('createImageBitmap', async () => ({ width: 64, height: 64 }))
    const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => ({ drawImage: () => {} })) as unknown as HTMLCanvasElement['getContext'])
    const dataURL = vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,')
    const toBlob = vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation((done: BlobCallback) => done(png))
    const toast = vi.spyOn(toastManager, 'add')
    await render(<ServerSettingsPage server={server({ phase: 'stopped', worldMissing: missing })} />)
    const input = document.querySelector<HTMLInputElement>('input[type=file][aria-label="Upload picture"]')
    if (!input) throw new Error('no icon upload')
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'icon.png', { type: 'image/png' })], configurable: true })
    await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
    await act(async () => {})
    expect(toast).toHaveBeenCalledWith({ title: message, description: hint, type: 'error' })
    for (const spy of [toast, toBlob, dataURL, context]) spy.mockRestore()
    vi.unstubAllGlobals()
  })

  it('shortens a long activity line instead of widening the page', async () => {
    answer({ '/activity': [{ ts: new Date().toISOString(), serverId: 'abcdefghjk', kind: 'restored_after_restart' }] })
    await render(<HomePage />)
    const line = [...document.querySelectorAll('li > span')].find((el) => el.textContent === 'Survival restored after Playkeeper restarted')
    // happy-dom has no layout. A line with no width of its own can't push its card past a phone's screen.
    expect(line?.className.split(' ')).toContain('w-0')
  })
})

describe('Sticky headers and switching pages', () => {
  const onPhone = () => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))

  it('keeps a server’s name, status, actions and tabs in the header that stays at the top on a computer', async () => {
    await render(<ServerPage slug="survival" tab="overview" />)
    const header = document.querySelector('header[data-sticky-header]')
    expect(header?.className.split(' ')).toEqual(expect.arrayContaining(['sticky', 'top-2']))
    expect(header?.querySelector('h1')?.textContent).toBe('Survival')
    expect(header?.textContent).toContain('Copy join address')
    expect(header?.querySelector('nav[aria-label="Server pages"]')?.textContent).toContain('Settings')
    expect(document.querySelectorAll('header[data-sticky-header]')).toHaveLength(1)
  })

  it('keeps a compact bar with the name and search at the top on a phone, and lets the status scroll away', async () => {
    const phone = onPhone()
    await render(<ServerPage slug="survival" tab="overview" />)
    const header = document.querySelector('header[data-sticky-header]')
    expect(header?.className.split(' ')).toEqual(expect.arrayContaining(['sticky', 'top-0']))
    expect(header?.querySelector('h1 button')?.textContent).toBe('Survival')
    expect(header?.querySelector('button[aria-label="Search or jump to…"]')).not.toBeNull()
    expect(header?.textContent).not.toContain('Online')
    expect(page()).toContain('Online')
    phone.mockRestore()
  })

  it('keeps the Settings header, and its list of pages beside the content, at the top on a computer', async () => {
    await render(<GlobalSettingsPage page={{ name: 'team' }} />)
    expect(document.querySelector('header[data-sticky-header] h1')?.textContent).toBe('Settings')
    expect(document.querySelector('nav[aria-label="Settings sections"]')?.className).toMatch(/\bsticky\b/)
  })

  it('shows a new page or tab at once, with what fades in on it already in place', async () => {
    const fade = { animationName: 'fade', finish: vi.fn(), effect: { getComputedTiming: () => ({ endTime: 200 }) } }
    ;(document as { getAnimations?: () => unknown[] }).getAnimations = () => [fade]
    function Tabs() {
      const [tab, setTab] = useState<'overview' | 'console'>('overview')
      return (
        <AppShell route={{ name: 'server', slug: 'survival', tab }}>
          <button type="button" onClick={() => setTab('console')}>
            {tab}
          </button>
        </AppShell>
      )
    }
    await render(<Tabs />)
    fade.finish.mockClear()
    await click('overview')
    expect(page()).toContain('console')
    expect(fade.finish).toHaveBeenCalled()
    expect(document.querySelector('main [class*="animate-"]')).toBeNull()
    delete (document as { getAnimations?: () => unknown[] }).getAnimations
  })
})
