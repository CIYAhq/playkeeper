// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { PackPage as PackPageData, PackShare, ServerStatus, ShareText } from '@/api/types'
import { PackShareNotice } from '@/components/app/pack-share'
import { href, parse } from '@/lib/router'
import { PackPage } from './pack'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => new Promise(() => {})),
}))

const token = 'Rk9yZnJpZW5kc09ubHkxMj'
const text = (key: string, english: string, params?: Record<string, string>): ShareText => ({ key, text: english, params })
const needed = text('share.need.required', 'Friends need it')
const optional = text('share.need.optional', 'Optional for friends')
const pack = { name: 'Cobblemon Modpack', version: '26.1.2-5', source: 'modrinth', page: 'https://modrinth.com/modpack/cobblemon-fabric', need: 'required' as const, label: needed }
const notice = text('share.notice.pack_one', 'Friends need the pack plus Waystones', { pack: 'Cobblemon Modpack', mod: 'Waystones' })
const yourself = [
  {
    name: 'Emote Wheel',
    path: 'mods/emote-wheel-1.2.jar',
    page: 'https://www.curseforge.com/minecraft/mc-mods/emote-wheel',
    need: 'required' as const,
    reason: text('share.yourself.curseforge', 'Emote Wheel comes from CurseForge.', { name: 'Emote Wheel', folder: 'mods' }),
  },
  {
    name: 'Trainer HUD',
    path: 'mods/trainer-hud.jar',
    page: 'https://modrinth.com/modpack/cobblemon-fabric',
    need: 'required' as const,
    reason: text('share.yourself.inside_pack', 'Trainer HUD only comes inside the pack.', { name: 'Trainer HUD', pack: 'Cobblemon Modpack', folder: 'mods' }),
  },
]

const page: PackPageData = {
  server: 'Cobblemon',
  minecraftVersion: '26.1.2',
  loader: 'fabric',
  loaderName: 'Fabric',
  loaderVersion: '0.17.2',
  pack,
  notice,
  steps: [],
  launchers: [
    {
      id: 'modrinth-app',
      name: 'Modrinth App',
      site: 'https://modrinth.com/app',
      steps: [
        text('share.launcher.modrinth_app.add', ''),
        text('share.launcher.modrinth_app.pick', '', { file: 'cobblemon.mrpack' }),
        text('share.launcher.modrinth_app.play', ''),
      ],
    },
    {
      id: 'prism',
      name: 'Prism Launcher',
      site: 'https://prismlauncher.org',
      steps: [text('share.launcher.prism.add', ''), text('share.launcher.prism.pick', '', { file: 'cobblemon.mrpack' }), text('share.launcher.prism.launch', '')],
    },
  ],
  mods: [
    { name: 'Balm', version: '21.0.20', from: 'user', need: 'required', label: needed, inFile: true, neededBy: 'Waystones' },
    { name: 'Chunky', version: '1.4.40', from: 'user', need: 'optional', label: optional, inFile: true },
    { name: 'Emote Wheel', version: '1.2', from: 'user', need: 'required', label: needed, inFile: false },
    { name: 'Waystones', version: '21.1.4', from: 'user', need: 'required', label: needed, inFile: true },
    { name: 'Cobblemon', version: '1.7.1', from: 'pack', need: 'required', label: needed, inFile: true },
    { name: 'Sodium', version: '0.9.2', from: 'pack', need: 'optional', label: optional, inFile: true },
    { name: 'Trainer HUD', from: 'pack', need: 'required', label: needed, inFile: false },
  ],
  yourself,
  download: { url: `/packs/${token}/cobblemon.mrpack`, name: 'cobblemon.mrpack', size: 38_912, type: 'application/x-modrinth-modpack+zip' },
  address: 'cobblemon.alex.playkeeper.io',
  hasIcon: true,
}

let root: Root | undefined

async function render(node: ReactNode): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(node))
  await act(async () => {})
  return document.body.textContent ?? ''
}

