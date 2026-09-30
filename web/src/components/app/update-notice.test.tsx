// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, ServerConfig, ServerStatus, UpdateInfo } from '@/api/types'
import { WorkspaceProvider } from '@/api/workspace'
import { AppShell } from '@/components/app/shell'
import { HomePage } from '@/pages/home'
import type { Route } from '@/lib/router'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn((_path: string, body: unknown) => Promise.resolve(body ?? {})),
}))

// The notice of a new release: for those who can install it, read from
// what the machine's last check found, dismissed one release at a time.

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'machines.view']
const owner: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-10-02T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.8',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: true, can: everything },
}
const member = (username: string, role: Me['access']['role'], can: Action[], over: Partial<Me['access']> = {}): Me => ({ ...owner, user: { username, role: 'member' }, access: { ...owner.access, role, can, ...over } })
const admin = member('sam', 'admin', everything)
const moderator = member('mo', 'moderator', ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'machines.view'])
const viewer = member('vi', 'viewer', ['view', 'account.manage', 'machines.view'])
const creator = member('cleo', 'admin', ['view', 'account.manage', 'servers.run', 'servers.manage', 'servers.create_own'], { servers: { servers: ['abcdefghjk'] }, home: 'm2345abcde' })
const customer = member('alex', 'admin', ['view', 'account.manage', 'servers.run', 'servers.manage', 'servers.create_own'], { servers: { servers: ['abcdefghjk'] }, home: 'm2345abcde' })

const live = { hostname: 'my-vps', os: 'Ubuntu 24.04', arch: 'x86-64', cpus: 4, memoryTotalMB: 16384, systemReserveMB: 1536, serversMemoryMB: 2048, memoryFreeMB: 12800, docker: true, agentVersion: '0.4.8', defaultGamePort: 25565, offlineModeTest: false, servers: 1 }
const machine = (over: Partial<MachineView['live']> = {}): MachineView => ({ id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local', live: { ...live, updateAvailable: '0.4.9', ...over } })
const survival: ServerStatus = {
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
  config: { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 2048, levelName: 'world', maxPlayers: 10, whitelist: true } as ServerConfig,
  firstSteps: { backedUp: true, downloaded: true, invited: 'alex', friendJoined: 'alex' },
}
const info: UpdateInfo = { current: '0.4.8', supported: true, latest: '0.4.9', available: true, notes: '- Backups finish sooner', autoCheck: true }

/** Answers the dashboard's reads: this machine, Survival, and prefs once they're let go. */
function serve(m: MachineView, prefs: Promise<Record<string, string>> = Promise.resolve({})) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path === '/api/machines') return Promise.resolve([m])
    if (path === '/api/servers') return Promise.resolve([survival])
    if (path === '/api/me/prefs') return prefs
    if (path === `/api/machines/${m.id}/update`) return Promise.resolve(info)
    return new Promise(() => {})
  }) as typeof client.get)
}

function media(phone: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: phone && query === '(max-width: 639px)',
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  window.history.replaceState(null, '', '/')
  vi.clearAllMocks()
  vi.restoreAllMocks()
})

async function render(me: Me, node: ReactNode) {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceProvider me={me} onMe={() => {}} onSignedOut={() => {}}>{node}</WorkspaceProvider>))
  await act(async () => {})
}

const shell = (route: Route = { name: 'home' }) => (
  <AppShell route={route}>
    <p>page</p>
  </AppShell>
)
const notice = () => [...document.querySelectorAll('section')].find((s) => s.textContent?.includes('is out'))
const button = (within: Element | undefined, name: string) => [...(within?.querySelectorAll('button') ?? [])].find((b) => (b.getAttribute('aria-label') ?? b.textContent) === name) as HTMLElement | undefined
const updateReads = () => vi.mocked(client.get).mock.calls.filter(([p]) => String(p).includes('/update'))
const checks = () => vi.mocked(client.post).mock.calls.filter(([p]) => String(p).includes('/update/'))

