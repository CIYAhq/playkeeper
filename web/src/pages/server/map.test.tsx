// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, MapArea, MapAreaOption, MapInfo, MapPlayers, Pregen, ServerStatus } from '@/api/types'
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

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover', 'machines.view']

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

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
})

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  writeText.mockClear()
})

/** Renders the Map tab with the agent's answer about the map, and who's playing and the map's area once they load. */
async function renderMap(info: MapInfo, players?: MapPlayers, area?: MapArea, ws: Workspace = workspace) {
  vi.mocked(client.get).mockImplementation(((path: string) =>
    path.endsWith('/map')
      ? Promise.resolve(info)
      : players && path.endsWith('/map/players')
        ? Promise.resolve(players)
        : area && path.endsWith('/map/area')
          ? Promise.resolve(area)
          : new Promise(() => {})) as typeof client.get)
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{<MapPage server={server} />}</WorkspaceContext.Provider>))
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

const sizes: MapAreaOption[] = [
  { id: 'small', radius: 1000, chunks: 16_129, seconds: 720, diskBytes: 157_286_400, fits: true },
  { id: 'medium', radius: 2500, chunks: 99_225, seconds: 5400, diskBytes: 996_147_200, fits: true },
  { id: 'large', radius: 5000, chunks: 393_129, seconds: 21_600, diskBytes: 3_972_844_749, fits: true },
  { id: 'huge', radius: 10_000, chunks: 1_565_001, seconds: 86_400, diskBytes: 15_891_378_995, fits: false },
]

function mapArea(over: Partial<MapArea> = {}, fill: Partial<Pregen> = {}): MapArea {
  return {
    area: 'explored',
    options: sizes,
    fill: { state: 'idle', world: 'world', chunks: 0, total: 0, percent: 0, etaSeconds: -1, pauseForPlayers: true, installed: false, presets: [], ...fill },
    ...over,
  }
}

const running = mapArea({ area: 'medium', radius: 2500 }, { state: 'running', preset: 'medium', radius: 2500, chunks: 41_675, total: 99_225, percent: 42.6, etaSeconds: 5400, installed: true })

async function click(el: Element | null | undefined) {
  if (!(el instanceof HTMLElement)) throw new Error('nothing to click')
  await act(async () => el.click())
  await act(async () => {})
}

const within = () => document.querySelector('[role="dialog"]') ?? document.body
const buttonNamed = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label)
const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((m) => m.textContent?.trim() === label)
const choice = (label: string) => [...within().querySelectorAll('label')].find((l) => l.textContent?.startsWith(label))
const checked = () => within().querySelector('[role="radio"][aria-checked="true"]')?.closest('label')?.textContent ?? ''
const fillCard = () => [...document.querySelectorAll('section')].find((c) => c.textContent?.startsWith(t('mapArea.filling')))

async function openArea() {
  await click(buttonNamed(t('map.options')))
  await click(menuItem(t('mapArea.menu')))
}

