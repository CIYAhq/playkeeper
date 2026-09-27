// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, Schedule, SchedulePreview, ScheduleTiming, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import * as when from '@/lib/when'
import { SchedulesSection } from './schedules'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve({})),
}))

/** The viewer's time zone, as the page reads it. */
const viewer = vi.hoisted(() => ({ zone: 'UTC' }))
vi.mock('@/lib/when', async (importOriginal) => ({ ...(await importOriginal<typeof when>()), viewerTimeZone: () => viewer.zone }))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 1536, heapMB: 1024, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true, playStyle: 'friends' } as ServerConfig
const server: ServerStatus = {
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
  gamePort: 25565,
  offlineModeTest: false,
  crashCount: 0,
  pendingRestart: false,
  gameplay: {},
  config,
  firstSteps: { backedUp: false, downloaded: false },
}
const ws: Workspace = {
  me,
  servers: [server],
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
}

/** Answers GETs by how their path ends; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([end]) => path.endsWith(end))
    return hit ? Promise.resolve(hit[1]) : new Promise(() => {})
  }) as typeof client.get)
}

let root: Root | undefined

async function render(node: ReactNode): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
  return document.body.textContent ?? ''
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === label || el.getAttribute('aria-label') === label)
  if (!b) throw new Error(`no button ${label}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

async function wait(ms: number) {
  await act(async () => new Promise((resolve) => setTimeout(resolve, ms)))
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

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  viewer.zone = 'UTC'
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
})

describe('the schedule dialog’s preview', () => {
  const tomorrow = new Date(Date.now() + 86_400_000).toISOString()
  const cases: { name: string; preview: () => Promise<SchedulePreview>; saves: boolean; says?: string }[] = [
    {
      name: 'a preview over the rate limit',
      preview: () => Promise.reject(new client.ApiError(429, { error: 'Too many actions in a short time. Wait a moment.', code: 'rate_limited' }, 30)),
      saves: true,
    },
    { name: 'a preview lost on the way', preview: () => Promise.reject(new TypeError('Failed to fetch')), saves: true },
    {
      name: 'a schedule the agent finds not valid',
      preview: () => Promise.resolve({ valid: false, nextRuns: [], error: { error: 'Pick a time of day.', code: 'invalid', field: 'timing.at' } }),
      saves: false,
      says: 'Pick a time of day.',
    },
    { name: 'a valid schedule', preview: () => Promise.resolve({ valid: true, nextRuns: [tomorrow], summary: 'every day at 05:00' }), saves: true, says: 'Next run:' },
  ]
  for (const c of cases) {
    it(`${c.saves ? 'leaves Save on' : 'turns Save off'} for ${c.name}`, async () => {
      answer({ '/schedules': { schedules: [] }, '/schedules/runs?limit=6': { runs: [] } })
      vi.mocked(client.post).mockImplementation(((path: string) => (path.endsWith('/schedules/preview') ? c.preview() : Promise.resolve({}))) as typeof client.post)
      await render(<SchedulesSection server={server} />)
      await click(button('New schedule'))
      await wait(400)
      expect(vi.mocked(client.post).mock.calls.some(([path]) => String(path).endsWith('/schedules/preview'))).toBe(true)
      expect(button('Save schedule').disabled).toBe(!c.saves)
      const text = document.body.textContent ?? ''
      if (c.says) expect(text).toContain(c.says)
      expect(text).not.toContain('Too many actions')
      expect(text).not.toContain('Failed to fetch')
    })
  }
})

describe('a restart skipped because people were playing', () => {
  const minutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString()
  const retry = minutes(30)
  const skipped = (nextRun: string): Schedule => ({
    id: 'r1',
    serverId: server.id,
    kind: 'restart',
    timing: { kind: 'daily', timeZone: when.viewerTimeZone(), at: '04:00' },
    payload: { warnSeconds: [600], skipIfPlaying: true },
    enabled: true,
    createdAt: minutes(-3000),
    updatedAt: minutes(-3000),
    createdBy: 'siya',
    updatedBy: 'siya',
    lastRun: { due: minutes(-30), finished: minutes(-30), result: 'skipped', reason: 'people_playing', players: 3, retryAt: retry },
    nextRun,
    summary: 'every day at 04:00',
  })
  const cases = [
    { name: 'lists its retry while the agent plans it next', nextRun: retry, listed: true },
    { name: 'doesn’t list a retry the agent no longer plans', nextRun: minutes(24 * 60 - 30), listed: false },
  ]
  for (const c of cases) {
    it(c.name, async () => {
      answer({ '/schedules': { schedules: [skipped(c.nextRun)] }, '/schedules/runs?limit=6': { runs: [] } })
      const text = await render(<SchedulesSection server={server} />)
      expect(text.includes('tries again at')).toBe(c.listed)
      expect(text.includes('next: ')).toBe(!c.listed)
    })
  }
})

describe('changing a schedule saved in another time zone', () => {
  const saved = (timing: ScheduleTiming): Schedule => ({
    id: 'r1',
    serverId: server.id,
    kind: 'restart',
    timing,
    payload: { warnSeconds: [600, 300], message: 'Survival restarts in {minutes} minutes.', skipIfPlaying: false },
    enabled: true,
    createdAt: '2026-09-20T10:00:00Z',
    updatedAt: '2026-09-20T10:00:00Z',
    createdBy: 'owner',
    updatedBy: 'owner',
    summary: '',
  })
  /** Opens the dialog on the schedule, as the list shows it on the viewer's clock. */
  async function open(timing: ScheduleTiming): Promise<string> {
    answer({ '/schedules': { schedules: [saved(timing)] }, '/schedules/runs?limit=6': { runs: [] } })
    vi.mocked(client.post).mockImplementation(((path: string) => Promise.resolve(path.endsWith('/schedules/preview') ? { valid: true, nextRuns: [] } : {})) as typeof client.post)
    const list = await render(<SchedulesSection server={server} />)
    const row = document.querySelector<HTMLButtonElement>('li button')
    if (!row) throw new Error('no schedule in the list')
    await click(row)
    return list
  }
  const at = () => document.querySelector<HTMLInputElement>('#schedule-at')?.value
  const often = () => document.querySelector('[aria-label="How often"]')?.textContent ?? ''
  async function saveWithAWarningMore(): Promise<{ timing: ScheduleTiming; payload: { warnSeconds?: number[] } }> {
    const box = [...document.querySelectorAll('label')].find((l) => l.textContent?.trim() === '1 minute')
    if (!box) throw new Error('no 1 minute box')
    await click(box)
    return save()
  }
  async function save(): Promise<{ timing: ScheduleTiming; payload: { warnSeconds?: number[] } }> {
    await click(button('Save schedule'))
    const call = vi.mocked(client.post).mock.calls.find(([path]) => String(path).endsWith('/schedules/r1'))
    if (!call) throw new Error('nothing was saved')
    return call[1] as { timing: ScheduleTiming; payload: { warnSeconds?: number[] } }
  }

  const cases: { name: string; zone: string; timing: ScheduleTiming; lists: string; shows: string; often: string }[] = [
    { name: 'saved in the viewer’s zone', zone: 'Asia/Tokyo', timing: { kind: 'daily', timeZone: 'Asia/Tokyo', at: '05:00' }, lists: 'Restart every day at 05:00', shows: '05:00', often: 'Every day' },
    { name: 'every day, in another zone', zone: 'UTC', timing: { kind: 'daily', timeZone: 'Asia/Tokyo', at: '05:00' }, lists: 'Restart every day at 20:00', shows: '20:00', often: 'Every day' },
    {
      name: 'on days that fall on others in the viewer’s zone',
      zone: 'UTC',
      timing: { kind: 'weekly', timeZone: 'Asia/Tokyo', at: '07:30', days: ['mon', 'thu'] },
      lists: 'Restart every Wednesday and Sunday at 22:30',
      shows: '22:30',
      often: 'Every Wednesday and Sunday',
    },
    { name: 'every few hours, in another zone', zone: 'UTC', timing: { kind: 'interval', timeZone: 'Asia/Tokyo', at: '01:00', everyHours: 6 }, lists: 'Restart every 6 hours', shows: '16:00', often: 'Every 6 hours' },
  ]
  for (const c of cases) {
    it(`shows a schedule ${c.name} on the viewer’s clock and keeps its moments`, async () => {
      viewer.zone = c.zone
      const list = await open(c.timing)
      expect(list).toContain(c.lists)
      expect(at()).toBe(c.shows)
      expect(often()).toContain(c.often)
      const sent = await saveWithAWarningMore()
      expect(sent.timing).toEqual(c.timing)
      expect(sent.payload.warnSeconds).toEqual([600, 300, 60])
    })
  }

  it('saves a new time in the viewer’s zone', async () => {
    viewer.zone = 'UTC'
    await open({ kind: 'weekly', timeZone: 'Asia/Tokyo', at: '07:30', days: ['mon', 'thu'] })
    await typeInto('#schedule-at', '21:00')
    const sent = await save()
    expect(sent.timing).toEqual({ kind: 'weekly', timeZone: 'UTC', at: '21:00', days: ['wed', 'sun'] })
  })
})
