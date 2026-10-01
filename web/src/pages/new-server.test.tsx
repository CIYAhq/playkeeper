// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import * as templates from '@/api/templates'
import type { Action, Catalog, MachineView, Me, ModpackCard, ModpackDetail, ModpackPreview, Operation, TemplatePlan, WorldImport, WorldImportPreview } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import type { ModpackChoice } from '@/components/app/modpacks'
import type { TemplateChoice } from '@/components/app/templates'
import * as upload from '@/lib/upload'
import { catalogFor, NewServerPage, type StartFrom } from './new-server'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
}))

vi.mock('@/api/templates', async (importOriginal) => ({
  ...(await importOriginal<typeof templates>()),
  planTemplate: vi.fn(() => new Promise(() => {})),
}))

vi.mock('@/lib/upload', async (importOriginal) => ({
  ...(await importOriginal<typeof upload>()),
  uploadWorld: vi.fn(),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover', 'machines.view']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView

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

const entry = (id: string, v: string, recommended: boolean) => ({ id, label: v, minecraftVersion: v, paperBuild: 1, jarSha256: '', java: 25, recommended, notes: '', channel: 'STABLE', experimental: false, supported: true })

const catalog: Catalog = {
  type: 'paper',
  types: [{ id: 'paper', name: 'Paper', available: true }],
  versions: [entry('paper-26.1.2', '26.1.2', true)],
  memoryOptionsMB: [2048, 4096, 6144],
  recommendedMemoryMB: 4096,
  hostMemoryMB: 16384,
  maxMemoryMB: 6144,
  systemReserveMB: 1536,
  memoryFreeMB: 10752,
  servers: [],
  suggestedPort: 25565,
  image: 'itzg/minecraft-server',
}

const base = '/api/machines/m2345abcde/world-imports'
const file = { index: 0, name: 'Survival-2024.zip', size: 100, received: 100 }
const uploaded: WorldImport = { id: 'imp2345abc', createdAt: '2026-09-25T10:00:00Z', files: [file], limitBytes: 2 ** 34 }
const world = { id: 'w1', archive: 'Survival-2024.zip', path: 'Survival 2024', origin: 'singleplayer' as const, level: { name: 'Survival 2024', version: '1.21.4', hardcore: false, spawn: { x: 40, z: 12 } }, dimensions: ['minecraft:overworld'], players: 0, sizeBytes: 100, files: 10, default: true }
const inspected: WorldImport = { ...uploaded, inspection: { archives: [], worlds: [world] } }

function preview(versionId: string, target: string): WorldImportPreview {
  return {
    id: uploaded.id,
    preview: {
      world,
      target: { type: 'paper', minecraftVersion: target, levelName: 'world' },
      version: { compat: target === '1.21.4' ? 'same' : 'upgrade', world: '1.21.4', target },
      folders: ['world'],
      fileCount: 10,
      sizeBytes: 1.2 * 2 ** 30,
      dimensions: [{ id: 'minecraft:overworld', folder: 'world', files: 10, bytes: 100 }],
      players: 0,
    },
    versions: [
      { ...entry('paper-26.1.2', '26.1.2', true), keep: false },
      { ...entry('paper-1.21.4', '1.21.4', false), keep: true },
    ],
    versionId,
    keepsOriginal: versionId === 'paper-26.1.2',
    memoryMB: 4096,
  }
}

let root: Root | undefined
let finish: ((imp: WorldImport) => void) | undefined

function text(): string {
  return document.body.textContent ?? ''
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((x) => x.textContent?.trim() === label || x.getAttribute('aria-label') === label)
  if (!b) throw new Error(`no button "${label}" in: ${text()}`)
  return b
}

/** Lets chained requests and the renders after them finish. */
const settle = () => new Promise((r) => setTimeout(r, 0))

async function click(el: Element) {
  await act(async () => {
    ;(el as HTMLElement).click()
    await settle()
  })
}

async function chooseFile(name: string) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')
  if (!input) throw new Error('no file input')
  Object.defineProperty(input, 'files', { value: [new File(['x'], name)], configurable: true })
  await act(async () => {
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
  })
}

async function render() {
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={workspace}>{<NewServerPage />}</WorkspaceContext.Provider>))
  await act(settle)
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

beforeEach(() => {
  window.history.replaceState(null, '', '/servers/new#world')
  vi.mocked(client.get).mockImplementation(((path: string) => (path.includes('/catalog') ? Promise.resolve(catalog) : new Promise(() => {}))) as typeof client.get)
  vi.mocked(upload.uploadWorld).mockImplementation((o) => {
    o.onImport?.(uploaded)
    o.onProgress?.({ sent: 62, total: 100, retrying: false })
    return new Promise((resolve) => {
      finish = resolve
    })
  })
  vi.mocked(client.post).mockImplementation(((path: string, body?: { versionId?: string }) => {
    if (path.endsWith('/inspect')) return Promise.resolve(inspected)
    if (path.endsWith('/preview')) return Promise.resolve(body?.versionId === 'paper-1.21.4' ? preview('paper-1.21.4', '1.21.4') : preview('paper-26.1.2', '26.1.2'))
    if (path.endsWith('/create')) return Promise.resolve({ id: 'op1', kind: 'create', status: 'running', serverId: 's2345abcde' } as Operation)
    return Promise.resolve({})
  }) as typeof client.post)
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  finish = undefined
  document.body.innerHTML = ''
  vi.mocked(client.post).mockReset()
  vi.mocked(client.del).mockClear()
})

describe('New server from a world', () => {
  it('keeps Check the world off until the upload is done', async () => {
    await render()
    expect(text()).toContain('Where is the world now?')
    expect(text()).toContain('Get your singleplayer world')
    expect(text()).toContain('Not uploaded yet')
    expect(text()).toContain('Paper, suggested')
    expect(button('Check the world').disabled).toBe(true)
    expect(button('Check the world').title).toBe('Upload the world first.')

    await click(button('Aternos'))
    expect(text()).toContain('Get your world from Aternos')

    await chooseFile('Survival-2024.zip')
    expect(upload.uploadWorld).toHaveBeenCalledWith(expect.objectContaining({ base }))
    expect(text()).toContain('Uploading · 62%')
    expect(text()).toContain('It resumes if the connection drops.')
    expect(button('Check the world').disabled).toBe(true)
    expect(button('Check the world').title).toBe('Wait for the upload to finish.')

    await act(async () => finish?.(uploaded))
    expect(text()).toContain('Survival-2024.zip')
    expect(button('Check the world').disabled).toBe(false)
    expect(button('Check the world').title).toBe('')
  })

  it('says why a world the check found a problem with can’t go on', async () => {
    const problem = { kind: 'bedrock_world', text: 'This is a Bedrock world, and Playkeeper runs Java servers.', hint: 'Upload a world from Minecraft: Java Edition.' }
    const post = vi.mocked(client.post).getMockImplementation()
    vi.mocked(client.post).mockImplementation(((path: string, body?: unknown) => {
      if (path.endsWith('/preview')) {
        const p = preview('paper-26.1.2', '26.1.2')
        return Promise.resolve({ ...p, preview: { ...p.preview, problems: [problem] } })
      }
      return post?.(path, body)
    }) as typeof client.post)
    await render()
    await chooseFile('Survival-2024.zip')
    await act(async () => finish?.(uploaded))
    await click(button('Check the world'))

    expect(text()).toContain(problem.text)
    expect(button('Continue to memory').disabled).toBe(true)
    expect(button('Continue to memory').title).toBe('The check found a problem with this world.')
  })

  it('refuses files that aren’t archives before uploading', async () => {
    await render()
    await chooseFile('level.dat')
    expect(text()).toContain('Choose a .zip, .tar.gz or .tar file.')
    expect(upload.uploadWorld).not.toHaveBeenCalled()
  })

  it('checks the world, skips play style and creates the server from the upload', async () => {
    await render()
    await chooseFile('Survival-2024.zip')
    await act(async () => finish?.(uploaded))
    await click(button('Check the world'))

    expect(client.post).toHaveBeenCalledWith(`${base}/${uploaded.id}/inspect`)
    expect(client.post).toHaveBeenCalledWith(`${base}/${uploaded.id}/preview`, { options: { world: 'w1' } })
    expect(text()).toContain('Here’s what’s inside Survival-2024.zip')
    expect(text()).toContain('1.2 GB · spawn at x 40, z 12 · seed and game rules kept')
    expect(text()).toContain('No plugins inside')
    expect(text()).toContain('1.21.4 → 26.1.2')

    const keep = [...document.querySelectorAll('label')].find((l) => l.textContent?.includes('Keep 1.21.4'))
    const radio = keep?.querySelector('[role="radio"]')
    if (!radio) throw new Error('no Keep 1.21.4 choice')
    await click(radio)
    expect(client.post).toHaveBeenCalledWith(`${base}/${uploaded.id}/preview`, { options: { world: 'w1' }, versionId: 'paper-1.21.4' })

    await click(button('Continue to memory'))
    expect(text()).toContain('For this world, 4 GB is a good fit.')
    await click(button('Continue to name'))
    const name = document.querySelector<HTMLInputElement>('input[maxlength="32"]')
    expect(name?.value).toBe('Survival 2024')
    expect(document.querySelector('input[maxlength="59"]')).toBeNull()

    const eula = document.querySelector('input[type="checkbox"]')
    if (!eula) throw new Error('no EULA checkbox')
    await click(eula)
    expect(document.querySelector('[role="checkbox"]')?.getAttribute('aria-checked')).toBe('true')
    await click(button('Create and start Survival 2024'))
    expect(client.post).toHaveBeenCalledWith(`${base}/${uploaded.id}/create`, {
      options: { world: 'w1', keepAddons: false, keepPlayerLists: false, keepOperators: false },
      versionId: 'paper-1.21.4',
      name: 'Survival 2024',
      memoryMB: 4096,
      acceptEula: true,
    })

    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await act(async () => void window.dispatchEvent(new Event('pagehide')))
    await act(async () => root?.unmount())
    root = undefined
    expect(client.del).not.toHaveBeenCalled()
    expect(fetch).not.toHaveBeenCalled()
    fetch.mockRestore()
  })

  it('deletes the upload when it’s cancelled', async () => {
    await render()
    await chooseFile('Survival-2024.zip')
    await click(button('Cancel'))
    expect(client.del).toHaveBeenCalledWith(`${base}/${uploaded.id}`)
    expect(text()).toContain('Drop the world .zip here')
  })

  // A reload or a closed tab doesn't unmount the page, and a request the page
  // makes as it goes must outlive it.
  it.each([
    { name: 'while it uploads', finished: false },
    { name: 'once it’s uploaded', finished: true },
  ])('deletes the upload when the page goes away $name', async ({ finished }) => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await render()
    await chooseFile('Survival-2024.zip')
    if (finished) await act(async () => finish?.(uploaded))
    await act(async () => void window.dispatchEvent(new Event('pagehide')))
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith(`${base}/${uploaded.id}`, expect.objectContaining({ method: 'DELETE', keepalive: true }))
    expect(text()).toContain('Drop the world .zip here')
    await act(async () => root?.unmount())
    root = undefined
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(client.del).not.toHaveBeenCalled()
    fetch.mockRestore()
  })

  it('carries on after the disk filled up without announcing the file again', async () => {
    const actual = await vi.importActual<typeof upload>('@/lib/upload')
    const files: { name: string; size: number; received: number }[] = []
    const view = (): WorldImport => ({ ...uploaded, files: files.map((f, index) => ({ index, ...f })) })
    let full = true
    const put: upload.Put = async (url, body) => {
      if (full) return { status: 507, text: JSON.stringify({ error: 'The disk filled up during the upload.', code: 'insufficient_space' }) }
      const f = files[Number(/\/files\/(\d+)\?/.exec(url)?.[1])]
      if (f) f.received += body.size
      return { status: 200, text: JSON.stringify(view()) }
    }
    vi.mocked(upload.uploadWorld).mockImplementation((o) => actual.uploadWorld({ ...o, put, wait: async () => {} }))
    const get = vi.mocked(client.get).getMockImplementation()
    vi.mocked(client.get).mockImplementation(((path: string) => (path === `${base}/${uploaded.id}` ? Promise.resolve(view()) : get?.(path))) as typeof client.get)
    const post = vi.mocked(client.post).getMockImplementation()
    vi.mocked(client.post).mockImplementation(((path: string, body?: { name: string; size: number }) => {
      if (path === base) return Promise.resolve(view())
      if (path === `${base}/${uploaded.id}/files` && body) {
        files.push({ name: body.name, size: body.size, received: 0 })
        return Promise.resolve(view())
      }
      return post?.(path, body)
    }) as typeof client.post)

    await render()
    await chooseFile('Survival-2024.zip')
    expect(text()).toContain('The disk filled up during the upload.')
    full = false
    await click(button('Try again'))
    expect(vi.mocked(upload.uploadWorld).mock.calls[1]?.[0].resume?.files).toEqual([{ index: 0, name: 'Survival-2024.zip', size: 1, received: 0 }])
    expect(files).toEqual([{ name: 'Survival-2024.zip', size: 1, received: 1 }])
    expect(text()).toContain('1 B · uploaded')
  })
})

