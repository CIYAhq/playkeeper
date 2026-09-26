// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, SchedulePreview, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { SchedulesSection } from './schedules'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve({})),
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

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
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
