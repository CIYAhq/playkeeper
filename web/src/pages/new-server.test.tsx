// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, Catalog, MachineView, Me, Operation, WorldImport, WorldImportPreview } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import * as upload from '@/lib/upload'
import { NewServerPage } from './new-server'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve(undefined)),
}))

vi.mock('@/lib/upload', async (importOriginal) => ({
  ...(await importOriginal<typeof upload>()),
  uploadWorld: vi.fn(),
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