describe('The notice of a new release', () => {
  for (const [who, me] of [
    ['the owner', owner],
    ['an admin of every server', admin],
  ] as const) {
    it(`tells ${who} in the sidebar, and Update opens the update dialog`, async () => {
      media(false)
      serve(machine())
      await render(me, shell())
      await vi.waitFor(() => expect(notice()?.textContent).toBe('Playkeeper 0.4.9 is outYou have 0.4.8Update'))
      expect(notice()?.closest('aside')).not.toBeNull()
      expect(updateReads()).toEqual([])
      await act(async () => button(notice(), 'Update')?.click())
      await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Update Playkeeper to 0.4.9'))
      expect(document.querySelector('[role="dialog"]')?.textContent).toContain('Backups finish sooner')
      expect(checks()).toEqual([])
    })
  }

  for (const [who, me] of [
    ['a moderator', moderator],
    ['a viewer', viewer],
    ['a creator', creator],
    ['a customer', customer],
  ] as const) {
    it(`never shows to ${who}, who can’t update the machine`, async () => {
      media(false)
      serve(machine())
      await render(me, shell())
      await vi.waitFor(() => expect(document.querySelector('aside')?.textContent).toContain('Survival'))
      await act(async () => {})
      expect(notice()).toBeUndefined()
      expect(document.body.textContent).not.toContain('0.4.9')

      media(true)
      await render(me, shell({ name: 'server', slug: 'survival', tab: 'overview' }))
      await vi.waitFor(() => expect(document.querySelector('a[href="/more"]')).not.toBeNull())
      expect(document.querySelector('a[href="/more"] .bg-success')).toBeNull()
    })
  }

  it('goes when dismissed, stays gone for that release, and comes back for the next', async () => {
    media(false)
    serve(machine())
    await render(owner, shell())
    await vi.waitFor(() => expect(notice()).toBeDefined())
    await act(async () => button(notice(), 'Dismiss')?.click())
    expect(client.post).toHaveBeenCalledWith('/api/me/prefs', { 'update.dismissed': '0.4.9' })
    expect(notice()).toBeUndefined()

    serve(machine(), Promise.resolve({ 'update.dismissed': '0.4.9' }))
    await render(owner, shell())
    await vi.waitFor(() => expect(document.querySelector('aside')?.textContent).toContain('Survival'))
    await act(async () => {})
    expect(notice()).toBeUndefined()

    serve(machine({ updateAvailable: '0.4.10' }), Promise.resolve({ 'update.dismissed': '0.4.9' }))
    await render(owner, shell())
    await vi.waitFor(() => expect(notice()?.querySelector('h2')?.textContent).toBe('Playkeeper 0.4.10 is out'))
  })

  it('waits for the preferences, so a dismissed notice never flashes', async () => {
    media(false)
    let loaded: (p: Record<string, string>) => void = () => {}
    serve(machine(), new Promise((resolve) => (loaded = resolve)))
    await render(owner, shell())
    await vi.waitFor(() => expect(document.querySelector('aside')?.textContent).toContain('Survival'))
    await act(async () => {})
    expect(notice()).toBeUndefined()
    await act(async () => loaded({}))
    await vi.waitFor(() => expect(notice()).toBeDefined())
  })

  it('makes way for the progress while the update installs', async () => {
    media(false)
    serve(machine({ updateInstalling: '0.4.9' }))
    await render(owner, shell())
    await vi.waitFor(() => expect(document.querySelector('aside')?.textContent).toContain('Updating Playkeeper'))
    expect(notice()).toBeUndefined()
  })

  it('sits at the top of Home on a phone, and the More tab’s dot goes with it', async () => {
    media(true)
    serve(machine())
    await render(owner, <HomePage />)
    await vi.waitFor(() => expect(notice()?.querySelector('h2')?.textContent).toBe('Playkeeper 0.4.9 is out'))
    const main = document.querySelector('h1')?.parentElement?.parentElement?.parentElement
    expect(main?.querySelector('section')).toBe(notice())

    await render(owner, shell({ name: 'server', slug: 'survival', tab: 'overview' }))
    await vi.waitFor(() => expect(document.querySelector('a[href="/more"] .bg-success')).not.toBeNull())
    serve(machine(), Promise.resolve({ 'update.dismissed': '0.4.9' }))
    await render(owner, shell({ name: 'server', slug: 'survival', tab: 'overview' }))
    await vi.waitFor(() => expect(document.querySelector('a[href="/more"]')).not.toBeNull())
    await act(async () => {})
    expect(document.querySelector('a[href="/more"] .bg-success')).toBeNull()
  })
})