describe('New server for a creator', () => {
  const creator = (home: string): Me => ({ ...me, user: { username: 'alex', role: 'member' }, access: { ...me.access, servers: {}, twoFactor: true, home, can: ['view', 'account.manage', 'servers.run', 'servers.manage', 'servers.create_own'] } })
  const remote = { id: 'r2345abcde', projectId: 'p2345abcde', name: 'home-server', kind: 'remote', link: { machineId: 'r2345abcde', name: 'home-server', fingerprint: '', state: 'connected', problems: [] } } as MachineView
  const catalogs = () => vi.mocked(client.get).mock.calls.map(([p]) => String(p)).filter((p) => p.includes('/catalog'))

  async function renderAs(me: Me, asked?: string) {
    window.history.replaceState(null, '', '/servers/new')
    vi.mocked(client.get).mockClear()
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<WorkspaceContext.Provider value={{ ...workspace, me, machines: [machine, remote] }}>{<NewServerPage machine={asked} />}</WorkspaceContext.Provider>))
    await act(settle)
  }

  it('starts from a server type, a modpack, a template, a world or a backup, only on the machine their servers go on', async () => {
    await renderAs(creator(machine.id), remote.id)
    expect(text()).toContain('A server type')
    expect(text()).toContain('A world')
    expect(text()).toContain('Restore it as a new server')
    expect(text()).not.toContain('New server on')
    expect(text()).not.toContain('home-server')
    expect(catalogs().every((p) => p.startsWith(`/api/machines/${machine.id}/`))).toBe(true)
  })

  it('makes a customer’s server on the joined machine they were placed on, never naming it', async () => {
    await renderAs(creator(remote.id), machine.id)
    expect(text()).not.toContain('home-server')
    expect(text()).not.toContain('New server on')
    expect(catalogs().length).toBeGreaterThan(0)
    expect(catalogs().every((p) => p.startsWith(`/api/machines/${remote.id}/`))).toBe(true)
  })
})

