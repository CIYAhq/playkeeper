// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, DataPacks, MachineView, Me, Pregen, PregenPreset, ResourcePack, ResourcePackOffer, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { WorldTools } from './world-links'
import { isLocalHost, otherHost, packsLine, PacksPage, packTitle, zipProblem } from './world-packs'
import { firstPreset, longTime, pausedText, pregenLine, PregenPage, shortTime } from './world-pregen'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve({})),
  api: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 1536, heapMB: 1024, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true, playStyle: 'friends' } as ServerConfig

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
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    gameplay: {},
    config,
    firstSteps: { backedUp: false, downloaded: false },
    ...over,
  }
}

function workspace(over: Partial<Workspace> = {}): Workspace {
  return {
    me,
    servers: [server()],
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
    ...over,
  }
}

const presets: PregenPreset[] = [
  { id: 'small', radius: 1000, chunks: 16_129, seconds: 300, diskBytes: 95 * 2 ** 20, fits: true },
  { id: 'medium', radius: 2000, chunks: 63_001, seconds: 1320, diskBytes: 380 * 2 ** 20, fits: true },
  { id: 'large', radius: 3000, chunks: 142_129, seconds: 5400, diskBytes: 860 * 2 ** 20, fits: true },
  { id: 'huge', radius: 5000, chunks: 393_129, seconds: 4 * 3600, diskBytes: 2.3 * 2 ** 30, fits: false },
]

function pregen(over: Partial<Pregen> = {}): Pregen {
  return { state: 'idle', world: 'world', chunks: 0, total: 0, percent: 0, etaSeconds: -1, pauseForPlayers: true, installed: false, presets, diskFreeBytes: 41 * 2 ** 30, ...over }
}

const sha1 = 'a94a8fe5ccb19ba61c4c0873d391e987982fbbd3'
const offer: ResourcePackOffer = {
  sha1,
  fileName: 'Faithful_32x.zip',
  size: 4.2 * 2 ** 20,
  addedAt: new Date(Date.now() - 2 * 86400_000).toISOString(),
  url: `http://play.example.com:8451/resource-packs/${sha1}.zip`,
  required: true,
  prompt: 'Grab our pack for the full look!',
}

/** In name order, as the server sends them. */
function dataPacks(live = true): DataPacks {
  return {
    live,
    packs: [
      { name: 'Graves.zip', size: 30_000, enabled: live ? true : undefined, addedAt: '2026-09-24T10:01:00Z' },
      { name: 'More_mob_heads.zip', size: 800_000, enabled: live ? false : undefined, addedAt: '2026-09-24T10:02:00.5Z' },
      { name: 'Multiplayer_sleep.zip', description: 'Skip the night when half are asleep', size: 12_000, enabled: live ? true : undefined, addedAt: '2026-09-24T10:00:00Z' },
      { name: 'coordinates_hud', size: 0, folder: true, enabled: live ? true : undefined, addedAt: '2026-09-24T10:02:00Z' },
    ],
  }
}

