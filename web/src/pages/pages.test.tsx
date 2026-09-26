// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { AddonSources, Address, Backup, Catalog, Crash, FileRefusal, MachineView, Me, MemoryAdvice, MetricsResponse, ModpackDetail, ModpackResults, OffsiteCopy, OffsiteTestResult, OffsiteView, Operation, PlayersSummary, Preflight, RestorePreview, Running, ServerConfig, ServerStatus, SignInNotice, TemplateContents, TemplateExport, TemplatePlan } from '@/api/types'
import { useWorkspace, WorkspaceContext, WorkspaceProvider, type Workspace } from '@/api/workspace'
import { AddonSourcesCard } from '@/components/app/addon-sources'
import { GetStartedCard, hiddenKey } from '@/components/app/checklist'
import { CommandPalette } from '@/components/app/command-palette'
import { ModpackPicker } from '@/components/app/modpacks'
import { RestoreDialog, RestoreDropZone } from '@/components/app/restore'
import { AppShell } from '@/components/app/shell'
import { TemplateDialog } from '@/components/app/templates'
import { toastManager } from '@/components/ui/toast'
import { formatLongDate } from '@/lib/format'
import { HomePage } from './home'
import { MachinePage } from './machine'
import { createNote, NewServerPage } from './new-server'
import { Onboarding } from './onboarding'
import { RecoverPage } from './recover'
import { ServerPage } from './server'
import { CopiesCard } from './server/copies'
import { Overview } from './server/overview'
import { PlayersPage } from './server/players'
import { RunningPage } from './server/running'
import { ServerSettingsPage } from './server/settings'
import { WorldPage } from './server/world'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
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

/** Clicks the button whose text is exactly the label. */
async function click(label: string) {
  const button = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label)
  if (!button) throw new Error(`no button “${label}”`)
  await act(async () => button.click())
  await act(async () => {})
}

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
    expect(steps[1]?.textContent).not.toContain('Checksum matched')
    expect(text).not.toContain('Checksum matched')
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

  it('says who made a template and when, when it says both', async () => {
    window.history.replaceState(null, '', '/servers/new#template=eyJ2IjoxfQ')
    vi.mocked(client.api).mockResolvedValue({ ...plan, contents: { ...contents, author: 'siya', created: '2026-09-25' } })
    answer({ '/catalog': catalog })
    await render(<NewServerPage />)
    await act(async () => {})
    expect(document.body.textContent).toMatch(/Survival with friendsFrom a link · from siya · made (25 Sep|Sep 25)/)
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

  it('says what a pack’s server downloads, not Paper', () => {
    expect(createNote(4, 'modpack', 'fabric')).toBe('After you start it, Playkeeper downloads Fabric and the pack’s mods, checks each file, and tells you when friends can join.')
    expect(createNote(4, 'template', 'neoforge')).toContain('downloads NeoForge and the template’s add-ons')
    expect(createNote(4, 'modpack', '')).not.toContain('Paper')
    expect(createNote(4, 'type', 'purpur')).toContain('downloads Purpur, checks it')
    expect(createNote(0, 'modpack', 'fabric')).toBe('Friends install the same modpack. You get a link to send.')
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

  it('checks the machine in plain words, with the provider firewall to do by hand', async () => {
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

describe('Machine page', () => {
  const none: Address = { kind: '', ip: '198.51.100.10', panelPort: 8443, base: 'playkeeper.io', servers: [], names: { url: 'https://names.playkeeper.io' } }
  const day = 24 * 3600_000
  const certificate = { names: ['alex.playkeeper.io'], challenge: 'dns-01', notBefore: new Date(Date.now() - 30 * day).toISOString(), notAfter: new Date(Date.now() + 60 * day).toISOString() }
  const free: Address = { ...none, kind: 'playkeeper', host: 'alex.playkeeper.io', certificate }

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

  it('lets only the owner change where copies go or download the key', async () => {
    answer({ '/offsite': { ...sftp, enabled: true, key: { recipient: 'age1x', createdAt: '2026-09-24T10:00:00Z', oldKeys: 0, fileName: 'playkeeper-recovery-key-survival.txt' } } })
    await render(<CopiesCard server={server()} onChangeRules={() => {}} />, workspace({ me: { ...me, user: { username: 'friend', role: 'member' } } }))
    expect(document.body.textContent).toContain('Only the owner can change where copies go.')
    expect(document.querySelector<HTMLInputElement>('#offsite-host')?.disabled).toBe(true)
    const download = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Download')
    expect(download?.disabled).toBe(true)
    expect(download?.title).toBe('Only the owner can hold the recovery key.')
    const test = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Test connection')
    expect(test?.title).toBe('Only the owner can change where copies go.')
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
    answer({ '/offsite/copies': { copies: [copy('b2', '2026-09-25T12:47:00Z', true), copy('b1', '2026-09-20T18:47:00Z', false)] }, '/offsite': b2, '/backups': [backup('b3', '2026-09-25T18:47:00Z'), backup('b2', '2026-09-25T12:47:00Z')] })
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

  it('says why a copy’s checksum or deleting it is out of reach', async () => {
    const { item } = await openCopyMenu(copy('b1', '2026-09-20T18:47:00Z', false), workspace({ me: { ...me, user: { username: 'alex', role: 'admin' } } }))
    for (const [label, reason] of [
      ['Copy checksum', 'This copy’s checksum wasn’t recorded.'],
      ['Delete backup', 'Only the owner can delete copies kept on Backblaze B2.'],
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

  it('is the owner’s alone', async () => {
    const text = await render(<RecoverPage />, workspace({ servers: [], me: { ...me, user: { username: 'friend', role: 'member' } } }))
    expect(text).toContain('Only the owner can restore from a recovery key.')
    expect(document.querySelector('input[type=file]')).toBeNull()
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

  it('shortens a long activity line instead of widening the page', async () => {
    answer({ '/activity': [{ ts: new Date().toISOString(), serverId: 'abcdefghjk', kind: 'restored_after_restart' }] })
    await render(<HomePage />)
    const line = [...document.querySelectorAll('li > span')].find((el) => el.textContent === 'Survival restored after Playkeeper restarted')
    // happy-dom has no layout. A line with no width of its own can't push its card past a phone's screen.
    expect(line?.className.split(' ')).toContain('w-0')
  })
})
