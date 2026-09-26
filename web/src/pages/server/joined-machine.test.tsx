// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, Pregen, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { t } from '@/i18n'
import { SchedulesSection } from './schedules'
import { PregenPage } from './world-pregen'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
  post: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const dashboard: MachineView = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' }
const home: MachineView = {
  id: 'h2345abcde',
  projectId: 'p2345abcde',
  name: 'home-server',
  kind: 'remote',
  link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', connectedAt: '2026-09-26T10:00:00Z', problems: [] },
}
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
  config: { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 1536, levelName: 'world', maxPlayers: 10, whitelist: true } as ServerConfig,
  firstSteps: { backedUp: false, downloaded: false },
}
const pregen: Pregen = {
  state: 'idle',
  world: 'world',
  chunks: 0,
  total: 0,
  percent: 0,
  etaSeconds: -1,
  pauseForPlayers: true,
  installed: true,
  presets: [{ id: 'medium', radius: 2000, chunks: 63_001, seconds: 1320, diskBytes: 380 * 2 ** 20, fits: true }],
  diskFreeBytes: 41 * 2 ** 30,
}

function workspace(over: Partial<Workspace>): Workspace {
  return {
    me,
    servers: [server],
    serversError: undefined,
    machine: dashboard,
    machines: [dashboard, home],
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

let root: Root | undefined
let pregenNow = pregen

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  vi.mocked(client.get).mockImplementation(((path: string) =>
    Promise.resolve(path.endsWith('/pregen') ? pregenNow : path.includes('/schedules/runs') ? { runs: [] } : { schedules: [] })) as typeof client.get)
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

async function render(node: ReactNode, ws: Workspace) {
  root ??= createRoot(document.body.appendChild(document.createElement('div')))
  const r = root
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  for (let i = 0; i < 3; i++) await act(async () => {})
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === label)
  if (!b) throw new Error(`no button "${label}"`)
  return b
}

const onHome = { ...server, machineId: home.id }
const reachable = workspace({})

async function unmount() {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
}

// Pre-generation's Start, Pause and Cancel and a new schedule's Save go by
// the server's own machine, as its other controls do: a joined machine that's
// away or whose agent doesn't answer holds them back with the dashboard's
// agent answering, and one that answers doesn't wait for the dashboard's.
it.each([
  ['on a joined machine that answers', onHome, reachable, undefined],
  ['on a joined machine that’s away', onHome, workspace({ machines: [dashboard, { ...home, link: { ...home.link!, state: 'offline' } }] }), t('machines.away.pill', { name: 'home-server' })],
  ['on a joined machine whose agent doesn’t answer', onHome, workspace({ machines: [dashboard, { ...home, error: { error: 'The agent doesn’t answer.', code: 'agent_unavailable' } }] }), t('reason.noAgent')],
  ['on the dashboard’s machine while its agent doesn’t answer', server, workspace({ agentDown: true, stale: true }), t('reason.noAgent')],
] as [string, ServerStatus, Workspace, string | undefined][])('a server %s: pre-generating and Save schedule say why they wait', async (_, s, ws, reason) => {
  pregenNow = pregen
  await render(<PregenPage server={s} />, ws)
  const start = button(t('pregen.start'))
  expect([start.disabled, start.title || undefined]).toEqual([reason !== undefined, reason])
  await unmount()

  pregenNow = { ...pregen, state: 'running', preset: 'medium', radius: 2000, total: 63_001, chunks: 1000, percent: 1.6, etaSeconds: 1200 }
  await render(<PregenPage server={s} />, ws)
  for (const label of [t('pregen.pause'), t('common.cancel')]) {
    const b = button(label)
    expect([label, b.disabled, b.title || undefined]).toEqual([label, reason !== undefined, reason])
  }
  await unmount()

  await render(<SchedulesSection server={s} />, reachable)
  await act(async () => button(t('schedules.new')).click())
  await render(<SchedulesSection server={s} />, ws)
  const save = button(t('schedules.save'))
  expect([save.disabled, save.title || undefined]).toEqual([reason !== undefined, reason])
  const add = button(t('schedules.new'))
  expect([add.disabled, add.title || undefined]).toEqual([reason !== undefined, reason])
})
