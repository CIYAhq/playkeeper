// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'
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

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover', 'machines.view']
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

describe('code Vite can’t load', () => {
  const pageStartingFrom = (entry: string) => `<!doctype html><html><head><script type="module" crossorigin src="${entry}"></script></head><body><div id="root"></div></body></html>`
  let answer: () => Promise<{ ok: boolean; text: () => Promise<string> }>
  const fetchPage = vi.fn(() => answer())
  let reload: MockInstance<() => void>
  let now: MockInstance<() => number>

  /** index.html as the server sends it now, an error status, or a request that fails. */
  function serving(page: string | number | Error) {
    answer = async () => {
      if (page instanceof Error) throw page
      return typeof page === 'string' ? { ok: true, text: async () => page } : { ok: false, text: async () => 'Service Unavailable' }
    }
  }

  /** Vite couldn't load code this many times at once; says whether the error went on to LoadBoundary, once the page has checked. */
  async function failed(times = 1) {
    const events = Array.from({ length: times }, () => new Event('vite:preloadError', { cancelable: true }))
    for (const e of events) window.dispatchEvent(e)
    await new Promise((r) => setTimeout(r))
    return events.every((e) => !e.defaultPrevented)
  }

  beforeAll(() => reloadWhenCodeIsStale())

  beforeEach(() => {
    // The open page's index.html, parsed as the browser did: happy-dom would load a script set with innerHTML.
    document.head.replaceChildren(...new DOMParser().parseFromString(pageStartingFrom('/assets/index-Bx4k2mQa.js'), 'text/html').head.childNodes)
    vi.stubGlobal('fetch', fetchPage)
    fetchPage.mockClear()
    reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})
    now = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.replaceChildren()
  })

  // An update replaced the code when the page the server sends now starts
  // from another script. A page that still can't load right after that
  // reload shows its Reload instead of reloading over and over.
  it('reloads the page when an update replaced it, at most once a minute', async () => {
    serving(pageStartingFrom('/assets/index-7Dw9nPcE.js'))
    expect([await failed(3), reload.mock.calls.length], 'a page’s files that all fail at once reload it once').toEqual([true, 1])
    expect(fetchPage).toHaveBeenCalledOnce()
    expect(fetchPage).toHaveBeenCalledWith('/', { cache: 'no-store' })
    now.mockReturnValue(1_030_000)
    expect([await failed(), reload.mock.calls.length]).toEqual([true, 1])
    now.mockReturnValue(1_061_000)
    expect([await failed(), reload.mock.calls.length]).toEqual([true, 2])
  })

  // The error goes on to the page's Reload. Safari stops loading the code of
  // a page you leave, and a reload then would win over the link you pressed.
  it.each([
    ['the server sends the same page, as after a dropped connection', pageStartingFrom('/assets/index-Bx4k2mQa.js')],
    ['the server can’t be asked, as while Safari leaves the page or offline', new TypeError('Load failed')],
    ['the server answers with an error', 503],
    ['the server sends a page that isn’t the dashboard', '<!doctype html><title>Sign in to the Wi-Fi</title>'],
  ])('code that didn’t load for another reason, when %s, shows its Reload instead of reloading', async (_, page) => {
    serving(page)
    expect([await failed(3), reload.mock.calls.length]).toEqual([true, 0])
    expect(fetchPage).toHaveBeenCalledOnce()
  })
})