const body = () => document.body.textContent ?? ''

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === label || el.getAttribute('aria-label') === label)
  if (!b) throw new Error(`no button "${label}" in: ${body()}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

/** Answers the page's data request with status and body, and records every request. */
function answerPage(...answers: { status: number; body?: unknown }[]) {
  const calls: { url: string; init?: RequestInit }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      calls.push({ url, init })
      const a = answers[Math.min(calls.length, answers.length) - 1] ?? { status: 404 }
      return Promise.resolve(new Response(a.body === undefined ? 'This pack isn’t available.' : JSON.stringify(a.body), { status: a.status }))
    }),
  )
  return calls
}

function phone(on: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation(
    (query: string) =>
      ({
        matches: on && query.includes('max-width: 639px'),
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }) as MediaQueryList,
  )
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  vi.mocked(client.post).mockReset()
  vi.mocked(client.post).mockImplementation(() => new Promise(() => {}))
})

describe('the friends’ pack route', () => {
  it('opens /packs/<token>, and a link that can’t be a token as an unavailable page', () => {
    expect(parse(`/packs/${token}`)).toEqual({ name: 'pack', token })
    expect(parse(`/packs/${token}/`)).toEqual({ name: 'pack', token })
    expect(href({ name: 'pack', token })).toBe(`/packs/${token}`)
    for (const bad of ['/packs', '/packs/cobblemon', `/packs/${token}x`, `/packs/${token}/page`, `/packs/${token.slice(0, 21)}-`]) {
      expect(parse(bad), bad).toEqual({ name: 'pack', token: '' })
    }
  })
})

describe('the friends’ pack page', () => {
  it('shows the file, both launchers, the address and only what friends get, loading nothing from other sites', async () => {
    const calls = answerPage({ status: 200, body: page })
    const shown = await render(<PackPage token={token} />)

    expect(calls).toHaveLength(1)
    expect(calls[0]?.url).toBe(`/packs/${token}/page`)
    expect(calls[0]?.init?.credentials).toBe('omit')
    for (const want of [
      'Cobblemon',
      'Minecraft 26.1.2 · Fabric Loader 0.17.2',
      'Get the mods for Cobblemon',
      'Friends need the pack plus Waystones.',
      'cobblemon.mrpack',
      '38 KB · your launcher gets the mods from Modrinth',
      'In the Modrinth App, click + in the sidebar and choose From file.',
      'In Prism Launcher, click Add Instance and choose Import.',
      'Click Browse, pick cobblemon.mrpack and click OK. Pasting the download link works too.',
      'modrinth.com/app',
      'prismlauncher.org',
      'Then join',
      'cobblemon.alex.playkeeper.io',
      '3 mods',
      'Needed by Waystones',
      'Optional for friends',
      'From CurseForge. Put it in the game’s mods folder.',
      'Only inside the pack’s own download. Put it in the mods folder.',
      'CurseForge page',
      'Pack page',
      'Made with Playkeeper',
      'Not an official Minecraft product.',
    ]) {
      expect(shown, want).toContain(want)
    }

    const lists = [...document.querySelectorAll('ul')]
    const names = (ul: Element | undefined) => [...(ul?.querySelectorAll('li') ?? [])].map((li) => li.querySelector('.font-semibold')?.textContent)
    expect(names(lists[0])).toEqual(['Cobblemon Modpack', 'Waystones', 'Balm', 'Chunky'])
    expect(names(lists[1])).toEqual(['Emote Wheel', 'Trainer HUD'])
    // The pack's mods are one row, not a list of their own.
    expect(shown).not.toContain('Sodium')

    const file = document.querySelector<HTMLAnchorElement>('a[download]')
    expect(file?.getAttribute('href')).toBe(`/packs/${token}/cobblemon.mrpack`)
    for (const img of document.querySelectorAll('img')) expect(img.getAttribute('src') ?? '', 'images come from the panel').toMatch(/^(\/|data:)/)
    expect(document.querySelector('img[src$="/icon"]')?.getAttribute('src')).toBe(`/packs/${token}/icon`)
    for (const a of document.querySelectorAll<HTMLAnchorElement>('a[target="_blank"]')) expect(a.rel).toContain('noreferrer')
  })

  it('says the same thing, without the server’s name, for every link that doesn’t open a pack', async () => {
    answerPage({ status: 404 })
    const shown = await render(<PackPage token={token} />)
    expect(shown).toContain('This pack isn’t available')
    expect(shown).toContain('Ask whoever shared it for a new link.')
    expect(shown).not.toContain('Cobblemon')

    const calls = answerPage({ status: 200, body: page })
    expect(await render(<PackPage token="" />)).toBe(shown)
    expect(calls).toHaveLength(0)
  })

  it('offers to try again when the pack can’t be made right now', async () => {
    const calls = answerPage({ status: 503 }, { status: 200, body: page })
    const shown = await render(<PackPage token={token} />)
    expect(shown).toContain('Playkeeper can’t make this pack right now')
    expect(shown).toContain('Try again in a few minutes.')
    await click(button('Try again'))
    expect(calls).toHaveLength(2)
    expect(body()).toContain('Get the mods for Cobblemon')
  })

  it('switches between the launchers on a phone', async () => {
    phone(true)
    answerPage({ status: 200, body: page })
    const shown = await render(<PackPage token={token} />)
    expect(shown).toContain('In the Modrinth App, click + in the sidebar and choose From file.')
    expect(shown).not.toContain('In Prism Launcher, click Add Instance and choose Import.')
    await click(button('Prism Launcher'))
    expect(body()).toContain('In Prism Launcher, click Add Instance and choose Import.')
    expect(body()).not.toContain('In the Modrinth App, click + in the sidebar')
    expect(button('Copy the address')).toBeTruthy()
  })
})

const cobblemon = {
  id: 'k3v9q2m7xw',
  name: 'Cobblemon',
  slug: 'cobblemon',
  game: 'minecraft-java',
  type: 'fabric',
  machineId: 'm2345abcde',
  createdAt: '2026-09-20T10:00:00Z',
  exists: true,
  desired: 'running',
  phase: 'online',
  reachable: true,
  gamePort: 25566,
  offlineModeTest: false,
  crashCount: 0,
  pendingRestart: false,
  gameplay: {},
  firstSteps: { backedUp: false, downloaded: false },
} as ServerStatus

const off: PackShare = {
  public: false,
  file: 'cobblemon.mrpack',
  size: 38_912,
  loaderName: 'Fabric',
  share: {
    server: 'Cobblemon',
    type: 'fabric',
    minecraftVersion: '26.1.2',
    loaderVersion: '0.17.2',
    pack,
    notice,
    mods: [
      { name: 'Waystones', version: '21.1.4', path: 'mods/waystones.jar', from: 'user', onServer: true, need: 'required', label: needed, inFile: true },
      { name: 'spark', version: '1.10.124', path: 'mods/spark.jar', from: 'user', onServer: true, need: 'server_only', label: text('share.need.server_only', 'Server only'), inFile: false },
    ],
    yourself,
  },
}
const shared: PackShare = { ...off, public: true, token }

describe('Share with friends', () => {
  it('shares a page behind a random link, and stopping forgets it', async () => {
    vi.mocked(client.get).mockResolvedValue(off)
    const shown = await render(<PackShareNotice server={cobblemon} />)
    expect(client.get).toHaveBeenCalledWith('/api/servers/k3v9q2m7xw/mods/share')
    expect(shown).toContain('Friends need the pack plus Waystones')
    expect(shown).toContain('Send them one link to set it all up.')

    await click(button('Share with friends'))
    expect(body()).toContain('Send friends what they need')
    expect(body()).toContain('The pack, Waystones and Fabric in one file')
    expect(body()).toContain('Share a page with a link')
    expect(body()).toContain('Anyone with the link sees Cobblemon’s name, versions and the mods friends need. Server-only mods stay hidden.')
    const file = document.querySelector<HTMLAnchorElement>('a[download]')
    expect(file?.getAttribute('href')).toBe('/api/servers/k3v9q2m7xw/mods/share.mrpack')
    expect(file?.textContent).toContain('Download the file')
    expect(document.querySelector('input')).toBeNull()

    vi.mocked(client.post).mockResolvedValueOnce(shared)
    await click(button('Share a link'))
    expect(client.post).toHaveBeenLastCalledWith('/api/servers/k3v9q2m7xw/mods/share', { public: true })
    const link = document.querySelector<HTMLInputElement>('input[readonly]')
    expect(link?.value).toBe(`${window.location.origin}/packs/${token}`)
    expect(link?.value).not.toMatch(/cobblemon|k3v9q2m7xw/i)
    for (const want of [
      'Copy link',
      'Anyone with the link can see the page. It holds names and versions only.',
      'What friends do',
      'Open the link and download the file.',
      'Import it in the Modrinth App or Prism Launcher.',
      `Press Play, then join ${window.location.hostname}:25566.`,
      'Get these yourself',
      'From CurseForge, which doesn’t allow sharing it',
      'Only inside the pack’s own download',
      'Stop sharing',
      'Download the file',
    ]) {
      expect(body(), want).toContain(want)
    }
    expect(body()).not.toContain('spark')

    vi.mocked(client.post).mockResolvedValueOnce(off)
    await click(button('Stop sharing'))
    expect(client.post).toHaveBeenLastCalledWith('/api/servers/k3v9q2m7xw/mods/share', { public: false })
    expect(document.querySelector('input[readonly]')).toBeNull()
    expect(button('Share a link')).toBeTruthy()
  })

  it('puts Copy link and Stop sharing at the bottom of the phone sheet', async () => {
    phone(true)
    vi.mocked(client.get).mockResolvedValue(shared)
    const shown = await render(<PackShareNotice server={cobblemon} />)
    expect(shown).toContain('Send them one link.')
    expect(shown).not.toContain('to set it all up')
    await click(button('Share with friends'))
    expect(body()).toContain(`${window.location.host}/packs/${token}`)
    expect(body()).toContain('Anyone with the link can see the page.')
    expect(body()).not.toContain('It holds names and versions only.')
    expect(button('Copy link')).toBeTruthy()
    expect(button('Stop sharing')).toBeTruthy()
    const curseForge = document.querySelector<HTMLAnchorElement>('a[aria-label="CurseForge page for Emote Wheel (opens in a new tab)"]')
    expect(curseForge?.href).toBe('https://www.curseforge.com/minecraft/mc-mods/emote-wheel')
  })

  it('shows nothing for a server that runs no mods, and a way to try again when the pack can’t be made', async () => {
    vi.mocked(client.get).mockRejectedValue(new client.ApiError(409, { error: 'Survival doesn’t run mods, so there is no pack to share.', code: 'share_unsupported' }))
    expect(await render(<PackShareNotice server={{ ...cobblemon, type: 'paper' }} />)).toBe('')

    vi.mocked(client.get).mockRejectedValueOnce(new client.ApiError(503, { error: 'Playkeeper can’t reach Modrinth.', code: 'upstream' }))
    const shown = await render(<PackShareNotice server={cobblemon} />)
    expect(shown).toContain('Playkeeper can’t make the friends’ pack right now')
    vi.mocked(client.get).mockResolvedValue(off)
    await click(button('Try again'))
    expect(body()).toContain('Friends need the pack plus Waystones')
  })
})
