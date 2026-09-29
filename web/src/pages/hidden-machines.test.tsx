// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, Catalog, MachineView, Me, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceProvider } from '@/api/workspace'
import { Routes } from '@/App'
import { CommandPalette } from '@/components/app/command-palette'
import { AppShell } from '@/components/app/shell'
import type { Route } from '@/lib/router'
import { HomePage } from './home'
import { Overview } from './server/overview'
import { GlobalSettingsPage } from './settings'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

// Creators and customers never see the machines: the dashboard sends them
// their machines without names, and their pages show their servers and
// their plan. These machines have names, details and addresses, as the
// owner's list does, so that a page showing one to a customer says so.

const teamCan: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'machines.view']
const customerCan: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create_own']

const owner: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.9',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: true, can: teamCan },
}
const customer: Me = { ...owner, user: { username: 'alex', role: 'member' }, access: { projectId: 'p2345abcde', role: 'admin', servers: { servers: ['abcdefghjk', 'cobblemon1'] }, twoFactor: true, home: 'h2345abcde', can: customerCan } }

const live = {
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
  agentVersion: '0.4.9',
  defaultGamePort: 25565,
  offlineModeTest: false,
  servers: 0,
}
const local: MachineView = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local', live }
const home: MachineView = {
  id: 'h2345abcde',
  projectId: 'p2345abcde',
  name: 'home-server',
  kind: 'remote',
  live: { ...live, hostname: 'home-server' },
  link: { machineId: 'h2345abcde', name: 'home-server', fingerprint: 'X'.repeat(26), state: 'connected', address: '203.0.113.7:41234', problems: [] },
}
const away: MachineView = { ...home, live: undefined, link: { ...home.link!, state: 'offline', lastSeen: new Date(Date.now() - 20 * 60_000).toISOString() } }
/** What names or describes a machine, which a customer's pages never show. */
const machineWords = ['my-vps', 'home-server', '203.0.113.7', 'Ubuntu', 'machines']

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
    gamePort: 25566,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    gameplay: {},
    config: { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', memoryMB: 2048, levelName: 'world', maxPlayers: 10, whitelist: true } as ServerConfig,
    firstSteps: { backedUp: true, downloaded: true },
    machineId: home.id,
    zoneAddress: 'survival.play.example.com',
    ...over,
  }
}
const theirs = [server(), server({ id: 'cobblemon1', name: 'Cobblemon', slug: 'cobblemon', zoneAddress: 'cobblemon.play.example.com' })]
// A customer's catalog is their plan's (capCatalog): 4 GB, 2 GB of it left.
const plan: Catalog = { type: 'paper', types: [], versions: [], memoryOptionsMB: [2048], recommendedMemoryMB: 2048, hostMemoryMB: 4096, maxMemoryMB: 2048, systemReserveMB: 0, memoryFreeMB: 2048, servers: [], image: '' }

/** Answers the dashboard's requests with these machines and servers. */
function serve(machines: MachineView[], servers: ServerStatus[] = theirs) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path === '/api/machines') return Promise.resolve(machines)
    if (path === '/api/servers') return Promise.resolve(servers)
    if (path === '/api/me/prefs') return Promise.resolve({})
    if (path.startsWith('/api/activity')) return Promise.resolve([])
    if (path.includes('/catalog')) return Promise.resolve(plan)
    return new Promise(() => {})
  }) as typeof client.get)
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
})

async function render(me: Me, node: ReactNode) {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () =>
    r.render(
      <WorkspaceProvider me={me} onMe={() => {}} onSignedOut={() => {}}>
        {node}
      </WorkspaceProvider>,
    ),
  )
}

const page = () => document.body.textContent ?? ''
const sidebar = () => document.querySelector('aside')?.textContent ?? ''
const machineLinks = (within = 'body') => document.querySelectorAll(`${within} a[href^="/machines/"], ${within} a[href^="/settings/machines"]`).length

function expectNoMachine(text: string) {
  for (const word of machineWords) expect(text, `shows “${word}”`).not.toContain(word)
}

