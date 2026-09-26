// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { Routes } from '@/App'
import { reloadWhenCodeIsStale } from '@/components/app/load-boundary'
import { t } from '@/i18n'
import type { Route } from '@/lib/router'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

// How many more times each page's code fails to load, as it does while the
// panel restarts for an update or a phone loses its connection.
const failures = vi.hoisted(() => ({ account: Infinity, more: 1, console: Infinity, map: 1 }))
const unreachable = vi.hoisted(() => (name: keyof typeof failures) => {
  if (failures[name]-- > 0) throw new TypeError('Failed to fetch dynamically imported module')
})
vi.mock('@/pages/account', async (importOriginal) => (unreachable('account'), importOriginal()))
vi.mock('@/pages/more', async (importOriginal) => (unreachable('more'), importOriginal()))
vi.mock('@/pages/server/console', async (importOriginal) => (unreachable('console'), importOriginal()))
vi.mock('@/pages/server/map', async (importOriginal) => (unreachable('map'), importOriginal()))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine: MachineView = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' }
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
  reachableAt: new Date().toISOString(),
  startedAt: '2026-09-22T10:00:00Z',
  gamePort: 25565,
  offlineModeTest: false,
  crashCount: 0,
  pendingRestart: false,
  gameplay: {},
  config: { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 4096, levelName: 'world', maxPlayers: 10, whitelist: true } as ServerConfig,
  firstSteps: { backedUp: false, downloaded: false },
}
const workspace: Workspace = {
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

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

async function show(route: Route) {
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={workspace}><Routes route={route} /></WorkspaceContext.Provider>))
  await vi.waitFor(() => expect(document.querySelector('[role="alert"]')).not.toBeNull())
}

it.each([
  ['a page whose code never loads', { name: 'account' }],
  ['a page whose code fails to load once', { name: 'more' }],
  ['a server tab whose code never loads', { name: 'server', slug: 'survival', tab: 'console' }],
  ['a server tab whose code fails to load once', { name: 'server', slug: 'survival', tab: 'map' }],
] as [string, Route][])('%s says so with a Reload while the rest of the dashboard stays', async (_, route) => {
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})
  await show(route)
  const alert = document.querySelector('[role="alert"]')
  expect(alert?.textContent).toContain(t('load.failed'))
  expect(document.querySelector(`nav[aria-label="${t('nav.main')}"]`)).not.toBeNull()
  if (route.name === 'server') expect(document.querySelector('h1')?.textContent).toBe('Survival')
  const button = [...(alert?.querySelectorAll('button') ?? [])].find((b) => b.textContent === t('load.reload'))
  await act(async () => button?.click())
  expect(reload).toHaveBeenCalledOnce()
})

// Code Vite can't load, as after an update replaced it, reloads the page; a
// page that still can't load right after that shows its Reload instead of
// reloading over and over.
it('reloads the page when a page’s code can’t load, at most once a minute', () => {
  const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})
  const now = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
  sessionStorage.clear()
  reloadWhenCodeIsStale()
  const failed = () => {
    const e = new Event('vite:preloadError', { cancelable: true })
    window.dispatchEvent(e)
    return e.defaultPrevented
  }
  expect([failed(), reload.mock.calls.length]).toEqual([true, 1])
  now.mockReturnValue(1_030_000)
  expect([failed(), reload.mock.calls.length]).toEqual([false, 1])
  now.mockReturnValue(1_061_000)
  expect([failed(), reload.mock.calls.length]).toEqual([true, 2])
})
