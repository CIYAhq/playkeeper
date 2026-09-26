// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { Routes } from '@/App'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const requested = vi.hoisted(() => new Set<string>())
vi.mock('@/pages/server/backups', async (importOriginal) => (requested.add('backups'), importOriginal()))
vi.mock('@/pages/server/console', async (importOriginal) => (requested.add('console'), importOriginal()))
vi.mock('@/pages/server/copies', async (importOriginal) => (requested.add('copies'), importOriginal()))
vi.mock('@/pages/server/map', async (importOriginal) => (requested.add('map'), importOriginal()))
vi.mock('@/pages/server/players', async (importOriginal) => (requested.add('players'), importOriginal()))
vi.mock('@/pages/server/profile', async (importOriginal) => (requested.add('profile'), importOriginal()))
vi.mock('@/pages/server/plugins', async (importOriginal) => (requested.add('plugins'), importOriginal()))
vi.mock('@/pages/server/running', async (importOriginal) => (requested.add('running'), importOriginal()))
vi.mock('@/pages/server/schedules', async (importOriginal) => (requested.add('schedules'), importOriginal()))
vi.mock('@/pages/server/settings', async (importOriginal) => (requested.add('settings'), importOriginal()))
vi.mock('@/pages/server/world', async (importOriginal) => (requested.add('world'), importOriginal()))
vi.mock('@/pages/server/world-packs', async (importOriginal) => (requested.add('world-packs'), importOriginal()))
vi.mock('@/pages/server/world-pregen', async (importOriginal) => (requested.add('world-pregen'), importOriginal()))

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
const workspace: Workspace = {
  me,
  servers: [],
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
  document.body.innerHTML = ''
})

// A tab whose code only loads when it's opened can't open once the panel
// restarts, as it does for an update, so every tab's code loads after sign-in.
it('loads every server tab’s code while the browser is idle after sign-in', async () => {
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={workspace}><Routes route={{ name: 'home' }} /></WorkspaceContext.Provider>))
  expect(requested.size).toBe(0)
  const tabs = ['backups', 'console', 'copies', 'map', 'players', 'plugins', 'profile', 'running', 'schedules', 'settings', 'world', 'world-packs', 'world-pregen']
  await vi.waitFor(() => expect([...requested].sort()).toEqual(tabs), { timeout: 10_000, interval: 100 })
}, 15_000)
