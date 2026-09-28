// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, Crossplay, MachineView, Me, PublicPage, PublicServer, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { PlayerFace } from '@/components/app/bits'
import { serverPageApi } from '@/lib/server-page'
import { ServerPage as PublicServerPage } from '../server-page'
import { BedrockJoin, CrossplayRows } from './crossplay'
import { SleepRows } from './sleep'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'servers.run', 'players.manage', 'servers.manage']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-29T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.3',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.2', minecraftVersion: '26.2', paperBuild: 12, memoryMB: 3072, heapMB: 2048, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true } as ServerConfig
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
  firstSteps: { backedUp: true, downloaded: true },
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

let root: Root | undefined

async function render(node: React.ReactNode, view?: Crossplay, reads: Record<string, unknown> = {}) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (view && path.endsWith('/crossplay')) return Promise.resolve(view)
    if (path in reads) return Promise.resolve(reads[path])
    return new Promise(() => {})
  }) as typeof client.get)
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
}

function toggle(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="switch"]')
  if (!el) throw new Error('no switch')
  return el
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === label)
  if (!b) throw new Error(`no button ${label}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
})

describe('the crossplay switch', () => {
  it('says what turning it on does, with the port, and turns it on', async () => {
    await render(<CrossplayRows server={server} />, { on: false, available: true, port: 19133, plugins: [], prefix: '.' })
    expect(document.body.textContent).toContain('Friends on phones, tablets, consoles and Windows join too')
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    await click(toggle())
    const text = document.body.textContent ?? ''
    expect(text).toContain('Let Bedrock friends join')
    expect(text).toContain('Geyser from Modrinth · Floodgate from Hangar')
    expect(text).toContain('UDP 19133')
    expect(text).toContain('Open UDP 19133 in your provider’s firewall too.')
    expect(text).toContain('Survival restarts for about 20 s.')
    vi.mocked(client.post).mockResolvedValue({})
    await click(button('Turn on crossplay'))
    expect(vi.mocked(client.post).mock.calls).toEqual([['/api/servers/abcdefghjk/crossplay', { on: true }]])
  })

  it('says a sleeping server wakes up and stays awake, since Bedrock friends can’t wake it', async () => {
    const asleep: ServerStatus = { ...server, desired: 'sleeping', phase: 'stopped', sleep: { enabled: true, idleMinutes: 15, listening: true } }
    await render(<CrossplayRows server={asleep} />, { on: false, available: true, port: 19132, plugins: [], prefix: '.' })
    await click(toggle())
    expect(document.body.textContent).toContain('Survival wakes up now and stays awake while crossplay is on: a Bedrock friend can’t wake it.')
    await render(<SleepRows server={{ ...server, config: { ...config, crossplayPort: 19132 }, sleep: { enabled: true, idleMinutes: 15, listening: false } }} />)
    expect(document.body.textContent).toContain('Stays awake while crossplay is on: a Bedrock friend can’t wake a sleeping server.')
  })

  it('is off and says why where crossplay can’t be turned on', async () => {
    const notice = { kind: 'not_for_server_type', message: 'Crossplay is only offered for Paper and Purpur servers; this server runs Fabric.' }
    await render(<CrossplayRows server={{ ...server, type: 'fabric' }} />, { on: false, available: false, notice, plugins: [], prefix: '.' })
    expect(toggle().getAttribute('aria-disabled') === 'true' || toggle().hasAttribute('data-disabled')).toBe(true)
    expect(toggle().title).toBe(notice.message)
    expect(document.body.textContent).toContain(notice.message)
  })

  it('shows where Bedrock friends join once on, and turns off after asking', async () => {
    const on = { ...server, config: { ...config, crossplayPort: 19132 }, bedrock: { host: 'alex.playkeeper.me', port: 19132 } }
    await render(<CrossplayRows server={on} />, { on: true, available: true, port: 19132, plugins: [], prefix: '.' })
    expect(toggle().getAttribute('aria-checked')).toBe('true')
    const text = document.body.textContent ?? ''
    expect(text).toContain('alex.playkeeper.me')
    expect(text).toContain('port 19132')
    expect(text).toContain('Xbox, PlayStation and Switch need BedrockConnect.')
    expect(text).toContain('Bedrock names start with a dot, like .Steve.')
    await click(toggle())
    expect(document.body.textContent).toContain('Geyser and Floodgate come out and UDP 19132 closes.')
    vi.mocked(client.post).mockResolvedValue({})
    await click(button('Turn off crossplay'))
    expect(vi.mocked(client.post).mock.calls).toEqual([['/api/servers/abcdefghjk/crossplay', { on: false }]])
  })
})

describe('the Join card’s Bedrock line', () => {
  it('names the machine and the port while crossplay is on', async () => {
    await render(<BedrockJoin server={{ ...server, bedrock: { host: 'alex.playkeeper.me', port: 19133 } }} />)
    const text = document.body.textContent ?? ''
    expect(text).toContain('Bedrock')
    expect(text).toContain('alex.playkeeper.me')
    expect(text).toContain('port 19133')
    expect(text).toContain('Servers › Add Server, with this port.')
    expect(document.querySelector('a[href^="https://geysermc.org/wiki/geyser/using-geyser-with-consoles"]')?.textContent).toContain('Consoles')
  })

  it('falls back to the address the dashboard is open at without a name', async () => {
    await render(<BedrockJoin server={{ ...server, bedrock: { port: 19132 } }} />)
    expect(document.body.textContent).toContain(`${window.location.hostname}port 19132`)
  })

  it('is not there without crossplay', async () => {
    await render(<BedrockJoin server={server} />)
    expect(document.body.textContent).toBe('')
  })
})

describe('the public page’s Bedrock line', () => {
  const page = (over: Partial<PublicServer>): PublicPage => ({
    address: 'alex.playkeeper.me',
    servers: [{ slug: 'survival', name: 'Survival', motd: 'Hi', address: 'alex.playkeeper.me', state: 'online', minecraftVersion: '26.2', type: 'paper', inviteOnly: false, hasIcon: false, ...over }],
  })

  it('gives the address, the port and how Bedrock players join, with the allowlist in mind', async () => {
    await render(<PublicServerPage />, undefined, { [serverPageApi]: page({ inviteOnly: true, bedrock: { host: 'alex.playkeeper.me', port: 19133 } }) })
    const text = document.body.textContent ?? ''
    expect(text).toContain('On Bedrock Edition')
    expect(text).toContain('port 19133')
    expect(text).toContain('Servers › Add Server, and enter this address with port 19133.')
    expect(text).toContain('ask whoever runs the server to add your Xbox gamertag')
    expect(document.querySelector('a[href^="https://geysermc.org/wiki/geyser/using-geyser-with-consoles"]')?.textContent).toContain('Xbox, PlayStation or Switch')
  })

  it('is not there without crossplay', async () => {
    await render(<PublicServerPage />, undefined, { [serverPageApi]: page({}) })
    expect(document.body.textContent).toContain('Server address')
    expect(document.body.textContent).not.toContain('Bedrock')
  })
})

describe('a Bedrock player’s face', () => {
  it('is their initial, without asking for a Java skin', async () => {
    await render(<PlayerFace name=".Notch" />)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('[role="img"]')?.textContent).toBe('N')
    await render(<PlayerFace name="Notch" />)
    expect(document.querySelector('img')?.getAttribute('src')).toBe('/api/players/Notch/head')
  })
})
