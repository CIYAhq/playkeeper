// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, MapInfo, MapPlayers, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { t } from '@/i18n'
import { PublicMapPage } from '../public-map'
import { MapPage } from './map'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const token = 'Ab3dEf6hIj9lMn2pQr5tUv'
const oldToken = 'Zy9xWv8uTs7rQp6oNm5lKj'

const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView

const server = {
  id: 'abcdefghjk',
  name: 'Survival',
  slug: 'survival',
  game: 'minecraft-java',
  type: 'paper',
  exists: true,
  desired: 'running',
  phase: 'online',
  reachable: true,
  gamePort: 25565,
  config: { levelName: 'world' },
} as ServerStatus

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']

const workspace: Workspace = {
  me: {
    user: { username: 'siya', role: 'owner' },
    csrfToken: 't',
    expiresAt: '2026-09-26T00:00:00Z',
    idleTimeoutSeconds: 43200,
    version: '0.3.0',
    access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
  },
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

function mapInfo(over: Partial<MapInfo>): MapInfo {
  return {
    supported: true,
    enabled: true,
    state: 'ready',
    message: 'The map is ready.',
    areas: 12,
    bytes: 3 << 20,
    plugin: 'squaremap',
    estimatedMinutes: 5,
    estimatedMegabytes: 40,
    public: false,
    publicPlayers: false,
    path: '',
    restartWhenEmpty: false,
    checkedAt: '2026-09-25T22:00:00Z',
    ...over,
  }
}

let root: Root | undefined
const writeText = vi.fn<(text: string) => Promise<void>>(() => Promise.resolve())

beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  writeText.mockClear()
})

/** Renders the Map tab with the agent's answer about the map, and who's playing once that loads. */
async function renderMap(info: MapInfo, players?: MapPlayers) {
  vi.mocked(client.get).mockImplementation(((path: string) =>
    path.endsWith('/map') ? Promise.resolve(info) : players && path.endsWith('/map/players') ? Promise.resolve(players) : new Promise(() => {})) as typeof client.get)
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={workspace}>{<MapPage server={server} />}</WorkspaceContext.Provider>))
  await act(async () => {})
}

const copyButton = () => document.querySelector<HTMLButtonElement>(`button[aria-label="${t('map.copyLink')}"]`)

describe('The shared map page', () => {
  it('says the map isn’t available when its first answer fails, instead of loading forever', async () => {
    vi.mocked(client.get).mockImplementation((() => Promise.reject(new client.ApiError(502, { error: 'Bad gateway', code: 'internal' }))) as typeof client.get)
    if (root) await act(async () => root?.unmount())
    document.body.innerHTML = ''
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<PublicMapPage token={token} />))
    await act(async () => {})
    expect(document.body.textContent).toContain('This map isn’t available')
  })
})

describe('Map sharing', () => {
  it('shows and copies the link with its token', async () => {
    const link = `https://play.example.com:8443/map/${token}`
    await renderMap(mapInfo({ public: true, path: `/map/${token}`, link }))
    const text = document.body.textContent ?? ''
    expect(text).toContain(`play.example.com:8443/map/${token}`)
    expect(text).not.toContain('/map/survival')
    expect(text).not.toContain(t('map.noAddress').replace(/<\/?address>/g, ''))
    await act(async () => copyButton()?.click())
    expect(writeText).toHaveBeenCalledWith(link)
  })

  it('uses the address this dashboard was opened on while the machine has no working name', async () => {
    await renderMap(mapInfo({ public: true, path: `/map/${token}` }))
    expect(document.body.textContent).toContain(`${window.location.host}/map/${token}`)
    expect(document.body.textContent).toContain(t('map.noAddress').replace(/<\/?address>/g, ''))
    expect(document.querySelector(`a[href="/machines/${machine.id}/settings"]`)?.textContent).toBe('Set up an address')
    await act(async () => copyButton()?.click())
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/map/${token}`)
  })

  it('shows no link while sharing is off, and only the new one once it is on again', async () => {
    await renderMap(mapInfo({ public: true, path: `/map/${oldToken}`, link: `https://play.example.com:8443/map/${oldToken}` }))
    expect(document.body.textContent).toContain(oldToken)

    await renderMap(mapInfo({ public: false, path: '' }))
    expect(copyButton()).toBeNull()
    expect(document.body.textContent).not.toContain('/map/')

    await renderMap(mapInfo({ public: true, path: `/map/${token}`, link: `https://play.example.com:8443/map/${token}` }))
    expect(document.body.textContent).toContain(token)
    expect(document.body.textContent).not.toContain(oldToken)
  })

  it('waits for the token instead of offering a link without one', async () => {
    await renderMap(mapInfo({ public: true, path: '' }))
    expect(copyButton()).toBeNull()
    expect(document.body.textContent).not.toContain('/map/')
    expect(document.querySelector('[data-slot="skeleton"]')).not.toBeNull()
  })

  it('says why a sharing switch waits while its change is saved', async () => {
    vi.mocked(client.post).mockImplementationOnce(() => new Promise(() => {}))
    await renderMap(mapInfo({}))
    const share = document.querySelector<HTMLElement>('[role="switch"]')
    await act(async () => share?.click())
    expect(client.post).toHaveBeenCalledWith(`/api/servers/${server.id}/map/share`, { public: true })
    expect(share?.hasAttribute('data-disabled')).toBe(true)
    expect(share?.getAttribute('title')).toBe(t('reason.saving'))
  })
})

describe('A map without its plugin', () => {
  it('says squaremap was removed and offers to turn the map on, not that it is on', async () => {
    await renderMap(mapInfo({ enabled: false, missing: true, state: 'not_installed', areas: 0, bytes: 0 }))
    const text = document.body.textContent ?? ''
    expect(text).toContain(t('map.missingTitle', { plugin: 'squaremap' }))
    expect(text).toContain(t('map.missingLead'))
    expect(text).not.toContain(t('map.setupTitle'))
    expect(text).not.toContain(t('map.share'))
    const turnOn = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(t('map.turnOn')))
    await act(async () => turnOn?.click())
    expect(client.post).toHaveBeenCalledWith(`/api/servers/${server.id}/map/enable`)
  })

  it('keeps the first-time words for a map that was never on', async () => {
    await renderMap(mapInfo({ enabled: false, state: 'not_installed' }))
    expect(document.body.textContent).toContain(t('map.setupTitle'))
    expect(document.body.textContent).not.toContain(t('map.missingLead'))
  })
})

describe('Who is playing', () => {
  const playingCard = () => [...document.querySelectorAll('section')].find((c) => c.textContent?.startsWith(t('map.playing')))

  it('shows grey shapes until the players load, not that nobody is playing', async () => {
    await renderMap(mapInfo({}))
    expect(playingCard()?.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(document.body.textContent).not.toContain(t('map.nobody'))
  })

  it('says nobody is playing once the list is in', async () => {
    await renderMap(mapInfo({}), { players: [], updatedAt: '2026-09-25T22:00:00Z' })
    expect(playingCard()?.querySelector('[data-slot="skeleton"]')).toBeNull()
    expect(playingCard()?.textContent).toContain(t('map.nobody'))
  })
})