describe('What a customer sees of the machines', () => {
  it('lists a customer’s servers on Home under no machine, with their plan instead of its memory', async () => {
    serve([local, home])
    await render(customer, <HomePage />)
    await vi.waitFor(() => expect(page()).toContain('2 GB left in your plan'))
    expect(page()).toContain('Cobblemon')
    expect(page()).toContain('2 servers')
    expect(page()).not.toContain('Memory reserved')
    expectNoMachine(page())
    expect(machineLinks()).toBe(0)

    serve([local, home])
    await render(owner, <HomePage />)
    await vi.waitFor(() => expect(page()).toContain('2 servers on 2 machines'))
    expect(page()).toContain('On home-server')
  })

  it('says a customer’s server can’t be reached while its machine is away, never which machine', async () => {
    serve([local, away])
    await render(customer, <HomePage />)
    await vi.waitFor(() => expect(page()).toContain('Cobblemon'))
    const card = [...document.querySelectorAll('article')].find((a) => a.textContent?.includes('Survival'))?.textContent ?? ''
    expect(card).toContain('Can’t reach it')
    expectNoMachine(page())

    await render(customer, <Overview server={theirs[0]!} />)
    await vi.waitFor(() => expect(page()).toContain('Can’t reach your server right now'))
    expect(page()).toContain('Survival may still be running. Playkeeper can’t see it right now.')
    expect(page()).toContain('Can’t check it right now')
    expect(page()).toContain('survival.play.example.com')
    expect(page()).not.toContain('Machine details')
    expectNoMachine(page())
    expect(machineLinks()).toBe(0)
  })

  it('lists a customer’s servers in the sidebar under no machine, with one machine or several', async () => {
    const shell = <AppShell route={{ name: 'home' }}><p>page</p></AppShell>
    serve([local, home])
    await render(customer, shell)
    await vi.waitFor(() => expect(sidebar()).toContain('Cobblemon'))
    expectNoMachine(sidebar())
    expect(machineLinks('aside')).toBe(0)

    // An invited creator's servers go on the dashboard's own machine.
    serve([local], [server({ machineId: local.id, zoneAddress: undefined })])
    await render({ ...customer, access: { ...customer.access, home: local.id } }, shell)
    await vi.waitFor(() => expect(sidebar()).toContain('Survival'))
    expectNoMachine(sidebar())
    expect(machineLinks('aside')).toBe(0)

    serve([local, home])
    await render(owner, shell)
    await vi.waitFor(() => expect(sidebar()).toContain('home-server'))
    expect(sidebar()).toContain('my-vps')
    expect(machineLinks('aside')).toBeGreaterThan(0)
  })

  it('offers a customer no machine in the command palette or Settings', async () => {
    const palette = <CommandPalette open onOpenChange={() => {}} route={{ name: 'home' }} onShortcuts={() => {}} />
    serve([local, home])
    await render(customer, palette)
    await vi.waitFor(() => expect(page()).toContain('Cobblemon'))
    expectNoMachine(page())
    expect(page()).not.toContain('Machine settings')

    await render(customer, <GlobalSettingsPage page={{ name: 'settings' }} />)
    await vi.waitFor(() => expect(page()).toContain('Version 0.4.9'))
    expect([...document.querySelectorAll('a')].map((a) => a.textContent)).not.toContain('Machines')
    expect(page()).not.toContain('Usage stats')
    expectNoMachine(page())

    serve([local, home])
    await render(owner, palette)
    await vi.waitFor(() => expect(page()).toContain('home-server'))
  })

  it('sends a customer who opens a machine’s page to Home', async () => {
    serve([local, home])
    const machinePages: Route[] = [{ name: 'machine', id: home.id }, { name: 'machine', id: home.id, sub: 'disk' }, { name: 'machine-settings', id: local.id }, { name: 'machines' }, { name: 'machine-details', id: home.id }, { name: 'welcome' }]
    for (const route of machinePages) {
      window.history.replaceState(null, '', '/elsewhere')
      await render(customer, <Routes route={route} />)
      await vi.waitFor(() => expect(window.location.pathname, route.name).toBe('/'))
    }
    window.history.replaceState(null, '', `/machines/${local.id}`)
    await render(owner, <Routes route={{ name: 'machine', id: local.id }} />)
    await act(async () => {})
    expect(window.location.pathname).toBe(`/machines/${local.id}`)
  })
})
