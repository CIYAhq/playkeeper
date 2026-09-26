// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Backup, Crash, FileRefusal, MachineView, Me, MemoryAdvice, MetricsResponse, Operation, PlayersSummary, Preflight, RestorePreview, Running, ServerConfig, ServerStatus } from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { toastManager } from '@/components/ui/toast'
import { HomePage } from './home'
import { Onboarding } from './onboarding'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { RunningPage } from './server/running'
import { ServerSettingsPage } from './server/settings'
import { WorldPage } from './server/world'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
  api: vi.fn(() => Promise.resolve({})),
}))

const me: Me = { user: { username: 'siya', role: 'owner' }, csrfToken: 't', expiresAt: '2026-09-26T00:00:00Z', idleTimeoutSeconds: 43200, version: '0.3.0' }

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

/** Answers GETs by path prefix; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([prefix]) => path.includes(prefix))
    return hit ? Promise.resolve(hit[1]) : new Promise(() => {})
  }) as typeof client.get)
}

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

    const pregen = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Pre-generate the map'))
    expect(pregen?.disabled).toBe(true)
    expect(pregen?.title).toBe('Coming later')
    expect(rows[0]).toContain('Coming later')
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
    expect(text).toContain('Stops it when empty, wakes it on join.')
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

  it('keeps the section in the address current in the nav while the one above it is still in view', async () => {
    const observers: { cb: IntersectionObserverCallback; els: Element[] }[] = []
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        els: Element[] = []
        constructor(cb: IntersectionObserverCallback) {
          observers.push({ cb, els: this.els })
        }
        observe(el: Element) {
          this.els.push(el)
        }
        disconnect() {}
      },
    )
    at('/servers/survival/settings#memory')
    answer({ '/memory': keep })
    await render(<ServerSettingsPage server={server()} />)
    const current = () => document.querySelector('nav [aria-current="location"]')?.textContent
    const inView = async (...ids: string[]) => {
      const o = observers.at(-1)
      if (!o) throw new Error('no IntersectionObserver')
      const entries = o.els.map((target) => ({ target, isIntersecting: ids.includes(target.id) }) as unknown as IntersectionObserverEntry)
      await act(async () => o.cb(entries, {} as IntersectionObserver))
    }
    await inView('list', 'memory')
    expect(current()).toBe('Memory')
    await inView('game', 'list')
    expect(current()).toBe('In the game')
    at('/servers/survival/settings#version')
    await act(async () => window.dispatchEvent(new HashChangeEvent('hashchange')))
    expect(current()).toBe('Minecraft version')
    vi.unstubAllGlobals()
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
    expect(text).toContain('About 15 seconds. Players stay online.')
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
    expect(text).toContain('Update Multiverse-PortalsComing later')
    expect(text).not.toContain('Recommended')
    expect(labelled('Update Multiverse-Portals')?.querySelector('[data-disabled]')).not.toBeNull()
    expect(labelled('Update Multiverse-Portals')?.title).toBe('Coming later')
    expect(labelled('Remove Multiverse-Portals')?.querySelector('[data-checked]')).not.toBeNull()
    await press('Remove and start Survival')
    expect(posts()).toEqual([['/addons/remove-file', { jar: 'Multiverse-Portals-5.0.2.jar', start: true }]])
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

describe('Players', () => {
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
    answer({ '/whitelist': [{ name: 'Lenn0x' }, { name: 'mara_k' }], '/operators': [], '/players/summary': summary, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('≈ 6 h 10 m')
    expect(text).toContain('1 hour')
    expect(text).not.toContain('≈ 1 hour')
    expect(text).toContain('≈ means it ended in a crash.')
  })

  it('shows a new player at once and puts the name back if Minecraft refuses it', async () => {
    answer({ '/whitelist': [], '/operators': [], '/players/summary': { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
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
    answer({ '/whitelist': [], '/operators': [], '/players/summary': { tz: 'UTC', days: [], players: [], observedSessions: 0, uncertainSessions: 0, retentionDays: 180 }, '/players/sessions': { from: '', to: '', sessions: [] }, '/activity': [] })
    const text = await render(<PlayersPage server={server()} />)
    expect(text).toContain('Nobody’s joined yet')
    expect(text).toContain('You add their name')
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