describe('New server on a joined machine', () => {
  const remote = { id: 'r2345abcde', projectId: 'p2345abcde', name: 'home-server', kind: 'remote' } as MachineView
  const on = (state: 'connected' | 'offline'): Workspace => ({ ...workspace, machines: [machine, { ...remote, link: { machineId: remote.id, name: remote.name, fingerprint: '', state, problems: [] } }] })
  const unreachable = new client.ApiError(503, { error: 'home-server is not connected.', code: 'machine_not_connected' })

  /** Renders the page for home-server, or renders it again with the workspace changed. */
  async function renderOn(ws: Workspace) {
    const r = root ?? createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{<NewServerPage key={remote.id} machine={remote.id} />}</WorkspaceContext.Provider>))
    await act(settle)
  }

  function continueButton(): HTMLButtonElement {
    const b = [...document.querySelectorAll('button')].find((x) => /^(Continue to|Create and start)/.test(x.textContent?.trim() ?? ''))
    if (!b) throw new Error(`no Continue button in: ${text()}`)
    return b
  }

  async function next(times = 1) {
    for (let i = 0; i < times; i++) await click(continueButton())
  }

  async function acceptEula() {
    const eula = document.querySelector('input[type="checkbox"]')
    if (!eula) throw new Error('no EULA checkbox')
    await click(eula)
  }

  const posted = () => vi.mocked(client.post).mock.calls.map(([path]) => path)
  const asked = () => vi.mocked(client.get).mock.calls.map(([path]) => path)

  beforeEach(() => {
    window.history.replaceState(null, '', `/servers/new?machine=${remote.id}`)
    vi.mocked(client.get).mockClear()
    vi.mocked(client.post).mockImplementation((() => Promise.resolve({ id: 'op1', kind: 'create', status: 'running', serverId: 's2345abcde' } as Operation)) as typeof client.post)
  })

  // The walkthrough of 1 Oct 2026: the owner's own name goes on every new
  // server's allowlist, but an agent before 0.4.16 refuses the field.
  it('puts your own Minecraft name on the new server, where its agent takes it', async () => {
    const created = async (agentVersion: string) => {
      vi.mocked(client.post).mockClear()
      const ws = on('connected')
      await renderOn({ ...ws, prefs: { 'minecraft.name': 'Steve_Builds' }, me: { ...ws.me, version: '0.4.16' }, machines: ws.machines.map((m) => (m.id === remote.id ? { ...m, live: { ...m.live, agentVersion } as MachineView['live'] } : m)) })
      await next(4)
      await acceptEula()
      await next()
      return vi.mocked(client.post).mock.calls.find(([path]) => path === '/api/machines/r2345abcde/servers')?.[1] as { players?: string[] } | undefined
    }
    expect((await created('0.4.16'))?.players).toEqual(['Steve_Builds'])
    act(() => root?.unmount())
    root = undefined
    document.body.innerHTML = ''
    const old = await created('0.4.15')
    expect(old).toBeDefined()
    expect(old?.players).toBeUndefined()
  })

  for (const tc of [
    {
      name: 'makes the server there while it’s connected, and keeps the machine once the flow starts',
      steps: async () => {
        await renderOn(on('connected'))
        expect(text()).toContain('New server on')
        await next()
        expect(text()).not.toContain('New server on')
        await next(3)
        await acceptEula()
        await next()
      },
      posts: ['/api/machines/r2345abcde/servers'],
    },
    {
      name: 'says it’s away and holds Create back when it’s away as the page opens',
      steps: async () => {
        vi.mocked(client.get).mockImplementation(((path: string) => (path.includes('/catalog') ? (path.includes(remote.id) ? Promise.reject(unreachable) : Promise.resolve(catalog)) : new Promise(() => {}))) as typeof client.get)
        await renderOn(on('offline'))
        expect(text()).toContain('Can’t reach home-server Create waits until it’s back.')
        expect(text()).not.toContain('Couldn’t load the versions')
        expect(continueButton().disabled).toBe(true)
        expect(continueButton().title).toBe('Can’t reach home-server')
        await click(continueButton())
      },
      posts: [],
    },
    {
      name: 'keeps the machine when it goes away after step 2, and goes on once it’s back',
      steps: async () => {
        await renderOn(on('connected'))
        await next(2)
        await renderOn(on('offline'))
        expect(text()).toContain('Can’t reach home-server')
        expect(continueButton().title).toBe('Can’t reach home-server')
        await click(continueButton())
        expect(continueButton().textContent).toContain('Continue to memory')
        await renderOn(on('connected'))
        expect(text()).not.toContain('Can’t reach home-server')
        await next(2)
        await acceptEula()
        await next()
      },
      posts: ['/api/machines/r2345abcde/servers'],
    },
  ]) {
    it(tc.name, async () => {
      await tc.steps()
      expect(posted()).toEqual(tc.posts)
      expect(asked().filter((p) => p.includes(machine.id))).toEqual([])
    })
  }
})