/** Answers GETs by path suffix; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([suffix]) => path.endsWith(suffix))
    return hit ? Promise.resolve(hit[1]) : new Promise(() => {})
  }) as typeof client.get)
}

let root: Root | undefined

async function render(node: ReactNode, ws: Workspace = workspace()): Promise<string> {
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
  return document.body.textContent ?? ''
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === label || el.getAttribute('aria-label') === label)
  if (!b) throw new Error(`no button ${label}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

function switches(): Record<string, string | null> {
  return Object.fromEntries([...document.querySelectorAll('[role="switch"]')].map((el) => [el.getAttribute('aria-label') ?? el.closest('label')?.textContent ?? '', el.getAttribute('aria-checked')]))
}

const happyDOM = (window as unknown as { happyDOM: { setURL(url: string): void; setViewport(v: { width: number; height: number }): void } }).happyDOM

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  vi.mocked(client.post).mockClear()
  vi.mocked(client.api).mockClear()
  happyDOM.setViewport({ width: 1024, height: 768 })
  happyDOM.setURL('http://localhost:3000/')
})

describe('Pre-generation words', () => {
  it('rounds times the way people say them', () => {
    expect(shortTime(20)).toBe('1 min')
    expect(shortTime(14 * 60)).toBe('14 min')
    expect(longTime(14 * 60)).toBe('14 minutes')
    expect(shortTime(59 * 60 + 40)).toBe('1 h')
    expect(shortTime(1.5 * 3600)).toBe('1.5 h')
    expect(longTime(1.5 * 3600)).toBe('1.5 hours')
    expect(shortTime(9.8 * 3600)).toBe('10 h')
    expect(shortTime(47 * 3600)).toBe('47 h')
    expect(longTime(3 * 86400)).toBe('3 days')
  })

  it('sums up the task on the World card', () => {
    expect(pregenLine(undefined, 'Survival')).toBe('So exploring doesn’t lag')
    expect(pregenLine(pregen(), 'Survival')).toBe('So exploring doesn’t lag')
    expect(pregenLine(pregen({ state: 'starting', step: 'installing' }), 'Survival')).toBe('Installing Chunky…')
    expect(pregenLine(pregen({ state: 'starting', step: 'restarting' }), 'Survival')).toBe('Restarting Survival to load Chunky…')
    expect(pregenLine(pregen({ state: 'running', percent: 42.7, etaSeconds: 5400 }), 'Survival')).toBe('Pre-generating · 42% · about 1.5 h left')
    expect(pregenLine(pregen({ state: 'running', percent: 99.6, etaSeconds: -1 }), 'Survival')).toBe('Pre-generating · 99%')
    expect(pregenLine(pregen({ state: 'paused', percent: 42.7, pausedBy: 'user' }), 'Survival')).toBe('Paused at 42%')
    expect(pregenLine(pregen({ state: 'finished', radius: 2000, percent: 100 }), 'Survival')).toBe('Ready out to 2,000 blocks')
  })

  it('says why a task is paused', () => {
    expect(pausedText(pregen({ state: 'paused', pausedBy: 'players', pausedFor: 'mara_k' }), 'Survival')).toBe('Paused while mara_k plays')
    expect(pausedText(pregen({ state: 'paused', pausedBy: 'players' }), 'Survival')).toBe('Paused while people play')
    expect(pausedText(pregen({ state: 'paused', pausedBy: 'server' }), 'Survival')).toBe('Paused while Survival is stopped')
    expect(pausedText(pregen({ state: 'paused', pausedBy: 'user' }), 'Survival')).toBe('Paused')
  })

  it('picks Medium first, the biggest smaller size that fits, or the next size after a finished one', () => {
    expect(firstPreset(presets)).toBe('medium')
    expect(firstPreset(presets.map((p) => ({ ...p, fits: p.id === 'small' })))).toBe('small')
    expect(firstPreset(presets.map((p) => ({ ...p, fits: false })))).toBe('medium')
    expect(firstPreset(presets, 2000)).toBe('large')
    expect(firstPreset(presets, 3000)).toBe('huge')
  })
})

describe('Pre-generate page', () => {
  it('offers the sizes with what each takes, Medium first', async () => {
    answer({ '/pregen': pregen() })
    const text = await render(<PregenPage server={server()} />)
    expect(text).toContain('How far out from spawn?')
    expect(text).toContain('MediumRecommended2,000 blocksabout 22 min · 380 MB')
    expect(text).toContain('Large3,000 blocksabout 1.5 h · 860 MB')
    expect(text).toContain('Huge5,000 blocksNot enough free disk')
    expect(text).toContain('Installs the Chunky plugin the first time. Learn more')
    expect(text).toContain('my-vps has 41 GB free')
    expect(document.querySelector('[role="radio"][aria-checked="true"]')?.closest('label')?.textContent).toContain('Medium')
    expect(document.querySelector('a[href="https://modrinth.com/plugin/chunky"]')?.getAttribute('target')).toBe('_blank')
    await click(button('Start'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/pregen/start', { preset: 'medium', pauseForPlayers: true })
  })

  it('says a mod is installed on mod servers, and nothing once Chunky is there', async () => {
    answer({ '/pregen': pregen() })
    expect(await render(<PregenPage server={server({ type: 'fabric' })} />)).toContain('Installs the Chunky mod the first time.')
    answer({ '/pregen': pregen({ installed: true }) })
    expect(await render(<PregenPage server={server()} />)).not.toContain('Installs the Chunky')
  })

  it('waits for another job before starting', async () => {
    answer({ '/pregen': pregen() })
    const operation = { id: 'backup-1', kind: 'backup', status: 'running' as const, phase: 'archiving', actor: 'siya', startedAt: new Date().toISOString() }
    await render(<PregenPage server={server({ operation })} />)
    expect(button('Start').disabled).toBe(true)
    expect(button('Start').title).toBe('Backing up Survival. Try again when it’s done.')
  })

  it('says why a size that doesn’t fit can’t be started', async () => {
    answer({ '/pregen': pregen({ presets: pregen().presets.map((p) => ({ ...p, fits: p.id === 'small' })) }) })
    await render(<PregenPage server={server()} />)
    expect(document.querySelector('[role="radio"][aria-checked="true"]')?.closest('label')?.textContent).toContain('Small')
    expect(button('Start').title).toBe('')
    expect(document.querySelector('label[title="Not enough free disk"]')?.textContent).toContain('Medium')
  })

  it('follows a running task with its progress', async () => {
    answer({ '/pregen': pregen({ state: 'running', preset: 'medium', radius: 2000, chunks: 26_460, total: 63_001, percent: 42, etaSeconds: 5400, installed: true }) })
    const text = await render(<PregenPage server={server()} />)
    expect(text).toContain('Medium · out to 2,000 blocks from spawn')
    expect(text).toContain('42%')
    expect(text).toContain('26,460 of 63,001 chunks')
    expect(text).toContain('About 1.5 hours left')
    expect(text).toContain('Pauses when someone joins.')
    expect(document.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('42')
    await click(button('Pause'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/pregen/pause')
    await click(button('Cancel'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/pregen/cancel')
  })

  it('holds the other button back while pausing', async () => {
    answer({ '/pregen': pregen({ state: 'running', preset: 'medium', radius: 2000, chunks: 26_460, total: 63_001, percent: 42, etaSeconds: 5400, installed: true }) })
    await render(<PregenPage server={server()} />)
    vi.mocked(client.post).mockImplementationOnce(() => new Promise(() => {}))
    await click(button('Pause'))
    expect(button('Cancel').disabled).toBe(true)
    expect(button('Cancel').title).toBe('Pausing. Try again when it’s done.')
  })

  it('says who a paused task waits for, and resumes it', async () => {
    answer({ '/pregen': pregen({ state: 'paused', preset: 'medium', radius: 2000, chunks: 26_460, total: 63_001, percent: 42, pausedBy: 'players', pausedFor: 'mara_k', installed: true }) })
    const text = await render(<PregenPage server={server()} />)
    expect(text).toContain('Paused while mara_k plays')
    expect(text).not.toContain('Pauses when someone joins.')
    await click(button('Resume'))
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/pregen/continue')
  })

  it('shows Chunky being installed in plain words, without buttons', async () => {
    answer({ '/pregen': pregen({ state: 'starting', step: 'installing', preset: 'small', radius: 1000 }) })
    const text = await render(<PregenPage server={server()} />)
    expect(text).toContain('Installing Chunky…')
    expect(() => button('Pause')).toThrow()
  })

  it('celebrates a finished map and offers the next size', async () => {
    answer({ '/pregen': pregen({ state: 'finished', preset: 'medium', radius: 2000, chunks: 63_001, total: 63_001, percent: 100, elapsedSeconds: 1320, diskBytes: 380 * 2 ** 20, installed: true }) })
    const text = await render(<PregenPage server={server()} />)
    expect(text).toContain('The map is ready out to 2,000 blocks')
    expect(text).toContain('63,001 chunks in 22 minutes · 380 MB')
    await click(button('Go further'))
    expect(document.querySelector('[role="radio"][aria-checked="true"]')?.closest('label')?.textContent).toContain('Large')
  })

  it('has nowhere further to go after the biggest size', async () => {
    answer({ '/pregen': pregen({ state: 'finished', preset: 'huge', radius: 5000, chunks: 393_129, total: 393_129, percent: 100, installed: true }) })
    await render(<PregenPage server={server()} />)
    expect(() => button('Go further')).toThrow()
  })

  it('explains which servers can pre-generate', async () => {
    vi.mocked(client.get).mockRejectedValue(new client.ApiError(400, { error: 'Chunky needs Paper, Fabric or NeoForge.', code: 'unsupported_server' }))
    const text = await render(<PregenPage server={server({ type: 'vanilla' })} />)
    expect(text).toContain('Pre-generating needs a Paper, Fabric or NeoForge server.')
    expect(text).not.toContain('Try again')
  })

  it('lists the sizes as rows on a phone', async () => {
    happyDOM.setViewport({ width: 390, height: 844 })
    answer({ '/pregen': pregen() })
    const text = await render(<PregenPage server={server()} />)
    expect(document.querySelector('h1')?.textContent).toBe('Pre-generate')
    expect(text).toContain('MediumRecommended2,000 blocks · about 22 min · 380 MB')
    expect(text).not.toContain('Learn more')
    expect(text).not.toContain('GB free')
    expect(document.querySelectorAll('[role="radio"]')).toHaveLength(4)
  })
})

describe('Pack words', () => {
  it('names packs the way people say them', () => {
    expect(packTitle('Faithful_32x.zip')).toBe('Faithful 32x')
    expect(packTitle('More-Mob-Heads.ZIP')).toBe('More-Mob-Heads')
    expect(packTitle('.zip')).toBe('.zip')
  })

  it('knows the addresses players’ games could never download from', () => {
    for (const h of ['localhost', 'LOCALHOST.', 'panel.localhost', '127.0.0.1', '127.1.2.3', '[::1]', '0.0.0.0', '[::]', '169.254.10.1', '[fe80::1]']) expect(isLocalHost(h), h).toBe(true)
    for (const h of ['play.example.com', '203.0.113.7', '10.0.0.5', '192.168.1.20', '[2001:db8::1]']) expect(isLocalHost(h), h).toBe(false)
  })

  it('names the address players download from when it isn’t this one', () => {
    expect(otherHost(offer, 'play.example.com')).toBeUndefined()
    expect(otherHost(offer, 'PLAY.EXAMPLE.COM')).toBeUndefined()
    expect(otherHost(offer, '203.0.113.7')).toBe('play.example.com')
    expect(otherHost({ ...offer, url: 'not a url' }, '203.0.113.7')).toBeUndefined()
  })

  it('checks a file before uploading it', () => {
    expect(zipProblem(new File(['x'], 'pack.rar'))).toBe('Choose a .zip file.')
    expect(zipProblem({ name: 'big.zip', size: 262_144_001 } as File)).toBe('Packs can be up to 250 MB.')
    expect(zipProblem(new File(['x'], 'Graves.ZIP'))).toBeUndefined()
  })

  it('sums up the packs on the World card', () => {
    expect(packsLine(undefined, undefined)).toBe('Sent to players automatically')
    expect(packsLine({ pending: false }, { packs: [], live: true })).toBe('Sent to players automatically')
    expect(packsLine({ offer, pending: false }, dataPacks())).toBe('Faithful 32x · 3 of 4 data packs on')
    expect(packsLine({ offer, pending: false }, { packs: [], live: true })).toBe('Faithful 32x')
    expect(packsLine({ offer, pending: false, problem: 'Upload the pack again.' }, { packs: [], live: true })).toBe('Faithful 32x can’t be offered')
    expect(packsLine({ pending: false }, { live: false, packs: dataPacks(false).packs.slice(0, 1) })).toBe('1 data pack')
  })
})

describe('Packs page', () => {
  beforeEach(() => happyDOM.setURL('https://play.example.com:8451/servers/survival/world/packs'))

  it('starts empty with a place to drop each kind of pack', async () => {
    answer({ '/resourcepack': { pending: false } satisfies ResourcePack, '/datapacks': { packs: [], live: true } satisfies DataPacks })
    const text = await render(<PacksPage server={server()} />)
    expect(text).toContain('Drop a resource pack .zip here, or choose a file')
    expect(text).toContain('No data packs yet')
    expect(text).toContain('Add recipes, rules and small features.')
    expect(text).toContain('Applies when players next join.')
    expect(text).toContain('Applies right away.')
    expect(text).not.toContain('can’t download')
  })

  it('shows the offered pack with its settings, and each data pack with a switch', async () => {
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': dataPacks() })
    const text = await render(<PacksPage server={server()} />)
    expect(text).toContain('Faithful 32x')
    expect(text).toContain('4.2 MB · added 2 days ago')
    expect(text).not.toContain('Players download it from')
    expect(document.querySelector<HTMLInputElement>('input[placeholder^="e.g."]')?.value).toBe('Grab our pack for the full look!')
    expect(text).toContain('Skip the night when half are asleep')
    expect(switches()).toEqual({ 'Players must accept it to join': 'true', 'Multiplayer sleep': 'true', Graves: 'true', 'More mob heads': 'false', 'coordinates hud': 'true' })
    expect(Object.keys(switches()).slice(1)).toEqual(['Multiplayer sleep', 'Graves', 'coordinates hud', 'More mob heads'])
    expect(document.querySelectorAll('[aria-label^="Actions for"]')).toHaveLength(3)
  })

  it('switches a data pack off and saves the pack settings', async () => {
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': dataPacks() })
    await render(<PacksPage server={server()} />)
    await click(document.querySelector<HTMLElement>('[role="switch"][aria-label="Graves"]') as HTMLElement)
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/datapacks/Graves.zip/disable')
    await click(document.querySelector('label [role="switch"]')?.closest('label') as HTMLElement)
    expect(client.post).toHaveBeenCalledWith('/api/servers/abcdefghjk/resourcepack/settings', { required: false, prompt: 'Grab our pack for the full look!' })
  })

  it('lets a removed data pack fade out once the server has deleted it, before the list reloads', async () => {
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': dataPacks() })
    await render(<PacksPage server={server()} />)
    await click(button('Actions for Graves'))
    let reload: (dp: DataPacks) => void = () => {}
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': new Promise<DataPacks>((done) => (reload = done)) })
    await click(document.querySelector<HTMLElement>('[role="menuitem"]') as HTMLElement)
    expect(client.del).toHaveBeenCalledWith('/api/servers/abcdefghjk/datapacks/Graves.zip')
    const leaving = document.querySelector('li[data-leaving]')
    expect(leaving?.textContent).toContain('Graves')
    expect(leaving?.hasAttribute('inert')).toBe(true)
    await act(async () => new Promise((done) => window.setTimeout(done, 250)))
    expect(document.body.textContent).not.toContain('Graves')
    await act(async () => reload({ ...dataPacks(), packs: dataPacks().packs.filter((p) => p.name !== 'Graves.zip') }))
    await act(async () => {})
    expect(Object.keys(switches())).toEqual(['Players must accept it to join', 'Multiplayer sleep', 'coordinates hud', 'More mob heads'])
  })

  it.each([1024, 390])('says why the pack can’t be offered, and never that it applies, %i px wide', async (width) => {
    happyDOM.setViewport({ width, height: 844 })
    const problem = 'The message shown to players must be one line of at most 200 characters, without percent signs or backslashes. Change the message players see, or remove the pack.'
    answer({ '/resourcepack': { offer, pending: false, problem } satisfies ResourcePack, '/datapacks': dataPacks() })
    const text = await render(<PacksPage server={server()} />)
    expect(text).toContain(`Survival can’t offer this pack ${problem}`)
    expect(text).toContain('Until it’s fixed, players get what Survival offered before.')
    expect(text).not.toContain('Applies when players next join.')
  })

  it('says when changes apply while the server is stopped', async () => {
    answer({ '/resourcepack': { offer, pending: true } satisfies ResourcePack, '/datapacks': dataPacks(false) })
    const text = await render(<PacksPage server={server({ phase: 'stopped' })} />)
    expect(text).toContain('Applies after Survival restarts.')
    expect(text).toContain('Applies when Survival starts.')
    expect(Object.keys(switches())).toEqual(['Players must accept it to join'])
  })

  it('uploads a data pack and refuses other files', async () => {
    answer({ '/resourcepack': { pending: false } satisfies ResourcePack, '/datapacks': { packs: [], live: true } satisfies DataPacks })
    await render(<PacksPage server={server()} />)
    const input = document.querySelectorAll<HTMLInputElement>('input[type="file"]')[1] as HTMLInputElement
    const choose = async (file: File) => {
      Object.defineProperty(input, 'files', { value: [file], configurable: true })
      await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
      await act(async () => {})
    }
    await choose(new File(['x'], 'notes.txt'))
    expect(client.api).not.toHaveBeenCalled()
    const pack = new File(['x'], 'Graves.zip')
    await choose(pack)
    expect(client.api).toHaveBeenCalledWith('POST', '/api/servers/abcdefghjk/datapacks?name=Graves.zip', undefined, pack)
  })

  it('names the address players download from when the dashboard is open at another', async () => {
    happyDOM.setURL('https://203.0.113.7:8451/servers/survival/world/packs')
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': dataPacks() })
    expect(await render(<PacksPage server={server()} />)).toContain('Players download it from play.example.com')
  })

  it('warns that players can’t download from a local address, and holds uploads back', async () => {
    happyDOM.setURL('http://localhost:8451/servers/survival/world/packs')
    answer({ '/resourcepack': { pending: false } satisfies ResourcePack, '/datapacks': { packs: [], live: true } satisfies DataPacks })
    const text = await render(<PacksPage server={server()} />)
    expect(text).toContain('Players can’t download packs from localhost. Open the dashboard at the address players join with.')
    expect(button('Drop a resource pack .zip here, or choose a file').disabled).toBe(true)
    expect(button('Drop a resource pack .zip here, or choose a file').title).toBe('Players can’t download packs from localhost. Open the dashboard at the address players join with.')
  })

  it('says why nothing can change while another job runs', async () => {
    answer({ '/resourcepack': { offer, pending: false } satisfies ResourcePack, '/datapacks': dataPacks() })
    const operation = { id: 'backup-1', kind: 'backup', status: 'running' as const, phase: 'archiving', actor: 'siya', startedAt: new Date().toISOString() }
    await render(<PacksPage server={server({ operation })} />)
    const why = 'Backing up Survival. Try again when it’s done.'
    for (const label of ['Upload data pack', 'Remove', 'Drop a .zip to replace it', 'Actions for Graves']) {
      expect(button(label).disabled, label).toBe(true)
      expect(button(label).title, label).toBe(why)
    }
    const graves = document.querySelector<HTMLElement>('[role="switch"][aria-label="Graves"]')
    expect(graves?.getAttribute('title')).toBe(why)
    expect(graves?.hasAttribute('data-disabled')).toBe(true)
    expect(document.querySelector('label [role="switch"]')?.closest('label')?.getAttribute('title')).toBe(why)
  })
})

describe('World card rows', () => {
  it('link to both pages with what each is doing now', async () => {
    answer({
      '/pregen': pregen({ state: 'running', percent: 42.7, etaSeconds: 5400 }),
      '/resourcepack': { offer, pending: false } satisfies ResourcePack,
      '/datapacks': dataPacks(),
    })
    const text = await render(<WorldTools server={server()} phone={false} />)
    expect(text).toContain('Pre-generate the mapPre-generating · 42% · about 1.5 h left')
    expect(text).toContain('Resource and data packsFaithful 32x · 3 of 4 data packs on')
    expect([...document.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['/servers/survival/world/pregen', '/servers/survival/world/packs'])
  })
})