describe('The map’s area', () => {
  it('starts filling in a bigger area from the options menu, with what it takes shown first', async () => {
    await renderMap(mapInfo({}), undefined, mapArea())
    await openArea()
    const text = within().textContent ?? ''
    expect(text).toContain(t('mapArea.lead'))
    expect(text).toContain('Explored only Where players have been')
    expect(text).toContain('1,000 blocks about 12 min · 150 MB')
    expect(text).toContain('2,500 blocks Recommended about 1.5 h · 950 MB')
    expect(text).toContain('10,000 blocks Not enough free disk')
    expect(choice('10,000 blocks')?.getAttribute('title')).toBe(t('pregen.noRoom'))
    expect(checked()).toContain('Explored only')
    const start = () => buttonNamed(t('pregen.start'), within())
    expect(start()?.disabled).toBe(true)
    expect(start()?.title).toBe(t('mapArea.unchanged'))
    expect(within().querySelector('[role="switch"]')).toBeNull()

    await click(choice('2,500 blocks')?.querySelector('[role="radio"]'))
    expect(start()?.disabled).toBe(false)
    expect(within().textContent).toContain(t('mapArea.load', { server: 'Survival' }))
    expect(within().textContent).toContain('Installs the Chunky plugin the first time.')
    expect(within().querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true')
    await click(start())
    expect(client.post).toHaveBeenCalledWith(`/api/servers/${server.id}/map/area`, { area: 'medium', pauseForPlayers: true })
  })

  it('shows the area being filled in beside the map, and Explored only stops it', async () => {
    vi.mocked(client.post).mockClear()
    await renderMap(mapInfo({}), undefined, running)
    const card = fillCard()?.textContent ?? ''
    expect(card).toContain('Out to 2,500 blocks')
    expect(card).toContain('42%')
    expect(card).toContain('41,675 of 99,225 chunks')
    expect(card).toContain('About 1.5 hours left')
    expect(card).toContain(t('pregen.pausesForPlayers'))
    expect(fillCard()?.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('43')

    await openArea()
    expect(checked()).toContain('2,500 blocks Filling in · 42%')
    await click(choice('Explored only')?.querySelector('[role="radio"]'))
    expect(within().textContent).toContain(t('mapArea.stopNote'))
    await click(choice('5,000 blocks')?.querySelector('[role="radio"]'))
    expect(within().textContent).toContain(t('mapArea.replaceNote'))
    await click(choice('Explored only')?.querySelector('[role="radio"]'))
    await click(buttonNamed(t('mapArea.stop'), within()))
    expect(client.post).toHaveBeenCalledWith(`/api/servers/${server.id}/map/area`, { area: 'explored', pauseForPlayers: true })
  })

  it('says why a paused fill waits', async () => {
    await renderMap(mapInfo({}), undefined, { ...running, fill: { ...running.fill, state: 'paused', pausedBy: 'players', pausedFor: 'mara_k' } })
    const card = fillCard()
    expect(card?.textContent).toContain('Paused while mara_k plays')
    expect(card?.textContent).not.toContain(t('pregen.pausesForPlayers'))
  })

  it('offers the world border, and keeps what the map has and sizes past the border out of reach', async () => {
    const border: MapAreaOption = { id: 'border', radius: 3000, chunks: 142_129, seconds: 7200, diskBytes: 1_395_864_371, fits: true }
    const options = [...sizes.slice(0, 2), ...sizes.slice(2).map((o) => ({ ...o, pastBorder: true })), border]
    await renderMap(mapInfo({}), undefined, mapArea({ options }))
    await openArea()
    expect(within().textContent).toContain('Up to the world border 3,000 blocks · about 2 h · 1.3 GB')
    expect(choice('5,000 blocks')?.getAttribute('title')).toBe(t('mapArea.pastBorder'))
    expect(choice('5,000 blocks')?.querySelector('[role="radio"]')?.hasAttribute('data-disabled')).toBe(true)
    await click(choice('Up to the world border')?.querySelector('[role="radio"]'))
    await click(buttonNamed(t('pregen.start'), within()))
    expect(client.post).toHaveBeenCalledWith(`/api/servers/${server.id}/map/area`, { area: 'border', pauseForPlayers: true })

    const done = [...sizes.slice(0, 2).map((o) => ({ ...o, done: true })), ...sizes.slice(2).map((o) => ({ ...o, pastBorder: true })), { ...border, done: true }]
    await renderMap(mapInfo({}), undefined, mapArea({ area: 'border', radius: 3000, options: done }, { state: 'finished', preset: 'border', radius: 3000, percent: 100, installed: true }))
    expect(fillCard()).toBeUndefined()
    await openArea()
    expect(checked()).toContain('Up to the world border')
    expect(choice('Explored only')?.textContent).toContain(t('mapArea.stays'))
    expect(choice('Explored only')?.querySelector('[role="radio"]')?.hasAttribute('data-disabled')).toBe(true)
    expect(choice('1,000 blocks')?.getAttribute('title')).toBe(t('mapArea.onMap'))
    expect(buttonNamed(t('pregen.start'), within())?.disabled).toBe(true)
  })

  it('is left out for those who can’t change the map, who still see an area being filled in', async () => {
    const can = everything.filter((a) => a !== 'servers.manage')
    const moderator: Workspace = { ...workspace, me: { ...workspace.me, access: { ...workspace.me.access, role: 'moderator', can } } }
    await renderMap(mapInfo({}), undefined, running, moderator)
    await click(buttonNamed(t('map.options')))
    expect(menuItem(t('map.turnOffMenu'))).toBeDefined()
    expect(menuItem(t('mapArea.menu'))).toBeUndefined()
    expect(fillCard()).toBeDefined()

    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    try {
      await renderMap(mapInfo({}), undefined, running, moderator)
      await click(buttonNamed(t('map.settings')))
      expect(document.body.textContent).toContain(t('map.turnOffMenu'))
      expect(buttonNamed(`${t('mapArea.row')}2,500 blocks`)).toBeUndefined()
    } finally {
      phone.mockRestore()
    }
  })

  it('opens from the phone’s Map settings, which say what the area is', async () => {
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    try {
      await renderMap(mapInfo({}), undefined, running)
      expect(fillCard()?.textContent).toContain('41,675 of 99,225 chunks')
      await click(buttonNamed(t('map.settings')))
      const row = buttonNamed(`${t('mapArea.row')}2,500 blocks`)
      expect(row).toBeDefined()
      await click(row)
      expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t('mapArea.title'))
      expect(checked()).toContain('2,500 blocks')
    } finally {
      phone.mockRestore()
    }
  })
})

describe('The phone’s Map header', () => {
  it('stays at the top like the other phone headers, with the way back, the title and the map’s settings', async () => {
    const phone = vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: query === '(max-width: 639px)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
    try {
      await renderMap(mapInfo({}))
      const header = document.querySelector('header[data-sticky-header]')
      expect(header?.className.split(' ')).toEqual(expect.arrayContaining(['sticky', 'top-0']))
      expect(header?.querySelector('a')?.textContent).toBe(t('nav.more'))
      expect(header?.querySelector('h1')?.textContent).toBe(t('tab.map'))
      expect(header?.querySelector(`button[aria-label="${t('map.settings')}"]`)).not.toBeNull()
    } finally {
      phone.mockRestore()
    }
  })
})