describe('What New server sizes memory for', () => {
  const types = [
    { id: 'paper', name: 'Paper', available: true },
    { id: 'fabric', name: 'Fabric', available: true },
    { id: 'neoforge', name: 'NeoForge', available: true },
    { id: 'forge', name: 'Forge', available: false },
  ]
  const plan = (type: string, addons: number, modpack = false): TemplatePlan => ({
    contents: {
      name: 'Fast SMP',
      type,
      minecraftVersion: '1.21.1',
      settings: {},
      addons: Array.from({ length: addons }, (_, i) => ({ source: 'modrinth', name: `Add-on ${i}` })),
      ...(modpack ? { modpack: { source: 'modrinth', name: 'Adrenaserver' } } : {}),
      resourcePacks: 0,
      dataPacks: 0,
      packs: [],
    },
    type,
    minecraftVersion: '1.21.1',
    memoryMB: 0,
    skipped: [],
    warnings: [],
    blockers: [],
    ready: true,
    fingerprint: 'f0f0f0f0',
  })
  const tpl = (p: TemplatePlan): TemplateChoice => ({ plan: p, fileName: '', text: 'template' })
  const pack = (type: string, mods?: number): ModpackChoice => ({ source: 'modrinth', projectId: 'H9OFWiay', versionId: '', name: 'Pack', type, minecraftVersion: '1.21.1', memoryMB: 0, mods })

  it.each<{ name: string; from: StartFrom; pack?: ModpackChoice; tpl?: TemplateChoice; want: object }>([
    { name: 'a Paper template with 3 plugins', from: 'template', tpl: tpl(plan('paper', 3)), want: { type: 'paper', plugins: 3 } },
    { name: 'a Paper template with 30 plugins', from: 'template', tpl: tpl(plan('paper', 30)), want: { type: 'paper', plugins: 30 } },
    { name: 'a Fabric template with 5 mods', from: 'template', tpl: tpl(plan('fabric', 5)), want: { type: 'fabric', mods: 5 } },
    { name: 'a Fabric template with 60 mods', from: 'template', tpl: tpl(plan('fabric', 60)), want: { type: 'fabric', mods: 60 } },
    { name: 'a modpack template, as at least a few mods', from: 'template', tpl: tpl(plan('fabric', 0, true)), want: { type: 'fabric', mods: 1 } },
    { name: 'a modpack template with 60 mods on top', from: 'template', tpl: tpl(plan('fabric', 60, true)), want: { type: 'fabric', mods: 60 } },
    { name: 'a template of a type this machine can’t create', from: 'template', tpl: tpl(plan('forge', 5)), want: { type: 'paper' } },
    { name: 'a pack that says its mods', from: 'modpack', pack: pack('neoforge', 180), want: { type: 'neoforge', mods: 180 } },
    { name: 'a pack that doesn’t say its mods, by its type', from: 'modpack', pack: pack('fabric'), want: { type: 'fabric' } },
    { name: 'a pack of a type this machine can’t create', from: 'modpack', pack: pack('forge', 40), want: { type: 'paper', mods: 40 } },
    { name: 'a type, when a pack was chosen before', from: 'type', pack: pack('fabric', 40), want: { type: 'paper' } },
  ])('asks the catalog for $name', ({ from, pack, tpl, want }) => {
    expect(catalogFor(from, 'paper', pack, tpl, types)).toEqual(want)
  })

  // All the Mods 10 as its plan reads it: 460 mods and 8 GB of heap in its manifest, which takes 12 GB.
  const atm10: ModpackPreview = {
    type: 'neoforge', minecraftVersion: '1.21.1', loaderVersion: '21.1.251', files: 3904, downloadSize: 1_361_589_811, ready: true,
    blockers: [], warnings: [], manual: [], mods: 460, memoryMB: 12 << 10, heapMB: 8196,
  }
  const atm10Card: ModpackCard = {
    source: 'modrinth', projectId: 'ATM10AAA', slug: 'atm10', name: 'All the Mods 10', summary: '', downloads: 1, updated: '2026-09-22T00:00:00Z',
    pageUrl: 'https://modrinth.com/modpack/atm10', types: ['neoforge'], minecraftVersions: ['1.21.1'],
  }
  /** Picks All the Mods 10 in New server, on a machine with these memory options; the plan answers when plan resolves. */
  async function pickPack(options: number[], plan: Promise<ModpackPreview>) {
    window.history.replaceState(null, '', '/servers/new')
    const neoforge: Catalog = { ...catalog, types, memoryOptionsMB: options, maxMemoryMB: options[options.length - 1] ?? 0 }
    const detail: ModpackDetail = { ...atm10Card, versions: [{ id: 'ATMV0001', number: '8.2', channel: 'release', published: '2026-09-22T00:00:00Z', size: 1, type: 'neoforge', minecraftVersion: '1.21.1' }], newest: 'ATMV0001' }
    vi.mocked(client.get).mockImplementation(((path: string) => {
      if (path.includes('/catalog')) return Promise.resolve({ ...neoforge, type: new URLSearchParams(path.split('?')[1]).get('type') ?? 'paper' })
      if (path.includes('/preview')) return plan
      if (path.includes('/modpacks?')) return Promise.resolve({ cards: [atm10Card], total: 1, offset: 0, limit: 12, sources: ['modrinth'] })
      if (path.includes('/modpacks/modrinth/ATM10AAA')) return Promise.resolve(detail)
      return new Promise(() => {})
    }) as typeof client.get)
    await render()
    await click(button('A modpack'))
    await click(button('Pick All the Mods 10'))
  }
  /** Picks All the Mods 10 and goes on to memory. */
  async function packMemoryStep(options: number[], plan: Promise<ModpackPreview>) {
    await pickPack(options, plan)
    await click(button('Continue with this modpack'))
    await act(settle)
  }
  const slider = () => document.querySelector('input[type="range"][aria-label="Memory for this server"]')

  it('suggests the memory a pack needs, from its mods and its own Java heap', async () => {
    await packMemoryStep([2048, 4096, 6144, 8192, 12288], Promise.resolve(atm10))
    expect(text()).toContain('All the Mods 10 needs about 12 GB.')
    expect(slider()?.getAttribute('aria-valuetext')).toBe('12 GB')
    expect(text()).not.toContain('Not enough memory')
    expect(client.get).toHaveBeenCalledWith(`/api/machines/${machine.id}/catalog?type=neoforge&mods=460`)
  })

  it('says so when the machine can’t give a pack the memory it needs', async () => {
    await packMemoryStep([2048, 4096, 6144, 8192], Promise.resolve(atm10))
    expect(text()).toContain('Not enough memory on my-vps')
    expect(text()).toContain('All the Mods 10 needs about 12 GB, and 8 GB is the most it can get here, so it may run out of memory.')
    expect(slider()?.getAttribute('aria-valuetext')).toBe('8 GB')
    expect(text()).not.toContain('Recommended')
  })

  it.each([
    { name: 'follows a plan that answers late', pick: false, want: '12 GB' },
    { name: 'keeps a memory picked before the plan', pick: true, want: '6 GB' },
  ])('$name', async ({ pick, want }) => {
    let answer: (p: ModpackPreview) => void = () => {}
    await packMemoryStep([2048, 4096, 6144, 8192, 12288], new Promise((r) => (answer = r)))
    expect(slider()?.getAttribute('aria-valuetext')).toBe('4 GB')
    if (pick) {
      await act(async () => {
        slider()?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
        await settle()
      })
      expect(slider()?.getAttribute('aria-valuetext')).toBe('6 GB')
    }
    await act(async () => {
      answer(atm10)
      await settle()
    })
    expect(slider()?.getAttribute('aria-valuetext')).toBe(want)
  })

  it('holds Next on memory until the pack’s plan answers, then creates with the memory it needs', async () => {
    let answer: (p: ModpackPreview) => void = () => {}
    await packMemoryStep([2048, 4096, 6144, 8192, 12288], new Promise((r) => (answer = r)))
    expect(button('Continue to name').disabled).toBe(true)
    expect(button('Continue to name').title).toBe('Checking the pack…')
    await click(button('Continue to name'))
    await act(async () => {
      answer(atm10)
      await settle()
    })
    expect(slider()?.getAttribute('aria-valuetext')).toBe('12 GB')
    await click(button('Continue to name'))
    const eula = document.querySelector('input[type="checkbox"]')
    if (!eula) throw new Error('no EULA checkbox')
    await click(eula)
    await click(button('Create and start All the Mods 10'))
    expect(client.post).toHaveBeenCalledWith(`/api/machines/${machine.id}/servers`, expect.objectContaining({ memoryMB: 12 << 10 }))
  })

  it('sizes a server type as usual after a pack was only picked', async () => {
    await pickPack([2048, 4096, 6144, 8192, 12288], Promise.resolve(atm10))
    await act(settle)
    await act(settle)
    await click(button('A server type'))
    await click(button('Continue to version'))
    await click(button('Continue to play style'))
    await click(button('Continue to memory'))
    expect(slider()?.getAttribute('aria-valuetext')).toBe('4 GB')
  })

  it('sizes a shared template by its type and mods', async () => {
    window.history.replaceState(null, '', '/servers/new#template=shared')
    const fabric: Catalog = { ...catalog, types }
    vi.mocked(client.get).mockImplementation(((path: string) => (path.includes('/catalog') ? Promise.resolve({ ...fabric, type: new URLSearchParams(path.split('?')[1]).get('type') ?? 'paper' }) : new Promise(() => {}))) as typeof client.get)
    vi.mocked(templates.planTemplate).mockResolvedValue(plan('fabric', 60))
    await render()
    await act(settle)
    expect(templates.planTemplate).toHaveBeenCalledWith(machine.id, 'shared')
    expect(client.get).toHaveBeenCalledWith(`/api/machines/${machine.id}/catalog?type=fabric&mods=60`)
  })
})
